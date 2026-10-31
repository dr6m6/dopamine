// Package projecthub manages dopamine projects and terminal workspaces. It never
// changes Codex's user configuration, browser profiles, or existing processes.
package projecthub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type Project struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	GitHub    string   `json:"github,omitempty"`
	Launch    []string `json:"launch,omitempty"`
	LaunchVPN bool     `json:"launch_vpn,omitempty"`
	AppURL    string   `json:"app_url,omitempty"`
}

type Registry struct {
	Projects    []Project `json:"projects"`
	ReservedIDs []string  `json:"reserved_project_ids,omitempty"`
}

type Service struct {
	Home, ConfigDir, StateDir, CodexHome, ProjectsRoot string
	Executable, Installer                              string
	ReadSkillFlags                                     func(string, []string) (map[string]bool, error)
}

func Default() Service {
	home, _ := os.UserHomeDir()
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local/state")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	exe, _ := os.Executable()
	if v := os.Getenv("PROJECT_HUB_EXECUTABLE"); v != "" {
		exe = v
	}
	s := Service{Home: home, ConfigDir: filepath.Join(config, "project-hub"), StateDir: filepath.Join(state, "project-hub"), CodexHome: codex, ProjectsRoot: filepath.Join(home, "projects"), Executable: exe}
	if v := os.Getenv("PROJECT_HUB_CONFIG_DIR"); v != "" {
		s.ConfigDir = v
	}
	if v := os.Getenv("PROJECT_HUB_STATE_DIR"); v != "" {
		s.StateDir = v
	}
	if v := os.Getenv("PROJECT_HUB_PROJECTS_ROOT"); v != "" {
		s.ProjectsRoot = v
	}
	s.Installer = filepath.Join(codex, "skills/.system/skill-installer/scripts/install-skill-from-github.py")
	return s
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s Service) RegistryPath() string { return filepath.Join(s.ConfigDir, "projects.json") }

func (s Service) Load() (Registry, error) {
	b, err := os.ReadFile(s.RegistryPath())
	if errors.Is(err, os.ErrNotExist) {
		return s.Discover(), nil
	}
	if err != nil {
		return Registry{}, err
	}
	r, err := s.parseRegistry(b)
	if err != nil {
		return r, err
	}
	return s.discover(r), nil
}

func (s Service) parseRegistry(b []byte) (Registry, error) {
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("projects.json: %w", err)
	}
	seen := map[string]bool{}
	for i := range r.Projects {
		p := &r.Projects[i]
		if !validID.MatchString(p.ID) || seen[p.ID] {
			return r, fmt.Errorf("invalid or duplicate project ID %q", p.ID)
		}
		seen[p.ID] = true
		p.Path = s.Expand(p.Path)
		if !filepath.IsAbs(p.Path) {
			return r, fmt.Errorf("project %s requires an absolute path", p.ID)
		}
		if p.Name == "" {
			p.Name = p.ID
		}
	}
	return r, nil
}

func (s Service) Expand(p string) string {
	if p == "~" {
		return s.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(s.Home, p[2:])
	}
	return filepath.Clean(p)
}

// Discover only reads project directories. It neither launches nor configures them.
func (s Service) Discover() Registry {
	return s.discover(Registry{})
}

// Saved entries take precedence. Add discoveries in memory so refresh never
// rewrites user settings or changes the order of already registered projects.
// Missing directories are hidden, but their settings and IDs remain reserved
// so restoring a directory brings back the same project.
func (s Service) discover(r Registry) Registry {
	paths, ids := map[string]bool{}, map[string]bool{}
	for _, id := range r.ReservedIDs {
		ids[id] = true
	}
	visible := make([]Project, 0, len(r.Projects))
	for _, p := range r.Projects {
		paths[projectPathKey(s.Expand(p.Path))] = true
		ids[p.ID] = true
		info, err := os.Stat(p.Path)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
			continue
		}
		visible = append(visible, p)
	}
	r.Projects = visible
	var added []Project
	entries, _ := os.ReadDir(s.ProjectsRoot)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.EqualFold(e.Name(), "octo") || e.Name() == "lost+found" {
			continue
		}
		path := filepath.Join(s.ProjectsRoot, e.Name())
		key := projectPathKey(path)
		if paths[key] {
			continue
		}
		paths[key] = true
		id := regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(e.Name(), "-")
		base := id
		for suffix := 2; ids[id]; suffix++ {
			id = fmt.Sprintf("%s-%d", base, suffix)
		}
		ids[id] = true
		p := Project{ID: id, Name: e.Name(), Path: path}
		// Git only enriches repository metadata; plain folders are projects too.
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			if out, err := command("git", "-C", path, "remote", "get-url", "origin"); err == nil {
				p.GitHub = GitHubURL(strings.TrimSpace(out))
			}
		}
		added = append(added, p)
	}
	sort.Slice(added, func(i, j int) bool { return strings.ToLower(added[i].Name) < strings.ToLower(added[j].Name) })
	r.Projects = append(r.Projects, added...)
	return r
}

// Resolve aliases for discovery deduplication without rewriting saved paths.
func projectPathKey(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func GitHubURL(remote string) string {
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimPrefix(remote, "git@github.com:")
	remote = strings.TrimPrefix(remote, "https://github.com/")
	if regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(remote) {
		return "https://github.com/" + remote
	}
	return ""
}

func AtomicJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hub-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s Service) Save(r Registry) error { return AtomicJSON(s.RegistryPath(), r) }

func (s Service) Project(id string) (Project, error) {
	r, err := s.Load()
	if err != nil {
		return Project{}, err
	}
	for _, p := range r.Projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("unknown project %q", id)
}

func (s Service) lock() (*os.File, error) {
	if err := os.MkdirAll(s.StateDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.StateDir, "actions.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func ShellArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}
