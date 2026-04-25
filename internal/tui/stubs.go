package tui

// stubs.go — placeholder implementations that have not yet been replaced by
// real panels. Tasks 33-36 are now live; only the inventory stub remains.
// stubInventoryPanel will be replaced by task 47 (Phase 5).

import (
	tea "github.com/charmbracelet/bubbletea"
)

// ── stubInventory ────────────────────────────────────────────────────────────

type stubInventory struct{ focused bool }

func (s *stubInventory) Update(msg tea.Msg) (InventoryPanel, tea.Cmd) { return s, nil }
func (s *stubInventory) View() string                                  { return "[inventory panel — task 47]" }
func (s *stubInventory) Focus()                                        { s.focused = true }
func (s *stubInventory) Blur()                                         { s.focused = false }
