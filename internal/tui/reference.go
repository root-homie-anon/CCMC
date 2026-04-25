package tui

// reference.go — reference search overlay (task 35).
// Modal overlay composed over the two-column layout. Takes a *reference.Engine
// from the App constructor; the App owns the engine instance.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"ccmc/internal/reference"
	"ccmc/pkg/ccmc"
)

// closeOverlayMsg is emitted when the overlay closes itself (e.g. Esc in list view),
// letting App.Update transition focus back to sessions.
type closeOverlayMsg struct{}

// referenceModel implements ReferencePanel.
type referenceModel struct {
	engine     *reference.Engine
	input      textinput.Model
	results    []ccmc.RefEntry
	selected   int
	detailView bool // true when showing full detail for the selected entry
	focused    bool
	width      int
	height     int
}

// NewReferencePanel constructs the reference overlay with the given search engine.
func NewReferencePanel(engine *reference.Engine) ReferencePanel {
	ti := textinput.New()
	ti.Placeholder = "search…"
	ti.CharLimit = 128

	return &referenceModel{
		engine: engine,
		input:  ti,
	}
}

func (m *referenceModel) Focus() {
	m.focused = true
	m.input.Focus()
}

func (m *referenceModel) Blur() {
	m.focused = false
	m.input.Blur()
}

func (m *referenceModel) Update(msg tea.Msg) (ReferencePanel, tea.Cmd) {
	switch msg := msg.(type) {

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

	// Forward all other messages to the text input so typing works.
	if m.focused {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.runSearch()
		return m, cmd
	}
	return m, nil
}

func (m *referenceModel) handleKey(msg tea.KeyMsg) (ReferencePanel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.detailView {
			// Esc in detail → back to list.
			m.detailView = false
			return m, nil
		}
		// Esc in list → close overlay.
		m.detailView = false
		return m, func() tea.Msg { return closeOverlayMsg{} }

	case "enter":
		if !m.detailView && len(m.results) > 0 {
			m.detailView = true
		}
		return m, nil

	case "up", "k":
		if !m.detailView && m.selected > 0 {
			m.selected--
		}
		return m, nil

	case "down", "j":
		if !m.detailView && m.selected < len(m.results)-1 {
			m.selected++
		}
		return m, nil
	}

	// All other keys go to the text input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.runSearch()
	return m, cmd
}

// runSearch re-runs the fuzzy search after any input change and resets the cursor.
func (m *referenceModel) runSearch() {
	if m.engine == nil {
		return
	}
	query := m.input.Value()
	m.results = m.engine.Search(query, nil, 10)
	// Clamp cursor.
	if m.selected >= len(m.results) {
		m.selected = max(0, len(m.results)-1)
	}
}

func (m *referenceModel) View() string {
	var sb strings.Builder

	sb.WriteString(Title.Render("Reference"))
	sb.WriteString("\n")
	sb.WriteString(m.input.View())
	sb.WriteString("\n\n")

	if m.detailView {
		m.renderDetail(&sb)
	} else {
		m.renderList(&sb)
	}

	return sb.String()
}

func (m *referenceModel) renderList(sb *strings.Builder) {
	if len(m.results) == 0 {
		sb.WriteString(Muted.Render("no results"))
		return
	}

	for i, e := range m.results {
		desc := e.Description
		// Truncate description to keep rows on one line.
		maxDesc := m.width - 40
		if maxDesc < 10 {
			maxDesc = 10
		}
		if len(desc) > maxDesc {
			desc = desc[:maxDesc-1] + "…"
		}
		line := fmt.Sprintf("%-12s  %-24s  %s", string(e.Category), e.Name, Muted.Render(desc))
		if i == m.selected {
			sb.WriteString(Selected.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}
}

func (m *referenceModel) renderDetail(sb *strings.Builder) {
	if len(m.results) == 0 || m.selected >= len(m.results) {
		sb.WriteString(Muted.Render("(no entry selected)"))
		return
	}
	e := m.results[m.selected]

	sb.WriteString(Title.Render(fmt.Sprintf("%s — %s", e.Category, e.Name)))
	sb.WriteString("\n")
	sb.WriteString(e.Description)
	sb.WriteString("\n\n")

	if e.Usage != "" {
		sb.WriteString(Subtitle.Render("Usage"))
		sb.WriteString("\n  ")
		sb.WriteString(e.Usage)
		sb.WriteString("\n\n")
	}

	if e.Detail != "" {
		sb.WriteString(Subtitle.Render("Detail"))
		sb.WriteString("\n")
		sb.WriteString(e.Detail)
		sb.WriteString("\n\n")
	}

	if len(e.Examples) > 0 {
		sb.WriteString(Subtitle.Render("Examples"))
		sb.WriteString("\n")
		for _, ex := range e.Examples {
			sb.WriteString("  " + ex + "\n")
		}
		sb.WriteString("\n")
	}

	if len(e.Gotchas) > 0 {
		sb.WriteString(Subtitle.Render("Gotchas"))
		sb.WriteString("\n")
		for _, g := range e.Gotchas {
			sb.WriteString("  • " + g + "\n")
		}
		sb.WriteString("\n")
	}

	if len(e.Related) > 0 {
		sb.WriteString(Subtitle.Render("Related"))
		sb.WriteString("\n  ")
		sb.WriteString(strings.Join(e.Related, ", "))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(Muted.Render("[Esc] back to list"))
}

// max returns the larger of two ints.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
