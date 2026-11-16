package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const NativeCloudKind = "cloud-native"

func (s Service) StartNativeCloud(p Project) (Record, error) {
	r, err := s.Start(p, NativeCloudKind, "")
	if err != nil {
		return r, err
	}
	if err = s.ConfigureDopamine(); err != nil {
		return r, s.failNativeCloud(r, err.Error())
	}
	return r, s.ConnectNativeCloud(r)
}

// These are invocation-only overrides of the installed native TUI. Its Cloud
// connection owns authentication, model discovery, the conversation and tools.
func (s Service) ExecNativeCloud() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ownedRoot := filepath.Join(s.StateDir, "native-cloud")
	if !belongs(cwd, ownedRoot) {
		return errors.New("native cloud requires its private client directory")
	}
	return s.ExecCodex([]string{
		"--cd", cwd,
		"--enable", "tui_multi_backend",
		"-c", `tui.keymap.global.change_host="alt-f12"`,
		"-c", `tui.terminal_title=["app-name","run-state","thread-id","used-tokens"]`,
	})
}

func (s Service) ownedNativeClient(r Record) (string, error) {
	paths, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{pane_start_path}\n#{pane_current_path}")
	if err != nil {
		return "", err
	}
	expected := filepath.Join(s.StateDir, "native-cloud", r.Key)
	parts := strings.SplitN(paths, "\n", 2)
	if projectPathKey(parts[0]) != projectPathKey(expected) {
		return "", errors.New("Cloud client did not start in its private directory")
	}
	// tmux cannot read /proc/<pid>/cwd when the VPN launcher runs through
	// sudo. In that case use the recorded launch directory: ExecNativeCloud
	// also pins it with --cd before entering that launcher.
	if len(parts) == 2 && parts[1] != "" && projectPathKey(parts[1]) != projectPathKey(expected) {
		return "", errors.New("Cloud client left its private directory")
	}
	entries, err := os.ReadDir(expected)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return "", errors.New("Cloud client directory contains unexpected files; refusing automatic trust")
		}
	}
	return expected, nil
}

func nativeTrustPrompt(screen string) bool {
	text := ansi.Strip(screen)
	return strings.Contains(text, "Folder access") && strings.Contains(text, "Trust this folder?") && regexp.MustCompile(`(?m)^[ \t]*›[ \t]*1\.[ \t]*Trust and continue[ \t]*$`).MatchString(text)
}

