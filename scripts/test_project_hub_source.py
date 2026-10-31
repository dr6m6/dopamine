"""Launcher integration tests use a fake Nix binary and temporary HOME only."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


SOURCE = load("hub_source", ROOT / "scripts/project-hub-source.py")


class SourceLauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="hub-launcher-test-")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.state = self.home / ".local/state/project-hub"
        self.state.mkdir(parents=True)
        self.launcher = self.home / ".local/bin/project-hub"
        self.launcher.parent.mkdir(parents=True)
        self.desktop = self.home / ".local/share/applications/project-hub.desktop"
        self.desktop.parent.mkdir(parents=True)
        self.receipt = self.state / "install.json"
        self.pinned = "/nix/store/old-project-hub"
        (self.state / "package").symlink_to(self.pinned)
        self.launcher.symlink_to(self.state / "package/bin/project-hub")
        self.desktop.write_text("[Desktop Entry]\nExec=pinned-hub\n")
        self.record = {
            "package": self.pinned, "launcher_target": str(self.state / "package/bin/project-hub"),
            "desktop_sha256": hashlib.sha256(self.desktop.read_bytes()).hexdigest(),
        }
        self.receipt.write_text(json.dumps(self.record))
        fake = self.home / "tools"
        fake.mkdir()
        nix = fake / "nix"
        nix.write_text('#!' + sys.executable + '\n' + '''import json, os, pathlib, sys
with open(os.environ["NIX_CALLS"], "a") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
if os.environ.get("NIX_FAIL"):
    sys.exit(42)
link = pathlib.Path(sys.argv[sys.argv.index("--out-link") + 1])
if link.is_symlink():
    link.unlink()
link.symlink_to(os.environ["NIX_PACKAGE"])
print(os.environ["NIX_PACKAGE"])
''')
        nix.chmod(0o755)
        self.env = os.environ.copy()
        self.env.update(HOME=str(self.home), XDG_STATE_HOME=str(self.home / ".local/state"),
                        XDG_DATA_HOME=str(self.home / ".local/share"),
                        PATH=str(fake) + ":" + self.env["PATH"],
                        NIX_CALLS=str(self.home / "nix-calls"), NIX_PACKAGE="/nix/store/new-project-hub")

    def install(self, *args, check=True):
        return subprocess.run([sys.executable, str(ROOT / "scripts/install-project-hub.py"), *args],
                              env=self.env, text=True, capture_output=True, check=check)

    def test_migration_and_future_builds_keep_original_package_and_data(self):
        marker = self.state / "keep-session-data"
        marker.write_text("history")
        self.install("--source")
        self.assertEqual(os.readlink(self.launcher), str(ROOT / "scripts/project-hub-source.py"))
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)
        self.assertEqual(json.loads((self.state / "install.before-source.json").read_text()), self.record)
        self.assertIn(str(self.launcher), self.desktop.read_text())
        self.env["NIX_PACKAGE"] = "/nix/store/next-project-hub"
        with patch.dict(os.environ, self.env, clear=True):
            self.assertEqual(SOURCE.build(ROOT, self.state), self.env["NIX_PACKAGE"])
        record = json.loads(self.receipt.read_text())
        self.assertEqual(record["package"], self.env["NIX_PACKAGE"])
        calls = [json.loads(line) for line in (self.home / "nix-calls").read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertIn(f"path:{ROOT}#dopamine", calls[1])
        self.assertIn("--no-write-lock-file", calls[1])
        self.install("--uninstall")
        self.assertFalse(self.launcher.is_symlink())
        self.assertFalse(self.desktop.exists())
        self.assertFalse((self.state / "source-package").is_symlink())
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)
        self.assertEqual(marker.read_text(), "history")

    def test_failed_build_does_not_replace_installed_launcher(self):
        before = (os.readlink(self.launcher), self.desktop.read_bytes(), self.receipt.read_bytes())
        self.env["NIX_FAIL"] = "1"
        result = self.install("--source", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Nix command failed (exit 42)", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertEqual(before, (os.readlink(self.launcher), self.desktop.read_bytes(), self.receipt.read_bytes()))
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)

    def test_changed_desktop_is_preserved_before_any_build(self):
        self.desktop.write_text("user changes")
        result = self.install("--source", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed desktop entry", result.stderr)
        self.assertEqual(self.desktop.read_text(), "user changes")
        self.assertFalse((self.home / "nix-calls").exists())

    def test_source_mode_can_be_enabled_repeatedly(self):
        self.install("--source")
        backup = (self.state / "install.before-source.json").read_bytes()
        self.install("--source")
        self.assertEqual((self.state / "install.before-source.json").read_bytes(), backup)

    def test_launch_uses_both_binaries_from_new_package_and_forwards_arguments(self):
        with patch.object(SOURCE, "build", return_value="/nix/store/current-hub"), \
             patch.object(SOURCE.os, "execve") as execute, \
             patch.object(SOURCE.sys, "argv", ["project-hub", "sessions"]), \
             patch.dict(os.environ, {"PROJECT_HUB_EXECUTABLE": "old", "PROJECT_HUB_DASHBOARD": "old", "PROJECT_HUB_TUI": "1"}):
            SOURCE.main()
        path, argv, env = execute.call_args.args
        self.assertEqual(path, "/nix/store/current-hub/bin/dopamine")
        self.assertEqual(argv, [path, "sessions"])
        self.assertEqual(env["PROJECT_HUB_EXECUTABLE"], path)
        self.assertNotIn("PROJECT_HUB_DASHBOARD", env)
        self.assertNotIn("PROJECT_HUB_TUI", env)


if __name__ == "__main__":
    unittest.main()
