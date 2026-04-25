package integrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// testConfig returns a Config with CloneDir set to a temp directory.
func testConfig(t *testing.T, cloneDir string) config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Integrator.CloneDir = cloneDir
	cfg.Integrator.Model = "claude-sonnet-4-6"
	return cfg
}

// newTestInstaller creates an Installer with its registry and clone dir inside t.TempDir().
func newTestInstaller(t *testing.T) (*Installer, string) {
	t.Helper()
	tmp := t.TempDir()
	cloneDir := filepath.Join(tmp, "tools")
	cfg := testConfig(t, cloneDir)
	ins := NewInstaller(cfg)
	// Override the registry path to a temp location.
	ins.registryPath = filepath.Join(tmp, "tools.json")
	return ins, tmp
}

// stubClone replaces cloneCmd with a function that creates a minimal fake repo
// at dest containing the given files (map of relative path → content).
func stubClone(t *testing.T, files map[string]string) func() {
	t.Helper()
	orig := cloneCmd
	cloneCmd = func(_ context.Context, _, dest string) error {
		for rel, content := range files {
			full := filepath.Join(dest, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	return func() { cloneCmd = orig }
}

// stubNpmInstall replaces npmInstallCmd with a no-op.
func stubNpmInstall(t *testing.T) func() {
	t.Helper()
	orig := npmInstallCmd
	npmInstallCmd = func(_ context.Context, _ string) error { return nil }
	return func() { npmInstallCmd = orig }
}

// buildMCPStdioSettings builds a minimal example settings.json with mcpServers
// containing a stdio entry.
func buildMCPStdioSettings(t *testing.T, serverName, command string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"command": command,
				"args":    []string{"arg1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildMCPStdioSettings: %v", err)
	}
	return string(b)
}

// buildMCPSSESettings builds an example settings.json with an SSE entry.
func buildMCPSSESettings(t *testing.T, serverName, sseURL string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"url": sseURL,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildMCPSSESettings: %v", err)
	}
	return string(b)
}

// ── Type detection tests ───────────────────────────────────────────────────────

func TestInstall_DetectStdio(t *testing.T) {
	ec := ccmc.EvalContext{
		ExampleSettings: buildMCPStdioSettings(t, "my-mcp", "node"),
	}
	got, _, err := detectToolType(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "mcp-stdio" {
		t.Errorf("type = %q; want mcp-stdio", got)
	}
}

func TestInstall_DetectSSE(t *testing.T) {
	ec := ccmc.EvalContext{
		ExampleSettings: buildMCPSSESettings(t, "my-sse-mcp", "https://example.com/mcp/sse"),
	}
	got, _, err := detectToolType(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "mcp-sse" {
		t.Errorf("type = %q; want mcp-sse", got)
	}
}

func TestInstall_DetectSkill(t *testing.T) {
	ec := ccmc.EvalContext{
		ReadmeMarkdown: "# My Skill\n\nThis skill uses SKILL.md frontmatter for configuration.\n",
	}
	got, _, err := detectToolType(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "skill" {
		t.Errorf("type = %q; want skill", got)
	}
}

func TestInstall_DetectAgent(t *testing.T) {
	ec := ccmc.EvalContext{
		ReadmeMarkdown: "# My Agent\n\nPlace this in agents/my-agent.md with description: frontmatter.",
	}
	got, _, err := detectToolType(ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "agent" {
		t.Errorf("type = %q; want agent", got)
	}
}

func TestInstall_UnknownType(t *testing.T) {
	ec := ccmc.EvalContext{
		ReadmeMarkdown: "# Generic Repo\n\nJust a regular Go library.",
		PackageJSON:    `{"name": "some-lib"}`,
	}
	_, _, err := detectToolType(ec)
	if err == nil {
		t.Fatal("expected an error for undetectable type; got nil")
	}
}

// ── Installation tests ─────────────────────────────────────────────────────────

// TestInstall_MCPStdio_WritesSettings — after install, the target scope's
// settings.json gains a new mcpServers entry, and a .bak is created.
func TestInstall_MCPStdio_WritesSettings(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"package.json": `{"name":"mcp-test","main":"index.js"}`,
		"index.js":     `// stub`,
	})()
	defer stubNpmInstall(t)()

	scopeDir := filepath.Join(tmp, "myproject")
	if err := os.MkdirAll(filepath.Join(scopeDir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	src := InstallSource{
		URL:      "https://github.com/example/mcp-test",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPStdioSettings(t, "mcp-test", "node")},
		Scope:    scopeDir,
		ToolType: "mcp-stdio",
	}

	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Type != "mcp-stdio" {
		t.Errorf("Type = %q; want mcp-stdio", result.Type)
	}

	// Verify settings.json was written.
	settingsPath := filepath.Join(scopeDir, ".claude", "settings.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not found: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatalf("settings.json malformed: %v", err)
	}
	if _, ok := settings["mcpServers"]; !ok {
		t.Error("settings.json missing mcpServers key")
	}

	// Verify .bak was created.
	bakPath := settingsPath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		t.Errorf(".bak not created: %v", err)
	}
}

// TestInstall_MCPSSE_NoCloneNoDeps — an SSE install must not invoke cloneCmd
// or npmInstallCmd; it must only write to settings.json.
func TestInstall_MCPSSE_NoCloneNoDeps(t *testing.T) {
	ins, tmp := newTestInstaller(t)

	cloneInvoked := false
	orig := cloneCmd
	cloneCmd = func(_ context.Context, _, _ string) error {
		cloneInvoked = true
		return nil
	}
	defer func() { cloneCmd = orig }()

	npmInvoked := false
	origNpm := npmInstallCmd
	npmInstallCmd = func(_ context.Context, _ string) error {
		npmInvoked = true
		return nil
	}
	defer func() { npmInstallCmd = origNpm }()

	scopeDir := filepath.Join(tmp, "myproject2")
	if err := os.MkdirAll(filepath.Join(scopeDir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	src := InstallSource{
		URL:      "https://github.com/example/mcp-sse-tool",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPSSESettings(t, "mcp-sse-tool", "https://api.example.com/mcp/sse")},
		Scope:    scopeDir,
		ToolType: "mcp-sse",
	}

	_, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install SSE: %v", err)
	}

	if cloneInvoked {
		t.Error("cloneCmd must not be called for SSE installs")
	}
	if npmInvoked {
		t.Error("npmInstallCmd must not be called for SSE installs")
	}

	// Settings must contain the URL entry.
	settingsPath := filepath.Join(scopeDir, ".claude", "settings.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not found: %v", err)
	}
	if !strings.Contains(string(b), "api.example.com") {
		t.Errorf("settings.json does not contain the SSE URL; content: %s", b)
	}
}

// TestInstall_Skill_CopiesDir — skill install copies source dir contents to target.
func TestInstall_Skill_CopiesDir(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"SKILL.md":     "---\nname: test-skill\n---\n# Test Skill",
		"skill-body.md": "Details here.",
	})()

	// Override skill target dir via scope manipulation.
	scopeDir := filepath.Join(tmp, "skillproject")

	src := InstallSource{
		URL:      "https://github.com/example/test-skill",
		EvalCtx:  ccmc.EvalContext{ReadmeMarkdown: "This is a skill with SKILL.md"},
		Scope:    scopeDir,
		ToolType: "skill",
	}

	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install skill: %v", err)
	}
	if result.Type != "skill" {
		t.Errorf("Type = %q; want skill", result.Type)
	}

	// ConfigPath is the target skill directory. Verify SKILL.md landed there.
	skillMD := filepath.Join(result.ConfigPath, "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		t.Errorf("SKILL.md not found at target: %v", err)
	}
}

