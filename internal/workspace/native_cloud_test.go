package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNativeCloudLaunchUsesOriginalCodexAndInvocationOnlySettings(t *testing.T) {
	if _, err := os.Stat(codexVPN); err != nil {
		t.Skip("requires existing VPN launcher")
	}
	s, _ := fixture(t)
	s.CodexHome = filepath.Join(s.Home, ".codex")
	client := filepath.Join(s.StateDir, "native-cloud", "cloud-native-demo-test")
	if err := os.MkdirAll(client, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(client)
	t.Setenv("TMUX_PANE", "%42")
	launched := false
	useRunner(t, stubRunner{output: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no tmux") }, replace: func(path string, args, env []string) error {
		launched = true
		if path != codexVPN || !slices.Contains(args, "--no-daemon") || !slices.Contains(args, "tui_multi_backend") || !slices.Contains(args, `tui.keymap.global.change_host="alt-f12"`) {
			t.Fatal(path, args)
		}
		if slices.Contains(args, "cloud") || slices.Contains(args, "exec") {
			t.Fatal("launched batch task instead of native interactive session", args)
		}
		cd := slices.Index(args, "--cd")
		if cd < 0 || cd+1 >= len(args) || args[cd+1] != client {
			t.Fatal("native client directory was not pinned", args)
		}
		return nil
	}})
	if err := s.ExecNativeCloud(); err != nil || !launched {
		t.Fatal(err, launched)
	}
}

func TestNativeCloudTrustChecksLaunchDirectoryWhenCurrentPathIsUnavailable(t *testing.T) {
	s, _ := fixture(t)
	r := Record{Key: "cloud-native-demo-path", Kind: NativeCloudKind, Pane: "%42", Token: "owned"}
	client := filepath.Join(s.StateDir, "native-cloud", r.Key)
	if err := os.MkdirAll(client, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, start, current string
		ok                   bool
	}{
		{"visible current directory", client, client, true},
		{"sudo hides current directory", client, "", true},
		{"missing launch directory", "", "", false},
		{"different launch directory", s.Home, "", false},
		{"client changed directory", client, s.Home, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[len(args)-1] {
				case "#{pane_current_path}":
					return []byte(tc.current), nil
				case "#{pane_start_path}\n#{pane_current_path}":
					return []byte(tc.start + "\n" + tc.current), nil
				default:
					t.Fatal(args)
					return nil, nil
				}
			}})
			got, err := s.ownedNativeClient(r)
			if (err == nil) != tc.ok || (tc.ok && got != client) {
				t.Fatal(got, err)
			}
		})
	}
}
func TestNativeCloudRequiresAcknowledgementAndSendsOnlyOneHostKey(t *testing.T) {
	s, p := fixture(t)
	r := Record{Key: "cloud-native-demo-test", Kind: NativeCloudKind, Project: p.ID, Pane: "%42", Token: "owned", State: Starting}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		t.Fatal(err)
	}
	sent, screenCalls := 0, 0
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Fatal(name)
		}
		switch args[0] {
		case "display-message":
			return []byte("owned|0|codex | Ready | " + testUUID), nil
		case "capture-pane":
			screenCalls++
			if sent == 0 {
				return []byte("OpenAI Codex\nLocal  alt+f12 to cycle\n›"), nil
			}
			return []byte("OpenAI Codex\nCloud  alt+f12 to cycle\n›"), nil
		case "send-keys":
			sent++
			if !slices.Equal(args, []string{"send-keys", "-t", "%42", "M-F12"}) {
				t.Fatal("unexpected input injected", args)
			}
			return nil, nil
		default:
			t.Fatal(args)
			return nil, nil
		}
	}})
	if err := s.connectNativeCloud(r, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	saved, err := s.readRecord(r.Key)
	if err != nil || saved.State != "idle" || saved.SessionID != testUUID || sent != 1 || screenCalls < 2 {
		t.Fatal(saved, err, sent, screenCalls)
	}
}
func TestNativeCloudFailureReplacesLocalProcessAndKeepsReadableError(t *testing.T) {
	s, p := fixture(t)
	r := Record{Key: "cloud-native-demo-test", Kind: NativeCloudKind, Project: p.ID, Pane: "%42", Token: "owned", State: Starting}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		t.Fatal(err)
	}
	stopped := false
	useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			if strings.Contains(args[len(args)-1], "pane_dead") {
				return []byte("owned|0|codex | Ready"), nil
			}
			return []byte("owned"), nil
		case "capture-pane":
			return []byte("Could not add the Cloud connection. unavailable"), nil
		case "respawn-pane":
			stopped = true
			if !slices.Contains(args, "-k") || !slices.Contains(args, "cloud-failed") {
				t.Fatal(args)
			}
			return nil, nil
		default:
			t.Fatal(args)
			return nil, nil
		}
	}})
	if err := s.connectNativeCloud(r, time.Second, time.Millisecond); err == nil {
		t.Fatal("failed connection accepted")
	}
	saved, _ := s.readRecord(r.Key)
	if !stopped || saved.State != Failed || !strings.Contains(saved.Error, "unavailable") {
		t.Fatal(saved, stopped)
	}
}
func TestNativeCloudStatusComesFromTitleFields(t *testing.T) {
	for title, want := range map[string]string{
		"codex | Ready | " + testUUID:                  "idle",
		"codex | Thinking | " + testUUID:               "running",
		"codex | Working | " + testUUID:                "running",
		"codex | Waiting | " + testUUID:                "running",
		"codex | Ready | " + testUUID + " | 2.4K used": "ready",
		"[ ! ] Action Required | codex | " + testUUID:  "attention",
		"user says Working":                            "unknown",
	} {
		if got := nativeCloudState(title); got != want {
			t.Fatal(title, got, want)
		}
	}
}
func TestNativeCloudDoesNotSendKeysToAReplacedPane(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "display-message" {
			t.Fatal("touched replaced pane", args)
		}
		return []byte("someone-else|0|codex"), nil
	}})
	if err := s.connectNativeCloud(Record{Kind: NativeCloudKind, Token: "owned"}, time.Second, time.Millisecond); err == nil {
		t.Fatal("replaced pane accepted")
	}
}

