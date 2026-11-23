package dopamine

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type codexUsage struct {
	Limit      usageLimit `json:"rate_limit"`
	CodeReview usageLimit `json:"code_review_rate_limit"`
	Extra      []struct {
		Name    string     `json:"limit_name"`
		Feature string     `json:"metered_feature"`
		Limit   usageLimit `json:"rate_limit"`
	} `json:"additional_rate_limits"`
	Resets struct {
		Available *int `json:"available_count"`
		Credits   []struct {
			Status    string    `json:"status"`
			Title     string    `json:"title"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"credits"`
	} `json:"rate_limit_reset_credits"`
	updated time.Time
	stale   bool
}

type usageLimit struct {
	Primary   usageWindow `json:"primary_window"`
	Secondary usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	Used       *float64 `json:"used_percent"`
	Seconds    int64    `json:"limit_window_seconds"`
	ResetAt    int64    `json:"reset_at"`
	ResetAfter int64    `json:"reset_after_seconds"`
}

func readCodexUsage() codexUsage {
	cache, err := os.UserCacheDir()
	if err != nil {
		return codexUsage{}
	}
	// AI Usage Bar owns fetching and credentials. The dashboard only reads its
	// default OpenAI snapshot, shared by local and SSH clients of this dashboard.
	root := filepath.Join(cache, "ai-usagebar", "openai")
	file, err := os.Open(filepath.Join(root, "usage.json"))
	if err != nil {
		return codexUsage{}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return codexUsage{}
	}
	const maxSnapshotSize = 256 * 1024
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotSize+1))
	var usage codexUsage
	if err != nil || len(data) > maxSnapshotSize || json.Unmarshal(data, &usage) != nil {
		return codexUsage{}
	}
	usage.updated = info.ModTime()
	_, err = os.Stat(filepath.Join(root, ".stale"))
	usage.stale = err == nil
	return usage
}

func (u codexUsage) isStale(now time.Time) bool {
	return u.stale || now.Sub(u.updated) > 15*time.Minute
}

func (l usageLimit) windows() []usageWindow {
	var windows []usageWindow
	for _, w := range []usageWindow{l.Primary, l.Secondary} {
		if w.Used != nil && *w.Used >= 0 && *w.Used <= 100 {
			windows = append(windows, w)
		}
	}
	slices.SortStableFunc(windows, func(a, b usageWindow) int { return cmp.Compare(a.Seconds, b.Seconds) })
	return windows
}

func (w usageWindow) label() string {
	const hour = int64(time.Hour / time.Second)
	switch {
	case w.Seconds <= 0:
		return "quota"
	case w.Seconds%(24*hour) == 0:
		return fmt.Sprintf("%dd", w.Seconds/(24*hour))
	case w.Seconds%hour == 0:
		return fmt.Sprintf("%dh", w.Seconds/hour)
	default:
		return (time.Duration(w.Seconds) * time.Second).String()
	}
}

func (w usageWindow) percent() string {
	return fmt.Sprintf("%s %.0f%% used", w.label(), math.Round(*w.Used))
}

func resetCountdown(remaining time.Duration) string {
	minutes := int64(remaining / time.Minute)
	switch {
	case minutes >= 24*60:
		return fmt.Sprintf("%dd %dh", minutes/(24*60), minutes/60%24)
	case minutes >= 60:
		return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return "<1m"
	}
}

func (w usageWindow) resetTime(updated time.Time) time.Time {
	if w.ResetAt > 0 {
		return time.Unix(w.ResetAt, 0)
	}
	if w.ResetAfter > 0 {
		return time.Unix(updated.Unix()+w.ResetAfter, 0)
	}
	return time.Time{}
}

func (w usageWindow) summary(updated, now time.Time) string {
	reset := w.resetTime(updated)
	if reset.IsZero() {
		return w.percent()
	}
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return w.percent() + " · reset due"
	}
	return w.percent() + " · resets in " + resetCountdown(remaining)
}

func (u codexUsage) resetCount() string {
	if u.Resets.Available == nil || *u.Resets.Available < 0 {
		return "reset count unavailable"
	}
	if *u.Resets.Available == 1 {
		return "1 reset available"
	}
	return fmt.Sprintf("%d resets available", *u.Resets.Available)
}

