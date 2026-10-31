package projecthub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func notificationFixture(t *testing.T) (Service, Record) {
	t.Helper()
	s, p := fixture(t)
	r := Record{Key: "codex-demo-new", Kind: "codex", Project: p.ID, Pane: "%3", Token: "owned", Log: filepath.Join(s.StateDir, "logs", "codex.log")}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.Log), 0700); err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestNotificationNumberFollowsSlotsAndRejectsStalePanes(t *testing.T) {
	s, r := notificationFixture(t)
	db := dbFixture(t, s)
	if _, err := db.Exec(`INSERT INTO threads (id,source) VALUES (?, 'cli')`, testUUID); err != nil {
		t.Fatal(err)
	}
	older := Record{Key: "codex-older", Kind: "codex", Pane: "%2", Token: "older"}
	if err := AtomicJSON(s.recordPath(older.Key), older); err != nil {
		t.Fatal(err)
	}
	oldVisible, alive := true, true
	var notifications [][]string
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "notify-send" {
			notifications = append(notifications, args)
			return nil, nil
		}
		if name != "tmux" {
			t.Fatalf("unexpected external command %s", name)
		}
		switch args[0] {
		case "display-message":
			if !alive {
				return []byte("recycled|%3|0|project-hub:1.0"), nil
			}
			return []byte("owned|%3|0|project-hub:1.0"), nil
		case "list-panes":
			rows := "@1|%1|dashboard||0\n@3|%3||owned|0"
			if oldVisible {
				rows += "\n@2|%2||older|0"
			}
			return []byte(rows), nil
		default:
			t.Fatalf("notification changed the terminal: %v", args)
		}
		return nil, nil
	}})
	payload := `{"type":"agent-turn-complete","thread-id":"` + testUUID + `","last-assistant-message":"private conversation text"}`
	for _, want := range []string{"done · codex #3 needs you", "done · codex #2 needs you"} {
		if err := s.NotifyCodex(r.Key, r.Token, payload); err != nil {
			t.Fatal(err)
		}
		args := notifications[len(notifications)-1]
		if !reflect.DeepEqual(args, []string{"--app-name=dopamine notifications", "--category=done", "--icon=" + filepath.Join(s.StateDir, "icons", "dopamine.svg"), "--", want}) {
			t.Fatal("wrong application, slot, or private text exposed", args)
		}
		icon, err := os.ReadFile(filepath.Join(s.StateDir, "icons", "dopamine.svg"))
		if err != nil || !bytes.Equal(icon, notificationIconSVG) {
			t.Fatal("notification icon missing or different from the embedded asset", err)
		}
		oldVisible = false
	}
	if _, err := db.Exec(`UPDATE threads SET source='subagent' WHERE id=?`, testUUID); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyCodex(r.Key, r.Token, payload); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyCodex(r.Key, "stale", payload); err == nil {
		t.Fatal("stale instance token accepted")
	}
	for _, event := range []string{`{"type":"unrelated"}`, payload} {
		alive = false
		if err := s.NotifyCodex(r.Key, r.Token, event); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifications) != 2 {
		t.Fatal("stale or unrelated event notified", notifications)
	}
}

func TestCodexNotificationOverridesAreLocalAndValidTOML(t *testing.T) {
	s, r := notificationFixture(t)
	t.Setenv("TMUX_PANE", r.Pane)
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || args[0] != "display-message" {
			t.Fatal(name, args)
		}
		return []byte("owned|%3|0|project-hub:1.0"), nil
	}})
	config := filepath.Join(s.CodexHome, "config.toml")
	before := "model = 'keep-me'\n"
	write(t, config, before)
	args, err := s.codexNotificationOverrides()
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatal("unexpected CLI override", args)
		}
		text.WriteString(args[i+1] + "\n")
	}
	var settings struct {
		Notify []string
		TUI    struct {
			Notifications []string
			Method        string `toml:"notification_method"`
			Condition     string `toml:"notification_condition"`
		}
	}
	if _, err := toml.Decode(text.String(), &settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.Notify, s.notificationCommand("codex-notify", r.Key, r.Token)) || settings.TUI.Method != "osc9" || settings.TUI.Condition != "always" {
		t.Fatal("callback lost its instance identity or desktop delivery", settings)
	}
	if !reflect.DeepEqual(settings.TUI.Notifications, []string{"approval-requested", "async-question", "plan-mode-prompt"}) {
		t.Fatal("question signals must exclude turn-complete previews", settings)
	}
	if data, _ := os.ReadFile(config); string(data) != before {
		t.Fatal("global configuration changed")
	}
}

