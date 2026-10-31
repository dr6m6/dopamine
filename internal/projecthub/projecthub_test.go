package projecthub

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const testUUID = "11111111-2222-3333-4444-555555555555"

func fixture(t *testing.T) (Service, Project) {
	t.Helper()
	root := t.TempDir()
	s := Service{Home: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), CodexHome: filepath.Join(root, "codex"), ProjectsRoot: filepath.Join(root, "projects"), Executable: "/bin/false"}
	p := Project{ID: "demo", Name: "Demo", Path: filepath.Join(s.ProjectsRoot, "demo")}
	if err := os.MkdirAll(p.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	return s, p
}
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func dbFixture(t *testing.T, s Service) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(s.CodexHome, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(s.CodexHome, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE threads(id TEXT,cwd TEXT,name TEXT,title TEXT,updated_at INTEGER,model TEXT,git_branch TEXT,archived INTEGER,source TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRegistryPreservesAndValidates(t *testing.T) {
	s, p := fixture(t)
	if err := os.MkdirAll(filepath.Join(s.Home, "project with spaces"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, s.RegistryPath(), `{"projects":[{"id":"demo","path":"~/project with spaces","launch":["echo","a;$(false)"],"chatgpt":"https://chatgpt.com/c/example"}]}`)
	r, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if r.Projects[0].Path != filepath.Join(s.Home, "project with spaces") || r.Projects[0].Launch[1] != "a;$(false)" {
		t.Fatal(r)
	}
	p.ID = "../escape"
	if err = s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(); err == nil {
		t.Fatal("unsafe ID accepted")
	}
	if Got := GitHubURL("git@github.com:dr6m6/demo.git"); Got != "https://github.com/dr6m6/demo" {
		t.Fatal(Got)
	}
	if GitHubURL("https://user:password@github.com/a/b.git") != "" {
		t.Fatal("credential-bearing remote exposed")
	}
}

func TestSessionsReadOnlyAndSubagentFiltering(t *testing.T) {
	s, p := fixture(t)
	db := dbFixture(t, s)
	for _, source := range []string{"cli", `{"subagent":{"other":"guardian"}}`} {
		if _, err := db.Exec(`INSERT INTO threads VALUES(?,?,NULL,'title',42,'gpt-test','topic',0,?)`, testUUID, p.Path, source); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := s.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "title" {
		t.Fatal(sessions)
	}
	ro, err := openReadOnly(filepath.Join(s.CodexHome, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err = ro.Exec(`DELETE FROM threads`); err == nil {
		t.Fatal("read-only reader allowed a write")
	}

}

func TestLockChecksDoNotTreatLeftoverFileAsActive(t *testing.T) {
	s, _ := fixture(t)
	path := filepath.Join(s.CodexHome, "thread-writer-locks", testUUID+".lock")
	write(t, path, "")
	if err := s.CheckInactive(testUUID); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckInactive(testUUID); err == nil {
		t.Fatal("active writer was allowed to resume")
	}
	owners, err := s.LockOwners()
	if err != nil {
		t.Fatal(err)
	}
	if owners[testUUID] != os.Getpid() {
		t.Fatalf("lock owner %v", owners)
	}
}

func TestSkillPolicyLinkRollbackAndConfigPreservation(t *testing.T) {
	s, p := fixture(t)
	file := filepath.Join(s.Home, ".agents/skills/test-skill/SKILL.md")
	s.ReadSkillFlags = func(string, []string) (map[string]bool, error) {
		return map[string]bool{"/already/disabled/SKILL.md": false, "/project/disabled/SKILL.md": false}, nil
	}
	write(t, file, "---\nname: test-skill\ndescription: test\n---\n")
	global := filepath.Join(s.CodexHome, "config.toml")
	before := "model = 'keep-me'\n[[skills.config]]\npath = '/already/disabled/SKILL.md'\nenabled = false\n"
	write(t, global, before)
	all, err := s.Skills(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatal(all)
	}
	if err = s.ConnectSkill(p, all[0]); err != nil {
		t.Fatal(err)
	}
	if err = s.ConnectSkill(p, all[0]); err == nil {
		t.Fatal("duplicate link overwrote a file")
	}
	if err = s.ToggleSkill(p, all[0]); err != nil {
		t.Fatal(err)
	}
	t.Chdir(p.Path)
	args, err := s.SkillOverridesForCwd()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || !strings.Contains(args[1], "/already/disabled/") || !strings.Contains(args[1], file) {
		t.Fatal(args)
	}
	if b, _ := os.ReadFile(global); string(b) != before {
		t.Fatal("global config changed")
	}
	projectLink := all[0]
	projectLink.Path = filepath.Join(p.Path, ".agents/skills/test-skill/SKILL.md")
	if err = s.DisconnectSkill(p, projectLink); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Fatal("source lost", err)
	}
	if err = s.ToggleSkill(p, all[0]); err != nil {
		t.Fatal(err)
	}
	args, err = s.SkillOverridesForCwd()
	if err != nil || len(args) != 0 {
		t.Fatal(args, err)
	}
}

func TestURLAndShellInjectionBoundaries(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://chatgpt.com.evil.test/", "https://user@chatgpt.com/", "https://chatgpt.com:444/"} {
		if err := OpenURL(raw, "chatgpt"); err == nil {
			t.Fatal("unsafe URL", raw)
		}
	}

	s, p := fixture(t)
	if err := s.InstallSkill(p, "--repo evil"); err == nil {
		t.Fatal("unsafe install URL")
	}
}

func TestProjectSkillsNeverFollowGlobalDirectorySymlinks(t *testing.T) {
	s, p := fixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(p.Path, ".agents")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(s.Home, ".agents/skills/example/SKILL.md")
	write(t, file, "---\nname: example\ndescription: test\n---\n")
	if err := s.ConnectSkill(p, Skill{Name: "example", Path: file}); err == nil {
		t.Fatal("global directory alias accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("global directory modified")
	}
}

func TestOfficialInstallerProjectOnlyAndRollback(t *testing.T) {
	raw := os.Getenv("PROJECT_HUB_INSTALL_TEST_URL")
	if raw == "" {
		t.Skip("optional single-skill network smoke, project scope only")
	}
	s, p := fixture(t)
	native := Default()
	s.Installer = native.Installer
	if err := s.InstallSkill(p, raw); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(raw)
	file := filepath.Join(p.Path, ".agents/skills", name, "SKILL.md")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if err := s.DisconnectSkill(p, Skill{Name: name, Path: file}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("rollback did not remove project registration", err)
	}
	backups, _ := filepath.Glob(filepath.Join(p.Path, ".agents/project-hub-skill-backups/*/SKILL.md"))
	if len(backups) != 1 {
		t.Fatal("source not preserved in rollback backup", backups)
	}
}

func TestConcurrentProjectLaunchHasOneProcessAndRetainsLog(t *testing.T) {
	isolatedTmux(t)
	s, p := fixture(t)
	marker := filepath.Join(p.Path, "starts")
	p.Launch = []string{"sh", "-c", "echo started >> " + Quote(marker) + "; exec sleep 30"}
	var wg sync.WaitGroup
	results := make(chan Record, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, err := s.Start(p, "app", ""); results <- r; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Record
	for r := range results {
		if first.Key == "" {
			first = r
		}
		if r.Pane != first.Pane {
			t.Fatalf("duplicate process: %v vs %v", first, r)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(marker); e == nil && string(b) == "started\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "started\n" {
		t.Fatal(string(b), err)
	}
	if !liveRecord(first) {
		t.Fatal("created pane not alive")
	}
	if _, err = tmux("move-window", "-s", strings.Split(first.Target, ".")[0], "-t", first.Session+":8"); err != nil {
		t.Fatal(err)
	}
	again, err := s.Start(p, "app", "")
	if err != nil || again.Pane != first.Pane || !strings.Contains(again.Target, ":8.") {
		t.Fatal("renumbering duplicated process", again, err)
	}
	st, err := os.Stat(first.Log)
	if err != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("private log permissions", st, err)
	}
	// A reused target with a different token is never accepted as our process.
	if _, err = tmux("set-option", "-p", "-t", first.Pane, "@project_hub_token", "different"); err != nil {
		t.Fatal(err)
	}
	if liveRecord(first) {
		t.Fatal("recycled target accepted")
	}
}

func TestNativeMetadataProtocol(t *testing.T) {
	if os.Getenv("PROJECT_HUB_NATIVE_TESTS") != "1" {
		t.Skip("requires installed Codex VPN launcher; opt-in metadata-only smoke")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	flags, err := effectiveSkillFlags(cwd, []string{"-c", `skills.config=[{path="/tmp/project-hub-synthetic/SKILL.md",enabled=false}]`})
	if err != nil {
		t.Fatal(err)
	}
	if enabled, exists := flags["/tmp/project-hub-synthetic/SKILL.md"]; !exists || enabled {
		t.Fatal("effective config did not retain disable", flags)
	}
	raw, err := nativeRPC(cwd, "skills/list", map[string]any{"cwds": []string{cwd}, "forceReload": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data []struct {
			Skills []struct {
				Name, Path, Scope string
				Enabled           bool
			}
		}
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || len(result.Data[0].Skills) == 0 {
		t.Fatal("native inventory empty")
	}
	t.Logf("native inventory: %d skills", len(result.Data[0].Skills))
}

func TestCompletedWorktreeRemainsAssignedToProject(t *testing.T) {
	useRunner(t, stubRunner{})
	s, p := fixture(t)
	db := dbFixture(t, s)
	cwd := filepath.Join(t.TempDir(), "codex-worktree")
	if _, err := db.Exec(`INSERT INTO threads VALUES(?,?,NULL,'worktree',42,'gpt-test','topic',0,'cli')`, testUUID, cwd); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(filepath.Join(s.StateDir, "runs/run.json"), Record{Key: "run", Project: p.ID, Kind: "codex", SessionID: testUUID, Pane: "%999999", Token: "gone"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(ForProject(all, p)) != 1 {
		t.Fatal("completed worktree lost its project", all)
	}
	t.Setenv("PROJECT_HUB_PROJECT_ID", p.ID)
	t.Chdir(p.Path)
	if err = s.ToggleSkill(p, Skill{Path: "missing"}); err == nil {
		t.Fatal("uninstalled skill policy accepted")
	}
}

func TestDashboardReopensOnExistingServerWithExplicitEnvironment(t *testing.T) {
	isolatedTmux(t)
	s, p := fixture(t)
	marker := filepath.Join(p.Path, "dashboard-env")
	// Start a server before supplying Hub-specific environment, as happens
	// when users already have unrelated tmux sessions.
	if _, err := tmux("new-session", "-d", "-s", "unrelated"); err != nil {
		t.Fatal(err)
	}
	s.Executable = filepath.Join(p.Path, "dashboard-fixture")
	write(t, s.Executable, "#!/bin/sh\nprintf '%s|%s' \"$PROJECT_HUB_TUI\" \"$PROJECT_HUB_STATE_DIR\" > "+Quote(marker)+"\nexec sleep 30\n")
	if err := os.Chmod(s.Executable, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECT_HUB_STATE_DIR", s.StateDir)
	first, err := s.EnsureDashboard()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(marker); e == nil && string(b) == "1|"+s.StateDir {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "1|"+s.StateDir {
		t.Fatal("environment lost", string(b), e)
	}
	second, err := s.EnsureDashboard()
	if err != nil || first != second {
		t.Fatal("duplicate dashboard", first, second, err)
	}
	// Keep a second window alive while the dashboard exits.
	if _, err = tmux("new-window", "-d", "-t", "project-hub:", "sleep 30"); err != nil {
		t.Fatal(err)
	}
	if _, err = tmux("kill-window", "-t", first); err != nil {
		t.Fatal(err)
	}
	third, err := s.EnsureDashboard()
	if err != nil || third == "" {
		t.Fatal("dashboard did not reopen", third, err)
	}
}
