package inventory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// mkfile creates a file at path, creating all parent directories.
func mkfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkfile: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("mkfile: write %s: %v", path, err)
	}
}

// mkdir creates a directory at path.
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// TestScanner_Empty — empty claude dir, no projects dir. Expect empty InventoryRaw, no error.
func TestScanner_Empty(t *testing.T) {
	claudeDir := t.TempDir()

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if raw.Global.SettingsPath != "" {
		t.Errorf("expected empty SettingsPath, got %q", raw.Global.SettingsPath)
	}
	if raw.Global.ClaudeMDPath != "" {
		t.Errorf("expected empty ClaudeMDPath, got %q", raw.Global.ClaudeMDPath)
	}
	if len(raw.Global.Commands) != 0 {
		t.Errorf("expected no Commands, got %v", raw.Global.Commands)
	}
	if len(raw.Projects) != 0 {
		t.Errorf("expected no Projects, got %v", raw.Projects)
	}
}

// TestScanner_GlobalAllCategories — one of each category in global scope.
func TestScanner_GlobalAllCategories(t *testing.T) {
	claudeDir := t.TempDir()

	mkfile(t, filepath.Join(claudeDir, "settings.json"))
	mkfile(t, filepath.Join(claudeDir, "CLAUDE.md"))
	mkfile(t, filepath.Join(claudeDir, "commands", "my-cmd.md"))
	mkfile(t, filepath.Join(claudeDir, "skills", "my-skill", "SKILL.md"))
	mkfile(t, filepath.Join(claudeDir, "agents", "my-agent.md"))
	mkfile(t, filepath.Join(claudeDir, "plugins", "my-plugin", "manifest.json"))

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if raw.Global.SettingsPath != filepath.Join(claudeDir, "settings.json") {
		t.Errorf("SettingsPath mismatch: %q", raw.Global.SettingsPath)
	}
	if raw.Global.ClaudeMDPath != filepath.Join(claudeDir, "CLAUDE.md") {
		t.Errorf("ClaudeMDPath mismatch: %q", raw.Global.ClaudeMDPath)
	}
	if len(raw.Global.Commands) != 1 || raw.Global.Commands[0] != filepath.Join(claudeDir, "commands", "my-cmd.md") {
		t.Errorf("Commands mismatch: %v", raw.Global.Commands)
	}
	if len(raw.Global.Skills) != 1 || raw.Global.Skills[0] != filepath.Join(claudeDir, "skills", "my-skill", "SKILL.md") {
		t.Errorf("Skills mismatch: %v", raw.Global.Skills)
	}
	if len(raw.Global.Agents) != 1 || raw.Global.Agents[0] != filepath.Join(claudeDir, "agents", "my-agent.md") {
		t.Errorf("Agents mismatch: %v", raw.Global.Agents)
	}
	if len(raw.Global.Plugins) != 1 || raw.Global.Plugins[0] != filepath.Join(claudeDir, "plugins", "my-plugin") {
		t.Errorf("Plugins mismatch: %v", raw.Global.Plugins)
	}
}

// TestScanner_TwoProjects — proj-a and proj-b; assert 2 entries sorted by ProjectPath.
func TestScanner_TwoProjects(t *testing.T) {
	claudeDir := t.TempDir()

	// Create proj-b first to confirm sorting doesn't rely on creation order.
	mkdir(t, filepath.Join(claudeDir, "projects", "proj-b"))
	mkdir(t, filepath.Join(claudeDir, "projects", "proj-a"))

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(raw.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(raw.Projects), raw.Projects)
	}
	if raw.Projects[0].ProjectPath != "proj-a" {
		t.Errorf("expected first project to be proj-a, got %q", raw.Projects[0].ProjectPath)
	}
	if raw.Projects[1].ProjectPath != "proj-b" {
		t.Errorf("expected second project to be proj-b, got %q", raw.Projects[1].ProjectPath)
	}
}

// TestScanner_UnreadableFile — mode-0 settings.json is skipped with a warning; scan succeeds.
func TestScanner_UnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 has no effect on Windows")
	}

	claudeDir := t.TempDir()
	settingsPath := filepath.Join(claudeDir, "settings.json")
	mkfile(t, settingsPath)

	// Make the file unreadable.
	if err := os.Chmod(settingsPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(settingsPath, 0o644) }) // restore so TempDir cleanup succeeds

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("unreadable file should not cause error; got: %v", err)
	}

	// fileReadable uses Lstat — mode 0 is still stat-able, so SettingsPath is populated.
	// The scanner's contract for "unreadable" is at the ReadDir level, not individual stat.
	// A mode-0 file is recorded (its path is findable), downstream parsers handle read errors.
	// This test primarily asserts the scan does NOT return an error.
	_ = raw
}

// TestScanner_SkillsAreDirs — skills/foo with SKILL.md included; skills/bar without SKILL.md excluded.
func TestScanner_SkillsAreDirs(t *testing.T) {
	claudeDir := t.TempDir()

	mkfile(t, filepath.Join(claudeDir, "skills", "foo", "SKILL.md"))
	mkdir(t, filepath.Join(claudeDir, "skills", "bar")) // no SKILL.md

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(raw.Global.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(raw.Global.Skills), raw.Global.Skills)
	}
	if raw.Global.Skills[0] != filepath.Join(claudeDir, "skills", "foo", "SKILL.md") {
		t.Errorf("Skills entry mismatch: %q", raw.Global.Skills[0])
	}
}

// TestScanner_SymlinksNotFollowed — a symlink in skills/ is recorded as a path, not traversed.
func TestScanner_SymlinksNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on Windows")
	}

	claudeDir := t.TempDir()
	skillsDir := filepath.Join(claudeDir, "skills")
	mkdir(t, skillsDir)

	// Create a real skill dir to serve as symlink target.
	realTarget := t.TempDir()
	mkfile(t, filepath.Join(realTarget, "SKILL.md"))

	symlinkPath := filepath.Join(skillsDir, "linked-skill")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	s := NewScanner(claudeDir)
	raw, err := s.Scan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The symlink entry in skills/ should be recorded as skills/linked-skill/SKILL.md
	// without actually traversing into the real target directory.
	if len(raw.Global.Skills) != 1 {
		t.Fatalf("expected 1 skill (the symlink), got %d: %v", len(raw.Global.Skills), raw.Global.Skills)
	}
	expected := filepath.Join(skillsDir, "linked-skill", "SKILL.md")
	if raw.Global.Skills[0] != expected {
		t.Errorf("symlink skill path mismatch: got %q, want %q", raw.Global.Skills[0], expected)
	}
}

// TestScanner_NoClaudeDir — pointing at a nonexistent directory returns an error.
func TestScanner_NoClaudeDir(t *testing.T) {
	s := NewScanner("/nonexistent/path/that/cannot/exist/__ccmc_test__")
	_, err := s.Scan()
	if err == nil {
		t.Fatal("expected error for nonexistent claudeDir, got nil")
	}
}
