package integrator

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestManager creates a Manager backed by a temp directory.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "tools.json")
	m := NewManager(registryPath)
	return m, dir
}

// writeRegistry writes a toolsRegistry directly to registryPath for test setup.
func writeRegistry(t *testing.T, registryPath string, entries []ccmc.ToolRegistryEntry) {
	t.Helper()
	reg := toolsRegistry{Tools: entries}
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("writeRegistry: marshal: %v", err)
	}
	if err := os.WriteFile(registryPath, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("writeRegistry: write: %v", err)
	}
}

// readRegistry reads the registry directly for test assertions.
func readRegistry(t *testing.T, registryPath string) []ccmc.ToolRegistryEntry {
	t.Helper()
	b, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("readRegistry: read: %v", err)
	}
	var reg toolsRegistry
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatalf("readRegistry: unmarshal: %v", err)
	}
	return reg.Tools
}

// entry is a convenience constructor for test entries.
func entry(name, typ, scope string) ccmc.ToolRegistryEntry {
	return ccmc.ToolRegistryEntry{
		Name:        name,
		Type:        typ,
		SourceURL:   "https://github.com/example/" + name,
		Scope:       scope,
		InstalledAt: time.Now().Format(time.RFC3339),
	}
}

// writeSettings writes a minimal settings.json with the given mcpServers keys.
func writeSettings(t *testing.T, path string, mcpNames []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("writeSettings: mkdir: %v", err)
	}
	servers := make(map[string]json.RawMessage, len(mcpNames))
	for _, n := range mcpNames {
		servers[n] = json.RawMessage(`{"command":"npx","args":["` + n + `"]}`)
	}
	raw, _ := json.Marshal(servers)
	settings := map[string]json.RawMessage{
		"mcpServers": json.RawMessage(raw),
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("writeSettings: write: %v", err)
	}
}

// hasMCPKey returns true if settingsPath contains name under mcpServers.
func hasMCPKey(t *testing.T, settingsPath, name string) bool {
	t.Helper()
	m, err := config.ReadSettings(settingsPath)
	if err != nil {
		t.Fatalf("hasMCPKey: ReadSettings: %v", err)
	}
	raw, ok := m["mcpServers"]
	if !ok {
		return false
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatalf("hasMCPKey: unmarshal: %v", err)
	}
	_, found := servers[name]
	return found
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestList_Empty(t *testing.T) {
	m, _ := newTestManager(t)
	entries, err := m.List()
	if err != nil {
		t.Fatalf("List error on missing registry: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty list, got %d entries", len(entries))
	}
}

func TestList_Multiple(t *testing.T) {
	m, _ := newTestManager(t)
	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		entry("zeta", "stdio", "global"),
		entry("alpha", "sse", "global"),
		entry("mu", "skill", "global"),
	})
	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	names := []string{entries[0].Name, entries[1].Name, entries[2].Name}
	want := []string{"alpha", "mu", "zeta"}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("entry[%d]: got %q, want %q", i, n, want[i])
		}
	}
}

func TestGet_Found(t *testing.T) {
	m, _ := newTestManager(t)
	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		entry("mcp-go", "stdio", "global"),
	})
	e, err := m.Get("mcp-go")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Name != "mcp-go" {
		t.Errorf("Get: name = %q, want mcp-go", e.Name)
	}
}

func TestGet_NotFound(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Get("nonexistent")
	if !errors.Is(err, ErrToolNotFound) {
		t.Errorf("Get: expected ErrToolNotFound, got %v", err)
	}
}

func TestAdd_Append(t *testing.T) {
	m, _ := newTestManager(t)
	e := entry("my-tool", "stdio", "global")
	if err := m.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries := readRegistry(t, m.registryPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "my-tool" {
		t.Errorf("entry name = %q, want my-tool", entries[0].Name)
	}
}

func TestAdd_IdempotentOnNameScope(t *testing.T) {
	m, _ := newTestManager(t)
	e := entry("my-tool", "stdio", "global")
	if err := m.Add(e); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	// Second add with same (name, scope) — should update, not duplicate.
	e2 := entry("my-tool", "stdio", "global")
	e2.SourceURL = "https://github.com/example/my-tool-updated"
	if err := m.Add(e2); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	entries := readRegistry(t, m.registryPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after idempotent add, got %d", len(entries))
	}
	if entries[0].SourceURL != "https://github.com/example/my-tool-updated" {
		t.Errorf("SourceURL not updated: %s", entries[0].SourceURL)
	}
}

func TestRemove_RegistryEntry(t *testing.T) {
	m, _ := newTestManager(t)
	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		entry("tool-a", "stdio", "global"),
		entry("tool-b", "stdio", "global"),
	})
	if err := m.Remove("tool-a", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	entries := readRegistry(t, m.registryPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "tool-b" {
		t.Errorf("remaining entry = %q, want tool-b", entries[0].Name)
	}
}

func TestRemove_SettingsJSONUpdated(t *testing.T) {
	m, _ := newTestManager(t)

	// Use a temp directory as the "global" Claude dir by overriding CLAUDE_CONFIG_DIR.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	writeSettings(t, settingsPath, []string{"tool-mcp", "other-tool"})

	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		{Name: "tool-mcp", Type: "stdio", Scope: "global", InstalledAt: time.Now().Format(time.RFC3339)},
	})

	if err := m.Remove("tool-mcp", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if hasMCPKey(t, settingsPath, "tool-mcp") {
		t.Error("expected tool-mcp to be removed from mcpServers, but it is still present")
	}
	if !hasMCPKey(t, settingsPath, "other-tool") {
		t.Error("expected other-tool to remain in mcpServers")
	}
}

