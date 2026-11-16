package dopamine

import (
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

var serverModes = []string{workspace.AutostartOff, workspace.AutostartDopamine, workspace.AutostartLogin}

type serverModeChanged struct {
	project, mode string
	err           error
}
type cloudHistoryLoaded struct {
	tasks []workspace.CloudTask
	err   error
}

func (m Model) updateServerSettings(key string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	switch key {
	case "esc":
		m.serverSettings = false
		m.status = ""
	case "up", "k":
		m.autostartChoice = (m.autostartChoice + len(serverModes) - 1) % len(serverModes)
	case "down", "j":
		m.autostartChoice = (m.autostartChoice + 1) % len(serverModes)
	case "enter":
		p := m.projects[m.project]
		mode := serverModes[m.autostartChoice]
		m.busy = true
		m.status = "saving autostart…"
		return m, func() tea.Msg { return serverModeChanged{p.ID, mode, m.service.SetServerAutostart(p, mode)} }
	}
	return m, nil
}
func (m Model) serverSettingsView(width int) string {
	p := m.projects[m.project]
	lines := []string{"", bright.Render("  server autostart · " + clean(p.Name)), ""}
	for i, mode := range serverModes {
		prefix := "  "
		if i == m.autostartChoice {
			prefix = "> "
		}
		label := workspace.AutostartLabel(mode)
		if p.ServerAutostart == mode || (p.ServerAutostart == "" && mode == workspace.AutostartOff) {
			label += " · selected"
		}
		lines = append(lines, prefix+label)
	}
	lines = append(lines, "", ansi.Wrap("at login runs without dopamine open. when dopamine opens starts once on launch.", max(1, width), ""), "", muted.Render("enter: save · esc: back"), clean(m.status))
	return strings.Join(lines, "\n")
}
func (m Model) updateCloudHistory(key string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	switch key {
	case "esc":
		m.cloudHistory = false
	case "up", "k":
		m.cloudChoice = max(0, m.cloudChoice-1)
	case "down", "j":
		m.cloudChoice = min(max(0, len(m.cloudTasks)-1), m.cloudChoice+1)
	case "n":
		m.cloudHistory = false
		m.busy = true
		return m, m.perform("c", m.projects[m.project])
	case "enter":
		if len(m.cloudTasks) == 0 {
			return m, nil
		}
		key := m.cloudTasks[m.cloudChoice].Key
		p := m.projects[m.project]
		m.busy = true
		return m, func() tea.Msg {
			r, err := m.service.Start(p, "cloud", key)
			if err == nil {
				err = m.service.ConfigureDopamine()
			}
			if err == nil {
				_, err = exec.Command("tmux", "select-window", "-t", r.Target).CombinedOutput()
			}
			if err == nil {
				_, err = exec.Command("tmux", "select-pane", "-t", r.Pane).CombinedOutput()
			}
			return finished{err: err, workspaceOpened: err == nil}
		}
	}
	return m, nil
}
func (m Model) cloudHistoryView(width int) string {
	lines := []string{"", bright.Render("  cloud tasks · " + clean(m.projects[m.project].Name)), ""}
	rows := max(1, m.height-8)
	start := max(0, m.cloudChoice-rows+1)
	if len(m.cloudTasks) == 0 {
		lines = append(lines, "no cloud tasks yet. press n to create one.")
	}
	for i := start; i < len(m.cloudTasks) && i < start+rows; i++ {
		prefix := "  "
		if i == m.cloudChoice {
			prefix = "> "
		}
		lines = append(lines, ansi.Truncate(prefix+cloudTitle(m.cloudTasks[i]), max(1, width), "…"))
	}
	lines = append(lines, "", muted.Render("enter: open · n: new cloud task · esc: back"), clean(m.status))
	return strings.Join(lines, "\n")
}
func (m Model) railSlotAt(y int) int {
	if index := y - 2 + m.railOffset(); y >= 2 && index >= 0 && index < len(m.slots) {
		return index
	}
	return -1
}
