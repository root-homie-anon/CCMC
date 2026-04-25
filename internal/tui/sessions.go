package tui

// sessions.go — left-panel session list (task 33).
// Hand-rolled rather than bubbles/list: the built-in list keymap (/, filter, etc.)
// conflicts with the global '?' overlay key and adds unnecessary complexity.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// SessionSelectedMsg is emitted whenever the cursor moves or Enter is pressed,
// so the inspector panel can track the cursor live.
type SessionSelectedMsg struct {
	Session ccmc.Session
}

// sessionsModel implements SessionsPanel.
type sessionsModel struct {
	sessions []ccmc.Session // sorted by LastActivity desc
	cursor   int
	focused  bool
	width    int
	height   int
}

// NewSessionsPanel constructs the session list panel.
func NewSessionsPanel() SessionsPanel {
	return &sessionsModel{}
}

func (m *sessionsModel) Focus() { m.focused = true }
func (m *sessionsModel) Blur()  { m.focused = false }

func (m *sessionsModel) Selected() *ccmc.Session {
	if len(m.sessions) == 0 || m.cursor >= len(m.sessions) {
		return nil
	}
	s := m.sessions[m.cursor]
	return &s
}

func (m *sessionsModel) Update(msg tea.Msg) (SessionsPanel, tea.Cmd) {
	switch msg := msg.(type) {

	case sessionsRefreshedMsg:
		if msg.err != nil {
			return m, nil
		}
		// Sort descending by LastActivity.
		sorted := make([]ccmc.Session, len(msg.sessions))
		copy(sorted, msg.sessions)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].LastActivity.After(sorted[j].LastActivity)
		})
		m.sessions = sorted
		// Clamp cursor to valid range after list change.
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}
		return m, nil

	case paneSizeMsg:
		m.width = msg.w
		m.height = msg.h
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *sessionsModel) handleKey(msg tea.KeyMsg) (SessionsPanel, tea.Cmd) {
	n := len(m.sessions)
	if n == 0 {
		return m, nil
	}

	prev := m.cursor

	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = n - 1
	case "enter":
		// Explicit selection — emit even if cursor didn't move.
		return m, m.emitSelected()
	}

	// Emit on any cursor movement.
	if m.cursor != prev {
		return m, m.emitSelected()
	}
	return m, nil
}

func (m *sessionsModel) emitSelected() tea.Cmd {
	if len(m.sessions) == 0 {
		return nil
	}
	s := m.sessions[m.cursor]
	return func() tea.Msg { return SessionSelectedMsg{Session: s} }
}

func (m *sessionsModel) View() string {
	if len(m.sessions) == 0 {
		return Muted.Render("no sessions")
	}

	var sb strings.Builder
	for i, s := range m.sessions {
		line := m.renderRow(i, s)
		sb.WriteString(line)
		if i < len(m.sessions)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// renderRow formats a single session row.
// Layout: [badge] <project-name>  <truncated-path>
func (m *sessionsModel) renderRow(idx int, s ccmc.Session) string {
	badge := renderBadge(s.Status)

	// Project name + truncated directory path.
	name := s.ProjectName
	if name == "" {
		name = "(unnamed)"
	}

	path := truncateLeft(s.ProjectPath, 50)
	age := formatAge(s.LastActivity)

	content := fmt.Sprintf("%s  %-20s  %-52s  %s", badge, name, path, age)

	// Available width after badge+padding (rough; we don't have exact lipgloss widths in View).
	maxW := m.width - 2
	if maxW > 0 && len(content) > maxW {
		content = content[:maxW-1] + "…"
	}

	if idx == m.cursor && m.focused {
		return Selected.Render(content)
	}
	return content
}

// renderBadge returns the styled badge text for a session status.
func renderBadge(status ccmc.SessionStatus) string {
	switch status {
	case ccmc.SessionActive:
		return BadgeActive.Render("active")
	case ccmc.SessionIdle:
		return BadgeIdle.Render("idle ")
	case ccmc.SessionDead:
		return BadgeDead.Render("dead ")
	default:
		return BadgeUnknown.Render("?    ")
	}
}

// truncateLeft returns the last n chars of s, prefixed with "…" if truncated.
func truncateLeft(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-(n-1):]
}

// formatAge formats a duration since lastActivity as a compact relative time.
func formatAge(t time.Time) string {
	if t.IsZero() {
		return Muted.Render("—")
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return Muted.Render("just now")
	case d < time.Hour:
		return Muted.Render(fmt.Sprintf("%dm ago", int(d.Minutes())))
	case d < 24*time.Hour:
		return Muted.Render(fmt.Sprintf("%dh ago", int(d.Hours())))
	default:
		return Muted.Render(t.Format("Jan 2"))
	}
}
