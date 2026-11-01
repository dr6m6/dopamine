package workspace

import (
	"fmt"
	"os"
	"strings"
)

// EnsureDashboard explicitly supplies its environment to the new tmux pane.
// A running server does not inherit the environment of a later tmux client.
func (s Service) EnsureDashboard() (string, error) {
	lock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer lock.Close()
	args := []string{"env", "DOPAMINE_TUI=1"}
	for _, key := range []string{"HOME", "PATH", "SHELL", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_SESSION_TYPE", "NIRI_SOCKET", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "CODEX_HOME", "DOPAMINE_CONFIG_DIR", "DOPAMINE_STATE_DIR", "DOPAMINE_PROJECTS_ROOT", "DOPAMINE_DASHBOARD", "DOPAMINE_EXECUTABLE"} {
		if v, ok := os.LookupEnv(key); ok {
			args = append(args, key+"="+v)
		}
	}
	args = append(args, s.Executable)
	out, _ := tmux("list-panes", "-a", "-F", "#{session_name}|#{window_index}|#{pane_index}|#{@dopamine_role}|#{pane_dead}|#{pane_id}|#{@dopamine_executable}|#{window_panes}")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 8 || f[0] != "dopamine" || f[3] != "dashboard" {
			continue
		}
		// Older dopamine versions marked the window rather than its interface pane.
		// Do not guess which process to replace if that window was split.
		if f[6] == "" && f[7] != "1" {
			return "", fmt.Errorf("legacy dopamine window has multiple panes; close its old interface before updating")
		}
		target := fmt.Sprintf("%s:%s.%s", f[0], f[1], f[2])
		if f[6] != s.Executable || f[4] != "0" {
			// Replace only the interface. Native sessions, apps and their panes
			// keep running when the launcher has built a new package.
			if _, err = tmux(append([]string{"respawn-pane", "-k", "-t", f[5]}, args...)...); err != nil {
				return "", err
			}
		}
		if err = s.markDashboard(f[5]); err != nil {
			return "", err
		}
		if err = s.ConfigureShortcuts(); err != nil {
			return "", err
		}
		return target, nil
	}
	base := []string{"new-window", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}", "-t", "dopamine:", "-n", "dashboard"}
	if _, err = tmux("has-session", "-t", "=dopamine"); err != nil {
		base = []string{"new-session", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}", "-s", "dopamine", "-n", "dashboard"}
	}
	// Multi-argument shell-command uses tmux's direct exec, without shell
	// interpolation of paths or registry values.
	target, err := tmux(append(base, args...)...)
	if err != nil {
		return "", err
	}
	if err = s.markDashboard(target); err != nil {
		return "", err
	}
	if err = s.ConfigureShortcuts(); err != nil {
		return "", err
	}
	return target, nil
}

func (s Service) markDashboard(target string) error {
	for _, option := range [][]string{{"@dopamine_role", "dashboard"}, {"@dopamine_executable", s.Executable}} {
		if _, err := tmux("set-option", "-p", "-t", target, option[0], option[1]); err != nil {
			return err
		}
	}
	// Remove the legacy window marker so later splits cannot inherit the
	// interface role and be mistaken for the dopamine during an update.
	_, err := tmux("set-option", "-w", "-u", "-t", target, "@dopamine_role")
	return err
}
