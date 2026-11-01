package workspace

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const dopamineKeyTable = "dopamine-root"

var rootBinding = regexp.MustCompile(`(?m)^(bind-key\s+(?:-r\s+)?-T\s+)root(\s+)`)

// ConfigureShortcuts copies the user's root bindings into a dopamine-only table.
// Neither root/prefix nor the user's tmux configuration is modified.
func (s Service) ConfigureShortcuts() error {
	root, err := tmux("list-keys", "-T", "root")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.StateDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.StateDir, "tmux-keys-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	cloned := rootBinding.ReplaceAllString(root, "${1}"+dopamineKeyTable+"${2}")
	if _, err = f.WriteString(cloned + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, existsErr := tmux("list-keys", "-T", dopamineKeyTable); existsErr == nil {
		if _, err = tmux("unbind-key", "-a", "-T", dopamineKeyTable); err != nil {
			return err
		}
	}
	if _, err = tmux("source-file", f.Name()); err != nil {
		return err
	}

	// Check every pane in the current tab, including an unfocused Codex split.
	// confirm-before expands window_id before opening the prompt, fixing the
	// target even if the user switches windows before confirming.
	liveCodex := "#{m:*1*,#{P:#{&&:#{!=:#{pane_dead},1},#{||:#{==:#{@dopamine_kind},codex},#{m:*codex*,#{pane_current_command}}}}}}"
	closeTab := "kill-window -t #{window_id}"
	confirm := "confirm-before -y -p " + Quote("codex is running. close this tab? enter: yes / esc: no") + " " + Quote(closeTab)
	if _, err = tmux("bind-key", "-T", dopamineKeyTable, "C-w", "if-shell", "-F", liveCodex, confirm, "kill-window"); err != nil {
		return err
	}
	quit := append(s.paneEnvironment(""), s.Executable, "kill-dopamine")
	if _, err = tmux("bind-key", "-T", dopamineKeyTable, "M-S-F4", "run-shell", "-b", ShellArgs(quit)); err != nil {
		return err
	}
	// set-option resolves session options differently from target-pane commands:
	// it treats the leading '=' literally and rejects '=dopamine'.
	if _, err = tmux("set-option", "-t", "dopamine", "key-table", dopamineKeyTable); err != nil {
		return err
	}
	// Keep tab numbers consecutive after any window closes. Pane/window IDs
	// remain stable, so existing sessions and confirmation targets survive.
	if _, err = tmux("set-option", "-t", "dopamine", "renumber-windows", "on"); err != nil {
		return err
	}
	// Adopt already-running dopamine Codex panes without restarting their processes.
	records, err := s.records()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Kind != "codex" || !liveRecord(r) {
			continue
		}
		if _, err = recordTmux(r, "set-option", "-p", "-t", r.Pane, "@dopamine_kind", "codex"); err != nil {
			return err
		}
	}
	return nil
}

// KillDashboard removes only marked dopamine interface panes, including an
// unresponsive interface. Codex and application windows belong to tmux.
func (s Service) KillDashboard() error {
	out, err := tmux("list-panes", "-s", "-t", "=dopamine", "-F", "#{session_name}|#{@dopamine_role}|#{pane_id}|#{@dopamine_executable}")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 4 || f[0] != "dopamine" || f[1] != "dashboard" || f[3] == "" {
			continue
		}
		if _, err = tmux("kill-pane", "-t", f[2]); err != nil {
			return fmt.Errorf("close dopamine interface: %w", err)
		}
	}
	return nil
}
