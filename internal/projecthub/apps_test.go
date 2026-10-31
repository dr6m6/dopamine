package projecthub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAppStatusRestoresLiveProcessWithoutCodexAndDetectsMissingPane(t *testing.T) {
	s, p := fixture(t)
	r := Record{Key: "app-demo", Project: p.ID, Kind: "app", Pane: "%3", Token: "instance", State: Starting, Created: time.Now().Unix(), Log: "diagnostics.log"}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		t.Fatal(err)
	}
	live := true
	useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n != "tmux" {
			t.Fatalf("unexpected external action %s", n)
		}
		if live {
			return []byte("instance|%3|0|hub-app:0.0"), nil
		}
		return nil, errors.New("missing pane")
	}})
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Running {
		t.Fatal(st)
	}
	live = false
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Failed || st.Error == "" || st.Log != "diagnostics.log" {
		t.Fatal(st)
	}
	// A fresh service instance reads the persisted failure.
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Failed {
		t.Fatal(st)
	}
}

func TestAppExitRetainsFailureAndOnlyRemovesOwnedSession(t *testing.T) {
	for _, code := range []string{"0", "7", ""} {
		t.Run("code="+code, func(t *testing.T) {
			s, _ := fixture(t)
			r := Record{Key: "app-demo", Project: "demo", Kind: "app", Pane: "%9", Session: "$5", Token: "own", State: Starting}
			AtomicJSON(s.recordPath(r.Key), r)
			killed := false
			useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
				if n != "tmux" {
					t.Fatal(n)
				}
				switch a[0] {
				case "display-message":
					if strings.Contains(a[len(a)-1], "pane_id") {
						return []byte("own|%9|1|hub-app:0.0"), nil
					}
					return []byte("own|1|" + code + "|TERM"), nil
				case "show-options":
					return []byte("own"), nil
				case "kill-session":
					if len(a) != 3 || a[2] != "$5" {
						t.Fatal(a)
					}
					killed = true
					return nil, nil
				}
				t.Fatalf("unexpected tmux command %v", a)
				return nil, nil
			}})
			if err := s.AppExited(r.Key, r.Token); err != nil {
				t.Fatal(err)
			}
			got, _ := s.readRecord(r.Key)
			want := Failed
			if code == "0" {
				want = Stopped
			}
			if got.State != want || !killed {
				t.Fatal(got, killed)
			}
			if code == "7" && (got.ExitCode == nil || *got.ExitCode != 7 || got.Error == "") {
				t.Fatal(got)
			}
			if err := s.AppExited(r.Key, "other"); err == nil {
				t.Fatal("stale callback accepted")
			}
		})
	}
}

func TestAppStartDetachedNoKittyNoClientSwitchAndDuplicateReuse(t *testing.T) {
	s, p := fixture(t)
	p.Launch = []string{"printf", "a;$(false)"}
	created := 0
	exists := false
	useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n != "tmux" {
			t.Fatalf("application start invoked %s", n)
		}
		if len(a) > 2 && a[0] == "-S" {
			if a[1] != "/tmp/app-server" {
				t.Fatal(a)
			}
			a = a[2:]
		}
		switch a[0] {
		case "new-session":
			created++
			joined := strings.Join(a, " ")
			if !strings.Contains(joined, " -d ") || !strings.Contains(joined, "-c "+p.Path) {
				t.Fatal(a)
			}
			return []byte("$3|hub-app:0.0|%7|99999999|/tmp/app-server"), nil
		case "set-hook":
			if a[4] != "pane-died[0]" {
				t.Fatalf("hook must use a numeric array index for tmux 3.6: %v", a)
			}
		case "new-window", "switch-client", "select-window", "attach-session":
			t.Fatalf("start switched UI: %v", a)
		case "display-message":
			if exists {
				r, _ := s.readRecord("app-demo")
				return []byte(r.Token + "|%7|0|hub-app:0.0"), nil
			}
			return nil, errors.New("absent")
		case "wait-for":
			exists = true
		}
		return nil, nil
	}})
	first, err := s.Start(p, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Start(p, "app", "")
	if err != nil || second.Pane != first.Pane || created != 1 {
		t.Fatal(second, err, created)
	}
	if first.Session != "$3" || first.State != Starting {
		t.Fatal(first)
	}
}

