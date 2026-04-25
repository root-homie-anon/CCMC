package tui

// stubs.go — placeholder implementations of the five panel interfaces.
// Each stub renders a single line with the panel name and the task number that
// will replace it. When tasks 33-36 and 47 ship, the concrete types replace
// these stubs; only the interface assignment in app.go changes.

import (
	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// ── stubSessions ─────────────────────────────────────────────────────────────

type stubSessions struct{ focused bool }

func (s *stubSessions) Update(msg tea.Msg) (SessionsPanel, tea.Cmd) { return s, nil }
func (s *stubSessions) View() string                                  { return "[sessions panel — task 33]" }
func (s *stubSessions) Focus()                                        { s.focused = true }
func (s *stubSessions) Blur()                                         { s.focused = false }
func (s *stubSessions) Selected() *ccmc.Session                       { return nil }

// ── stubInspector ────────────────────────────────────────────────────────────

type stubInspector struct{ focused bool }

func (s *stubInspector) Update(msg tea.Msg) (InspectorPanel, tea.Cmd) { return s, nil }
func (s *stubInspector) View() string                                  { return "[inspector panel — task 34]" }
func (s *stubInspector) Focus()                                        { s.focused = true }
func (s *stubInspector) Blur()                                         { s.focused = false }

// ── stubInventory ────────────────────────────────────────────────────────────

type stubInventory struct{ focused bool }

func (s *stubInventory) Update(msg tea.Msg) (InventoryPanel, tea.Cmd) { return s, nil }
func (s *stubInventory) View() string                                  { return "[inventory panel — task 47]" }
func (s *stubInventory) Focus()                                        { s.focused = true }
func (s *stubInventory) Blur()                                         { s.focused = false }

// ── stubReference ────────────────────────────────────────────────────────────

type stubReference struct{ focused bool }

func (s *stubReference) Update(msg tea.Msg) (ReferencePanel, tea.Cmd) { return s, nil }
func (s *stubReference) View() string                                  { return "[reference overlay — task 35]" }
func (s *stubReference) Focus()                                        { s.focused = true }
func (s *stubReference) Blur()                                         { s.focused = false }

// ── stubCommandBar ───────────────────────────────────────────────────────────

type stubCommandBar struct{ focused bool }

func (s *stubCommandBar) Update(msg tea.Msg) (CommandBarPanel, tea.Cmd) { return s, nil }
func (s *stubCommandBar) View() string                                   { return "[command bar — task 36]" }
func (s *stubCommandBar) Focus()                                         { s.focused = true }
func (s *stubCommandBar) Blur()                                          { s.focused = false }
