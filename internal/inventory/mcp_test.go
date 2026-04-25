package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ccmc/pkg/ccmc"
)

// writeSettings writes a map as a JSON settings.json at the given path, creating
// parent directories as needed.
func writeSettings(t *testing.T, path string, content map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeSettings: mkdir: %v", err)
	}
	b, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("writeSettings: marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writeSettings: write: %v", err)
	}
}

// TestParseMCPs_GlobalStdio — one stdio MCP in global settings.json.
func TestParseMCPs_GlobalStdio(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"my-tool": map[string]any{
				"command": "/usr/bin/my-tool",
				"args":    []string{"--port", "9000"},
			},
		},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Name != "my-tool" {
		t.Errorf("Name: got %q, want %q", e.Name, "my-tool")
	}
	if e.Scope != "global" {
		t.Errorf("Scope: got %q, want %q", e.Scope, "global")
	}
	if e.Type != "stdio" {
		t.Errorf("Type: got %q, want %q", e.Type, "stdio")
	}
	if e.Command != "/usr/bin/my-tool" {
		t.Errorf("Command: got %q, want %q", e.Command, "/usr/bin/my-tool")
	}
	if len(e.Args) != 2 || e.Args[0] != "--port" || e.Args[1] != "9000" {
		t.Errorf("Args: got %v", e.Args)
	}
	if e.Status != "configured" {
		t.Errorf("Status: got %q, want %q", e.Status, "configured")
	}
	if e.URL != "" {
		t.Errorf("URL should be empty for stdio, got %q", e.URL)
	}
}

// TestParseMCPs_GlobalSSE — one SSE MCP in global settings.json.
func TestParseMCPs_GlobalSSE(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"remote-mcp": map[string]any{
				"url": "https://mcp.example.com/v1",
			},
		},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Name != "remote-mcp" {
		t.Errorf("Name: got %q", e.Name)
	}
	if e.Type != "sse" {
		t.Errorf("Type: got %q, want sse", e.Type)
	}
	if e.URL != "https://mcp.example.com/v1" {
		t.Errorf("URL: got %q", e.URL)
	}
	if e.Command != "" {
		t.Errorf("Command should be empty for sse, got %q", e.Command)
	}
	if e.Scope != "global" {
		t.Errorf("Scope: got %q, want global", e.Scope)
	}
	if e.Status != "configured" {
		t.Errorf("Status: got %q, want configured", e.Status)
	}
}

// TestParseMCPs_ProjectsAppended — global + 2 project settings, each with MCPs.
// Asserts global entries come first, then projects in ProjectPath order, MCPs
// alphabetical within each scope.
func TestParseMCPs_ProjectsAppended(t *testing.T) {
	dir := t.TempDir()

	globalSettings := filepath.Join(dir, "global", "settings.json")
	writeSettings(t, globalSettings, map[string]any{
		"mcpServers": map[string]any{
			"global-mcp": map[string]any{"command": "/bin/global"},
		},
	})

	// proj-a comes second alphabetically but scanner guarantees sorted Projects slice.
	projASettings := filepath.Join(dir, "proj-a", "settings.json")
	writeSettings(t, projASettings, map[string]any{
		"mcpServers": map[string]any{
			"alpha-mcp": map[string]any{"command": "/bin/alpha"},
			"beta-mcp":  map[string]any{"url": "https://beta.example.com"},
		},
	})

	projBSettings := filepath.Join(dir, "proj-b", "settings.json")
	writeSettings(t, projBSettings, map[string]any{
		"mcpServers": map[string]any{
			"zeta-mcp": map[string]any{"command": "/bin/zeta"},
		},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: globalSettings},
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: "proj-a", SettingsPath: projASettings},
			{ProjectPath: "proj-b", SettingsPath: projBSettings},
		},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}

	// Global first.
	if entries[0].Name != "global-mcp" || entries[0].Scope != "global" {
		t.Errorf("entry[0]: got name=%q scope=%q", entries[0].Name, entries[0].Scope)
	}
	// proj-a entries, alphabetical.
	if entries[1].Name != "alpha-mcp" || entries[1].Scope != "proj-a" {
		t.Errorf("entry[1]: got name=%q scope=%q", entries[1].Name, entries[1].Scope)
	}
	if entries[2].Name != "beta-mcp" || entries[2].Scope != "proj-a" {
		t.Errorf("entry[2]: got name=%q scope=%q", entries[2].Name, entries[2].Scope)
	}
	// proj-b entry.
	if entries[3].Name != "zeta-mcp" || entries[3].Scope != "proj-b" {
		t.Errorf("entry[3]: got name=%q scope=%q", entries[3].Name, entries[3].Scope)
	}
}

// TestParseMCPs_NoSettings — empty InventoryRaw; returns empty slice, no error.
func TestParseMCPs_NoSettings(t *testing.T) {
	entries, err := ParseMCPs(ccmc.InventoryRaw{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %+v", entries)
	}
}

// TestParseMCPs_NoMCPsBlock — settings.json exists but has no mcpServers key.
// Expect empty slice, no error.
func TestParseMCPs_NoMCPsBlock(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"permissions": map[string]any{"allow": []string{"Read"}},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %+v", entries)
	}
}

// TestParseMCPs_MalformedScope — settings.json with mcpServers as a string (not object).
// Design choice: this returns an error rather than silently skipping, because the value
// type is wrong — it signals a corrupt or incompatible file, not a missing key.
func TestParseMCPs_MalformedScope(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"mcpServers": "this-is-not-an-object",
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	_, err := ParseMCPs(raw)
	if err == nil {
		t.Fatal("expected error for mcpServers as string, got nil")
	}
}

// TestParseMCPs_ToolsArray — entry with explicit tools array; captured correctly.
func TestParseMCPs_ToolsArray(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"tooled-mcp": map[string]any{
				"command": "/bin/tooled",
				"tools":   []string{"read_file", "write_file", "search"},
			},
		},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if len(e.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %v", e.Tools)
	}
	want := []string{"read_file", "write_file", "search"}
	for i, w := range want {
		if e.Tools[i] != w {
			t.Errorf("Tools[%d]: got %q, want %q", i, e.Tools[i], w)
		}
	}
}

// TestParseMCPs_TypeUnknown — entry has neither command nor url; type is "unknown".
func TestParseMCPs_TypeUnknown(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeSettings(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"mystery-mcp": map[string]any{
				"someOtherField": "some-value",
			},
		},
	})

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{SettingsPath: settingsPath},
	}

	entries, err := ParseMCPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Type != "unknown" {
		t.Errorf("Type: got %q, want unknown", e.Type)
	}
	if e.Command != "" {
		t.Errorf("Command should be empty, got %q", e.Command)
	}
	if e.URL != "" {
		t.Errorf("URL should be empty, got %q", e.URL)
	}
}
