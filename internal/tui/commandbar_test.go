package tui

import (
	"strings"
	"testing"
)

// makeCommandBar returns a commandBarModel wired with the given focus and width.
func makeCommandBar(focus panel, width int) CommandBarPanel {
	p := NewCommandBarPanel()
	p.Update(focusChangedMsg{active: focus, width: width})
	return p
}

// TestCommandBar_RendersForEachFocus verifies that for each panel-focus value,
// the expected key tokens appear in View().
func TestCommandBar_RendersForEachFocus(t *testing.T) {
	cases := []struct {
		focus     panel
		wantKeys  []string
	}{
		{
			focus:    panelSessions,
			wantKeys: []string{"[↑↓]", "[Enter]", "[l]", "[k]", "[i]", "[?]", "[q]"},
		},
		{
			focus:    panelInspector,
			wantKeys: []string{"[↑↓]", "[r]", "[Tab]", "[?]", "[q]"},
		},
		{
			focus:    panelReference,
			wantKeys: []string{"[type]", "[↑↓]", "[Enter]", "[Esc]"},
		},
		{
			focus:    panelInventory,
			wantKeys: []string{"[↑↓]", "[Tab]", "[?]", "[q]"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(hintsFor(tc.focus), func(t *testing.T) {
			cb := makeCommandBar(tc.focus, 300)
			view := cb.View()

			for _, key := range tc.wantKeys {
				if !strings.Contains(view, key) {
					t.Errorf("focus %d: View() missing key token %q\nView: %s", tc.focus, key, view)
				}
			}
		})
	}
}

// TestCommandBar_TruncatesWhenNarrow sets width to 20 and feeds the longest hint
// set (panelSessions), then asserts View() ends with the truncation marker "…".
func TestCommandBar_TruncatesWhenNarrow(t *testing.T) {
	cb := makeCommandBar(panelSessions, 20)
	view := cb.View()

	if !strings.Contains(view, "…") {
		t.Errorf("narrow width (20): View() missing truncation marker '…'\nView: %s", view)
	}
}
