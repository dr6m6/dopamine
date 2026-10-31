#!/usr/bin/env python3
"""Real dopamine + tmux + PTY; fake Codex, test HTTP server, no API calls."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import signal
import socket
import sqlite3
import struct
import subprocess
import sys
import tempfile
import termios
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/dopamine")
    parser.add_argument("--capture", default="/tmp/dopamine-dashboard.txt")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    # Nix's wrapper replaces PATH and PROJECT_HUB_EXECUTABLE. Use its payload
    # so the fixture shims, socket and fake desktop notifier stay isolated.
    payload = Path(binary).with_name("." + Path(binary).name + "-wrapped")
    if payload.is_file():
        binary = str(payload)
    real_tmux = subprocess.check_output(["which", "tmux"], text=True).strip()
    with tempfile.TemporaryDirectory(prefix="dopamine-smoke-") as temp:
        root = Path(temp)
        tools = root / "tools"
        tools.mkdir()
        sock = str(root / "tmux.sock")
        tmux = [real_tmux, "-S", sock, "-f", "/dev/null"]
        # Every child tmux call stays on the isolated server, including gates.
        (tools / "tmux").write_text("#!/bin/sh\nexec " + shlex.join(tmux) + ' "$@"\n')
        (tools / "tmux").chmod(0o755)
        (tools / "xdg-open").write_text("#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shlex.quote(str(root / "opened")) + "\n")
        (tools / "xdg-open").chmod(0o755)
        (tools / "notify-send").write_text("#!" + sys.executable + "\nimport json,sys\nfrom pathlib import Path\nwith Path(" + repr(str(root / "notifications.jsonl")) + ").open('a') as stream:\n stream.write(json.dumps(sys.argv[1:]) + '\\n')\n")
        (tools / "notify-send").chmod(0o755)
        (root / ".codex").mkdir()
        with sqlite3.connect(root / ".codex/state_5.sqlite") as db:
            db.execute("CREATE TABLE threads(id TEXT, source TEXT)")
            db.execute("INSERT INTO threads VALUES (?, 'cli')", ("11111111-2222-3333-4444-555555555555",))
        fake_codex = root / "fake-codex.py"
        fake_codex.write_text("""import json, os, subprocess, sys
from pathlib import Path
print('CODEX FIXTURE', os.getpid(), flush=True)
for line in sys.stdin:
 command = line.strip()
 if command == 'question':
  print('\\x1bPtmux;\\x1b\\x1b]9;Question: Fixture needs an answer\\a\\x1b\\\\', end='', flush=True)
 elif command == 'done':
  for path in (Path(os.environ['PROJECT_HUB_STATE_DIR'])/'runs').glob('*.json'):
   record = json.loads(path.read_text())
   if record['Kind'] == 'codex' and record['Pane'] == os.environ['TMUX_PANE']:
    event = json.dumps({'type':'agent-turn-complete','thread-id':'11111111-2222-3333-4444-555555555555'})
    subprocess.run([os.environ['PROJECT_HUB_EXECUTABLE'],'codex-notify',record['Key'],record['Token'],event],check=True)
 print('echo:', command, flush=True)
