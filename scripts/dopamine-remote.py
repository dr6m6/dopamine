#!/usr/bin/env python3
"""Use the home dopamine over OpenSSH; Python standard library only."""
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit, urlunsplit


def config_root():
    return Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config")) / "dopamine"


def atomic_write(path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, name = tempfile.mkstemp(prefix=".remote-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            os.fchmod(stream.fileno(), mode)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def port_number(value):
    try:
        port = int(value)
    except (TypeError, ValueError):
        raise ValueError("port must be an integer from 1 to 65535") from None
    if str(port) != str(value) or not 1 <= port <= 65535:
        raise ValueError("port must be an integer from 1 to 65535")
    return port


def validate(profile):
    if not isinstance(profile, dict):
        raise ValueError("remote.json must contain an object")
    destination = profile.get("destination", "")
    if not isinstance(destination, str) or not re.fullmatch(r"(?:[A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9_.:-]*", destination):
        raise ValueError("destination must be [user@]hostname or [user@]IP (SSH aliases are supported)")
    host = destination.rsplit("@", 1)[-1]
    if ":" in host:
        ipaddress.IPv6Address(host)
    if profile.get("port") is not None:
        profile["port"] = port_number(profile["port"])
    for key in ("identity", "ssh_config", "command"):
        value = profile.get(key)
        if value is not None and (not isinstance(value, str) or not value or any(ord(c) < 32 or ord(c) == 127 for c in value)):
            raise ValueError(f"invalid {key}")
    command = profile.get("command")
    if command and not (command.startswith(("/", "~/")) or re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]*", command)):
        raise ValueError("command must be an executable name, absolute path or ~/path; arguments are not accepted")
    return profile


def load_profile(path):
    try:
        return validate(json.loads(path.read_text()))
    except FileNotFoundError:
        raise ValueError("no connection saved; run dopamine-remote setup user@home-address") from None


def ssh_base(profile, control=None):
    ssh = shutil.which("ssh")
    if not ssh:
        raise ValueError("OpenSSH client is required (Void: sudo xbps-install -S openssh)")
    args = [ssh]
    if profile.get("ssh_config"):
        args += ["-F", profile["ssh_config"]]
    if profile.get("port") is not None:
        args += ["-p", str(profile["port"])]
    args += ["-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
             "-o", "ConnectTimeout=15", "-o", "ForwardAgent=no", "-o", "ForwardX11=no",
             "-o", "PermitLocalCommand=no", "-o", "ExitOnForwardFailure=yes",
             "-o", "ControlPersist=no", "-o", "ControlMaster=" + ("yes" if control else "no"),
             "-S", str(control) if control else "none"]
    if profile.get("identity"):
        args += ["-i", profile["identity"], "-o", "IdentitiesOnly=yes"]
    return args


def remote_command(profile, *args):
    suffix = shlex.join(args)
    command = profile.get("command")
    if command:
        executable = '"$HOME"/' + shlex.quote(command[2:]) if command.startswith("~/") else shlex.quote(command)
        return f"exec {executable} {suffix}".rstrip()
    # Non-interactive SSH shells often omit ~/.local/bin from PATH.
    return ('if [ -x "$HOME/.local/bin/dopamine" ]; then '
            f'exec "$HOME/.local/bin/dopamine" {suffix}; else exec dopamine {suffix}; fi')


def remote_run(profile, *args):
    result = subprocess.run(ssh_base(profile) + ["-T", profile["destination"], remote_command(profile, *args)],
                            stdout=subprocess.PIPE, text=True, timeout=300)
    if result.returncode:
        raise ValueError(f"SSH command failed (exit {result.returncode}); check Tailscale, SSH key and the home dopamine installation")
    return result.stdout


def projects(profile):
    data = json.loads(remote_run(profile, "projects"))
    if not isinstance(data, dict) or not isinstance(data.get("projects"), list):
        raise ValueError("home dopamine returned an invalid project registry")
    for project in data["projects"]:
        if not isinstance(project, dict) or not isinstance(project.get("id"), str):
            raise ValueError("home dopamine returned an invalid project")
    return data["projects"]


def display(value):
    return "".join(c if c.isprintable() else "?" for c in str(value))


def app_target(raw, local_port=None):
    if not isinstance(raw, str) or not raw or any(ord(c) < 33 or ord(c) == 127 for c in raw):
        raise ValueError("project has no valid app_url")
    url = urlsplit(raw)
    if url.scheme not in ("http", "https") or url.username is not None or url.password is not None:
        raise ValueError("app_url must be local HTTP(S), without credentials")
    port = url.port if url.port is not None else (443 if url.scheme == "https" else 80)
    port = port_number(port)
    host = url.hostname
    if host not in ("localhost", "127.0.0.1", "::1") and (host, port) != ("10.203.31.1", 8420):
        raise ValueError("app_url must use loopback or the existing CUTTER VPN relay")
    local_port = port_number(local_port) if local_port is not None else port
    remote_host = f"[{host}]" if ":" in host else host
    forward = f"127.0.0.1:{local_port}:{remote_host}:{port}"
    local_url = urlunsplit((url.scheme, f"127.0.0.1:{local_port}", url.path, url.query, url.fragment))
    return local_port, forward, local_url


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def open_project(profile, project_id, local_port=None, browser=True):
    matches = [p for p in projects(profile) if p["id"] == project_id]
    if len(matches) != 1:
        raise ValueError("project not found or ambiguous; run dopamine-remote projects")
    port, forward, url = app_target(matches[0].get("app_url"), local_port)
    firefox = shutil.which("firefox") if browser else None
    if browser and not firefox:
        raise ValueError("Firefox is required; use --no-browser to open the printed URL yourself")
    # Diagnose an occupied port before SSH. OpenSSH also checks the bind itself,
    # covering races; never fall back to somebody else's existing listener.
    with socket.socket() as check:
        check.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            check.bind(("127.0.0.1", port))
        except OSError:
            raise ValueError(f"local port {port} is unavailable; choose --local-port PORT") from None
    with tempfile.TemporaryDirectory(prefix="dopamine-ssh-") as temp:
        control = Path(temp) / "s"
        args = ssh_base(profile, control) + ["-N", "-T", "-L", forward, profile["destination"]]
        process = subprocess.Popen(args)
        try:
            deadline = time.monotonic() + 300
            while not control.exists():
                if process.poll() is not None:
                    raise ValueError(f"SSH tunnel failed (exit {process.returncode})")
                if time.monotonic() >= deadline:
                    raise ValueError("SSH tunnel did not become ready")
                time.sleep(0.05)
            # The mux socket is installed after authentication and successful
            # local forwarding. Check the actual master before opening Firefox.
            subprocess.run([shutil.which("ssh"), "-S", str(control), "-O", "check", profile["destination"]],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
            print(f"{url}\nTunnel active. Keep this terminal open; Ctrl+C closes only this tunnel.", flush=True)
            if browser:
                browser_process = subprocess.Popen([firefox, "--new-tab", url], start_new_session=True)
                try:
                    code = browser_process.wait(timeout=0.3)
                    if code:
                        raise ValueError(f"Firefox launch failed (exit {code})")
                except subprocess.TimeoutExpired:
                    pass  # A new Firefox process may own its window for hours.
            code = process.wait()
            if code:
                raise ValueError(f"SSH tunnel disconnected (exit {code}); run open again to reconnect")
        finally:
            stop_process(process)


def install(uninstall=False):
    state = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")) / "dopamine/remote-install.json"
    launcher = Path.home() / ".local/bin/dopamine-remote"
    receipt = json.loads(state.read_text()) if state.exists() else None
    if launcher.exists() or launcher.is_symlink():
        if (launcher.is_symlink() or not receipt or receipt.get("sha256") != hashlib.sha256(launcher.read_bytes()).hexdigest()):
            raise ValueError(f"refusing to overwrite a changed or unowned launcher: {launcher}")
    if uninstall:
        if receipt:
            launcher.unlink(missing_ok=True)
            state.unlink()
        print("Remote client removed; connection settings and home sessions preserved.")
        return
    data = Path(__file__).read_bytes()
    atomic_write(launcher, data, 0o755)
    atomic_write(state, (json.dumps({"sha256": hashlib.sha256(data).hexdigest()}) + "\n").encode())
    print(f"Installed: {launcher}\nAdd ~/.local/bin to PATH if needed. No Nix, Go or local Codex installation is required.")


def main(argv=None):
    parser = argparse.ArgumentParser(prog="dopamine-remote", description=__doc__)
    parser.add_argument("--config", type=Path, default=config_root() / "remote.json", help="connection settings file")
    commands = parser.add_subparsers(dest="action")
    setup = commands.add_parser("setup", help="save a connection; never modifies SSH configuration or keys")
    setup.add_argument("destination", help="[user@]hostname, Tailscale IP, or existing SSH alias")
    setup.add_argument("--port", type=int, help="override the port from SSH config (default: OpenSSH's default)")
    setup.add_argument("--identity", type=Path)
    setup.add_argument("--ssh-config", type=Path, help="optional OpenSSH config file")
    setup.add_argument("--command", help="home executable path; default: ~/.local/bin/dopamine, then PATH")
    commands.add_parser("connect", help="attach to home dopamine; repeat after a disconnect (default)")
    commands.add_parser("check", help="check SSH, home dopamine and its project registry")
    commands.add_parser("projects", help="list home project IDs and dev-server URLs")
    opening = commands.add_parser("open", help="forward a project's dev server and open it in local Firefox")
    opening.add_argument("project", help="project ID from projects")
    opening.add_argument("--local-port", type=int)
    opening.add_argument("--no-browser", action="store_true")
    installing = commands.add_parser("install", help="install this standalone client into ~/.local/bin")
    installing.add_argument("--uninstall", action="store_true")
    args = parser.parse_args(argv)
    if args.action == "install":
        install(args.uninstall)
        return 0
    if args.action == "setup":
        profile = {"destination": args.destination}
        if args.port is not None:
            profile["port"] = args.port
        for key in ("identity", "ssh_config", "command"):
            value = getattr(args, key)
            if value is not None:
                profile[key] = str(value.expanduser().resolve()) if isinstance(value, Path) else value
        validate(profile)
        if args.config.exists():
            existing = json.loads(args.config.read_text())
            if not isinstance(existing, dict):
                raise ValueError("remote.json must contain an object")
            # Preserve fields owned by a future version or another tool.
            existing.update(profile)
            profile = validate(existing)
        atomic_write(args.config, (json.dumps(profile, indent=2) + "\n").encode())
        print(f"Saved: {args.config}\nNext: dopamine-remote check, then dopamine-remote connect")
        return 0
    profile = load_profile(args.config)
    if args.action == "check":
        print(display(remote_run(profile, "--version").strip()))
        print(f"SSH and project registry available: {len(projects(profile))} projects. Ready to connect.")
    elif args.action == "projects":
        for project in projects(profile):
            print(display(project["id"]) + "\t" + display(project.get("app_url", "")))
    elif args.action == "open":
        open_project(profile, args.project, args.local_port, not args.no_browser)
    else:
        print("Connecting to home dopamine. Re-run this command after a disconnect.\n"
              "Dev sites on this laptop: dopamine-remote open PROJECT (in another terminal).", flush=True)
        code = subprocess.call(ssh_base(profile) + ["-t", profile["destination"], remote_command(profile)])
        if code:
            print(f"SSH ended (exit {code}); home tmux sessions are retained while the PC stays running.", file=sys.stderr)
        return code
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"dopamine-remote: {error}", file=sys.stderr)
        sys.exit(1)