func TestKittyExplicitAttachUsesSocketAndClearsNesting(t *testing.T) {
	p := Project{Name: "Demo", Path: "/tmp/project with spaces"}
	r := Record{Pane: "%5", Token: "own", Socket: "/tmp/isolated-server"}
	opened := false
	useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n == "tmux" {
			if len(a) < 3 || a[0] != "-S" || a[1] != "/tmp/isolated-server" {
				t.Fatalf("wrong server after Hub restart %v", a)
			}
			if strings.Contains(a[len(a)-1], "socket_path") {
				return []byte("/tmp/isolated-server"), nil
			}
			return []byte("own|%5|0|hub-app:0.0"), nil
		}
		if n != "kitty" {
			t.Fatal(n)
		}
		want := []string{"--detach", "--title", "Project Hub · Demo", "--directory", p.Path, "env", "-u", "TMUX", "-u", "TMUX_PANE", "tmux", "-S", "/tmp/isolated-server", "attach-session", "-t", "hub-app:0.0"}
		if fmt.Sprint(a) != fmt.Sprint(want) {
			t.Fatal(a)
		}
		opened = true
		return nil, nil
	}})
	if err := OpenRecordTerminal(p, r); err != nil || !opened {
		t.Fatal(err, opened)
	}
}

func TestTerminateOwnedTreeIncludingEscapedChildPreservesUnrelatedProcess(t *testing.T) {
	if os.Getenv("PROJECT_HUB_PROCESS_TESTS") != "1" {
		t.Skip("opt-in real subprocess shutdown")
	}
	root := t.TempDir()
	childPath := filepath.Join(root, "child")
	cmd := exec.Command("sh", "-c", "trap '' INT TERM; setsid sh -c 'trap \"\" INT TERM; echo $$ > "+Quote(childPath)+"; exec sleep 60' & wait")
	cmd.Env = append(os.Environ(), "PROJECT_HUB_APP_TOKEN=test-owned-tree")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	unrelated := exec.Command("sleep", "60")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	r := Record{RootPID: cmd.Process.Pid, RootStart: processStart(cmd.Process.Pid), BootID: bootID(), Token: "test-owned-tree"}
	var child int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(childPath)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if child > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child <= 1 {
		t.Fatal("child did not start")
	}
	defer signalApp(r, syscall.SIGKILL)
	if !ownedAppPID(r, child) || ownedAppPID(r, unrelated.Process.Pid) {
		t.Fatal("ownership is wrong")
	}
	if err := terminateApp(r, 0); err != nil {
		t.Fatal(err)
	}
	if ownedAppPID(r, child) || processStart(unrelated.Process.Pid) == "" {
		t.Fatal("child survived or unrelated process stopped")
	}
	r.RootStart = "recycled"
	if ownedAppPID(r, unrelated.Process.Pid) {
		t.Fatal("recycled identity accepted")
	}
}

func TestServiceHealthStartingFailureRecoveryAndNoDuplicate(t *testing.T) {
	s, p := fixture(t)
	p.AppURL = "http://127.0.0.1:54321/"
	p.LaunchVPN = true
	r := Record{Key: "app-demo", Kind: "app", Project: p.ID, Pane: "%2", Token: "own", State: Starting, Created: time.Now().Unix()}
	AtomicJSON(s.recordPath(r.Key), r)
	healthy := false
	useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n == "tmux" {
			if a[0] != "display-message" {
				t.Fatalf("duplicate launch: %v", a)
			}
			return []byte("own|%2|0|hub-app:0.0"), nil
		}
		if n != "/run/current-system/sw/bin/vpn-run" {
			t.Fatal(n)
		}
		if !healthy {
			return nil, errors.New("HTTP 503")
		}
		return nil, nil
	}})
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Starting {
		t.Fatal(st)
	}
	r.Created = time.Now().Add(-time.Minute).Unix()
	AtomicJSON(s.recordPath(r.Key), r)
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Failed || st.Error == "" {
		t.Fatal(st)
	}
	if reused, err := s.Start(p, "app", ""); err != nil || reused.Pane != r.Pane {
		t.Fatal(reused, err)
	}
	healthy = true
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Running {
		t.Fatal(st)
	}
}

