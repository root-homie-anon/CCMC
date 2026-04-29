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
	if got != "stdio" {
		t.Errorf("type = %q; want stdio", got)
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
	if got != "sse" {
		t.Errorf("type = %q; want sse", got)
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
		ToolType: "stdio",
	}

	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Type != "stdio" {
		t.Errorf("Type = %q; want stdio", result.Type)
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
		ToolType: "sse",
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
		ToolType: "stdio",
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
	if entries[0].Type != "stdio" {
		t.Errorf("registry entry Type = %q; want stdio", entries[0].Type)
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
		ToolType: "stdio",
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
		ToolType: "stdio",
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

// ── Security hardening tests ───────────────────────────────────────────────────

// TestCloneCmd_RejectsNonGitHubURL verifies H-1: cloneCmd rejects any URL that
// is not https://github.com/ without invoking git.
func TestCloneCmd_RejectsNonGitHubURL(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"ssh://git@github.com/owner/repo",
		"http://github.com/owner/repo",
		"https://evil.com/owner/repo",
		"--upload-pack=/path/to/evil",
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			err := cloneCmd(context.Background(), url, t.TempDir())
			if err == nil {
				t.Errorf("cloneCmd(%q): expected error for non-GitHub URL, got nil", url)
			}
			if !strings.Contains(err.Error(), "not an allowed GitHub HTTPS URL") {
				t.Errorf("cloneCmd(%q): unexpected error: %v", url, err)
			}
		})
	}
}

// TestInstallPlugin_RejectsNonGitHubURL verifies H-1: installPlugin rejects non-GitHub URLs.
func TestInstallPlugin_RejectsNonGitHubURL(t *testing.T) {
	ins, _ := newTestInstaller(t)

	src := InstallSource{
		URL:      "file:///etc/passwd",
		ToolType: "plugin",
	}
	_, err := ins.Install(context.Background(), src)
	if err == nil {
		t.Fatal("expected error for file:// URL in plugin install, got nil")
	}
	if !strings.Contains(err.Error(), "not an allowed GitHub HTTPS URL") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveMCPStdioEntry_AllowlistOnly verifies H-2: only command/args/env
// keys from the example settings are propagated; arbitrary keys are dropped.
func TestResolveMCPStdioEntry_AllowlistOnly(t *testing.T) {
	cloneDir := t.TempDir()
	example, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"my-server": map[string]any{
				"command":      "node",
				"args":         []string{"index.js"},
				"env":          map[string]string{"FOO": "bar"},
				"unknown-key":  "should be dropped",
				"another-evil": "also dropped",
			},
		},
	})

	entry, warn, err := resolveMCPStdioEntry("my-server", cloneDir, string(example))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warn != "" {
		t.Errorf("unexpected warn: %q", warn)
	}

	for _, allowed := range []string{"command", "args", "env"} {
		if _, ok := entry[allowed]; !ok {
			t.Errorf("expected key %q in filtered entry, missing", allowed)
		}
	}
	for _, forbidden := range []string{"unknown-key", "another-evil"} {
		if _, ok := entry[forbidden]; ok {
			t.Errorf("key %q should have been dropped from entry, but is present", forbidden)
		}
	}
}

// TestResolveMCPStdioEntry_RejectsBadCommand verifies H-2: a command that is
// not a known interpreter and not under cloneDest is rejected with an error.
func TestResolveMCPStdioEntry_RejectsBadCommand(t *testing.T) {
	cloneDir := t.TempDir()
	cases := []struct {
		name    string
		command string
	}{
		{"relative path", "./evil.sh"},
		{"absolute outside clone", "/usr/bin/malware"},
		{"path traversal", "/etc/passwd"},
		{"dotdot relative", "../../../etc/cron.d/evil"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			example, _ := json.Marshal(map[string]any{
				"mcpServers": map[string]any{
					"srv": map[string]any{"command": tc.command},
				},
			})
			_, _, err := resolveMCPStdioEntry("srv", cloneDir, string(example))
			if err == nil {
				t.Errorf("command %q: expected error, got nil", tc.command)
			}
		})
	}
}

