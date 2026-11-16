package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAutostartModesPreserveProjectSettingsAndOnlyEnableLogin(t *testing.T) {
	s, p := fixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(s.Home, "xdg"))
	p.Launch = []string{"python3", "-m", "http.server"}
	if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatal(name, args)
		}
		calls = append(calls, args)
		if slices.Contains(args, "stop") || slices.Contains(args, "start") {
			t.Fatal("setting a mode must leave running server alone", args)
		}
		return nil, nil
	}})
	for _, mode := range []string{AutostartDopamine, AutostartLogin, AutostartOff} {
		if err := s.SetServerAutostart(p, mode); err != nil {
			t.Fatal(err)
		}
		saved, err := s.Project(p.ID)
		if err != nil || saved.ServerAutostart != mode || !slices.Equal(saved.Launch, p.Launch) {
			t.Fatal(saved, err)
		}
		p = saved
	}
	if !s.ownsUnit(p) {
		t.Fatal("unit ownership lost")
	}
	unit, err := os.ReadFile(s.unitPath(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Type=exec", "serve-project", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), part) {
			t.Fatal(string(unit))
		}
	}
	enabled, disabled := false, false
	for _, args := range calls {
		enabled = enabled || slices.Contains(args, "enable")
		disabled = disabled || slices.Contains(args, "disable")
	}
	if !enabled || !disabled {
		t.Fatal(calls)
	}
}
func TestAutostartRefusesDeclarativeUnitsAndRollsBackFailedEnable(t *testing.T) {
	s, p := fixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(s.Home, "xdg"))
	p.Launch = []string{"false"}
	write(t, s.unitPath(p), "# home-manager unit\n")
	useRunner(t, stubRunner{output: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }})
	if err := s.SetServerAutostart(p, AutostartLogin); err == nil {
		t.Fatal("foreign unit overwritten")
	}
	b, _ := os.ReadFile(s.unitPath(p))
	if string(b) != "# home-manager unit\n" {
		t.Fatal(string(b))
	}
	if err := os.Remove(s.unitPath(p)); err != nil {
		t.Fatal(err)
	}
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if slices.Contains(args, "enable") {
			return []byte("denied"), errors.New("exit 1")
		}
		return nil, nil
	}})
	if err := s.SetServerAutostart(p, AutostartLogin); err == nil {
		t.Fatal("failed enable accepted")
	}
	if _, err := os.Stat(s.unitPath(p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed setup left a unit", err)
	}
	saved, _ := s.Project(p.ID)
	if saved.ServerAutostart == AutostartLogin {
		t.Fatal(saved)
	}
}
func TestLoginServerStatusAndStopUseOwnedService(t *testing.T) {
	s, p := fixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(s.Home, "xdg"))
	p.Launch = []string{"false"}
	p.ServerAutostart = AutostartLogin
	if err := s.writeUnit(p); err != nil {
		t.Fatal(err)
	}
	stopped := false
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" {
			return nil, errors.New("no tmux")
		}
		if slices.Contains(args, "stop") {
			stopped = true
			return nil, nil
		}
		return []byte("ActiveState=active\nSubState=running\nResult=success"), nil
	}})
	status := s.AppStatuses([]Project{p})[p.ID]
	if status.State != Running {
		t.Fatal(status)
	}
	if err := s.StopApp(p); err != nil || !stopped {
		t.Fatal(err, stopped)
	}
}
func TestServerExecKeepsArgumentsLiteralAndUsesProjectDirectory(t *testing.T) {
	s, p := fixture(t)
	t.Chdir(s.Home)
	p.Launch = []string{"sh", "-c", "printf '%s' 'a;$(false)'"}
	if err := s.Save(Registry{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	called := false
	useRunner(t, stubRunner{replace: func(path string, args, env []string) error {
		called = true
		cwd, _ := os.Getwd()
		if cwd != p.Path || !slices.Equal(args[1:], p.Launch[1:]) {
			t.Fatal(cwd, args)
		}
		return nil
	}})
	if err := s.ExecServer(p.ID); err != nil || !called {
		t.Fatal(err, called)
	}
}
func TestGeneratedServerUnitPassesSystemdParser(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze unavailable")
	}
	s, p := fixture(t)
	s.Executable = "/run/current-system/sw/bin/true"
	p.ID = "demo"
	p.Path = filepath.Join(s.Home, "space % dollar $")
	unit := filepath.Join(t.TempDir(), s.unitName(p))
	write(t, unit, s.serverUnit(p))
	output, err := exec.Command("systemd-analyze", "--user", "verify", unit).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd rejected generated unit: %v\n%s", err, output)
	}
}

func TestLoginUnitUsesStableSourcePackageRoot(t *testing.T) {
	s, p := fixture(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(s.Home, "xdg-state"))
	packagePath := filepath.Join(s.Home, "package/bin/dopamine")
	write(t, packagePath, "binary")
	root := filepath.Join(s.Home, "xdg-state/dopamine/source-package")
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.Home, "package"), root); err != nil {
		t.Fatal(err)
	}
	s.Executable = packagePath
	if got := s.serverExecutable(); got != filepath.Join(root, "bin/dopamine") {
		t.Fatal(got)
	}
	if !strings.Contains(s.serverUnit(p), filepath.Join(root, "bin/dopamine")) {
		t.Fatal(s.serverUnit(p))
	}
	s.Executable = "/nix/store/temporary-package/bin/dopamine"
	if got := s.serverExecutable(); got != "" {
		t.Fatal("unrooted package accepted", got)
	}
}
