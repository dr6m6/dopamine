package projecthub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type stubRunner struct {
	output  func(context.Context, string, ...string) ([]byte, error)
	run     func(string, ...string) error
	replace func(string, []string, []string) error
}

func (r stubRunner) Output(c context.Context, n string, a ...string) ([]byte, error) {
	if r.output != nil {
		return r.output(c, n, a...)
	}
	return nil, errors.New("command unavailable")
}
func (r stubRunner) Run(n string, a ...string) error {
	if r.run != nil {
		return r.run(n, a...)
	}
	return errors.New("unexpected command")
}
func (r stubRunner) Replace(n string, a, e []string) error {
	if r.replace != nil {
		return r.replace(n, a, e)
	}
	return errors.New("unexpected process replacement")
}
func useRunner(t *testing.T, r Runner) {
	t.Helper()
	old := processRunner
	processRunner = r
	t.Cleanup(func() { processRunner = old })
}

func TestLinksUseOnlyTheStandardBrowserHandler(t *testing.T) {
	called := false
	useRunner(t, stubRunner{run: func(name string, args ...string) error {
		called = true
		if name != "xdg-open" || len(args) != 1 || args[0] != "https://github.com/example/project" {
			t.Fatal(name, args)
		}
		return nil
	}})
	if err := OpenURL("https://github.com/example/project", "github"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("browser handler unused")
	}
}

func TestExternalSessionFocusUsesExistingNiriWindow(t *testing.T) {
	focused := false
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "niri" {
			t.Fatal(name)
		}
		if len(args) > 1 && args[1] == "--json" {
			return json.Marshal([]map[string]int{{"id": 42, "pid": os.Getpid()}})
		}
		if len(args) == 5 && args[1] == "action" && args[2] == "focus-window" && args[4] == "42" {
			focused = true
			return nil, nil
		}
		t.Fatal(args)
		return nil, nil
	}})
	if err := FocusExternal(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Fatal("existing window was not selected")
	}
}

func TestCodexCannotAttachToAnUnverifiedRemoteServer(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{})
	if err := s.ExecCodex([]string{"--remote", "unix:///tmp/other-daemon"}); err == nil {
		t.Fatal("remote override accepted")
	}
}

func TestCodexLaunchUsesAutomaticApprovalReview(t *testing.T) {
	const launcher = "/run/current-system/sw/bin/codex-vpn"
	if _, err := os.Stat(launcher); err != nil {
		t.Skip("requires the existing NixOS VPN launcher")
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "new session"},
		{name: "worktree", args: []string{"--worktree"}},
		{name: "resume", args: []string{"resume", testUUID}},
		{name: "prompt and model", args: []string{"--model", "gpt-5.5", "hello; $(false)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := fixture(t)
			s.CodexHome = filepath.Join(s.Home, ".codex")
			t.Chdir(p.Path)
			t.Setenv("TMUX_PANE", "%42")
			called := false
			useRunner(t, stubRunner{replace: func(path string, args, env []string) error {
				called = true
				want := append([]string{launcher, "--no-daemon", "--approve-for-me"}, tc.args...)
				if path != launcher || !slices.Equal(args, want) {
					t.Fatalf("launch = %q %q, want %q %q", path, args, launcher, want)
				}
				if !slices.Contains(env, "TMUX_PANE=%42") {
					t.Fatal("tmux environment lost")
				}
				return nil
			}})
			if err := s.ExecCodex(tc.args); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("VPN launcher was not invoked")
			}
		})
	}
}
