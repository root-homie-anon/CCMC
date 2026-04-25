package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/internal/inspector"
	"ccmc/pkg/ccmc"
)

// makeInspector constructs an InspectorPanel with a known terminal size so the
// internal viewport is initialised and View() renders real content.
func makeInspector() InspectorPanel {
	p := NewInspectorPanel(nil)
	p.Update(paneSizeMsg{w: 100, h: 40})
	return p
}

// swapLoadFunc replaces inspectorLoadFunc for the duration of a test and
// restores the original on cleanup.
func swapLoadFunc(t *testing.T, fn func(ccmc.Session) inspectorLoadedMsg) {
	t.Helper()
	orig := inspectorLoadFunc
	inspectorLoadFunc = fn
	t.Cleanup(func() { inspectorLoadFunc = orig })
}

// runLoad feeds a SessionSelectedMsg then executes the returned Cmd synchronously
// so the loaded content is flushed into the model before we call View().
func runLoad(p InspectorPanel, s ccmc.Session) (InspectorPanel, string) {
	p, cmd := p.Update(SessionSelectedMsg{Session: s})
	if cmd != nil {
		msg := cmd()
		p, _ = p.Update(msg)
	}
	return p, p.View()
}

// testSession returns a minimal ccmc.Session for inspector tests.
func testSession() ccmc.Session {
	return ccmc.Session{
		ID:           "testsession001",
		ProjectPath:  "/home/user/myproject",
		ProjectName:  "myproject",
		Status:       ccmc.SessionActive,
		LastActivity: time.Now(),
	}
}

// goodView returns a canned inspector.SessionView with data in every section.
func goodView() *inspector.SessionView {
	return &inspector.SessionView{
		Agents:          []string{"architect"},
		ActiveSubagents: []inspector.SubagentInfo{{Name: "worker-1", Description: "builds"}},
		RecentToolCalls: []inspector.ToolCall{
			{Tool: "Read", Target: "/home/user/myproject/main.go"},
		},
		FilesRead:       []string{"/home/user/myproject/main.go"},
		FilesModified:   []string{"/home/user/myproject/out.go"},
		ContextEstimate: 4096,
		EventCount:      42,
	}
}

// TestInspector_LoadsOnSelection verifies that after SessionSelectedMsg + Cmd
// execution, View() contains the project path from the selected session.
func TestInspector_LoadsOnSelection(t *testing.T) {
	s := testSession()
	swapLoadFunc(t, func(sess ccmc.Session) inspectorLoadedMsg {
		return inspectorLoadedMsg{session: sess, view: goodView()}
	})

	p := makeInspector()
	_, view := runLoad(p, s)

	if !strings.Contains(view, s.ProjectPath) {
		t.Errorf("View() missing project path %q\nView:\n%s", s.ProjectPath, view)
	}
}

// TestInspector_RendersAllSections asserts all 8 spec section headers appear in
// View() when the aggregator returns a non-empty SessionView.
func TestInspector_RendersAllSections(t *testing.T) {
	s := testSession()
	swapLoadFunc(t, func(sess ccmc.Session) inspectorLoadedMsg {
		return inspectorLoadedMsg{session: sess, view: goodView()}
	})

	p := makeInspector()
	_, view := runLoad(p, s)

	// Section 1 (header) — verified by project path presence above.
	// Sections 2-8 are titled in buildContent().
	sections := []string{
		"Loaded Agents",
		"Active Subagents",
		"Recent Tool Calls",
		"Files Touched",
		"Todos",
		"Memory Summary",
		"Context Estimate",
	}
	for _, sec := range sections {
		if !strings.Contains(view, sec) {
			t.Errorf("View() missing section header %q\nView:\n%s", sec, view)
		}
	}
	// Header section — project path renders as the Title.
	if !strings.Contains(view, s.ProjectPath) {
		t.Errorf("View() missing project path (header section)\nView:\n%s", view)
	}
}

// TestInspector_AggregatorErrorRendersInline stubs inspectorLoadFunc to return
// an error and asserts the error text appears inline in View().
func TestInspector_AggregatorErrorRendersInline(t *testing.T) {
	s := testSession()
	swapLoadFunc(t, func(sess ccmc.Session) inspectorLoadedMsg {
		return inspectorLoadedMsg{session: sess, err: errors.New("disk full")}
	})

	p := makeInspector()
	_, view := runLoad(p, s)

	if !strings.Contains(view, "disk full") {
		t.Errorf("View() missing error text 'disk full'\nView:\n%s", view)
	}
}

// TestInspector_RefreshKeyReruns verifies that pressing 'r' after an initial
// load triggers a second call to the aggregator.
func TestInspector_RefreshKeyReruns(t *testing.T) {
	s := testSession()
	calls := 0
	swapLoadFunc(t, func(sess ccmc.Session) inspectorLoadedMsg {
		calls++
		return inspectorLoadedMsg{session: sess, view: goodView()}
	})

	p := makeInspector()
	p.Focus()

	// Initial load.
	p, cmd := p.Update(SessionSelectedMsg{Session: s})
	if cmd != nil {
		msg := cmd()
		p, _ = p.Update(msg)
	}
	if calls != 1 {
		t.Fatalf("expected 1 aggregator call after initial load, got %d", calls)
	}

	// Press 'r' — inspector must be focused for the key to be handled.
	p, cmd = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		msg := cmd()
		p, _ = p.Update(msg)
	}
	_ = p
	if calls != 2 {
		t.Fatalf("expected 2 aggregator calls after 'r' refresh, got %d", calls)
	}
}

// TestInspector_EmptySectionsShowNone loads a session with a zero-value
// SessionView and asserts each section header is present and "(none)" markers
// appear for sections with no data.
func TestInspector_EmptySectionsShowNone(t *testing.T) {
	s := testSession()
	swapLoadFunc(t, func(sess ccmc.Session) inspectorLoadedMsg {
		return inspectorLoadedMsg{session: sess, view: &inspector.SessionView{}}
	})

	p := makeInspector()
	_, view := runLoad(p, s)

	if !strings.Contains(view, "(none)") {
		t.Errorf("View() with empty SessionView missing any '(none)' markers\nView:\n%s", view)
	}

	headers := []string{
		"Loaded Agents",
		"Active Subagents",
		"Recent Tool Calls",
		"Files Touched",
		"Todos",
		"Memory Summary",
	}
	for _, h := range headers {
		if !strings.Contains(view, h) {
			t.Errorf("View() missing section header %q in empty state\nView:\n%s", h, view)
		}
	}
}
