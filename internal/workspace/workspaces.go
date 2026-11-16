package workspace

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// Slot is a terminal, not an agent state. IDs stay stable when tmux renumbers.
type Slot struct {
	Window, Pane, Project string
	Kind                  string
	Key, Token            string
	Dashboard, Exited     bool
	CloudState            string
}

func (s Service) Slots() ([]Slot, error) {
	out, err := tmux("list-panes", "-s", "-t", "=dopamine", "-F", "#{window_id}|#{pane_id}|#{@dopamine_role}|#{@dopamine_token}|#{pane_dead}")
	if err != nil {
		return nil, err
	}
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	byPane := make(map[string]Record)
	for _, r := range records {
		if r.Kind == "codex" || r.Kind == "shell" || r.Kind == "cloud" {
			byPane[r.Pane+"|"+r.Token] = r
		}
	}
	var slots []Slot
	hasDashboard := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 5 {
			continue
		}
		if f[2] == "dashboard" {
			hasDashboard = true
			slots = append(slots, Slot{Window: f[0], Pane: f[1], Dashboard: true, Exited: f[4] == "1"})
		} else if r, ok := byPane[f[1]+"|"+f[3]]; ok {
			slots = append(slots, Slot{Window: f[0], Pane: f[1], Project: r.Project, Kind: r.Kind, Key: r.Key, Token: r.Token, Exited: f[4] == "1"})
			if r.Kind == "cloud" {
				task, _ := s.CloudTask(r.Key)
				slots[len(slots)-1].CloudState = task.State
			}
		}
	}
	if !hasDashboard {
		// Keep desktop 1 reserved after the UI exits; opening it recreates only
		// the interface and leaves the native terminals in their original slots.
		slots = append(slots, Slot{Dashboard: true})
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Dashboard != slots[j].Dashboard {
			return slots[i].Dashboard
		}
		// Window IDs increase with creation and survive renumbering; second
		// precision run timestamps can tie when two projects open quickly.
		iID, _ := strconv.Atoi(strings.TrimPrefix(slots[i].Window, "@"))
		jID, _ := strconv.Atoi(strings.TrimPrefix(slots[j].Window, "@"))
		return iID < jID
	})
	return slots, nil
}

func (s Service) FocusSlot(number int) error {
	slots, err := s.Slots()
	if err != nil {
		return err
	}
	if number < 1 || number > len(slots) {
		// Number shortcuts are always bound, including slots not opened yet.
		// An unavailable slot is a no-op: a failed run-shell callback makes
		// tmux display its output over the user's terminal in copy mode.
		return nil
	}
	if slots[number-1].Dashboard && (slots[number-1].Window == "" || slots[number-1].Exited) {
		if _, err = s.EnsureDashboard(); err != nil {
			return err
		}
		if err = s.ConfigureDopamine(); err != nil {
			return err
		}
		return s.FocusSlot(1)
	}
	return FocusSlot(slots[number-1])
}

func FocusSlot(slot Slot) error {
	if _, err := tmux("select-window", "-t", slot.Window); err != nil {
		return err
	}
	_, err := tmux("select-pane", "-t", slot.Pane)
	return err
}

