package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadLastLine_SingleLine(t *testing.T) {
	f := writeTempFile(t, `{"timestamp":"2026-04-16T10:00:00Z","type":"user"}`)
	got, err := readLastLine(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"timestamp":"2026-04-16T10:00:00Z","type":"user"}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadLastLine_MultipleLines(t *testing.T) {
	content := `{"timestamp":"2026-04-16T09:00:00Z","type":"user"}
{"timestamp":"2026-04-16T10:00:00Z","type":"assistant"}
{"timestamp":"2026-04-16T11:00:00Z","type":"tool_use"}
`
	f := writeTempFile(t, content)
	got, err := readLastLine(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"timestamp":"2026-04-16T11:00:00Z","type":"tool_use"}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadLastLine_EmptyFile(t *testing.T) {
	f := writeTempFile(t, "")
	_, err := readLastLine(f)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestLastLineTimestamp(t *testing.T) {
	content := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
{"type":"assistant","timestamp":"2026-04-16T11:30:00Z"}
`
	f := writeTempFile(t, content)
	got, err := lastLineTimestamp(f)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 4, 16, 11, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanSessions_WithFixture(t *testing.T) {
	// Set up a mock ~/.claude/projects/ structure
	tmpDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", tmpDir)

	// Create an encoded project directory (simulating URL-encoded path)
	projDir := filepath.Join(tmpDir, "projects", "-Users-testuser-myproject")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a fixture JSONL
	jsonl := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
{"type":"assistant","timestamp":"2026-04-16T09:01:00Z"}
{"type":"tool_use","timestamp":"2026-04-16T09:02:00Z"}
`
	if err := os.WriteFile(filepath.Join(projDir, "abc123.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := ScanSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}

	s := sessions[0]
	if s.ID != "abc123" {
		t.Errorf("ID = %q, want abc123", s.ID)
	}
	if s.ProjectName != "myproject" {
		t.Errorf("ProjectName = %q, want myproject", s.ProjectName)
	}
	wantPath := "/Users/testuser/myproject"
	if s.ProjectPath != wantPath {
		t.Errorf("ProjectPath = %q, want %q", s.ProjectPath, wantPath)
	}
	wantTime := time.Date(2026, 4, 16, 9, 2, 0, 0, time.UTC)
	if !s.LastActivity.Equal(wantTime) {
		t.Errorf("LastActivity = %v, want %v", s.LastActivity, wantTime)
	}
}

func TestScanSessions_EmptyProjectsDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", tmpDir)

	// Create empty projects dir
	os.MkdirAll(filepath.Join(tmpDir, "projects"), 0o755)

	sessions, err := ScanSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

func TestScanSessions_NoProjectsDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", tmpDir)

	sessions, err := ScanSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions != nil {
		t.Errorf("got %v, want nil", sessions)
	}
}

func TestDecodeProjectDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"-Users-testuser-myproject", "/Users/testuser/myproject"},
		{"simple", "simple"},
		{"-Users-macmini-projects-ADForge", "/Users/macmini/projects/ADForge"},
		{"", ""},
	}
	for _, tt := range tests {
		got := decodeProjectDir(tt.input)
		if got != tt.want {
			t.Errorf("decodeProjectDir(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}
