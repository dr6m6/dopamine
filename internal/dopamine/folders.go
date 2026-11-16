package dopamine

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dr6m6/dopamine/internal/workspace"
)

type projectAction struct{ key, label string }

func (m Model) projectActions() []projectAction {
	actions := []projectAction{{"n", "new codex session"}, {"t", "new terminal"}, {"s", "start server"}, {"x", "stop server"}, {"o", "open url"}}
	actions = append(actions, projectAction{"c", "new cloud session"}, projectAction{"history", "cloud tasks…"})
	if m.project < len(m.projects) && len(m.projects[m.project].Launch) > 0 {
		actions = append(actions, projectAction{"a", "server autostart: " + workspace.AutostartLabel(m.projects[m.project].ServerAutostart)})
	}
	if m.project < len(m.projects) && m.service.ExternalFolder(m.projects[m.project].Path) {
		actions = append(actions, projectAction{"remove", "remove from list"})
	}
	return actions
}

func (m Model) updateFolderInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.addingFolder, m.status = false, ""
			m.folderInput.Blur()
			return m, nil
		case "enter":
			path := m.folderInput.Value()
			m.busy, m.status = true, "adding folder…"
			return m, func() tea.Msg {
				p, err := m.service.AddFolder(path)
				if err != nil {
					return foldersChanged{err: err}
				}
				r, err := m.service.Load()
				return foldersChanged{projects: r.Projects, projectID: p.ID, err: err}
			}
		case "tab":
			m.completeFolder()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.folderInput, cmd = m.folderInput.Update(msg)
	return m, cmd
}

// Complete just directory names. Filesystem paths are never interpreted by a shell.
func (m *Model) completeFolder() {
	value := m.folderInput.Value()
	if value == "" || value == "~" {
		m.folderInput.SetValue("~/")
		m.folderInput.CursorEnd()
		return
	}
	dir, prefix := filepath.Split(value)
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(m.service.Expand(dir))
	if err != nil {
		m.status = err.Error()
		return
	}
	var matches []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || (strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		info, err := os.Stat(filepath.Join(m.service.Expand(dir), entry.Name()))
		if err == nil && info.IsDir() {
			matches = append(matches, dir+entry.Name()+"/")
		}
	}
	if len(matches) == 0 {
		m.status = "no matching folders"
		return
	}
	completion := []rune(matches[0])
	for _, match := range matches[1:] {
		runes := []rune(match)
		i := 0
		for i < len(completion) && i < len(runes) && completion[i] == runes[i] {
			i++
		}
		completion = completion[:i]
	}
	m.folderInput.SetValue(string(completion))
	m.folderInput.CursorEnd()
	m.status = ""
	if len(matches) > 1 {
		names := make([]string, 0, min(4, len(matches)))
		for _, match := range matches[:min(4, len(matches))] {
			names = append(names, strings.TrimPrefix(match, dir))
		}
		m.status = strings.Join(names, "  ")
	}
}

func (m Model) updateFolderRemoval(key string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	switch key {
	case "esc", "ctrl+c":
		m.removingFolder, m.status = false, ""
	case "enter":
		if m.project >= len(m.projects) {
			m.removingFolder = false
			return m, nil
		}
		id := m.projects[m.project].ID
		m.busy, m.status = true, "removing folder…"
		return m, func() tea.Msg {
			if err := m.service.RemoveFolder(id); err != nil {
				return foldersChanged{err: err}
			}
			r, err := m.service.Load()
			return foldersChanged{projects: r.Projects, err: err}
		}
	}
	return m, nil
}

func (m Model) folderView(width int) string {
	m.folderInput.SetWidth(max(1, width-8))
	return strings.Join([]string{"", "  " + bright.Render("dopamine"), "", "  " + bright.Render("add folder"), "", m.folderInput.View(), "", muted.Render("tab: complete path · enter: add · esc: cancel"), "", clean(m.status)}, "\n")
}
