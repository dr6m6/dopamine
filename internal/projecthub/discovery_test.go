package projecthub

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultProjectsRootAndOverride(t *testing.T) {
	t.Setenv("PROJECT_HUB_PROJECTS_ROOT", "")
	s := Default()
	if s.ProjectsRoot != filepath.Join(s.Home, "projects") {
		t.Fatalf("unexpected default discovery root: %s", s.ProjectsRoot)
	}
	custom := t.TempDir()
	t.Setenv("PROJECT_HUB_PROJECTS_ROOT", custom)
	if got := Default().ProjectsRoot; got != custom {
		t.Fatalf("custom discovery root ignored: %s", got)
	}
}

func TestDiscoverPlainFoldersWithoutRegistryOrGit(t *testing.T) {
	s, p := fixture(t)
	if err := os.Remove(s.RegistryPath()); err != nil {
		t.Fatal(err)
	}
	// Plain directories must not invoke Git, including when Git is unavailable.
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("plain folder discovery invoked %s %v", name, args)
		return nil, nil
	}})
	for _, name := range []string{"empty-project", "files-project"} {
		path := filepath.Join(s.ProjectsRoot, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if name == "files-project" {
			write(t, filepath.Join(path, "nested", "README.md"), "project files")
		}
	}
	for i := 0; i < 2; i++ {
		r, err := s.Load()
		if err != nil || len(r.Projects) != 3 {
			t.Fatalf("plain folders not discovered exactly once: %+v, %v", r, err)
		}
		if r.Projects[0].Path != p.Path || r.Projects[1].ID != "empty-project" || r.Projects[2].ID != "files-project" {
			t.Fatalf("unexpected discovery order or nested entry: %+v", r)
		}
	}
	if _, err := os.Stat(s.RegistryPath()); !os.IsNotExist(err) {
		t.Fatalf("discovery created a registry: %v", err)
	}
}

func TestLoadDeduplicatesLegacyPathsWithoutRewritingSettings(t *testing.T) {
	for _, aliasRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved alias", true: "discovery root alias"}[aliasRoot], func(t *testing.T) {
			s, p := fixture(t)
			alias := filepath.Join(s.Home, "legacy-projects")
			if err := os.Symlink(s.ProjectsRoot, alias); err != nil {
				t.Fatal(err)
			}
			if aliasRoot {
				s.ProjectsRoot = alias
			} else {
				p.Path = filepath.Join(alias, "demo")
			}
			p.ID = "original-id"
			p.Launch = []string{"my-server", "--dev"}
			p.AppURL = "http://127.0.0.1:8080/"
			if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.RegistryPath())
			if err != nil {
				t.Fatal(err)
			}
			// A second legacy name must never become another project entry.
			if err := os.Symlink(p.Path, filepath.Join(s.ProjectsRoot, "demo-alias")); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				r, err := s.Load()
				if err != nil || !reflect.DeepEqual(r.Projects, []Project{p}) {
					t.Fatalf("saved project duplicated or changed: %+v, %v", r, err)
				}
			}
			after, err := os.ReadFile(s.RegistryPath())
			if err != nil || string(before) != string(after) {
				t.Fatalf("saved registry was rewritten: %v", err)
			}
		})
	}
}

func TestLoadDiscoversNewProjectsAndPreservesRegistry(t *testing.T) {
	s, p := fixture(t)
	p.ID = "custom-demo"
	p.Name = "My project"
	p.GitHub = "https://github.com/owner/custom"
	p.Launch = []string{"nix", "develop", "-c", "my-app"}
	p.LaunchVPN = true
	p.AppURL = "http://127.0.0.1:8080/"
	if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := command("git", "init", "--quiet", p.Path); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil || len(r.Projects) != 1 || !reflect.DeepEqual(r.Projects[0], p) {
		t.Fatalf("configured path was duplicated or changed: %+v, %v", r, err)
	}
	path := filepath.Join(s.ProjectsRoot, "bettersc")
	if _, err := command("git", "init", "--quiet", path); err != nil {
		t.Fatal(err)
	}
	if _, err := command("git", "-C", path, "remote", "add", "origin", "https://github.com/owner/bettersc.git"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, err = s.Load()
		if err != nil || len(r.Projects) != 2 {
			t.Fatalf("refresh did not find the new project exactly once: %+v, %v", r, err)
		}
		if !reflect.DeepEqual(r.Projects[0], p) {
			t.Fatalf("saved settings changed: %+v", r.Projects[0])
		}
		want := Project{ID: "bettersc", Name: "bettersc", Path: path, GitHub: "https://github.com/owner/bettersc"}
		if !reflect.DeepEqual(r.Projects[1], want) {
			t.Fatalf("unexpected discovery: %+v", r.Projects[1])
		}
	}
	if _, err := s.Project("bettersc"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.RegistryPath())
	if err != nil || string(after) != string(before) {
		t.Fatalf("refresh rewrote the registry: %v", err)
	}
}

