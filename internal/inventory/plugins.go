package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// ParsePlugins converts the raw plugin paths in raw into a flat list of PluginEntry values.
// Global scope is processed first; project scopes follow in ascending ProjectPath order.
// Within each scope plugins are sorted alphabetically by Name.
// Unreadable or malformed plugin paths are skipped with a warning.
func ParsePlugins(raw ccmc.InventoryRaw) ([]ccmc.PluginEntry, error) {
	var entries []ccmc.PluginEntry

	for _, path := range raw.Global.Plugins {
		if e, ok := parseOnePlugin(path, "global"); ok {
			entries = append(entries, e)
		}
	}

	for _, proj := range raw.Projects {
		for _, path := range proj.Plugins {
			if e, ok := parseOnePlugin(path, proj.ProjectPath); ok {
				entries = append(entries, e)
			}
		}
	}

	return entries, nil
}

// parseOnePlugin builds a PluginEntry for a single raw path.
// Returns (entry, true) on success; (zero, false) when the path is unreadable.
func parseOnePlugin(path, scope string) (ccmc.PluginEntry, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		warnf("plugins: cannot stat %s: %v — skipping", path, err)
		return ccmc.PluginEntry{}, false
	}

	entry := ccmc.PluginEntry{
		Path:  path,
		Scope: scope,
	}

	if !info.IsDir() {
		// File-based plugin: record name only (no sub-component scanning).
		base := filepath.Base(path)
		entry.Name = strings.TrimSuffix(base, filepath.Ext(base))
		return entry, true
	}

	entry.Name = filepath.Base(path)
	entry.Skills = pluginSkills(path)
	entry.Agents = pluginAgents(path)
	entry.Hooks, entry.MCPs = pluginSettingsComponents(path)

	return entry, true
}

// pluginSkills returns the basenames of immediate subdirectories under <pluginDir>/skills/.
// Only directories (not files) are treated as skill names. Sorted alphabetically.
func pluginSkills(pluginDir string) []string {
	skillsDir := filepath.Join(pluginDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			warnf("plugins: cannot read skills dir %s: %v", skillsDir, err)
		}
		return nil
	}

	var names []string
	for _, e := range entries {
		if isDotfile(e.Name()) {
			continue
		}
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}

	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}

// pluginAgents returns agent names from <pluginDir>/agents/*.md — basename without ".md".
// Sorted alphabetically.
func pluginAgents(pluginDir string) []string {
	agentsDir := filepath.Join(pluginDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			warnf("plugins: cannot read agents dir %s: %v", agentsDir, err)
		}
		return nil
	}

	var names []string
	for _, e := range entries {
		if isDotfile(e.Name()) || e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".md" {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}

	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}

// pluginSettingsComponents reads <pluginDir>/settings.json and extracts hook event names
// (top-level keys under "hooks") and MCP server names (top-level keys under "mcpServers").
// Returns (nil, nil) when settings.json is absent. On malformed JSON, logs a warning and
// returns (nil, nil) so the caller still records the plugin with empty component lists.
func pluginSettingsComponents(pluginDir string) (hooks, mcps []string) {
	settingsPath := filepath.Join(pluginDir, "settings.json")
	if !fileReadable(settingsPath) {
		return nil, nil
	}

	m, err := config.ReadSettings(settingsPath)
	if err != nil {
		warnf("plugins: cannot parse settings.json in %s: %v — hooks/MCPs will be empty", pluginDir, err)
		return nil, nil
	}

	// Extract hook event names: "hooks" is an object whose keys are event names.
	if raw, ok := m["hooks"]; ok {
		var hooksObj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &hooksObj); err == nil {
			for k := range hooksObj {
				hooks = append(hooks, k)
			}
			sort.Strings(hooks)
		}
		// Non-object "hooks" value: silently skip — not our schema, don't error.
	}

	// Extract MCP server names: "mcpServers" is an object whose keys are server names.
	if raw, ok := m["mcpServers"]; ok {
		var mcpObj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &mcpObj); err == nil {
			for k := range mcpObj {
				mcps = append(mcps, k)
			}
			sort.Strings(mcps)
		}
	}

	if len(hooks) == 0 {
		hooks = nil
	}
	if len(mcps) == 0 {
		mcps = nil
	}
	return hooks, mcps
}