func TestRemove_DeleteClone(t *testing.T) {
	m, _ := newTestManager(t)

	// Clone must be inside CcmcDir. Override CCMC_DIR so the clone path is inside it.
	ccmcDir := t.TempDir()
	t.Setenv("CCMC_DIR", ccmcDir)
	// Recreate manager using the new CcmcDir.
	registryPath := filepath.Join(ccmcDir, "tools.json")
	m = NewManager(registryPath)

	cloneDir := filepath.Join(ccmcDir, "tools", "my-tool")
	if err := os.MkdirAll(cloneDir, 0o700); err != nil {
		t.Fatalf("create clone dir: %v", err)
	}
	// Drop a file inside to confirm RemoveAll works.
	if err := os.WriteFile(filepath.Join(cloneDir, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file in clone: %v", err)
	}

	writeRegistry(t, registryPath, []ccmc.ToolRegistryEntry{
		{Name: "my-tool", Type: "stdio", Scope: "global", ClonePath: cloneDir, InstalledAt: time.Now().Format(time.RFC3339)},
	})

	if err := m.Remove("my-tool", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(cloneDir); !os.IsNotExist(err) {
		t.Error("expected clone dir to be deleted, but it still exists")
	}
}

func TestRemove_RefusesEscapingCloneDir(t *testing.T) {
	m, _ := newTestManager(t)

	ccmcDir := t.TempDir()
	t.Setenv("CCMC_DIR", ccmcDir)
	registryPath := filepath.Join(ccmcDir, "tools.json")
	m = NewManager(registryPath)

	// Malicious clone_path pointing outside CcmcDir.
	maliciousPath := "/tmp/escape-test-target"

	writeRegistry(t, registryPath, []ccmc.ToolRegistryEntry{
		{Name: "bad-tool", Type: "stdio", Scope: "global", ClonePath: maliciousPath, InstalledAt: time.Now().Format(time.RFC3339)},
	})

	err := m.Remove("bad-tool", true)
	if err == nil {
		t.Fatal("expected error for escaping clone_path, got nil")
	}
	if !strings.Contains(err.Error(), "outside allowed directory") {
		t.Errorf("expected 'outside allowed directory' in error, got: %v", err)
	}
}

func TestRemove_NotFound(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.Remove("ghost", false)
	if !errors.Is(err, ErrToolNotFound) {
		t.Errorf("expected ErrToolNotFound, got %v", err)
	}
}

func TestUpdate_StdioRunsGitPull(t *testing.T) {
	m, _ := newTestManager(t)

	var capturedPath string
	gitPullCmd = func(clonePath string) *exec.Cmd {
		capturedPath = clonePath
		// Return a no-op command so no real git is needed.
		return exec.Command("true")
	}
	t.Cleanup(func() {
		gitPullCmd = func(clonePath string) *exec.Cmd {
			return exec.Command("git", "-C", clonePath, "pull")
		}
	})

	clonePath := t.TempDir()
	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		{Name: "stdio-tool", Type: "stdio", Scope: "global", ClonePath: clonePath, InstalledAt: time.Now().Format(time.RFC3339)},
	})

	if err := m.Update("stdio-tool"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if capturedPath != clonePath {
		t.Errorf("git pull invoked with path %q, want %q", capturedPath, clonePath)
	}
}

func TestUpdate_NoCloneIsNoop(t *testing.T) {
	m, _ := newTestManager(t)

	var gitCalled bool
	gitPullCmd = func(clonePath string) *exec.Cmd {
		gitCalled = true
		return exec.Command("true")
	}
	t.Cleanup(func() {
		gitPullCmd = func(clonePath string) *exec.Cmd {
			return exec.Command("git", "-C", clonePath, "pull")
		}
	})

	writeRegistry(t, m.registryPath, []ccmc.ToolRegistryEntry{
		{Name: "sse-tool", Type: "sse", Scope: "global", InstalledAt: time.Now().Format(time.RFC3339)},
	})

	if err := m.Update("sse-tool"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gitCalled {
		t.Error("expected git pull NOT to be called for sse type with no clone_path")
	}
}

func TestRegistry_SymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realFile, []byte(`{"tools":[]}`+"\n"), 0o600); err != nil {
		t.Fatalf("write real file: %v", err)
	}
	symlinkPath := filepath.Join(dir, "tools.json")
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	m := NewManager(symlinkPath)
	_, err := m.List()
	if err == nil {
		t.Fatal("expected error for symlink registry path, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected 'symlink' in error, got: %v", err)
	}
}
