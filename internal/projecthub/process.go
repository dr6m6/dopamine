package projecthub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Record struct {
	Key, Project, Kind, SessionID, Target, Pane, Token, Log string
	Created                                                 int64
	Session, State, Error, BootID, RootStart, Socket        string
	RootPID                                                 int
	ExitCode                                                *int
}

func tmux(args ...string) (string, error) { return command("tmux", args...) }
func recordTmux(r Record, args ...string) (string, error) {
	if r.Socket != "" {
		args = append([]string{"-S", r.Socket}, args...)
	}
	return tmux(args...)
}
func shortHash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:8]) }

func (s Service) records() ([]Record, error) {
	entries, err := filepath.Glob(filepath.Join(s.StateDir, "runs/*.json"))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var r Record
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// liveRecord verifies a per-window token as well as the pane ID. tmux IDs are
// recycled across server restarts; an old numeric ID is never proof of identity.
func liveRecord(r Record) bool {
	_, ok := resolveRecord(r)
	return ok
}

func resolveRecord(r Record) (Record, bool) {
	out, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@project_hub_token}|#{pane_id}|#{pane_dead}|#{session_name}:#{window_index}.#{pane_index}")
	f := strings.Split(out, "|")
	if err != nil || len(f) != 4 || f[0] != r.Token || f[1] != r.Pane || f[2] != "0" {
		return r, false
	}
	r.Target = f[3]
	return r, true
}

func (s Service) Start(p Project, kind, id string) (Record, error) {
	lock, err := s.lock()
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	info, err := os.Stat(p.Path)
	if err != nil {
		return Record{}, err
	}
	if !info.IsDir() {
		return Record{}, errors.New("working directory is not a folder")
	}
	if kind == "app" || kind == "terminal" {
		return s.startDetached(p, kind)
	}
	worktree := kind == "worktree"
	if worktree {
		kind = "codex"
	}
	if kind != "codex" && kind != "shell" {
		return Record{}, errors.New("unknown launch kind")
	}
	key := kind + "-" + p.ID
	launchDir := p.Path
	if kind == "shell" {
		key += "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if kind == "codex" {
		if id != "" && !sessionID.MatchString(id) {
			return Record{}, errors.New("invalid session UUID")
		}
		if id != "" {
			key += "-" + id
		} else {
			key += "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
	}
	path := filepath.Join(s.StateDir, "runs", key+".json")
	var old Record
	if b, e := os.ReadFile(path); e == nil {
		if json.Unmarshal(b, &old) != nil {
			return Record{}, errors.New("invalid run record")
		}
		if current, ok := resolveRecord(old); ok {
			return current, nil
		}
	}
	if kind == "codex" && id != "" {
		// Also catches records created under a new-session key before its UUID
		// became available, and prevents a second window during startup.
		records, e := s.records()
		if e != nil {
			return Record{}, e
		}
		belongsByRecord := false
		for _, r := range records {
			if r.SessionID == id && r.Project == p.ID {
				belongsByRecord = true
			}
			if r.SessionID == id {
				if current, ok := resolveRecord(r); ok {
					return current, nil
				}
			}
		}
		if err = s.CheckInactive(id); err != nil {
			return Record{}, err
		}
		all, e := s.Sessions()
		if e != nil {
			return Record{}, e
		}
		found := false
		for _, t := range all {
			if t.ID == id {
				found = true
				launchDir = t.Cwd
				if !belongs(t.Cwd, p.Path) && !belongsByRecord {
					return Record{}, errors.New("session belongs to another project")
				}
			}
		}
		if !found {
			return Record{}, errors.New("session is missing or archived")
		}
		if info, e := os.Stat(launchDir); e != nil || !info.IsDir() {
			return Record{}, errors.New("saved working directory is missing; restore it before resuming")
		}
	}
	var args []string
	switch kind {
	case "codex":
		args = []string{s.Executable, "exec-codex"}
		if id != "" {
			args = append(args, "resume", id)
		}
		if worktree {
			args = append(args, "--worktree")
		}
		args = append(s.paneEnvironment(p.ID), args...)
	}
	// Stay on the caller's existing server. When the Hub owns no server yet,
	// create its session without loading or rewriting user tmux configuration.
	if _, err = tmux("has-session", "-t", "=project-hub"); err != nil {
		if _, err = tmux("new-session", "-d", "-s", "project-hub", "-n", "home", "-c", p.Path); err != nil {
			return Record{}, err
		}
	}
	if kind == "shell" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell, err = tmux("show-options", "-gv", "default-shell")
			if err != nil {
				return Record{}, err
			}
		}
		shell, err = exec.LookPath(shell)
		if err != nil {
			return Record{}, fmt.Errorf("interactive shell: %w", err)
		}
		args = append(s.paneEnvironment(p.ID), shell, "-l")
	}
	token := shortHash(key + strconv.FormatInt(time.Now().UnixNano(), 10))
	if _, err = tmux("set-option", "-t", "project-hub", "history-limit", "50000"); err != nil {
		return Record{}, err
	}
	if err = s.ConfigureShortcuts(); err != nil {
		return Record{}, err
	}
	name := p.ID + "-" + kind + "-" + token[:6]
	// Create a gated pane first, configure logging/remain-on-exit, then exec.
	// The gate ensures early exits still produce a diagnostic retained pane.
	gate := "project-hub-" + token
	script := "tmux wait-for " + Quote(gate) + "; exec " + ShellArgs(args)
	out, err := tmux("new-window", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}|#{pane_id}", "-t", "project-hub:", "-n", name, "-c", launchDir, script)
	if err != nil {
		return Record{}, err
	}
	parts := strings.Split(out, "|")
	if len(parts) != 2 {
		return Record{}, errors.New("unexpected tmux response")
	}
	r := Record{Key: key, Project: p.ID, Kind: kind, SessionID: id, Target: parts[0], Pane: parts[1], Token: token, Created: time.Now().Unix(), Log: filepath.Join(s.StateDir, "logs", key+".log")}
	if _, err = tmux("set-option", "-p", "-t", r.Pane, "@project_hub_token", token); err != nil {
		return r, err
	}
	committed := false
	defer func() {
		if !committed && liveRecord(r) {
			_, _ = tmux("kill-pane", "-t", r.Pane)
		}
	}()
	if _, err = tmux("set-option", "-p", "-t", r.Pane, "@project_hub_kind", kind); err != nil {
		return r, err
	}
	if err = os.MkdirAll(filepath.Dir(r.Log), 0700); err != nil {
		return r, err
	}
	if _, err = tmux("set-option", "-w", "-t", r.Target, "remain-on-exit", "on"); err != nil {
		return r, err
	}
	if err = AtomicJSON(path, r); err != nil {
		return r, err
	}
	if kind == "codex" {
		if err = s.configureCodexObserver(r); err != nil {
			return r, err
		}
	} else {
		// pane-died supplies hook_pane, but some tmux versions leave
		// hook_window empty. Capture the stable window ID before launch.
		window, err := tmux("display-message", "-p", "-t", r.Pane, "#{window_id}")
		if err != nil {
			return r, err
		}
		onExit := "if-shell -F " + Quote("#{==:#{hook_pane},"+r.Pane+"}") + " " + Quote("kill-window -t "+window)
		if _, err = tmux("set-hook", "-w", "-t", r.Pane, "pane-died[1001]", onExit); err != nil {
			return r, err
		}
	}
	_, err = tmux("wait-for", "-S", gate)
	committed = err == nil
	return r, err
}

