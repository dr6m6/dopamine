"""Exercise the real installer with fake Nix and an isolated HOME."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

import test_project_hub_source as legacy


class DopamineInstallTests(unittest.TestCase):
    setUp = legacy.SourceLauncherTests.setUp

    def install(self, *args, check=True):
        return subprocess.run([sys.executable, str(legacy.ROOT / "scripts/install-dopamine.py"), *args],
                              env=self.env, text=True, capture_output=True, check=check)

    @property
    def new_launcher(self):
        return self.home / ".local/bin/dopamine"

    @property
    def new_desktop(self):
        return self.home / ".local/share/applications/dopamine.desktop"

    def own_legacy(self):
        self.launcher.unlink()
        self.launcher.symlink_to(legacy.ROOT / "scripts/project-hub-source.py")
        self.record["launcher_target"] = str(legacy.ROOT / "scripts/project-hub-source.py")
        self.record["desktop_sha256"] = hashlib.sha256(self.desktop.read_bytes()).hexdigest()
        self.receipt.write_text(json.dumps(self.record))

    def test_migrate_and_uninstall_preserve_runtime_and_rollback(self):
        self.own_legacy()
        marker = self.state / "keep-session-data"
        marker.write_text("history")
        before = self.receipt.read_bytes()
        self.install()
        self.assertEqual(self.new_launcher.resolve(), legacy.ROOT / "scripts/dopamine-source.py")
        self.assertFalse(self.new_desktop.exists())
        self.assertFalse(self.launcher.is_symlink())
        self.assertFalse(self.desktop.exists())
        self.assertEqual(self.receipt.read_bytes(), before)
        self.assertEqual(marker.read_text(), "history")
        self.install()  # Updating an owned installation remains repeatable.
        self.install("--uninstall")
        self.assertFalse(self.new_launcher.is_symlink())
        self.assertFalse(self.new_desktop.exists())
        self.assertEqual(marker.read_text(), "history")
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)

    def test_failed_build_leaves_existing_launchers_untouched(self):
        self.own_legacy()
        self.env["NIX_FAIL"] = "1"
        before = (os.readlink(self.launcher), self.desktop.read_bytes(), self.receipt.read_bytes())
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, (os.readlink(self.launcher), self.desktop.read_bytes(), self.receipt.read_bytes()))
        self.assertFalse(self.new_launcher.is_symlink())

    def test_changed_legacy_desktop_is_not_removed(self):
        self.own_legacy()
        self.desktop.write_text("user changes")
        self.install()
        self.assertTrue(self.launcher.is_symlink())
        self.assertEqual(self.desktop.read_text(), "user changes")

    def test_changed_dopamine_launcher_is_rejected_before_build(self):
        self.install()
        self.new_launcher.unlink()
        self.new_launcher.write_text("user script")
        calls = (self.home / "nix-calls").read_bytes()
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed launcher", result.stderr)
        self.assertEqual((self.home / "nix-calls").read_bytes(), calls)
        self.assertEqual(self.new_launcher.read_text(), "user script")

    def test_existing_owned_app_entry_is_removed_and_not_recreated(self):
        self.install()
        self.new_desktop.write_text("[Desktop Entry]\nName=dopamine\n")
        receipt = self.home / ".local/state/dopamine/install.json"
        record = json.loads(receipt.read_text())
        record["desktop_sha256"] = hashlib.sha256(self.new_desktop.read_bytes()).hexdigest()
        receipt.write_text(json.dumps(record))
        self.install()
        self.assertFalse(self.new_desktop.exists())
        self.assertNotIn("desktop_sha256", json.loads(receipt.read_text()))
        self.install()
        self.assertFalse(self.new_desktop.exists())


if __name__ == "__main__":
    unittest.main()
