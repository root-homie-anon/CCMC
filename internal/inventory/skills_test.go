package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ccmc/pkg/ccmc"
)

// writeFile creates a file at path (and any needed parent dirs) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeFile: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: write %s: %v", path, err)
	}
}

// rawWithGlobalSkill constructs an InventoryRaw with a single global skill path.
func rawWithGlobalSkill(path string) ccmc.InventoryRaw {
	return ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Skills: []string{path}},
	}
}

// rawWithGlobalCommand constructs an InventoryRaw with a single global command path.
func rawWithGlobalCommand(path string) ccmc.InventoryRaw {
	return ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Commands: []string{path}},
	}
}

// --- ParseSkills tests ---

func TestParseSkills_Empty(t *testing.T) {
	raw := ccmc.InventoryRaw{}
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected empty slice, got %v", skills)
	}
}

func TestParseSkills_GlobalSingle(t *testing.T) {
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "skills", "my-skill", "SKILL.md")
	writeFile(t, skillMD, `---
name: my-skill
description: Does something useful.
user-invocable: true
disable-model-invocation: false
---

# My Skill
`)

	raw := rawWithGlobalSkill(skillMD)
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "my-skill" {
		t.Errorf("Name: got %q, want %q", s.Name, "my-skill")
	}
	if s.Path != skillMD {
		t.Errorf("Path: got %q, want %q", s.Path, skillMD)
	}
	if s.Scope != "global" {
		t.Errorf("Scope: got %q, want %q", s.Scope, "global")
	}
	if s.Description != "Does something useful." {
		t.Errorf("Description: got %q", s.Description)
	}
	if !s.UserInvocable {
		t.Errorf("UserInvocable: expected true")
	}
	if s.DisableModelInvocation {
		t.Errorf("DisableModelInvocation: expected false")
	}
}

func TestParseSkills_NameFromBasename(t *testing.T) {
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "skills", "cool-tool", "SKILL.md")
	// Frontmatter present but no "name" key.
	writeFile(t, skillMD, `---
description: A skill without a name field.
---
`)

	raw := rawWithGlobalSkill(skillMD)
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "cool-tool" {
		t.Errorf("Name: got %q, want %q", skills[0].Name, "cool-tool")
	}
}

func TestParseSkills_BoolFlags(t *testing.T) {
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "skills", "flagged", "SKILL.md")
	writeFile(t, skillMD, `---
name: flagged
user-invocable: true
disable-model-invocation: true
---
`)

	raw := rawWithGlobalSkill(skillMD)
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if !s.UserInvocable {
		t.Errorf("UserInvocable: expected true")
	}
	if !s.DisableModelInvocation {
		t.Errorf("DisableModelInvocation: expected true")
	}
}

func TestParseSkills_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "skills", "bare", "SKILL.md")
	// File has no --- markers.
	writeFile(t, skillMD, "# Just a markdown file\nNo frontmatter here.\n")

	raw := rawWithGlobalSkill(skillMD)
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("scan should succeed even with no frontmatter, got: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected skill to be skipped, got %v", skills)
	}
}

func TestParseSkills_TwoProjects(t *testing.T) {
	dir := t.TempDir()

	// Global skill.
	globalMD := filepath.Join(dir, "global", "skills", "g-skill", "SKILL.md")
	writeFile(t, globalMD, "---\nname: g-skill\n---\n")

	// Project A skill.
	projAMD := filepath.Join(dir, "projA", "skills", "a-skill", "SKILL.md")
	writeFile(t, projAMD, "---\nname: a-skill\n---\n")

	// Project B skill.
	projBMD := filepath.Join(dir, "projB", "skills", "b-skill", "SKILL.md")
	writeFile(t, projBMD, "---\nname: b-skill\n---\n")

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Skills: []string{globalMD}},
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: "/path/to/projA", Skills: []string{projAMD}},
			{ProjectPath: "/path/to/projB", Skills: []string{projBMD}},
		},
	}

	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d: %v", len(skills), skills)
	}

	// Global must be first.
	if skills[0].Scope != "global" {
		t.Errorf("first skill should be global, got scope %q", skills[0].Scope)
	}
	// Then projA, then projB (sorted by ProjectPath).
	if skills[1].Scope != "/path/to/projA" {
		t.Errorf("second skill should be projA, got scope %q", skills[1].Scope)
	}
	if skills[2].Scope != "/path/to/projB" {
		t.Errorf("third skill should be projB, got scope %q", skills[2].Scope)
	}
}

