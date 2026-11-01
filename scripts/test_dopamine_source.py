"""Source launcher checks use fake Nix and a temporary HOME."""
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
spec = importlib.util.spec_from_file_location("dopamine_source", ROOT / "scripts/dopamine-source.py")
SOURCE = importlib.util.module_from_spec(spec)
spec.loader.exec_module(SOURCE)


class InstallFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="dopamine-launcher-test-")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.state = self.home / ".local/state/dopamine"
        self.state.mkdir(parents=True)
        self.launcher = self.home / ".local/bin/dopamine"
        self.launcher.parent.mkdir(parents=True)
        self.desktop = self.home / ".local/share/applications/dopamine.desktop"
        self.desktop.parent.mkdir(parents=True)
        self.receipt = self.state / "install.json"
        self.pinned = "/nix/store/previous-dopamine"
        (self.state / "package").symlink_to(self.pinned)
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
                        NIX_CALLS=str(self.home / "nix-calls"), NIX_PACKAGE="/nix/store/current-dopamine")

    def install(self, *args, check=True):
        return subprocess.run([sys.executable, str(ROOT / "scripts/install-dopamine.py"), *args],
                              env=self.env, text=True, capture_output=True, check=check)


class SourceLauncherTests(InstallFixture):
    def test_rebuild_updates_receipt_and_keeps_rollback_and_data(self):
        self.install()
        marker = self.state / "keep-session-data"
        marker.write_text("history")
        self.env["NIX_PACKAGE"] = "/nix/store/next-dopamine"
        with patch.dict(os.environ, self.env, clear=True):
            self.assertEqual(SOURCE.build(ROOT, self.state), self.env["NIX_PACKAGE"])
        self.assertEqual(json.loads(self.receipt.read_text())["package"], self.env["NIX_PACKAGE"])
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)
        self.assertEqual(marker.read_text(), "history")
        calls = [json.loads(line) for line in (self.home / "nix-calls").read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertIn(f"path:{ROOT}#dopamine", calls[1])
        self.assertIn("--no-write-lock-file", calls[1])

    def test_launch_forwards_arguments_and_uses_dopamine_state_and_environment(self):
        with patch.object(SOURCE, "build", return_value="/nix/store/current-dopamine") as build, \
             patch.object(SOURCE.os, "execve") as execute, \
             patch.object(SOURCE.sys, "argv", ["dopamine", "sessions"]), \
             patch.dict(os.environ, {**self.env, "DOPAMINE_EXECUTABLE": "old", "DOPAMINE_DASHBOARD": "old", "DOPAMINE_TUI": "1"}, clear=True):
            SOURCE.main()
        build.assert_called_once_with(ROOT, self.state)
        path, argv, env = execute.call_args.args
        self.assertEqual(path, "/nix/store/current-dopamine/bin/dopamine")
        self.assertEqual(argv, [path, "sessions"])
        self.assertEqual(env["DOPAMINE_EXECUTABLE"], path)
        self.assertNotIn("DOPAMINE_DASHBOARD", env)
        self.assertNotIn("DOPAMINE_TUI", env)


if __name__ == "__main__":
    unittest.main()
