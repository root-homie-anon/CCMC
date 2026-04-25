package tui

// msgs.go — shared message types used across multiple panels.
// Panel-specific messages (e.g. inspectorLoadedMsg) live in their own files.

// paneSizeMsg is dispatched by App.Update on tea.WindowSizeMsg so each panel
// knows its allocated width and height for layout and truncation.
type paneSizeMsg struct {
	w int
	h int
}
