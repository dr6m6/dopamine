package projecthub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// nativeRPC is a short-lived, local protocol client. It only initializes and
// sends the requested metadata method; it never starts a turn or a model call.
type MetadataRunner interface {
	Call(string, string, any, []string) (json.RawMessage, error)
}
type execMetadataRunner struct{}

var metadataRunner MetadataRunner = execMetadataRunner{}

func nativeRPC(cwd, method string, params any, overrides []string) (json.RawMessage, error) {
	return metadataRunner.Call(cwd, method, params, overrides)
}

func (execMetadataRunner) Call(cwd, method string, params any, overrides []string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := append([]string{"app-server", "--listen", "stdio://"}, overrides...)
	cmd := exec.CommandContext(ctx, "/run/current-system/sw/bin/codex-vpn", args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { in.Close(); cmd.Cancel(); cmd.Wait() }()
	enc := json.NewEncoder(in)
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 65536), 16<<20)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := enc.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for scan.Scan() {
			var v struct {
				ID     *int                      `json:"id"`
				Result json.RawMessage           `json:"result"`
				Error  *struct{ Message string } `json:"error"`
			}
			if json.Unmarshal(scan.Bytes(), &v) != nil || v.ID == nil || *v.ID != id {
				continue
			}
			if v.Error != nil {
				return nil, fmt.Errorf("Codex %s: %s", method, v.Error.Message)
			}
			return v.Result, nil
		}
		return nil, fmt.Errorf("Codex metadata protocol ended: %v", scan.Err())
	}
	if _, err = call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "project_hub", "version": "0.1.0"}}); err != nil {
		return nil, err
	}
	if err = enc.Encode(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	return call(2, method, params)
}

func effectiveSkillFlags(cwd string, overrides []string) (map[string]bool, error) {
	raw, err := nativeRPC(cwd, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, overrides)
	if err != nil {
		return nil, err
	}
	var v struct {
		Config struct {
			Skills struct {
				Config []struct {
					Path    string
					Enabled *bool
				}
			}
		}
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	flags := map[string]bool{}
	for _, c := range v.Config.Skills.Config {
		if c.Enabled != nil {
			flags[c.Path] = *c.Enabled
		}
	}
	return flags, nil
}
