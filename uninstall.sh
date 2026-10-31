#!/bin/sh
# Remove owned dopamine launchers only. Preserve all user data and processes.
set -eu
dopamine_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec python3 "$dopamine_ROOT/scripts/install-dopamine.py" --uninstall "$@"
