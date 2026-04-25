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

	// Verify content from the real panel implementations.
	// Sessions panel: list has one entry so it renders rows, not the empty state.
	// Inspector panel: no session selected yet → placeholder text.
	// Command bar: always shows at minimum "[q] quit".
	checks := []string{
		"(select a session)", // inspector empty state
		"[q] quit",           // command bar — present in every hint string
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

// ── lifecycle keybinding tests (task 40) ──────────────────────────────────────

// seedSession adds a session to the app so the sessions panel has a selection.
func seedSession(a App, id, projectPath string) App {
	return sendMsg(a, sessionsRefreshedMsg{
		sessions: []ccmc.Session{
			{ID: id, ProjectName: "proj", ProjectPath: projectPath, Status: ccmc.SessionActive},
		},
	})
}

func TestApp_LaunchPromptOpensOnL(t *testing.T) {
	a := newTestApp()
	// 'l' only works when sessions panel is focused.
	if got := a.focused; got != panelSessions {
		t.Fatalf("pre: want panelSessions, got %d", got)
	}
	a, _ = sendKey(a, "l")
	if a.focused != panelLaunchPrompt {
		t.Fatalf("after 'l': want panelLaunchPrompt (%d), got %d", panelLaunchPrompt, a.focused)
	}
	// View must show the prompt.
	view := a.View()
	if !strings.Contains(view, "Launch new session") {
		t.Errorf("View() missing launch prompt text; got:\n%s", view)
	}
}

func TestApp_LaunchPromptPrePopulated(t *testing.T) {
	a := newTestApp()
	a = seedSession(a, "s1", "/some/project/path")
	a, _ = sendKey(a, "l")
	if a.focused != panelLaunchPrompt {
		t.Fatalf("want panelLaunchPrompt, got %d", a.focused)
	}
	// The textinput should be pre-populated with the session's ProjectPath.
	if got := a.launchInput.Value(); got != "/some/project/path" {
		t.Errorf("launch input pre-population: want %q, got %q", "/some/project/path", got)
	}
}

func TestApp_LaunchPromptEscCancels(t *testing.T) {
	a := newTestApp()
	a, _ = sendKey(a, "l")
	if a.focused != panelLaunchPrompt {
		t.Fatal("precondition: launch prompt must be open")
	}
	// Feed Esc — should close prompt and return to sessions.
	updated, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = updated.(App)
	if a.focused != panelSessions {
		t.Fatalf("after Esc: want panelSessions (%d), got %d", panelSessions, a.focused)
	}
	// No lifecycle command should have been dispatched.
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(lifecycleResultMsg); ok {
			t.Error("Esc from launch prompt should not dispatch a lifecycle command")
		}
	}
}

func TestApp_KillConfirmOpensOnK(t *testing.T) {
	a := newTestApp()
	a = seedSession(a, "kill-me", "/projects/foo")
	a, _ = sendKey(a, "k")
	if a.focused != panelKillConfirm {
		t.Fatalf("after 'k': want panelKillConfirm (%d), got %d", panelKillConfirm, a.focused)
	}
	view := a.View()
	if !strings.Contains(view, "kill-me") {
		t.Errorf("View() missing session ID in kill confirm prompt; got:\n%s", view)
	}
}

func TestApp_KillConfirmYExecutes(t *testing.T) {
	// Stub killFunc to capture the call.
	var calledWith string
	origKill := killFunc
	killFunc = func(client daemonClient, id string) error {
		calledWith = id
		return nil
	}
	t.Cleanup(func() { killFunc = origKill })

	a := newTestApp()
	a = seedSession(a, "session-xyz", "/projects/bar")
	a, _ = sendKey(a, "k")
	if a.focused != panelKillConfirm {
		t.Fatal("precondition: kill confirm must be open")
	}

	// Feed 'y' — should execute kill.
	updated, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	a = updated.(App)

	if cmd == nil {
		t.Fatal("'y' in kill confirm should return a non-nil Cmd")
	}
	// Execute the command synchronously.
	msg := cmd()
	result, ok := msg.(lifecycleResultMsg)
	if !ok {
		t.Fatalf("expected lifecycleResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected kill error: %v", result.err)
	}
	if calledWith != "session-xyz" {
		t.Errorf("killFunc called with %q, want %q", calledWith, "session-xyz")
	}
	// Focus should have returned to sessions.
	if a.focused != panelSessions {
		t.Errorf("after 'y': want panelSessions, got %d", a.focused)
	}
}

func TestApp_KillConfirmCancelsOnN(t *testing.T) {
	// Stub killFunc to detect unwanted calls.
	killCalled := false
	origKill := killFunc
	killFunc = func(client daemonClient, id string) error {
		killCalled = true
		return nil
	}
	t.Cleanup(func() { killFunc = origKill })

	a := newTestApp()
	a = seedSession(a, "dont-kill", "/projects/baz")
	a, _ = sendKey(a, "k")
	if a.focused != panelKillConfirm {
		t.Fatal("precondition: kill confirm must be open")
	}

	// Feed 'n' — should cancel.
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	a = updated.(App)

	if killCalled {
		t.Error("killFunc must not be called when user presses 'n'")
	}
	if a.focused != panelSessions {
		t.Errorf("after 'n': want panelSessions, got %d", a.focused)
	}
}

