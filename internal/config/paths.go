package config

import (
	"os"
	"path/filepath"
)

// ClaudeDir returns the root Claude config directory.
// Respects CLAUDE_CONFIG_DIR env var; defaults to ~/.claude.
func ClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
}

// ClaudeSettingsPath returns the path to the global settings.json.
func ClaudeSettingsPath() string {
	return filepath.Join(ClaudeDir(), "settings.json")
}

// ClaudeProjectsDir returns the directory containing per-project session data.
func ClaudeProjectsDir() string {
	return filepath.Join(ClaudeDir(), "projects")
}

// ClaudeTodosDir returns the directory containing per-session todo JSON files.
func ClaudeTodosDir() string {
	return filepath.Join(ClaudeDir(), "todos")
}

// CcmcDir returns the CCMC-specific data directory.
// Respects CCMC_DIR env var; defaults to ~/.ccmc.
func CcmcDir() string {
	if d := os.Getenv("CCMC_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".ccmc")
}

// CcmcConfigPath returns the path to CCMC's own config file.
func CcmcConfigPath() string {
	return filepath.Join(CcmcDir(), "config.yaml")
}

// CcmcRegistryPath returns the path to the session registry JSON dump.
func CcmcRegistryPath() string {
	return filepath.Join(CcmcDir(), "registry.json")
}

// CcmcSocketPath returns the path to the daemon's unix socket.
func CcmcSocketPath() string {
	return filepath.Join(CcmcDir(), "ccmc.sock")
}

// CcmcDaemonPidPath returns the path to the daemon PID file.
func CcmcDaemonPidPath() string {
	return filepath.Join(CcmcDir(), "daemon.pid")
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return os.Getenv("HOME")
	}
	return h
}
