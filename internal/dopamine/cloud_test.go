package dopamine

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

func TestCloudRailUsesOneRowForEveryWorkspace(t *testing.T) {
	m := dashboardFixture()
	m.height = 16
	m.slots = []workspace.Slot{{Dashboard: true}, {Kind: workspace.NativeCloudKind, Window: "@2", CloudState: "running"}, {Kind: "shell", Window: "@3"}, {Kind: "cloud", Window: "@4", CloudState: "ready"}}
	rows := strings.Split(ansi.Strip(m.rail()), "\n")
	if len(rows) != 6 || !strings.Contains(rows[3], "2c~") || !strings.Contains(rows[4], "3") || !strings.Contains(rows[5], "4c✓") {
		t.Fatal(rows)
	}
	for y, want := range map[int]int{2: 0, 3: 1, 4: 2, 5: 3, 6: -1, 1: -1} {
		if got := m.railSlotAt(y); got != want {
			t.Fatal(y, got, want)
		}
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > railWidth {
			t.Fatal(row)
		}
	}
	for i := 0; i < 30; i++ {
		m.slots = append(m.slots, workspace.Slot{Kind: workspace.NativeCloudKind, Window: fmt.Sprintf("@%d", i+5)})
	}
	m.sidebar, m.window = true, "@34"
	text := ansi.Strip(m.rail())
	if !strings.Contains(text, "34c") || len(strings.Split(text, "\n")) > 16 {
		t.Fatal(text)
	}
}
func TestOpeningHistoryAndRemovingProjectReleaseNestedScreens(t *testing.T) {
	m := dashboardFixture()
	m.projectOpen, m.cloudHistory = true, true
	updated, _ := m.Update(finished{workspaceOpened: true})
	m = updated.(Model)
	if m.cloudHistory || m.projectOpen {
		t.Fatal("hidden history steals keys")
	}
	m.projectOpen, m.serverSettings = true, true
	updated, _ = m.Update(loaded{})
	m = updated.(Model)
	if m.serverSettings || m.cloudHistory || m.projectOpen {
		t.Fatal("missing project keeps child screen")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatal("empty registry did not keep global terminal action reachable")
	}
}
func TestCloudRecoveryControlsStayVisibleWithLongErrors(t *testing.T) {
	for _, size := range [][2]int{{40, 16}, {74, 24}, {94, 30}} {
		m := NewCloud(workspace.Service{}, workspace.Project{Name: "Example"}, "cloud-demo-test")
		m.task = workspace.CloudTask{Key: m.key, State: "submission uncertain", Detail: strings.Repeat("long diagnostic ", 1000), Error: "check codex cloud"}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(CloudModel)
		text := ansi.Strip(m.View().Content)
		if !strings.Contains(text, "task:") || !strings.Contains(text, "enter: attach") {
			t.Fatal(text)
		}
		if len(strings.Split(text, "\n")) > size[1] {
			t.Fatal(text)
		}
		for _, row := range strings.Split(text, "\n") {
			if ansi.StringWidth(row) > size[0] {
				t.Fatal(row)
			}
		}
	}
}
func TestCloudFormDoesNotSendWithMissingFieldsAndShowsSaveErrors(t *testing.T) {
	m := NewCloud(workspace.Service{}, workspace.Project{}, "cloud-demo-test")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd != nil || updated.(CloudModel).busy {
		t.Fatal("empty request submitted")
	}
	m = updated.(CloudModel)
	updated, _ = m.Update(cloudUpdated{err: errors.New("settings are read only")})
	if updated.(CloudModel).task.Key != "" || updated.(CloudModel).busy || !strings.Contains(updated.(CloudModel).View().Content, "settings are read only") {
		t.Fatal(updated)
	}
}
