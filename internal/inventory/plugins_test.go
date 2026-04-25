package inventory

import (
	"os"
	"path/filepath"
	"testing"

	"ccmc/pkg/ccmc"
)

// makeFile creates a file at path (creating parent dirs as needed) with the given content.
func makeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("makeFile: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("makeFile: write %s: %v", path, err)
	}
}

// makeDir creates a directory at path.
func makeDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("makeDir %s: %v", path, err)
	}
}

// TestParsePlugins_Empty — no plugin paths in raw at all.
func TestParsePlugins_Empty(t *testing.T) {
	raw := ccmc.InventoryRaw{}
	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// TestParsePlugins_DirWithAllComponents — plugin dir with skills, agents, and settings.json.
func TestParsePlugins_DirWithAllComponents(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "myplugin")

	makeDir(t, filepath.Join(pluginDir, "skills", "foo"))
	makeDir(t, filepath.Join(pluginDir, "skills", "bar"))
	makeFile(t, filepath.Join(pluginDir, "agents", "myagent.md"), "")
	makeFile(t, filepath.Join(pluginDir, "settings.json"), `{
		"hooks": {
			"PostToolUse": [{"matcher":"*","hooks":[{"type":"command","command":"echo"}]}],
			"SessionStart": [{"matcher":"*","hooks":[{"type":"command","command":"echo"}]}]
		},
		"mcpServers": {
			"my-mcp": {"command": "node", "args": ["server.js"]}
		}
	}`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{pluginDir},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Name != "myplugin" {
		t.Errorf("Name: got %q want %q", e.Name, "myplugin")
	}
	if e.Scope != "global" {
		t.Errorf("Scope: got %q want %q", e.Scope, "global")
	}

	// Skills: sorted alphabetically
	wantSkills := []string{"bar", "foo"}
	if len(e.Skills) != len(wantSkills) {
		t.Fatalf("Skills len: got %d want %d", len(e.Skills), len(wantSkills))
	}
	for i, s := range wantSkills {
		if e.Skills[i] != s {
			t.Errorf("Skills[%d]: got %q want %q", i, e.Skills[i], s)
		}
	}

	// Agents
	if len(e.Agents) != 1 || e.Agents[0] != "myagent" {
		t.Errorf("Agents: got %v want [myagent]", e.Agents)
	}

	// Hooks — two event names
	if len(e.Hooks) != 2 {
		t.Errorf("Hooks len: got %d want 2 (%v)", len(e.Hooks), e.Hooks)
	}

	// MCPs — one server
	if len(e.MCPs) != 1 || e.MCPs[0] != "my-mcp" {
		t.Errorf("MCPs: got %v want [my-mcp]", e.MCPs)
	}
}

// TestParsePlugins_DirWithSubsetComponents — only skills, no agents or settings.json.
func TestParsePlugins_DirWithSubsetComponents(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "partial")

	makeDir(t, filepath.Join(pluginDir, "skills", "alpha"))

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{pluginDir},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if len(e.Skills) != 1 || e.Skills[0] != "alpha" {
		t.Errorf("Skills: got %v want [alpha]", e.Skills)
	}
	if e.Agents != nil {
		t.Errorf("Agents: expected nil, got %v", e.Agents)
	}
	if e.Hooks != nil {
		t.Errorf("Hooks: expected nil, got %v", e.Hooks)
	}
	if e.MCPs != nil {
		t.Errorf("MCPs: expected nil, got %v", e.MCPs)
	}
}

// TestParsePlugins_FileEntry — plugin path is a file, not a directory.
func TestParsePlugins_FileEntry(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "myplugin.zip")
	makeFile(t, pluginFile, "fake zip content")

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{pluginFile},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Name != "myplugin" {
		t.Errorf("Name: got %q want %q", e.Name, "myplugin")
	}
	if e.Skills != nil {
		t.Errorf("Skills: expected nil for file entry, got %v", e.Skills)
	}
	if e.Agents != nil {
		t.Errorf("Agents: expected nil for file entry, got %v", e.Agents)
	}
	if e.Hooks != nil {
		t.Errorf("Hooks: expected nil for file entry, got %v", e.Hooks)
	}
	if e.MCPs != nil {
		t.Errorf("MCPs: expected nil for file entry, got %v", e.MCPs)
	}
}

