package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Runner keeps external process effects replaceable in tests.
type Runner interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Run(string, ...string) error
	Replace(string, []string, []string) error
}
type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
func (execRunner) Run(name string, args ...string) error { return exec.Command(name, args...).Run() }
func (execRunner) Replace(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}

var processRunner Runner = execRunner{}

func command(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := processRunner.Output(ctx, name, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