// TestInstall_Agent_CopiesFile — agent install copies the .md file to agents dir.
func TestInstall_Agent_CopiesFile(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"agents/my-agent.md": "---\nname: my-agent\ndescription: Does things\n---\n# My Agent",
	})()

	scopeDir := filepath.Join(tmp, "agentproject")

	src := InstallSource{
		URL:      "https://github.com/example/my-agent",
		EvalCtx:  ccmc.EvalContext{ReadmeMarkdown: "Agent with agents/ directory frontmatter description:"},
		Scope:    scopeDir,
		ToolType: "agent",
	}

	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install agent: %v", err)
	}
	if result.Type != "agent" {
		t.Errorf("Type = %q; want agent", result.Type)
	}

	// The .md file should exist at the ConfigPath location.
	if _, err := os.Stat(result.ConfigPath); err != nil {
		t.Errorf("agent file not found at ConfigPath %s: %v", result.ConfigPath, err)
	}
}

// TestInstall_RegistryAppended — after a successful install, tools.json
// (in the test temp dir) contains the new entry.
func TestInstall_RegistryAppended(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"package.json": `{"name":"reg-tool"}`,
		"index.js":     "// stub",
	})()
	defer stubNpmInstall(t)()

	scopeDir := filepath.Join(tmp, "regproject")

	src := InstallSource{
		URL:      "https://github.com/example/reg-tool",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPStdioSettings(t, "reg-tool", "node")},
		Scope:    scopeDir,
		ToolType: "mcp-stdio",
	}

	if _, err := ins.Install(context.Background(), src); err != nil {
		t.Fatalf("Install: %v", err)
	}

	entries, err := ins.loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("registry is empty after install")
	}
	if entries[0].SourceURL != src.URL {
		t.Errorf("registry entry SourceURL = %q; want %q", entries[0].SourceURL, src.URL)
	}
	if entries[0].Scope != scopeDir {
		t.Errorf("registry entry Scope = %q; want %q", entries[0].Scope, scopeDir)
	}
	if entries[0].Type != "mcp-stdio" {
		t.Errorf("registry entry Type = %q; want mcp-stdio", entries[0].Type)
	}
}

