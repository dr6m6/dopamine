package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func isolatedTmux(t *testing.T) {
	t.Helper()
	if os.Getenv("DOPAMINE_INTEGRATION_TESTS") != "1" {
		t.Skip("explicit real tmux smoke: set DOPAMINE_INTEGRATION_TESTS=1")
	}
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	name := "dopamine-test-" + shortHash(t.TempDir())
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "tmux")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+Quote(real)+" -L "+Quote(name)+" -f /dev/null \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "")
	t.Cleanup(func() { exec.Command(real, "-L", name, "kill-server").Run() })
}

func TestShellQuotingIntegration(t *testing.T) {
	if os.Getenv("DOPAMINE_INTEGRATION_TESTS") != "1" {
		t.Skip("explicit subprocess smoke")
	}
	got, err := exec.Command("sh", "-c", "printf '%s' "+Quote("a';$(echo BAD);'b")).Output()
	if err != nil || string(got) != "a';$(echo BAD);'b" {
		t.Fatal(string(got), err)
	}
}