""")
        shim = tools / "dopamine"
        shim.write_text("#!/bin/sh\nif [ \"${1:-}\" = exec-codex ]; then exec " + shlex.join([sys.executable, str(fake_codex)]) + "; fi\nexec " + shlex.quote(binary) + ' "$@"\n')
        shim.chmod(0o755)
        project = root / "project"
        second = root / "second"
        project.mkdir()
        second.mkdir()
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        url = f"http://127.0.0.1:{port}/"
        config = root / "config"
        config.mkdir()
        (config / "projects.json").write_text(json.dumps({"projects": [
            {"id": "second", "name": "Second", "path": str(second)},
            {"id": "fixture", "name": "Fixture", "path": str(project), "app_url": url,
             "launch": [sys.executable, "-m", "http.server", str(port), "--bind", "127.0.0.1"]}]}))
        env = os.environ.copy()
        env.update(HOME=temp, XDG_STATE_HOME=str(root / "xdg-state"), XDG_CONFIG_HOME=str(root / "xdg-config"),
                   CODEX_HOME=str(root / ".codex"),
                   PROJECT_HUB_EXECUTABLE=str(shim), PROJECT_HUB_CONFIG_DIR=str(config),
                   PROJECT_HUB_STATE_DIR=str(root / "state"), PROJECT_HUB_PROJECTS_ROOT=str(root / "no-discover"),
                   PATH=str(tools) + ":" + env["PATH"], TERM="xterm-256color", SHELL=shutil.which("bash"))
        for key in ("TMUX", "TMUX_PANE", "PROJECT_HUB_TUI", "PROJECT_HUB_DASHBOARD"):
            env.pop(key, None)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
        process = None
        output = bytearray()

        def drain():
            while select.select([master], [], [], 0)[0]:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break

        def command(*parts):
            return subprocess.check_output(tmux + list(parts), env=env, text=True, stderr=subprocess.DEVNULL).strip()

        def wait_for(check, description, timeout=12):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                drain()
                try:
                    if check():
                        return
                except (OSError, subprocess.CalledProcessError, urllib.error.URLError):
                    pass
                time.sleep(0.1)
            raise AssertionError(description + "\n" + command("capture-pane", "-p", "-t", "project-hub:"))

        def screen():
            return command("capture-pane", "-p", "-t", "project-hub:")

        def overview():
            text = screen()
            return "dopamine" in text and ("> Fixture" in text or "> Second" in text) and "new codex session" not in text

        def key(value):
            os.write(master, value)

        def resize(width, height):
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
            os.kill(process.pid, signal.SIGWINCH)
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{window_width}") == str(width), "tmux did not receive terminal resize")

        def rail_is_fixed(pane):
            rows = command("list-panes", "-t", pane, "-F", "#{pane_id}|#{pane_width}|#{@project_hub_role}").splitlines()
            rails = [row.split("|") for row in rows if row.endswith("|dopamine-rail")]
            window_width = int(command("display-message", "-p", "-t", pane, "#{window_width}"))
            main_width = int(command("display-message", "-p", "-t", pane, "#{pane_width}"))
            return len(rails) == 1 and rails[0][1] == "5" and main_width == window_width - 6

        def healthy():
            with urllib.request.urlopen(url, timeout=0.3) as response:
                return response.status == 200

        def connect():
            return subprocess.Popen([str(shim)], env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)

        def notifications():
            path = root / "notifications.jsonl"
            return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

        def expect_notification(pane, kind, number):
            before = len(notifications())
            command("send-keys", "-t", pane, kind, "Enter")
            wait_for(lambda: len(notifications()) > before, kind + " notification did not arrive")
            icon = root / "state/icons/dopamine.svg"
            assert icon.is_file(), "notification icon was not materialized"
            assert notifications()[-1] == ["--app-name=dopamine notifications", "--category=" + kind, "--icon=" + str(icon), "--", f"{kind} · codex #{number} needs you"], notifications()

        def missing_slot_is_ignored(number, pane):
            # Synchronize with the callback completing; a sleep can miss the
            # copy-mode error that run-shell emits after its command exits.
            marker = root / ("missing-slot-" + str(number))
            marker.unlink(missing_ok=True)
            binding = shlex.split(command("list-keys", "-T", "project-hub-root", "M-" + str(number)))
            callback = binding[-1]
            assert "slot" in callback, binding
            command("bind-key", "-T", "project-hub-root", "M-" + str(number),
                    "run-shell", "-b", callback +
                    "; result=$?; printf '%s' \"$result\" > " + shlex.quote(str(marker)) +
                    "; exit \"$result\"")
            key(b"\x1b" + str(number).encode())
            wait_for(marker.exists, "missing-slot callback did not finish")
            assert marker.read_text() == "0", "missing slot returned an error"
            assert command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == pane
            assert command("display-message", "-p", "-t", pane, "#{pane_in_mode}") == "0", "missing slot entered copy mode"

        try:
            process = connect()
            wait_for(lambda: overview() and "Fixture" in screen(), "Dashboard did not open")
            wait_for(lambda: "project-hub" in command("list-clients", "-F", "#{client_session}"), "test client did not attach")
            dashboard = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            Path(args.capture).write_text(screen() + "\n")
            dashboard_screen = screen()
            assert all(line.index("│") == 5 for line in dashboard_screen.splitlines()[1:] if "│" in line), dashboard_screen
            statuses = [line.index("stopped") for line in dashboard_screen.splitlines() if "stopped" in line]
            assert len(statuses) == 1, statuses
            assert dashboard_screen.index("Fixture") < dashboard_screen.index("Second"), "unconfigured project sorted first"
            assert "stopped" not in next(line for line in dashboard_screen.splitlines() if "Second" in line)
            assert "Alt+" not in dashboard_screen and "s Start" not in dashboard_screen
            assert "start server" not in dashboard_screen and "new codex session" not in dashboard_screen
            rows = dashboard_screen.splitlines()
            global_row = next(i for i, row in enumerate(rows) if "new terminal" in row)
            assert not rows[global_row - 1].split("│", 1)[-1].strip(), "global actions need a blank line after projects"
            notification_row = next(i for i, row in enumerate(rows) if "notifications: on" in row)
            assert notification_row > global_row + 2 and not rows[notification_row - 1].split("│", 1)[-1].strip(), "notifications need a gap below folder action"
            missing_slot_is_ignored(3, dashboard)
            key(b"sn")
            time.sleep(0.2)
            assert not (root / "state/runs/app-fixture.json").exists(), "shortcut launched server before project selection"
            key(b"?")
            wait_for(lambda: "keyboard shortcuts" in screen(), "? did not open keyboard help")
            key(b"?")
            wait_for(lambda: overview() and "keyboard shortcuts" not in screen(), "? did not close help")
            key(b"j")
            wait_for(lambda: "> Second" in screen(), "project navigation failed")
            key(b"k")
            key(b"\r")
            wait_for(lambda: "new codex session" in screen(), "Enter did not open project actions")
            Path(args.capture).with_name("dopamine-project.txt").write_text(screen() + "\n")
            key(b"\x1b")
            wait_for(lambda: overview() and "start server" not in screen(), "Esc did not return to project list")
            key(b"\r")
            wait_for(lambda: "new codex session" in screen(), "project actions did not reopen")
            key(b"s")
            wait_for(healthy, "test server did not start")
            assert "start server" in screen(), "server start closed the project menu"
            record_path = root / "state/runs/app-fixture.json"
            record = json.loads(record_path.read_text())
            key(b"s")
            time.sleep(0.4)
            drain()
            assert json.loads(record_path.read_text())["Token"] == record["Token"], "duplicate server started"
            key(b"o")
            wait_for(lambda: (root / "opened").exists(), "open url did not call browser handler")
            assert (root / "opened").read_text().strip() == url
            assert "start server" in screen(), "open url closed the project menu"
            key(b"x")
            wait_for(lambda: json.loads(record_path.read_text())["State"] == "Stopped", "test server did not stop")
            assert "start server" in screen(), "server stop closed the project menu"
            key(b"\r")
            wait_for(lambda: "CODEX FIXTURE" in screen(), "new workspace did not open")
            first = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            first_pid = command("display-message", "-p", "-t", first, "#{pane_pid}")
            missing_slot_is_ignored(3, first)
            missing_slot_is_ignored(9, first)
            panes = command("list-panes", "-t", "project-hub:", "-F", "#{pane_width}|#{@project_hub_role}")
            assert "5|dopamine-rail" in panes, panes
            for width, height in [(220, 60), (80, 24), (180, 50), (100, 30)]:
                resize(width, height)
                wait_for(lambda: rail_is_fixed(first), "rail expanded after fullscreen/restore")
                assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            command("select-layout", "-t", first, "even-horizontal")
            wait_for(lambda: rail_is_fixed(first), "layout change left the rail expanded")
            key(b"hello\r")
            wait_for(lambda: "echo: hello" in screen(), "native workspace input failed")
            expect_notification(first, "question", 2)
            expect_notification(first, "done", 2)
            # Alt+1 is owned by the session table; Codex receives no shortcuts.
            key(b"\x1b1")
            wait_for(overview, "Alt+1 did not return to the project list after Codex launch")
            key(b"jjjj")
            wait_for(lambda: "> notifications: on" in screen(), "notification row not reachable by project navigation")
            key(b"\r")
            wait_for(lambda: "> notifications: off" in screen(), "Enter did not mute notifications")
            assert json.loads((config / "settings.json").read_text())["notifications"] is False
            before_muted = len(notifications())
            command("send-keys", "-t", first, "question", "Enter", "done", "Enter", "mute-check-complete", "Enter")
            first_log = next(json.loads(path.read_text())["Log"] for path in (root / "state/runs").glob("*.json") if json.loads(path.read_text())["Pane"] == first)
            wait_for(lambda: "echo: mute-check-complete" in Path(first_log).read_text(), "muting interrupted Codex input or its log")
            time.sleep(0.2)
            assert len(notifications()) == before_muted, "muted question or completion reached desktop"
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            key(b"\r")
            wait_for(lambda: "> notifications: on" in screen(), "Enter did not re-enable notifications")
            expect_notification(first, "question", 2)
            expect_notification(first, "done", 2)
            key(b"kkkk")
            wait_for(overview, "notification toggle lost project navigation")
            resize(200, 55)
            key(b"\r")
            wait_for(lambda: "start server" in screen(), "project menu did not reopen for a second Codex")
            key(b"n")
            wait_for(lambda: "CODEX FIXTURE" in screen() and command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") != first, "second workspace did not open")
            third = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            expect_notification(third, "question", 3)
            expect_notification(third, "done", 3)
            key(b"\x1b2")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == first, "Alt+2 failed")
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            wait_for(lambda: rail_is_fixed(first), "switching from fullscreen Dashboard left the rail expanded")
            resize(100, 30)
            wait_for(lambda: rail_is_fixed(first), "restoring terminal size left the rail expanded")
            # Close confirmation must be cancellable without losing Codex.
            key(b"\x17")
            wait_for(lambda: b"close this workspace?" in output, "Ctrl+w did not show close confirmation")
            key(b"\x1b")
            # Let tmux's default escape-time expire before the next Alt key.
            time.sleep(0.7)
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            key(b"\x1b1")
            wait_for(overview, "second Codex launch left its project menu open")
            # Disconnect this test client to simulate a later update, without
            # invoking the application's Exit action yet.
            command("detach-client")
            process.wait(timeout=5)
            assert process.returncode == 0, process.returncode
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            expect_notification(first, "question", 2)
            expect_notification(first, "done", 2)
            dashboard_pid = command("display-message", "-p", "-t", dashboard, "#{pane_pid}")
            # Simulate a newly built package path, exercising the real update
            # mechanism while the native workspaces remain alive.
            updated_shim = tools / "dopamine-updated"
            updated_shim.write_bytes(shim.read_bytes())
            updated_shim.chmod(0o755)
            env["PROJECT_HUB_EXECUTABLE"] = str(updated_shim)
            process = connect()
            wait_for(lambda: overview() and command("display-message", "-p", "-t", dashboard, "#{pane_pid}") != dashboard_pid, "interface update/reattach failed")
            key(b"j")
            wait_for(lambda: "> Second" in screen(), "input failed after reattach")
            assert command("display-message", "-p", "-t", dashboard, "#{pane_id}") == dashboard
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            expect_notification(first, "question", 2)
            key(b"\x1b3")
            wait_for(lambda: "CODEX FIXTURE" in screen(), "third slot unavailable after interface update")
            before_prompt = len(output)
            key(b"\x17")
            wait_for(lambda: b"close this workspace?" in output[before_prompt:], "close confirmation missing after update")
            key(b"\r")
            wait_for(lambda: len(command("list-windows", "-t", "project-hub", "-F", "#{window_id}").splitlines()) == 2, "confirmed workspace did not close")
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            missing_slot_is_ignored(3, command("display-message", "-p", "-t", "project-hub:", "#{pane_id}"))
            key(b"\x1b1")
            wait_for(lambda: overview(), "dashboard unavailable after closing workspace")
            key(b"q")
            process.wait(timeout=5)
            wait_for(lambda: dashboard not in command("list-panes", "-s", "-t", "project-hub", "-F", "#{pane_id}").splitlines(), "q left the interface running")
            assert command("display-message", "-p", "-t", first, "#{pane_pid}") == first_pid
            expect_notification(first, "question", 2)
            expect_notification(first, "done", 2)
            # Slot 1 remains Dashboard even after Exit removes its process.
            subprocess.run([str(updated_shim), "slot", "1"], env=env, check=True)
            process = connect()
            wait_for(lambda: overview(), "Dashboard could not reopen after Exit")
            dashboard = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            key(b"\x1b2")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == first, "workspace number changed after Exit")
            key(b"\x1b1")
            wait_for(lambda: overview(), "Dashboard slot 1 was not preserved")

            # Native shell workspaces share the rail and survive navigation.
            key(b"\r")
            wait_for(lambda: "new codex session" in screen(), "project menu unavailable for terminal")
            key(b"t")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{@project_hub_kind}") == "shell", "project terminal did not open")
            project_shell = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            project_shell_pid = command("display-message", "-p", "-t", project_shell, "#{pane_pid}")
            wait_for(lambda: command("display-message", "-p", "-t", project_shell, "#{pane_current_path}") == str(project), "project shell started in wrong directory")
            wait_for(lambda: rail_is_fixed(project_shell), "shell rail was not configured")
            assert command("display-message", "-p", "-t", project_shell, "#{pane_pipe}") == "0", "shell received Codex observer"
            key(b"\x1b1")
            wait_for(overview, "terminal launch left project menu open")
            key(b"jj")
            wait_for(lambda: "> new terminal" in screen(), "global terminal action unavailable")
            key(b"\r")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{@project_hub_kind}") == "shell" and command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") != project_shell, "independent home terminal did not open")
            home_shell = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            home_pid = command("display-message", "-p", "-t", home_shell, "#{pane_pid}")
            wait_for(lambda: command("display-message", "-p", "-t", home_shell, "#{pane_current_path}") == str(root), "global terminal did not start in home")
            key(("printf 'native shell works' > " + shlex.quote(str(root / "shell-result")) + "\r").encode())
            wait_for(lambda: (root / "shell-result").exists(), "native shell input did not execute")
            assert (root / "shell-result").read_text() == "native shell works"
            # Detach/reattach must preserve both shell processes and their rails.
            command("detach-client")
            process.wait(timeout=5)
            process = connect()
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == dashboard and "new terminal" in screen() and command("list-clients", "-F", "#{client_session}") == "project-hub", "Dashboard did not reopen with live shells")
            assert command("display-message", "-p", "-t", home_shell, "#{pane_pid}") == home_pid
            assert command("display-message", "-p", "-t", project_shell, "#{pane_pid}") == project_shell_pid
            key(b"\x1b4")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == home_shell, "home terminal lost its slot")
            wait_for(lambda: rail_is_fixed(home_shell), "home shell rail changed after reattach")
            key(b"\x1b1")
            wait_for(lambda: "new terminal" in screen() and "new codex session" not in screen(), "Dashboard unavailable for folder addition")
            key(b"kkkkkkjjj")
            wait_for(lambda: "> + add folder" in screen(), "add folder action unavailable after reconnect")
            key(b"\r")
            wait_for(lambda: "path:" in screen(), "add folder did not open input")
            external = root / "quite external 123"
            external.mkdir()
            prefix = str(root / "quite")
            key(b"\x1b[200~" + prefix.encode() + b"\x1b[201~")
            key(b"\t")
            wait_for(lambda: "quite external 123/" in screen(), "Tab completion or pasted path failed")
            key(b"\r")
            wait_for(lambda: "> quite external 123" in screen(), "external folder did not appear selected")
            saved = json.loads((config / "projects.json").read_text())["projects"]
            assert len(saved) == 3 and saved[-1]["path"] == str(external)
            key(b"\r")
            wait_for(lambda: "remove from list" in screen(), "external folder has no remove action")
            key(b"t")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_current_path}") == str(external), "terminal in external folder failed")
            external_shell = command("display-message", "-p", "-t", "project-hub:", "#{pane_id}")
            external_pid = command("display-message", "-p", "-t", external_shell, "#{pane_pid}")
            key(b"\x1b1")
            wait_for(lambda: "> quite external 123" in screen(), "external folder selection was lost")
            key(b"\r")
            wait_for(lambda: "remove from list" in screen(), "external menu unavailable")
            key(b"jjjjj\r")
            wait_for(lambda: "remove this folder from the list?" in screen(), "remove confirmation missing")
            key(b"\x1b")
            wait_for(lambda: "remove from list" in screen(), "remove cancellation failed")
            assert len(json.loads((config / "projects.json").read_text())["projects"]) == 3
            key(b"\r\r")
            wait_for(lambda: "folder removed from list" in screen(), "external folder was not removed")
            assert external.is_dir() and len(json.loads((config / "projects.json").read_text())["projects"]) == 2
            assert command("display-message", "-p", "-t", external_shell, "#{pane_pid}") == external_pid
            command("send-keys", "-t", external_shell, "exit", "Enter")
            wait_for(lambda: external_shell not in command("list-panes", "-s", "-t", "project-hub", "-F", "#{pane_id}").splitlines(), "exit left an orphan shell rail")
            command("send-keys", "-t", project_shell, "exit", "Enter")
            wait_for(lambda: project_shell not in command("list-panes", "-s", "-t", "project-hub", "-F", "#{pane_id}").splitlines(), "exiting project shell failed")
            key(b"\x1b3")
            wait_for(lambda: command("display-message", "-p", "-t", "project-hub:", "#{pane_id}") == home_shell, "shell slot did not renumber")
            before_prompt = len(output)
            key(b"\x17")
            wait_for(lambda: b"terminal is running." in output[before_prompt:], "shell close confirmation missing")
            key(b"\x1b")
            time.sleep(0.7)
            assert command("display-message", "-p", "-t", home_shell, "#{pane_pid}") == home_pid
            before_prompt = len(output)
            key(b"\x17")
            wait_for(lambda: b"close this workspace?" in output[before_prompt:], "shell close confirmation missing on retry")
            key(b"\r")
            wait_for(lambda: home_shell not in command("list-panes", "-s", "-t", "project-hub", "-F", "#{pane_id}").splitlines(), "confirmed shell close failed")
            key(b"\x1b1")
            wait_for(lambda: "new terminal" in screen() and "new codex session" not in screen(), "Dashboard unavailable after closing shells")
            key(b"q")
            process.wait(timeout=5)
            print("PASS: notification mute/unmute preserves live Codex and logs; native project/home/external terminals, preserved shells, exit and close confirmation; folder input, completion and safe removal; separated global actions; Codex notifications, sorting, menus, fixed rails and native input; no Codex API calls")
        finally:
            Path(args.capture).with_suffix(".pty").write_bytes(output)
            subprocess.run(tmux + ["kill-server"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)
            os.close(master)
            os.close(slave)


if __name__ == "__main__":
    main()