func TestApp_StatusLineExpires(t *testing.T) {
	a := newTestApp()
	a.statusLine = "some status"
	// Set expiry in the past.
	a.statusExpiry = time.Now().Add(-1 * time.Second)

	// Feed a tick — the tick handler should clear the expired status.
	updated, _ := a.Update(tickMsg(time.Now()))
	a = updated.(App)

	if a.statusLine != "" {
		t.Errorf("expected status line to be cleared on tick after expiry, got %q", a.statusLine)
	}
}

func TestApp_OKeyOpensInITerm(t *testing.T) {
	var capturedDir string
	origOpen := openInITermFunc
	openInITermFunc = func(dir string) error {
		capturedDir = dir
		return nil
	}
	t.Cleanup(func() { openInITermFunc = origOpen })

	a := newTestApp()
	a = seedSession(a, "open-me", "/projects/open-target")
	a, cmd := sendKey(a, "o")

	if cmd == nil {
		t.Fatal("'o' should return a non-nil Cmd")
	}
	// Execute the command synchronously.
	cmd()

	if capturedDir != "/projects/open-target" {
		t.Errorf("openInITermFunc called with %q, want %q", capturedDir, "/projects/open-target")
	}
	// Focus must stay on sessions (o doesn't change focus).
	if a.focused != panelSessions {
		t.Errorf("after 'o': want panelSessions, got %d", a.focused)
	}
}

func TestApp_HKeyOpensHelpOverlay(t *testing.T) {
	a := newTestApp()
	a, _ = sendKey(a, "h")
	if a.focused != panelHelp {
		t.Fatalf("after 'h': want panelHelp (%d), got %d", panelHelp, a.focused)
	}
	view := a.View()
	if !strings.Contains(view, "Keyboard Help") {
		t.Errorf("View() missing help overlay text; got:\n%s", view)
	}

	// Esc closes the overlay and returns to sessions.
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = updated.(App)
	if a.focused != panelSessions {
		t.Fatalf("after Esc from help: want panelSessions (%d), got %d", panelSessions, a.focused)
	}
}

// TestApp_IKeySwapsRightPanelToInventory feeds 'i' from the sessions panel and
// asserts the right panel now renders inventory content (category headers) instead
// of the inspector placeholder.
func TestApp_IKeySwapsRightPanelToInventory(t *testing.T) {
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg {
		return inventoryLoadedMsg{
			mcps: []ccmc.MCPEntry{{Name: "mcp-one", Scope: "global", Type: "stdio"}},
		}
	})

	a := newTestApp()
	// Simulate a window size so inventory panel gets its paneSizeMsg and fires the load.
	updated, loadCmd := a.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	a = updated.(App)
	// Execute the load Cmd returned from dispatchSizeMsg → inventoryModel.Update.
	if loadCmd != nil {
		loadMsg := loadCmd()
		if loadMsg != nil {
			a = sendMsg(a, loadMsg)
		}
	}

	// Press 'i' — inventoryMode toggles on and focus stays on sessions (not right panel).
	a, _ = sendKey(a, "i")
	if !a.inventoryMode {
		t.Fatal("inventoryMode must be true after pressing 'i'")
	}

	view := a.View()
	// The right panel should show inventory — look for the "Inventory" title header.
	if !strings.Contains(view, "Inventory") {
		t.Errorf("View() missing 'Inventory' header after 'i' press\nView:\n%s", view)
	}
	// Inspector placeholder must not be present when inventory mode is active.
	if strings.Contains(view, "(select a session)") {
		t.Errorf("View() still shows inspector placeholder when inventoryMode is true\nView:\n%s", view)
	}
}

// TestApp_IKeyToggleSwapsBack feeds 'i' twice and asserts the inspector is shown again.
func TestApp_IKeyToggleSwapsBack(t *testing.T) {
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg { return inventoryLoadedMsg{} })

	a := newTestApp()
	// Provide a window size so panels initialise.
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	a = updated.(App)

	// First 'i' — inventory mode on.
	a, _ = sendKey(a, "i")
	if !a.inventoryMode {
		t.Fatal("precondition: inventoryMode must be true after first 'i'")
	}

	// Second 'i' — inventory mode off.
	a, _ = sendKey(a, "i")
	if a.inventoryMode {
		t.Fatal("inventoryMode must be false after pressing 'i' twice")
	}

	view := a.View()
	// Inspector should be visible again.
	if !strings.Contains(view, "(select a session)") {
		t.Errorf("View() missing inspector placeholder after toggling back\nView:\n%s", view)
	}
}

func TestApp_HelpDoesNotConflictWithReferenceOverlay(t *testing.T) {
	// When the reference overlay is open, 'h' should NOT open the help overlay.
	// The reference overlay intercepts all key events via dispatchToFocused.
	a := newTestApp()
	a, _ = sendKey(a, "?") // open reference overlay
	if a.focused != panelReference {
		t.Fatal("precondition: reference overlay must be open")
	}

	// Feed 'h' — should be consumed by the reference panel, not open help.
	// We verify by checking that focused is still panelReference (not panelHelp).
	// The reference panel's Update will receive 'h' and handle it internally
	// (typing 'h' into the search box or ignoring it — either is acceptable).
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	a = updated.(App)
	if a.focused == panelHelp {
		t.Error("'h' while reference overlay is open should not open help overlay")
	}
	if a.focused != panelReference {
		t.Errorf("focus should remain panelReference, got %d", a.focused)
	}
}