func TestQuestionScannerHandlesChunkingWrappersAndFalsePositives(t *testing.T) {
	stream := "Question: ordinary prose\a\x1b]0;Question: title\a" +
		"\x1b]9;Question: Choose an option\a" +
		"\x1bPtmux;\x1b\x1b]9;Approval requested: example\a\x1b\\" +
		"\x1b]9;Codex wants to edit file\x1b\\" +
		"\x1b]9;Approval requested by server\a\x1b]9;Plan mode prompt: plan\a" +
		"\x1b]9;Agent turn complete\a\x1b]9;Question: " + strings.Repeat("x", 4200) + "\a"
	for _, size := range []int{1, 2, 3, 32, len(stream)} {
		var scanner codexQuestionScanner
		count := 0
		for start := 0; start < len(stream); start += size {
			count += scanner.feed([]byte(stream[start:min(start+size, len(stream))]))
		}
		if count != 5 {
			t.Fatalf("chunk size %d: got %d question events", size, count)
		}
		if len(scanner.pending) > 4100 {
			t.Fatal("unbounded partial sequence")
		}
	}
}

func TestObserverPreservesRawLogWhenDesktopNotificationsFail(t *testing.T) {
	s, r := notificationFixture(t)
	alerts := 0
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "notify-send" {
			alerts++
			if args[len(args)-1] != "question · codex #2 needs you" {
				t.Error("wrong question category", args)
			}
			return nil, errors.New("no desktop bus")
		}
		if args[0] == "display-message" {
			return []byte("owned|%3|0|project-hub:1.0"), nil
		}
		return []byte("@1|%1|dashboard||0\n@3|%3||owned|0"), nil
	}})
	raw := "normal terminal output\r\n\x1bPtmux;\x1b\x1b]9;Question: Choose\a\x1b\\\x1b[31mremaining output"
	if err := s.ObserveCodex(r.Key, r.Token, strings.NewReader(raw)); err != nil {
		t.Fatal("desktop failure interrupted the logger", err)
	}
	data, err := os.ReadFile(r.Log)
	if err != nil || string(data) != raw || alerts != 1 {
		t.Fatalf("raw output changed or question missing: %q, %d, %v", data, alerts, err)
	}
	info, _ := os.Stat(r.Log)
	if info.Mode().Perm() != 0600 {
		t.Fatal("log permissions changed", info.Mode())
	}
}

func TestMutedNotificationsSuppressQuestionsAndCompletionsAndKeepLogging(t *testing.T) {
	s, r := notificationFixture(t)
	db := dbFixture(t, s)
	if _, err := db.Exec(`INSERT INTO threads (id,source) VALUES (?, 'cli')`, testUUID); err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.ToggleNotifications(); err != nil || enabled {
		t.Fatal("could not mute notifications", enabled, err)
	}
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("muted notification invoked %s %v", name, args)
		return nil, nil
	}})
	payload := `{"type":"agent-turn-complete","thread-id":"` + testUUID + `"}`
	if err := s.NotifyCodex(r.Key, r.Token, payload); err != nil {
		t.Fatal(err)
	}
	if err := s.notifyCodexRecord(r, "question"); err != nil {
		t.Fatal(err)
	}
	raw := "before\r\n\x1b]9;Question: Muted question\aafter"
	if err := s.ObserveCodex(r.Key, r.Token, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(r.Log); err != nil || string(data) != raw {
		t.Fatal("mute interrupted terminal logging", string(data), err)
	}
}