// The native client initially prepares a local empty conversation. Use only
// an app-owned directory and credential-free origin metadata for that step.
func (s Service) prepareNativeCloud(p Project, key string) (string, error) {
	if !strings.HasPrefix(key, "cloud-native-") || !validID.MatchString(key) {
		return "", errors.New("invalid native cloud workspace")
	}
	dir := filepath.Join(s.StateDir, "native-cloud", key)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if _, err := command("git", "-c", "init.templateDir=", "init", "-q", dir); err != nil {
		return "", err
	}
	if remote, err := command("git", "-C", p.Path, "remote", "get-url", "origin"); err == nil {
		if origin := GitHubURL(remote); origin != "" {
			if _, err := command("git", "-C", dir, "config", "remote.origin.url", origin); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

var nativeCloudControl = regexp.MustCompile(`(?im)^\s*(Cloud|Local)\s+alt\+f12\s+to cycle\s*$`)
var nativeThreadID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

func nativeHost(screen string) string {
	match := nativeCloudControl.FindStringSubmatch(ansi.Strip(screen))
	if len(match) > 1 {
		return strings.ToLower(match[1])
	}
	return ""
}

func nativeCloudProblem(screen string) string {
	for _, line := range strings.Split(ansi.Strip(screen), "\n") {
		for _, text := range []string{"Could not add the Cloud connection", "Failed to prepare the new conversation", "Failed to start the new conversation", "Cloud stream ended. Reopen the conversation", "Cloud conversation activated but its stream did not connect"} {
			if strings.Contains(line, text) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// The new window remains in the background until the native TUI acknowledges
// Cloud. Only the host-switch key and, when necessary, acceptance of its
// verified app-owned client directory are sent. No model prompt is injected.
func (s Service) ConnectNativeCloud(r Record) error {
	return s.connectNativeCloud(r, 45*time.Second, 150*time.Millisecond)
}
func (s Service) connectNativeCloud(r Record, timeout, interval time.Duration) error {
	if r.Kind != NativeCloudKind {
		return errors.New("not a native cloud workspace")
	}
	deadline := time.Now().Add(timeout)
	switched := false
	trusted := false
	problem := "native cloud did not become ready; check your Codex version and ChatGPT Cloud access"
	for time.Now().Before(deadline) {
		meta, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}|#{pane_title}")
		fields := strings.SplitN(meta, "|", 3)
		if err != nil || len(fields) != 3 || fields[0] != r.Token {
			return errors.New("native cloud pane is no longer owned by this workspace")
		}
		if fields[1] == "1" {
			problem = "native Codex exited before connecting to Cloud; check the pane log"
			break
		}
		screen, err := recordTmux(r, "capture-pane", "-p", "-t", r.Pane)
		if err != nil {
			problem = err.Error()
			break
		}
		if problem := nativeCloudProblem(screen); problem != "" {
			return s.failNativeCloud(r, problem)
		}
		if !trusted && !switched && nativeTrustPrompt(screen) {
			if _, err := s.ownedNativeClient(r); err != nil {
				return s.failNativeCloud(r, err.Error())
			}
			if _, err := recordTmux(r, "send-keys", "-t", r.Pane, "Enter"); err != nil {
				return s.failNativeCloud(r, err.Error())
			}
			trusted = true
		}
		host := nativeHost(screen)
		if host == "cloud" {
			after, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}|#{pane_title}")
			if err != nil {
				return s.failNativeCloud(r, err.Error())
			}
			if after != meta {
				time.Sleep(interval)
				continue
			}
			current, err := s.readRecord(r.Key)
			if err != nil || current.Token != r.Token {
				return errors.New("native cloud workspace changed during startup")
			}
			current.State, current.Error, current.SessionID = "idle", "", nativeThreadID.FindString(fields[2])
			return AtomicJSON(s.recordPath(r.Key), current)
		}
		if host == "local" && !switched {
			if _, err = recordTmux(r, "send-keys", "-t", r.Pane, "M-F12"); err != nil {
				problem = err.Error()
				break
			}
			switched = true
		}
		time.Sleep(interval)
	}
	return s.failNativeCloud(r, problem)
}

// Fail closed: retain a readable error tab instead of an editable local Codex
// that the user could mistake for a connected Cloud conversation.
func (s Service) failNativeCloud(r Record, problem string) error {
	meta, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}")
	if err != nil || meta != r.Token {
		return errors.New("cloud pane ownership changed")
	}
	r.State, r.Error = Failed, problem
	saveErr := AtomicJSON(s.recordPath(r.Key), r)
	args := append(s.paneEnvironment(r.Project), s.Executable, "cloud-failed", r.Key, r.Token)
	if saveErr == nil {
		if _, err := recordTmux(r, append([]string{"respawn-pane", "-k", "-t", r.Pane}, args...)...); err == nil {
			return errors.New(problem)
		}
	}
	_, stopErr := recordTmux(r, "kill-pane", "-t", r.Pane)
	return errors.Join(errors.New(problem), saveErr, stopErr)
}

func nativeConnectorAlive(r Record) bool {
	return r.RootPID > 1 && r.RootStart != "" && r.BootID == bootID() && processStart(r.RootPID) == r.RootStart
}

func (s Service) FocusNativeCloud(r Record) error {
	meta, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}")
	if err != nil {
		return err
	}
	f := strings.Split(meta, "|")
	if len(f) != 2 || f[0] != r.Token {
		return errors.New("cloud workspace changed")
	}
	current, err := s.readRecord(r.Key)
	if err != nil {
		return err
	}
	if (current.State == Starting || current.State == Failed) && f[1] != "1" {
		return nil
	}
	return FocusSlot(Slot{Window: r.Target, Pane: r.Pane, Kind: NativeCloudKind, Exited: f[1] == "1", CloudState: s.NativeCloudState(current)})
}
func (s Service) ShowCloudFailure(key, token string) error {
	r, err := s.readRecord(key)
	if err != nil {
		return err
	}
	if r.Kind != NativeCloudKind || r.Token != token {
		return errors.New("stale cloud error")
	}
	fmt.Println("\ndopamine · cloud unavailable\n\n" + ansi.Strip(r.Error) + "\n\nno task was sent. reopen a cloud session to retry.\nalt+1: dashboard · ctrl+w: close this tab")
	return nil
}

func nativeCloudState(title string) string {
	if !strings.Contains(strings.ToLower(title), "codex") {
		return "unknown"
	}
	if strings.Contains(title, "Action Required") {
		return "attention"
	}
	// Native run-state and token count are process-local terminal-title fields,
	// rather than words searched for in model output or user prompts.
	parts := strings.FieldsFunc(title, func(r rune) bool { return r == '|' || r == '·' })
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "Working", "Thinking", "Waiting":
			return "running"
		case "Starting":
			return "connecting"
		case "Ready":
			if strings.Contains(title, " used") {
				return "ready"
			}
			return "idle"
		}
	}
	return "unknown"
}
func (s Service) NativeCloudState(r Record) string {
	_, state := s.NativeCloudStatus(r)
	return state
}

