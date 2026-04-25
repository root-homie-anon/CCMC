package tui

// inspector.go — right-panel inspector (task 34).
// Subscribes to SessionSelectedMsg; runs the aggregator in a tea.Cmd so the UI
// never blocks. Renders all 8 spec sections via a bubbles viewport.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"ccmc/internal/daemon"
	"ccmc/internal/inspector"
	"ccmc/pkg/ccmc"
)

// inspectorLoadedMsg carries the result of a completed aggregation back into Update.
type inspectorLoadedMsg struct {
	session ccmc.Session
	view    *inspector.SessionView
	memory  string
	todos   []inspector.Todo
	err     error
}

// inspectorModel implements InspectorPanel.
type inspectorModel struct {
	focused  bool
	session  *ccmc.Session        // currently selected session, nil if none
	view     *inspector.SessionView
	memory   string
	todos    []inspector.Todo
	loadErr  error
	loading  bool
	viewport viewport.Model
	width    int
	height   int
	ready    bool // viewport initialised with dimensions
}

// NewInspectorPanel constructs the inspector panel.
// The client parameter is reserved for future GetSession calls (task 40+); the
// panel currently resolves JSONL via daemon.FindSessionJSONL directly.
func NewInspectorPanel(_ daemonClient) InspectorPanel {
	return &inspectorModel{}
}

func (m *inspectorModel) Focus() { m.focused = true }
func (m *inspectorModel) Blur()  { m.focused = false }

