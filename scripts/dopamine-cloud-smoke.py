#!/usr/bin/env python3
"""Real TUI/tmux/HTTP; fixture cloud CLI and systemd manager, no model calls."""
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
import struct
import subprocess
import sys
import tempfile
import termios
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='bin/dopamine')
    parser.add_argument('--capture-dir', default='/tmp/dopamine-cloud-captures')
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    payload = Path(binary).with_name('.' + Path(binary).name + '-wrapped')
    if payload.is_file():
        binary = str(payload)
    capture = Path(args.capture_dir)
    capture.mkdir(parents=True, exist_ok=True)
    real_tmux = shutil.which('tmux')
    with tempfile.TemporaryDirectory(prefix='dopamine-cloud-smoke-') as temp:
        root = Path(temp)
        tools = root / 'tools'
        tools.mkdir()
        project = root / 'project'
        project.mkdir()
        config = root / 'config'
        config.mkdir()
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        url = f'http://127.0.0.1:{port}/'
        registry = config / 'projects.json'
        registry.write_text(json.dumps({'extra': 'keep', 'projects': [{'id': 'fixture', 'name': 'Fixture', 'path': str(project), 'extra': 'keep',
            'launch': [sys.executable, '-m', 'http.server', str(port), '--bind', '127.0.0.1'], 'app_url': url}]}))
        tmux = [real_tmux, '-S', str(root / 'tmux.sock'), '-f', '/dev/null']
        (tools / 'tmux').write_text('#!/bin/sh\nexec ' + shlex.join(tmux) + ' "$@"\n')
        (tools / 'tmux').chmod(0o755)
        (tools / 'xdg-open').write_text('#!/bin/sh\nprintf "%s\\n" "$1" > ' + shlex.quote(str(root / 'opened')) + '\n')
        (tools / 'xdg-open').chmod(0o755)
        cloud = root / 'cloud.py'
        cloud.write_text('''import json, os, sys
from pathlib import Path
root = Path(os.environ['HOME'])
args=sys.argv[1:]
with (root/'cloud-calls.jsonl').open('a') as f: f.write(json.dumps(args)+'\\n')
if args[1] == 'exec': print('https://chatgpt.com/codex/tasks/task_e_fixture')
elif args[1] == 'status':
 state=(root/'cloud-status').read_text().strip() if (root/'cloud-status').exists() else 'PENDING'
 print('['+state+'] cloud smoke fixture\\nfixture environment · now\\n+2/-1 · 1 file')
 sys.exit(0 if state=='READY' else 1)
elif args[1] == 'diff': print('diff --git a/hello b/hello\\n+fixture change')
''')
        shim = tools / 'dopamine'
        shim.write_text('#!/bin/sh\nif [ "${1:-}" = exec-cloud ]; then shift; exec ' + shlex.join([sys.executable, str(cloud)]) + ' "$@"; fi\nexec ' + shlex.quote(binary) + ' "$@"\n')
        shim.chmod(0o755)
        manager = tools / 'systemctl'
        manager.write_text('#!' + sys.executable + '''
import json,os,signal,subprocess,sys
from pathlib import Path
root=Path(os.environ['HOME']);args=sys.argv[1:]
with (root/'systemctl-calls.jsonl').open('a') as f:f.write(json.dumps(args)+'\\n')
state=root/'manager-state';current=json.loads(state.read_text()) if state.exists() else {'state':'inactive'}
if 'start' in args:
 if current['state']!='active':
  with (root/'service.log').open('ab') as log:
   p=subprocess.Popen([os.environ['DOPAMINE_EXECUTABLE'],'serve-project','fixture'],stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
  current={'state':'active','pid':p.pid};state.write_text(json.dumps(current))
elif 'stop' in args:
 if current.get('pid'):
  try:os.killpg(current['pid'],signal.SIGTERM)
  except ProcessLookupError:pass
 current={'state':'inactive'};state.write_text(json.dumps(current))
elif 'show' in args:print('ActiveState='+current['state']+'\\nSubState=running\\nResult=success')
''')
        manager.chmod(0o755)
        env = os.environ.copy()
        env.update(HOME=temp, XDG_CONFIG_HOME=str(root / 'xdg-config'), XDG_STATE_HOME=str(root / 'xdg-state'),
            DOPAMINE_EXECUTABLE=str(shim), DOPAMINE_CONFIG_DIR=str(config), DOPAMINE_STATE_DIR=str(root / 'state'),
            DOPAMINE_PROJECTS_ROOT=str(root / 'no-discovery'), PATH=str(tools) + ':' + env['PATH'], TERM='xterm-256color', SHELL=shutil.which('bash'))
        for name in ['TMUX', 'TMUX_PANE', 'DOPAMINE_TUI', 'DOPAMINE_DASHBOARD']:
            env.pop(name, None)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 30, 100, 0, 0))
        process = None
        output = bytearray()

        def drain():
            while select.select([master], [], [], 0)[0]:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break

        def command(*args):
            return subprocess.check_output(tmux + list(args), env=env, text=True, stderr=subprocess.DEVNULL).strip()

        def screen():
            return command('capture-pane', '-p', '-t', 'dopamine:')

        def wait(check, message, timeout=15):
            end = time.monotonic() + timeout
            while time.monotonic() < end:
                drain()
                try:
                    if check():
                        return
                except (OSError, subprocess.CalledProcessError, urllib.error.URLError):
                    pass
                time.sleep(.1)
            raise AssertionError(message + '\n' + screen())

        def key(text):
            os.write(master, text)

        def healthy():
            with urllib.request.urlopen(url, timeout=.3) as response:
                return response.status == 200

        def capture_screen(name):
            (capture / (name + '.txt')).write_text(command('capture-pane', '-p', '-e', '-t', 'dopamine:') + '\n')
            # Include the functional rail in the same capture.
            panes = command('list-panes', '-t', 'dopamine:', '-F', '#{pane_id}|#{@dopamine_role}').splitlines()
            rails = [line.split('|')[0] for line in panes if line.endswith('|dopamine-rail')]
            if rails:
                (capture / (name + '-rail.txt')).write_text(command('capture-pane', '-p', '-e', '-t', rails[0]) + '\n')

        def open_menu():
            wait(lambda: '> Fixture' in command('capture-pane', '-p', '-t', dashboard), 'workspace opening did not finish on dashboard')
            command('select-window', '-t', dashboard)
            command('select-pane', '-t', dashboard)
            wait(lambda: '> Fixture' in screen(), 'dashboard unavailable')
            key(b'\r')
            wait(lambda: 'new codex session' in screen(), 'project menu unavailable')

        def set_mode(index):
            key(b'a')
            wait(lambda: 'server autostart · Fixture' in screen(), 'autostart settings missing')
            capture_screen('autostart')
            # Start at the current selection, so choose via Home-like up sequence.
            key(b'k' * 3 + b'j' * index + b'\r')

        try:
            process = subprocess.Popen([str(shim)], env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
            wait(lambda: 'dopamine' in command('list-clients', '-F', '#{client_session}') and not (termios.tcgetattr(slave)[3] & termios.ICANON), 'client not ready for input')
            wait(lambda: '> Fixture' in screen(), 'dashboard missing')
            dashboard = command('display-message', '-p', '-t', 'dopamine:', '#{pane_id}')
            open_menu()
            key(b'c')
            wait(lambda: 'codex cloud · Fixture' in screen(), 'cloud composer missing')
            cloud_pane = command('display-message', '-p', '-t', 'dopamine:', '#{pane_id}')
            capture_screen('cloud-compose')
            key(b'fixture environment\r')
            key(b'add a fixture change')
            wait(lambda: 'add a fixture change' in screen(), 'task text was not entered')
            wait(lambda: 'close cloud tab?' in command('list-keys', '-T', 'dopamine-root'), 'workspace shortcuts not ready')
            before = len(output)
            key(b'\x17')
            wait(lambda: b'close cloud tab?' in output[before:], 'cloud draft close had no confirmation')
            key(b'\x1b')
            time.sleep(.7)  # Let tmux resolve a standalone Escape before Ctrl+s.
            key(b'\x13')
            wait(lambda: 'working · queued or running' in screen(), 'pending exit1 was treated as error')
            cloud_files = list((root / 'state/cloud').glob('*.json'))
            assert len(cloud_files) == 1
            saved = json.loads(cloud_files[0].read_text())
            assert saved['ID'] == 'task_e_fixture' and saved['Prompt'] == 'add a fixture change', saved
            rails = command('list-panes', '-t', cloud_pane, '-F', '#{pane_id}|#{@dopamine_role}').splitlines()
            rail = next(line.split('|')[0] for line in rails if line.endswith('|dopamine-rail'))
            wait(lambda: '⢔⣊⣑⡢' in command('capture-pane', '-p', '-t', rail) and '2~' in command('capture-pane', '-p', '-t', rail), 'cloud icon or working marker missing')
            rows = command('capture-pane', '-p', '-t', rail).splitlines()
            icon = next(i for i, line in enumerate(rows) if '⢔⣊⣑⡢' in line)
            assert '2~' in rows[icon-1], rows
            capture_screen('cloud-working')
            (root / 'cloud-status').write_text('READY')
            key(b'r')
            wait(lambda: 'completed · ready to review' in screen(), 'completion status missing')
            wait(lambda: '2✓' in command('capture-pane', '-p', '-t', rail), 'completed marker missing')
            capture_screen('cloud-ready')
            key(b'd')
            wait(lambda: '+fixture change' in screen(), 'cloud diff missing')
            key(b'\x1b')
            wait(lambda: 'completed · ready to review' in screen(), 'diff did not close')
            key(b'o')
            wait(lambda: (root / 'opened').exists(), 'cloud result did not open')
            assert (root / 'opened').read_text().strip() == saved['URL']
            open_menu()
            key(b'j' * 6 + b'\r')
            wait(lambda: 'cloud tasks · Fixture' in screen(), 'cloud history missing')
            key(b'\r')
            wait(lambda: command('display-message', '-p', '-t', 'dopamine:', '#{pane_id}') == cloud_pane, 'history duplicated live cloud tab')
            # Close the task's tab, then restore it from persisted history.
            before = len(output)
            key(b'\x17')
            wait(lambda: b'close cloud tab?' in output[before:], 'saved cloud close had no confirmation')
            key(b'\r')
            wait(lambda: cloud_pane not in command('list-panes', '-s', '-t', 'dopamine', '-F', '#{pane_id}').splitlines(), 'cloud tab did not close')
            open_menu()
            key(b'j' * 6 + b'\r')
            wait(lambda: 'cloud tasks · Fixture' in screen(), 'saved history unavailable')
            key(b'\r')
            wait(lambda: 'completed · ready to review' in screen(), 'saved cloud task did not restore')
            open_menu()
            # Save mode when Dopamine opens, exit and reconnect to trigger it.
            set_mode(1)
            wait(lambda: 'server autostart: when dopamine opens' in screen(), 'mode not saved')
            assert registry.read_text().count('"extra": "keep"') == 2
            key(b'\x1b')
            wait(lambda: '> Fixture' in screen(), 'project menu did not close')
            key(b'q')
            process.wait(timeout=5)
            process = subprocess.Popen([str(shim)], env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
            wait(healthy, 'server did not autostart when dopamine opened')
            wait(lambda: 'dopamine' in command('list-clients', '-F', '#{client_session}') and not (termios.tcgetattr(slave)[3] & termios.ICANON), 'reconnected client not ready')
            dashboard = command('display-message', '-p', '-t', 'dopamine:', '#{pane_id}')
            open_menu()
            key(b'x')
            wait(lambda: 'server stopped · Fixture' in screen(), 'autostarted server stop failed')
            # Current mode=1, k*3 wraps back to 1; choose 2 with one j.
            key(b'a')
            wait(lambda: 'server autostart · Fixture' in screen(), 'settings missing')
            key(b'j\r')
            wait(lambda: 'server autostart: at login' in screen(), 'login mode not saved')
            units = list((root / 'xdg-config/systemd/user').glob('dopamine-server-*.service'))
            assert len(units) == 1 and 'WantedBy=default.target' in units[0].read_text()
            key(b's')
            wait(healthy, 'foreground login service server did not start')
            wait(lambda: 'server started · Fixture' in screen(), 'service start UI missing')
            key(b'x')
            wait(lambda: 'server stopped · Fixture' in screen(), 'systemd-managed server did not stop')
            key(b'a')
            wait(lambda: 'server autostart · Fixture' in screen(), 'settings missing')
            key(b'j\r')  # login -> off
            wait(lambda: 'server autostart: off' in screen(), 'off mode not saved')
            calls = [json.loads(line) for line in (root / 'cloud-calls.jsonl').read_text().splitlines()]
            assert sum(call[1] == 'exec' for call in calls) == 1, calls
            assert json.loads(cloud_files[0].read_text())['ID'] == 'task_e_fixture'
            print('PASS: cloud composer, protected draft, single submission, pending/ready states, cloud rail, result/diff/history, both autostart modes, real HTTP foreground server and stop; fixture cloud/systemd, no model calls')
        finally:
            (capture / 'session.pty').write_bytes(output)
            subprocess.run([str(manager), '--user', 'stop', 'fixture'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            subprocess.run(tmux + ['kill-server'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)
            os.close(master)
            os.close(slave)


if __name__ == '__main__':
    main()