func (u codexUsage) summary(width int, now time.Time) []string {
	if u.updated.IsZero() {
		return []string{"  " + muted.Render("codex usage unavailable · u details")}
	}
	heading := "codex usage"
	if u.isStale(now) {
		heading += " (stale)"
	}
	lines := []string{"  " + bright.Render(heading) + muted.Render(" · u details")}
	windows := u.Limit.windows()
	if len(windows) == 0 {
		lines = append(lines, "  "+muted.Render("quota unavailable"))
	}
	for _, w := range windows {
		lines = append(lines, "  "+w.summary(u.updated, now))
	}
	resets := u.resetCount()
	if u.Resets.Available != nil && *u.Resets.Available > 0 {
		var soonest time.Time
		for _, credit := range u.Resets.Credits {
			if credit.Status == "available" && !credit.ExpiresAt.IsZero() && (soonest.IsZero() || credit.ExpiresAt.Before(soonest)) {
				soonest = credit.ExpiresAt
			}
		}
		if soonest.After(now) {
			resets += " · exp " + soonest.Local().Format("02 Jan")
		} else if !soonest.IsZero() {
			resets += " · expiry passed"
		}
	}
	return append(lines, "  "+ansi.Truncate(resets, max(0, width-2), "…"))
}

func (u codexUsage) detailLines(width int, now time.Time) []string {
	if u.updated.IsZero() {
		return []string{"no AI Usage Bar data for Codex", "refresh Codex in AI Usage Bar on this host"}
	}
	const localDate = "02 Jan 2006 15:04 MST"
	updated := "updated " + u.updated.Local().Format(localDate)
	if u.isStale(now) {
		updated += " · stale"
	}
	lines := []string{ansi.Truncate(updated, width, "…"), ""}
	addLimit := func(label string, windows []usageWindow) {
		if len(windows) == 0 {
			return
		}
		lines = append(lines, ansi.Truncate(label, width, "…"))
		for _, w := range windows {
			lines = append(lines, "  "+w.summary(u.updated, now))
			if reset := w.resetTime(u.updated); !reset.IsZero() {
				lines = append(lines, "  resets "+reset.Local().Format(localDate))
			}
		}
		lines = append(lines, "")
	}
	addLimit("codex quota", u.Limit.windows())
	lines = append(lines, u.resetCount())
	if u.Resets.Available != nil && *u.Resets.Available > 0 {
		shown := 0
		for _, credit := range u.Resets.Credits {
			if credit.Status != "available" {
				continue
			}
			title := strings.TrimSpace(clean(credit.Title))
			if title == "" {
				title = "reset credit"
			}
			lines = append(lines, "  "+ansi.Truncate(title, max(0, width-2), "…"))
			expiry := "expiry unavailable"
			if !credit.ExpiresAt.IsZero() {
				label := "expires "
				if !credit.ExpiresAt.After(now) {
					label = "expired "
				}
				expiry = label + credit.ExpiresAt.Local().Format(localDate)
			}
			lines = append(lines, "  "+expiry)
			shown++
		}
		if shown < *u.Resets.Available {
			lines = append(lines, "  some reset details unavailable")
		}
	}
	lines = append(lines, "")
	addLimit("code review", u.CodeReview.windows())
	for _, extra := range u.Extra {
		label := strings.TrimSpace(clean(cmp.Or(extra.Name, extra.Feature)))
		if label == "" {
			label = "additional quota"
		}
		addLimit(label, extra.Limit.windows())
	}
	return lines[:len(lines)-1]
}

func (m Model) updateUsage(key string) Model {
	switch key {
	case "esc", "u":
		m.usageOpen = false
	case "up", "k":
		m.usageOffset = max(0, m.usageOffset-1)
	case "down", "j":
		lines := m.usage.detailLines(max(1, m.width-railWidth-3), time.Now())
		m.usageOffset = min(m.usageOffset+1, max(0, len(lines)-m.usageRows()))
	}
	return m
}

func (m Model) usageRows() int {
	return max(1, m.height-5)
}

func (m Model) usageView(width int) string {
	details := m.usage.detailLines(width, time.Now())
	rows := m.usageRows()
	start := min(m.usageOffset, max(0, len(details)-rows))
	footer := "esc back"
	if len(details) > rows {
		footer = "↑↓ scroll · " + footer
	}
	lines := []string{"", bright.Render("codex usage"), ""}
	lines = append(lines, details[start:min(start+rows, len(details))]...)
	lines = append(lines, "", muted.Render(footer))
	return strings.Join(lines, "\n")
}
