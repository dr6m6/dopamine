// Package dopamine is the deliberately small dopamine terminal interface.
package dopamine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dr6m6/dopamine/internal/workspace"
)

const Molecule = `⢀⡀      ⣀⠤⡢⢄⡀      ⢀⡠⠔⠤⣀      ⢀⡀
 ⠈⠑⠢⢄⠤⠒⠉  ⠈⠑⠪⢕⡢⢄⡠⠔⠊⠁    ⠉⠒⠤⡠⠔⠊⠁
    ⢸⢰        ⠈ ⡇
    ⢸⢸          ⡇
    ⢸⠸        ⢀ ⡇
 ⢀⡠⠔⠊⠒⠤⣀  ⢀⡠⠔⣊⠥⠒⠁
⠈⠁      ⠉⠒⠥⠒⠉`

const railWidth = 5
const globalActionCount = 3

var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var bright = lipgloss.NewStyle().Bold(true)
var selected = lipgloss.NewStyle().Reverse(true)

type loaded struct {
	usage codexUsage

	projects           []workspace.Project
	apps               map[string]workspace.AppStatus
	slots              []workspace.Slot
	err                error
	notificationsMuted bool
}
type tick struct{}
type finished struct {
	message         string
	err             error
	workspaceOpened bool
}
type foldersChanged struct {
	projects  []workspace.Project
	projectID string
	err       error
}
type notificationsToggled struct {
	enabled bool
	err     error
}

type Model struct {
	usage       codexUsage
	usageOpen   bool
	usageOffset int

	service            workspace.Service
	projects           []workspace.Project
	apps               map[string]workspace.AppStatus
	slots              []workspace.Slot
	width, height      int
	project            int
	action             int
	busy, ready        bool
	sidebar            bool
	window             string
	status             string
	help               bool
	projectOpen        bool
	addingFolder       bool
	removingFolder     bool
	folderInput        textinput.Model
	notificationsMuted bool
	serverSettings     bool
	autostartChoice    int
	cloudHistory       bool
	cloudTasks         []workspace.CloudTask
	cloudChoice        int
}

func New(s workspace.Service, sidebar bool, window string) Model {
	input := textinput.New()
	input.Prompt = "path: "
	input.Placeholder = "~/folder"
	input.SetVirtualCursor(true)
	return Model{service: s, sidebar: sidebar, window: window, width: 80, height: 24, folderInput: input}
}

func (m Model) Init() tea.Cmd { return m.load() }

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		slots, err := m.service.Slots()
		if m.sidebar {
			return loaded{slots: slots, err: err}
		}
		usage := readCodexUsage()
		r, loadErr := m.service.Load()
		if loadErr != nil {
			return loaded{slots: slots, err: loadErr, usage: usage}
		}
		enabled, settingsErr := m.service.NotificationsEnabled()
		return loaded{projects: r.Projects, apps: m.service.AppStatuses(r.Projects), slots: slots, err: errors.Join(err, settingsErr), notificationsMuted: !enabled, usage: usage}
	}
}

func nextTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tick{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
	case loaded:
		m.usage = v.usage
		id := ""
		global := m.ready && !m.projectOpen && m.project >= len(m.projects)
		globalIndex := m.project - len(m.projects)
		if m.project < len(m.projects) {
			id = m.projects[m.project].ID
		}
		m.projects, m.apps, m.slots, m.ready = v.projects, v.apps, v.slots, true
		m.notificationsMuted = v.notificationsMuted
		// Keep the registry order within each group and preserve selection by ID.
		sort.SliceStable(m.projects, func(i, j int) bool {
			return len(m.projects[i].Launch) > 0 && len(m.projects[j].Launch) == 0
		})
		found := false
		for i, p := range m.projects {
			if p.ID == id {
				m.project = i
				found = true
				break
			}
		}
		if !found {
			m.projectOpen = false
			m.removingFolder = false
			m.serverSettings, m.cloudHistory = false, false
		}
		if global {
			m.project = len(m.projects) + min(globalActionCount-1, globalIndex)
		} else {
			m.project = max(0, min(m.project, len(m.projects)-1))
		}
		m.action = min(m.action, len(m.projectActions())-1)
		if v.err != nil {
			m.status = v.err.Error()
		}
		return m, nextTick()
	case tick:
		return m, m.load()
	case serverModeChanged:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.serverSettings = false
			m.status = "server autostart: " + workspace.AutostartLabel(v.mode)
			for i := range m.projects {
				if m.projects[i].ID == v.project {
					m.projects[i].ServerAutostart = v.mode
				}
			}
		}
	case cloudHistoryLoaded:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.cloudHistory, m.cloudTasks, m.cloudChoice, m.status = true, v.tasks, 0, ""
		}
	case finished:
		m.busy = false
		m.status = v.message
		if v.err != nil {
			m.status = v.err.Error()
		}
		if v.workspaceOpened && v.err == nil {
			m.projectOpen = false
			m.cloudHistory, m.serverSettings = false, false
			m.action = 0
		}
		// The regular refresh chain remains serial. Do not start a second poll.
	case notificationsToggled:
		m.busy = false
		m.status = ""
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.notificationsMuted = !v.enabled
		}
	case foldersChanged:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		m.addingFolder, m.removingFolder, m.projectOpen = false, false, false
		m.folderInput.Blur()
		m.status = "folder removed from list"
		m.projects = v.projects
		sort.SliceStable(m.projects, func(i, j int) bool {
			return len(m.projects[i].Launch) > 0 && len(m.projects[j].Launch) == 0
		})
		m.project = max(0, min(m.project, len(m.projects)-1))
		for i, p := range m.projects {
			if p.ID == v.projectID {
				m.project = i
				m.status = "folder selected · " + p.Name
				break
			}
		}
	case tea.MouseClickMsg:
		mouse := v.Mouse()
		if mouse.Button == tea.MouseLeft && mouse.X < 5 {
			i := m.railSlotAt(mouse.Y)
			if i >= 0 && i < len(m.slots) {
				return m, m.focus(i + 1)
			}
		}
	case tea.KeyPressMsg:
		key := v.String()
		if m.addingFolder {
			return m.updateFolderInput(msg)
		}
		if m.removingFolder {
			return m.updateFolderRemoval(key)
		}
		if m.serverSettings {
			return m.updateServerSettings(key)
		}
		if m.cloudHistory {
			return m.updateCloudHistory(key)
		}
		if !m.sidebar && (key == "?" || (key == "esc" && m.help)) {
			m.help = !m.help
			return m, nil
		}
		if m.help && key != "q" && key != "ctrl+c" {
			return m, nil
		}
		if key == "ctrl+c" || key == "q" {
			if m.sidebar {
				return m, m.focus(1)
			}
			return m, func() tea.Msg {
				if err := exec.Command("tmux", "detach-client").Run(); err != nil {
					return finished{err: err}
				}
				return tea.Quit()
			}
		}
		if n, err := strconv.Atoi(key); err == nil && n > 0 && n <= len(m.slots) {
			return m, m.focus(n)
		}
		if key == "tab" || key == "shift+tab" {
			direction := 1
			if key == "shift+tab" {
				direction = -1
			}
			return m, func() tea.Msg { return finished{err: m.service.CycleSlot(direction)} }
		}
		if m.sidebar || m.busy {
			return m, nil
		}
		if m.usageOpen {
			return m.updateUsage(key), nil
		}
		if key == "esc" && m.projectOpen {
			m.projectOpen, m.status = false, ""
			return m, nil
		}
		if !m.projectOpen {
			switch key {
			case "u":
				m.usageOpen, m.usageOffset = true, 0
			case "j", "down":
				m.project = min(len(m.projects)+globalActionCount-1, m.project+1)
			case "k", "up":
				m.project = max(0, m.project-1)
			case "enter":
				switch {
				case m.project == len(m.projects):
					m.busy, m.status = true, "opening terminal…"
					return m, m.perform("t", workspace.Project{Path: m.service.Home})
				case m.project == len(m.projects)+1:
					m.addingFolder, m.status = true, ""
					m.folderInput.Reset()
					return m, m.folderInput.Focus()
				case m.project == len(m.projects)+2:
					m.busy = true
					return m, func() tea.Msg {
						enabled, err := m.service.ToggleNotifications()
						return notificationsToggled{enabled: enabled, err: err}
					}
				case len(m.projects) > 0:
					m.projectOpen, m.action, m.status = true, 0, ""
				}
			}
			return m, nil
		}
		switch key {
		case "k", "up", "left", "h":
			m.action = (m.action + len(m.projectActions()) - 1) % len(m.projectActions())
		case "j", "down", "right", "l":
			m.action = (m.action + 1) % len(m.projectActions())
		case "s", "x", "o", "b", "n", "t", "c", "a", "enter":
			if len(m.projects) == 0 {
				return m, nil
			}
			if key == "enter" {
				key = m.projectActions()[m.action].key
			}
			if key == "remove" {
				m.removingFolder, m.status = true, ""
				return m, nil
			}
			if key == "a" && len(m.projects[m.project].Launch) > 0 {
				m.serverSettings, m.autostartChoice, m.status = true, 0, ""
				for i, mode := range serverModes {
					if m.projects[m.project].ServerAutostart == mode {
						m.autostartChoice = i
					}
				}
				return m, nil
			}
			if key == "history" {
				m.busy = true
				return m, func() tea.Msg {
					tasks, err := m.service.CloudTasks(m.projects[m.project].ID)
					return cloudHistoryLoaded{tasks: tasks, err: err}
				}
			}
			m.busy, m.status = true, "working…"
			return m, m.perform(key, m.projects[m.project])
		}
	}
	if m.addingFolder {
		return m.updateFolderInput(msg)
	}
	return m, nil
}

