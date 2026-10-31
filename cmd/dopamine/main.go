package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/dr6m6/dopamine/internal/dopamine"
	"github.com/dr6m6/dopamine/internal/projecthub"
)

var Version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dopamine:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	s := projecthub.Default()
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			fmt.Println("dopamine — terminals, codex workspaces and project servers\n\nusage: dopamine [--help | --version | projects]\n\nno arguments: open the tui. press ? on dashboard for keyboard shortcuts.")
			return nil
		case "--version":
			fmt.Println("dopamine", Version)
			return nil
		case "projects":
			r, err := s.Load()
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(r)
		// Private tmux callbacks also retain compatibility with existing runs.
		case "exec-codex":
			return s.ExecCodex(args[1:])
		case "codex-notify":
			if len(args) == 4 {
				// Notification failures must not interrupt a native Codex turn.
				_ = s.NotifyCodex(args[1], args[2], args[3])
				return nil
			}
		case "observe-codex":
			if len(args) == 3 {
				return s.ObserveCodex(args[1], args[2], os.Stdin)
			}
		case "app-exited":
			if len(args) == 3 {
				return s.AppExited(args[1], args[2])
			}
		case "probe-app":
			if len(args) == 2 {
				return projecthub.ProbeApp(args[1])
			}
		case "kill-hub":
			return s.KillDashboard()
		case "sidebar":
			return dopamine.Run(s, true)
		case "slot":
			if len(args) == 2 {
				if args[1] == "next" {
					return s.CycleSlot(1)
				}
				if args[1] == "previous" {
					return s.CycleSlot(-1)
				}
				n, err := strconv.Atoi(args[1])
				if err != nil {
					return err
				}
				return s.FocusSlot(n)
			}
		}
		return fmt.Errorf("unknown command or arguments; run dopamine --help")
	}
	if os.Getenv("PROJECT_HUB_TUI") == "1" {
		return dopamine.Run(s, false)
	}
	if _, err := os.Stat(s.RegistryPath()); os.IsNotExist(err) {
		if err = s.Save(s.Discover()); err != nil {
			return err
		}
	}
	os.Setenv("PROJECT_HUB_EXECUTABLE", s.Executable)
	target, err := s.EnsureDashboard()
	if err != nil {
		return err
	}
	if err = s.ConfigureDopamine(); err != nil {
		return err
	}
	if os.Getenv("TMUX") != "" {
		return projecthub.Jump(target)
	}
	if out, err := exec.Command("tmux", "select-window", "-t", strings.Split(target, ".")[0]).CombinedOutput(); err != nil {
		return fmt.Errorf("select dashboard: %w: %s", err, out)
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	return syscall.Exec(tmux, []string{tmux, "attach-session", "-t", "=project-hub"}, os.Environ())
}
