#!/usr/bin/env python3
"""Install dopamine's terminal command, preserving source builds on launch."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import subprocess


ROOT = Path(__file__).resolve().parents[1]


def source_module():
    spec = importlib.util.spec_from_file_location("dopamine_source", ROOT / "scripts/project-hub-source.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def atomic_link(target, destination):
    handle, name = tempfile.mkstemp(prefix=".dopamine-", dir=destination.parent)
    os.close(handle)
    os.unlink(name)
    try:
        os.symlink(target, name)
        os.replace(name, destination)
    finally:
        if os.path.lexists(name):
            os.unlink(name)


def verified(record, launcher, desktop):
    if not launcher.is_symlink() or os.readlink(launcher) != record["launcher_target"]:
        raise SystemExit(f"Refusing to overwrite a changed launcher: {launcher}")
    if desktop.exists() and record.get("desktop_sha256") and hashlib.sha256(desktop.read_bytes()).hexdigest() != record["desktop_sha256"]:
        raise SystemExit(f"Refusing to overwrite a changed desktop entry: {desktop}")


def remove_owned_desktop(record, desktop):
    if record and record.get("desktop_sha256") and desktop.is_file():
        if hashlib.sha256(desktop.read_bytes()).hexdigest() == record["desktop_sha256"]:
            desktop.unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--uninstall", action="store_true", help="Remove owned launchers; preserve all project/session data")
    args = parser.parse_args()
    home = Path.home()
    state_root = Path(os.environ.get("XDG_STATE_HOME", home / ".local/state"))
    data_root = Path(os.environ.get("XDG_DATA_HOME", home / ".local/share"))
    state = state_root / "dopamine"
    receipt = state / "install.json"
    launcher = home / ".local/bin/dopamine"
    desktop = data_root / "applications/dopamine.desktop"
    record = json.loads(receipt.read_text()) if receipt.exists() else None
    if record:
        verified(record, launcher, desktop)
    elif launcher.exists() or launcher.is_symlink():
        raise SystemExit("Refusing to overwrite an existing dopamine installation without its receipt")
    if args.uninstall:
        if record:
            launcher.unlink()
            remove_owned_desktop(record, desktop)
            receipt.unlink()
        print("dopamine launchers removed. Projects, logs, workspaces and Nix rollback roots preserved.")
        return

    # Retire only a verified old launcher from this checkout. Config, runs,
    # logs, rollback roots and the old receipt remain in their original paths.
    old_launcher = home / ".local/bin/project-hub"
    old_desktop = data_root / "applications/project-hub.desktop"
    old_receipt = state_root / "project-hub/install.json"
    retire = False
    if old_receipt.exists() and old_launcher.is_symlink() and old_desktop.is_file():
        old = json.loads(old_receipt.read_text())
        # A moved checkout may leave a stale receipt path. The actual launcher
        # must still resolve to this exact checkout, and the desktop must match.
        actual = old_launcher.resolve()
        expected = ROOT / "scripts/project-hub-source.py"
        retire = actual == expected and hashlib.sha256(old_desktop.read_bytes()).hexdigest() == old.get("desktop_sha256")

    source = source_module()
    # Reuse the old build root so existing rollback/GC behavior is preserved.
    package = source.build(ROOT, state_root / "project-hub")
    launcher.parent.mkdir(parents=True, exist_ok=True)
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    target = str(ROOT / "scripts/dopamine-source.py")
    atomic_link(target, launcher)
    remove_owned_desktop(record, desktop)
    source.atomic_json(receipt, {"mode": "source", "checkout": str(ROOT), "package": package,
                                "launcher_target": target})
    if retire:
        old_launcher.unlink()
        old_desktop.unlink()
    print(f"Installed: {launcher}\nTerminal command only; no app launcher entry.\nEvery launch builds current sources with the pinned Nix flake.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print(f"dopamine: installation failed: {error}", file=sys.stderr)
        sys.exit(1)
