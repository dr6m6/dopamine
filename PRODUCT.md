# dopamine

A minimal terminal workspace for shells, Codex, and project dev servers.

- Tab 1 is the dashboard. Select a project and press Enter to open its menu.
- Project actions: new codex session, new terminal, start server, stop server, open url.
- Opening a workspace returns the dashboard to its project list. Server actions keep the menu open.
- Global actions below the list: new terminal in the home directory, add folder, and notification mute.
- Folder input accepts absolute paths and ~/ paths, paste, and Tab completion.
- Removing an external folder only removes its registry entry, after confirmation.
- Configured servers appear first with a status. Other folders have no server status.
- Codex and shells run in native terminals. Closing a running workspace requires confirmation.
- Navigation shows only tab numbers, with neutral colors and inverted selection. No separator line.
- The molecule artwork and lowercase interface labels are part of the existing identity.
- Help appears on ?. Names and paths keep their original case.
- Leaving the interface keeps workspaces and servers running in tmux.
- The installed source launcher builds the current checkout on each launch.
- The interface does not make background model API calls.

The minimum terminal size is 46 × 16. Artwork is hidden below 22 rows to leave room for actions. All tabs are reachable with Alt+left/right; Alt+1…9 and mouse clicks select a number directly.
