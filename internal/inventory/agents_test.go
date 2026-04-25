package inventory

import (
	"os"
	"path/filepath"
	"testing"

	"ccmc/pkg/ccmc"
)

// writeAgent creates a file at dir/filename with the given content and returns its path.
func writeAgent(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeAgent: %v", err)
	}
	return path
}

func TestParseAgents_Empty(t *testing.T) {
	raw := ccmc.InventoryRaw{}
	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty slice, got %d entries", len(entries))
	}
}

func TestParseAgents_GlobalSingle(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "myagent.md", `---
name: myagent
description: Does something useful
model: sonnet
tools: [Read, Write]
---

# Body
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Name != "myagent" {
		t.Errorf("Name: got %q, want %q", e.Name, "myagent")
	}
	if e.Description != "Does something useful" {
		t.Errorf("Description: got %q", e.Description)
	}
	if e.Model != "sonnet" {
		t.Errorf("Model: got %q", e.Model)
	}
	if e.Scope != "global" {
		t.Errorf("Scope: got %q, want %q", e.Scope, "global")
	}
	if e.Path != agentPath {
		t.Errorf("Path: got %q, want %q", e.Path, agentPath)
	}
	if len(e.Tools) != 2 || e.Tools[0] != "Read" || e.Tools[1] != "Write" {
		t.Errorf("Tools: got %v, want [Read Write]", e.Tools)
	}
}

func TestParseAgents_NameFromFilename(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "agent.md", `---
description: No name in frontmatter
---
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "agent" {
		t.Errorf("Name: got %q, want %q", entries[0].Name, "agent")
	}
}

func TestParseAgents_ToolsArrayForm(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "arr.md", `---
name: arr
tools: [Read, Write]
---
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	tools := entries[0].Tools
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "Write" {
		t.Errorf("Tools: got %v, want [Read Write]", tools)
	}
}

func TestParseAgents_ToolsCommaForm(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "comma.md", `---
name: comma
tools: "Read, Write"
---
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	tools := entries[0].Tools
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "Write" {
		t.Errorf("Tools: got %v, want [Read Write]", tools)
	}
}

func TestParseAgents_ToolsMissing(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "notools.md", `---
name: notools
description: no tools field
---
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Tools != nil {
		t.Errorf("Tools: got %v, want nil", entries[0].Tools)
	}
}

func TestParseAgents_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "nofm.md", `# Just a heading

No frontmatter here.
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected file without frontmatter to be skipped, got %d entries", len(entries))
	}
}

func TestParseAgents_TwoProjects(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	pathA := writeAgent(t, dirA, "alpha.md", `---
name: alpha
---
`)
	pathB := writeAgent(t, dirB, "beta.md", `---
name: beta
---
`)

	// Deliberately put project "b" first in the slice — ParseAgents must respect the
	// InventoryRaw.Projects order (scanner already sorts ascending by ProjectPath).
	// We use fixed strings to guarantee predictable ordering in the assertion.
	raw := ccmc.InventoryRaw{
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: "/project/a", Agents: []string{pathA}},
			{ProjectPath: "/project/b", Agents: []string{pathB}},
		},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "alpha" {
		t.Errorf("entries[0].Name: got %q, want %q", entries[0].Name, "alpha")
	}
	if entries[1].Name != "beta" {
		t.Errorf("entries[1].Name: got %q, want %q", entries[1].Name, "beta")
	}
	if entries[0].Scope != "/project/a" {
		t.Errorf("entries[0].Scope: got %q, want %q", entries[0].Scope, "/project/a")
	}
}

func TestParseAgents_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	agentPath := writeAgent(t, dir, "bad.md", `---
name: [unclosed bracket
---
`)

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Agents: []string{agentPath}},
	}

	entries, err := ParseAgents(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected malformed YAML to be skipped, got %d entries", len(entries))
	}
}
