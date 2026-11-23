package dopamine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func usageCache(t *testing.T, data string) string {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	dir := filepath.Join(cache, "ai-usagebar", "openai")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWeeklyOnlyUsageAndResetCredits(t *testing.T) {
	path := usageCache(t, `{
		"rate_limit":{"primary_window":{"used_percent":12.5,"limit_window_seconds":604800,"reset_after_seconds":7200},"secondary_window":null},
		"rate_limit_reset_credits":{"available_count":3,"credits":[
			{"status":"redeemed","title":"Spent reset","expires_at":"2026-11-25T12:00:00Z"},
			{"status":"available","title":"Full reset","expires_at":"2026-12-12T12:00:00Z"},
			{"status":"available","title":"Weekly reset","expires_at":null}
		]}
	}`)
	updated := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
	u := readCodexUsage()
	summary := ansi.Strip(strings.Join(u.meters(38, updated.Add(5*time.Minute)), "\n"))
	for _, label := range []string{"codex usage", "7d 13% · reset 1h 55m", "█", "░"} {
		if !strings.Contains(summary, label) {
			t.Fatalf("missing %q in %q", label, summary)
		}
	}
	for _, label := range []string{"5h", "stale", "3 resets available", "u details"} {
		if strings.Contains(summary, label) {
			t.Fatalf("unexpected %q in %q", label, summary)
		}
	}
	details := strings.Join(u.detailLines(38, updated.Add(time.Hour)), "\n")
	for _, label := range []string{"stale", "3 resets available", "Full reset", "expires " + time.Date(2026, 12, 12, 12, 0, 0, 0, time.UTC).Local().Format("02 Jan 2006 15:04 MST"), "Weekly reset", "expiry unavailable", "some reset details unavailable"} {
		if !strings.Contains(details, label) {
			t.Fatalf("missing %q in %q", label, details)
		}
	}
	if strings.Contains(details, "Spent reset") {
		t.Fatalf("redeemed credit shown in %q", details)
	}
}

func TestUsageMeterSegments(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		cells   string
	}{{0, "░░░░░░░░░░"}, {36, "████░░░░░░"}, {100, "██████████"}} {
		meter := usageMeter(10, tc.percent)
		if got := ansi.Strip(meter); got != tc.cells || ansi.StringWidth(meter) != 10 {
			t.Fatalf("%g%%: %q", tc.percent, got)
		}
		if tc.percent == 36 && (!strings.Contains(meter, "\x1b[38;5;255m") || !strings.Contains(meter, "\x1b[38;5;245m")) {
			t.Fatalf("meter segments lack distinct colors: %q", meter)
		}
	}
}

func TestUsageViewAtMinimumSize(t *testing.T) {
	usageCache(t, `{
		"rate_limit":{"primary_window":{"used_percent":43,"limit_window_seconds":604800},"secondary_window":{"used_percent":0,"limit_window_seconds":18000}},
		"additional_rate_limits":[{"metered_feature":"model quota","rate_limit":{"primary_window":{"used_percent":90,"limit_window_seconds":18000}}}],
		"rate_limit_reset_credits":{"available_count":3,"credits":[
			{"status":"available","title":"Full reset"},
			{"status":"available","title":"Weekly reset"},
			{"status":"available","title":"Extra reset"}
		]}
	}`)
	m := dashboardFixture()
	m.width, m.height = 46, 16
	m.status = "server started"
	for range 4 {
		m.projects = append(m.projects, m.projects[0])
	}
	m.usage = readCodexUsage()
	view := ansi.Strip(m.View().Content)
	heading := strings.Index(view, "codex usage")
	session, weekly := strings.Index(view, "5h 0% used"), strings.Index(view, "7d 43% used")
	notifications, status := strings.Index(view, "notifications:"), strings.Index(view, m.status)
	if notifications < 0 || status <= notifications || heading <= status || session <= heading || weekly <= session || !strings.Contains(view, "6 projects") || !strings.Contains(view, "█") {
		t.Fatalf("incomplete dashboard at 46 × 16: %s", view)
	}
	if strings.Contains(view, "3 resets available") || strings.Contains(view, "u details") {
		t.Fatalf("reset credits or shortcut leaked onto the dashboard: %s", view)
	}
	help, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !strings.Contains(ansi.Strip(help.View().Content), "u            codex usage and resets") {
		t.Fatalf("usage shortcut missing in help: %s", help.View().Content)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	m = updated.(Model)
	if view = ansi.Strip(m.View().Content); !strings.Contains(view, "3 resets available") || !strings.Contains(view, "Full reset") || !strings.Contains(view, "↑↓ scroll · esc back") {
		t.Fatalf("usage details did not open: %s", view)
	}
	for range 10 {
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = updated.(Model)
	}
	if view = ansi.Strip(m.View().Content); !strings.Contains(view, "Extra reset") || !strings.Contains(view, "model quota") || !strings.Contains(view, "5h 90% used") {
		t.Fatalf("details could not be scrolled: %s", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m = updated.(Model); m.usageOpen || !strings.Contains(m.View().Content, "+ add folder") {
		t.Fatal("Esc did not return to the dashboard")
	}
}

func TestUsageCacheUnavailableAndZeroResets(t *testing.T) {
	path := usageCache(t, `{"rate_limit_reset_credits":{"available_count":0}}`)
	if got := readCodexUsage().resetCount(); got != "0 resets available" {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte(`{"rate_limit":`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readCodexUsage(); !got.updated.IsZero() || got.resetCount() != "reset count unavailable" {
		t.Fatal("invalid cache produced a usage snapshot")
	}
}
