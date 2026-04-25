package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ── ReadSettings ─────────────────────────────────────────────────────────────

func TestReadSettings_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// File does not exist — must return empty map, no error.
	m, err := ReadSettings(path)
	if err != nil {
		t.Fatalf("ReadSettings returned error for missing file: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestReadSettings_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	content := `{"verbose": true, "env": {"FOO": "bar"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := ReadSettings(path)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}

	if _, ok := m["verbose"]; !ok {
		t.Error("expected 'verbose' key in result")
	}
	if _, ok := m["env"]; !ok {
		t.Error("expected 'env' key in result")
	}

	// Verify round-trip on a nested value.
	var envMap map[string]string
	if err := json.Unmarshal(m["env"], &envMap); err != nil {
		t.Fatalf("env unmarshal: %v", err)
	}
	if envMap["FOO"] != "bar" {
		t.Errorf("env[FOO] = %q, want %q", envMap["FOO"], "bar")
	}
}

func TestReadSettings_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if err := os.WriteFile(path, []byte(`{bad json`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadSettings(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestReadSettings_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")

	if err := os.WriteFile(target, []byte(`{"sensitive": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := ReadSettings(link)
	if err == nil {
		t.Fatal("expected error for symlink at path, got nil")
	}

	// Target must be untouched.
	b, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("target disappeared: %v", readErr)
	}
	if string(b) != `{"sensitive": true}` {
		t.Errorf("target content changed: %q", b)
	}
}

func TestReadSettings_NonObjectTopLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// A JSON array is valid JSON but not an object — Unmarshal into map must fail.
	if err := os.WriteFile(path, []byte(`[1,2,3]`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadSettings(path)
	if err == nil {
		t.Fatal("expected error for non-object top-level JSON, got nil")
	}
}

// ── WriteSettings ─────────────────────────────────────────────────────────────

func TestWriteSettings_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	settings := map[string]json.RawMessage{
		"foo": json.RawMessage(`"bar"`),
	}

	if err := WriteSettings(path, settings); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}

	// File must exist and be valid JSON.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	// No stale temp files left behind.
	entries, err := filepath.Glob(filepath.Join(dir, ".ccmc-settings-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("stale temp files found: %v", entries)
	}
}

func TestWriteSettings_BakRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	bakPath := path + ".bak"

	// Write an initial settings file.
	initial := []byte(`{"version": 1}`)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a .bak that differs from the current file — rotation must occur.
	oldBak := []byte(`{"original": true}`)
	if err := os.WriteFile(bakPath, oldBak, 0o600); err != nil {
		t.Fatal(err)
	}

	settings := map[string]json.RawMessage{"version": json.RawMessage(`2`)}
	if err := WriteSettings(path, settings); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}

	// A rotated .bak.<ts> must exist.
	rotated, err := filepath.Glob(bakPath + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) == 0 {
		t.Fatal("expected rotated .bak.<ts> file but found none")
	}
	rc, err := os.ReadFile(rotated[0])
	if err != nil {
		t.Fatalf("cannot read rotated bak: %v", err)
	}
	if string(rc) != string(oldBak) {
		t.Errorf("rotated bak = %q, want %q", rc, oldBak)
	}

	// The new .bak must contain the pre-write file contents.
	newBak, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf(".bak not found after write: %v", err)
	}
	if string(newBak) != string(initial) {
		t.Errorf(".bak = %q, want %q", newBak, initial)
	}
}

func TestWriteSettings_BakNoRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	bakPath := path + ".bak"

	content := []byte(`{"same": true}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a .bak identical to settings.json — no rotation should occur.
	if err := os.WriteFile(bakPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	settings := map[string]json.RawMessage{"new": json.RawMessage(`true`)}
	if err := WriteSettings(path, settings); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}

	rotated, err := filepath.Glob(bakPath + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) != 0 {
		t.Errorf("unexpected rotated bak files: %v", rotated)
	}
}

func TestWriteSettings_SymlinkOnPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")

	if err := os.WriteFile(target, []byte(`{"sensitive": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	settings := map[string]json.RawMessage{"evil": json.RawMessage(`true`)}
	err := WriteSettings(link, settings)
	if err == nil {
		t.Fatal("expected error for symlink at path, got nil")
	}

	// Target must be untouched.
	b, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("target disappeared: %v", readErr)
	}
	if string(b) != `{"sensitive": true}` {
		t.Errorf("target modified: %q", b)
	}
}

func TestWriteSettings_SymlinkOnBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	bakPath := path + ".bak"

	initial := []byte(`{"verbose": true}`)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink at the .bak path.
	bakTarget := filepath.Join(dir, "bak-target.txt")
	if err := os.WriteFile(bakTarget, []byte("should-not-change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bakTarget, bakPath); err != nil {
		t.Fatal(err)
	}

	settings := map[string]json.RawMessage{"evil": json.RawMessage(`true`)}
	err := WriteSettings(path, settings)
	if err == nil {
		t.Fatal("expected error for symlink at .bak path, got nil")
	}

	// Bak target must be untouched.
	b, readErr := os.ReadFile(bakTarget)
	if readErr != nil {
		t.Fatalf("bak target disappeared: %v", readErr)
	}
	if string(b) != "should-not-change" {
		t.Errorf("bak target modified: %q", b)
	}

	// Original settings.json must be untouched.
	curr, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("settings.json disappeared: %v", readErr)
	}
	if string(curr) != string(initial) {
		t.Errorf("settings.json modified: %q", curr)
	}
}

func TestWriteSettings_AtomicOnFailure(t *testing.T) {
	dir := t.TempDir()

	// Create the directory writable first so we can place the initial file.
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(roDir, "settings.json")
	initial := []byte(`{"original": true}`)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	// Now make the directory read-only so CreateTemp inside it will fail.
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore permissions on exit so TempDir cleanup can remove the directory.
	defer os.Chmod(roDir, 0o755)

	settings := map[string]json.RawMessage{"new": json.RawMessage(`true`)}
	err := WriteSettings(path, settings)
	if err == nil {
		// Some environments (root) allow writes to mode-555 dirs. Skip rather than fail.
		t.Skip("write to read-only dir succeeded (likely running as root); skipping atomic test")
	}

	// Original must be byte-for-byte intact.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("settings.json disappeared: %v", readErr)
	}
	if string(got) != string(initial) {
		t.Errorf("settings.json modified despite failure: %q", got)
	}
}
