package inventory

import (
	"path/filepath"
	"sort"
	"strings"

	"ccmc/pkg/ccmc"
)

// ParseAgents reads frontmatter from every agent markdown path in raw and returns
// a sorted slice of AgentEntry. Global entries come first; within each scope agents
// are sorted alphabetically by Name. Files without valid frontmatter are warned and
// skipped — they do not cause an error return.
func ParseAgents(raw ccmc.InventoryRaw) ([]ccmc.AgentEntry, error) {
	var result []ccmc.AgentEntry

	// Global scope.
	var globalEntries []ccmc.AgentEntry
	for _, p := range raw.Global.Agents {
		if e, ok := parseAgentFile(p, "global"); ok {
			globalEntries = append(globalEntries, e)
		}
	}
	sortAgentsByName(globalEntries)
	result = append(result, globalEntries...)

	// Project scopes — InventoryRaw.Projects is already sorted ascending by ProjectPath.
	for _, scopeFiles := range raw.Projects {
		var scopeEntries []ccmc.AgentEntry
		for _, p := range scopeFiles.Agents {
			if e, ok := parseAgentFile(p, scopeFiles.ProjectPath); ok {
				scopeEntries = append(scopeEntries, e)
			}
		}
		sortAgentsByName(scopeEntries)
		result = append(result, scopeEntries...)
	}

	return result, nil
}

// parseAgentFile reads one agent .md file, extracts its frontmatter, and returns a
// populated AgentEntry. Returns (zero, false) when frontmatter is absent or malformed.
// Relies on parseFrontmatter from skills.go (same package) which handles file I/O and
// logs warnings.
func parseAgentFile(path, scope string) (ccmc.AgentEntry, bool) {
	fm, ok := parseFrontmatter(path)
	if !ok {
		return ccmc.AgentEntry{}, false
	}

	entry := ccmc.AgentEntry{
		Path:        path,
		Scope:       scope,
		Description: stringField(fm, "description"),
		Model:       stringField(fm, "model"),
	}

	// Name: prefer frontmatter "name", fall back to filename without extension.
	entry.Name = stringField(fm, "name")
	if entry.Name == "" {
		base := filepath.Base(path)
		entry.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	// Tools field supports two YAML forms:
	//   list form:   tools: [Read, Write, Edit]  — yaml.v3 yields []any
	//   string form: tools: "Read, Write, Edit"  — split on comma, trim whitespace
	entry.Tools = agentToolsField(fm, "tools")

	return entry, true
}

// agentToolsField parses the "tools" frontmatter key from an agent file. Two legal forms:
//   - YAML list:   tools: [Read, Write, Edit]  — yaml.v3 decodes as []any
//   - YAML string: tools: "Read, Write, Edit"  — split on comma, trim whitespace
//
// Returns nil when the key is absent or produces an empty effective list.
func agentToolsField(fm map[string]any, key string) []string {
	v, ok := fm[key]
	if !ok {
		return nil
	}

	switch val := v.(type) {
	case []any:
		var tools []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					tools = append(tools, s)
				}
			}
		}
		if len(tools) == 0 {
			return nil
		}
		return tools

	case string:
		parts := strings.Split(val, ",")
		var tools []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				tools = append(tools, p)
			}
		}
		if len(tools) == 0 {
			return nil
		}
		return tools

	default:
		return nil
	}
}

// sortAgentsByName sorts a slice of AgentEntry in-place, alphabetically by Name.
func sortAgentsByName(entries []ccmc.AgentEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
}