// TestInstall_AlreadyInstalledRefused — second install of same URL+scope returns
// ErrToolAlreadyInstalled. With Force=true, it succeeds.
func TestInstall_AlreadyInstalledRefused(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"package.json": `{"name":"dup-tool"}`,
		"index.js":     "// stub",
	})()
	defer stubNpmInstall(t)()

	scopeDir := filepath.Join(tmp, "dupproject")

	src := InstallSource{
		URL:      "https://github.com/example/dup-tool",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPStdioSettings(t, "dup-tool", "node")},
		Scope:    scopeDir,
		ToolType: "mcp-stdio",
	}

	// First install must succeed.
	if _, err := ins.Install(context.Background(), src); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Second install must be refused.
	_, err := ins.Install(context.Background(), src)
	if !errors.Is(err, ErrToolAlreadyInstalled) {
		t.Errorf("second install: want ErrToolAlreadyInstalled; got %v", err)
	}

	// With Force=true, the second install must succeed.
	src.Force = true
	if _, err := ins.Install(context.Background(), src); err != nil {
		t.Errorf("forced install: %v", err)
	}
}

// TestInstall_ScopeProject — installs write to <project>/.claude/... not ~/.claude/...
func TestInstall_ScopeProject(t *testing.T) {
	ins, tmp := newTestInstaller(t)
	defer stubClone(t, map[string]string{
		"package.json": `{"name":"scoped-tool"}`,
		"index.js":     "// stub",
	})()
	defer stubNpmInstall(t)()

	// Use a temp project path as the scope.
	projectPath := filepath.Join(tmp, "myproject-scoped")

	src := InstallSource{
		URL:      "https://github.com/example/scoped-tool",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPStdioSettings(t, "scoped-tool", "node")},
		Scope:    projectPath,
		ToolType: "mcp-stdio",
	}

	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install scoped: %v", err)
	}

	// ConfigPath must be inside the project directory, not the user home.
	if !strings.HasPrefix(result.ConfigPath, projectPath) {
		t.Errorf("ConfigPath %q should be under project path %q", result.ConfigPath, projectPath)
	}

	// The settings.json must exist at <project>/.claude/settings.json.
	expectedSettings := filepath.Join(projectPath, ".claude", "settings.json")
	if _, err := os.Stat(expectedSettings); err != nil {
		t.Errorf("project settings.json not found at %s: %v", expectedSettings, err)
	}

	// Global ~/.claude/settings.json must NOT have been touched. We verify by
	// checking that the ConfigPath is not under the home directory.
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(result.ConfigPath, home) {
		t.Errorf("ConfigPath %q should NOT be under home directory for project scope", result.ConfigPath)
	}
}
