package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// stubSearcher records each Search call and returns a canned result set.
// It satisfies the searcher interface added as a test seam in reference.go.
type stubSearcher struct {
	calls   []string         // query string from each call, in order
	results []ccmc.RefEntry  // fixed result set returned on every call
}

func (s *stubSearcher) Search(query string, _ *ccmc.RefCategory, _ int) []ccmc.RefEntry {
	s.calls = append(s.calls, query)
	return s.results
}

// threeEntries returns three distinct RefEntry values for navigation/detail tests.
func threeEntries() []ccmc.RefEntry {
	return []ccmc.RefEntry{
		{
			Name:        "alpha",
			Category:    ccmc.RefCommands,
			Description: "first entry description",
			Detail:      "alpha full detail text only in detail view",
		},
		{
			Name:        "beta",
			Category:    ccmc.RefCommands,
			Description: "second entry description",
			Detail:      "beta full detail text only in detail view",
		},
		{
			Name:        "gamma",
			Category:    ccmc.RefCommands,
			Description: "third entry description",
			Detail:      "gamma full detail text only in detail view",
		},
	}
}

// newRefPanel constructs a referenceModel with the given stubSearcher injected
// directly, bypassing NewReferencePanel which takes *reference.Engine.
func newRefPanel(stub *stubSearcher) *referenceModel {
	raw := NewReferencePanel(nil)
	rm := raw.(*referenceModel)
	rm.engine = stub
	rm.width = 120
	rm.height = 40
	return rm
}

// buildRefPanel is a helper that constructs a *referenceModel with a stub and
// pre-loads results so navigation tests start with data.
func buildRefPanel(stub *stubSearcher) *referenceModel {
	p := newRefPanel(stub)
	// Seed the results directly so we do not depend on the input widget.
	p.results = stub.results
	return p
}

// sendRefKey feeds a key to a referenceModel and returns the updated model and cmd.
func sendRefKey(p *referenceModel, key string) (*referenceModel, tea.Cmd) {
	var msg tea.KeyMsg
	switch key {
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, cmd := p.Update(msg)
	return updated.(*referenceModel), cmd
}

// TestReference_TypingTriggersSearch feeds rune keys "s", "e", "s" one at a
// time and asserts Search was called with progressively longer queries.
func TestReference_TypingTriggersSearch(t *testing.T) {
	stub := &stubSearcher{results: threeEntries()}
	rm := newRefPanel(stub)
	rm.focused = true
	rm.input.Focus()

	// Feed runes one by one. Each rune goes through handleKey → input.Update → runSearch.
	for _, ch := range []string{"s", "e", "s"} {
		updated, _ := rm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(ch)})
		rm = updated.(*referenceModel)
	}

	// Three runes typed → Search called 3 times.
	if len(stub.calls) != 3 {
		t.Fatalf("expected 3 Search calls, got %d: %v", len(stub.calls), stub.calls)
	}
	// Queries must grow progressively: "s", "se", "ses".
	want := []string{"s", "se", "ses"}
	for i, q := range stub.calls {
		if q != want[i] {
			t.Errorf("call[%d]: got query %q, want %q", i, q, want[i])
		}
	}
}

// TestReference_NavigatesResults pre-loads 3 results, feeds ↓, and asserts the
// selected index increments; then feeds ↓ many more times and asserts clamping.
func TestReference_NavigatesResults(t *testing.T) {
	stub := &stubSearcher{results: threeEntries()}
	p := buildRefPanel(stub)
	p.focused = true

	if p.selected != 0 {
		t.Fatalf("initial selected = %d, want 0", p.selected)
	}

	p, _ = sendRefKey(p, "down")
	if p.selected != 1 {
		t.Errorf("after one ↓: selected = %d, want 1", p.selected)
	}

	// Many more downs — should clamp at 2 (last index).
	for i := 0; i < 10; i++ {
		p, _ = sendRefKey(p, "down")
	}
	if p.selected != 2 {
		t.Errorf("bottom clamp: selected = %d, want 2", p.selected)
	}
}

// TestReference_EnterShowsDetail feeds Enter on a selected result and asserts
// View() contains the entry's Detail field (only present in the detail view).
func TestReference_EnterShowsDetail(t *testing.T) {
	stub := &stubSearcher{results: threeEntries()}
	p := buildRefPanel(stub)
	p.focused = true

	// Enter while in list view should open detail.
	p, _ = sendRefKey(p, "enter")

	if !p.detailView {
		t.Fatal("detailView should be true after Enter in list view")
	}

	view := p.View()
	// Detail text is only rendered in detail view, not in the list summary.
	wantDetail := threeEntries()[0].Detail
	if !strings.Contains(view, wantDetail) {
		t.Errorf("View() in detail mode missing entry Detail %q\nView:\n%s", wantDetail, view)
	}
}

// TestReference_EscFromDetailReturnsToList opens the detail view then presses
// Esc and asserts we're back in list mode (panel's own state machine, not App's
// global Esc which closes the overlay).
func TestReference_EscFromDetailReturnsToList(t *testing.T) {
	stub := &stubSearcher{results: threeEntries()}
	p := buildRefPanel(stub)
	p.focused = true

	// Open detail.
	p, _ = sendRefKey(p, "enter")
	if !p.detailView {
		t.Fatal("precondition: detailView should be true after Enter")
	}

	// Esc from detail → back to list.
	p, cmd := sendRefKey(p, "esc")
	if p.detailView {
		t.Error("detailView should be false after Esc in detail mode")
	}
	// Esc from detail must NOT emit closeOverlayMsg — that's only for list-level Esc.
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(closeOverlayMsg); ok {
			t.Error("Esc from detail view must not emit closeOverlayMsg (only list-level Esc should)")
		}
	}

	// View should show list summary, not the detail-only text.
	view := p.View()
	detailText := threeEntries()[0].Detail
	if strings.Contains(view, detailText) {
		t.Errorf("View() after Esc still shows detail text %q — should be list view\nView:\n%s",
			detailText, view)
	}
}
