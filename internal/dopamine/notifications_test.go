package dopamine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

func TestNotificationRowNavigatesTogglesAndPreservesSelectionOnRefresh(t *testing.T) {
	m := dashboardFixture()
	root := t.TempDir()
	m.service = workspace.Service{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	m.project = len(m.projects) + 1
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	if m.project != len(m.projects)+2 || cmd != nil {
		t.Fatal("notification row is not reachable after add folder")
	}
	if !strings.Contains(ansi.Strip(m.dashboard(70)), "+ add folder…\n\n> notifications: on") {
		t.Fatal("notifications must be below the folder action with a gap")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.busy || m.projectOpen {
		t.Fatal("Enter did not dispatch the toggle")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !m.notificationsMuted || m.busy || !strings.Contains(ansi.Strip(m.dashboard(70)), "> notifications: off") {
		t.Fatal("row did not reflect saved mute state", m.status)
	}
	projects := append(m.projects, workspace.Project{ID: "new", Name: "New"})
	updated, _ = m.Update(loaded{projects: projects, notificationsMuted: true})
	m = updated.(Model)
	if m.project != len(projects)+2 || !m.notificationsMuted {
		t.Fatal("refresh moved notification selection or reset mute")
	}
	updated, _ = m.Update(notificationsToggled{err: errors.New("write failed")})
	m = updated.(Model)
	if !m.notificationsMuted || m.status != "write failed" {
		t.Fatal("failed save silently changed the displayed state")
	}
}
