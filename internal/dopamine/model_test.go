package dopamine

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

func dashboardFixture() Model {
	m := New(workspace.Service{}, false, "")
	m.ready = true
	m.projects = []workspace.Project{{ID: "one", Name: "First", Path: "/tmp/first"}, {ID: "two", Name: "Second", Path: "/tmp/second"}}
	m.apps = map[string]workspace.AppStatus{"one": {State: workspace.Stopped}}
	m.slots = []workspace.Slot{{Dashboard: true}, {Window: "@2"}}
	return m
}

func TestDashboardNavigationAndActionDispatch(t *testing.T) {
	m := dashboardFixture()
	for _, tc := range []struct {
		key     string
		project int
	}{{"down", 1}, {"down", 2}, {"up", 1}, {"up", 0}, {"up", 0}} {
		code := tea.KeyDown
		if tc.key == "up" {
			code = tea.KeyUp
		}
		updated, _ := m.Update(tea.KeyPressMsg{Code: code})
		m = updated.(Model)
		if m.project != tc.project {
			t.Fatalf("%s: project %d", tc.key, m.project)
		}
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !updated.(Model).projectOpen || cmd != nil || updated.(Model).busy {
		t.Fatal("Enter on the project list must only open the project menu")
	}
	m = updated.(Model)
	// Missing URL is a real action error; no browser process is launched.
	m.action = 4
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !updated.(Model).busy || cmd == nil {
		t.Fatal("Enter did not dispatch selected server action")
	}
	result := cmd().(finished)
	if result.err == nil || !strings.Contains(result.err.Error(), "no url configured") {
		t.Fatal(result)
	}
}

func TestViewKeepsActionsAndNumberedRailInsideTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {46, 16}, {15, 5}, {0, 0}} {
		m := dashboardFixture()
		m.width, m.height = size[0], size[1]
		for i := 0; i < 40; i++ {
			m.projects = append(m.projects, workspace.Project{ID: fmt.Sprint(i), Name: "Длинное имя проекта", Path: "/tmp/long"})
		}
		m.project = len(m.projects) - 1
		view := m.View()
		text := ansi.Strip(view.Content)
		if m.height > 0 && len(strings.Split(text, "\n")) > m.height {
			t.Fatalf("overflow at %v: %s", size, text)
		}
		for _, line := range strings.Split(text, "\n") {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("width overflow at %v: %s", size, line)
			}
		}
		if m.width >= 46 && m.height >= 16 {
			for _, label := range []string{"dopamine"} {
				if !strings.Contains(text, label) {
					t.Fatalf("missing %q at %v: %s", label, size, text)
				}
			}
			for _, label := range []string{"start server", "new codex session"} {
				if strings.Contains(text, label) {
					t.Fatalf("project action visible before Enter at %v: %s", size, text)
				}
			}
			m.projectOpen = true
			text = ansi.Strip(m.View().Content)
			for _, label := range []string{"start server", "stop server", "open url", "new codex session"} {
				if !strings.Contains(text, label) {
					t.Fatalf("missing project action %q at %v: %s", label, size, text)
				}
			}
		}
	}
}

