#!/usr/bin/env bash
# Optional prefix+D integration, using the same always-current source launcher.
set -eu
dopamine_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf -v dopamine_COMMAND '%q' "$dopamine_ROOT/scripts/dopamine-source.py"
tmux bind-key D run-shell "$dopamine_COMMAND"
