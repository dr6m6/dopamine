package projecthub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestUnavailableSlotShortcutsAreNoOps(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || args[0] != "list-panes" {
			t.Fatalf("unavailable slot changed the terminal: %s %v", name, args)
		}
		return []byte("@1|%1|dashboard||0"), nil
	}})
	for _, number := range []int{-1, 0, 2, 3, 9} {
		if err := s.FocusSlot(number); err != nil {
			t.Fatalf("missing slot %d must not fail a tmux callback: %v", number, err)
		}
	}
}

func TestSlotShortcutPreservesRealTmuxErrors(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{})
	if err := s.FocusSlot(1); err == nil {
		t.Fatal("tmux failure was hidden")
	}
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "list-panes" {
			return []byte("@1|%1|dashboard||0"), nil
		}
		return nil, errors.New("cannot focus existing pane")
	}})
	if err := s.FocusSlot(1); err == nil {
		t.Fatal("focus failure was hidden")
	}
}

func TestSlotsUseOwnedPaneTokensAndStableOrder(t *testing.T) {
	s, p := fixture(t)
	for _, r := range []Record{
		{Key: "a", Kind: "codex", Project: p.ID, Pane: "%2", Token: "old", Created: 10},
		{Key: "b", Kind: "codex", Project: p.ID, Pane: "%3", Token: "new", Created: 20},
		{Key: "recycled", Kind: "codex", Pane: "%4", Token: "stale"},
		{Key: "server", Kind: "app", Pane: "%5", Token: "server"},
		{Key: "z-stale", Kind: "codex", Pane: "%2", Token: "recycled"},
	} {
		if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
			t.Fatal(err)
		}
	}
	var focus [][]string
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Fatal(name)
		}
		if args[0] == "list-panes" {
			return []byte("@9|%3||new|1\n@2|%1|dashboard||0\n@7|%2||old|0\n@4|%4||reused|0\n@5|%5||server|0\n@7|%6|dopamine-rail||0"), nil
		}
		focus = append(focus, args)
		return nil, nil
	}})
	slots, err := s.Slots()
	if err != nil || len(slots) != 3 {
		t.Fatal(slots, err)
	}
	if !slots[0].Dashboard || slots[1].Pane != "%2" || slots[2].Pane != "%3" || !slots[2].Exited {
		t.Fatal("unstable order or lost exited terminal", slots)
	}
	if err = s.FocusSlot(2); err != nil {
		t.Fatal(err)
	}
	if len(focus) != 2 || !slices.Equal(focus[0], []string{"select-window", "-t", "@7"}) || !slices.Equal(focus[1], []string{"select-pane", "-t", "%2"}) {
		t.Fatal("focused a renumberable target", focus)
	}
}

func TestDopamineRefreshesOnlyRailAndUsesSessionBindings(t *testing.T) {
	s, p := fixture(t)
	if err := AtomicJSON(s.recordPath("codex-demo"), Record{Key: "codex-demo", Kind: "codex", Project: p.ID, Pane: "%2", Token: "token"}); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(s.recordPath("shell-demo"), Record{Key: "shell-demo", Kind: "shell", Project: p.ID, Pane: "%4", Token: "shell-token"}); err != nil {
		t.Fatal(err)
	}
	respawn, bindings, resizeHooks := 0, 0, 0
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Fatal(name)
		}
		switch args[0] {
		case "display-message":
			if args[len(args)-1] != "#{@dopamine_observer}|#{pane_pipe}" || args[3] != "%2" {
				t.Fatal("unexpected observer query", args)
			}
			return []byte(s.Executable + "|token|1"), nil
		case "list-panes":
			if strings.Contains(args[len(args)-1], "@dopamine_executable") {
				if args[2] == "@3" {
					return []byte("%4||/keep/shell|0\n%5|dopamine-rail|" + s.Executable + "|0"), nil
				}
				return []byte("%2||/keep/codex|0\n%3|dopamine-rail|/old/package|0"), nil
			}
			return []byte("@1|%1|dashboard||0\n@2|%2||token|0\n@3|%4||shell-token|0"), nil
		case "respawn-pane":
			if args[3] != "%3" {
				t.Fatal("attempted to replace Codex", args)
			}
			respawn++
		case "bind-key":
			if args[2] != hubKeyTable {
				t.Fatal("global binding changed", args)
			}
			bindings++
		case "split-window", "kill-pane", "kill-window":
			t.Fatal("unexpected destructive operation", args)
		case "set-hook":
			if len(args) != 6 || args[1] != "-w" || (args[3] != "@2" && args[3] != "@3") || !strings.HasSuffix(args[4], "[1000]") {
				t.Fatal("resize hook must be indexed and confined to the Codex window", args)
			}
			rail := "%3"
			if args[3] == "@3" {
				rail = "%5"
			}
			if !strings.Contains(args[5], "-t "+rail) || !strings.Contains(args[5], "#{!=:#{pane_width},5}") || !strings.Contains(args[5], "@project_hub_role") {
				t.Fatal("resize hook lost its marked rail target or recursion guard", args)
			}
			resizeHooks++
		case "set-option", "resize-pane", "rename-window":
		default:
			return nil, fmt.Errorf("unexpected command %v", args)
		}
		return nil, nil
	}})
	if err := s.ConfigureDopamine(); err != nil {
		t.Fatal(err)
	}
	if respawn != 1 || bindings != 12 || resizeHooks != 4 {
		t.Fatal(respawn, bindings, resizeHooks)
	}
}
