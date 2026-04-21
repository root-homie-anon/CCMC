package reference

import (
	"testing"

	"ccmc/pkg/ccmc"
)

// testEntries is a small, deterministic fixture set for engine tests.
var testEntries = []ccmc.RefEntry{
	{Name: "inspect", Category: ccmc.RefCommands, Description: "show session details"},
	{Name: "ls", Category: ccmc.RefCommands, Description: "list active sessions"},
	{Name: "fuzzy-find", Category: ccmc.RefSkills, Description: "in-memory search skill"},
	{Name: "session-start", Category: ccmc.RefHooks, Description: "fires when a session begins"},
	{Name: "debug-flag", Category: ccmc.RefFlags, Description: "enable verbose output"},
}

func newTestEngine() *Engine {
	return NewEngine(testEntries)
}

// ptr is a helper to take the address of a RefCategory literal.
func ptr(c ccmc.RefCategory) *ccmc.RefCategory { return &c }

func TestSearch_EmptyQuery_ReturnsAll(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("", nil, 0)
	if len(results) != len(testEntries) {
		t.Fatalf("expected %d results, got %d", len(testEntries), len(results))
	}
}

func TestSearch_EmptyQuery_LimitEnforced(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("", nil, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(results))
	}
}

func TestSearch_NameMatch(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("inspect", nil, 0)
	if len(results) == 0 {
		t.Fatal("expected at least one result for query 'inspect', got none")
	}
	if results[0].Name != "inspect" {
		t.Errorf("expected top result to be 'inspect', got %q", results[0].Name)
	}
}

func TestSearch_DescriptionMatch(t *testing.T) {
	eng := newTestEngine()
	// "verbose" appears only in debug-flag's description.
	results := eng.Search("verbose", nil, 0)
	if len(results) == 0 {
		t.Fatal("expected at least one result for query 'verbose', got none")
	}
	found := false
	for _, r := range results {
		if r.Name == "debug-flag" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'debug-flag' in description-match results, got %v", results)
	}
}

func TestSearch_CategoryFilter_NonNil(t *testing.T) {
	eng := newTestEngine()
	cat := ccmc.RefCommands
	results := eng.Search("", &cat, 0)
	for _, r := range results {
		if r.Category != ccmc.RefCommands {
			t.Errorf("unexpected category %q in filtered results", r.Category)
		}
	}
	// Expect exactly the 2 command entries.
	if len(results) != 2 {
		t.Fatalf("expected 2 command entries, got %d", len(results))
	}
}

func TestSearch_CategoryFilter_NilReturnsAll(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("", nil, 0)
	if len(results) != len(testEntries) {
		t.Fatalf("nil category should return all %d entries, got %d", len(testEntries), len(results))
	}
}

func TestSearch_CategoryFilter_WithQuery(t *testing.T) {
	eng := newTestEngine()
	// "session" matches both "ls" description and "session-start" name,
	// but category=RefHooks should restrict to hooks only.
	cat := ccmc.RefHooks
	results := eng.Search("session", &cat, 0)
	for _, r := range results {
		if r.Category != ccmc.RefHooks {
			t.Errorf("result %q has wrong category %q", r.Name, r.Category)
		}
	}
}

func TestSearch_LimitEnforced_WithQuery(t *testing.T) {
	eng := newTestEngine()
	// "s" will fuzzily match several entries; limit to 1.
	results := eng.Search("s", nil, 1)
	if len(results) > 1 {
		t.Fatalf("expected at most 1 result with limit=1, got %d", len(results))
	}
}

func TestSearch_ZeroLimit_NoTruncation(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("", nil, 0)
	if len(results) != len(testEntries) {
		t.Fatalf("limit=0 should not truncate; expected %d, got %d", len(testEntries), len(results))
	}
}

func TestSearch_NegativeLimit_NoTruncation(t *testing.T) {
	eng := newTestEngine()
	results := eng.Search("", nil, -1)
	if len(results) != len(testEntries) {
		t.Fatalf("limit=-1 should not truncate; expected %d, got %d", len(testEntries), len(results))
	}
}

func TestNewEngine_EmptyEntries(t *testing.T) {
	eng := NewEngine(nil)
	results := eng.Search("anything", nil, 0)
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty engine, got %d", len(results))
	}
}
