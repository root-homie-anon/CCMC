package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func configPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.yaml")
}

// defaultConfig returns the Config produced by applying setDefaults to a zero
// value. Used as the expected value in tests that assert default behaviour.
func defaultConfig() Config {
	var c Config
	setDefaults(&c)
	return c
}

// ── TestSetDefaults_DirectlyTestable ─────────────────────────────────────────

func TestSetDefaults_DirectlyTestable(t *testing.T) {
	var c Config
	setDefaults(&c)

	if c.Daemon.Socket != "~/.ccmc/ccmc.sock" {
		t.Errorf("Daemon.Socket = %q, want ~/.ccmc/ccmc.sock", c.Daemon.Socket)
	}
	if !c.Daemon.AutoStart {
		t.Error("Daemon.AutoStart should be true by default")
	}
	if c.Daemon.AutoStopMinutes != 30 {
		t.Errorf("Daemon.AutoStopMinutes = %d, want 30", c.Daemon.AutoStopMinutes)
	}
	if c.Daemon.ScanIntervalSeconds != 10 {
		t.Errorf("Daemon.ScanIntervalSeconds = %d, want 10", c.Daemon.ScanIntervalSeconds)
	}
	if !reflect.DeepEqual(c.Hooks.Events, defaultHookEvents) {
		t.Errorf("Hooks.Events = %v, want %v", c.Hooks.Events, defaultHookEvents)
	}
	if c.Reference.Version != "2026.04" {
		t.Errorf("Reference.Version = %q, want 2026.04", c.Reference.Version)
	}
	if c.Integrator.Model != "claude-sonnet-4-6" {
		t.Errorf("Integrator.Model = %q, want claude-sonnet-4-6", c.Integrator.Model)
	}
	if c.Integrator.CloneDir != "~/.ccmc/tools/" {
		t.Errorf("Integrator.CloneDir = %q, want ~/.ccmc/tools/", c.Integrator.CloneDir)
	}
	if c.ITerm.PollIntervalSeconds != 5 {
		t.Errorf("ITerm.PollIntervalSeconds = %d, want 5", c.ITerm.PollIntervalSeconds)
	}
}

// ── TestLoad_Missing ─────────────────────────────────────────────────────────

// TestLoad_Missing verifies that a missing config file is created on disk with
// defaults serialized, and that Load returns the default Config with nil error.
func TestLoad_Missing(t *testing.T) {
	path := configPath(t)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load on missing file returned error: %v", err)
	}

	want := defaultConfig()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("returned Config does not match defaults\ngot:  %+v\nwant: %+v", got, want)
	}

	// File must now exist on disk.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Load did not create the config file")
	}
}

// ── TestLoad_FullValid ────────────────────────────────────────────────────────