func TestProjectActionsRequireExplicitSelectionAndEscapeReturnsToList(t *testing.T) {
	m := dashboardFixture()
	for _, key := range []rune{'s', 'x', 'o', 'n'} {
		updated, cmd := m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
		m = updated.(Model)
		if cmd != nil || m.busy || m.projectOpen {
			t.Fatalf("%c dispatched before project selection", key)
		}
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	if m.action != 1 || m.project != 0 {
		t.Fatal("menu arrows must select actions within the chosen project")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.projectOpen || strings.Contains(m.View().Content, "start server") {
		t.Fatal("Esc did not return to the plain project list")
	}
}

func TestMoleculeHasNoLettersAndRailScrollsPastNine(t *testing.T) {
	if strings.IndexFunc(Molecule, unicode.IsLetter) >= 0 {
		t.Fatal("molecule must not contain chemical labels")
	}
	m := New(workspace.Service{}, true, "@20")
	m.height = 12
	for i := 0; i < 20; i++ {
		m.slots = append(m.slots, workspace.Slot{Window: fmt.Sprintf("@%d", i+1)})
	}
	if !strings.Contains(ansi.Strip(m.rail()), "20") || m.railOffset() == 0 {
		t.Fatal("active workspace beyond 9 is not visible")
	}
}

func TestDashboardAlignsRailAndServerStatusAndHidesHelp(t *testing.T) {
	m := dashboardFixture()
	m.projects[0].Launch = []string{"first-server"}
	m.projects[1].Launch = []string{"second-server"}
	m.projects[1].Name = "Longer project name"
	m.apps["two"] = workspace.AppStatus{State: workspace.Stopped}
	view := ansi.Strip(m.View().Content)
	columns := []int{}
	for _, line := range strings.Split(view, "\n") {
		if column := strings.Index(line, "│"); column >= 0 && column != railWidth {
			t.Fatalf("dashboard border column %d, native tmux border column %d", column, railWidth)
		}
		if column := strings.Index(line, "stopped"); column >= 0 {
			columns = append(columns, column)
		}
	}
	if len(columns) != 2 || columns[0] != columns[1] {
		t.Fatal("server statuses must share a column", columns)
	}
	for _, hint := range []string{"dashboard", "alt+", "s start", "n new", "q detach", "keyboard shortcuts"} {
		if strings.Contains(view, hint) {
			t.Fatalf("persistent keyboard reminder %q", hint)
		}
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m = updated.(Model)
	if !strings.Contains(ansi.Strip(m.View().Content), "keyboard shortcuts") {
		t.Fatal("? did not open help")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(Model).help {
		t.Fatal("Esc did not close help")
	}
}

func TestServerStatusRequiresLaunchCommand(t *testing.T) {
	m := dashboardFixture()
	m.projects[0].Launch = []string{"first-server"}
	m.projects[1].AppURL = "http://localhost:8080/"
	// A URL alone or a stale app record must not label an unconfigured server.
	m.apps["two"] = workspace.AppStatus{State: workspace.Running}
	view := ansi.Strip(m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "First") && !strings.Contains(line, "stopped") {
			t.Fatal("configured server lost its status", line)
		}
		if strings.Contains(line, "Second") && (strings.Contains(line, "running") || strings.Contains(line, "stopped")) {
			t.Fatal("unconfigured project displayed a server status", line)
		}
	}
}

func TestLoadedProjectsGroupConfiguredServersAndKeepSelection(t *testing.T) {
	m := dashboardFixture()
	m.project = 1
	m.projectOpen = true
	projects := []workspace.Project{
		{ID: "one", Name: "First"},
		{ID: "z", Name: "Zulu", Launch: []string{"z-server"}},
		{ID: "two", Name: "Second", AppURL: "http://localhost:8080/"},
		{ID: "a", Name: "Alpha", Launch: []string{"a-server"}},
	}
	updated, _ := m.Update(loaded{projects: projects})
	m = updated.(Model)
	ids := make([]string, len(m.projects))
	for i, p := range m.projects {
		ids[i] = p.ID
	}
	if !slices.Equal(ids, []string{"z", "a", "one", "two"}) {
		t.Fatal("configured servers must come first, preserving order within groups", ids)
	}
	if m.projects[m.project].ID != "two" || !m.projectOpen {
		t.Fatal("refresh changed the selected project or closed its menu", m.project)
	}
}

func TestOnlySuccessfulCodexLaunchReturnsToProjectList(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   finished
		menuOpen bool
	}{
		{"Codex opened", finished{workspaceOpened: true}, false},
		{"Codex failed", finished{err: errors.New("launch failed")}, true},
		{"server started", finished{message: "Server started"}, true},
		{"server stopped", finished{message: "Server stopped"}, true},
		{"URL opened", finished{message: "Opened URL"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := dashboardFixture()
			m.projectOpen, m.busy = true, true
			updated, _ := m.Update(tc.result)
			m = updated.(Model)
			if m.projectOpen != tc.menuOpen || m.busy {
				t.Fatal("incorrect menu state after action", m.projectOpen, m.busy)
			}
		})
	}
}
