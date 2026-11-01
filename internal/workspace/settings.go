package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (s Service) notificationSettings() (map[string]json.RawMessage, bool, error) {
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(filepath.Join(s.ConfigDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return doc, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return nil, false, errors.New("settings.json requires a valid JSON object")
	}
	enabled := true
	if raw, ok := doc["notifications"]; ok {
		var value *bool
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return nil, false, errors.New("settings.json: notifications requires true or false")
		}
		enabled = *value
	}
	return doc, enabled, nil
}

// Read at delivery time so running notification callbacks and observers see
// the current preference without restarting Codex or changing its configuration.
func (s Service) NotificationsEnabled() (bool, error) {
	_, enabled, err := s.notificationSettings()
	return enabled, err
}

func (s Service) ToggleNotifications() (bool, error) {
	lock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer lock.Close()
	doc, enabled, err := s.notificationSettings()
	if err != nil {
		return false, err
	}
	if enabled {
		doc["notifications"] = json.RawMessage("false")
	} else {
		doc["notifications"] = json.RawMessage("true")
	}
	if err := AtomicJSON(filepath.Join(s.ConfigDir, "settings.json"), doc); err != nil {
		return false, err
	}
	return !enabled, nil
}
