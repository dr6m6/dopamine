package projecthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Skill struct {
	Name, Path, Source, Scope string
	Disabled                  bool
}

type SkillPolicy struct {
	Disabled  []string          `json:"disabled"`
	Links     map[string]string `json:"links,omitempty"`
	Installed map[string]string `json:"installed,omitempty"`
}

func (s Service) policyPath(p Project) string {
	return filepath.Join(s.StateDir, "skills", p.ID+".json")
}
func (s Service) policy(p Project) (SkillPolicy, error) {
	var v SkillPolicy
	b, err := os.ReadFile(s.policyPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}

// Skills uses Codex's native folder discovery, including symlinked folders. It
// lists legacy ~/.codex/skills alongside the current .agents/skills locations.
func (s Service) Skills(p Project) ([]Skill, error) {
	policy, err := s.policy(p)
	if err != nil {
		return nil, err
	}
	disabled := map[string]bool{}
	for _, x := range policy.Disabled {
		disabled[x] = true
	}
	// Prefer Codex's own inventory when using the real installation. A cached
	// plugin directory alone does not prove that a plugin is installed/enabled.
	if home, _ := os.UserHomeDir(); s.Home == home && s.CodexHome == filepath.Join(home, ".codex") && p.Path != "" {
		if raw, e := nativeRPC(p.Path, "skills/list", map[string]any{"cwds": []string{p.Path}, "forceReload": true}, nil); e == nil {
			var response struct {
				Data []struct {
					Skills []struct {
						Name, Path, Scope string
						Enabled           bool
						PluginID          string
					}
					Errors []struct{ Message string }
				}
			}
			if json.Unmarshal(raw, &response) == nil && len(response.Data) > 0 {
				var out []Skill
				for _, group := range response.Data {
					for _, v := range group.Skills {
						source := filepath.Dir(v.Path)
						if real, e := filepath.EvalSymlinks(source); e == nil {
							source = real
						}
						if v.PluginID != "" {
							source = v.PluginID + " · " + source
						}
						if url := policy.Installed[v.Name]; url != "" {
							source = url + " · " + source
						}
						out = append(out, Skill{Name: v.Name, Path: v.Path, Source: source, Scope: v.Scope, Disabled: !v.Enabled || disabled[v.Path]})
					}
				}
				sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
				return out, nil
			}
		}
	}
	var config struct {
		Skills struct {
			Config []struct {
				Path    string
				Enabled *bool
			}
		}
	}
	if b, e := os.ReadFile(filepath.Join(s.CodexHome, "config.toml")); e == nil {
		if _, e = toml.Decode(string(b), &config); e != nil {
			return nil, e
		}
		for _, c := range config.Skills.Config {
			if c.Enabled != nil && !*c.Enabled {
				disabled[c.Path] = true
			}
		}
	}
	type root struct{ path, scope string }
	roots := []root{{filepath.Join(s.Home, ".agents/skills"), "user"}, {filepath.Join(s.CodexHome, "skills"), "user (legacy)"}, {"/etc/codex/skills", "admin"}}
	for dir := p.Path; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if dir == s.Home {
			break
		}
		roots = append(roots, root{filepath.Join(dir, ".agents/skills"), "project"})
	}
	// Plugin bundles may use a version directory or a direct local directory.
	filepath.WalkDir(filepath.Join(s.CodexHome, "plugins/cache"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if d.IsDir() && d.Name() == "skills" {
			roots = append(roots, root{path, "plugin (availability follows Codex)"})
			return filepath.SkipDir
		}
		return nil
	})
	var out []Skill
	seen := map[string]bool{}
	for _, r := range roots {
		entries, e := os.ReadDir(r.path)
		if e != nil {
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			return nil, e
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".system" {
				continue
			}
			if entry.Name() == ".system" {
				rootsystem, _ := os.ReadDir(filepath.Join(r.path, entry.Name()))
				for _, v := range rootsystem {
					roots = append(roots, root{filepath.Join(r.path, ".system", v.Name()), "system"})
				}
				continue
			}
			dir := filepath.Join(r.path, entry.Name())
			file := filepath.Join(dir, "SKILL.md")
			if st, e := os.Stat(file); e != nil || st.IsDir() {
				continue
			}
			if seen[file] {
				continue
			}
			seen[file] = true
			source := dir
			if target, e := filepath.EvalSymlinks(dir); e == nil {
				source = target
			}
			out = append(out, Skill{Name: entry.Name(), Path: file, Source: source, Scope: r.scope + " (filesystem)", Disabled: disabled[file]})
		}
	}
	// System skills have one extra directory level.
	entries, _ := os.ReadDir(filepath.Join(s.CodexHome, "skills/.system"))
	for _, e := range entries {
		p := filepath.Join(s.CodexHome, "skills/.system", e.Name(), "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			out = append(out, Skill{Name: e.Name(), Path: p, Source: filepath.Dir(p), Scope: "system", Disabled: disabled[p]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s Service) ToggleSkill(p Project, skill Skill) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	all, err := s.Skills(p)
	if err != nil {
		return err
	}
	found := false
	for _, v := range all {
		if v.Path == skill.Path {
			found = true
		}
	}
	if !found {
		return errors.New("skill no longer exists")
	}
	v, err := s.policy(p)
	if err != nil {
		return err
	}
	for i, path := range v.Disabled {
		if path == skill.Path {
			v.Disabled = append(v.Disabled[:i], v.Disabled[i+1:]...)
			return AtomicJSON(s.policyPath(p), v)
		}
	}
	v.Disabled = append(v.Disabled, skill.Path)
	return AtomicJSON(s.policyPath(p), v)
}

// Connect follows the official symlink mechanism, never overwrites an existing
// folder, and does not copy or execute any skill code.
func (s Service) ConnectSkill(p Project, skill Skill) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if !validID.MatchString(skill.Name) {
		return errors.New("unsafe skill name")
	}
	if _, err = os.Stat(skill.Path); err != nil {
		return err
	}
	dir, err := safeProjectSkillsDir(p)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err = os.Symlink(filepath.Dir(skill.Path), filepath.Join(dir, skill.Name)); err != nil {
		return err
	}
	policy, err := s.policy(p)
	if err != nil {
		return err
	}
	if policy.Links == nil {
		policy.Links = map[string]string{}
	}
	policy.Links[skill.Name] = filepath.Dir(skill.Path)
	return AtomicJSON(s.policyPath(p), policy)
}

// Rollback only removes Hub-created links. Source skills and unrecognized
// project files are preserved, including skills installed by the user.
func (s Service) DisconnectSkill(p Project, skill Skill) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	dir, err := safeProjectSkillsDir(p)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, skill.Name)
	if path != filepath.Dir(skill.Path) {
		return errors.New("select the project symlink first")
	}
	policy, err := s.policy(p)
	if err != nil {
		return err
	}
	if target, e := os.Readlink(path); e == nil {
		if policy.Links[skill.Name] != target {
			return errors.New("symlink was not created by Hub; preserved")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		delete(policy.Links, skill.Name)
	} else {
		if policy.Installed[skill.Name] == "" {
			return errors.New("skill was not installed by Hub; preserved")
		}
		// ~/Documents is a separate filesystem on this host. Keep rollback
		// beside the project, outside Codex's skill search path, to avoid EXDEV.
		backup := filepath.Join(p.Path, ".agents/project-hub-skill-backups", skill.Name+"-"+fmt.Sprint(time.Now().UnixNano()))
		if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
			return err
		}
		if err = os.Rename(path, backup); err != nil {
			return err
		}
		delete(policy.Installed, skill.Name)
	}
	return AtomicJSON(s.policyPath(p), policy)
}

