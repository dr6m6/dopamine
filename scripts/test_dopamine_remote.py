"""Connection validation, safe quoting and data-preserving installation."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("dopamine-remote.py")
SPEC = importlib.util.spec_from_file_location("dopamine_remote", SCRIPT)
remote = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(remote)


class RemoteTests(unittest.TestCase):
    def test_rejects_options_and_shell_syntax_in_destination(self):
        for destination in ("-oProxyCommand=bad", "user@host;touch /tmp/no", "user@host\nx", "user@host/name", "user@host:22", None, ""):
            with self.subTest(destination=destination), self.assertRaises(ValueError):
                remote.validate({"destination": destination})
        for destination in ("home", "user@home.example", "user@100.64.0.1", "user@fd7a:115c:a1e0::1"):
            remote.validate({"destination": destination})

    def test_remote_executable_is_shell_quoted(self):
        with tempfile.TemporaryDirectory() as temp:
            executable = Path(temp) / "executable ' with spaces $(touch nope)"
            executable.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            executable.chmod(0o755)
            command = remote.remote_command({"command": str(executable)}, "projects", "';touch nope")
            result = subprocess.check_output(["sh", "-c", command], cwd=temp, text=True)
            self.assertEqual(result, "projects\n';touch nope\n")
            self.assertFalse((Path(temp) / "nope").exists())
            home_command = remote.remote_command({"command": "~/.local/bin/a b"}, "projects")
            self.assertIn('"$HOME"/', home_command)
        with self.assertRaises(ValueError):
            remote.validate({"destination": "home", "command": "dopamine --bad"})

    def test_forward_only_local_app_urls_and_preserves_paths(self):
        self.assertEqual(remote.app_target("http://localhost:5173/a?q=1#x", 15173),
                         (15173, "127.0.0.1:15173:localhost:5173", "http://127.0.0.1:15173/a?q=1#x"))
        self.assertEqual(remote.app_target("https://[::1]/")[1], "127.0.0.1:443:[::1]:443")
        self.assertEqual(remote.app_target("http://10.203.31.1:8420/")[1], "127.0.0.1:8420:10.203.31.1:8420")
        for raw in ("file:///etc/passwd", "http://example.com:80/", "http://localhost:0/", "http://localhost:65536/",
                    "http://user@localhost/", "http://127.0.0.2/", "http://localhost/\n", None):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                remote.app_target(raw)

    def test_ssh_alias_keeps_configured_port_and_authentication(self):
        profile = remote.validate({"destination": "home"})
        args = remote.ssh_base(profile)
        self.assertNotIn("-p", args)
        self.assertIn("ForwardAgent=no", args)
        self.assertNotIn("StrictHostKeyChecking=no", args)
        profile.update(port=2222, identity="/tmp/key with spaces")
        args = remote.ssh_base(profile)
        self.assertEqual(args[args.index("-p") + 1], "2222")
        self.assertEqual(args[args.index("-i") + 1], "/tmp/key with spaces")

    def test_arbitrary_mockup_does_not_require_a_project_registry(self):
        with patch.object(remote, "projects", side_effect=AssertionError("registry should not be read")):
            self.assertEqual(remote.target_url({}, "42807"), "http://127.0.0.1:42807/")
            self.assertEqual(remote.app_target(remote.target_url({}, "http://127.0.0.1:42807/a?q=1#x")),
                             (42807, "127.0.0.1:42807:127.0.0.1:42807", "http://127.0.0.1:42807/a?q=1#x"))
            for target in ("0", "65536", "00428", "http://example.com/", "file:///etc/passwd"):
                with self.subTest(target=target), self.assertRaises(ValueError):
                    remote.app_target(remote.target_url({}, target))

    def test_project_open_remains_supported_and_rejects_ambiguous_ids(self):
        with patch.object(remote, "projects", return_value=[{"id": "demo", "app_url": "http://localhost:5173/"}]):
            self.assertEqual(remote.target_url({}, "demo"), "http://localhost:5173/")
            with self.assertRaises(ValueError):
                remote.target_url({}, "missing")
        with patch.object(remote, "projects", return_value=[{"id": "demo"}, {"id": "demo"}]):
            with self.assertRaises(ValueError):
                remote.target_url({}, "demo")
        with patch.object(remote, "projects", return_value=[{"id": "42807", "app_url": "http://localhost:5173/"}]):
            self.assertEqual(remote.target_url({}, "42807", project=True), "http://localhost:5173/")

    def test_setup_preserves_unknown_fields_and_has_private_permissions(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "remote.json"
            path.write_text(json.dumps({"destination": "old", "future": {"keep": True}}))
            remote.main(["--config", str(path), "setup", "user@home"])
            self.assertEqual(json.loads(path.read_text())["future"], {"keep": True})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_installer_preserves_foreign_or_modified_commands_and_settings(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"HOME": temp, "XDG_STATE_HOME": temp + "/state"}):
            launcher = Path(temp) / ".local/bin/dopamine-remote"
            remote.install()
            remote.install()
            self.assertEqual(launcher.read_bytes(), SCRIPT.read_bytes())
            launcher.write_text("user customization")
            with self.assertRaises(ValueError):
                remote.install()
            with self.assertRaises(ValueError):
                remote.install(uninstall=True)
            self.assertEqual(launcher.read_text(), "user customization")
            launcher.write_bytes(SCRIPT.read_bytes())
            settings = Path(temp) / ".config/dopamine/remote.json"
            settings.parent.mkdir(parents=True)
            settings.write_text("preserve")
            remote.install(uninstall=True)
            self.assertFalse(launcher.exists())
            self.assertEqual(settings.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
