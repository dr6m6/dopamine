package workspace

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/dopamine.svg
var notificationIconSVG []byte

// Keep the icon with the application state so notification daemons can load it
// after this short-lived callback exits, including for an unpackaged dev build.
func (s Service) notificationIcon() (string, error) {
	dir := filepath.Join(s.StateDir, "icons")
	path := filepath.Join(dir, "dopamine.svg")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, notificationIconSVG) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".dopamine-*.svg")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(notificationIconSVG)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Codex uses notify for completed turns and filtered OSC 9 notifications for
// questions. These process-local settings require no hook-trust bypass and do
// not change the real user config or the VPN environment allowlist.
func (s Service) codexNotificationOverrides() ([]string, error) {
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.Kind != "codex" || r.Pane != os.Getenv("TMUX_PANE") || !liveRecord(r) {
			continue
		}
		callback, _ := json.Marshal(s.notificationCommand("codex-notify", r.Key, r.Token))
		return []string{
			"-c", "notify=" + string(callback),
			"-c", `tui.notifications=["approval-requested","async-question","plan-mode-prompt"]`,
			"-c", `tui.notification_method="osc9"`,
			"-c", `tui.notification_condition="always"`,
		}, nil
	}
	return nil, nil
}

func (s Service) notificationCommand(action, key, token string) []string {
	return []string{"env", "DOPAMINE_CONFIG_DIR=" + s.ConfigDir,
		"DOPAMINE_STATE_DIR=" + s.StateDir, "DOPAMINE_PROJECTS_ROOT=" + s.ProjectsRoot,
		s.Executable, action, key, token}
}

func (s Service) notificationRecord(key, token string) (Record, error) {
	if !validID.MatchString(key) || token == "" {
		return Record{}, errors.New("invalid notification identity")
	}
	r, err := s.readRecord(key)
	if err != nil {
		return r, err
	}
	if r.Key != key || r.Kind != "codex" || r.Token != token {
		return r, errors.New("notification does not belong to this Codex pane")
	}
	return r, nil
}

// NotifyCodex handles Codex's documented agent-turn-complete JSON argument.
// Conversation text is deliberately excluded from system notifications.
func (s Service) NotifyCodex(key, token, payload string) error {
	var event struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread-id"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return err
	}
	if event.Type != "agent-turn-complete" {
		return nil
	}
	if !sessionID.MatchString(event.ThreadID) {
		return errors.New("invalid completion thread UUID")
	}
	r, err := s.notificationRecord(key, token)
	if err != nil {
		return err
	}
	// Subagents inherit settings but must not report their completion as the
	// user's main Codex finishing. Reuse the existing read-only state adapter.
	db, err := openReadOnly(filepath.Join(s.CodexHome, "state_5.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	var source string
	if err = db.QueryRow(`SELECT source FROM threads WHERE id=?`, event.ThreadID).Scan(&source); err != nil {
		return err
	}
	if source != "cli" && source != "vscode" {
		return nil
	}
	return s.notifyCodexRecord(r, "done")
}

func (s Service) notifyCodexRecord(r Record, kind string) error {
	enabled, err := s.NotificationsEnabled()
	if err != nil || !enabled {
		return err
	}
	if !liveRecord(r) {
		return nil
	}
	slots, err := s.Slots()
	if err != nil {
		return err
	}
	for i, slot := range slots {
		if slot.Pane == r.Pane && slot.Key == r.Key && slot.Token == r.Token {
			icon, err := s.notificationIcon()
			if err != nil {
				return err
			}
			// Noctalia prettifies single lowercase identifiers as title case;
			// a descriptive sender label preserves the requested lowercase.
			_, err = command("notify-send", "--app-name=dopamine notifications", "--category="+kind,
				"--icon="+icon, "--", fmt.Sprintf("%s · codex #%d needs you", kind, i+1))
			return err
		}
	}
	return nil
}

// configureCodexObserver replaces only our own pipe logger, preserving its log
// and leaving the native terminal process and input untouched. The marker makes
// refresh a no-op; an update also repairs older cat-only loggers.
func (s Service) configureCodexObserver(r Record) error {
	marker := s.Executable + "|" + r.Token
	current, err := tmux("display-message", "-p", "-t", r.Pane, "#{@dopamine_observer}|#{pane_pipe}")
	if err == nil && current == marker+"|1" {
		return nil
	}
	if _, err = tmux("pipe-pane", "-t", r.Pane, ShellArgs(s.notificationCommand("observe-codex", r.Key, r.Token))); err != nil {
		return err
	}
	_, err = tmux("set-option", "-p", "-t", r.Pane, "@dopamine_observer", marker)
	return err
}

// ObserveCodex replaces the existing cat logger. It only reacts to Codex's OSC 9
// question signals, never to rendered prose, terminal titles, or BEL alone.
// Notification delivery cannot block the terminal's output or logging.
func (s Service) ObserveCodex(key, token string, input io.Reader) error {
	r, err := s.notificationRecord(key, token)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(r.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	questions := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range questions {
			// A missing desktop bus must never affect Codex's execution.
			_ = s.notifyCodexRecord(r, "question")
		}
	}()
	defer func() { close(questions); <-done }()
	var scanner codexQuestionScanner
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err := log.Write(buffer[:n]); err != nil {
				return err
			}
			count := scanner.feed(buffer[:n])
			if count > 0 {
				enabled, err := s.NotificationsEnabled()
				// Muted events are discarded, while the raw log keeps flowing.
				if err == nil && enabled {
					for ; count > 0; count-- {
						select {
						case questions <- struct{}{}:
						default:
							// Bound a notification flood without delaying terminal output.
						}
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

type codexQuestionScanner struct{ pending []byte }

// Codex 0.166 emits ESC ] 9 ; message BEL, optionally inside a tmux DCS
// wrapper. Searching for the inner OSC also handles escaped ESC in that wrapper.
// Keep only a bounded partial sequence across reads, including split terminators.
func (s *codexQuestionScanner) feed(chunk []byte) int {
	s.pending = append(s.pending, chunk...)
	count := 0
	for {
		start := bytes.Index(s.pending, []byte("\x1b]9;"))
		if start < 0 {
			s.pending = append(s.pending[:0], s.pending[max(0, len(s.pending)-3):]...)
			return count
		}
		s.pending = s.pending[start:]
		body := s.pending[4:]
		end, terminator := bytes.IndexByte(body, '\a'), 1
		if st := bytes.Index(body, []byte("\x1b\\")); st >= 0 && (end < 0 || st < end) {
			end, terminator = st, 2
		}
		if end < 0 {
			if len(body) > 4096 {
				s.pending = s.pending[4:]
				continue
			}
			return count
		}
		if end <= 4096 && codexQuestionMessage(string(body[:end])) {
			count++
		}
		s.pending = s.pending[4+end+terminator:]
	}
}

func codexQuestionMessage(message string) bool {
	for _, prefix := range []string{"Question: ", "Approval requested: ", "Approval requested by ", "Codex wants to edit ", "Plan mode prompt: "} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}
