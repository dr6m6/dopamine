package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// FocusExternal uses Niri's documented IPC to focus the terminal that already
// owns a session. It neither attaches to nor interrupts Codex. No Kitty remote
// control or terminal/browser profile modification is needed.
func FocusExternal(pid int) error {
	if pid <= 1 {
		return errors.New("session terminal ownership is unavailable")
	}
	out, err := command("niri", "msg", "--json", "windows")
	if err != nil {
		return fmt.Errorf("existing terminal cannot be focused on this desktop: %w", err)
	}
	var windows []struct {
		ID  int `json:"id"`
		PID int `json:"pid"`
	}
	if err = json.Unmarshal([]byte(out), &windows); err != nil {
		return err
	}
	for i := 0; i < 20 && pid > 1; i++ {
		var ids []int
		for _, w := range windows {
			if w.PID == pid {
				ids = append(ids, w.ID)
			}
		}
		if len(ids) == 1 {
			_, err = command("niri", "msg", "action", "focus-window", "--id", strconv.Itoa(ids[0]))
			return err
		}
		if len(ids) > 1 {
			return errors.New("several terminal windows share this process; choose the existing window manually")
		}
		pid = parentPID(pid)
	}
	return errors.New("existing terminal not found; finish and close it manually before resuming this UUID")
}

// OpenNativeApp focuses the configured Niri window or starts the project in
// the same managed session used by Start server and Stop server.
func (s Service) OpenNativeApp(p Project) error {
	if p.AppID == "" {
		return errors.New("no native app ID configured")
	}
	id, err := nativeAppWindow(p.AppID)
	if err != nil {
		return err
	}
	if id == 0 {
		r, err := s.Start(p, "app", "")
		if err != nil {
			return err
		}
		deadline := time.Now().Add(15 * time.Second)
		for id == 0 && time.Now().Before(deadline) {
			id, err = nativeAppWindow(p.AppID)
			if err != nil {
				return err
			}
			if id != 0 {
				break
			}
			if r.Pane != "" && !liveRecord(r) && !applicationProcessAlive(r) {
				return fmt.Errorf("application exited before opening a window · %s", r.Log)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	if id == 0 {
		return fmt.Errorf("no window for %s yet · use Open when it appears", p.Name)
	}
	_, err = command("niri", "msg", "action", "focus-window", "--id", strconv.Itoa(id))
	return err
}

func nativeAppWindow(appID string) (int, error) {
	out, err := command("niri", "msg", "--json", "windows")
	if err != nil {
		return 0, fmt.Errorf("native app window unavailable: %w", err)
	}
	var windows []struct {
		ID      int    `json:"id"`
		AppID   string `json:"app_id"`
		Focused bool   `json:"is_focused"`
	}
	if err = json.Unmarshal([]byte(out), &windows); err != nil {
		return 0, err
	}
	id, count := 0, 0
	for _, window := range windows {
		if window.AppID != appID {
			continue
		}
		if window.Focused {
			return window.ID, nil
		}
		id = window.ID
		count++
	}
	if count > 1 {
		return 0, fmt.Errorf("several windows match %s · focus the intended window", appID)
	}
	return id, nil
}