func (m Model) focus(number int) tea.Cmd {
	return func() tea.Msg { return finished{err: m.service.FocusSlot(number)} }
}

func (m Model) perform(key string, p workspace.Project) tea.Cmd {
	return func() tea.Msg {
		switch key {
		case "s":
			if p.AppID != "" {
				return finished{message: "opened · " + p.Name, err: m.service.OpenNativeApp(p)}
			}
			_, err := m.service.Start(p, "app", "")
			return finished{message: "server started · " + p.Name, err: err}
		case "x":
			return finished{message: "server stopped · " + p.Name, err: m.service.StopApp(p)}
		case "o", "b":
			if p.AppID != "" {
				return finished{message: "opened · " + p.Name, err: m.service.OpenNativeApp(p)}
			}
			if p.AppURL == "" {
				return finished{err: fmt.Errorf("no url configured for %s · edit %s", p.Name, m.service.RegistryPath())}
			}
			return finished{message: "opened · " + p.AppURL, err: workspace.OpenURL(p.AppURL, "app")}
		case "n", "t", "c":
			if key == "c" {
				r, err := m.service.StartNativeCloud(p)
				if r.Pane != "" {
					err = errors.Join(err, m.service.FocusNativeCloud(r))
				}
				return finished{message: "cloud session opened · " + p.Name, err: err, workspaceOpened: err == nil}
			}
			kind := "codex"
			if key == "t" {
				kind = "shell"
			}
			r, err := m.service.Start(p, kind, "")
			if err == nil {
				err = m.service.ConfigureDopamine()
			}
			if err == nil {
				_, err = exec.Command("tmux", "select-window", "-t", r.Target).CombinedOutput()
			}
			if err == nil {
				_, err = exec.Command("tmux", "select-pane", "-t", r.Pane).CombinedOutput()
			}
			return finished{message: "workspace opened · " + p.Name, err: err, workspaceOpened: err == nil}
		}
		return finished{}
	}
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}

func (m Model) current() int {
	for i, slot := range m.slots {
		if (m.sidebar && slot.Window == m.window) || (!m.sidebar && slot.Dashboard) {
			return i
		}
	}
	return 0
}

func (m Model) railOffset() int {
	return max(0, m.current()-max(1, m.height-2)+1)
}

func (m Model) rail() string {
	lines := []string{"", ""}
	for i := m.railOffset(); i < len(m.slots) && len(lines) < m.height; i++ {
		label := fmt.Sprintf(" %d ", i+1)
		if m.slots[i].Kind == "cloud" || m.slots[i].Kind == "cloud-native" {
			badge := "c"
			if m.slots[i].CloudHost == "local" {
				badge = "l"
			}
			if m.slots[i].CloudHost == "unknown" {
				badge = "?"
			}
			label = fmt.Sprintf(" %d%s%s", i+1, badge, cloudMarker(m.slots[i].CloudState))
		}
		if i == m.current() {
			label = selected.Render(label)
		} else {
			label = muted.Render(label)
		}
		lines = append(lines, lipgloss.PlaceHorizontal(railWidth, lipgloss.Left, label))
	}
	return strings.Join(lines, "\n")
}

func (m Model) View() tea.View {
	var content string
	if m.sidebar {
		content = m.rail()
	} else {
		body := m.dashboard(max(1, m.width-railWidth-3))
		if m.help {
			body = m.helpView()
		}
		// Match the native rail plus tmux's blank separator column.
		rail := lipgloss.NewStyle().Width(railWidth + 1).Height(max(1, m.height)).Render(m.rail())
		content = lipgloss.JoinHorizontal(lipgloss.Top, rail, "  "+strings.ReplaceAll(body, "\n", "\n  "))
	}
	// Clip once at the terminal boundary, including very small resize events.
	lines := strings.Split(content, "\n")
	if len(lines) > m.height {
		lines = lines[:max(0, m.height)]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(0, m.width), "")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "dopamine"
	return v
}

