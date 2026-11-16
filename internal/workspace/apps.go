package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	Running  = "Running"
	Starting = "Starting"
	Stopping = "Stopping"
	Stopped  = "Stopped"
	Failed   = "Failed"
)

type AppStatus struct {
	State, Error, Log string
	ExitCode          *int
}

func (s Service) recordPath(key string) string { return filepath.Join(s.StateDir, "runs", key+".json") }
func (s Service) readRecord(key string) (Record, error) {
	var r Record
	b, err := os.ReadFile(s.recordPath(key))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// startDetached runs under the existing cross-process action lock. A gate lets
// us install the native exit hook and logging before even a fast command exits.
func (s Service) startDetached(p Project, kind string) (r Record, err error) {
	if !validID.MatchString(p.ID) {
		return r, errors.New("invalid project ID")
	}
	key := kind + "-" + p.ID
	old, e := s.readRecord(key)
	if e == nil {
		if current, ok := resolveRecord(old); ok {
			return current, nil
		}
		if applicationProcessAlive(old) {
			return old, nil
		}
		if kind == "app" {
			if err = s.finishApp(old); err != nil {
				return r, err
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return r, e
	}
	args := p.Launch
	if kind == "terminal" {
		args = []string{"bash", "-l"}
	}
	if len(args) == 0 {
		return r, errors.New("project has no launch command; edit projects.json")
	}
	if kind == "app" && p.AppURL != "" {
		if _, err = appAddress(p.AppURL); err != nil {
			return r, err
		}
		if s.probe(p) == nil {
			return r, errors.New("app URL already responds; no duplicate launched (b opens it)")
		}
	}
	token := shortHash(s.StateDir + key + strconv.FormatInt(time.Now().UnixNano(), 10))
	env := s.paneEnvironment(p.ID)
	// Pass identity after vpn-run, whose existing environment allowlist is kept.
	if kind == "app" {
		args = append([]string{"env", "DOPAMINE_APP_TOKEN=" + token}, args...)
	}
	if p.LaunchVPN {
		args = append([]string{"/run/current-system/sw/bin/vpn-run"}, args...)
	}
	args = append(env, args...)
	name := "dopamine-" + kind + "-" + shortHash(s.StateDir+key) + "-" + token[:6]
	gate := "dopamine-" + token
	out, err := tmux("new-session", "-d", "-P", "-F", "#{session_id}|#{session_name}:#{window_index}.#{pane_index}|#{pane_id}|#{pane_pid}|#{socket_path}", "-s", name, "-n", kind, "-c", p.Path, "tmux wait-for "+Quote(gate)+"; exec "+ShellArgs(args))
	if err != nil {
		return r, err
	}
	f := strings.Split(out, "|")
	if len(f) != 5 {
		return r, errors.New("unexpected tmux response")
	}
	r = Record{Key: key, Project: p.ID, Kind: kind, Session: f[0], Socket: f[4], Target: f[1], Pane: f[2], Token: token, Created: time.Now().Unix(), State: Starting, Log: filepath.Join(s.StateDir, "logs", key+"-"+token+".log")}
	r.RootPID, _ = strconv.Atoi(f[3])
	r.RootStart = processStart(r.RootPID)
	r.BootID = bootID()
	committed := false
	defer func() {
		if !committed {
			_, _ = recordTmux(r, "kill-session", "-t", r.Session)
		}
	}()
	if err = os.MkdirAll(filepath.Dir(r.Log), 0700); err != nil {
		return r, err
	}
	log, err := os.OpenFile(r.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	log.Close()
	options := [][]string{
		{"set-option", "-p", "-t", r.Pane, "@dopamine_token", token},
		{"set-option", "-t", r.Session, "@dopamine_token", token},
		{"set-option", "-t", r.Session, "destroy-unattached", "off"},
		{"set-option", "-t", r.Session, "history-limit", "50000"},
		{"set-option", "-w", "-t", r.Pane, "remain-on-exit", "off"},
		{"pipe-pane", "-o", "-t", r.Pane, "umask 077; cat >> " + Quote(r.Log)},
	}
	if kind == "app" {
		callback := ShellArgs(append(s.paneEnvironment(p.ID), s.Executable, "app-exited", key, token))
		options = append(options, []string{"set-option", "-w", "-t", r.Pane, "remain-on-exit", "on"}, []string{"set-hook", "-p", "-t", r.Pane, "pane-died[0]", "run-shell -b " + Quote(callback)})
	}
	for _, a := range options {
		if _, err = tmux(a...); err != nil {
			return r, err
		}
	}
	if err = AtomicJSON(s.recordPath(key), r); err != nil {
		return r, err
	}
	_, err = tmux("wait-for", "-S", gate)
	committed = err == nil
	return r, err
}

func (s Service) probe(p Project) error {
	if p.LaunchVPN {
		_, err := command("/run/current-system/sw/bin/vpn-run", s.Executable, "probe-app", p.AppURL)
		return err
	}
	return ProbeApp(p.AppURL)
}

// Application state is independent of Codex. Polling also reconciles legacy
// records and a missed exit hook after a dopamine restart; saved Running is no proof.
func (s Service) AppStatuses(projects []Project) map[string]AppStatus {
	out := make(map[string]AppStatus, len(projects))
	for _, p := range projects {
		r, err := s.readRecord("app-" + p.ID)
		if err != nil || (!liveRecord(r) && !applicationProcessAlive(r)) {
			if status, owned := s.loginServerStatus(p); owned && (p.ServerAutostart == AutostartLogin || status.State != Stopped) {
				out[p.ID] = status
				continue
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			out[p.ID] = AppStatus{State: Stopped}
			continue
		}
		if err != nil {
			out[p.ID] = AppStatus{State: Failed, Error: err.Error()}
			continue
		}
		state := r.State
		detail := r.Error
		_, paneAlive := resolveRecord(r)
		if paneAlive || applicationProcessAlive(r) {
			if state != Stopping {
				state = Running
				detail = ""
				if !paneAlive {
					detail = "Application process alive; tmux pane unavailable"
				}
				if p.AppURL != "" {
					if err = s.probe(p); err != nil {
						state = Starting
						if time.Since(time.Unix(r.Created, 0)) > 30*time.Second {
							state = Failed
						}
						detail = "Service unavailable: " + err.Error()
					}
				}
				if r.State == Failed && r.Error != "" {
					state = Failed
					detail = r.Error
				}
				if r.RootPID > 0 && processStart(r.RootPID) != r.RootStart {
					state = Failed
					detail = "Application process is missing or replaced"
				}
			}
		} else if state != Stopped && state != Failed {
			// Serialize destructive reconciliation with start/stop and native hooks.
			if err = s.AppExited(r.Key, r.Token); err != nil {
				state = Failed
				detail = err.Error()
			} else if current, e := s.readRecord(r.Key); e == nil {
				r = current
				state = r.State
				detail = r.Error
			}
		}
		out[p.ID] = AppStatus{State: state, Error: detail, Log: r.Log, ExitCode: r.ExitCode}
	}
	return out
}

func (s Service) AppExited(key, token string) error {
	if !strings.HasPrefix(key, "app-") || !validID.MatchString(key) {
		return errors.New("invalid app key")
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	r, err := s.readRecord(key)
	if err != nil {
		return err
	}
	if r.Kind != "app" || r.Token != token {
		return errors.New("stale app callback")
	}
	if liveRecord(r) {
		return nil
	}
	if applicationProcessAlive(r) {
		return errors.New("application process still alive; refusing exit cleanup")
	}
	return s.finishApp(r)
}

func (s Service) StopApp(p Project) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if s.ownsUnit(p) {
		if _, err := command("systemctl", "--user", "stop", s.unitName(p)); err != nil {
			return err
		}
	}
	r, err := s.readRecord("app-" + p.ID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Kind != "app" || r.Project != p.ID {
		return errors.New("app ownership mismatch")
	}
	if r.RootPID == 0 && liveRecord(r) {
		pid, e := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{pane_pid}")
		if e != nil {
			return e
		}
		r.RootPID, _ = strconv.Atoi(pid)
		r.RootStart = processStart(r.RootPID)
		r.BootID = bootID()
	}
	r.State = Stopping
	if err = AtomicJSON(s.recordPath(r.Key), r); err != nil {
		return err
	}
	// Ctrl+C reaches the pane's foreground group, including the existing VPN
	// wrapper, without signalling the dopamine or any tmux client.
	if liveRecord(r) {
		_, _ = recordTmux(r, "send-keys", "-t", r.Pane, "C-c")
	}
	if err = terminateApp(r, 2*time.Second); err != nil {
		r.State = Failed
		r.Error = err.Error()
		_ = AtomicJSON(s.recordPath(r.Key), r)
		return err
	}
	deadline := time.Now().Add(time.Second)
	for liveRecord(r) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return s.finishApp(r)
}

func (s Service) finishApp(r Record) error {
	if r.Kind != "app" {
		return nil
	}
	if applicationProcessAlive(r) {
		return errors.New("application process still alive; refusing exit cleanup")
	}
	out, e := recordTmux(r, "display-message", "-p", "-t", r.Pane, "#{@dopamine_token}|#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}")
	f := strings.Split(out, "|")
	if e == nil && len(f) == 4 && f[0] == r.Token {
		if f[1] != "1" {
			return errors.New("application still has a live pane")
		}
		code, err := strconv.Atoi(f[2])
		if err == nil {
			r.ExitCode = &code
		}
		if r.State != Stopping {
			r.State = Stopped
			if err != nil || code != 0 {
				r.State = Failed
				r.Error = fmt.Sprintf("Application exited: status=%s signal=%s", f[2], f[3])
			}
		}
	} else if r.State != Stopping && r.State != Stopped && r.State != Failed {
		r.State = Failed
		r.Error = "Application pane disappeared; exit status unavailable"
	}
	if err := terminateApp(r, 0); err != nil {
		return err
	}
	if r.State == Stopping {
		r.State = Stopped
	}
	if err := AtomicJSON(s.recordPath(r.Key), r); err != nil {
		return err
	}
	// Exact session ID plus instance token; never kill a shared legacy session.
	if r.Session != "" {
		token, err := recordTmux(r, "show-options", "-v", "-t", r.Session, "@dopamine_token")
		if err == nil && token == r.Token {
			_, err = recordTmux(r, "kill-session", "-t", r.Session)
			return err
		}
	}
	if e == nil && len(f) == 4 && f[0] == r.Token {
		_, err := recordTmux(r, "kill-pane", "-t", r.Pane)
		return err
	}
	return nil
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}
func procFields(pid int) []string {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	i := strings.LastIndex(string(b), ")")
	if i < 0 {
		return nil
	}
	return strings.Fields(string(b)[i+1:])
}
func processStart(pid int) string {
	f := procFields(pid)
	if len(f) < 20 {
		return ""
	}
	return f[19]
}
func applicationProcessAlive(r Record) bool {
	if r.RootPID <= 1 || r.RootStart == "" || r.BootID == "" || r.BootID != bootID() {
		return false
	}
	f := procFields(r.RootPID)
	return len(f) >= 20 && f[0] != "Z" && f[19] == r.RootStart
}

func ownedAppPID(r Record, pid int) bool {
	if r.RootPID <= 1 || r.BootID == "" || r.BootID != bootID() {
		return false
	}
	f := procFields(pid)
	if len(f) < 20 || f[0] == "Z" {
		return false
	}
	// SID retains children even when the launch command has exited. Reject a
	// recycled session leader before signalling anything from a saved record.
	leader := processStart(r.RootPID)
	if leader != "" && leader != r.RootStart {
		return false
	}
	if f[3] == strconv.Itoa(r.RootPID) {
		return true
	}
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	return strings.Contains("\x00"+string(b), "\x00DOPAMINE_APP_TOKEN="+r.Token+"\x00")
}
func appPIDs(r Record) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		pid, _ := strconv.Atoi(e.Name())
		if pid > 1 && ownedAppPID(r, pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}
func signalApp(r Record, sig syscall.Signal) {
	for _, pid := range appPIDs(r) {
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		if ownedAppPID(r, pid) {
			_ = unix.PidfdSendSignal(fd, sig, nil, 0)
		}
		unix.Close(fd)
	}
}
func waitApp(r Record, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if len(appPIDs(r)) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(40 * time.Millisecond)
	}
}
func terminateApp(r Record, grace time.Duration) error {
	if waitApp(r, grace) {
		return nil
	}
	signalApp(r, syscall.SIGTERM)
	if waitApp(r, 2*time.Second) {
		return nil
	}
	signalApp(r, syscall.SIGKILL)
	if waitApp(r, time.Second) {
		return nil
	}
	return errors.New("owned application processes did not stop; diagnostics retained")
}

// OpenTerminal is the only application action that launches Kitty. A clean
// TMUX environment avoids nesting, while an explicit socket keeps the server.
func (s Service) OpenTerminal(p Project) error {
	r, err := s.StartTerminalRecord(p)
	if err != nil {
		return err
	}
	return OpenRecordTerminal(p, r)
}
func (s Service) StartTerminalRecord(p Project) (Record, error) {
	if r, e := s.readRecord("app-" + p.ID); e == nil {
		if current, ok := resolveRecord(r); ok {
			return current, nil
		}
	}
	return s.Start(p, "terminal", "")
}
func OpenRecordTerminal(p Project, r Record) error {
	current, ok := resolveRecord(r)
	if !ok {
		return errors.New("terminal process has exited")
	}
	socket, err := recordTmux(current, "display-message", "-p", "-t", current.Pane, "#{socket_path}")
	if err != nil {
		return err
	}
	_, err = command("kitty", "--detach", "--title", "dopamine · "+p.Name, "--directory", p.Path, "env", "-u", "TMUX", "-u", "TMUX_PANE", "tmux", "-S", socket, "attach-session", "-t", current.Target)
	return err
}
