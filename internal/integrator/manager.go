package integrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// ErrToolNotFound is returned by Get and Remove when the named tool is absent
// from the registry.
var ErrToolNotFound = errors.New("tool not found in registry")

// toolsRegistry is the on-disk shape of ~/.ccmc/tools.json.
type toolsRegistry struct {
	Tools []ccmc.ToolRegistryEntry `json:"tools"`
}

// Manager reads and mutates the tools registry at registryPath.
// All registry writes are atomic (temp file + os.Rename, mode 0o600).
// The registry path itself is lstat-checked to refuse symlinks before every
// read and write.
type Manager struct {
	registryPath string
}

// NewManager returns a Manager backed by registryPath.
// If registryPath is empty it defaults to config.CcmcDir()/tools.json.
func NewManager(registryPath string) *Manager {
	if registryPath == "" {
		registryPath = filepath.Join(config.CcmcDir(), "tools.json")
	}
	return &Manager{registryPath: registryPath}
}

// List returns all registry entries sorted ascending by name.
// If the registry file does not exist, an empty slice is returned (not an error).
// Returns an error if the file exists but is malformed JSON, or is a symlink.
func (m *Manager) List() ([]ccmc.ToolRegistryEntry, error) {
	reg, err := m.load()
	if err != nil {
		return nil, err
	}
	entries := append([]ccmc.ToolRegistryEntry(nil), reg.Tools...)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Get returns the first registry entry whose name matches. Returns ErrToolNotFound
// if no entry exists with that name.
func (m *Manager) Get(name string) (ccmc.ToolRegistryEntry, error) {
	reg, err := m.load()
	if err != nil {
		return ccmc.ToolRegistryEntry{}, err
	}
	for _, e := range reg.Tools {
		if e.Name == name {
			return e, nil
		}
	}
	return ccmc.ToolRegistryEntry{}, fmt.Errorf("%w: %s", ErrToolNotFound, name)
}

// Add appends entry to the registry. If an entry with the same (name, scope)
// already exists, it is updated in place (InstalledAt is refreshed) rather than
// duplicated. Idempotent on (name, scope).
func (m *Manager) Add(entry ccmc.ToolRegistryEntry) error {
	reg, err := m.load()
	if err != nil {
		return err
	}
	for i, e := range reg.Tools {
		if e.Name == entry.Name && e.Scope == entry.Scope {
			// Update in place — refresh install timestamp and any mutable fields.
			entry.InstalledAt = time.Now().Format(time.RFC3339)
			reg.Tools[i] = entry
			return m.save(reg)
		}
	}
	if entry.InstalledAt == "" {
		entry.InstalledAt = time.Now().Format(time.RFC3339)
	}
	reg.Tools = append(reg.Tools, entry)
	return m.save(reg)
}

// Remove deletes the named entry from the registry, removes the corresponding
// mcpServers entry from the scope's settings.json (for mcp types), removes any
// on-disk files for skill/agent/plugin types, and optionally deletes the clone
// directory.
//
// The clone directory removal is guarded: if clone_path does not share the
// config.CcmcDir() prefix (or a tilde-expanded equivalent), the operation is
// refused to defend against a corrupt registry pointing at arbitrary paths.
func (m *Manager) Remove(name string, deleteClone bool) error {
	reg, err := m.load()
	if err != nil {
		return err
	}

	idx := -1
	var entry ccmc.ToolRegistryEntry
	for i, e := range reg.Tools {
		if e.Name == name {
			idx = i
			entry = e
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}

	// Remove entry from the registry slice.
	reg.Tools = append(reg.Tools[:idx], reg.Tools[idx+1:]...)
	if err := m.save(reg); err != nil {
		return err
	}

	// Remove from scope settings.json for MCP types (stdio / sse).
	if entry.Type == "stdio" || entry.Type == "sse" {
		settingsPath := scopeSettingsPath(entry.Scope)
		if err := removeFromMCPSettings(settingsPath, name); err != nil {
			return fmt.Errorf("manager: remove from settings %s: %w", settingsPath, err)
		}
	}

	// Remove on-disk artifacts for skill / agent / plugin types.
	if err := removeArtifact(entry); err != nil {
		return err
	}

	// Optionally delete the clone directory.
	if deleteClone && entry.ClonePath != "" {
		if err := safeRemoveClone(entry.ClonePath); err != nil {
			return err
		}
	}

	return nil
}

// gitPullCmd is a package-level variable so tests can stub the git invocation
// without spawning a real process. The "--" separator prevents git from
// misinterpreting a path as a flag (C-2/M-2 defense-in-depth).
var gitPullCmd = func(clonePath string) *exec.Cmd {
	return exec.Command("git", "-C", "--", clonePath, "pull")
}

// Update runs git pull in the clone directory for the named tool. It is a no-op
// (returns nil) for types without a clone path (skill, agent, sse). An
// informational note is written to stderr for those types.
//
// M-2: the clone path is prefix-checked against config.CcmcDir() before
// invoking git pull, mirroring the guard in safeRemoveClone.
func (m *Manager) Update(name string) error {
	entry, err := m.Get(name)
	if err != nil {
		return err
	}

	if entry.ClonePath == "" {
		fmt.Fprintf(os.Stderr, "manager: nothing to update for type %s (no clone path)\n", entry.Type)
		return nil
	}

	// M-2: guard against a corrupt registry pointing clone_path outside the
	// allowed clone directory. Mirrors safeRemoveClone's logic.
	abs, err := filepath.Abs(entry.ClonePath)
	if err != nil {
		return fmt.Errorf("manager: resolve clone path %s: %w", entry.ClonePath, err)
	}
	allowedAbs, err := filepath.Abs(config.CcmcDir())
	if err != nil {
		return fmt.Errorf("manager: resolve allowed base: %w", err)
	}
	if !strings.HasPrefix(abs, allowedAbs+string(filepath.Separator)) && abs != allowedAbs {
		return fmt.Errorf("manager: clone_path %s is outside allowed directory %s — refusing to update", entry.ClonePath, config.CcmcDir())
	}

	cmd := gitPullCmd(entry.ClonePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("manager: git pull in %s: %w", entry.ClonePath, err)
	}
	return nil
}

// ── internal helpers ─────────────────────────────────────────────────────────

// registryLstatGuard returns an error if registryPath exists and is a symlink.
// Missing path is allowed (registry doesn't exist yet). Any other Lstat error
// is surfaced as-is.
func (m *Manager) registryLstatGuard() error {
	fi, err := os.Lstat(m.registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("lstat %s: %w", m.registryPath, err)
	}
	if fi.Mode().Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %s is a symlink — refusing to proceed", m.registryPath)
	}
	return nil
}

// load reads and decodes the registry. Missing file → empty registry (no error).
// Symlink at registry path → error. Malformed JSON → error.
func (m *Manager) load() (toolsRegistry, error) {
	if err := m.registryLstatGuard(); err != nil {
		return toolsRegistry{}, fmt.Errorf("manager: symlink check %s: %w", m.registryPath, err)
	}
	b, err := readFileOrEmpty(m.registryPath)
	if err != nil {
		return toolsRegistry{}, fmt.Errorf("manager: read %s: %w", m.registryPath, err)
	}
	if len(b) == 0 {
		return toolsRegistry{}, nil
	}
	var reg toolsRegistry
	if err := json.Unmarshal(b, &reg); err != nil {
		return toolsRegistry{}, fmt.Errorf("manager: parse %s: %w", m.registryPath, err)
	}
	if reg.Tools == nil {
		reg.Tools = []ccmc.ToolRegistryEntry{}
	}
	return reg, nil
}

// save atomically writes reg to m.registryPath (temp file + os.Rename, 0o600).
func (m *Manager) save(reg toolsRegistry) error {
	dir := filepath.Dir(m.registryPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("manager: mkdir %s: %w", dir, err)
	}

	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("manager: encode registry: %w", err)
	}
	b = append(b, '\n')

	f, err := os.CreateTemp(dir, ".ccmc-tools-*")
	if err != nil {
		return fmt.Errorf("manager: create temp: %w", err)
	}
	tmpPath := f.Name()

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("manager: chmod temp: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("manager: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("manager: sync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("manager: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, m.registryPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("manager: rename to %s: %w", m.registryPath, err)
	}
	return nil
}

// scopeSettingsPath returns the absolute path to settings.json for scope.
// "global" → config.ClaudeSettingsPath(); any other value is treated as an
// absolute project path and the settings live at <scope>/.claude/settings.json.
func scopeSettingsPath(scope string) string {
	if scope == "global" {
		return config.ClaudeSettingsPath()
	}
	return filepath.Join(scope, ".claude", "settings.json")
}

// removeFromMCPSettings opens the settings.json at path, deletes the key for
// name under mcpServers, and writes it back via config.WriteSettings.
// If the file doesn't exist or mcpServers doesn't contain name, this is a no-op.
func removeFromMCPSettings(settingsPath, name string) error {
	m, err := config.ReadSettings(settingsPath)
	if err != nil {
		return err
	}
	raw, ok := m["mcpServers"]
	if !ok {
		return nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil {
		return fmt.Errorf("parse mcpServers: %w", err)
	}
	if _, exists := servers[name]; !exists {
		return nil
	}
	delete(servers, name)
	encoded, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("re-encode mcpServers: %w", err)
	}
	m["mcpServers"] = json.RawMessage(encoded)
	return config.WriteSettings(settingsPath, m)
}

// removeArtifact deletes on-disk files for skill, agent, and plugin entry types.
// For MCP (stdio/sse) types there are no files to remove here — they are only
// registered in settings.json which removeFromMCPSettings handles.
func removeArtifact(entry ccmc.ToolRegistryEntry) error {
	claudeBase := artifactClaudeBase(entry.Scope)
	switch entry.Type {
	case "skill":
		p := filepath.Join(claudeBase, "skills", entry.Name)
		return removeIfExists(p)
	case "agent":
		p := filepath.Join(claudeBase, "agents", entry.Name+".md")
		return removeIfExists(p)
	case "plugin":
		p := filepath.Join(claudeBase, "plugins", entry.Name)
		return removeIfExists(p)
	}
	return nil
}

// artifactClaudeBase returns the .claude directory root for a given scope value.
// "global" → config.ClaudeDir(); project scopes → <scope>/.claude.
func artifactClaudeBase(scope string) string {
	if scope == "global" {
		return config.ClaudeDir()
	}
	return filepath.Join(scope, ".claude")
}

// safeRemoveClone removes clonePath via os.RemoveAll, but only if the path is
// contained within config.CcmcDir(). This guards against a corrupt registry
// pointing clone_path at an arbitrary directory (e.g. "/", "~", or a home dir).
func safeRemoveClone(clonePath string) error {
	// Resolve to absolute so relative paths can't slip past the prefix check.
	abs, err := filepath.Abs(clonePath)
	if err != nil {
		return fmt.Errorf("manager: resolve clone path %s: %w", clonePath, err)
	}

	allowedBase := config.CcmcDir()
	allowedAbs, err := filepath.Abs(allowedBase)
	if err != nil {
		return fmt.Errorf("manager: resolve allowed base %s: %w", allowedBase, err)
	}

	// Ensure abs is strictly inside allowedAbs (with separator to avoid prefix
	// collisions like /home/user/.ccmcevil matching /home/user/.ccmc).
	if !strings.HasPrefix(abs, allowedAbs+string(filepath.Separator)) && abs != allowedAbs {
		return fmt.Errorf(
			"manager: clone_path %s is outside allowed directory %s — refusing to delete",
			clonePath, allowedBase,
		)
	}

	return os.RemoveAll(abs)
}

// removeIfExists calls os.RemoveAll on path only if it exists.
func removeIfExists(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("manager: stat %s: %w", path, err)
	}
	return os.RemoveAll(path)
}

// readFileOrEmpty reads a file and returns nil (no error) when it doesn't exist.
func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}