// TestParsePlugins_NoSettingsJSON — plugin dir with only skills; no settings.json.
func TestParsePlugins_NoSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "nosettings")
	makeDir(t, filepath.Join(pluginDir, "skills", "myskill"))

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{pluginDir},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Hooks != nil {
		t.Errorf("Hooks: expected nil when no settings.json, got %v", e.Hooks)
	}
	if e.MCPs != nil {
		t.Errorf("MCPs: expected nil when no settings.json, got %v", e.MCPs)
	}
}

// TestParsePlugins_MalformedSettingsJSON — broken JSON in settings.json; plugin still recorded
// with empty Hooks/MCPs rather than failing.
func TestParsePlugins_MalformedSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "badconfig")
	makeDir(t, pluginDir)
	makeFile(t, filepath.Join(pluginDir, "settings.json"), `{ invalid json !!!`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{pluginDir},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected plugin to still be recorded; got %d entries", len(entries))
	}

	e := entries[0]
	if e.Name != "badconfig" {
		t.Errorf("Name: got %q want %q", e.Name, "badconfig")
	}
	if e.Hooks != nil {
		t.Errorf("Hooks: expected nil after malformed JSON, got %v", e.Hooks)
	}
	if e.MCPs != nil {
		t.Errorf("MCPs: expected nil after malformed JSON, got %v", e.MCPs)
	}
}

// TestParsePlugins_TwoProjects — global + two project scopes; assert sort order.
// Global entries come first; projects follow in ascending ProjectPath order.
func TestParsePlugins_TwoProjects(t *testing.T) {
	dir := t.TempDir()

	globalPlugin := filepath.Join(dir, "global-plugin")
	makeDir(t, globalPlugin)

	projA := filepath.Join(dir, "project-a")
	projAPlugin := filepath.Join(projA, "pa-plugin")
	makeDir(t, projAPlugin)

	projB := filepath.Join(dir, "project-b")
	projBPlugin := filepath.Join(projB, "pb-plugin")
	makeDir(t, projBPlugin)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{
			Plugins: []string{globalPlugin},
		},
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: projA, Plugins: []string{projAPlugin}},
			{ProjectPath: projB, Plugins: []string{projBPlugin}},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Global must come first.
	if entries[0].Scope != "global" {
		t.Errorf("entries[0].Scope: got %q want %q", entries[0].Scope, "global")
	}
	// Projects in the order supplied by scanner (ascending ProjectPath).
	if entries[1].Scope != projA {
		t.Errorf("entries[1].Scope: got %q want %q", entries[1].Scope, projA)
	}
	if entries[2].Scope != projB {
		t.Errorf("entries[2].Scope: got %q want %q", entries[2].Scope, projB)
	}
}

// TestParsePlugins_ProjectScopeTagging — each entry's Scope reflects its origin correctly.
func TestParsePlugins_ProjectScopeTagging(t *testing.T) {
	dir := t.TempDir()

	projPath := filepath.Join(dir, "my-project")
	pluginDir := filepath.Join(projPath, "my-plugin")
	makeDir(t, pluginDir)

	raw := ccmc.InventoryRaw{
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: projPath, Plugins: []string{pluginDir}},
		},
	}

	entries, err := ParsePlugins(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Scope != projPath {
		t.Errorf("Scope: got %q want %q", e.Scope, projPath)
	}
	if e.Name != "my-plugin" {
		t.Errorf("Name: got %q want %q", e.Name, "my-plugin")
	}
}
