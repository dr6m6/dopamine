# Cloud tasks and server autostart

## Codex Cloud

Open a project's menu and choose **new cloud task** (`c`). The new tab has a cloud below its number, so it stays distinct from a local Codex terminal.

1. Enter the name or ID of an existing Codex Cloud environment. Use the native `codex cloud` picker or your Codex Cloud settings to find/configure it. The environment needs the repository and tools required by the task.
2. Press Enter or Tab to write the task. Enter adds a new line; **Ctrl+s sends it**. The environment is remembered for this project.
3. The tab shows the last known cloud status, the time it was checked, the remote branch, and the task link. It checks every 15 seconds while the tab is running. `r` refreshes, `o` opens the result through the existing browser handler, and `d` shows the cloud diff with scrolling. Esc returns from the diff or visits the dashboard.
4. **Cloud tasks…** in the project menu reopens a saved task, including after tmux or the computer restarts. Reopening an existing live tab selects it. History labels are cached; opening the task refreshes its state.

Rail markers: `~` means submitting/working, `✓` means completed, `!` means failed/cancelled, and `?` means a submission needs checking. Every cloud tab has `⢔⣊⣑⡢` on the row immediately below its number; clicking either row selects it. The cloud CLI reports `PENDING` for both queued and running tasks, so dopamine displays **working · queued or running** rather than claiming a finer state. `READY` means the result is ready to review; `APPLIED` is also complete.

The task runs remotely after submission, independently of the local tab or computer. Ctrl+w confirms closing a cloud tab to protect unsent text; closing a submitted task's tab does not cancel it. Draft text before submission lives in that tab. Saved submitted tasks live under `$XDG_STATE_HOME/dopamine/cloud` (or the configured dopamine state directory), with private file permissions.

Cloud uses the remote version of the selected branch. Dopamine sends the task text and branch name; it does not upload uncommitted files, push a branch, apply the resulting diff, or merge it. Review the result before applying it with the native Codex Cloud workflow.

If sending times out or returns no identifiable task link, dopamine saves **submission uncertain** and does not retry automatically. Check `codex cloud` first, then paste the task ID or link into the attachment field. This avoids creating a duplicate after a lost response. A status/network error retains the last known state and timestamp and displays the refresh error.

The integration reuses the existing `/run/current-system/sw/bin/codex-vpn` launcher, Codex login and network setup. It requires a Codex CLI with `cloud exec`, `cloud status`, and `cloud diff` (checked against 0.169.0). This is an experimental upstream CLI interface; dopamine does not call undocumented cloud HTTP endpoints or manage credentials itself.

![Cloud task with a cloud drawn beneath its tab number](assets/dopamine/cloud-working.png)

## Server autostart

A project with a configured `launch` command has **server autostart** (`a`) in its menu. Choose with arrows and save with Enter:

| Mode | When the server starts |
|---|---|
| off | Only when you choose start server |
| when dopamine opens | Once at each ordinary dopamine launch; an existing managed server is reused |
| at login | Through an enabled user systemd service, without opening dopamine |

Changing the mode affects future starts and does not stop a currently running server. Use **start server** to start immediately and **stop server** to stop either a tmux server or an owned systemd service. Stopping a login service keeps autostart enabled for the next login. To prevent that, select off or when dopamine opens.

Login mode uses a dedicated `dopamine-server-<hash>.service` in `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`). It runs the configured foreground command in the project's directory, preserves `launch_vpn`, and restarts failures through systemd. It does not enable linger, request root access, or change NixOS, Home Manager, Codex, VPN, or other services. Existing externally managed units are not overwritten. If user linger is already enabled independently, the user manager may start before an interactive login.

Installed source builds use the existing stable `source-package/bin/dopamine` GC root, so a package update does not strand the service on an old Nix store hash. NixOS package installations use the stable system command when it refers to the current package. Temporary unrooted `nix run` packages must be installed before enabling login mode. Keep the project directory, source installation and configured commands available. Select off before uninstalling if you also want to disable future login starts; uninstall preserves project settings and services.

The service reads current project settings on every start. Its environment contains the captured PATH, HOME and dopamine registry/state locations; use the project's declared `nix develop -c …` launch command for dependencies. Environment-specific secrets should be supplied through the project's existing mechanisms. Status failures name the journal command, `journalctl --user -u dopamine-server-<hash>.service`.

Both settings are per project in `projects.json`: `cloud_environment` and `server_autostart` (`off`, `dopamine`, `login`). Edits preserve unknown settings and missing project entries.

![Server autostart choices](assets/dopamine/autostart.png)

## Implementation choice and verification

Reuse native Codex cloud commands instead of duplicating authentication or depending on private HTTP APIs. The existing Go/tmux/Bubble Tea app handles display and persists task IDs; no new service or dependency is needed. Native systemd user services provide login lifecycle and process supervision; the existing tmux launcher already covers starting when dopamine opens. This is a smaller integration than adding a second background daemon.

Sources: [OpenAI CLI reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli), [cloud environments](https://learn.chatgpt.com/docs/environments/cloud-environments), [upstream status formatting and exit codes](https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/cloud-tasks/src/lib.rs), [systemd services](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html).

`make test` includes cloud/autostart unit tests and an isolated real tmux/TUI/HTTP scenario. Its Codex Cloud commands and systemd manager are fixtures: it verifies transitions, controls, registry persistence and real foreground server start/stop without making a model call or changing the user's services. Generated units are also checked with the real systemd parser when available. A fixture run does not establish account access to a live cloud environment or replace testing an actual login.