// The test binary acts only as the native pane-died hook callback, so opt-in
// integration tests exercise the same lifecycle without a daemon or Go rebuild.
func TestAppExitHookHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "app-exited" && len(os.Args) == i+3 {
			if err := Default().AppExited(os.Args[i+1], os.Args[i+2]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func TestDetachedLifecycleIntegration(t *testing.T) {
	isolatedTmux(t)
	s, p := fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	callback := filepath.Join(t.TempDir(), "hub-hook")
	write(t, callback, "#!/bin/sh\nexec "+Quote(exe)+" -test.run=TestAppExitHookHelper -- \"$@\"\n")
	os.Chmod(callback, 0700)
	s.Executable = callback
	// Preserve a separate sentinel session on this isolated server.
	if _, err = tmux("new-session", "-d", "-s", "sentinel", "sleep 90"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(p.Path, "child")
	p.Launch = []string{"sh", "-c", "sleep 90 & echo $! > " + Quote(marker) + "; wait"}
	first, err := s.Start(p, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Target, "hub-app-") {
		t.Fatal(first)
	}
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Running {
		t.Fatal(st)
	}
	// Recreate the service object (no in-memory state) as after closing the Hub.
	restored := Service{Home: s.Home, ConfigDir: s.ConfigDir, StateDir: s.StateDir, CodexHome: s.CodexHome, ProjectsRoot: s.ProjectsRoot, Executable: s.Executable}
	if st := restored.AppStatuses([]Project{p})[p.ID]; st.State != Running {
		t.Fatal(st)
	}
	again, err := restored.Start(p, "app", "")
	if err != nil || again.Pane != first.Pane {
		t.Fatal(again, err)
	}
	if err = restored.StopApp(p); err != nil {
		t.Fatal(err)
	}
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Stopped {
		t.Fatal(st)
	}
	if _, err = tmux("has-session", "-t", first.Session); err == nil {
		t.Fatal("application session leaked")
	}
	if pids := appPIDs(first); len(pids) != 0 {
		t.Fatal("processes leaked", pids)
	}
	if _, err = tmux("has-session", "-t", "=sentinel"); err != nil {
		t.Fatal("unrelated session stopped", err)
	}
	// Fast failures must retain code/log and release their own session even when
	// no Hub is polling: the native pane-died hook does the cleanup.
	p.Launch = []string{"sh", "-c", "echo diagnostic-marker; exit 7"}
	failed, err := s.Start(p, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var record Record
	for time.Now().Before(deadline) {
		record, _ = s.readRecord(failed.Key)
		if record.State == Failed {
			if _, e := tmux("has-session", "-t", failed.Session); e != nil {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if record.State != Failed || record.ExitCode == nil || *record.ExitCode != 7 {
		t.Fatal(record)
	}
	if b, e := os.ReadFile(failed.Log); e != nil || !strings.Contains(string(b), "diagnostic-marker") {
		t.Fatal("failure log lost", string(b), e)
	}
	if _, e := tmux("has-session", "-t", failed.Session); e == nil {
		t.Fatal("failed session leaked")
	}
}

func TestLostTmuxConnectionDoesNotStopOrDuplicateLiveApplication(t *testing.T) {
	s, p := fixture(t)
	r := Record{Key: "app-demo", Project: p.ID, Kind: "app", Pane: "%7", Token: "own", State: Running, RootPID: os.Getpid(), RootStart: processStart(os.Getpid()), BootID: bootID()}
	AtomicJSON(s.recordPath(r.Key), r)
	useRunner(t, stubRunner{output: func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n != "tmux" || a[0] != "display-message" {
			t.Fatalf("observation attempted mutation: %s %v", n, a)
		}
		return nil, errors.New("tmux connection unavailable")
	}})
	if st := s.AppStatuses([]Project{p})[p.ID]; st.State != Running || st.Error == "" {
		t.Fatal(st)
	}
	if err := s.AppExited(r.Key, r.Token); err == nil {
		t.Fatal("cleanup accepted a live root")
	}
	reused, err := s.Start(p, "app", "")
	if err != nil || reused.RootPID != r.RootPID {
		t.Fatal(reused, err)
	}
}
