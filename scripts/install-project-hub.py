#!/usr/bin/env python3
"""Install the built Nix package without changing the system or main profile."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def source_launcher(root):
    spec = importlib.util.spec_from_file_location("project_hub_source", root / "scripts/project-hub-source.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def owned_install(receipt, launcher, desktop):
    record = json.loads(receipt.read_text())
    if not launcher.is_symlink() or os.readlink(launcher) != record["launcher_target"]:
        raise SystemExit(f"Refusing to overwrite a changed launcher: {launcher}")
    if not desktop.is_file() or hashlib.sha256(desktop.read_bytes()).hexdigest() != record["desktop_sha256"]:
        raise SystemExit(f"Refusing to overwrite a changed desktop entry: {desktop}")
    return record


def install_source(root, state, receipt, launcher, desktop):
    previous = None
    if receipt.exists():
        previous = owned_install(receipt, launcher, desktop)
    elif any(path.exists() or path.is_symlink() for path in (launcher, desktop)):
        raise SystemExit("Refusing to overwrite an existing launcher or desktop entry without an install receipt")
    source = source_launcher(root)
    package = source.build(root, state)
    target = str(root / "scripts/project-hub-source.py")
    body = (root / "nix/project-hub.desktop").read_text().replace("Exec=project-hub", f"Exec={launcher}")
    launcher.parent.mkdir(parents=True, exist_ok=True)
    desktop.parent.mkdir(parents=True, exist_ok=True)
    if previous and previous.get("mode") != "source":
        # Preserve the original receipt and GC root when migrating a pinned
        # installation. No registry, logs, sessions or system profile changes.
        backup = state / "install.before-source.json"
        if not backup.exists():
            source.atomic_json(backup, previous)
    handle, temp_name = tempfile.mkstemp(prefix=".project-hub-", dir=launcher.parent)
    os.close(handle)
    os.unlink(temp_name)
    try:
        os.symlink(target, temp_name)
        os.replace(temp_name, launcher)
    finally:
        if os.path.lexists(temp_name):
            os.unlink(temp_name)
    handle, temp_name = tempfile.mkstemp(prefix=".project-hub-", dir=desktop.parent)
    try:
        with os.fdopen(handle, "w") as stream:
            stream.write(body)
        os.chmod(temp_name, 0o644)
        os.replace(temp_name, desktop)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)
    source.atomic_json(receipt, {
        "mode": "source", "checkout": str(root), "package": package,
        "package_link": str(state / "source-package"), "launcher_target": target,
        "desktop_sha256": hashlib.sha256(body.encode()).hexdigest(),
    })
    print(f"Source launcher installed: {launcher}\nCheckout: {root}\nEvery launch builds the current sources; unchanged builds use the Nix cache.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--uninstall", action="store_true")
    mode.add_argument("--source", action="store_true", help="Follow this checkout and build current sources on every launch")
    args = parser.parse_args()
    home = Path.home()
    state = Path(os.environ.get("XDG_STATE_HOME", home / ".local/state")) / "project-hub"
    receipt = state / "install.json"
    launcher = home / ".local/bin/project-hub"
    desktop = Path(os.environ.get("XDG_DATA_HOME", home / ".local/share")) / "applications/project-hub.desktop"
    gcroot = state / "package"
    root = Path(__file__).resolve().parents[1]
    if args.source:
        install_source(root, state, receipt, launcher, desktop)
        return
    if args.uninstall:
        record = json.loads(receipt.read_text())
        if launcher.is_symlink() and os.readlink(launcher) == record["launcher_target"]:
            launcher.unlink()
        if desktop.exists() and hashlib.sha256(desktop.read_bytes()).hexdigest() == record["desktop_sha256"]:
            desktop.unlink()
        gcroot = Path(record.get("package_link", gcroot))
        if gcroot.is_symlink() and os.readlink(gcroot) == record["package"]:
            gcroot.unlink()
        receipt.unlink()
        print("Launcher removed. Project registry, session logs and skills backups preserved.")
        return
    if receipt.exists():
        print("Already installed; run project-hub. Use --source to follow this checkout automatically.")
        return
    for path in (launcher, desktop, gcroot):
        if path.exists() or path.is_symlink():
            raise SystemExit(f"Refusing to overwrite existing path: {path}")
    package = subprocess.check_output(
        ["nix", "build", f"path:{root}#project-hub", "--print-out-paths", "--no-link"], text=True
    ).strip()
    if not package.startswith("/nix/store/") or "\n" in package:
        raise SystemExit("Unexpected Nix package output")
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    subprocess.run(["nix-store", "--add-root", str(gcroot), "--indirect", "--realise", package], check=True)
    launcher.parent.mkdir(parents=True, exist_ok=True)
    desktop.parent.mkdir(parents=True, exist_ok=True)
    target = str(gcroot / "bin/project-hub")
    os.symlink(target, launcher)
    # The existing desktop launcher selects its configured terminal for a
    # standard Terminal=true entry. No Noctalia, Kitty or Niri config changes.
    body = (Path(package) / "share/applications/project-hub.desktop").read_text()
    body = body.replace(f"Exec={package}/bin/project-hub", f"Exec={launcher}")
    with desktop.open("x") as out:
        out.write(body)
    record = {"package": package, "launcher_target": target,
              "desktop_sha256": hashlib.sha256(body.encode()).hexdigest()}
    with receipt.open("x") as out:
        os.chmod(receipt, 0o600)
        json.dump(record, out, indent=2)
    print(f"Installed: {launcher}\nDesktop entry: {desktop}\nRollback: {__file__} --uninstall")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        print(f"Project Hub: Nix command failed (exit {error.returncode}); see output above.", file=sys.stderr)
        sys.exit(1)
