package reference

import (
	"github.com/sahilm/fuzzy"

	"ccmc/pkg/ccmc"
)

// Engine provides fuzzy search over a loaded set of RefEntry values.
// It is safe for concurrent reads; the entry slice is immutable after construction.
type Engine struct {
	entries []ccmc.RefEntry
	corpus  []string // parallel slice: corpus[i] == entries[i] search target
}

// NewEngine constructs an Engine from a slice of RefEntry values (typically the
// output of LoadAll). The caller retains ownership of the slice — Engine copies
// only the derived corpus strings, not the entries themselves.
func NewEngine(entries []ccmc.RefEntry) *Engine {
	corpus := make([]string, len(entries))
	for i, e := range entries {
		corpus[i] = e.Name + " " + e.Description
	}
	return &Engine{entries: entries, corpus: corpus}
}

// Search returns RefEntry values that match query, optionally filtered by
// category. Rules:
//   - If category is non-nil, only entries with that category are considered.
//   - If query is empty, entries are returned in load order (no ranking).
//   - Otherwise, fuzzy matching is applied to "name description" and results are
//     returned in descending match-score order.
//   - limit <= 0 means no limit; otherwise at most limit entries are returned.
func (e *Engine) Search(query string, category *ccmc.RefCategory, limit int) []ccmc.RefEntry {
	// Build the candidate index set — either all entries or category-filtered.
	candidates := e.candidateIndices(category)

	if query == "" {
		return e.applyLimit(e.pickEntries(candidates), limit)
	}

	// Build a sub-corpus aligned to the candidate indices so fuzzy.Find
	// returns indices into candidates, not into the full corpus.
	subCorpus := make([]string, len(candidates))
	for i, idx := range candidates {
		subCorpus[i] = e.corpus[idx]
	}

	matches := fuzzy.Find(query, subCorpus)

	results := make([]ccmc.RefEntry, 0, len(matches))
	for _, m := range matches {
		results = append(results, e.entries[candidates[m.Index]])
	}

	return e.applyLimit(results, limit)
}

// candidateIndices returns the indices into e.entries that pass the optional
// category filter. When category is nil all indices are returned.
func (e *Engine) candidateIndices(category *ccmc.RefCategory) []int {
	indices := make([]int, 0, len(e.entries))
	for i, entry := range e.entries {
		if category == nil || entry.Category == *category {
			indices = append(indices, i)
		}
	}
	return indices
}

// pickEntries materialises a slice of RefEntry from an index list.
func (e *Engine) pickEntries(indices []int) []ccmc.RefEntry {
	out := make([]ccmc.RefEntry, len(indices))
	for i, idx := range indices {
		out[i] = e.entries[idx]
	}
	return out
}

// applyLimit truncates result to at most limit entries. limit <= 0 means no cap.
func (e *Engine) applyLimit(results []ccmc.RefEntry, limit int) []ccmc.RefEntry {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}
