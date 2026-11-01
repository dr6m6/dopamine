package workspace

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestHubShortcutsPreserveRootAndUseNativeConfirmation(t *testing.T) {
	s, _ := fixture(t)
	var closeBinding, quitBinding []string
	cloned := false
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Fatal(name)
		}
		switch args[0] {
		case "list-keys":
			if args[2] == "root" {
				return []byte("bind-key -T root M-1 select-window -t :0\nbind-key -r -T root M-2 select-window -t :1"), nil
			}
			return nil, errors.New("table does not exist yet")
		case "unbind-key":
			t.Fatal("must not unbind a missing table or the user's root table")
		case "source-file":
			b, err := os.ReadFile(args[1])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "-T root ") || !strings.Contains(string(b), "-T "+dopamineKeyTable+" M-1") || !strings.Contains(string(b), "-r -T "+dopamineKeyTable+" M-2") {
				t.Fatalf("user bindings not cloned safely: %s", b)
			}
			cloned = true
		case "bind-key":
			if args[2] != dopamineKeyTable {
				t.Fatal("global key table changed", args)
			}
			if args[3] == "C-w" {
				closeBinding = args
			} else if args[3] == "M-S-F4" {
				quitBinding = args
			}
		case "set-option":
			if !slices.Equal(args, []string{"set-option", "-t", "dopamine", "key-table", dopamineKeyTable}) && !slices.Equal(args, []string{"set-option", "-t", "dopamine", "renumber-windows", "on"}) {
				t.Fatal("unrelated session changed", args)
			}
		default:
			t.Fatal(args)
		}
		return nil, nil
	}})
	if err := s.ConfigureShortcuts(); err != nil {
		t.Fatal(err)
	}
	if !cloned || len(closeBinding) != 9 || len(quitBinding) != 7 {
		t.Fatal("missing bindings", closeBinding, quitBinding)
	}
	if !strings.Contains(closeBinding[6], "#{P:") || !strings.Contains(closeBinding[6], "pane_dead") || !strings.Contains(closeBinding[7], "confirm-before -y") || !strings.Contains(closeBinding[7], "enter: yes / esc: no") || !strings.Contains(closeBinding[7], "kill-window -t #{window_id}") || closeBinding[8] != "kill-window" {
		t.Fatal("Codex confirmation or stable tab target missing", closeBinding)
	}
	if !strings.Contains(quitBinding[6], "'kill-dopamine'") || !strings.Contains(quitBinding[6], s.StateDir) {
		t.Fatal("force quit lost executable/environment", quitBinding)
	}
}

func TestKillHubLeavesCodexAppsAndUnrelatedPanesAlone(t *testing.T) {
	s, _ := fixture(t)
	var killed []string
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Fatal(name)
		}
		switch args[0] {
		case "list-panes":
			return []byte("dopamine|dashboard|%10|/old/dopamine\ndopamine|codex|%11|\nhub-app-demo|dashboard|%12|/old/dopamine\ndopamine||%13|\ndopamine|dashboard|%14|"), nil
		case "kill-pane":
			killed = append(killed, args[2])
		default:
			t.Fatal(args)
		}
		return nil, nil
	}})
	if err := s.KillDashboard(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(killed, []string{"%10"}) {
		t.Fatal("wrong panes killed", killed)
	}
}

func TestReopeningHubReplacesOnlyAnOutdatedInterface(t *testing.T) {
	for _, tc := range []struct {
		name, previous, dead string
		wantRestart          bool
	}{
		{"same build", "/new/dopamine", "0", false},
		{"new build", "/old/dopamine", "0", true},
		{"legacy build", "", "0", true},
		{"dead interface", "/new/dopamine", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t)
			s.Executable = "/new/dopamine"
			restarted := false
			useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" {
					t.Fatal(name)
				}
				switch args[0] {
				case "list-panes":
					return []byte("dopamine|2|0|dashboard|" + tc.dead + "|%10|" + tc.previous + "|1\ndopamine|3|0|codex|0|%11||1"), nil
				case "respawn-pane":
					restarted = true
					if len(args) < 6 || args[1] != "-k" || args[3] != "%10" || args[len(args)-1] != s.Executable {
						t.Fatal("wrong process replaced", args)
					}
				case "set-option", "list-keys", "source-file", "bind-key", "unbind-key":
				default:
					t.Fatal("unexpected mutation", args)
				}
				return nil, nil
			}})
			target, err := s.EnsureDashboard()
			if err != nil || target != "dopamine:2.0" || restarted != tc.wantRestart {
				t.Fatal(target, restarted, err)
			}
		})
	}
}

func TestUpdatingLegacySplitDoesNotGuessWhichProcessIsTheHub(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || args[0] != "list-panes" {
			t.Fatal("legacy split mutated", name, args)
		}
		return []byte("dopamine|2|0|dashboard|0|%10||2\ndopamine|2|1|dashboard|0|%11||2"), nil
	}})
	if _, err := s.EnsureDashboard(); err == nil || !strings.Contains(err.Error(), "multiple panes") {
		t.Fatal("legacy split must require an explicit interface choice", err)
	}
}