func (s Service) paneEnvironment(project string) []string {
	args := []string{"env", "PROJECT_HUB_CONFIG_DIR=" + s.ConfigDir, "PROJECT_HUB_STATE_DIR=" + s.StateDir, "PROJECT_HUB_PROJECTS_ROOT=" + s.ProjectsRoot, "PROJECT_HUB_PROJECT_ID=" + project}
	for _, key := range []string{"HOME", "PATH", "SHELL", "PROJECT_HUB_EXECUTABLE", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			args = append(args, key+"="+v)
		}
	}
	return args
}

func Jump(target string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+:[0-9]+\.[0-9]+$`).MatchString(target) {
		return errors.New("invalid tmux target")
	}
	if _, err := tmux("switch-client", "-t", strings.Split(target, ":")[0]); err != nil {
		return err
	}
	if _, err := tmux("select-window", "-t", strings.Split(target, ".")[0]); err != nil {
		return err
	}
	_, err := tmux("select-pane", "-t", target)
	return err
}

func parentPID(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := strings.LastIndex(string(b), ")")
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 2 {
		return 0
	}
	p, _ := strconv.Atoi(f[1])
	return p
}
func descendant(pid, root int) bool {
	for i := 0; i < 20 && pid > 1; i++ {
		if pid == root {
			return true
		}
		pid = parentPID(pid)
	}
	return false
}

func (s Service) Snapshot() ([]Session, error) {
	all, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	// A lock owner is authoritative; timestamps/cwd alone are never enough to
	// attach a history UUID to a fresh Codex process.
	for _, r := range records {
		if r.Kind != "codex" {
			continue
		}
		for i := range all {
			if all[i].ID == r.SessionID {
				all[i].ProjectID = r.Project
			}
		}
		current, ok := resolveRecord(r)
		if !ok {
			continue
		}
		r = current
		out, e := tmux("display-message", "-p", "-t", r.Target, "#{pane_pid}")
		if e != nil {
			continue
		}
		root, _ := strconv.Atoi(out)
		matched := false
		for i := range all {
			if all[i].PID > 0 && descendant(all[i].PID, root) {
				all[i].ProjectID = r.Project
				all[i].Target = r.Target
				all[i].Pane = r.Pane
				all[i].State = s.nativeState(all[i].ID, r.Target)
				matched = true
				if r.SessionID != all[i].ID {
					r.SessionID = all[i].ID
					s.enrichRecord(r)
				}
			}
		}
		if !matched {
			all = append(all, Session{ProjectID: r.Project, ID: r.SessionID, Cwd: projectPath(s, r.Project), Title: "Codex: startup / trust prompt", State: "starting", Updated: r.Created, Target: r.Target, Pane: r.Pane, Active: true})
		}
	}
	sortSessions(all)
	return all, nil
}

func (s Service) enrichRecord(r Record) {
	lock, err := s.lock()
	if err != nil {
		return
	}
	defer lock.Close()
	path := filepath.Join(s.StateDir, "runs", r.Key+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var current Record
	if json.Unmarshal(b, &current) == nil && current.Token == r.Token {
		_ = AtomicJSON(path, r)
	}
}

func projectPath(s Service, id string) string { p, _ := s.Project(id); return p.Path }

func (s Service) nativeState(id, target string) string {
	db, err := openReadOnly(filepath.Join(s.CodexHome, "thread_history_1.sqlite"))
	if err != nil {
		return "idle_prompt"
	}
	defer db.Close()
	var status string
	err = db.QueryRow(`SELECT status FROM thread_turns WHERE thread_id=? ORDER BY rollout_ordinal DESC LIMIT 1`, id).Scan(&status)
	if err != nil {
		return "idle_prompt"
	}
	if status == "inProgress" || status == "in_progress" {
		// This is a UI hint, not an approval decision. Actual approvals stay in
		// Codex; Hub never accepts a trust or permission prompt automatically.
		out, _ := tmux("capture-pane", "-p", "-t", target, "-S", "-15")
		if strings.Contains(out, "Would you like") || strings.Contains(out, "approval") {
			return "permission"
		}
		return "running"
	}
	if status == "failed" {
		return "error"
	}
	return "idle_prompt"
}

func OpenURL(raw, kind string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return errors.New("invalid URL")
	}
	ok := false
	switch kind {
	case "github":
		ok = u.Scheme == "https" && u.Host == "github.com"
	case "app":
		_, e := appAddress(raw)
		ok = e == nil
	}
	if !ok {
		return errors.New("unsupported URL for " + kind)
	}
	return processRunner.Run("xdg-open", raw)
}

func appAddress(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("app_url must be local HTTP(S)")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "10.203.31.1" && port == "8420") {
		return "", errors.New("app_url must use loopback or the existing CUTTER VPN relay")
	}
	return net.JoinHostPort(host, port), nil
}

func ProbeApp(raw string) error {
	if _, err := appAddress(raw); err != nil {
		return err
	}
	client := http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodHead, raw, nil)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return fmt.Errorf("service returned HTTP %d", response.StatusCode)
	}
	return nil
}

// ExecCodex always enters the existing fail-closed wrapper. Never fall back to
// the native binary or a direct-internet launch. No shared daemon is attached.
func (s Service) ExecCodex(args []string) error {
	for _, arg := range args {
		if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
			return errors.New("remote/shared server overrides are disabled for Hub sessions; use the existing VPN launcher")
		}
	}
	if os.Getenv("TMUX_PANE") == "" {
		return errors.New("Codex launches from Hub require a tmux pane")
	}
	if s.CodexHome != filepath.Join(s.Home, ".codex") {
		return errors.New("the existing VPN launcher only supports the default ~/.codex; custom CODEX_HOME launch refused")
	}
	launcher := "/run/current-system/sw/bin/codex-vpn"
	if _, err := os.Stat(launcher); err != nil {
		return fmt.Errorf("required VPN launcher unavailable: %w", err)
	}
	// Config overrides persist only for this process. The native CLI still
	// reads the real user configuration; Hub opts into automatic approval review.
	overrides, err := s.SkillOverridesForCwd(args...)
	if err != nil {
		return err
	}
	argv := []string{launcher, "--no-daemon", "--approve-for-me"}
	argv = append(argv, overrides...)
	notifications, err := s.codexNotificationOverrides()
	if err != nil {
		return err
	}
	argv = append(argv, notifications...)
	argv = append(argv, args...)
	return processRunner.Replace(launcher, argv, os.Environ())
}