func (s Service) NativeCloudStatus(r Record) (string, string) {
	if r.State == Failed {
		return "cloud", "error"
	}
	if r.State == Starting {
		return "cloud", "connecting"
	}
	title, err := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}|#{@dopamine_cloud_thread}|#{@dopamine_local_thread}|#{pane_title}")
	fields := strings.SplitN(title, "|", 5)
	if err != nil || len(fields) != 5 || fields[0] != r.Token {
		return "unknown", "unknown"
	}
	if fields[1] == "1" {
		return "cloud", "closed"
	}
	id := nativeThreadID.FindString(fields[4])
	host := "unknown"
	if id != "" && (id == r.SessionID || id == fields[2]) {
		host = "cloud"
	}
	if id != "" && id == fields[3] {
		host = "local"
	}
	if screen, e := recordTmux(r, "capture-pane", "-p", "-t", r.Pane); e == nil {
		if nativeCloudProblem(screen) != "" {
			return host, "error"
		}
		if control := nativeHost(screen); control != "" {
			// A host label may disappear after the first turn. Retain its
			// association with this exact thread, never with every conversation.
			after, _ := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}|#{@dopamine_cloud_thread}|#{@dopamine_local_thread}|#{pane_title}")
			if after == title {
				host = control
				if id != "" {
					option := "@dopamine_cloud_thread"
					if host == "local" {
						option = "@dopamine_local_thread"
					}
					condition := "#{==:#{@dopamine_token}," + r.Token + "}"
					_, _ = recordTmux(r, "if-shell", "-F", "-t", r.Pane, condition, ShellArgs([]string{"set-option", "-p", "-t", r.Pane, option, id}))
				}
			}
		}
	}
	return host, nativeCloudState(fields[4])
}

func (s Service) nativeCloudLog(r Record) error {
	if err := os.MkdirAll(filepath.Dir(r.Log), 0700); err != nil {
		return err
	}
	// Log only this owned pane. Native controls and input stay attached to its PTY.
	_, err := recordTmux(r, "pipe-pane", "-t", r.Pane, "umask 077; cat >> "+Quote(r.Log))
	return err
}
