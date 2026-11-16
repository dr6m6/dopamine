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
        native = root / 'native.py'
        native.write_text(r"""import os,sys,tty,json
from pathlib import Path
root=Path(os.environ['HOME']);tty.setraw(sys.stdin.fileno())
host='Local';state='Ready';used='';text='';overlay=False
uuid='11111111-2222-3333-4444-555555555555'
def paint(body=''):
 title='codex | '+state+' | '+uuid+used
 screen='\x1b]0;'+title+'\a\x1b[2J\x1b[H>_ OpenAI Codex (native fixture)\r\n\r\n'+body+'\r\n\r\n'+host+'  alt+f12 to cycle\r\n\r\n› Ask Codex to do anything\r\nGPT-6.1-Sol high · fixture'
 sys.stdout.write(screen);sys.stdout.flush()
if (root/'native-unavailable').exists():
 paint('Could not add the Cloud connection. fixture unavailable')
else:paint()
while True:
 data=os.read(sys.stdin.fileno(),1024)
 if not data:break
 if b'24' in data and b'\x1b' in data:
  host='Cloud';paint();continue
 if data==b'\x1b':overlay=False;text='';paint();continue
 for value in data:
  if value in (10,13):
   with (root/'native-input.jsonl').open('a') as f:f.write(json.dumps(text)+'\n')
   if text=='/model':overlay=True;paint('Select Model and Effort\r\nGPT-6.1-Sol\r\nGPT-6.1-Terra')
   elif text=='fixture working':state='Working';paint('working remotely')
   elif text=='fixture done':state='Ready';used=' | 123 used';paint('completed remotely')
   else:paint('native command: '+text)
   text=''
  elif value>=32:text+=chr(value)
""")
        shim = tools / 'dopamine'
        shim.write_text('#!/bin/sh\nif [ "${1:-}" = exec-cloud ]; then shift; exec ' + shlex.join([sys.executable, str(cloud)]) + ' "$@"; fi\nif [ "${1:-}" = exec-native-cloud ]; then exec ' + shlex.join([sys.executable, str(native)]) + '; fi\nexec ' + shlex.quote(binary) + ' "$@"\n')
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
            wait(lambda: 'OpenAI Codex (native fixture)' in screen() and 'Cloud  alt+f12 to cycle' in screen(), 'native cloud session did not open')
            cloud_pane = command('display-message', '-p', '-t', 'dopamine:', '#{pane_id}')
            rails = command('list-panes', '-t', cloud_pane, '-F', '#{pane_id}|#{@dopamine_role}').splitlines()
            rail = next(line.split('|')[0] for line in rails if line.endswith('|dopamine-rail'))
            wait(lambda: '2c' in command('capture-pane', '-p', '-t', rail), 'compact cloud badge missing')
            rows = command('capture-pane', '-p', '-t', rail).splitlines()
            assert len([row for row in rows if row.strip()]) == 2, rows
            capture_screen('cloud-compose')
            key(b'/model\r')
            wait(lambda: 'Select Model and Effort' in screen(), 'native model picker missing')
            capture_screen('cloud-models')
            key(b'\x1b')
            wait(lambda: 'Select Model and Effort' not in screen(), 'model picker did not close')
            key(b'fixture working\r')
            wait(lambda: '2c~' in command('capture-pane', '-p', '-t', rail), 'native running status missing')
            capture_screen('cloud-working')
            key(b'fixture done\r')
            wait(lambda: '2c✓' in command('capture-pane', '-p', '-t', rail), 'native completion status missing')
            capture_screen('cloud-ready')
            before = len(output)
            key(b'\x17')
            wait(lambda: b'close cloud tab?' in output[before:], 'native cloud close had no confirmation')
            key(b'\r')
            wait(lambda: cloud_pane not in command('list-panes', '-s', '-t', 'dopamine', '-F', '#{pane_id}').splitlines(), 'native cloud tab did not close')
            submitted = [json.loads(line) for line in (root/'native-input.jsonl').read_text().splitlines()]
            assert submitted == ['/model','fixture working','fixture done'], submitted
            # Existing batch-task IDs remain readable through their original viewer.
            saved_dir=root/'state/cloud';saved_dir.mkdir(parents=True,exist_ok=True)
            saved={'Key':'cloud-fixture-saved','Project':'fixture','Environment':'fixture environment','Prompt':'previously saved task','Directory':str(project),'ID':'task_e_fixture','URL':'https://chatgpt.com/codex/tasks/task_e_fixture','State':'ready','Updated':1}
            cloud_file=saved_dir/'cloud-fixture-saved.json';cloud_file.write_text(json.dumps(saved))
            (root/'cloud-status').write_text('READY')
            open_menu()
            key(b'j'*6+b'\r')
            wait(lambda: 'cloud tasks · Fixture' in screen(), 'saved cloud history missing')
            key(b'\r')
            wait(lambda: 'completed · ready to review' in screen(), 'saved task did not reopen')
            wait(lambda: '> Fixture' in command('capture-pane', '-p', '-t', dashboard), 'saved task opening not finished')
            legacy_pane=command('display-message','-p','-t','dopamine:','#{pane_id}')
            key(b'd')
            wait(lambda: '+fixture change' in screen(), 'saved diff missing')
            key(b'\x1b')
            wait(lambda: 'completed · ready to review' in screen(), 'saved diff did not close')
            key(b'o')
            wait(lambda: (root/'opened').exists(), 'saved task link did not open')
            assert (root/'opened').read_text().strip()==saved['URL']
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
            # A refused Cloud connection must leave no editable Local process.
            (root/'native-unavailable').touch()
            key(b'c')
            def failed_native():
                records=[json.loads(path.read_text()) for path in (root/'state/runs').glob('*.json')]
                return next((r for r in records if r['Kind']=='cloud-native' and r.get('State')=='Failed'),None)
            wait(lambda: failed_native() is not None, 'Cloud failure was not retained')
            failed=failed_native()
            wait(lambda: command('display-message','-p','-t',failed['Pane'],'#{pane_dead}')=='1', 'failed Cloud left an editable local process')
            failure_screen=command('capture-pane','-p','-t',failed['Pane'])
            assert 'cloud unavailable' in failure_screen and 'OpenAI Codex' not in failure_screen, failure_screen
            calls = [json.loads(line) for line in (root / 'cloud-calls.jsonl').read_text().splitlines()]
            assert sum(call[1] == 'exec' for call in calls) == 0, calls
            assert json.loads(cloud_file.read_text())['ID'] == 'task_e_fixture'
            print('PASS: compact single-row cloud tabs, native Codex handoff and model picker, native running/completion states, protected close, saved batch-task history and diff, both server autostart modes and real HTTP server; fixture Cloud/systemd, no model calls')
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
