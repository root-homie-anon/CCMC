package inspector

import (
	"os"
	"path/filepath"
	"testing"
)

// makeSummary creates the full directory tree for a session-memory summary
// under fakeRoot/<projectDir>/<sessionID>/session-memory/summary.md.
func makeSummary(t *testing.T, fakeRoot, projectDir, sessionID, content string) {
	t.Helper()
	dir := filepath.Join(fakeRoot, projectDir, sessionID, "session-memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("makeSummary: MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "summary.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("makeSummary: WriteFile: %v", err)
	}
}

// TestReadMemorySummary_Missing verifies that a missing summary returns ("", nil).
func TestReadMemorySummary_Missing(t *testing.T) {
	root := t.TempDir()

	// Create a project dir but no session-memory inside it
	if err := os.MkdirAll(filepath.Join(root, "-Users-foo-bar"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMemorySummary(root, "abc123")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for missing file, got: %q", got)
	}
}

// TestReadMemorySummary_MissingProjectsDir verifies that a non-existent
// projectsDir is treated as missing, not as an error.
func TestReadMemorySummary_MissingProjectsDir(t *testing.T) {
	root := t.TempDir()
	noSuchDir := filepath.Join(root, "does-not-exist")

	got, err := ReadMemorySummary(noSuchDir, "abc123")
	if err != nil {
		t.Fatalf("expected nil error for missing projectsDir, got: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for missing projectsDir, got: %q", got)
	}
}

// TestReadMemorySummary_Present verifies that a present summary returns its contents.
func TestReadMemorySummary_Present(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-abc-001"
	wantContent := "# Session Summary\n\nWorking on the memory reader.\n"

	makeSummary(t, root, "-Users-macmini-projects-myapp", sessionID, wantContent)

	got, err := ReadMemorySummary(root, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantContent {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", got, wantContent)
	}
}

// TestReadMemorySummary_EncodedCWDLayout verifies resolution under the
// encoded-cwd layout (~/.claude/projects/<encoded-cwd>/<session-id>/...).
func TestReadMemorySummary_EncodedCWDLayout(t *testing.T) {
	root := t.TempDir()
	// Encoded cwd: /Users/macmini/projects/ccmc → -Users-macmini-projects-ccmc
	projectDir := "-Users-macmini-projects-ccmc"
	sessionID := "encoded-cwd-session-001"
	wantContent := "Encoded CWD layout summary."

	makeSummary(t, root, projectDir, sessionID, wantContent)

	got, err := ReadMemorySummary(root, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantContent {
		t.Fatalf("content mismatch: got %q, want %q", got, wantContent)
	}
}

// TestReadMemorySummary_HashedLayout verifies resolution under the hashed layout
// (~/.claude/projects/<hash>/<session-id>/...).
func TestReadMemorySummary_HashedLayout(t *testing.T) {
	root := t.TempDir()
	// Hashed layout: parent dir is an opaque hash, not a dash-encoded path
	projectDir := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	sessionID := "hashed-layout-session-001"
	wantContent := "Hashed layout summary."

	makeSummary(t, root, projectDir, sessionID, wantContent)

	got, err := ReadMemorySummary(root, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantContent {
		t.Fatalf("content mismatch: got %q, want %q", got, wantContent)
	}
}

// TestReadMemorySummary_MultipleProjects verifies that when multiple project
// directories exist, the correct session summary is returned and the function
// does not confuse sessions across projects.
func TestReadMemorySummary_MultipleProjects(t *testing.T) {
	root := t.TempDir()

	// Project A: encoded-cwd layout, contains targetSession
	makeSummary(t, root, "-Users-alice-projectA", "target-session", "Summary for target.")
	// Project B: hashed layout, contains a different session
	makeSummary(t, root, "deadbeefdeadbeef", "other-session", "Summary for other.")

	got, err := ReadMemorySummary(root, "target-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Summary for target." {
		t.Fatalf("got wrong summary: %q", got)
	}

	got2, err := ReadMemorySummary(root, "other-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2 != "Summary for other." {
		t.Fatalf("got wrong summary: %q", got2)
	}
}

// TestReadMemorySummary_SessionIDNotFound verifies that requesting a sessionID
// that exists in no project dir returns ("", nil) even when other sessions exist.
func TestReadMemorySummary_SessionIDNotFound(t *testing.T) {
	root := t.TempDir()
	makeSummary(t, root, "-Users-bob-repo", "existing-session", "Some content.")

	got, err := ReadMemorySummary(root, "nonexistent-session")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string, got: %q", got)
	}
}