func (m Model) dashboard(width int) string {
	if m.width < 46 || m.height < 16 {
		return "\n" + "  " + bright.Render("dopamine") + "\n\nenlarge terminal to 46 × 16."
	}
	if m.usageOpen {
		return m.usageView(width)
	}
	if m.projectOpen && len(m.projects) > 0 {
		if m.serverSettings {
			return m.serverSettingsView(width)
		}
		if m.cloudHistory {
			return m.cloudHistoryView(width)
		}
		return m.projectView()
	}
	if m.addingFolder {
		return m.folderView(width)
	}
	lines := []string{"", "  " + bright.Render("dopamine"), ""}
	usage := m.usage.meters(width, time.Now())
	status := clean(m.status)
	reserved := 7 + len(usage) // actions (5), project counter (1), and space above usage (1)
	if status != "" {
		reserved += 2
	}
	if m.height >= max(22, 21+len(usage)) {
		for _, line := range strings.Split(Molecule, "\n") {
			lines = append(lines, muted.Render("  "+line))
		}
		lines = append(lines, "")
	}
	rows := max(1, m.height-len(lines)-reserved)
	start := max(0, min(m.project, len(m.projects)-1)-rows+1)
	nameWidth := 0
	for _, p := range m.projects {
		nameWidth = max(nameWidth, ansi.StringWidth(clean(p.Name)))
	}
	nameWidth = min(nameWidth, max(8, width-12))
	if !m.ready {
		lines = append(lines, muted.Render("loading projects…"))
	} else if len(m.projects) == 0 {
		lines = append(lines, "no projects yet.")
	} else {
		for i := start; i < len(m.projects) && i < start+rows; i++ {
			p := m.projects[i]
			prefix := "  "
			if i == m.project {
				prefix = "> "
			}
			name := ansi.Truncate(clean(p.Name), nameWidth, "…")
			name += strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))
			line := prefix + name
			if len(p.Launch) > 0 {
				state := strings.ToLower(m.apps[p.ID].State)
				if state == "" {
					state = "stopped"
				}
				line += muted.Render("  " + state)
			}
			lines = append(lines, line)
		}
		if len(m.projects) > rows {
			lines = append(lines, muted.Render(fmt.Sprintf("%d / %d projects", min(m.project+1, len(m.projects)), len(m.projects))))
		}
	}
	lines = append(lines, "")
	for i, label := range []string{"new terminal", "+ add folder…"} {
		prefix := "  "
		if m.project == len(m.projects)+i {
			prefix = "> "
		}
		lines = append(lines, prefix+label)
	}
	prefix, notifications := "  ", "on"
	if m.project == len(m.projects)+2 {
		prefix = "> "
	}
	if m.notificationsMuted {
		notifications = "off"
	}
	lines = append(lines, "", prefix+"notifications: "+notifications)
	if status != "" {
		lines = append(lines, "", status)
	}
	for len(lines)+len(usage) < m.height {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, usage...), "\n")
}

func (m Model) projectView() string {
	p := m.projects[m.project]
	lines := []string{"", "  " + bright.Render("dopamine"), "", "  " + bright.Render(clean(p.Name)), "  " + muted.Render(clean(p.Path))}
	if p.AppURL != "" {
		lines = append(lines, "  "+muted.Render(clean(p.AppURL)))
	}
	lines = append(lines, "")
	if m.removingFolder {
		return strings.Join(append(lines, "remove this folder from the list?", muted.Render("files and open workspaces are kept."), "", "enter: remove · esc: cancel", "", clean(m.status)), "\n")
	}
	actions := m.projectActions()
	rows := max(1, m.height-len(lines)-3)
	start := max(0, m.action-rows+1)
	for i := start; i < len(actions) && i < start+rows; i++ {
		action := actions[i]
		prefix := "  "
		if i == m.action {
			prefix = "> "
		}
		lines = append(lines, prefix+action.label)
	}
	status := m.status
	if status == "" {
		status = m.apps[p.ID].Error
	}
	return strings.Join(append(lines, "", clean(status)), "\n")
}

func (m Model) helpView() string {
	return "\n" + bright.Render("keyboard shortcuts") + "\n\n" + strings.Join([]string{
		"↑↓ / j k     select project / action",
		"enter        open project / run action",
		"esc          back to projects",
		"s / x / o    project server actions",
		"n / t        new codex / project terminal",
		"c / a        cloud task / server autostart",
		"u            codex usage and resets",
		"1…9          select slot",
		"alt+1…9      select slot from terminal",
		"alt+←→       previous / next slot",
		"rail: ~ cloud work · ✓ done · ! failed",
		"tab / ⇧tab   next / previous slot",
		"ctrl+w       close workspace",
		"q / ctrl+c   exit interface",
		"? / esc      close help",
	}, "\n")
}

func Run(service workspace.Service, sidebar bool) error {
	window := ""
	if sidebar {
		out, err := exec.Command("tmux", "display-message", "-p", "-t", os.Getenv("TMUX_PANE"), "#{window_id}").Output()
		if err != nil {
			return err
		}
		window = strings.TrimSpace(string(out))
	}
	_, err := tea.NewProgram(New(service, sidebar, window)).Run()
	return err
}
