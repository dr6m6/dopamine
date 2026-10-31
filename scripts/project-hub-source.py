#!/usr/bin/env python3
"""Build this checkout with its pinned Nix flake before opening dopamine.

The historical filename is retained for already installed source launchers.
"""
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def atomic_json(path, value):
    handle, name = tempfile.mkstemp(prefix=".install-", dir=path.parent)
    try:
        with os.fdopen(handle, "w") as stream:
            json.dump(value, stream, indent=2)
            stream.write("\n")
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def build(root, state):
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    # Nix changes this GC root only after a successful build. The pinned
    # installation's separate `package` root remains available for rollback.
    with (state / "source-build.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        package = subprocess.check_output([
            "nix", "build", "--no-write-lock-file", f"path:{root}#dopamine",
            "--out-link", str(state / "source-package"), "--print-out-paths",
        ], text=True).strip()
        if not package.startswith("/nix/store/") or "\n" in package:
            raise RuntimeError("Unexpected Nix package output")
        for receipt in (state / "install.json", state.parent / "dopamine/install.json"):
            if receipt.exists():
                record = json.loads(receipt.read_text())
                if record.get("mode") == "source" and record.get("checkout") == str(root):
                    record["package"] = package
                    atomic_json(receipt, record)
        return package


def main():
    root = Path(__file__).resolve().parents[1]
    state = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")) / "project-hub"
    package = build(root, state)
    executable = str(Path(package) / "bin/dopamine")
    env = os.environ.copy()
    env["PROJECT_HUB_EXECUTABLE"] = executable
    env.pop("PROJECT_HUB_DASHBOARD", None)
    env.pop("PROJECT_HUB_TUI", None)
    os.execve(executable, [executable, *sys.argv[1:]], env)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print(f"dopamine: build or launch failed: {error}", file=sys.stderr)
        if sys.stdin.isatty():
            input("Press Enter to close. ")
        sys.exit(1)
