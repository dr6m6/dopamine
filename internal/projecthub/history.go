package projecthub

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	_ "modernc.org/sqlite"
)

type Session struct {
	ProjectID string `json:"project_id,omitempty"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	Updated   int64  `json:"updated"`
	State     string `json:"state"`
	Active    bool   `json:"active"`
	PID       int    `json:"pid,omitempty"`
	Target    string `json:"target,omitempty"`
	Pane      string `json:"pane,omitempty"`
	Model     string `json:"model,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1500)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

// Sessions is a deliberately read-only compatibility adapter for Codex 0.163's
// local state schema. Unknown schemas fail visibly rather than writing/migrating.
func (s Service) Sessions() ([]Session, error) {
	db, err := openReadOnly(filepath.Join(s.CodexHome, "state_5.sqlite"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,cwd,COALESCE(NULLIF(name,''),title),updated_at,COALESCE(model,''),COALESCE(git_branch,'') FROM threads WHERE archived=0 AND source IN ('cli','vscode') ORDER BY updated_at DESC LIMIT 2000`)
	if err != nil {
		return nil, fmt.Errorf("unsupported Codex state schema: %w", err)
	}
	defer rows.Close()
	owners, ownerErr := s.LockOwners()
	var sessions []Session
	for rows.Next() {
		var t Session
		if err = rows.Scan(&t.ID, &t.Cwd, &t.Title, &t.Updated, &t.Model, &t.Branch); err != nil {
			return nil, err
		}
		t.State = "saved"
		t.PID = owners[t.ID]
		t.Active = t.PID > 0
		if ownerErr != nil {
			t.State = "lock state unavailable"
		} else if t.Active {
			t.State = "external / active"
		}
		sessions = append(sessions, t)
	}
	return sessions, rows.Err()
}

// LockOwners uses kernel lock ownership, never lock-file existence (Codex leaves
// empty files behind). PID namespaces or denied /proc access fail closed.
func (s Service) LockOwners() (map[string]int, error) {
	b, err := os.ReadFile("/proc/locks")
	if err != nil {
		return nil, err
	}
	owners := map[string]int{}
	byInode := map[uint64]int{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[1] != "FLOCK" {
			continue
		}
		parts := strings.Split(f[5], ":")
		if len(parts) != 3 {
			continue
		}
		ino, _ := strconv.ParseUint(parts[2], 10, 64)
		pid, _ := strconv.Atoi(f[4])
		if pid > 0 {
			byInode[ino] = pid
		}
	}
	files, err := filepath.Glob(filepath.Join(s.CodexHome, "thread-writer-locks/*.lock"))
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		ino := st.Sys().(*syscall.Stat_t).Ino
		if pid := byInode[ino]; pid > 0 {
			owners[strings.TrimSuffix(filepath.Base(path), ".lock")] = pid
		}
	}
	return owners, nil
}

// CheckInactive also probes flock, because /proc/locks may be filtered by a PID
// namespace. An unreadable/locked file must never be interpreted as inactive.
func (s Service) CheckInactive(id string) error {
	if !sessionID.MatchString(id) {
		return fmt.Errorf("invalid session UUID")
	}
	f, err := os.Open(filepath.Join(s.CodexHome, "thread-writer-locks", id+".lock"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("session is active in another terminal; finish and close it before resuming: %w", err)
	}
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func belongs(cwd, root string) bool {
	if a, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = a
	}
	if a, err := filepath.EvalSymlinks(root); err == nil {
		root = a
	}
	rel, err := filepath.Rel(root, cwd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ForProject(all []Session, p Project) []Session {
	var out []Session
	for _, t := range all {
		if t.ProjectID == p.ID || (t.ProjectID == "" && belongs(t.Cwd, p.Path)) {
			out = append(out, t)
		}
	}
	return out
}

func sortSessions(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Updated > sessions[j].Updated })
}
