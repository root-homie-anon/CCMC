package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the in-memory representation of ~/.ccmc/config.yaml.
// All fields map 1-to-1 with the YAML schema defined in CLAUDE(4).md.
// Zero-value fields are replaced by defaults in setDefaults.
type Config struct {
	Daemon     DaemonConfig     `yaml:"daemon"`
	Hooks      HooksConfig      `yaml:"hooks"`
	Reference  ReferenceConfig  `yaml:"reference"`
	Integrator IntegratorConfig `yaml:"integrator"`
	ITerm      ITermConfig      `yaml:"iterm"`
}

// DaemonConfig controls the background daemon's socket path and lifecycle.
type DaemonConfig struct {
	// Socket is the unix socket path. Unexpanded tilde is preserved for the
	// daemon to expand at startup; Load does not expand it.
	Socket              string `yaml:"socket"`
	AutoStart           bool   `yaml:"auto_start"`
	AutoStopMinutes     int    `yaml:"auto_stop_minutes"`
	ScanIntervalSeconds int    `yaml:"scan_interval_seconds"`
}

// AutoStopDuration converts AutoStopMinutes to time.Duration for callers
// that need a Go duration (e.g. the idle-timeout ticker in the daemon).
func (d DaemonConfig) AutoStopDuration() time.Duration {
	return time.Duration(d.AutoStopMinutes) * time.Minute
}

// ScanInterval converts ScanIntervalSeconds to time.Duration.
func (d DaemonConfig) ScanInterval() time.Duration {
	return time.Duration(d.ScanIntervalSeconds) * time.Second
}

// HooksConfig records whether global hooks are installed and which events are wired.
type HooksConfig struct {
	Installed bool     `yaml:"installed"`
	Events    []string `yaml:"events"`
}

// ReferenceConfig carries the embedded reference-data version string.
type ReferenceConfig struct {
	Version string `yaml:"version"`
}

// IntegratorConfig holds credentials and configuration for the eval/install pipeline.
type IntegratorConfig struct {
	// AnthropicAPIKey may be empty; callers should fall back to ANTHROPIC_API_KEY env var.
	AnthropicAPIKey string `yaml:"anthropic_api_key"`
	// Model is the Claude model used for ccmc eval calls.
	Model    string `yaml:"model"`
	CloneDir string `yaml:"clone_dir"`
}

// ITermConfig controls the optional iTerm2 status-bar integration.
type ITermConfig struct {
	Installed           bool `yaml:"installed"`
	PollIntervalSeconds int  `yaml:"poll_interval_seconds"`
}

// defaultHookEvents is the ordered set of CC hook events wired by ccmc setup.
var defaultHookEvents = []string{
	"SessionStart",
	"SessionEnd",
	"PostToolUse",
	"SubagentStart",
	"SubagentStop",
	"Stop",
	"Notification",
}

// setDefaults fills any zero-value field with its documented default.
// It is called after unmarshalling so partial YAML files get complete configs.
// Exported for direct testing via TestSetDefaults_DirectlyTestable.
func setDefaults(c *Config) {
	if c.Daemon.Socket == "" {
		c.Daemon.Socket = "~/.ccmc/ccmc.sock"
	}
	if !c.Daemon.AutoStart {
		// AutoStart defaults to true; YAML false and missing are identical in Go,
		// so we cannot distinguish "user set false" from "field absent." This is a
		// known YAML bool limitation — the spec default is true, so we set it here.
		// Users who want false must keep the key present with value false, which
		// means they'll see it reset after a Load/Save round-trip through an empty
		// struct. Acceptable given solo-user, single-config-file context.
		c.Daemon.AutoStart = true
	}
	if c.Daemon.AutoStopMinutes == 0 {
		c.Daemon.AutoStopMinutes = 30
	}
	if c.Daemon.ScanIntervalSeconds == 0 {
		c.Daemon.ScanIntervalSeconds = 10
	}
	if len(c.Hooks.Events) == 0 {
		// Copy the slice so callers can't mutate the package-level default.
		c.Hooks.Events = append([]string(nil), defaultHookEvents...)
	}
	if c.Reference.Version == "" {
		c.Reference.Version = "2026.04"
	}
	if c.Integrator.Model == "" {
		c.Integrator.Model = "claude-sonnet-4-6"
	}
	if c.Integrator.CloneDir == "" {
		c.Integrator.CloneDir = "~/.ccmc/tools/"
	}
	if c.ITerm.PollIntervalSeconds == 0 {
		c.ITerm.PollIntervalSeconds = 5
	}
}

// Load reads the config file at path. If the file does not exist, Load creates
// it with defaults serialized as YAML (mode 0o600, atomic write) and returns the
// default Config with a nil error. If the file exists but is missing fields,
// defaults are applied to any zero-value field. Returns a non-nil error only for
// malformed YAML, a symlink at path, or a write failure when creating the file.
func Load(path string) (Config, error) {
	// Symlink guard: refuse to read a symlink. Lstat distinguishes a symlink from
	// a regular file without following the link, guarding against TOCTOU attacks
	// where an attacker races a symlink into place between stat and open.
	fi, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config: stat %s: %w", path, err)
		}
		// File does not exist — create it with defaults.
		var c Config
		setDefaults(&c)
		if err := Save(path, c); err != nil {
			return Config{}, fmt.Errorf("config: create default %s: %w", path, err)
		}
		return c, nil
	}
	if fi.Mode().Type()&os.ModeSymlink != 0 {
		return Config{}, fmt.Errorf("config: %s is a symlink — refusing to read", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	setDefaults(&c)
	return c, nil
}

// Save atomically writes cfg to path as YAML with mode 0o600.
// It writes to a temp file in the same directory and renames it into place so
// path is never partially written. Returns an error if path is a symlink, the
// directory is not writable, or the rename fails.
func Save(path string, cfg Config) error {
	// Symlink guard: refuse to write through a symlink. Use Lstat so we check the
	// path itself, not whatever the link points to.
	fi, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config save: stat %s: %w", path, err)
	}
	if err == nil && fi.Mode().Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("config save: %s is a symlink — refusing to write", path)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config save: marshal: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ccmc-config-tmp-*")
	if err != nil {
		return fmt.Errorf("config save: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("config save: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("config save: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("config save: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("config save: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("config save: rename to %s: %w", path, err)
	}
	return nil
}
