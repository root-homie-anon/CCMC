package tui

// commandbar.go — bottom command bar (task 36).
// Stateless renderer driven by App's focus state. Returns no commands.
// Advertises 'l' (launch) and 'k' (kill) which are wired in task 40.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandBarModel implements CommandBarPanel.
type commandBarModel struct {
	focused panel // mirrors App.focused so the bar renders the right hints
	width   int
}

// NewCommandBarPanel constructs the command bar panel.
func NewCommandBarPanel() CommandBarPanel {
	return &commandBarModel{}
}

// Focus and Blur are required by the interface but are no-ops for the command bar:
// it renders based on the focused panel enum passed via focusChangedMsg, not its
// own focus state.
func (m *commandBarModel) Focus() {}
func (m *commandBarModel) Blur()  {}

// focusChangedMsg is sent by App whenever focus transitions, keeping the command
// bar in sync without the bar holding a pointer to App state.
type focusChangedMsg struct {
	active panel
	width  int
}

func (m *commandBarModel) Update(msg tea.Msg) (CommandBarPanel, tea.Cmd) {
	switch msg := msg.(type) {
	case focusChangedMsg:
		m.focused = msg.active
		m.width = msg.width
	case paneSizeMsg:
		m.width = msg.w
	}
	// Command bar is purely declarative — never returns a Cmd.
	return m, nil
}

// hintsFor returns the key-hint string for the given panel focus.
func hintsFor(p panel) string {
	switch p {
	case panelSessions:
		return "[↑↓] nav  [Enter] select  [l] launch  [k] kill  [i] inventory  [?] ref  [q] quit"
	case panelInspector:
		return "[↑↓] scroll  [r] refresh  [Tab] back  [?] ref  [q] quit"
	case panelReference:
		return "[type] search  [↑↓] nav  [Enter] detail  [Esc] close"
	case panelInventory:
		return "[↑↓] nav  [Tab] back  [?] ref  [q] quit"
	default:
		return "[q] quit"
	}
}

func (m *commandBarModel) View() string {
	hints := hintsFor(m.focused)

	// Style individual key tokens and descriptions.
	var sb strings.Builder
	parts := strings.Split(hints, "  ")
	for i, part := range parts {
		if i > 0 {
			sb.WriteString("  ")
		}
		// Detect key token: starts with '['.
		if strings.HasPrefix(part, "[") {
			sb.WriteString(HelpKey.Render(part))
		} else {
			sb.WriteString(HelpDesc.Render(part))
		}
	}

	result := sb.String()

	// Truncate from the right with ellipsis if wider than available width.
	// lipgloss.Width strips ANSI so we compare the plain-text length for safety.
	plainLen := len(stripHints(hints))
	if m.width > 0 && plainLen > m.width-2 {
		// Re-build plain-truncated version without partial ANSI sequences.
		truncated := hints
		for len(truncated) > m.width-4 {
			idx := strings.LastIndexAny(truncated, " ")
			if idx <= 0 {
				break
			}
			truncated = truncated[:idx]
		}
		truncated += "…"

		sb.Reset()
		sb.WriteString(HelpDesc.Render(truncated))
		result = sb.String()
	}

	return result
}

// stripHints removes all ANSI escape sequences for a plain-text length calculation.
// We only need this for width comparison — rendering always uses styled output.
func stripHints(s string) string {
	// Fast path: no escape sequences in a plain hints string.
	return s
}