func TestNativeCloudPreparationCopiesOnlyCredentialFreeOrigin(t *testing.T) {
	s, p := fixture(t)
	write(t, filepath.Join(p.Path, "private.txt"), "private source")
	useRunner(t, execRunner{})
	if _, err := command("git", "init", "-q", p.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := command("git", "-C", p.Path, "remote", "add", "origin", "git@github.com:example/project.git"); err != nil {
		t.Fatal(err)
	}
	client, err := s.prepareNativeCloud(p, "cloud-native-demo-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client, "private.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("project source copied", err)
	}
	remote, err := command("git", "-C", client, "remote", "get-url", "origin")
	if err != nil || remote != "https://github.com/example/project" {
		t.Fatal(remote, err)
	}
	if _, err := s.prepareNativeCloud(p, "../../escape"); err == nil {
		t.Fatal("unsafe directory accepted")
	}
}
func TestUnconfirmedNativeCloudCannotBeFocused(t *testing.T) {
	useRunner(t, stubRunner{output: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("focused unconfirmed cloud")
		return nil, nil
	}})
	for _, state := range []string{"connecting", "error"} {
		if err := FocusSlot(Slot{Kind: NativeCloudKind, CloudState: state, CloudBlocked: true}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeCloudFailureStopsEvenWhenStateCannotBeSaved(t *testing.T) {
	s, _ := fixture(t)
	// A file in place of the runs directory makes AtomicJSON fail deterministically.
	write(t, filepath.Join(s.StateDir, "runs"), "not a directory")
	killed := false
	useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			return []byte("owned"), nil
		case "kill-pane":
			killed = true
			return nil, nil
		default:
			t.Fatal(args)
			return nil, nil
		}
	}})
	if err := s.failNativeCloud(Record{Key: "cloud-native-test", Kind: NativeCloudKind, Pane: "%42", Token: "owned"}, "unavailable"); err == nil || !killed {
		t.Fatal(err, killed)
	}
}
func TestNativeCloudMarksResumedUnknownHostRatherThanClaimingCloud(t *testing.T) {
	s, _ := fixture(t)
	r := Record{Kind: NativeCloudKind, Token: "owned", SessionID: testUUID}
	other := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for _, host := range []string{"local", "cloud", "unknown"} {
		title := "owned|0|||codex | Working | " + other
		useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "display-message" {
				return []byte(title), nil
			}
			if args[0] == "if-shell" {
				return nil, nil
			}
			if args[0] == "capture-pane" {
				if host == "unknown" {
					return []byte("running conversation"), nil
				}
				return []byte(strings.ToUpper(host[:1]) + host[1:] + "  alt+f12 to cycle"), nil
			}
			t.Fatal(args)
			return nil, nil
		}})
		actual, state := s.NativeCloudStatus(r)
		if actual != host || state != "running" {
			t.Fatal(actual, state, host)
		}
	}
}

func TestNativeCloudTrustAcceptsOnlyItsOwnEmptyClient(t *testing.T) {
	s, _ := fixture(t)
	r := Record{Key: "cloud-native-demo-trust", Kind: NativeCloudKind, Pane: "%42", Token: "owned", State: Starting}
	client := filepath.Join(s.StateDir, "native-cloud", r.Key)
	if err := os.MkdirAll(filepath.Join(client, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		t.Fatal(err)
	}
	trusted, switched := false, false
	useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			if args[len(args)-1] == "#{pane_start_path}\n#{pane_current_path}" {
				// The production VPN wrapper uses sudo, hiding the current path.
				return []byte(client + "\n"), nil
			}
			return []byte("owned|0|codex | Ready | " + testUUID), nil
		case "capture-pane":
			if !trusted {
				return []byte("Folder access\nTrust this folder?\n› 1. Trust and continue\n  2. Back"), nil
			}
			if !switched {
				return []byte("Local  alt+f12 to cycle"), nil
			}
			return []byte("Cloud  alt+f12 to cycle"), nil
		case "send-keys":
			if args[len(args)-1] == "Enter" {
				if trusted || switched {
					t.Fatal(args)
				}
				trusted = true
			} else if args[len(args)-1] == "M-F12" {
				if !trusted || switched {
					t.Fatal(args)
				}
				switched = true
			} else {
				t.Fatal(args)
			}
			return nil, nil
		default:
			t.Fatal(args)
			return nil, nil
		}
	}})
	if err := s.connectNativeCloud(r, time.Second, time.Millisecond); err != nil || !trusted || !switched {
		t.Fatal(err, trusted, switched)
	}
	write(t, filepath.Join(client, ".codex/config.toml"), "unsafe=true")
	if _, err := s.ownedNativeClient(r); err == nil {
		t.Fatal("modified client directory accepted")
	}
}

func TestDisconnectedNativeCloudRemainsFocusableForResume(t *testing.T) {
	calls := 0
	useRunner(t, stubRunner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[0] != "select-window" && args[0] != "select-pane" {
			t.Fatal(args)
		}
		return nil, nil
	}})
	if err := FocusSlot(Slot{Kind: NativeCloudKind, CloudState: "error", Window: "@2", Pane: "%42"}); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
