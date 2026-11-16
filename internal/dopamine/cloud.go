package dopamine

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

type cloudUpdated struct {
	task workspace.CloudTask
	err  error
}
type cloudTick struct{}
type cloudSaved struct{ err error }
type cloudDiff struct {
	text string
	err  error
}

type CloudModel struct {
	service       workspace.Service
	project       workspace.Project
	key           string
	task          workspace.CloudTask
	environment   textinput.Model
	prompt        textarea.Model
	recovery      textinput.Model
	viewport      viewport.Model
	width, height int
	field         int
	busy, polling bool
	watching      bool
	status        string
	showingDiff   bool
}

func NewCloud(s workspace.Service, p workspace.Project, key string) CloudModel {
	environment := textinput.New()
	environment.Prompt = "environment: "
	environment.Placeholder = "name or id from codex cloud"
	environment.SetValue(p.CloudEnvironment)
	environment.SetVirtualCursor(true)
	prompt := textarea.New()
	prompt.Placeholder = "what should codex do?"
	prompt.CharLimit = 32000
	prompt.ShowLineNumbers = false
	prompt.SetVirtualCursor(true)
	recovery := textinput.New()
	recovery.Prompt = "task: "
	recovery.Placeholder = "task id or ChatGPT link"
	recovery.SetVirtualCursor(true)
	m := CloudModel{service: s, project: p, key: key, width: 74, height: 24, environment: environment, prompt: prompt, recovery: recovery, viewport: viewport.New()}
	if task, err := s.CloudTask(key); err == nil {
		m.task = task
		m.watching, m.polling = task.ID != "", task.ID != ""
		if task.ID == "" {
			m.recovery.Focus()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.status = err.Error()
		m.busy = true
	}
	if m.task.Key == "" {
		if p.CloudEnvironment == "" {
			m.environment.Focus()
		} else {
			m.field = 1
			m.prompt.Focus()
		}
	}
	return m
}
func (m CloudModel) Init() tea.Cmd {
	if m.task.ID != "" {
		return tea.Batch(m.refresh(), cloudNextTick())
	}
	return textinput.Blink
}
func cloudNextTick() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return cloudTick{} })
}
func (m CloudModel) refresh() tea.Cmd {
	return func() tea.Msg { task, err := m.service.RefreshCloud(m.key); return cloudUpdated{task, err} }
}
func (m CloudModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.environment.SetWidth(max(1, v.Width-18))
		m.recovery.SetWidth(max(1, v.Width-10))
		m.prompt.SetWidth(max(1, v.Width-4))
		m.prompt.SetHeight(max(2, min(8, v.Height-12)))
		m.viewport.SetWidth(max(1, v.Width-4))
		m.viewport.SetHeight(max(1, v.Height-7))
	case cloudUpdated:
		m.busy, m.polling = false, false
		if v.task.Key != "" {
			m.task = v.task
		}
		if v.err != nil {
			m.status = v.err.Error()
			if m.task.Key != "" && m.task.ID == "" {
				m.recovery.Focus()
			}
			return m, nil
		} else {
			m.status = ""
		}
		if m.task.ID != "" {
			if !m.watching {
				m.watching = true
				m.polling = true
				return m, tea.Batch(m.refresh(), cloudNextTick())
			}
			return m, nil
		}
		m.recovery.Focus()
	case cloudDiff:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.showingDiff = true
			m.viewport.SetContent(v.text)
			m.viewport.GotoTop()
		}
	case cloudTick:
		if m.task.ID != "" {
			if !m.polling {
				m.polling = true
				return m, tea.Batch(m.refresh(), cloudNextTick())
			}
			return m, cloudNextTick()
		}
	case tea.KeyPressMsg:
		key := v.String()
		if key == "esc" {
			if m.showingDiff {
				m.showingDiff = false
				return m, nil
			}
			return m, func() tea.Msg { return finished{err: m.service.FocusSlot(1)} }
		}
		if m.busy {
			return m, nil
		}
		if m.showingDiff {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		if m.task.Key != "" {
			if m.task.ID == "" {
				if key == "enter" {
					id := m.recovery.Value()
					m.busy = true
					return m, func() tea.Msg { task, err := m.service.AttachCloud(m.key, id); return cloudUpdated{task, err} }
				}
				var cmd tea.Cmd
				m.recovery, cmd = m.recovery.Update(msg)
				return m, cmd
			}
			switch key {
			case "r":
				if !m.polling {
					m.polling = true
					return m, m.refresh()
				}
			case "o":
				return m, func() tea.Msg { return cloudSaved{workspace.OpenURL(m.task.URL, "cloud")} }
			case "d":
				m.busy = true
				return m, func() tea.Msg { text, err := m.service.CloudDiff(m.key); return cloudDiff{text, err} }
			}
			return m, nil
		}
		switch key {
		case "tab", "shift+tab":
			m.field = 1 - m.field
			if m.field == 0 {
				m.prompt.Blur()
				return m, m.environment.Focus()
			}
			m.environment.Blur()
			return m, m.prompt.Focus()
		case "enter":
			if m.field == 0 {
				m.field = 1
				m.environment.Blur()
				return m, m.prompt.Focus()
			}
		case "ctrl+s":
			environment, prompt := strings.TrimSpace(m.environment.Value()), strings.TrimSpace(m.prompt.Value())
			if environment == "" || prompt == "" {
				m.status = "enter an environment and a task"
				return m, nil
			}
			m.busy = true
			m.status = "submitting to codex cloud…"
			return m, func() tea.Msg {
				if err := m.service.SetCloudEnvironment(m.project, environment); err != nil {
					return cloudUpdated{err: err}
				}
				task, err := m.service.SubmitCloud(m.key, m.project, environment, prompt)
				return cloudUpdated{task, err}
			}
		}
	case cloudSaved:
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.status = "opened in browser"
		}
	case finished:
		if v.err != nil {
			m.status = v.err.Error()
		}
	}
	if m.task.Key == "" && !m.busy {
		var cmd tea.Cmd
		if m.field == 0 {
			m.environment, cmd = m.environment.Update(msg)
		} else {
			m.prompt, cmd = m.prompt.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}
func (m CloudModel) View() tea.View {
	lines := []string{"", bright.Render("  codex cloud · " + clean(m.project.Name)), ""}
	if m.showingDiff {
		lines = append(lines, "  cloud diff · ↑↓ / pgup / pgdown · esc: back", "", m.viewport.View())
	} else if m.task.Key == "" {
		lines = append(lines, m.environment.View(), "", m.prompt.View(), "", muted.Render("tab: environment / task · ctrl+s: send · esc: dashboard"), muted.Render("cloud uses the remote branch; local edits stay here."))
	} else {
		lines = append(lines, "  status: "+bright.Render(workspace.CloudStateLabel(m.task.State)))
		if m.task.Updated > 0 {
			lines = append(lines, muted.Render("  checked: "+time.Unix(m.task.Updated, 0).Local().Format("15:04:05")))
		}
		if m.task.URL != "" {
			lines = append(lines, "", "  "+clean(m.task.URL))
		}
		if m.task.Branch != "" {
			lines = append(lines, muted.Render("  branch: "+clean(m.task.Branch)))
		}
		detail := m.task.Detail
		if detail == "" {
			detail = m.task.Prompt
		}
		if m.task.Error != "" {
			detail += "\n\n" + m.task.Error
		}
		if m.status != "" {
			detail = m.status + "\n\n" + detail
		}
		footer := []string{"", muted.Render("r: refresh · o: result · d: diff · esc: back"), muted.Render("closing this tab keeps the cloud task running.")}
		if m.task.ID == "" {
			footer = []string{"", muted.Render("check codex cloud; attach the task below."), m.recovery.View(), muted.Render("enter: attach · esc: dashboard")}
		}
		available := max(1, m.height-len(lines)-len(footer)-1)
		body := strings.Split(ansi.Wrap(clean(detail), max(1, m.width-4), ""), "\n")
		if len(body) > available {
			body = body[:available]
			body[len(body)-1] = ansi.Truncate(body[len(body)-1], max(1, m.width-5), "") + "…"
		}
		lines = append(lines, "")
		lines = append(lines, body...)
		lines = append(lines, footer...)

	}
	if m.status != "" && m.task.Key == "" {
		lines = append(lines, ansi.Truncate(clean(m.status), max(1, m.width), "…"))
	}
	text := strings.Split(strings.Join(lines, "\n"), "\n")
	if len(text) > m.height {
		text = text[:max(0, m.height)]
	}
	for i := range text {
		text[i] = ansi.Truncate(text[i], max(0, m.width), "")
	}
	v := tea.NewView(strings.Join(text, "\n"))
	v.AltScreen = true
	v.WindowTitle = "dopamine · cloud"
	return v
}
func RunCloud(s workspace.Service, key string) error {
	p, err := s.CloudProject(key)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(NewCloud(s, p, key)).Run()
	return err
}

// Keep a small status vocabulary in the rail; the task tab spells it out.
func cloudMarker(state string) string {
	switch state {
	case "pending", "running", "queued", "submitting", "connecting":
		return "~"
	case "ready", "applied":
		return "✓"
	case "error", "cancelled", "canceled":
		return "!"
	case "submission uncertain", "attention", "unknown":
		return "?"
	default:
		return ""
	}
}

func cloudTitle(task workspace.CloudTask) string {
	title := strings.Fields(clean(task.Prompt))
	return fmt.Sprintf("%s  %s", workspace.CloudStateLabel(task.State), strings.Join(title, " "))
}
