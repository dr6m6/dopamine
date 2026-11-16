package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const codexVPN = "/run/current-system/sw/bin/codex-vpn"

type CloudTask struct {
	Key, Project, Environment, Prompt, Branch string
	Directory                                 string
	ID, URL, State, Detail, Error             string
	Updated                                   int64
}

func validCloudKey(key string) bool {
	return strings.HasPrefix(key, "cloud-") && validID.MatchString(key)
}
func (s Service) cloudPath(key string) string { return filepath.Join(s.StateDir, "cloud", key+".json") }
func (s Service) CloudTask(key string) (CloudTask, error) {
	var task CloudTask
	if !validCloudKey(key) {
		return task, errors.New("invalid cloud workspace")
	}
	b, err := os.ReadFile(s.cloudPath(key))
	if err != nil {
		return task, err
	}
	err = json.Unmarshal(b, &task)
	if err == nil && task.Key != key {
		err = errors.New("cloud workspace ownership mismatch")
	}
	return task, err
}
func (s Service) CloudTasks(project string) ([]CloudTask, error) {
	files, err := filepath.Glob(filepath.Join(s.StateDir, "cloud", "*.json"))
	if err != nil {
		return nil, err
	}
	var tasks []CloudTask
	for _, path := range files {
		task, err := s.CloudTask(strings.TrimSuffix(filepath.Base(path), ".json"))
		if err != nil {
			return nil, err
		}
		if task.Project == project {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Updated > tasks[j].Updated })
	return tasks, nil
}

func (s Service) cloudCommand(timeout time.Duration, directory string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if directory == "" {
		directory = s.Home
	}
	b, err := processRunner.Output(ctx, s.Executable, append([]string{"exec-cloud", directory}, args...)...)
	return strings.TrimSpace(ansi.Strip(string(b))), err
}

var cloudURL = regexp.MustCompile(`https://chatgpt\.com/(?:codex/tasks|remote)/(task_[A-Za-z0-9_-]+)`)
var cloudID = regexp.MustCompile(`^task_[A-Za-z0-9_-]+$`)
var statusLine = regexp.MustCompile(`(?m)^\[(PENDING|READY|APPLIED|ERROR|RUNNING|QUEUED|CANCELLED|CANCELED)\] [^\n]*`)

// Persist the submission intent before the request. Never automatically retry a
// timed-out submission: the backend may have created the task already.
func (s Service) SubmitCloud(key string, p Project, environment, prompt string) (CloudTask, error) {
	task := CloudTask{Key: key, Project: p.ID, Environment: strings.TrimSpace(environment), Prompt: strings.TrimSpace(prompt), State: "submitting", Updated: time.Now().Unix()}
	task.Directory = p.Path
	if !validCloudKey(key) || task.Environment == "" || task.Prompt == "" {
		return task, errors.New("enter an environment and a task")
	}
	lock, err := s.lock()
	if err != nil {
		return task, err
	}
	if _, err := os.Stat(s.cloudPath(key)); !errors.Is(err, os.ErrNotExist) {
		lock.Close()
		return task, errors.New("this workspace already has a submission; open its saved task instead")
	}
	if err = AtomicJSON(s.cloudPath(key), task); err != nil {
		lock.Close()
		return task, err
	}
	lock.Close()
	// All invocations use arguments, never shell interpolation. Explicit cwd is
	// unnecessary: send an explicit branch when this is a repository.
	if branch, e := command("git", "-C", p.Path, "branch", "--show-current"); e == nil {
		task.Branch = branch
	}
	args := []string{"exec", "--env", task.Environment, "--attempts", "1"}
	if task.Branch != "" {
		args = append(args, "--branch", task.Branch)
	}
	args = append(args, "--", task.Prompt)
	output, runErr := s.cloudCommand(2*time.Minute, p.Path, args...)
	match := cloudURL.FindStringSubmatch(output)
	if len(match) > 1 {
		task.URL, task.ID, task.State = match[0], match[1], "pending"
	} else {
		task.State = "submission uncertain"
		task.Error = "check codex cloud for the task before submitting again"
		task.Detail = output
		if runErr != nil {
			task.Error += ": " + runErr.Error()
		}
	}
	task.Updated = time.Now().Unix()
	if err = AtomicJSON(s.cloudPath(key), task); err != nil {
		return task, fmt.Errorf("save cloud task %s: %w", task.URL, err)
	}
	if task.ID == "" {
		return task, errors.New(task.Error)
	}
	return task, nil
}

// Native status exits 1 while PENDING and even after APPLIED. A recognized
// status line, rather than the exit code, establishes the task state.
func (s Service) RefreshCloud(key string) (CloudTask, error) {
	task, err := s.CloudTask(key)
	if err != nil {
		return task, err
	}
	if !cloudID.MatchString(task.ID) {
		return task, nil
	}
	output, runErr := s.cloudCommand(25*time.Second, task.Directory, "status", task.ID)
	match := statusLine.FindStringSubmatch(output)
	if len(match) > 1 {
		task.State, task.Detail, task.Error = strings.ToLower(match[1]), output, ""
		task.Updated = time.Now().Unix()
	} else {
		task.Error = "status unavailable; last known state is shown"
		if runErr != nil {
			task.Error += ": " + runErr.Error()
		}
		if output != "" {
			task.Error += " · " + output
		}
	}
	if err := AtomicJSON(s.cloudPath(key), task); err != nil {
		return task, err
	}
	return task, nil
}

func (s Service) AttachCloud(key, id string) (CloudTask, error) {
	task, err := s.CloudTask(key)
	if err != nil {
		return task, err
	}
	id = strings.TrimSpace(id)
	if match := cloudURL.FindStringSubmatch(id); len(match) > 1 {
		id = match[1]
	}
	if !cloudID.MatchString(id) {
		return task, errors.New("enter the task id or its ChatGPT link")
	}
	task.ID, task.URL, task.State, task.Error = id, "https://chatgpt.com/codex/tasks/"+id, "pending", ""
	if err := AtomicJSON(s.cloudPath(key), task); err != nil {
		return task, err
	}
	return s.RefreshCloud(key)
}

func CloudStateLabel(state string) string {
	switch state {
	case "":
		return "draft"
	case "pending":
		return "working · queued or running"
	case "ready":
		return "completed · ready to review"
	case "applied":
		return "completed · applied"
	case "error":
		return "failed"
	case "submitting":
		return "submitting · check cloud if interrupted"
	default:
		return state
	}
}

func (s Service) CloudProject(key string) (Project, error) {
	if !validCloudKey(key) {
		return Project{}, errors.New("invalid cloud workspace")
	}
	r, err := s.readRecord(key)
	if err != nil {
		return Project{}, err
	}
	if r.Kind != "cloud" {
		return Project{}, errors.New("not a cloud workspace")
	}
	return s.Project(r.Project)
}
func (s Service) CloudDiff(key string) (string, error) {
	task, err := s.CloudTask(key)
	if err != nil {
		return "", err
	}
	if !cloudID.MatchString(task.ID) {
		return "", errors.New("cloud task is not attached")
	}
	output, err := s.cloudCommand(30*time.Second, task.Directory, "diff", task.ID)
	if err != nil {
		return "", fmt.Errorf("cloud diff: %w: %s", err, output)
	}
	return output, nil
}

func (s Service) ExecCloud(directory string, args []string) error {
	if err := os.Chdir(directory); err != nil {
		return err
	}
	launcher, err := exec.LookPath(codexVPN)
	if err != nil {
		return err
	}
	return processRunner.Replace(launcher, append([]string{launcher, "cloud"}, args...), os.Environ())
}
