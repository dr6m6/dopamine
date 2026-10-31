package projecthub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNotificationsDefaultOnAndPersistWithoutChangingOtherSettings(t *testing.T) {
	s, _ := fixture(t)
	if enabled, err := s.NotificationsEnabled(); err != nil || !enabled {
		t.Fatal("notifications must default to on", enabled, err)
	}
	path := filepath.Join(s.ConfigDir, "settings.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reading the default created a settings file", err)
	}
	write(t, path, `{"future":{"keep":42}}`)
	for _, expected := range []bool{false, true, false} {
		if enabled, err := s.ToggleNotifications(); err != nil || enabled != expected {
			t.Fatal("toggle failed", enabled, err)
		}
		fresh := Service{ConfigDir: s.ConfigDir}
		if enabled, err := fresh.NotificationsEnabled(); err != nil || enabled != expected {
			t.Fatal("new service did not read saved preference", enabled, err)
		}
		data, err := os.ReadFile(path)
		var settings map[string]any
		if err != nil || json.Unmarshal(data, &settings) != nil || !reflect.DeepEqual(settings["future"], map[string]any{"keep": float64(42)}) {
			t.Fatal("other settings were lost", string(data), err)
		}
	}
}

func TestInvalidNotificationSettingsAreNotOverwritten(t *testing.T) {
	s, _ := fixture(t)
	path := filepath.Join(s.ConfigDir, "settings.json")
	for _, invalid := range []string{`{"notifications":"off"}`, `{"notifications":null}`, `null`, `{broken`} {
		write(t, path, invalid)
		if enabled, err := s.NotificationsEnabled(); err == nil || enabled {
			t.Fatal("invalid settings did not fail closed", invalid, enabled, err)
		}
		if _, err := s.ToggleNotifications(); err == nil {
			t.Fatal("invalid settings were silently replaced", invalid)
		}
		if data, _ := os.ReadFile(path); string(data) != invalid {
			t.Fatal("invalid settings were modified", string(data))
		}
	}
}
