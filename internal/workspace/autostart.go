package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	AutostartOff      = "off"
	AutostartDopamine = "dopamine"
	AutostartLogin    = "login"
)

func AutostartLabel(mode string) string {
	switch mode {
	case AutostartDopamine:
		return "when dopamine opens"
	case AutostartLogin:
		return "at login"
	default:
		return "off"
	}
}
func (s Service) unitName(p Project) string {
	return "dopamine-server-" + shortHash(s.ConfigDir+"/"+p.ID) + ".service"
}
func (s Service) unitPath(p Project) string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(s.Home, ".config")
	}
	return filepath.Join(config, "systemd/user", s.unitName(p))
}
func (s Service) unitMarker(p Project) string {
	return "# dopamine server " + shortHash(s.ConfigDir+"/"+p.ID) + "\n"
}
func (s Service) ownsUnit(p Project) bool {
	info, err := os.Lstat(s.unitPath(p))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(s.unitPath(p))
	return err == nil && strings.HasPrefix(string(b), s.unitMarker(p))
}
func systemdQuote(s string) string     { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
func systemdExecQuote(s string) string { return systemdQuote(strings.ReplaceAll(s, "$", "$$")) }

// Installed source builds maintain a GC root. Keep its stable name rather
// than a package hash that a later update can collect.
func (s Service) serverExecutable() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(s.Home, ".local/state")
	}
	current, err := os.Stat(s.Executable)
	if err == nil {
		for _, path := range []string{filepath.Join(state, "dopamine/source-package/bin/dopamine"), "/run/current-system/sw/bin/dopamine"} {
			if info, e := os.Stat(path); e == nil && os.SameFile(current, info) {
				return path
			}
		}
	}
	if strings.HasPrefix(s.Executable, "/nix/store/") {
		return ""
	}
	return s.Executable
}

func (s Service) serverUnit(p Project) string {
	fields := []string{"HOME=" + s.Home, "DOPAMINE_CONFIG_DIR=" + s.ConfigDir, "DOPAMINE_STATE_DIR=" + s.StateDir, "DOPAMINE_PROJECTS_ROOT=" + s.ProjectsRoot, "PATH=" + os.Getenv("PATH")}
	var env []string
	for _, field := range fields {
		env = append(env, "Environment="+systemdQuote(field))
	}
	return s.unitMarker(p) + "[Unit]\nDescription=dopamine project server\nAfter=network.target\n\n[Service]\nType=exec\n" + strings.Join(env, "\n") + "\nExecStart=" + systemdExecQuote(s.serverExecutable()) + " serve-project " + systemdExecQuote(p.ID) + "\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=10\n\n[Install]\nWantedBy=default.target\n"
}
func (s Service) writeUnit(p Project) error {
	path := s.unitPath(p)
	if _, err := os.Lstat(path); err == nil && !s.ownsUnit(p) {
		return fmt.Errorf("%s is managed elsewhere; keep its declarative configuration", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dopamine-service-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(s.serverUnit(p)); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Changing a mode changes future starts; it does not stop a running server.
// An enabled login service starts at the next login, or with Start server.
func (s Service) SetServerAutostart(p Project, mode string) error {
	if !validID.MatchString(p.ID) || len(p.Launch) == 0 {
		return errors.New("configure a server launch command first")
	}
	if mode != AutostartOff && mode != AutostartDopamine && mode != AutostartLogin {
		return errors.New("unknown autostart mode")
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if mode == AutostartLogin {
		if s.serverExecutable() == "" {
			return errors.New("install dopamine before enabling login autostart; this temporary Nix package has no stable launcher")
		}
		if _, err := command("systemctl", "--user", "show-environment"); err != nil {
			return fmt.Errorf("login autostart needs the user systemd session: %w", err)
		}
		old, readErr := os.ReadFile(s.unitPath(p))
		if err = s.writeUnit(p); err != nil {
			return err
		}
		if _, err = command("systemctl", "--user", "daemon-reload"); err == nil {
			_, err = command("systemctl", "--user", "enable", s.unitName(p))
		}
		if err != nil {
			if readErr == nil {
				_ = os.WriteFile(s.unitPath(p), old, 0600)
			} else {
				_ = os.Remove(s.unitPath(p))
			}
			_, _ = command("systemctl", "--user", "daemon-reload")
			return err
		}
	} else if s.ownsUnit(p) {
		if _, err = command("systemctl", "--user", "disable", s.unitName(p)); err != nil {
			return err
		}
	}
	if err = s.setProjectField(p, "server_autostart", mode); err != nil {
		// Do not leave a newly enabled login start after a failed settings save.
		if mode == AutostartLogin && p.ServerAutostart != AutostartLogin {
			_, _ = command("systemctl", "--user", "disable", s.unitName(p))
		}
		if mode != AutostartLogin && p.ServerAutostart == AutostartLogin {
			_, _ = command("systemctl", "--user", "enable", s.unitName(p))
		}
		return err
	}
	return nil
}

func (s Service) loginServerStatus(p Project) (AppStatus, bool) {
	if !s.ownsUnit(p) {
		return AppStatus{}, false
	}
	output, err := command("systemctl", "--user", "show", s.unitName(p), "--property=ActiveState,SubState,Result", "--no-pager")
	if err != nil {
		return AppStatus{State: Failed, Error: "server status unavailable: " + err.Error()}, true
	}
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			fields[k] = v
		}
	}
	switch fields["ActiveState"] {
	case "active":
		result := AppStatus{State: Running}
		if p.AppURL != "" {
			if err := s.probe(p); err != nil {
				result.State = Starting
				result.Error = "service started; url not ready: " + err.Error()
			}
		}
		return result, true
	case "activating":
		return AppStatus{State: Starting}, true
	case "deactivating":
		return AppStatus{State: Stopping}, true
	case "failed":
		return AppStatus{State: Failed, Error: "server failed · journalctl --user -u " + s.unitName(p)}, true
	case "inactive":
		return AppStatus{State: Stopped}, true
	default:
		return AppStatus{State: Failed, Error: "unrecognized systemd server status"}, true
	}
}

func (s Service) startLoginServer(p Project) (Record, error) {
	if !s.ownsUnit(p) {
		return Record{}, errors.New("login service missing; select autostart at login again")
	}
	_, err := command("systemctl", "--user", "start", s.unitName(p))
	return Record{Project: p.ID, Kind: "app"}, err
}

func (s Service) AutostartServers() error {
	registry, err := s.Load()
	if err != nil {
		return err
	}
	var problems []error
	for _, p := range registry.Projects {
		if p.ServerAutostart != AutostartDopamine {
			continue
		}
		if _, err := s.Start(p, "app", ""); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errors.Join(problems...)
}

// systemd owns this foreground process and its children. No detached tmux is
// created here, so stop/restart and the user session lifecycle remain native.
func (s Service) ExecServer(id string) error {
	p, err := s.Project(id)
	if err != nil {
		return err
	}
	if len(p.Launch) == 0 {
		return errors.New("server launch command is missing")
	}
	if r, e := s.readRecord("app-" + p.ID); e == nil && (liveRecord(r) || applicationProcessAlive(r)) {
		return errors.New("a dopamine server is already running")
	}
	if p.AppURL != "" && s.probe(p) == nil {
		return errors.New("server url already responds; refusing duplicate")
	}
	if err = os.Chdir(p.Path); err != nil {
		return err
	}
	args := p.Launch
	if p.LaunchVPN {
		args = append([]string{"/run/current-system/sw/bin/vpn-run"}, args...)
	}
	executable, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return processRunner.Replace(executable, append([]string{executable}, args[1:]...), os.Environ())
}
