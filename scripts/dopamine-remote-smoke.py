#!/usr/bin/env python3
"""Real OpenSSH, PTY, tmux and HTTP forwarding; disposable keys and projects."""
import argparse
import base64
import fcntl
import getpass
import json
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time
import urllib.error
import urllib.request


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/dopamine")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    payload = Path(binary).with_name("." + Path(binary).name + "-wrapped")
    if payload.is_file():
        binary = str(payload)
    client = str(Path(__file__).with_name("dopamine-remote.py").resolve())
    tools_needed = ("sshd", "ssh", "ssh-keygen", "tmux", "bash", "env")
    tools = {name: shutil.which(name) for name in tools_needed}
    if not all(tools.values()):
        raise RuntimeError(f"required tools missing: {tools}")
    with tempfile.TemporaryDirectory(prefix="dopamine-remote-smoke-") as temp:
        root = Path(temp)
        ssh_port, app_port, local_port = free_port(), free_port(), free_port()
        while len({ssh_port, app_port, local_port}) != 3:
            local_port = free_port()
            app_port = free_port()
        for name in ("host-key", "client-key"):
            subprocess.run([tools["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-f", str(root / name)], check=True)
        host_key = (root / "host-key.pub").read_text().split()
        (root / "known-hosts").write_text(f"[127.0.0.1]:{ssh_port} {' '.join(host_key[:2])}\n")
        sshd_config = root / "sshd.conf"
        sshd_config.write_text(f"""Port {ssh_port}
ListenAddress 127.0.0.1
HostKey {root / 'host-key'}
PidFile {root / 'sshd.pid'}
AuthorizedKeysFile {root / 'client-key.pub'}
AllowUsers {getpass.getuser()}
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
UsePAM no
PermitRootLogin prohibit-password
PrintMotd no
LogLevel ERROR
""")
        ssh_config = root / "ssh.conf"
        ssh_config.write_text(f"""Host fixture-home
  HostName 127.0.0.1
  Port {ssh_port}
  User {getpass.getuser()}
  IdentityFile {root / 'client-key'}
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking yes
  UserKnownHostsFile {root / 'known-hosts'}
  GlobalKnownHostsFile /dev/null
""")
        tmux = [tools["tmux"], "-S", str(root / "tmux.sock"), "-f", "/dev/null"]
        shims = root / "tools"
        shims.mkdir()
        (shims / "tmux").write_text("#!/bin/sh\nexec " + shlex.join(tmux) + ' "$@"\n')
        (shims / "tmux").chmod(0o755)
        # The browser fixture records the local URL. Network evidence comes
        # from real HTTP through SSH below, without launching a user profile.
        (shims / "firefox").write_text("#!" + sys.executable + "\nimport os,sys,time\nfrom pathlib import Path\n"
                                      "Path(" + repr(str(root / "firefox-args")) + ").write_text('\\n'.join(sys.argv[1:])+'\\n')\n"
                                      "Path(" + repr(str(root / "firefox-pid")) + ").write_text(str(os.getpid()))\n"
                                      "while True: time.sleep(1)\n")
        (shims / "firefox").chmod(0o755)
        (shims / "wl-paste").write_text("#!" + sys.executable + "\nimport sys\nfrom pathlib import Path\n"
                                       "root=Path(" + repr(str(root)) + ")\n"
                                       "if '--list-types' in sys.argv: print((root/'clipboard-type').read_text())\n"
                                       "else: sys.stdout.buffer.write((root/'clipboard-data').read_bytes())\n")
        (shims / "wl-paste").chmod(0o755)
        project = root / "project"
        project.mkdir()
        (project / "index.html").write_text("dopamine remote HTTP fixture")
        config = root / "config"
        config.mkdir()
        (config / "projects.json").write_text(json.dumps({"projects": [{
            "id": "fixture", "name": "Fixture", "path": str(project),
            "app_url": f"http://127.0.0.1:{app_port}/index.html?remote=1#test",
            "launch": [sys.executable, "-m", "http.server", str(app_port), "--bind", "127.0.0.1"]}]}))
        usage_cache = root / "home-cache/ai-usagebar/openai"
        usage_cache.mkdir(parents=True)
        (usage_cache / "usage.json").write_text(json.dumps({
            "rate_limit": {"primary_window": {"used_percent": 17, "limit_window_seconds": 604800}},
            "rate_limit_reset_credits": {"available_count": 1, "credits": [{
                "status": "available", "title": "Full reset fixture", "expires_at": "2099-12-12T12:00:00Z"}]},
        }))
        env = os.environ.copy()
        env.update(HOME=temp, XDG_STATE_HOME=str(root / "xdg-state"), XDG_CONFIG_HOME=str(root / "xdg-config"),
                   CODEX_HOME=str(root / "codex"), PATH=str(shims) + ":" + env["PATH"],
                   DOPAMINE_CONFIG_DIR=str(config), DOPAMINE_STATE_DIR=str(root / "state"),
                   DOPAMINE_PROJECTS_ROOT=str(root / "no-discover"), DOPAMINE_EXECUTABLE=binary,
                   TERM="xterm-256color", SHELL=tools["bash"])
        for key in ("TMUX", "TMUX_PANE", "DOPAMINE_TUI", "DOPAMINE_DASHBOARD"):
            env.pop(key, None)
        wrapper = root / "home-dopamine"
        assignments = [f"{k}={v}" for k, v in env.items() if k in (
            "HOME", "PATH", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "CODEX_HOME", "SHELL", "TERM",
            "DOPAMINE_CONFIG_DIR", "DOPAMINE_STATE_DIR", "DOPAMINE_PROJECTS_ROOT", "DOPAMINE_EXECUTABLE")]
        assignments.append(f"XDG_CACHE_HOME={root / 'home-cache'}")
        wrapper.write_text("#!/bin/sh\nunset TMUX TMUX_PANE DOPAMINE_TUI DOPAMINE_DASHBOARD\nexec " +
                           shlex.join([tools["env"], *assignments, binary]) + ' "$@"\n')
        wrapper.chmod(0o755)
        profile = root / "remote.json"
        base = [sys.executable, client, "--config", str(profile)]
        subprocess.run(base + ["setup", "fixture-home", "--ssh-config", str(ssh_config), "--command", str(wrapper)], env=env, check=True)
        sshd_log = (root / "sshd.log").open("w+")
        sshd = subprocess.Popen([tools["sshd"], "-D", "-e", "-f", str(sshd_config)], stdout=sshd_log, stderr=sshd_log)
        clients = []
        terminal_output = bytearray()
        tunnel = None

        def command(*parts):
            return subprocess.check_output(tmux + list(parts), env=env, text=True, stderr=subprocess.DEVNULL).strip()

        def wait_for(check, description, timeout=20):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                for process, master in clients:
                    while select.select([master], [], [], 0)[0]:
                        try:
                            terminal_output.extend(os.read(master, 65536))
                        except OSError:
                            break
                try:
                    if check():
                        return
                except (OSError, subprocess.CalledProcessError, urllib.error.URLError, StopIteration):
                    pass
                if sshd.poll() is not None:
                    sshd_log.seek(0)
                    raise AssertionError("sshd exited: " + sshd_log.read())
                time.sleep(0.05)
            try:
                screen = command("capture-pane", "-p", "-t", "dopamine:")
            except subprocess.CalledProcessError:
                screen = "no dashboard"
            raise AssertionError(description + "\n" + screen + "\nPTY: " + terminal_output.decode(errors="replace")[-4000:])

        def accepting():
            with socket.create_connection(("127.0.0.1", ssh_port), timeout=0.1):
                return True

        def connect():
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
            # Give the SSH child a controlling terminal, as in a real terminal.
            process = subprocess.Popen([tools["bash"], "-c", 'exec "$@"', "remote", *base, "connect"],
                                       stdin=slave, stdout=slave, stderr=slave, env=env,
                                       start_new_session=True)
            os.close(slave)
            clients.append((process, master))
            return process, master

        try:
            wait_for(accepting, "isolated SSH server did not listen")
            subprocess.run(base + ["check"], env=env, check=True, timeout=30)
            first, first_pty = connect()
            wait_for(lambda: "Fixture" in command("capture-pane", "-p", "-t", "dopamine:"), "home dashboard did not open through SSH")
            wait_for(lambda: bool(command("list-clients", "-F", "#{client_session}")), "SSH client did not attach to the dashboard")
            wait_for(lambda: b"7d 17% used" in terminal_output and b"1 reset available" in terminal_output,
                     "home usage and reset count did not reach the SSH terminal")
            os.write(first_pty, b"u")
            wait_for(lambda: b"Full reset fixture" in terminal_output and b"12 Dec 2099" in terminal_output,
                     "reset title and expiry did not reach the SSH terminal")
            os.write(first_pty, b"u")
            wait_for(lambda: "> Fixture" in command("capture-pane", "-p", "-t", "dopamine:"), "usage view did not return to projects")
            os.write(first_pty, b"\r")
            wait_for(lambda: "start server" in command("capture-pane", "-p", "-t", "dopamine:"), "remote project menu did not open")
            os.write(first_pty, b"s")
            record_path = root / "state/runs/app-fixture.json"
            wait_for(record_path.exists, "remote dev server did not start")
            wait_for(lambda: "server started" in command("capture-pane", "-p", "-t", "dopamine:"), "server start did not finish")
            app_record = json.loads(record_path.read_text())
            os.write(first_pty, b"t")
            wait_for(lambda: any((root / "state/runs").glob("shell-*.json")), "remote shell did not open")
            shell = json.loads(next((root / "state/runs").glob("shell-*.json")).read_text())
            pane = shell["Pane"]
            wait_for(lambda: command("display-message", "-p", "-t", "dopamine:", "#{pane_id}") == pane
                     and "$" in command("capture-pane", "-p", "-t", pane), "remote shell was not ready for input")
            pid = command("display-message", "-p", "-t", pane, "#{pane_pid}")
            marker = root / "session-marker"
            os.write(first_pty, ("printf persisted > " + shlex.quote(str(marker)) + "\r").encode())
            wait_for(marker.exists, "remote SSH keyboard input did not reach the shell")
            # Terminate the actual SSH client. Python is its foreground parent;
            # use the parent's child, without killing the detached home tmux.
            children = Path(f"/proc/{first.pid}/task/{first.pid}/children").read_text().split()
            assert len(children) == 1, children
            os.kill(int(children[0]), signal.SIGTERM)
            first.wait(timeout=10)
            wait_for(lambda: not command("list-clients", "-F", "#{client_session}"), "SSH disconnect did not detach")
            assert command("display-message", "-p", "-t", pane, "#{pane_pid}") == pid
            second, second_pty = connect()
            wait_for(lambda: bool(command("list-clients", "-F", "#{client_session}")), "SSH reconnect did not attach")
            os.write(second_pty, b"\x1b2")
            wait_for(lambda: command("display-message", "-p", "-t", "dopamine:", "#{pane_id}") == pane, "reconnect could not return to the existing shell")
            assert command("display-message", "-p", "-t", pane, "#{pane_pid}") == pid
            assert json.loads(record_path.read_text())["Token"] == app_record["Token"]

            # A mouse-aware app (like Codex) must not consume SSH selections
            # and copy on the home desktop. Exercise real SGR mouse input;
            # the resulting OSC 52 must travel back through SSH to this PTY.
            mouse_app = root / "mouse-app.py"
            copied_text = "RemoteClipboardЖ"
            mouse_app.write_text("import os,tty\nfrom pathlib import Path\n"
                                 "tty.setraw(0)\n"
                                 "print('\\033[?1000h\\033[?1002h\\033[?1006h\\033[?2004h\\033[2J\\033[H' + " + repr(copied_text) + ", end='', flush=True)\n"
                                 "with Path(" + repr(str(root / "paste-input")) + ").open('ab', buffering=0) as output:\n"
                                 " while True:\n"
                                 "  data=os.read(0, 1)\n"
                                 "  if data == b'\\x04': break\n"
                                 "  output.write(data)\n"
                                 "print('\\033[?1000l\\033[?1002l\\033[?1006l', end='', flush=True)\n")
            os.write(second_pty, (shlex.join([sys.executable, str(mouse_app)]) + "\r").encode())
            wait_for(lambda: command("capture-pane", "-p", "-t", pane).startswith(copied_text), "mouse-aware app not ready")
            left, top = map(int, command("display-message", "-p", "-t", pane, "#{pane_left} #{pane_top}").split())

            def mouse(code, x, y, release=False):
                os.write(second_pty, f"\x1b[<{code};{left + x + 1};{top + y + 1}{'m' if release else 'M'}".encode())

            def clipboard_arrived(start, expected):
                data = bytes(terminal_output[start:])
                return b"\x1b]52;" in data and base64.b64encode(expected.encode()) in data

            start = len(terminal_output)
            mouse(0, 0, 0)
            mouse(32, len(copied_text), 0)
            time.sleep(0.15)
            mouse(0, len(copied_text), 0, release=True)
            wait_for(lambda: clipboard_arrived(start, copied_text), "mouse selection did not send UTF-8 OSC 52 through SSH")
            assert command("show-buffer") == copied_text
            time.sleep(0.5)
            for clicks in (2, 3):
                start = len(terminal_output)
                for _ in range(clicks):
                    mouse(0, 3, 0)
                    mouse(0, 3, 0, release=True)
                    time.sleep(0.05)
                wait_for(lambda: clipboard_arrived(start, copied_text), f"{clicks}-click selection did not reach SSH clipboard")
                time.sleep(0.5)
            # Model Codex's bracketed-paste input without making model calls.
            command("set-option", "-p", "-t", pane, "@dopamine_kind", "codex")
            text_payload = "текст с ноутбука\nsecond line\n"
            (root / "clipboard-type").write_text("text/plain;charset=utf-8")
            (root / "clipboard-data").write_text(text_payload)
            subprocess.run(base + ["paste"], env=env, check=True, timeout=30)
            wait_for(lambda: text_payload.encode() in (root / "paste-input").read_bytes(), "laptop text clipboard did not reach Codex pane")
            image_data = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNgYGAAAAAEAAH2FzhVAAAAAElFTkSuQmCC")
            (root / "clipboard-type").write_text("image/png")
            (root / "clipboard-data").write_bytes(image_data)
            subprocess.run(base + ["paste"], env=env, check=True, timeout=30)
            uploaded = list((root / "state/remote-uploads").glob("*.png"))
            assert len(uploaded) == 1 and uploaded[0].read_bytes() == image_data
            wait_for(lambda: str(uploaded[0]).encode() in (root / "paste-input").read_bytes(), "laptop image path did not reach Codex pane")
            received = (root / "paste-input").read_bytes()
            assert b"\x1b[200~" in received and b"\x1b[201~" in received, received
            assert b"\r" not in received, "paste must never submit Enter"
            command("set-option", "-p", "-t", pane, "@dopamine_kind", "shell")
            rejected = subprocess.run(base + ["paste"], env=env, capture_output=True, text=True, timeout=30)
            assert rejected.returncode == 1 and "select a running Codex tab" in rejected.stderr
            os.write(second_pty, b"\x04")
            # Restore shell terminal modes changed by the disposable mouse app.
            os.write(second_pty, b"stty sane\r")

            tunnel_log = (root / "tunnel.log").open("w+")
            tunnel = subprocess.Popen(base + ["open", "fixture", "--local-port", str(local_port)], env=env,
                                      stdout=tunnel_log, stderr=tunnel_log, start_new_session=True)
            local_url = f"http://127.0.0.1:{local_port}/index.html?remote=1#test"
            wait_for(lambda: (root / "firefox-args").exists(), "Firefox was not handed the forwarded local URL")
            assert (root / "firefox-args").read_text().splitlines() == ["--new-tab", local_url]
            with urllib.request.urlopen(local_url, timeout=5) as response:
                assert response.read() == b"dopamine remote HTTP fixture"
            assert tunnel.poll() is None
            os.killpg(tunnel.pid, signal.SIGINT)
            tunnel.wait(timeout=10)
            browser_pid = int((root / "firefox-pid").read_text())
            os.kill(browser_pid, 0)
            with socket.socket() as listener:
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(("127.0.0.1", local_port))
            tunnel = subprocess.Popen(base + ["open", str(app_port), "--local-port", str(local_port), "--no-browser"], env=env,
                                      stdout=tunnel_log, stderr=tunnel_log, start_new_session=True)

            def forwarded():
                with urllib.request.urlopen(local_url, timeout=0.2) as response:
                    return response.read() == b"dopamine remote HTTP fixture"

            wait_for(forwarded, "the same dev-server port could not be reopened after HTTP and tunnel close")
            occupied = subprocess.run(base + ["open", "fixture", "--local-port", str(local_port), "--no-browser"],
                                      env=env, capture_output=True, text=True, timeout=30)
            assert occupied.returncode == 1 and "unavailable" in occupied.stderr, occupied.stderr
            os.killpg(tunnel.pid, signal.SIGINT)
            tunnel.wait(timeout=10)
            assert command("display-message", "-p", "-t", pane, "#{pane_pid}") == pid
            tunnel = subprocess.Popen(base + ["open", f"http://127.0.0.1:{app_port}/index.html?direct=1#url", "--local-port", str(local_port), "--no-browser"],
                                      env=env, stdout=tunnel_log, stderr=tunnel_log, start_new_session=True)
            wait_for(forwarded, "arbitrary URL did not forward without a project ID")
            os.killpg(tunnel.pid, signal.SIGINT)
            tunnel.wait(timeout=10)
            print("remote SSH scenario passed: real TUI input, server start, reconnect with same shell PID, mouse/word/line copy over OSC 52, UTF-8 and PNG clipboard transfer without Enter, HTTP forwarding by project/port/URL, Firefox survives Ctrl+C, tunnel cleanup/reopen and occupied-port rejection")
        finally:
            for process, master in clients:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    process.wait(timeout=5)
                os.close(master)
            if tunnel is not None and tunnel.poll() is None:
                os.killpg(tunnel.pid, signal.SIGINT)
                tunnel.wait(timeout=10)
            if (root / "firefox-pid").exists():
                try:
                    os.kill(int((root / "firefox-pid").read_text()), signal.SIGTERM)
                except ProcessLookupError:
                    pass
            # tmux returns from kill-server before its shells finish writing
            # history. Wait for these exact fixture processes before removing
            # their temporary HOME; otherwise cleanup races with .bash_history.
            pane_pids = subprocess.run(tmux + ["list-panes", "-a", "-F", "#{pane_pid}"], env=env,
                                       capture_output=True, text=True).stdout.split()
            subprocess.run(tmux + ["kill-server"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            def alive(pid):
                try:
                    return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[0] != "Z"
                except FileNotFoundError:
                    return False

            deadline = time.monotonic() + 5
            while any(alive(pid) for pid in pane_pids) and time.monotonic() < deadline:
                time.sleep(0.05)
            sshd.terminate()
            sshd.wait(timeout=5)
            sshd_log.close()
            assert not any(alive(pid) for pid in pane_pids), "fixture tmux processes did not exit"


if __name__ == "__main__":
    main()
