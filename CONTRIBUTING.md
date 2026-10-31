# Contributing to dopamine

Use the pinned Nix development shell from the repository root:

```sh
nix develop path:. -c make build
nix develop path:. -c make test
nix build path:.#dopamine
```

`make test` checks Go formatting, runs vet and the race detector, builds dopamine, tests the source launcher and installer, and exercises the real TUI in an isolated tmux. Codex is a fixture in this scenario; no model API calls are made.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/dopamine/` | Entry point and internal callbacks |
| `internal/dopamine/` | Terminal interface |
| `internal/projecthub/` | Project registry, tmux workspaces, servers, and notifications |
| `scripts/install-dopamine.py` | Command installation |
| `scripts/dopamine-source.py` | Source launcher |
| `scripts/dopamine-smoke.py` | Isolated terminal integration scenario |
| `docs/assets/dopamine/` | Project artwork and screenshots |

The internal `project-hub` names remain compatible with existing user data and launchers. They are not separate product interfaces.

## Changes

Preserve user data, live sessions, and existing Codex and VPN settings. Use temporary directories, a separate tmux socket, and disposable projects for tests. Do not change system integrations to simplify a test.

In a pull request, describe the problem, the resulting behavior, and checks actually run. Include a demo screenshot for interface changes. Interface labels use lowercase; project names and paths retain their original case.

## Screenshots and artwork

After `make build`, capture the real interface with disposable projects:

```sh
nix develop path:. -c python3 scripts/capture-dopamine-docs.py
```

Rendering the PNG assets requires Python 3 with Pillow and a monospace font discoverable with fontconfig:

```sh
python3 scripts/render-dopamine-docs.py
```

The logo is the dotted molecule from the TUI. Its SVG represents the braille dots as editable circles. Screenshots are rendered from actual terminal captures, preserving selection and ANSI colors. No personal projects or conversations are needed.

[Security reports](SECURITY.md) · [MIT License](LICENSE)
