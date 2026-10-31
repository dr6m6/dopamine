#!/usr/bin/env python3
"""Capture the actual TUI in an isolated tmux with disposable demo projects."""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='bin/dopamine')
    parser.add_argument('--output', default='docs/assets/dopamine/captures')
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    real_tmux = shutil.which('tmux')
    if not real_tmux or not binary.is_file():
        parser.error('tmux and the built dopamine binary are required; run make build first')
    with tempfile.TemporaryDirectory(prefix='dopamine-docs-', dir='/tmp') as temp:
        root = Path(temp)
        tools = root / 'tools'
        tools.mkdir()
        tmux = [real_tmux, '-S', str(root / 'tmux.sock'), '-f', '/dev/null']
        shim = tools / 'tmux'
        shim.write_text('#!/bin/sh\nexec ' + shlex.join(tmux) + ' "$@"\n')
        shim.chmod(0o755)
        projects = root / 'projects'
        config = root / 'config'
        config.mkdir()
        for name in ('website', 'api', 'dotfiles', 'notes'):
            (projects / name).mkdir(parents=True)
        (projects / 'website' / 'index.html').write_text('<h1>demo website</h1>\n')
        (projects / 'website' / 'README.md').write_text('# demo website\n')
        (root / '.bash_profile').write_text("PS1='\\w $ '; export PS1\n")
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        registry = []
        for name in ('website', 'api', 'dotfiles', 'notes'):
            project = {'id': name, 'name': name, 'path': str(projects / name)}
            if name in ('website', 'api'):
                project['launch'] = [sys.executable, '-m', 'http.server', str(port), '--bind', '127.0.0.1']
                project['app_url'] = f'http://127.0.0.1:{port}/'
            registry.append(project)
        (config / 'projects.json').write_text(json.dumps({'projects': registry}))
        env = os.environ.copy()
        env.update(HOME=temp, PATH=str(tools) + ':' + env['PATH'], SHELL=shutil.which('bash'),
                   TERM='xterm-256color', COLORTERM='truecolor', PROJECT_HUB_TUI='1',
                   PROJECT_HUB_EXECUTABLE=str(binary), PROJECT_HUB_CONFIG_DIR=str(config),
                   PROJECT_HUB_STATE_DIR=str(root / 'state'), PROJECT_HUB_PROJECTS_ROOT=str(projects),
                   CODEX_HOME=str(root / '.codex'), XDG_CONFIG_HOME=str(root / 'xdg-config'),
                   XDG_STATE_HOME=str(root / 'xdg-state'))
        for key in ('TMUX', 'TMUX_PANE', 'DBUS_SESSION_BUS_ADDRESS', 'DISPLAY', 'WAYLAND_DISPLAY',
                    'PROJECT_HUB_DASHBOARD'):
            env.pop(key, None)

        def command(*parts):
            return subprocess.check_output(tmux + list(parts), env=env, text=True, stderr=subprocess.DEVNULL).rstrip('\n')

        def screen():
            return command('capture-pane', '-p', '-t', 'project-hub:')

        def wait_for(check, message, timeout=15):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                try:
                    if check():
                        return
                except subprocess.CalledProcessError:
                    pass
                time.sleep(0.1)
            raise RuntimeError(message + '\n' + screen())

        def capture(name, target='project-hub:'):
            # Preserve cell positions and SGR styling, including the inverted slot.
            text = command('capture-pane', '-p', '-e', '-N', '-t', target)
            (output / (name + '.txt')).write_text('\n'.join(line.rstrip() for line in text.splitlines()) + '\n')

        try:
            command('new-session', '-d', '-s', 'project-hub', '-x', '100', '-y', '28', str(binary))
            command('set-option', '-t', 'project-hub', 'status', 'off')
            command('set-option', '-p', '-t', 'project-hub:', '@project_hub_role', 'dashboard')
            wait_for(lambda: '> website' in screen() and 'notifications: on' in screen(), 'dashboard did not load')
            command('send-keys', '-t', 'project-hub:', 'Enter')
            wait_for(lambda: 'new codex session' in screen(), 'project menu did not open')
            command('send-keys', '-t', 'project-hub:', 's')
            wait_for(lambda: 'server started' in screen(), 'demo server did not start')
            command('send-keys', '-t', 'project-hub:', 'Escape')
            wait_for(lambda: 'running' in screen() and '> website' in screen(), 'server status did not refresh')
            capture('dashboard')
            command('send-keys', '-t', 'project-hub:', 'Enter')
            wait_for(lambda: 'new codex session' in screen(), 'menu did not reopen')
            capture('project-menu')
            command('send-keys', '-t', 'project-hub:', 't')
            wait_for(lambda: command('display-message', '-p', '-t', 'project-hub:', '#{@project_hub_kind}') == 'shell', 'terminal did not open')
            main_pane = command('display-message', '-p', '-t', 'project-hub:', '#{pane_id}')
            wait_for(lambda: '$' in screen(), 'shell prompt did not appear')
            command('send-keys', '-t', main_pane, 'clear', 'Enter')
            command('send-keys', '-t', main_pane, 'pwd', 'Enter')
            command('send-keys', '-t', main_pane, 'ls', 'Enter')
            wait_for(lambda: 'index.html' in screen(), 'demo terminal output did not appear')
            wait_for(lambda: command('show-options', '-wv', '-t', 'project-hub:', 'pane-border-lines') == 'spaces', 'workspace border is visible')
            rail = next(line.split('|')[0] for line in command('list-panes', '-t', 'project-hub:', '-F', '#{pane_id}|#{@project_hub_role}').splitlines() if line.endswith('|dopamine-rail'))
            wait_for(lambda: '2' in command('capture-pane', '-p', '-t', rail), 'slot rail did not load')
            capture('terminal-main', main_pane)
            capture('terminal-rail', rail)
            # Pane captures exclude tmux's blank separator column.
            (output / 'layout.json').write_text(json.dumps({'columns': 100, 'rows': 28, 'rail_columns': 5, 'separator_columns': 1}, indent=2) + '\n')
            print('Captured dashboard, project menu and native shell; isolated tmux, no Codex calls.')
        finally:
            subprocess.run(tmux + ['kill-server'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
