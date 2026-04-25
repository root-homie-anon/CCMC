package inventory

import (
	"encoding/json"
	"fmt"
	"sort"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// mcpServerRaw mirrors the shape of one entry under mcpServers in settings.json.
// Fields beyond command/args/url/tools are ignored.
type mcpServerRaw struct {
	Command string          `json:"command"`
	Args    []string        `json:"args"`
	URL     string          `json:"url"`
	Tools   json.RawMessage `json:"tools"` // may be absent, array, or anything else
}

// ParseMCPs reads the mcpServers block from every settings.json referenced in raw,
// building a flat list of MCPEntry values. Global scope is processed first; project
// scopes follow in ascending ProjectPath order (the order guaranteed by scanner.go).
//
// Per-scope errors (unreadable file, malformed JSON) are logged to stderr and
// skipped; the remainder of the inventory is still returned. The only case that
// produces a non-nil return error is if mcpServers is present but is not a JSON
// object (e.g. it is a string or array) — that is a data-type mismatch that cannot
// be skipped silently because it signals the file is corrupt or written by a
// different tool with an incompatible schema.
func ParseMCPs(raw ccmc.InventoryRaw) ([]ccmc.MCPEntry, error) {
	var entries []ccmc.MCPEntry

	// Global scope: Scope field is "global".
	if raw.Global.SettingsPath != "" {
		scoped, err := parseScopeMCPs(raw.Global.SettingsPath, "global")
		if err != nil {
			return nil, err
		}
		entries = append(entries, scoped...)
	}

	// Project scopes: Scope field is the ProjectPath (absolute encoded dir name from scanner).
	for _, proj := range raw.Projects {
		if proj.SettingsPath == "" {
			continue
		}
		scoped, err := parseScopeMCPs(proj.SettingsPath, proj.ProjectPath)
		if err != nil {
			return nil, err
		}
		entries = append(entries, scoped...)
	}

	return entries, nil
}

// parseScopeMCPs reads one settings.json at path and returns all MCPEntry values for
// the given scope label. Returns nil slice (not an error) when the file has no
// mcpServers block. Returns an error only when mcpServers is the wrong JSON type.
func parseScopeMCPs(settingsPath, scope string) ([]ccmc.MCPEntry, error) {
	m, err := config.ReadSettings(settingsPath)
	if err != nil {
		warnf("mcp: cannot read settings %s: %v", settingsPath, err)
		return nil, nil
	}

	raw, ok := m["mcpServers"]
	if !ok {
		// No mcpServers key — valid; this scope has no MCPs.
		return nil, nil
	}

	// mcpServers must be a JSON object. Reject any other type so the caller knows
	// the file is structurally wrong rather than silently producing zero entries.
	var serversMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &serversMap); err != nil {
		return nil, fmt.Errorf("mcp: mcpServers in %s is not an object: %w", settingsPath, err)
	}

	// Collect and sort names so output order is deterministic within each scope.
	names := make([]string, 0, len(serversMap))
	for name := range serversMap {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]ccmc.MCPEntry, 0, len(names))
	for _, name := range names {
		entryRaw := serversMap[name]

		var srv mcpServerRaw
		if err := json.Unmarshal(entryRaw, &srv); err != nil {
			warnf("mcp: cannot parse entry %q in %s: %v — skipping", name, settingsPath, err)
			continue
		}

		entry := ccmc.MCPEntry{
			Name:   name,
			Scope:  scope,
			Status: "configured",
		}

		switch {
		case srv.Command != "":
			entry.Type = "stdio"
			entry.Command = srv.Command
			entry.Args = srv.Args
		case srv.URL != "":
			entry.Type = "sse"
			entry.URL = srv.URL
		default:
			entry.Type = "unknown"
		}

		// Tools: parse only if the field is present and is an array of strings.
		// Any other shape (null, missing, wrong type) leaves entry.Tools nil.
		if len(srv.Tools) > 0 && string(srv.Tools) != "null" {
			var tools []string
			if err := json.Unmarshal(srv.Tools, &tools); err == nil {
				entry.Tools = tools
			}
			// If unmarshal fails (not a string array), leave Tools nil — don't error.
		}

		entries = append(entries, entry)
	}

	return entries, nil
}
