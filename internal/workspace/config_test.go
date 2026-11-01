package workspace

import (
	"path/filepath"
	"testing"
)

func TestDefaultUsesDopamineDirectoriesAndEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "DOPAMINE_CONFIG_DIR", "DOPAMINE_STATE_DIR", "DOPAMINE_PROJECTS_ROOT", "DOPAMINE_EXECUTABLE"} {
		t.Setenv(key, "")
	}
	s := Default()
	if s.ConfigDir != filepath.Join(root, ".config/dopamine") || s.StateDir != filepath.Join(root, ".local/state/dopamine") {
		t.Fatalf("unexpected default directories: %+v", s)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	s = Default()
	if s.ConfigDir != filepath.Join(root, "config/dopamine") || s.StateDir != filepath.Join(root, "state/dopamine") {
		t.Fatalf("XDG directories ignored: %+v", s)
	}
	t.Setenv("DOPAMINE_CONFIG_DIR", filepath.Join(root, "custom-config"))
	t.Setenv("DOPAMINE_STATE_DIR", filepath.Join(root, "custom-state"))
	t.Setenv("DOPAMINE_PROJECTS_ROOT", filepath.Join(root, "work"))
	t.Setenv("DOPAMINE_EXECUTABLE", "/custom/bin/dopamine")
	s = Default()
	if s.ConfigDir != filepath.Join(root, "custom-config") || s.StateDir != filepath.Join(root, "custom-state") || s.ProjectsRoot != filepath.Join(root, "work") || s.Executable != "/custom/bin/dopamine" {
		t.Fatalf("dopamine overrides ignored: %+v", s)
	}
}
