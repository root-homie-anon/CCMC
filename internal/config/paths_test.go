package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeDir_Default(t *testing.T) {
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	got := ClaudeDir()
	if !strings.HasSuffix(got, filepath.Join(".claude")) {
		t.Errorf("ClaudeDir() = %q, want suffix .claude", got)
	}
}

func TestClaudeDir_EnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/test-claude")
	got := ClaudeDir()
	if got != "/tmp/test-claude" {
		t.Errorf("ClaudeDir() = %q, want /tmp/test-claude", got)
	}
}

func TestCcmcDir_Default(t *testing.T) {
	os.Unsetenv("CCMC_DIR")
	got := CcmcDir()
	if !strings.HasSuffix(got, filepath.Join(".ccmc")) {
		t.Errorf("CcmcDir() = %q, want suffix .ccmc", got)
	}
}

func TestCcmcDir_EnvOverride(t *testing.T) {
	t.Setenv("CCMC_DIR", "/tmp/test-ccmc")
	got := CcmcDir()
	if got != "/tmp/test-ccmc" {
		t.Errorf("CcmcDir() = %q, want /tmp/test-ccmc", got)
	}
}

func TestClaudePaths_UseClaudeDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/mock/claude")

	tests := []struct {
		name string
		fn   func() string
		want string
	}{
		{"SettingsPath", ClaudeSettingsPath, "/mock/claude/settings.json"},
		{"ProjectsDir", ClaudeProjectsDir, "/mock/claude/projects"},
		{"TodosDir", ClaudeTodosDir, "/mock/claude/todos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn(); got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestCcmcPaths_UseCcmcDir(t *testing.T) {
	t.Setenv("CCMC_DIR", "/mock/ccmc")

	tests := []struct {
		name string
		fn   func() string
		want string
	}{
		{"ConfigPath", CcmcConfigPath, "/mock/ccmc/config.yaml"},
		{"RegistryPath", CcmcRegistryPath, "/mock/ccmc/registry.json"},
		{"SocketPath", CcmcSocketPath, "/mock/ccmc/ccmc.sock"},
		{"DaemonPidPath", CcmcDaemonPidPath, "/mock/ccmc/daemon.pid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn(); got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
