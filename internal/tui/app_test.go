package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// ── test double ───────────────────────────────────────────────────────────────

// mockClient is a daemonClient stub. ListSessionsFn and StatusFn are called on
// each invocation; if nil, the respective method returns a zero value and nil error.
type mockClient struct {
	ListSessionsFn func() ([]ccmc.Session, error)
	StatusFn       func() (ccmc.DaemonStatus, error)
}

func (m *mockClient) ListSessions() ([]ccmc.Session, error) {
	if m.ListSessionsFn != nil {
		return m.ListSessionsFn()
	}
	return nil, nil
}

func (m *mockClient) Status() (ccmc.DaemonStatus, error) {
	if m.StatusFn != nil {
		return m.StatusFn()
	}
	return ccmc.DaemonStatus{}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestApp constructs an App with sensible terminal dimensions for tests.
func newTestApp() App {
	a := NewApp(&mockClient{})
	a.width = 200
	a.height = 50
	return a
}

// sendKey returns the updated model and cmd after feeding a single key.
func sendKey(a App, key string) (App, tea.Cmd) {
	updated, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated.(App), cmd
}

// sendMsg returns the updated model after feeding an arbitrary message.
func sendMsg(a App, msg tea.Msg) App {
	updated, _ := a.Update(msg)
	return updated.(App)
}

// isQuitCmd reports whether cmd is tea.Quit. Bubble Tea does not export a
// direct equality check; we compare the result of calling it against nil.
// tea.Quit returns a tea.QuitMsg when invoked.
func isQuitCmd(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	_, ok := msg.(tea.QuitMsg)
	return ok
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestApp_QuitOnQ(t *testing.T) {
	a := newTestApp()
	_, cmd := sendKey(a, "q")
	if !isQuitCmd(t, cmd) {
		t.Fatal("expected tea.Quit cmd after pressing 'q', got something else")
	}
}

func TestApp_QuitOnCtrlC(t *testing.T) {
	a := newTestApp()
	updated, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = updated
	if !isQuitCmd(t, cmd) {
		t.Fatal("expected tea.Quit cmd after Ctrl+C")
	}
}

func TestApp_TabCyclesFocus(t *testing.T) {
	a := newTestApp()
	if a.focused != panelSessions {
		t.Fatalf("initial focus: want panelSessions (%d), got %d", panelSessions, a.focused)
	}

	// Tab from sessions → inspector (default, inventoryMode=false).
	updated1, _ := a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = updated1.(App)
	if got := a.focused; got != panelInspector {
		t.Fatalf("after Tab: want panelInspector (%d), got %d", panelInspector, got)
	}

	// Tab again → back to sessions.
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = updated.(App)
	if a.focused != panelSessions {
		t.Fatalf("after second Tab: want panelSessions (%d), got %d", panelSessions, a.focused)
	}
}

func TestApp_OpenReferenceOverlay(t *testing.T) {
	a := newTestApp()
	a, _ = sendKey(a, "?")
	if a.focused != panelReference {
		t.Fatalf("after '?': want panelReference (%d), got %d", panelReference, a.focused)
	}
}

func TestApp_OpenReferenceOverlaySlash(t *testing.T) {
	a := newTestApp()
	a, _ = sendKey(a, "/")
	if a.focused != panelReference {
		t.Fatalf("after '/': want panelReference (%d), got %d", panelReference, a.focused)
	}
}

func TestApp_CloseReferenceOverlay(t *testing.T) {
	a := newTestApp()
	// Open overlay.
	a, _ = sendKey(a, "?")
	if a.focused != panelReference {
		t.Fatal("precondition: overlay must be open")
	}
	// Close with Esc.
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = updated.(App)
	if a.focused != panelSessions {
		t.Fatalf("after Esc: want panelSessions (%d), got %d", panelSessions, a.focused)
	}
}

func TestApp_TickRefreshesSessions(t *testing.T) {
	sessions := []ccmc.Session{
		{ID: "abc123", ProjectName: "my-project", Status: ccmc.SessionActive},
	}
	a := newTestApp()
	a = sendMsg(a, sessionsRefreshedMsg{sessions: sessions})
	if len(a.sessions) != 1 || a.sessions[0].ID != "abc123" {
		t.Fatalf("sessions not updated: got %+v", a.sessions)
	}
}

func TestApp_DaemonUnavailable_RendersWarning(t *testing.T) {
	// Seed last-known sessions.
	sessions := []ccmc.Session{
		{ID: "xyz", ProjectName: "old-project", Status: ccmc.SessionIdle},
	}
	a := newTestApp()
	a = sendMsg(a, sessionsRefreshedMsg{sessions: sessions})

	// Now simulate daemon going away.
	a = sendMsg(a, sessionsRefreshedMsg{err: ccmc.ErrDaemonUnavailable})

	if !errors.Is(a.daemonErr, ccmc.ErrDaemonUnavailable) {
		t.Fatal("expected daemonErr to be ErrDaemonUnavailable")
	}
	// Last-known sessions should still be present.
	if len(a.sessions) != 1 || a.sessions[0].ID != "xyz" {
		t.Fatalf("last-known sessions lost; got %+v", a.sessions)
	}

	// View must include the warning string.
	view := a.View()
	if !strings.Contains(view, "daemon unavailable") {
		t.Fatalf("View() missing warning; got:\n%s", view)
	}
}

func TestApp_View_HasNoColorsInTest(t *testing.T) {
	// Verify View() produces a non-empty string containing the stub panel
	// placeholder text. We do not snapshot exact ANSI codes — that would be
	// too brittle as lipgloss behaviour varies by terminal profile.
	a := newTestApp()
	// Seed sessions so the view has content.
	a = sendMsg(a, sessionsRefreshedMsg{
		sessions: []ccmc.Session{{ID: "s1", ProjectName: "proj", Status: ccmc.SessionActive}},
	})

	view := a.View()
	if view == "" {
		t.Fatal("View() returned empty string")
	}

	// Stub panel placeholders must be present.
	checks := []string{
		"[sessions panel — task 33]",
		"[inspector panel — task 34]",
		"[command bar — task 36]",
	}
	for _, want := range checks {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q", want)
		}
	}
}

func TestApp_InventoryModeToggle(t *testing.T) {
	a := newTestApp()
	if a.inventoryMode {
		t.Fatal("inventoryMode should be false initially")
	}
	a, _ = sendKey(a, "i")
	if !a.inventoryMode {
		t.Fatal("inventoryMode should be true after pressing 'i'")
	}
	a, _ = sendKey(a, "i")
	if a.inventoryMode {
		t.Fatal("inventoryMode should be false after pressing 'i' twice")
	}
}

func TestApp_WindowSizeUpdates(t *testing.T) {
	a := newTestApp()
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = updated.(App)
	if a.width != 120 || a.height != 40 {
		t.Fatalf("terminal dimensions not updated: %dx%d", a.width, a.height)
	}
}

func TestApp_FetchSessionsCmd(t *testing.T) {
	want := []ccmc.Session{{ID: "fetched", ProjectName: "fetched-proj", Status: ccmc.SessionActive}}
	client := &mockClient{
		ListSessionsFn: func() ([]ccmc.Session, error) { return want, nil },
		StatusFn:       func() (ccmc.DaemonStatus, error) { return ccmc.DaemonStatus{Running: true}, nil },
	}
	a := NewApp(client)
	a.width = 200
	a.height = 50

	// fetchSessions returns a Cmd; execute it synchronously in tests.
	cmd := a.fetchSessions()
	msg := cmd()

	refreshed, ok := msg.(sessionsRefreshedMsg)
	if !ok {
		t.Fatalf("expected sessionsRefreshedMsg, got %T", msg)
	}
	if refreshed.err != nil {
		t.Fatalf("unexpected error: %v", refreshed.err)
	}
	if len(refreshed.sessions) != 1 || refreshed.sessions[0].ID != "fetched" {
		t.Fatalf("wrong sessions: %+v", refreshed.sessions)
	}
}

func TestApp_TickMsg_FiresFetch(t *testing.T) {
	// A tickMsg should cause the Update to return a non-nil Cmd (the fetch Cmd).
	a := newTestApp()
	_, cmd := a.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("expected non-nil Cmd from tickMsg (the fetch Cmd)")
	}
}
