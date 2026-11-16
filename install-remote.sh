#!/bin/sh
# Standalone client for Void and other Linux distributions; no Nix required.
set -eu
dopamine_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec python3 "$dopamine_ROOT/scripts/dopamine-remote.py" install "$@"