func (s Service) InstallSkill(p Project, rawURL string) error {
	if !strings.HasPrefix(rawURL, "https://github.com/") || !strings.Contains(rawURL, "/tree/") {
		return errors.New("use a GitHub skill folder URL: https://github.com/owner/repo/tree/COMMIT/path")
	}
	if _, err := os.Stat(s.Installer); err != nil {
		return errors.New("official Codex skill-installer is missing: " + s.Installer)
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	dir, err := safeProjectSkillsDir(p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The official installer refuses existing destination folders. No global
	// Codex config is written, and installation does not enable bundled hooks.
	out, err := processRunner.Output(ctx, "python3", s.Installer, "--url", rawURL, "--dest", dir)
	if err != nil {
		return fmt.Errorf("official installer: %w: %s", err, out)
	}
	name := filepath.Base(strings.TrimRight(strings.Split(rawURL, "?")[0], "/"))
	policy, err := s.policy(p)
	if err != nil {
		return err
	}
	if policy.Installed == nil {
		policy.Installed = map[string]string{}
	}
	policy.Installed[name] = rawURL
	return AtomicJSON(s.policyPath(p), policy)
}

func safeProjectSkillsDir(p Project) (string, error) {
	root, err := filepath.EvalSymlinks(p.Path)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(p.Path, ".agents/skills")
	for _, path := range []string{filepath.Join(p.Path, ".agents"), dir} {
		actual, e := filepath.EvalSymlinks(path)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return "", e
		}
		rel, e := filepath.Rel(root, actual)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("project skills directory resolves outside the project; global changes refused")
		}
	}
	return dir, nil
}

func (s Service) SkillOverridesForCwd(cliArgs ...string) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	r, err := s.Load()
	if err != nil {
		return nil, err
	}
	var selected Project
	for _, p := range r.Projects {
		if id := os.Getenv("PROJECT_HUB_PROJECT_ID"); id != "" && id == p.ID {
			selected = p
			break
		}
		if belongs(cwd, p.Path) && len(p.Path) > len(selected.Path) {
			selected = p
		}
	}
	if selected.ID == "" {
		return nil, nil
	}
	v, err := s.policy(selected)
	if err != nil {
		return nil, err
	}
	if len(v.Disabled) == 0 {
		return nil, nil
	}
	// Preserve effective trusted project/admin layers via Codex's own loader.
	var overrides []string
	for i := 0; i < len(cliArgs); i++ {
		a := cliArgs[i]
		if a == "-p" || a == "--profile" || strings.HasPrefix(a, "--profile=") {
			return nil, errors.New("project skills policy with a CLI profile requires an effective-config review; launch without -p or reset Hub toggles")
		}
		if (a == "-c" || a == "--config") && i+1 < len(cliArgs) {
			i++
			overrides = append(overrides, "-c", cliArgs[i])
		}
		if strings.HasPrefix(a, "--config=") {
			overrides = append(overrides, "-c", strings.TrimPrefix(a, "--config="))
		}
	}
	read := s.ReadSkillFlags
	if read == nil {
		read = effectiveSkillFlags
	}
	flags, err := read(cwd, overrides)
	if err != nil {
		return nil, fmt.Errorf("cannot preserve effective skill config; launch refused: %w", err)
	}
	for _, path := range v.Disabled {
		flags[path] = false
	}
	var paths []string
	for path := range flags {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var values []string
	for _, path := range paths {
		b, _ := json.Marshal(path)
		values = append(values, fmt.Sprintf("{path=%s,enabled=%t}", b, flags[path]))
	}
	return []string{"-c", "skills.config=[" + strings.Join(values, ",") + "]"}, nil
}
