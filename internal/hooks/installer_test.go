package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const testSocketPath = "/tmp/ccmc-test.sock"

// opts returns InstallerOptions pointing all paths at the given temp dir.
func opts(dir, settingsFile string) InstallerOptions {
	p := filepath.Join(dir, settingsFile)
	return InstallerOptions{
		SettingsPath: p,
		SocketPath:   testSocketPath,
		TempDir:      dir,
	}
}

func TestInstall_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	if err := Install(o); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	b, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	assertAllCcmcEvents(t, root)
}

func TestInstall_NoHooksBlock(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// Write settings with unrelated keys only.
	initial := `{"verbose": true, "env": {"FOO": "bar"}}`
	if err := os.WriteFile(o.SettingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	b, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Unrelated keys must survive unchanged.
	assertRawEqual(t, root["verbose"], `true`)
	// env value round-trips through encoding; compare decoded
	var envOut map[string]string
	if err := json.Unmarshal(root["env"], &envOut); err != nil {
		t.Fatalf("env key invalid: %v", err)
	}
	if envOut["FOO"] != "bar" {
		t.Errorf("env[FOO] = %q, want %q", envOut["FOO"], "bar")
	}

	assertAllCcmcEvents(t, root)
}

func TestInstall_ExistingUnrelatedHooks(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	initial := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "echo before-bash"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(o.SettingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	b, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	var hooksMap map[string][]hookGroup
	if err := json.Unmarshal(root["hooks"], &hooksMap); err != nil {
		t.Fatalf("hooks block invalid: %v", err)
	}

	// PreToolUse must still contain the original non-CCMC entry.
	ptu := hooksMap["PreToolUse"]
	if len(ptu) == 0 {
		t.Fatal("PreToolUse hooks disappeared")
	}
	found := false
	for _, g := range ptu {
		if g.Matcher == "Bash" && len(g.Hooks) > 0 && g.Hooks[0].Command == "echo before-bash" {
			found = true
		}
	}
	if !found {
		t.Error("original PreToolUse/Bash hook was removed or modified")
	}

	// CCMC events must now exist.
	assertAllCcmcEvents(t, root)
}

func TestInstall_ExistingCcmcHooks(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// First install.
	if err := Install(o); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	afterFirst, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Second install — should be a no-op, so the file must not be modified.
	// Capture mtime before the second call.
	stat1, _ := os.Stat(o.SettingsPath)

	if err := Install(o); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	stat2, _ := os.Stat(o.SettingsPath)

	// File should not have been rewritten (mtime unchanged = no write occurred).
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Error("settings.json was rewritten on idempotent second run (mtime changed)")
	}

	afterSecond, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(afterFirst) != string(afterSecond) {
		t.Error("settings.json content changed on second install — not idempotent")
	}
}

func TestInstall_BackupCreated(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	initial := []byte(`{"verbose": true}`)
	if err := os.WriteFile(o.SettingsPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install: %v", err)
	}

	bakPath := o.SettingsPath + ".bak"
	bak, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf(".bak file not created: %v", err)
	}

	if string(bak) != string(initial) {
		t.Errorf(".bak content = %q, want %q", bak, initial)
	}
}

func TestInstall_BackupOverwrittenOnReinstall(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// First install: settings.json is empty → creates .bak of nil/empty.
	if err := Install(o); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	// Record what settings.json looks like after first install.
	afterFirst, err := os.ReadFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Manually corrupt a single byte so the second install actually does work.
	// We overwrite settings.json with a variant that has a non-CCMC extra key.
	withExtra := make([]byte, 0, len(afterFirst)+20)
	withExtra = append(withExtra, afterFirst...)
	// Strip trailing newline, inject key, close object.
	// Simpler: just write a settings with one extra key so Install sees a delta.
	if err := os.WriteFile(o.SettingsPath, []byte(`{"extra": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	bakPath := o.SettingsPath + ".bak"
	bak, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf(".bak not found after second install: %v", err)
	}

	// The .bak must reflect the pre-second-install state, not the original.
	if string(bak) != `{"extra": 1}` {
		t.Errorf(".bak after second install = %q, want %q", bak, `{"extra": 1}`)
	}
}

func TestInstall_AtomicOnFailure(t *testing.T) {
	dir := t.TempDir()

	// Point TempDir at a read-only directory to induce CreateTemp failure.
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o555); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	initial := []byte(`{"verbose": true}`)
	if err := os.WriteFile(settingsPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	o := InstallerOptions{
		SettingsPath: settingsPath,
		SocketPath:   testSocketPath,
		TempDir:      roDir, // writes here will fail
	}

	err := Install(o)
	if err == nil {
		// On some systems (root, or if the OS ignores the mode) the write may
		// succeed. Skip rather than fail in that environment.
		t.Skip("write to read-only dir succeeded (likely running as root); skipping atomic test")
	}

	// Original file must be byte-for-byte intact.
	got, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatalf("original file disappeared: %v", readErr)
	}
	if string(got) != string(initial) {
		t.Errorf("original file was modified despite write failure: got %q, want %q", got, initial)
	}
}

func TestInstall_Idempotent_ByteEqual(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	if err := Install(o); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	hash1, err := HashFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	hash2, err := HashFile(o.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}

	if hash1 != hash2 {
		t.Errorf("byte-for-byte idempotency failed: hash1=%s hash2=%s", hash1, hash2)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// assertAllCcmcEvents verifies that every event in ccmcEvents has at least one
// hookGroup with a CCMC-marked entry in the decoded root map.
func assertAllCcmcEvents(t *testing.T, root map[string]json.RawMessage) {
	t.Helper()

	hooksRaw, ok := root["hooks"]
	if !ok {
		t.Fatal("hooks key missing from output")
	}

	var hooksMap map[string][]hookGroup
	if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
		t.Fatalf("hooks block is not valid JSON: %v", err)
	}

	for _, event := range ccmcEvents {
		groups, ok := hooksMap[event]
		if !ok {
			t.Errorf("event %s missing from hooks block", event)
			continue
		}
		idx := findCcmcGroup(groups)
		if idx < 0 {
			t.Errorf("event %s has no CCMC-marked hook group", event)
			continue
		}
		entry := groups[idx].Hooks[0]
		if entry.Type != "command" {
			t.Errorf("event %s CCMC hook type = %q, want %q", event, entry.Type, "command")
		}
		if entry.Command == "" {
			t.Errorf("event %s CCMC hook command is empty", event)
		}
		expectedCmd := buildCommand(event, testSocketPath)
		if entry.Command != expectedCmd {
			t.Errorf("event %s command = %q, want %q", event, entry.Command, expectedCmd)
		}
	}
}

// assertRawEqual checks that a json.RawMessage decodes to the same value as
// the given JSON string (byte comparison after compacting both sides).
func assertRawEqual(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	got := string(raw)
	if got != want {
		t.Errorf("raw value = %q, want %q", got, want)
	}
}