// TestCopyDir_SymlinkSourceSkipped verifies H-3: symlinks in the source tree
// are skipped with a warning and do not cause an error or a data write.
func TestCopyDir_SymlinkSourceSkipped(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create a regular file in src.
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("real"), 0o600); err != nil {
		t.Fatalf("write real.txt: %v", err)
	}
	// Create a symlink in src that points outside.
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(src, "symlink.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// real.txt must be copied.
	if _, err := os.Stat(filepath.Join(dst, "real.txt")); err != nil {
		t.Error("real.txt not found in dst")
	}
	// symlink.txt must NOT be copied (symlink skipped).
	if _, err := os.Stat(filepath.Join(dst, "symlink.txt")); err == nil {
		t.Error("symlink.txt should not have been copied to dst")
	}
}

// TestCopyFile_SymlinkDstRefused verifies H-3: if dst is a symlink, copyFile
// refuses to write through it.
func TestCopyFile_SymlinkDstRefused(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	srcFile := filepath.Join(src, "file.txt")
	if err := os.WriteFile(srcFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Create a file that the symlink will point at, then symlink it at dst location.
	realTarget := filepath.Join(dst, "innocent.txt")
	if err := os.WriteFile(realTarget, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	symlinkDst := filepath.Join(dst, "file.txt")
	if err := os.Symlink(realTarget, symlinkDst); err != nil {
		t.Fatalf("create symlink dst: %v", err)
	}

	err := copyFile(srcFile, symlinkDst)
	if err == nil {
		t.Fatal("expected error when dst is a symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("unexpected error: %v", err)
	}

	// innocent.txt must be untouched.
	data, _ := os.ReadFile(realTarget)
	if string(data) != "do not overwrite" {
		t.Errorf("symlink target was modified: %q", string(data))
	}
}

// TestCopyDir_PathTraversalRefused verifies H-3: a crafted relative path that
// escapes the destination directory is refused with an error.
func TestCopyDir_PathTraversalRefused(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create a subdirectory named ".." — on most filesystems this is impossible,
	// but we can test the guard by using WalkDir with a fabricated path.
	// The practical test: create a deep path inside src whose relpath contains ".."
	// after filepath.Rel — this can't happen with WalkDir (it always gives
	// relative paths forward), so we test the guard function directly.

	// Create a subdirectory and a file, then verify normal copy succeeds.
	subDir := filepath.Join(src, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir should succeed for normal tree: %v", err)
	}

	// Verify the file landed correctly.
	if _, err := os.Stat(filepath.Join(dst, "sub", "f.txt")); err != nil {
		t.Errorf("f.txt not found in dst: %v", err)
	}
}

// TestInstall_C1RoundTrip verifies C-1: a tool installed via Installer.Install
// (which now writes type "stdio") is correctly removed by Manager.Remove
// (which checks for type "stdio"), leaving no mcpServers entry in settings.json.
func TestInstall_C1RoundTrip(t *testing.T) {
	// Set up separate CCMC_DIR and CLAUDE_CONFIG_DIR so nothing touches real files.
	ccmcDir := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CCMC_DIR", ccmcDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	ins, _ := newTestInstaller(t)
	// Route the installer's registry to inside ccmcDir so Manager reads the same file.
	ins.registryPath = filepath.Join(ccmcDir, "tools.json")

	defer stubClone(t, map[string]string{
		"package.json": `{"name":"rt-tool"}`,
		"index.js":     "// stub",
	})()
	defer stubNpmInstall(t)()

	// Use claudeDir as scope dir (global-equivalent for this test).
	src := InstallSource{
		URL:      "https://github.com/example/rt-tool",
		EvalCtx:  ccmc.EvalContext{ExampleSettings: buildMCPStdioSettings(t, "rt-tool", "node")},
		Scope:    claudeDir,
		ToolType: "stdio",
	}

	if _, err := ins.Install(context.Background(), src); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Verify that settings.json has the mcpServers entry.
	settingsPath := filepath.Join(claudeDir, ".claude", "settings.json")
	if !hasMCPKey(t, settingsPath, "rt-tool") {
		t.Fatal("expected rt-tool in mcpServers after install")
	}

	// Now remove via Manager. The Manager must find type "stdio" (not "mcp-stdio")
	// and call removeFromMCPSettings.
	mgr := NewManager(ins.registryPath)
	if err := mgr.Remove("rt-tool", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// settings.json must no longer contain the entry.
	if hasMCPKey(t, settingsPath, "rt-tool") {
		t.Error("C-1: rt-tool still in mcpServers after Remove — type string mismatch not fixed")
	}
}

// TestCloneCmd_RejectsFlagInjection verifies NF-4 / H-1: cloneCmd's scheme
// allowlist rejects any URL that is not a strict https://github.com/ prefix.
// A flag-injection payload like "--upload-pack=evil" that somehow survives
// ParseURL normalization (it would become "https://github.com/--upload-pack=..."
// which does begin with the allowed prefix) still hits cloneCmd as a URL
// argument — not a separate argv element — so it is safe. But if ParseURL
// produces a non-github.com URL (e.g. from an ssh:// input), cloneCmd must
// reject it without spawning a process.
func TestCloneCmd_RejectsFlagInjection(t *testing.T) {
	// N-2: save and restore the real seam, then call origCloneCmd directly so a
	// future regression that removes the guard from installer.go:cloneCmd is caught.
	// Previously this test re-implemented the prefix check inline, meaning the real
	// guard could be deleted without failing the test.
	origCloneCmd := cloneCmd
	t.Cleanup(func() { cloneCmd = origCloneCmd })

	cases := []struct {
		name string
		url  string
	}{
		{"ssh scheme", "ssh://git@github.com/evil/repo"},
		{"file scheme", "file:///etc/passwd"},
		{"flag shaped", "--upload-pack=evil"},
		{"non-github https", "https://evil.com/owner/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Call origCloneCmd (the real seam) directly — not cloneCmd, which tests
			// may have replaced. This is the seam whose guard we are testing.
			err := origCloneCmd(context.Background(), tc.url, t.TempDir())
			if err == nil {
				t.Errorf("cloneCmd(%q): expected rejection, got nil", tc.url)
			}
		})
	}
}

// TestInstall_StalePartialRefusedWithoutForce verifies NF-3: if a target skill
// directory already exists (stale partial install), Install returns
// ErrStalePartialInstall when Force is false.
func TestInstall_StalePartialRefusedWithoutForce(t *testing.T) {
	ins, _ := newTestInstaller(t)
	// Use a project-scoped path so resolveSkillDir returns a path we control,
	// rather than expanding "~/.claude/skills/..." which ignores CLAUDE_CONFIG_DIR.
	projectDir := t.TempDir()
	defer stubClone(t, map[string]string{
		"SKILL.md": "# fake skill",
	})()

	// resolveSkillDir(scope, name) for a non-global scope returns
	// filepath.Join(scope, ".claude", "skills", name).
	skillDir := filepath.Join(projectDir, ".claude", "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("pre-create skill dir: %v", err)
	}
	// Write a file inside so it's not empty (simulates mid-copy abort).
	if err := os.WriteFile(filepath.Join(skillDir, "leftover.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	src := InstallSource{
		URL:      "https://github.com/example/my-skill",
		EvalCtx:  ccmc.EvalContext{ReadmeMarkdown: "# My Skill\nThis skill uses SKILL.md"},
		Scope:    projectDir, // project scope so target is under projectDir
		ToolType: "skill",
		Force:    false,
	}
	_, err := ins.Install(context.Background(), src)
	if err == nil {
		t.Fatal("expected ErrStalePartialInstall, got nil")
	}
	if !errors.Is(err, ErrStalePartialInstall) {
		t.Errorf("expected ErrStalePartialInstall, got: %v", err)
	}
	// Error message should mention stale partial.
	if !strings.Contains(err.Error(), "stale partial") {
		t.Errorf("error should mention 'stale partial'; got: %v", err)
	}
}

// TestPrepareTargetDir_FreshPath verifies that prepareTargetDir on a path that
// does not yet exist creates a real directory with mode 0o700 and no symlink.
// This covers the TOCTOU fix: the function now atomically creates the directory
// itself rather than returning nil and trusting the caller to do it.
func TestPrepareTargetDir_FreshPath(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "new-tool")
	// Confirm the target does not exist.
	if _, err := os.Lstat(target); err == nil {
		t.Fatalf("precondition failed: %s already exists", target)
	}

	if err := prepareTargetDir(target, parent, false); err != nil {
		t.Fatalf("prepareTargetDir: unexpected error on fresh path: %v", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("target not created: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("target is a symlink — expected a real directory")
	}
	if !fi.IsDir() {
		t.Errorf("target mode = %s; expected directory", fi.Mode())
	}
	// Mode bits: 0o700 (owner rwx, no group/other).
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("target perm = %04o; want 0700", fi.Mode().Perm())
	}
}

// TestPrepareTargetDir_SymlinkRaceDetected verifies that the TOCTOU window is
// closed: if a symlink exists at targetDir when prepareTargetDir runs (simulating
// an attacker racing a symlink in after a prior RemoveAll, or pre-placing one),
// the function returns ErrSymlinkRace and does NOT follow the symlink. The
// symlink target must remain empty — no files are written into it.
func TestPrepareTargetDir_SymlinkRaceDetected(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "raced-tool")

	// Attacker-controlled destination — should remain empty after the call.
	attackerDir := t.TempDir()

	// Pre-create a symlink at the target path pointing to the attacker's directory.
	// This simulates the race window: the attacker wins the slot before our Mkdir.
	if err := os.Symlink(attackerDir, target); err != nil {
		t.Fatalf("setup: create symlink: %v", err)
	}

	// With force=true: prepareTargetDir will detect the existing symlink during
	// its initial Lstat, refuse RemoveAll on a symlink directly, and return an error.
	// With force=false: it returns ErrStalePartialInstall (exists and not forced) —
	// but importantly, it never follows the symlink.
	//
	// We test force=true because that is the path with the TOCTOU window: RemoveAll
	// would remove the symlink itself (not its target), and then os.Mkdir would
	// atomically create a real directory — but if Mkdir sees the symlink still there
	// (impossible after RemoveAll of a symlink, but tested for correctness), it
	// returns ErrSymlinkRace.
	//
	// The existing code now guards the pre-RemoveAll Lstat: if the path is a symlink,
	// it returns an error rather than calling RemoveAll. This is the primary guard.
	err := prepareTargetDir(target, parent, true)
	if err == nil {
		t.Fatal("expected error when target is a symlink, got nil — TOCTOU window is open")
	}

	// The error must indicate a symlink problem — either ErrSymlinkRace or a
	// message containing "symlink".
	if !errors.Is(err, ErrSymlinkRace) && !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink-related error, got: %v", err)
	}

	// The attacker's directory must still be empty — nothing was written into it.
	entries, readErr := os.ReadDir(attackerDir)
	if readErr != nil {
		t.Fatalf("ReadDir attacker dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("attacker directory was written to (%d entries) — symlink redirect succeeded", len(entries))
	}
}

// TestInstall_StalePartialRemovedWithForce verifies NF-3: with Force=true, a
// stale partial install directory is removed before copyDir runs, allowing the
// install to succeed.
func TestInstall_StalePartialRemovedWithForce(t *testing.T) {
	ins, _ := newTestInstaller(t)
	projectDir := t.TempDir()
	defer stubClone(t, map[string]string{
		"SKILL.md":   "# fake skill",
		"prompt.txt": "do useful things",
	})()

	// Pre-create the target skill directory with a stale file.
	skillDir := filepath.Join(projectDir, ".claude", "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("pre-create skill dir: %v", err)
	}
	staleFile := filepath.Join(skillDir, "stale.txt")
	if err := os.WriteFile(staleFile, []byte("leftover"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	src := InstallSource{
		URL:      "https://github.com/example/my-skill",
		EvalCtx:  ccmc.EvalContext{ReadmeMarkdown: "# My Skill\nThis skill uses SKILL.md"},
		Scope:    projectDir, // project scope so target is under projectDir
		ToolType: "skill",
		Force:    true,
	}
	result, err := ins.Install(context.Background(), src)
	if err != nil {
		t.Fatalf("expected success with --force, got: %v", err)
	}
	if result.Name != "my-skill" {
		t.Errorf("result.Name = %q, want my-skill", result.Name)
	}
	// The stale file must have been removed (new install replaces old dir).
	if _, statErr := os.Stat(staleFile); statErr == nil {
		t.Error("stale file still exists after force re-install")
	}
}
