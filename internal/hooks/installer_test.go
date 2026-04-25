package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// ── hardening tests (H2, M1, M4, L1/L2) ─────────────────────────────────────

// TestInstall_BakRotationOnChange verifies H2: when the existing .bak differs
// from the current settings.json, the old .bak is rotated to .bak.<ts> before
// a new .bak is written, so the pre-first-CCMC state is never silently destroyed.
func TestInstall_BakRotationOnChange(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")
	bakPath := o.SettingsPath + ".bak"

	// Plant an initial settings.json with content A.
	contentA := []byte(`{"first": true}`)
	if err := os.WriteFile(o.SettingsPath, contentA, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a pre-existing .bak whose content differs from contentA (simulates
	// a .bak left from a prior run with a different settings state).
	oldBakContent := []byte(`{"original": true}`)
	if err := os.WriteFile(bakPath, oldBakContent, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// The old .bak must have been rotated to a timestamped path.
	entries, err := filepath.Glob(bakPath + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a rotated .bak.<ts> file but found none")
	}
	rotatedContent, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatalf("cannot read rotated bak: %v", err)
	}
	if string(rotatedContent) != string(oldBakContent) {
		t.Errorf("rotated bak content = %q, want %q", rotatedContent, oldBakContent)
	}

	// The new .bak must contain contentA (the pre-write state of this run).
	newBak, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf(".bak not written after rotation: %v", err)
	}
	if string(newBak) != string(contentA) {
		t.Errorf(".bak after rotation = %q, want %q", newBak, contentA)
	}
}

// TestInstall_BakNoRotationWhenIdentical verifies H2 no-rotation branch:
// when the existing .bak already matches settings.json, no .bak.<ts> is
// created — no information is lost by overwriting an identical backup.
func TestInstall_BakNoRotationWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")
	bakPath := o.SettingsPath + ".bak"

	content := []byte(`{"same": true}`)
	if err := os.WriteFile(o.SettingsPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a .bak that is byte-for-byte equal to settings.json.
	if err := os.WriteFile(bakPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// No rotated backup should exist.
	entries, err := filepath.Glob(bakPath + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("unexpected rotated bak files: %v", entries)
	}
}

// TestInstall_SymlinkAtSettingsPath verifies M1: a symlink planted at
// settingsPath is rejected before any I/O occurs, and the symlink target is
// left untouched.
func TestInstall_SymlinkAtSettingsPath(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// Create an unrelated target file.
	target := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(target, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink at settingsPath pointing at the target.
	if err := os.Symlink(target, o.SettingsPath); err != nil {
		t.Fatal(err)
	}

	err := Install(o)
	if err == nil {
		t.Fatal("expected error when settings.json is a symlink, got nil")
	}

	// Target must be untouched.
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("target disappeared: %v", readErr)
	}
	if string(got) != "sensitive" {
		t.Errorf("target content changed: %q", got)
	}

	// No .bak should have been created.
	if _, statErr := os.Lstat(o.SettingsPath + ".bak"); statErr == nil {
		t.Error(".bak was created despite symlink at settingsPath")
	}
}

// TestInstall_SymlinkAtBakPath verifies M1: a symlink at the .bak path is
// caught after the settings read but before the backup write, leaving the
// original settings.json untouched.
func TestInstall_SymlinkAtBakPath(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")
	bakPath := o.SettingsPath + ".bak"

	// Write a real settings.json so Install proceeds past the read phase.
	initial := []byte(`{"verbose": true}`)
	if err := os.WriteFile(o.SettingsPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create an unrelated file and plant a symlink at bakPath pointing to it.
	target := filepath.Join(dir, "bak-target.txt")
	if err := os.WriteFile(target, []byte("should-not-change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, bakPath); err != nil {
		t.Fatal(err)
	}

	err := Install(o)
	if err == nil {
		t.Fatal("expected error when .bak is a symlink, got nil")
	}

	// Target file must be untouched.
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("target disappeared: %v", readErr)
	}
	if string(got) != "should-not-change" {
		t.Errorf("target content changed: %q", got)
	}

	// Original settings.json must be byte-for-byte intact.
	curr, readErr := os.ReadFile(o.SettingsPath)
	if readErr != nil {
		t.Fatalf("settings.json disappeared: %v", readErr)
	}
	if string(curr) != string(initial) {
		t.Errorf("settings.json modified despite symlink at bakPath: got %q", curr)
	}
}

// TestInstall_StaleTempsRemoved verifies M4: temp files older than 60 seconds
// are cleaned up at the top of Install.
func TestInstall_StaleTempsRemoved(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// Plant a stale temp file with mtime 5 minutes in the past.
	stale := filepath.Join(dir, ".ccmc-install-stale")
	if err := os.WriteFile(stale, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(stale, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Error("stale temp file was not removed by Install")
	}
}

// TestInstall_FreshTempNotRemoved verifies M4: temp files with a recent mtime
// (< 60 s) are left alone — they may belong to a concurrent install.
func TestInstall_FreshTempNotRemoved(t *testing.T) {
	dir := t.TempDir()
	o := opts(dir, "settings.json")

	// Plant a fresh temp file (current mtime — well within the 60 s window).
	fresh := filepath.Join(dir, ".ccmc-install-fresh")
	if err := os.WriteFile(fresh, []byte("in-flight"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(o); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("fresh temp file was incorrectly removed: %v", err)
	}
}

// TestInstall_ShellQuoteSocketPath verifies L1/L2: a socketPath containing
// shell metacharacters is properly single-quoted in the generated command so
// that /bin/sh -c would not execute injected code. We assert on the command
// string directly — we do not shell-execute the command in the test.
func TestInstall_ShellQuoteSocketPath(t *testing.T) {
	cases := []struct {
		name       string
		socketPath string
		// wantSubstr is a substring that must appear in the built command, proving
		// the metacharacter is inside single-quotes rather than raw on the command line.
		wantSubstr string
	}{
		{
			name:       "semicolon in path",
			socketPath: "/tmp/foo;rm -rf /",
			// After quoting the whole path is wrapped in '...', so the semicolon
			// is inside the single-quoted token.
			wantSubstr: "'/tmp/foo;rm -rf /'",
		},
		{
			name:       "dollar sign in path",
			socketPath: "/tmp/$HOME/ccmc.sock",
			wantSubstr: "'/tmp/$HOME/ccmc.sock'",
		},
		{
			name:       "single quote in path",
			socketPath: "/tmp/o'malley/ccmc.sock",
			// shellQuote escapes ' as '\'' — verify the output contains the escaped form.
			wantSubstr: `'/tmp/o'\''malley/ccmc.sock'`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildCommand("SessionStart", tc.socketPath)
			if !strings.Contains(cmd, tc.wantSubstr) {
				t.Errorf("command %q does not contain expected quoted substring %q", cmd, tc.wantSubstr)
			}
		})
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
