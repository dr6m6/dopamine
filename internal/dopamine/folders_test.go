package dopamine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/projecthub"
)

func TestGlobalActionsHaveGapAndRemainVisibleWithLongLists(t *testing.T) {
	for _, height := range []int{16, 22, 24, 40} {
		m := dashboardFixture()
		m.height = height
		for range 30 {
			m.projects = append(m.projects, projecthub.Project{ID: "last", Name: "Last project"})
		}
		m.project = len(m.projects) + 1
		view := ansi.Strip(m.dashboard(70))
		if !strings.Contains(view, "\n\n  new terminal\n> + add folder…") {
			t.Fatalf("global actions missing their separating blank line at height %d:\n%s", height, view)
		}
		if !strings.Contains(view, "+ add folder…\n\n  notifications: on") {
			t.Fatalf("notifications not visible below actions at height %d:\n%s", height, view)
		}
		if len(strings.Split(view, "\n")) > height {
			t.Fatalf("actions overflow at height %d:\n%s", height, view)
		}
	}
}

func TestFolderInputOwnsTypingPasteAndCompletion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "quite long folder 123")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	m := dashboardFixture()
	m.service.Home = root
	m.project = len(m.projects) + 1
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	for _, r := range "~/q" {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if !m.addingFolder || m.help || m.folderInput.Value() != "~/q" {
		t.Fatal("global shortcuts intercepted the folder input", m.folderInput.Value())
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(Model)
	if m.folderInput.Value() != "~/quite long folder 123/" {
		t.Fatal("Tab did not complete the folder path", m.folderInput.Value())
	}
	updated, _ = m.Update(tea.PasteMsg{Content: "?"})
	m = updated.(Model)
	if !strings.HasSuffix(m.folderInput.Value(), "?") || m.help {
		t.Fatal("paste was intercepted", m.folderInput.Value())
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(Model).addingFolder {
		t.Fatal("Escape did not cancel adding")
	}
}

func TestFolderInputSavesSelectsAndRemovesOnlyRegistration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "external")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s := projecthub.Service{Home: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), ProjectsRoot: filepath.Join(root, "projects")}
	m := New(s, false, "")
	m.ready, m.addingFolder = true, true
	m.folderInput.SetValue(path)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.addingFolder || len(m.projects) != 1 || m.projects[m.project].Path != path {
		t.Fatal("folder did not appear or become selected", m.status)
	}
	m.projectOpen = true
	m.action = len(m.projectActions()) - 1
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if !m.removingFolder {
		t.Fatal("removal did not require confirmation")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.removingFolder || len(m.projects) != 1 {
		t.Fatal("removal cancellation failed")
	}
	m.removingFolder = true
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.removingFolder || len(m.projects) != 0 || m.projectOpen {
		t.Fatal("folder remained after removal", m.status)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removing the registration touched its directory", err)
	}
}

func TestRefreshPreservesGlobalSelectionAndFolderDraft(t *testing.T) {
	m := dashboardFixture()
	m.project = len(m.projects) + 1
	m.addingFolder = true
	m.folderInput.SetValue("~/draft123")
	projects := append(m.projects, projecthub.Project{ID: "new", Name: "New"})
	updated, _ := m.Update(loaded{projects: projects})
	m = updated.(Model)
	if m.project != len(projects)+1 || !m.addingFolder || m.folderInput.Value() != "~/draft123" {
		t.Fatal("refresh moved global selection or discarded the draft")
	}
}
