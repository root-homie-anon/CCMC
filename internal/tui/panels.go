package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// SessionsPanel is the contract for the left-column session list (task 33).
// The Selected method lets the inspector react to cursor movement without coupling
// the two panels directly — App.Update reads Selected and dispatches to the inspector.
type SessionsPanel interface {
	Update(msg tea.Msg) (SessionsPanel, tea.Cmd)
	View() string
	Focus()
	Blur()
	// Selected returns the session under the cursor, or nil if the list is empty.
	Selected() *ccmc.Session
}

// InspectorPanel is the contract for the right-column inspector (task 34).
type InspectorPanel interface {
	Update(msg tea.Msg) (InspectorPanel, tea.Cmd)
	View() string
	Focus()
	Blur()
}

// InventoryPanel is the contract for the right-column inventory view (task 47).
type InventoryPanel interface {
	Update(msg tea.Msg) (InventoryPanel, tea.Cmd)
	View() string
	Focus()
	Blur()
}

// ReferencePanel is the contract for the reference search overlay (task 35).
type ReferencePanel interface {
	Update(msg tea.Msg) (ReferencePanel, tea.Cmd)
	View() string
	Focus()
	Blur()
}

// CommandBarPanel is the contract for the bottom command bar (task 36).
type CommandBarPanel interface {
	Update(msg tea.Msg) (CommandBarPanel, tea.Cmd)
	View() string
	Focus()
	Blur()
}
