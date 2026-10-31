#!/bin/sh
# dopamine always follows this checkout; no static release download.
set -eu
dopamine_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ "${1:-}" = "--build" ]; then shift; fi
exec python3 "$dopamine_ROOT/scripts/install-dopamine.py" "$@"