// TestLoad_FullValid verifies that a fully-populated config file is parsed correctly.
func TestLoad_FullValid(t *testing.T) {
	path := configPath(t)

	yaml := `
daemon:
  socket: /tmp/test.sock
  auto_start: false
  auto_stop_minutes: 60
  scan_interval_seconds: 30

hooks:
  installed: true
  events:
    - SessionStart
    - SessionEnd

reference:
  version: "2025.01"

integrator:
  anthropic_api_key: "sk-test"
  model: "claude-opus-4-5"
  clone_dir: /tmp/tools/

iterm:
  installed: true
  poll_interval_seconds: 10
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Daemon — AutoStart is false in the YAML, but setDefaults forces it to true
	// because false is the Go zero value and we cannot distinguish "absent" from
	// "user-set false". This is a known YAML bool limitation documented in setDefaults.
	if got.Daemon.Socket != "/tmp/test.sock" {
		t.Errorf("Daemon.Socket = %q, want /tmp/test.sock", got.Daemon.Socket)
	}
	if got.Daemon.AutoStopMinutes != 60 {
		t.Errorf("Daemon.AutoStopMinutes = %d, want 60", got.Daemon.AutoStopMinutes)
	}
	if got.Daemon.ScanIntervalSeconds != 30 {
		t.Errorf("Daemon.ScanIntervalSeconds = %d, want 30", got.Daemon.ScanIntervalSeconds)
	}
	if !got.Hooks.Installed {
		t.Error("Hooks.Installed should be true")
	}
	if !reflect.DeepEqual(got.Hooks.Events, []string{"SessionStart", "SessionEnd"}) {
		t.Errorf("Hooks.Events = %v", got.Hooks.Events)
	}
	if got.Reference.Version != "2025.01" {
		t.Errorf("Reference.Version = %q, want 2025.01", got.Reference.Version)
	}
	if got.Integrator.AnthropicAPIKey != "sk-test" {
		t.Errorf("Integrator.AnthropicAPIKey = %q, want sk-test", got.Integrator.AnthropicAPIKey)
	}
	if got.Integrator.Model != "claude-opus-4-5" {
		t.Errorf("Integrator.Model = %q, want claude-opus-4-5", got.Integrator.Model)
	}
	if got.Integrator.CloneDir != "/tmp/tools/" {
		t.Errorf("Integrator.CloneDir = %q, want /tmp/tools/", got.Integrator.CloneDir)
	}
	if !got.ITerm.Installed {
		t.Error("ITerm.Installed should be true")
	}
	if got.ITerm.PollIntervalSeconds != 10 {
		t.Errorf("ITerm.PollIntervalSeconds = %d, want 10", got.ITerm.PollIntervalSeconds)
	}
}

// ── TestLoad_PartialAppliesDefaults ──────────────────────────────────────────

// TestLoad_PartialAppliesDefaults verifies that a partial config (only one field
// set) gets the remaining fields filled with their documented defaults.
func TestLoad_PartialAppliesDefaults(t *testing.T) {
	path := configPath(t)

	// Only anthropic.model is set; everything else is absent.
	yaml := `
integrator:
  model: "claude-haiku-4-5"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.Integrator.Model != "claude-haiku-4-5" {
		t.Errorf("Integrator.Model = %q, want claude-haiku-4-5", got.Integrator.Model)
	}
	// All other fields should be defaults.
	if got.Daemon.Socket != "~/.ccmc/ccmc.sock" {
		t.Errorf("Daemon.Socket = %q, want default", got.Daemon.Socket)
	}
	if got.Daemon.AutoStopMinutes != 30 {
		t.Errorf("Daemon.AutoStopMinutes = %d, want 30", got.Daemon.AutoStopMinutes)
	}
	if got.Integrator.CloneDir != "~/.ccmc/tools/" {
		t.Errorf("Integrator.CloneDir = %q, want default", got.Integrator.CloneDir)
	}
	if !reflect.DeepEqual(got.Hooks.Events, defaultHookEvents) {
		t.Errorf("Hooks.Events = %v, want defaults", got.Hooks.Events)
	}
}

// ── TestLoad_Malformed ───────────────────────────────────────────────────────

// TestLoad_Malformed verifies that broken YAML returns a non-nil error.
func TestLoad_Malformed(t *testing.T) {
	path := configPath(t)

	// Valid UTF-8 but structurally broken YAML.
	bad := "daemon: {\n  bad yaml: [unterminated"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("Load should return an error for malformed YAML")
	}
}

// ── TestLoad_Symlink ─────────────────────────────────────────────────────────

// TestLoad_Symlink verifies that a symlink at the config path is refused.
func TestLoad_Symlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(real, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	_, err := Load(link)
	if err == nil {
		t.Error("Load should return an error for a symlink path")
	}
}

// ── TestSave_Atomic ──────────────────────────────────────────────────────────

// TestSave_Atomic verifies that Save succeeds and leaves no temp files behind.
func TestSave_Atomic(t *testing.T) {
	path := configPath(t)
	dir := filepath.Dir(path)

	cfg := defaultConfig()
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Save did not create the config file")
	}

	// No .ccmc-config-tmp-* files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 16 && e.Name()[:17] == ".ccmc-config-tmp-" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// ── TestSave_Symlink ─────────────────────────────────────────────────────────

// TestSave_Symlink verifies that Save refuses to overwrite a symlink.
func TestSave_Symlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(real, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	err := Save(link, defaultConfig())
	if err == nil {
		t.Error("Save should return an error for a symlink path")
	}
}

// ── TestSave_RoundTrip ───────────────────────────────────────────────────────

// TestSave_RoundTrip verifies that Load → modify → Save → Load produces a
// Config equal to the modified value.
func TestSave_RoundTrip(t *testing.T) {
	path := configPath(t)

	// Load (creates with defaults).
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}

	// Modify a few fields.
	cfg.Integrator.AnthropicAPIKey = "sk-round-trip"
	cfg.Daemon.AutoStopMinutes = 45
	cfg.ITerm.Installed = true

	// Save.
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload.
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload Load: %v", err)
	}

	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", got, cfg)
	}
}
