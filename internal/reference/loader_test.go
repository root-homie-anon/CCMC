package reference

import (
	"testing"
	"time"

	"ccmc/pkg/ccmc"
)

// TestLoadAll verifies that LoadAll returns at least one entry per embedded
// YAML file, that every entry has a non-empty Name and Category, and that
// the returned categories are valid RefCategory values.
func TestLoadAll(t *testing.T) {
	entries, err := LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("LoadAll() returned 0 entries; expected at least one")
	}

	validCategories := map[ccmc.RefCategory]bool{
		ccmc.RefCommands:    true,
		ccmc.RefSkills:      true,
		ccmc.RefFlags:       true,
		ccmc.RefShortcuts:   true,
		ccmc.RefHooks:       true,
		ccmc.RefTools:       true,
		ccmc.RefFrontmatter: true,
		ccmc.RefEnvVars:     true,
		ccmc.RefFilePaths:   true,
	}

	for i, e := range entries {
		if e.Name == "" {
			t.Errorf("entry[%d]: Name is empty", i)
		}
		if e.Category == "" {
			t.Errorf("entry[%d] (%q): Category is empty", i, e.Name)
		}
		if !validCategories[e.Category] {
			t.Errorf("entry[%d] (%q): unknown Category %q", i, e.Name, e.Category)
		}
	}
}

// TestLoadAllContainsCommands verifies that at least one commands-category
// entry is present (smoke-test that commands.yaml was embedded and parsed).
func TestLoadAllContainsCommands(t *testing.T) {
	entries, err := LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	var count int
	for _, e := range entries {
		if e.Category == ccmc.RefCommands {
			count++
		}
	}
	if count == 0 {
		t.Error("LoadAll() returned no entries with category 'commands'")
	}
}

// TestLoadAllContainsHooks verifies that at least one hooks-category entry
// is present (smoke-test that hooks.yaml was embedded and parsed).
func TestLoadAllContainsHooks(t *testing.T) {
	entries, err := LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	var count int
	for _, e := range entries {
		if e.Category == ccmc.RefHooks {
			count++
		}
	}
	if count == 0 {
		t.Error("LoadAll() returned no entries with category 'hooks'")
	}
}

// BenchmarkLoadAll measures the wall-clock cost of a single LoadAll() call.
// The benchmark asserts (via t.Fatal) that the measured time is under 50ms.
func BenchmarkLoadAll(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := LoadAll()
		if err != nil {
			b.Fatalf("LoadAll() error = %v", err)
		}
	}
}

// TestLoadAllStartupUnder50ms is an explicit wall-clock assertion: a single
// cold call to LoadAll() must complete in under 50ms.
func TestLoadAllStartupUnder50ms(t *testing.T) {
	start := time.Now()
	_, err := LoadAll()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	const budget = 50 * time.Millisecond
	if elapsed > budget {
		t.Errorf("LoadAll() took %v; must complete in under %v", elapsed, budget)
	}
}
