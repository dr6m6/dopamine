package projecthub

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