func (m *inspectorModel) Update(msg tea.Msg) (InspectorPanel, tea.Cmd) {
	switch msg := msg.(type) {

	case paneSizeMsg:
		m.width = msg.w
		m.height = msg.h
		if !m.ready {
			m.viewport = viewport.New(msg.w, msg.h)
			m.ready = true
		} else {
			m.viewport.Width = msg.w
			m.viewport.Height = msg.h
		}
		m.viewport.SetContent(m.buildContent())
		return m, nil

	case SessionSelectedMsg:
		m.session = &msg.Session
		m.loading = true
		m.loadErr = nil
		return m, m.loadSession(msg.Session)

	case inspectorLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.loadErr = msg.err
			m.view = nil
		} else {
			m.loadErr = nil
			m.view = msg.view
			m.memory = msg.memory
			m.todos = msg.todos
		}
		m.viewport.SetContent(m.buildContent())
		m.viewport.GotoTop()
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		switch msg.String() {
		case "r":
			if m.session != nil {
				m.loading = true
				m.loadErr = nil
				return m, m.loadSession(*m.session)
			}
		}
		// Forward scrolling keys to viewport.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	// Let viewport handle its own messages (e.g. mouse wheel).
	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// loadSession returns a Cmd that runs the aggregator off the UI goroutine.
func (m *inspectorModel) loadSession(s ccmc.Session) tea.Cmd {
	return func() tea.Msg {
		jsonlPath, _, err := daemon.FindSessionJSONL(s.ID)
		if err != nil {
			return inspectorLoadedMsg{session: s, err: fmt.Errorf("find JSONL: %w", err)}
		}
		if jsonlPath == "" {
			return inspectorLoadedMsg{session: s, err: fmt.Errorf("JSONL not found for session %s", s.ID)}
		}

		f, err := os.Open(jsonlPath)
		if err != nil {
			return inspectorLoadedMsg{session: s, err: fmt.Errorf("open JSONL: %w", err)}
		}
		defer f.Close()

		sv, err := inspector.AggregateSession(f)
		if err != nil {
			return inspectorLoadedMsg{session: s, err: fmt.Errorf("aggregate: %w", err)}
		}

		mem, err := inspector.ReadMemorySummaryForSession(s.ID)
		if err != nil {
			mem = "" // non-fatal; render "(none)"
		}

		todos, err := inspector.ReadTodos(s.ID)
		if err != nil {
			todos = nil // non-fatal
		}

		return inspectorLoadedMsg{session: s, view: sv, memory: mem, todos: todos}
	}
}

func (m *inspectorModel) View() string {
	if !m.ready {
		return Muted.Render("(select a session)")
	}
	return m.viewport.View()
}

// buildContent assembles all 8 spec sections into the viewport content string.
func (m *inspectorModel) buildContent() string {
	if m.session == nil {
		return Muted.Render("(select a session)")
	}

	var sb strings.Builder

	// ── 1. Header ────────────────────────────────────────────────────────────
	projPath := m.session.ProjectPath
	if projPath == "" {
		projPath = m.session.ProjectName
	}
	sb.WriteString(Title.Render(projPath))
	sb.WriteString("\n")

	var dur string
	if m.view != nil && !m.view.StartedAt.IsZero() {
		d := m.view.EndedAt.Sub(m.view.StartedAt)
		if m.view.EndedAt.IsZero() {
			d = time.Since(m.view.StartedAt)
		}
		dur = formatDuration(d)
	} else {
		dur = "—"
	}
	sb.WriteString(Subtitle.Render(fmt.Sprintf("Duration: %s  ", dur)))
	sb.WriteString(renderBadge(m.session.Status))
	sb.WriteString("\n\n")

	if m.loading {
		sb.WriteString(Muted.Render("loading…"))
		return sb.String()
	}

	if m.loadErr != nil {
		sb.WriteString(Muted.Render(fmt.Sprintf("error: %s", m.loadErr.Error())))
		return sb.String()
	}

	if m.view == nil {
		sb.WriteString(Muted.Render("(no data)"))
		return sb.String()
	}

	// ── 2. Loaded agents ─────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Loaded Agents"))
	sb.WriteString("\n")
	if len(m.view.Agents) == 0 {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		for _, a := range m.view.Agents {
			sb.WriteString("  • " + a)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ── 3. Active subagents ───────────────────────────────────────────────────
	sb.WriteString(Title.Render("Active Subagents"))
	sb.WriteString("\n")
	combined := combinedSubagents(m.session, m.view)
	if len(combined) == 0 {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		for _, sa := range combined {
			sb.WriteString("  • " + sa)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ── 4. Recent tool calls (last 10, most-recent first) ─────────────────────
	sb.WriteString(Title.Render("Recent Tool Calls"))
	sb.WriteString("\n")
	calls := m.view.RecentToolCalls
	// Take last 10 and reverse so most-recent is first.
	if len(calls) > 10 {
		calls = calls[len(calls)-10:]
	}
	if len(calls) == 0 {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		for i := len(calls) - 1; i >= 0; i-- {
			tc := calls[i]
			line := tc.Tool
			if tc.Target != "" {
				line += "  " + Muted.Render(tc.Target)
			}
			sb.WriteString("  " + line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ── 5. Files touched ─────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Files Touched"))
	sb.WriteString("\n")
	allFiles := dedupe(append(m.view.FilesRead, m.view.FilesModified...))
	if len(allFiles) == 0 {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		for _, f := range allFiles {
			sb.WriteString("  " + f)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ── 6. Todos ─────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Todos"))
	sb.WriteString("\n")
	if len(m.todos) == 0 {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		for _, td := range m.todos {
			marker := todoMarker(td.Status)
			sb.WriteString(fmt.Sprintf("  %s %s", marker, td.Title))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ── 7. Memory summary ────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Memory Summary"))
	sb.WriteString("\n")
	if m.memory == "" {
		sb.WriteString(Muted.Render("(none)"))
	} else {
		sb.WriteString(m.memory)
	}
	sb.WriteString("\n\n")

	// ── 8. Context estimate ──────────────────────────────────────────────────
	sb.WriteString(Title.Render("Context Estimate"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  %s bytes  (%d events)",
		formatBytes(m.view.ContextEstimate),
		m.view.EventCount,
	))
	sb.WriteString("\n")

	return sb.String()
}

// combinedSubagents merges ActiveSubagents from the aggregated view with the
// registry-supplied list from the Session struct, deduplicating by name.
func combinedSubagents(s *ccmc.Session, v *inspector.SessionView) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range s.ActiveSubagents {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, sa := range v.ActiveSubagents {
		if !seen[sa.Name] {
			seen[sa.Name] = true
			out = append(out, sa.Name)
		}
	}
	return out
}

func todoMarker(status string) string {
	switch status {
	case "completed":
		return "[x]"
	case "in_progress":
		return "[~]"
	default:
		return "[ ]"
	}
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
