package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCloudSubmitUsesSelectedProjectAndNeverRetries(t *testing.T) {
	s, p := fixture(t)
	prompt := "fix this\nkeep $(false) and `ticks` literal"
	calls := 0
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("feature/test"), nil
		}
		calls++
		want := []string{"exec-cloud", p.Path, "exec", "--env", "test env", "--attempts", "1", "--branch", "feature/test", "--", prompt}
		if name != s.Executable || !slices.Equal(args, want) {
			t.Fatalf("%s %q", name, args)
		}
		before, err := s.CloudTask("cloud-demo-test")
		if err != nil || before.State != "submitting" {
			t.Fatal("intent not saved before request", before, err)
		}
		return []byte("https://chatgpt.com/codex/tasks/task_e_test\n"), nil
	}})
	task, err := s.SubmitCloud("cloud-demo-test", p, " test env ", prompt)
	if err != nil || task.ID != "task_e_test" || task.State != "pending" || task.Directory != p.Path {
		t.Fatal(task, err)
	}
	if _, err = s.SubmitCloud(task.Key, p, "test env", prompt); err == nil {
		t.Fatal("duplicate submission accepted")
	}
	if calls != 1 {
		t.Fatal("duplicated request", calls)
	}
	if info, err := os.Stat(s.cloudPath(task.Key)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("task must be private", err)
	}
}
func TestCloudPendingExitOneAndOfflineState(t *testing.T) {
	s, p := fixture(t)
	task := CloudTask{Key: "cloud-demo-test", Project: p.ID, ID: "task_e_test", State: "pending", Directory: p.Path}
	if err := AtomicJSON(s.cloudPath(task.Key), task); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"PENDING", "READY", "APPLIED", "ERROR"} {
		useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if !slices.Equal(args, []string{"exec-cloud", p.Path, "status", task.ID}) {
				t.Fatal(args)
			}
			return []byte("[" + state + "] example\nno diff"), errors.New("exit status 1")
		}})
		updated, err := s.RefreshCloud(task.Key)
		if err != nil || updated.State != strings.ToLower(state) || updated.Error != "" || updated.Updated == 0 {
			t.Fatal(updated, err)
		}
	}
	last, _ := s.CloudTask(task.Key)
	useRunner(t, stubRunner{output: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("network offline"), errors.New("unavailable")
	}})
	updated, err := s.RefreshCloud(task.Key)
	if err != nil || updated.State != last.State || updated.Updated != last.Updated || !strings.Contains(updated.Error, "last known") {
		t.Fatal(updated, err)
	}
}
func TestCloudUncertainSubmissionCanAttachWithoutAnotherRequest(t *testing.T) {
	s, p := fixture(t)
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return nil, errors.New("not a repository")
		}
		if slices.Contains(args, "status") {
			return []byte("[READY] recovered task\nno diff"), nil
		}
		return nil, context.DeadlineExceeded
	}})
	task, err := s.SubmitCloud("cloud-demo-uncertain", p, "environment", "do work")
	if err == nil || task.State != "submission uncertain" {
		t.Fatal(task, err)
	}
	task, err = s.AttachCloud(task.Key, "https://chatgpt.com/remote/task_e_recovered")
	if err != nil || task.ID != "task_e_recovered" || task.State != "ready" {
		t.Fatal(task, err)
	}
	if _, err = s.CloudTask("../../secrets"); err == nil {
		t.Fatal("path accepted")
	}
	tasks, err := s.CloudTasks(p.ID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatal(tasks, err)
	}
}
func TestCloudSettingsPreserveUnknownAndMissingProjects(t *testing.T) {
	s, p := fixture(t)
	write(t, s.RegistryPath(), `{"extra":true,"projects":[{"id":"demo","path":`+strconvQuote(p.Path)+`,"extra":"keep"},{"id":"missing","path":"/missing","secret_ref":"keep"}]}`)
	if err := s.SetCloudEnvironment(p, "environment"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, piece := range []string{`"extra": true`, `"extra": "keep"`, `"secret_ref": "keep"`, `"cloud_environment": "environment"`} {
		if !strings.Contains(string(b), piece) {
			t.Fatal(string(b))
		}
	}
}
func TestCloudExecUsesExistingVPNWithoutLocalApprovalFlags(t *testing.T) {
	s, p := fixture(t)
	t.Chdir(s.Home)
	called := false
	useRunner(t, stubRunner{replace: func(path string, args, env []string) error {
		called = true
		cwd, _ := os.Getwd()
		if cwd != p.Path || path != codexVPN || !slices.Equal(args, []string{codexVPN, "cloud", "status", "task_e_test"}) {
			t.Fatal(cwd, path, args)
		}
		return nil
	}})
	if _, err := os.Stat(codexVPN); err != nil {
		t.Skip("requires NixOS codex-vpn launcher")
	}
	if err := s.ExecCloud(p.Path, []string{"status", "task_e_test"}); err != nil || !called {
		t.Fatal(err, called)
	}
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