// ConfigureDopamine changes only the owned session. A rail beside each native
// Codex terminal keeps its input, scrollback, mouse and escape keys untouched.
func (s Service) ConfigureDopamine() error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	slots, err := s.Slots()
	if err != nil {
		return err
	}
	for _, option := range [][]string{
		{"set-option", "-t", "dopamine", "status", "off"},
		{"set-option", "-t", "dopamine", "mouse", "on"},
	} {
		if _, err = tmux(option...); err != nil {
			return err
		}
	}
	for _, slot := range slots {
		if slot.Window == "" {
			continue
		}
		for _, option := range [][]string{
			{"set-option", "-w", "-t", slot.Window, "pane-border-lines", "spaces"},
			{"set-option", "-w", "-t", slot.Window, "pane-border-style", "fg=colour240,bg=default"},
			{"set-option", "-w", "-t", slot.Window, "pane-active-border-style", "fg=colour240,bg=default"},
			{"rename-window", "-t", slot.Window, "dopamine"},
		} {
			if _, err = tmux(option...); err != nil {
				return err
			}
		}
		if slot.Dashboard {
			continue
		}
		if slot.Kind == "codex" && !slot.Exited {
			r, err := s.notificationRecord(slot.Key, slot.Token)
			if err != nil {
				return err
			}
			if err = s.configureCodexObserver(r); err != nil {
				return err
			}
		}
		panes, err := tmux("list-panes", "-t", slot.Window, "-F", "#{pane_id}|#{@dopamine_role}|#{@dopamine_executable}|#{pane_dead}")
		if err != nil {
			return err
		}
		rail := ""
		for _, line := range strings.Split(panes, "\n") {
			f := strings.Split(line, "|")
			if len(f) == 4 && f[1] == "dopamine-rail" {
				rail = f[0]
				if f[2] != s.Executable || f[3] == "1" {
					if _, err = tmux(append([]string{"respawn-pane", "-k", "-t", rail}, append(s.paneEnvironment(""), s.Executable, "sidebar")...)...); err != nil {
						return err
					}
				}
				break
			}
		}
		if rail == "" {
			rail, err = tmux(append([]string{"split-window", "-h", "-b", "-d", "-l", "5", "-t", slot.Pane, "-P", "-F", "#{pane_id}"}, append(s.paneEnvironment(""), s.Executable, "sidebar")...)...)
			if err != nil {
				return err
			}
		}
		for _, option := range [][]string{
			{"set-option", "-p", "-t", rail, "@dopamine_role", "dopamine-rail"},
			{"set-option", "-p", "-t", rail, "@dopamine_executable", s.Executable},
		} {
			if _, err = tmux(option...); err != nil {
				return err
			}
		}
		// tmux redistributes extra columns between panes when the terminal
		// grows. Pin this window's rail after both real window resizes and
		// layout changes (including mouse resizing). The width guard makes
		// the resize's own layout notification a no-op.
		pin := "if-shell -F -t " + rail + " " + Quote("#{&&:#{==:#{@dopamine_role},dopamine-rail},#{!=:#{pane_width},5}}") + " " + Quote("resize-pane -t "+rail+" -x 5")
		for _, hook := range []string{"window-resized[1000]", "window-layout-changed[1000]"} {
			// Indexed, window-local hooks retain other handlers and never
			// change the user's global tmux configuration.
			if _, err = tmux("set-hook", "-w", "-t", slot.Window, hook, pin); err != nil {
				return err
			}
		}
		if _, err = tmux("resize-pane", "-t", rail, "-x", "5"); err != nil {
			return err
		}
	}
	for n := 1; n <= 9; n++ {
		cmd := ShellArgs(append(s.paneEnvironment(""), s.Executable, "slot", strconv.Itoa(n)))
		if _, err = tmux("bind-key", "-T", dopamineKeyTable, "M-"+strconv.Itoa(n), "run-shell", "-b", cmd); err != nil {
			return err
		}
	}
	for _, binding := range []struct{ key, direction string }{{"M-Left", "previous"}, {"M-Right", "next"}} {
		cmd := ShellArgs(append(s.paneEnvironment(""), s.Executable, "slot", binding.direction))
		if _, err = tmux("bind-key", "-T", dopamineKeyTable, binding.key, "run-shell", "-b", cmd); err != nil {
			return err
		}
	}
	// Close only workspaces with Ctrl+w; Dashboard exits through its own q key.
	liveCodex := "#{m:*1*,#{P:#{&&:#{!=:#{pane_dead},1},#{||:#{==:#{@dopamine_kind},codex},#{m:*codex*,#{pane_current_command}}}}}}"
	liveShell := "#{m:*1*,#{P:#{&&:#{!=:#{pane_dead},1},#{==:#{@dopamine_kind},shell}}}}"
	closeShell := "if-shell -F " + Quote(liveShell) + " " + Quote("confirm-before -y -p "+Quote("terminal is running. close this workspace? enter: yes / esc: no")+" "+Quote("kill-window -t #{window_id}")) + " kill-window"
	liveCloud := "#{m:*1*,#{P:#{&&:#{!=:#{pane_dead},1},#{==:#{@dopamine_kind},cloud}}}}"
	closeShell = "if-shell -F " + Quote(liveCloud) + " " + Quote("confirm-before -y -p "+Quote("close cloud tab? unsent text is lost; submitted work continues. enter: yes / esc: no")+" "+Quote("kill-window -t #{window_id}")) + " " + Quote(closeShell)
	close := "if-shell -F " + Quote(liveCodex) + " " + Quote("confirm-before -y -p "+Quote("codex is running. close this workspace? enter: yes / esc: no")+" "+Quote("kill-window -t #{window_id}")) + " " + Quote(closeShell)
	_, err = tmux("bind-key", "-T", dopamineKeyTable, "C-w", "if-shell", "-F", "#{!=:#{@dopamine_role},dashboard}", close)
	if err != nil {
		return err
	}
	return nil
}

func (s Service) CycleSlot(direction int) error {
	slots, err := s.Slots()
	if err != nil || len(slots) == 0 {
		return err
	}
	current, err := tmux("display-message", "-p", "-t", os.Getenv("TMUX_PANE"), "#{window_id}")
	if err != nil {
		return err
	}
	for i, slot := range slots {
		if slot.Window == current {
			return s.FocusSlot((i+direction+len(slots))%len(slots) + 1)
		}
	}
	return nil
}
