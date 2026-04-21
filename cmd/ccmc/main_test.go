package main

import (
	"bytes"
	"strings"
	"testing"
)

// runRefOut is a test helper that runs runRef with the given arguments and
// returns the captured stdout, captured stderr, and exit code.
func runRefOut(args []string) (stdout string, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = runRef(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// TestRef_NoArgs verifies that calling "ccmc ref" with no arguments exits 2
// and writes a usage message to stderr.
func TestRef_NoArgs(t *testing.T) {
	_, errOut, code := runRefOut(nil)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("expected Usage hint in stderr, got: %q", errOut)
	}
}

// TestRef_Query_FuzzySearch verifies shape 1: a free-text query that does not
// match a known category returns results from across all categories.
func TestRef_Query_FuzzySearch(t *testing.T) {
	// Use real entry names/terms that are present in the embedded YAML data.
	tests := []struct {
		name  string
		query string
	}{
		// "SessionStart" is the first entry in hooks.yaml.
		{"known term from hooks", "SessionStart"},
		// "clear" is a partial match for "/clear" in commands.yaml.
		{"partial name match for /clear", "clear"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{tc.query})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			// Output must contain the table header.
			if !strings.Contains(out, "NAME") {
				t.Errorf("expected table header in output, got:\n%s", out)
			}
			// At least one result row (header + separator + ≥1 result = 3 lines).
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) < 3 {
				t.Errorf("expected at least one result row, got:\n%s", out)
			}
		})
	}
}

// TestRef_Query_UnknownTerm verifies that a query matching nothing exits 0 and
// prints a "no results" message rather than an error.
func TestRef_Query_UnknownTerm(t *testing.T) {
	out, errOut, code := runRefOut([]string{"xyzzy_nonexistent_zz99"})
	if code != 0 {
		t.Fatalf("expected exit 0 for no-results, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(strings.ToLower(out), "no results") {
		t.Errorf("expected 'no results' message, got: %q", out)
	}
}

// TestRef_CategoryOnly verifies shape 2: a known category string lists all
// entries in that category with NAME/CATEGORY/DESCRIPTION columns.
func TestRef_CategoryOnly(t *testing.T) {
	categories := []string{"commands", "hooks"}

	for _, cat := range categories {
		t.Run(cat, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{cat})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			if !strings.Contains(out, "NAME") || !strings.Contains(out, "CATEGORY") {
				t.Errorf("expected tabular header in output for category %q, got:\n%s", cat, out)
			}
			// Every result row should reference the requested category.
			lines := strings.Split(strings.TrimSpace(out), "\n")
			for _, line := range lines[2:] { // skip header + separator
				if line == "" {
					continue
				}
				if !strings.Contains(line, cat) {
					t.Errorf("line %q does not contain category %q", line, cat)
				}
			}
		})
	}
}

// TestRef_CategoryName verifies shape 3: a category + name shows the full
// detail view (NAME, CATEGORY, DESC fields) for the top fuzzy hit.
func TestRef_CategoryName(t *testing.T) {
	tests := []struct {
		category string
		name     string
		wantIn   []string
	}{
		{
			// "SessionStart" is the first entry in hooks.yaml.
			category: "hooks",
			name:     "SessionStart",
			wantIn:   []string{"NAME", "CATEGORY", "hooks"},
		},
		{
			// "/clear" is the first entry in commands.yaml; "clear" fuzzy-matches it.
			category: "commands",
			name:     "clear",
			wantIn:   []string{"NAME", "CATEGORY", "commands"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.category+"/"+tc.name, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{tc.category, tc.name})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in output, got:\n%s", want, out)
				}
			}
		})
	}
}

// TestRef_CategoryName_NoMatch verifies that a category+name with no fuzzy
// match exits 1 with an error on stderr.
func TestRef_CategoryName_NoMatch(t *testing.T) {
	_, errOut, code := runRefOut([]string{"commands", "xyzzy_nonexistent_zz99"})
	if code != 1 {
		t.Errorf("expected exit code 1 for no match, got %d", code)
	}
	if !strings.Contains(errOut, "no match") {
		t.Errorf("expected 'no match' in stderr, got: %q", errOut)
	}
}

// TestRef_Query_LimitTen verifies that a broad free-text query never returns
// more than 10 results (the cap enforced by shape 1).
func TestRef_Query_LimitTen(t *testing.T) {
	// "a" should fuzzily match many entries across all categories.
	out, errOut, code := runRefOut([]string{"a"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Count result rows: total lines minus header (1) and separator (1).
	resultRows := 0
	for _, l := range lines[2:] {
		if strings.TrimSpace(l) != "" {
			resultRows++
		}
	}
	if resultRows > 10 {
		t.Errorf("expected at most 10 results, got %d", resultRows)
	}
}
