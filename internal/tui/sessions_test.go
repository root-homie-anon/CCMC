package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// makeSessions returns n synthetic sessions with distinct project paths.
func makeSessions(n int) []ccmc.Session {
	sessions := make([]ccmc.Session, n)
	for i := range sessions {
		sessions[i] = ccmc.Session{
			ID:           strings.Repeat("a", 8),
			ProjectPath:  strings.Repeat("/project/path-", 1) + string(rune('A'+i)),
			ProjectName:  string(rune('A' + i)),
			Status:       ccmc.SessionActive,
			LastActivity: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}
	return sessions
}

// feedSessions sends a sessionsRefreshedMsg to a fresh SessionsPanel and returns it.
func feedSessions(p SessionsPanel, sessions []ccmc.Session) SessionsPanel {
	updated, _ := p.Update(sessionsRefreshedMsg{sessions: sessions})
	return updated
}

// pressKey sends a single rune key to the panel (must be focused).
func pressSessionKey(p SessionsPanel, key string) (SessionsPanel, tea.Cmd) {
	var msg tea.KeyMsg
	switch key {
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	return p.Update(msg)
}

// TestSessions_RendersList verifies that all three session project names/paths
// appear in View() after a sessionsRefreshedMsg.
func TestSessions_RendersList(t *testing.T) {
	p := NewSessionsPanel()
	sessions := makeSessions(3)
	p = feedSessions(p, sessions)

	view := p.View()
	for _, s := range sessions {
		if !strings.Contains(view, s.ProjectName) {
			t.Errorf("View() missing project name %q\nView:\n%s", s.ProjectName, view)
		}
	}
}

// TestSessions_NavigatesUpDown feeds two ↓ keys and asserts the third session
// (index 2) is selected.
func TestSessions_NavigatesUpDown(t *testing.T) {
	p := NewSessionsPanel()
	p.Focus()
	sessions := makeSessions(3)
	p = feedSessions(p, sessions)

	p, _ = pressSessionKey(p, "down")
	p, _ = pressSessionKey(p, "down")

	got := p.Selected()
	if got == nil {
		t.Fatal("Selected() is nil after two down presses")
	}
	// makeSessions sorts by LastActivity desc; index 0 is newest (session A),
	// but sessionsModel sorts them the same way. After two downs, cursor==2.
	if got.ProjectName != sessions[2].ProjectName {
		t.Errorf("Selected().ProjectName = %q, want %q", got.ProjectName, sessions[2].ProjectName)
	}
}

// TestSessions_NavigationClampsAtBoundaries verifies the cursor stays at the
// last item when pressing down past the end, and stays at the first item when
// pressing up past the beginning.
func TestSessions_NavigationClampsAtBoundaries(t *testing.T) {
	p := NewSessionsPanel()
	p.Focus()
	sessions := makeSessions(3)
	p = feedSessions(p, sessions)

	// Press down many times — should clamp at last item (index 2).
	for i := 0; i < 20; i++ {
		p, _ = pressSessionKey(p, "down")
	}
	got := p.Selected()
	if got == nil {
		t.Fatal("Selected() is nil at bottom boundary")
	}
	wantBottom := sessions[2].ProjectName
	if got.ProjectName != wantBottom {
		t.Errorf("bottom clamp: got %q, want %q", got.ProjectName, wantBottom)
	}

	// Press up many times — should clamp at first item (index 0).
	for i := 0; i < 20; i++ {
		p, _ = pressSessionKey(p, "up")
	}
	got = p.Selected()
	if got == nil {
		t.Fatal("Selected() is nil at top boundary")
	}
	wantTop := sessions[0].ProjectName
	if got.ProjectName != wantTop {
		t.Errorf("top clamp: got %q, want %q", got.ProjectName, wantTop)
	}
}

// TestSessions_EnterEmitsSelectedMsg verifies that pressing Enter returns a Cmd
// that, when executed, produces a SessionSelectedMsg.
func TestSessions_EnterEmitsSelectedMsg(t *testing.T) {
	p := NewSessionsPanel()
	p.Focus()
	sessions := makeSessions(3)
	p = feedSessions(p, sessions)

	_, cmd := pressSessionKey(p, "enter")
	if cmd == nil {
		t.Fatal("Enter returned nil Cmd, expected SessionSelectedMsg cmd")
	}
	msg := cmd()
	sel, ok := msg.(SessionSelectedMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want SessionSelectedMsg", msg)
	}
	if sel.Session.ProjectName != sessions[0].ProjectName {
		t.Errorf("SessionSelectedMsg.Session.ProjectName = %q, want %q",
			sel.Session.ProjectName, sessions[0].ProjectName)
	}
}

// TestSessions_EmptyState verifies that an empty session list renders the
// expected empty-state string.
func TestSessions_EmptyState(t *testing.T) {
	p := NewSessionsPanel()
	p = feedSessions(p, nil)

	view := p.View()
	if !strings.Contains(view, "no sessions") {
		t.Errorf("empty state: View() missing 'no sessions'\nView:\n%s", view)
	}
}

// TestSessions_TruncatesLongPaths feeds a session with a 200-char project path
// and sets a narrow panel width, then asserts the truncation marker appears.
func TestSessions_TruncatesLongPaths(t *testing.T) {
	p := NewSessionsPanel()
	longPath := "/" + strings.Repeat("a", 199)
	sessions := []ccmc.Session{
		{
			ID:           "t1",
			ProjectPath:  longPath,
			ProjectName:  "longproj",
			Status:       ccmc.SessionActive,
			LastActivity: time.Now(),
		},
	}
	p = feedSessions(p, sessions)
	// Set a narrow width so the renderRow maxW truncation fires.
	p.Update(paneSizeMsg{w: 40, h: 20})

	view := p.View()
	if !strings.Contains(view, "…") {
		t.Errorf("View() missing truncation marker '…' for 200-char path\nView:\n%s", view)
	}
}
