"""Exercise the real installer with fake Nix and an isolated HOME."""
import hashlib
import json
import os
import unittest

from test_dopamine_source import InstallFixture, ROOT


class DopamineInstallTests(InstallFixture):
    def test_install_update_and_uninstall_preserve_runtime_and_rollback(self):
        marker = self.state / "keep-session-data"
        marker.write_text("history")
        self.install()
        self.assertEqual(self.launcher.resolve(), ROOT / "scripts/dopamine-source.py")
        self.assertFalse(self.desktop.exists())
        self.assertEqual(json.loads(self.receipt.read_text())["mode"], "source")
        self.install()
        self.install("--uninstall")
        self.assertFalse(self.launcher.is_symlink())
        self.assertFalse(self.desktop.exists())
        self.assertFalse(self.receipt.exists())
        self.assertEqual(marker.read_text(), "history")
        self.assertEqual(os.readlink(self.state / "package"), self.pinned)

    def test_failed_build_leaves_existing_installation_untouched(self):
        self.install()
        before = (os.readlink(self.launcher), self.receipt.read_bytes())
        self.env["NIX_FAIL"] = "1"
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, (os.readlink(self.launcher), self.receipt.read_bytes()))

    def test_changed_launcher_is_rejected_before_build(self):
        self.install()
        self.launcher.unlink()
        self.launcher.write_text("user script")
        calls = (self.home / "nix-calls").read_bytes()
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed launcher", result.stderr)
        self.assertEqual((self.home / "nix-calls").read_bytes(), calls)
        self.assertEqual(self.launcher.read_text(), "user script")

    def test_existing_unowned_launcher_is_preserved(self):
        self.launcher.write_text("user script")
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.home / "nix-calls").exists())
        self.assertEqual(self.launcher.read_text(), "user script")

    def test_existing_owned_app_entry_is_removed_and_not_recreated(self):
        self.install()
        self.desktop.write_text("[Desktop Entry]\nName=dopamine\n")
        record = json.loads(self.receipt.read_text())
        record["desktop_sha256"] = hashlib.sha256(self.desktop.read_bytes()).hexdigest()
        self.receipt.write_text(json.dumps(record))
        self.install()
        self.assertFalse(self.desktop.exists())
        self.assertNotIn("desktop_sha256", json.loads(self.receipt.read_text()))
        self.install()
        self.assertFalse(self.desktop.exists())

    def test_changed_desktop_is_preserved_before_any_build(self):
        self.install()
        self.desktop.write_text("owned desktop")
        record = json.loads(self.receipt.read_text())
        record["desktop_sha256"] = hashlib.sha256(self.desktop.read_bytes()).hexdigest()
        self.receipt.write_text(json.dumps(record))
        self.desktop.write_text("user changes")
        calls = (self.home / "nix-calls").read_bytes()
        result = self.install(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed desktop entry", result.stderr)
        self.assertEqual(self.desktop.read_text(), "user changes")
        self.assertEqual((self.home / "nix-calls").read_bytes(), calls)


if __name__ == "__main__":
    unittest.main()
