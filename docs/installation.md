# Install dopamine

[← Back to the project](../README.md)

## Requirements

| Component | Purpose |
|---|---|
| Linux x86_64 | The current pinned flake target, tested on NixOS |
| Nix with `nix-command` and `flakes` | Reproducible builds and runtime dependencies |
| Git | Cloning and updating the source |
| Python 3 | Installer and source launcher |
| UTF-8 terminal | Interface, shortcuts, and dotted artwork |
| System `codex-vpn` launcher | Required only for Codex sessions |

The Nix package provides tmux, Git, Python, Node.js, `xdg-open`, and `notify-send`. You do not need a separate host Go installation. Desktop notifications require a working desktop bus and notification service; opening a URL requires a browser handler. Project dependencies still need their own installation or development shell.

Codex launches currently use `/run/current-system/sw/bin/codex-vpn` with `--no-daemon --approve-for-me` and the default `~/.codex` directory. That launcher must already exist and support this interface. dopamine does not install a VPN, configure a Codex account, or rewrite those system settings. Projects, regular shells, and dev servers work independently of this integration. This version does not claim support for macOS, Windows, other architectures, or launching a plain `codex` executable instead.

## 1. Check Nix

```sh
nix --version
git --version
python3 --version
nix flake metadata github:dr6m6/dopamine
```

If Nix reports that experimental features are disabled, enable `nix-command` and `flakes` in your existing Nix configuration. On NixOS, this usually means:

```nix
nix.settings.experimental-features = [ "nix-command" "flakes" ];
```

Apply the change using your normal system configuration workflow. If you do not have Nix, use the [official Nix installer and documentation](https://nixos.org/download/).

## 2. Clone and install

```sh
git clone https://github.com/dr6m6/dopamine.git
cd dopamine
./install.sh
```

No `sudo` is required. The installer builds the pinned package and links `~/.local/bin/dopamine` to the source launcher in your clone. Keep the clone in place: it is used on subsequent launches. Installation adds a terminal command, without a desktop launcher entry.

Add `~/.local/bin` to `PATH` through your shell or Home Manager setup. For the current Bash/Zsh session:

```sh
export PATH="$HOME/.local/bin:$PATH"
dopamine --version
dopamine
```

The first build downloads dependencies and compiles the Go package. Subsequent launches of the same source state use Nix's cache. A build failure is reported rather than silently falling back to an older binary.

## 3. Try it without installing

From the clone:

```sh
nix run path:.#dopamine
```

Or build a development binary:

```sh
nix develop path:. -c make build
nix develop path:. -c ./bin/dopamine
```

The development shell provides tmux and the project's tools. Automatic rebuilding on each launch belongs to the installed source launcher; a direct `bin/dopamine` invocation does not rebuild itself.

## 4. Open a project

Folders directly inside `~/projects` appear automatically, even without a Git repository. Select a folder, press `Enter`, and choose `new terminal` or `new codex session`.

To use another discovery directory:

```sh
PROJECT_HUB_PROJECTS_ROOT="$HOME/work" dopamine
```

To add a single external directory, select `+ add folder…`, enter an absolute path or `~/…`, optionally complete it with `Tab`, and press `Enter`. `remove from list` removes the registry entry after confirmation; files and running workspaces are kept.

## 5. Configure a dev server

After the first launch, edit `~/.config/project-hub/projects.json`, or `$XDG_CONFIG_HOME/project-hub/projects.json` if set. Add a `launch` argument array and a local `app_url` to the relevant project. Keep other projects, IDs, and extra fields intact.

Example with Python's HTTP server:

```json
{
  "projects": [
    {
      "id": "demo",
      "name": "demo",
      "path": "~/projects/demo",
      "launch": ["python3", "-m", "http.server", "8080", "--bind", "127.0.0.1"],
      "app_url": "http://127.0.0.1:8080/"
    }
  ]
}
```

The directory must exist. `launch` runs in that directory without implicit shell interpolation; use your own script for a complex command. The server must remain in the foreground. Open the project's menu, choose `start server`, then `open url`. `stop server` stops the managed launch. Normal local URLs use `localhost`, `127.0.0.1`, or `::1`.

## Update

```sh
cd /path/to/dopamine
git pull --ff-only
dopamine
```

The installed launcher builds a new package when the sources change. Running Codex sessions, shells, and servers keep working. New Codex sessions receive updates to notification handlers. If you have local edits, save them using your usual Git workflow first; `--ff-only` will not overwrite diverging history.

If you cloned the earlier repository snapshot before dopamine received its standalone history, make a fresh clone instead of merging the two histories. Save any local changes first.

## NixOS module

You can install through your existing system flake:

```nix
# Fragments of your existing flake.nix
inputs.dopamine.url = "github:dr6m6/dopamine";

# In the modules list of the relevant nixosConfigurations:
modules = [
  inputs.dopamine.nixosModules.default
  { programs.dopamine.enable = true; }
];
```

The module adds the package to `environment.systemPackages`; you do not need `./install.sh` with this method. Updates follow your system lockfile and rebuild workflow. Rebuilding from a local clone on every launch applies to `./install.sh` installations. Configure the existing `codex-vpn` integration separately.

## Data and uninstall

| Default path | Contents |
|---|---|
| `~/.config/project-hub/projects.json` | Project registry and launch commands |
| `~/.config/project-hub/settings.json` | Notification setting |
| `~/.local/state/project-hub/` | Run records, logs, and Nix build links |
| `~/.local/state/dopamine/install.json` | Source launcher installation receipt |
| `~/.codex/` | Codex's own data and settings |

Paths honor the relevant XDG variables. The registry, runtime data, and discovery directory can also be set with `PROJECT_HUB_CONFIG_DIR`, `PROJECT_HUB_STATE_DIR`, and `PROJECT_HUB_PROJECTS_ROOT`. Internal `project-hub` names are retained for compatibility with existing data.

To uninstall the source launcher:

```sh
cd /path/to/dopamine
./uninstall.sh
```

The command is removed while data and live processes are kept. For a NixOS module installation, disable `programs.dopamine.enable` and apply your system configuration. Close an individual running shell or Codex workspace with `Ctrl+w` and confirmation; `exit` ends a shell.

## Troubleshooting

| Symptom | Check |
|---|---|
| `dopamine: command not found` | Put `~/.local/bin` in `PATH`, or try `~/.local/bin/dopamine` |
| `nix-command` or `flakes` disabled | Enable the experimental features in Nix settings |
| Nix daemon access denied | Check user access to the daemon and your system's Nix policy |
| Build failure | Run `nix build path:.#dopamine --no-link -L` in the clone and inspect the first error |
| `required VPN launcher unavailable` | Check `/run/current-system/sw/bin/codex-vpn`; plain `codex` is not a replacement for this integration |
| `custom CODEX_HOME launch refused` | Codex sessions use the default `~/.codex` |
| Installer refuses to replace a command | The command or receipt was changed; inspect it before taking manual action |
| Missing project | Check the discovery root and directory; add external folders with `+ add folder…` |
| No server status | A `launch` command is required; `app_url` alone does not enable a status |
| Alt shortcuts do not reach dopamine | Check terminal or window-manager bindings; use a number click or `Tab` on the dashboard |
| No notifications | Check `notifications: on`, a working desktop bus, and a new Codex session |
| Terminal size warning | Minimum 46 × 16; the molecule is hidden below 22 rows |

If the problem reproduces, [open an issue](https://github.com/dr6m6/dopamine/issues/new/choose) with the version, installation method, and steps. Do not include private conversations or credentials.
