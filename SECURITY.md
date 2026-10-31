# Security

Report vulnerabilities privately using [GitHub's vulnerability reporting](https://github.com/dr6m6/dopamine/security/advisories/new). Include the affected revision, reproduction steps and expected impact. Please leave credentials, personal conversations and private logs out of reports.

The maintained product is the `dopamine` executable and its installer. The current supported environment is Linux x86_64 with Nix; Codex launches require the existing system `codex-vpn` integration described in the [installation guide](docs/installation.md).

Project launch commands run with the user's permissions. Only add commands and folders you trust. Terminal transcripts are local user data, stored under `$XDG_STATE_HOME/project-hub` (default `~/.local/state/project-hub`).

Fixes are provided on a best-effort basis. Third-party services, Codex accounts and operating-system VPN configuration remain outside this project's maintenance scope.