func TestDiscoveryExclusionsAndIDCollisions(t *testing.T) {
	s, p := fixture(t)
	// Reserve the ID of a different directory in the saved registry.
	p.ID = "a-b"
	if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a b", "a-b", "OCTO", ".hidden", "lost+found"} {
		if err := os.MkdirAll(filepath.Join(s.ProjectsRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(s.ProjectsRoot, "plain-folder"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.ProjectsRoot, "regular-file"), "not a project")
	for _, tc := range []struct {
		load  func() (Registry, error)
		count int
	}{
		{s.Load, 4},
		{func() (Registry, error) { return s.Discover(), nil }, 4},
	} {
		r, err := tc.load()
		if err != nil || len(r.Projects) != tc.count {
			t.Fatalf("unexpected projects: %+v, %v", r, err)
		}
		seen := map[string]bool{}
		for _, project := range r.Projects {
			switch project.Name {
			case "OCTO", ".hidden", "lost+found", "regular-file":
				t.Fatalf("excluded entry discovered: %+v", project)
			}
			if seen[project.ID] || !validID.MatchString(project.ID) {
				t.Fatalf("invalid or duplicate ID: %+v", r)
			}
			seen[project.ID] = true
		}
		if !seen["plain-folder"] {
			t.Fatal("plain project directory was not discovered")
		}
	}
}

func TestLoadHidesRemovedProjectsAndRestoresTheirSettings(t *testing.T) {
	for _, replacement := range []string{"missing", "file", "broken symlink"} {
		t.Run(replacement, func(t *testing.T) {
			s, p := fixture(t)
			if err := os.Remove(p.Path); err != nil {
				t.Fatal(err)
			}
			// Registered projects may live outside the discovery root.
			p.Path = filepath.Join(s.Home, "custom-project")
			p.Launch = []string{"my-app", "--dev"}
			p.AppURL = "http://127.0.0.1:8080/"
			if err := os.MkdirAll(p.Path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.RegistryPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p.Path); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "file":
				write(t, p.Path, "not a directory")
			case "broken symlink":
				if err := os.Symlink(filepath.Join(s.Home, "missing-target"), p.Path); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				r, err := s.Load()
				if err != nil || len(r.Projects) != 0 {
					t.Fatalf("removed project remains visible: %+v, %v", r, err)
				}
			}
			if _, err := s.Project(p.ID); err == nil {
				t.Fatal("removed project still available for actions")
			}
			if replacement != "missing" {
				if err := os.Remove(p.Path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(p.Path, 0700); err != nil {
				t.Fatal(err)
			}
			r, err := s.Load()
			if err != nil || len(r.Projects) != 1 || !reflect.DeepEqual(r.Projects[0], p) {
				t.Fatalf("restored settings lost: %+v, %v", r, err)
			}
			after, err := os.ReadFile(s.RegistryPath())
			if err != nil || string(after) != string(before) {
				t.Fatalf("refresh rewrote saved settings: %v", err)
			}
		})
	}
}

func TestLoadRemovesDiscoveredProjectsAfterDirectoryDeletion(t *testing.T) {
	s, p := fixture(t)
	path := filepath.Join(s.ProjectsRoot, "new-project")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil || len(r.Projects) != 2 {
		t.Fatalf("discovery failed: %+v, %v", r, err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	r, err = s.Load()
	if err != nil || len(r.Projects) != 1 || !reflect.DeepEqual(r.Projects[0], p) {
		t.Fatalf("removed discovery remains visible: %+v, %v", r, err)
	}
}