func TestParseSkills_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "skills", "broken", "SKILL.md")
	// Opening marker present but YAML is invalid (indentation error / tab character).
	writeFile(t, skillMD, "---\nname: broken\n\tinvalid: yaml: here\n---\n")

	raw := rawWithGlobalSkill(skillMD)
	skills, err := ParseSkills(raw)
	if err != nil {
		t.Fatalf("scan should succeed even with malformed YAML, got: %v", err)
	}
	// The malformed skill must be skipped.
	for _, s := range skills {
		if strings.Contains(s.Path, "broken") {
			t.Errorf("malformed skill should have been skipped, but got entry: %+v", s)
		}
	}
}

// --- ParseCommands tests ---

func TestParseCommands_Empty(t *testing.T) {
	raw := ccmc.InventoryRaw{}
	cmds, err := ParseCommands(raw)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected empty slice, got %v", cmds)
	}
}

func TestParseCommands_GlobalSingle(t *testing.T) {
	dir := t.TempDir()
	cmdMD := filepath.Join(dir, "commands", "deploy.md")
	writeFile(t, cmdMD, `---
name: deploy
description: Run the deployment pipeline.
---

## Steps
`)

	raw := rawWithGlobalCommand(cmdMD)
	cmds, err := ParseCommands(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	c := cmds[0]
	if c.Name != "deploy" {
		t.Errorf("Name: got %q, want %q", c.Name, "deploy")
	}
	if c.Path != cmdMD {
		t.Errorf("Path: got %q, want %q", c.Path, cmdMD)
	}
	if c.Scope != "global" {
		t.Errorf("Scope: got %q, want %q", c.Scope, "global")
	}
	if c.Description != "Run the deployment pipeline." {
		t.Errorf("Description: got %q", c.Description)
	}
}

func TestParseCommands_NameFromFilename(t *testing.T) {
	dir := t.TempDir()
	cmdMD := filepath.Join(dir, "commands", "bar.md")
	// Frontmatter present but no "name" key.
	writeFile(t, cmdMD, "---\ndescription: A command without a name.\n---\n")

	raw := rawWithGlobalCommand(cmdMD)
	cmds, err := ParseCommands(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "bar" {
		t.Errorf("Name: got %q, want %q", cmds[0].Name, "bar")
	}
}

func TestParseCommands_TwoProjects(t *testing.T) {
	dir := t.TempDir()

	globalMD := filepath.Join(dir, "global", "commands", "gc.md")
	writeFile(t, globalMD, "---\nname: gc\n---\n")

	projAMD := filepath.Join(dir, "projA", "commands", "pa.md")
	writeFile(t, projAMD, "---\nname: pa\n---\n")

	projBMD := filepath.Join(dir, "projB", "commands", "pb.md")
	writeFile(t, projBMD, "---\nname: pb\n---\n")

	raw := ccmc.InventoryRaw{
		Global: ccmc.ScopeFiles{Commands: []string{globalMD}},
		Projects: []ccmc.ScopeFiles{
			{ProjectPath: "/path/to/projA", Commands: []string{projAMD}},
			{ProjectPath: "/path/to/projB", Commands: []string{projBMD}},
		},
	}

	cmds, err := ParseCommands(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d: %v", len(cmds), cmds)
	}
	if cmds[0].Scope != "global" {
		t.Errorf("first command should be global, got scope %q", cmds[0].Scope)
	}
	if cmds[1].Scope != "/path/to/projA" {
		t.Errorf("second command should be projA, got scope %q", cmds[1].Scope)
	}
	if cmds[2].Scope != "/path/to/projB" {
		t.Errorf("third command should be projB, got scope %q", cmds[2].Scope)
	}
}
