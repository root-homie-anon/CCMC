package integrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// Sentinel errors returned by Installer.Install.
var (
	// ErrUnknownToolType is returned when none of the detection heuristics match
	// the repo's content. The message includes the evidence examined.
	ErrUnknownToolType = errors.New("installer: cannot determine tool type")

	// ErrCloneFailed is returned when the git clone step fails.
	ErrCloneFailed = errors.New("installer: git clone failed")

	// ErrConfigWriteFailed is returned when writing to settings.json fails.
	ErrConfigWriteFailed = errors.New("installer: config write failed")

	// ErrToolAlreadyInstalled is returned when the registry already contains an
	// entry for the same SourceURL+Scope combination and Force is false.
	ErrToolAlreadyInstalled = errors.New("installer: tool already installed")

	// ErrStalePartialInstall is returned when a previous partial install left a
	// target directory in place. The caller should pass --force to remove it and
	// retry, or run `ccmc tools rm <name>` to clean up manually.
	ErrStalePartialInstall = errors.New("installer: stale partial install")

	// ErrSymlinkRace is returned when prepareTargetDir detects a symlink at the
	// target path immediately after RemoveAll — indicating a TOCTOU symlink-redirect
	// attack. The install is aborted (fail-closed).
	ErrSymlinkRace = errors.New("installer: symlink detected at target dir after removal — possible TOCTOU attack")
)

// cloneCmd is a package-level seam that tests replace to avoid real git invocations.
// The "--" separator is mandatory: it prevents git from misinterpreting a URL that
// starts with "--" as a flag (defense-in-depth against flag injection via URL).
var cloneCmd = func(ctx context.Context, url, dest string) error {
	// Scheme allowlist: only https://github.com/ is accepted.
	if !strings.HasPrefix(url, "https://github.com/") {
		return fmt.Errorf("cloneCmd: URL %q is not an allowed GitHub HTTPS URL", url)
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--", url, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// npmInstallCmd is a package-level seam for npm install.
var npmInstallCmd = func(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "npm", "install")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// pipInstallCmd is a package-level seam for pip install -e .
var pipInstallCmd = func(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "pip", "install", "-e", ".")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// uvSyncCmd is a package-level seam for uv sync.
var uvSyncCmd = func(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "uv", "sync")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// InstallSource describes what to install and where.
type InstallSource struct {
	URL      string             // GitHub URL (required)
	EvalCtx  ccmc.EvalContext   // Optional — if already fetched, avoids a second GitHub round-trip
	EvalRes  ccmc.EvalResult    // Optional — if already evaluated
	Scope    string             // "global" or absolute project path; default "" = "global"
	ToolType string             // Optional override; auto-detect when empty
	Force    bool               // If true, skip duplicate-install check
}

// Installer clones, copies, and configures Claude Code tools from GitHub repos.
// Construct with NewInstaller.
type Installer struct {
	cfg         config.Config
	registryPath string
}

// NewInstaller constructs an Installer using the provided config for model, clone
// directory, and other settings.
func NewInstaller(cfg config.Config) *Installer {
	registryPath := expandTilde("~/.ccmc/tools.json")
	return &Installer{
		cfg:          cfg,
		registryPath: registryPath,
	}
}

// Install fetches, detects, installs, and registers the tool described by src.
// It returns an InstallResult describing what was done and where.
func (i *Installer) Install(ctx context.Context, src InstallSource) (ccmc.InstallResult, error) {
	// Normalise scope: empty string means global.
	if src.Scope == "" {
		src.Scope = "global"
	}

	// Resolve tool name from the URL (repo basename).
	toolName := toolNameFromURL(src.URL)

	// ── Duplicate check ───────────────────────────────────────────────────────
	if !src.Force {
		if already, err := i.isRegistered(src.URL, src.Scope); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("installer: registry check: %w", err)
		} else if already {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %s at scope %s", ErrToolAlreadyInstalled, src.URL, src.Scope)
		}
	}

	// ── Type detection ────────────────────────────────────────────────────────
	toolType := src.ToolType
	if toolType == "" {
		detected, evidence, err := detectToolType(src.EvalCtx)
		if err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %s", ErrUnknownToolType, evidence)
		}
		toolType = detected
	}

	// ── Route to type-specific installer ─────────────────────────────────────
	var result ccmc.InstallResult
	var installErr error

	switch toolType {
	case "stdio":
		result, installErr = i.installMCPStdio(ctx, src, toolName)
	case "sse":
		result, installErr = i.installMCPSSE(src, toolName)
	case "skill":
		result, installErr = i.installSkill(ctx, src, toolName)
	case "agent":
		result, installErr = i.installAgent(ctx, src, toolName)
	case "plugin":
		result, installErr = i.installPlugin(ctx, src, toolName)
	default:
		return ccmc.InstallResult{}, fmt.Errorf("%w: unrecognised type %q", ErrUnknownToolType, toolType)
	}

	if installErr != nil {
		return ccmc.InstallResult{}, installErr
	}

	// ── Append to tools registry via Manager (has lstatGuard + atomic write) ─
	entry := ccmc.ToolRegistryEntry{
		Name:        toolName,
		Type:        toolType,
		SourceURL:   src.URL,
		Scope:       src.Scope,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		ClonePath:   result.ClonePath,
	}
	mgr := NewManager(i.registryPath)
	if err := mgr.Add(entry); err != nil {
		// Non-fatal: the tool is installed but the registry couldn't be updated.
		// Log to stderr so the user is aware; do not fail the install.
		fmt.Fprintf(os.Stderr, "installer: registry append failed: %v\n", err)
	}

	return result, nil
}

// ── Type detection ─────────────────────────────────────────────────────────────

// detectToolType inspects the EvalContext fields to determine the tool type.
//
// Priority order (first match wins):
//  1. ExampleSettings has mcpServers entry with "command" field → "stdio"
//  2. ExampleSettings has mcpServers entry with "url" field     → "sse"
//  3. PackageJSON or PyprojectTOML has "SKILL.md" signal OR
//     ReadmeMarkdown mentions SKILL.md                         → "skill"
//     (SKILL.md is detected by scanning the README for the filename pattern)
//  4. ReadmeMarkdown contains agent frontmatter signals        → "agent"
//  5. ExampleSettings or README mentions plugin layout         → "plugin"
//  6. Otherwise → error with evidence summary
func detectToolType(ec ccmc.EvalContext) (toolType string, evidence string, err error) {
	// 1 & 2: MCP detection via example settings.json
	if ec.ExampleSettings != "" {
		var settings map[string]json.RawMessage
		if jsonErr := json.Unmarshal([]byte(ec.ExampleSettings), &settings); jsonErr == nil {
			if raw, ok := settings["mcpServers"]; ok {
				var servers map[string]map[string]json.RawMessage
				if jsonErr := json.Unmarshal(raw, &servers); jsonErr == nil {
					for _, srv := range servers {
						if _, hasCmd := srv["command"]; hasCmd {
							return "stdio", "mcpServers.command field present", nil
						}
						if _, hasURL := srv["url"]; hasURL {
							return "sse", "mcpServers.url field present", nil
						}
					}
				}
			}
		}
	}

	// 3: Skill detection — presence of SKILL.md in README text
	if containsSkillSignal(ec.ReadmeMarkdown) {
		return "skill", "SKILL.md mentioned in README", nil
	}

	// 4: Agent detection — README contains agent frontmatter patterns
	if containsAgentSignal(ec.ReadmeMarkdown) {
		return "agent", "agent frontmatter detected in README", nil
	}

	// 5: Plugin detection — plugin.json or claude plugin install pattern
	if containsPluginSignal(ec.ReadmeMarkdown) {
		return "plugin", "plugin layout detected in README", nil
	}

	// Collect evidence for the error message.
	var evidenceParts []string
	if ec.ExampleSettings != "" {
		evidenceParts = append(evidenceParts, "has example settings.json but no mcpServers")
	}
	if ec.PackageJSON != "" {
		evidenceParts = append(evidenceParts, "has package.json")
	}
	if ec.PyprojectTOML != "" {
		evidenceParts = append(evidenceParts, "has pyproject.toml")
	}
	if len(evidenceParts) == 0 {
		evidenceParts = append(evidenceParts, "no identifying signals found")
	}

	return "", strings.Join(evidenceParts, "; "), errors.New("no match")
}

func containsSkillSignal(readme string) bool {
	lower := strings.ToLower(readme)
	return strings.Contains(lower, "skill.md") ||
		strings.Contains(lower, "skills/") ||
		strings.Contains(lower, "user-invocable") ||
		strings.Contains(lower, "disable-model-invocation")
}

func containsAgentSignal(readme string) bool {
	lower := strings.ToLower(readme)
	// Agent frontmatter fields are strong signals.
	return (strings.Contains(lower, "agents/") && strings.Contains(lower, "frontmatter")) ||
		(strings.Contains(lower, "agents/") && strings.Contains(lower, ".md")) ||
		strings.Contains(lower, "agent frontmatter") ||
		(strings.Contains(lower, "description:") && strings.Contains(lower, "agents/"))
}

func containsPluginSignal(readme string) bool {
	lower := strings.ToLower(readme)
	return strings.Contains(lower, "plugin.json") ||
		strings.Contains(lower, "claude plugin install") ||
		strings.Contains(lower, "plugins/")
}

// ── Type-specific installers ───────────────────────────────────────────────────

func (i *Installer) installMCPStdio(ctx context.Context, src InstallSource, toolName string) (ccmc.InstallResult, error) {
	// Resolve clone destination.
	cloneBase := expandTilde(i.cfg.Integrator.CloneDir)
	cloneDest := filepath.Join(cloneBase, toolName)

	if err := os.MkdirAll(cloneBase, 0o700); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir clone dir: %v", ErrCloneFailed, err)
	}

	// Clone — idempotent: if the directory already exists, skip the clone.
	if _, err := os.Stat(cloneDest); os.IsNotExist(err) {
		if err := cloneCmd(ctx, src.URL, cloneDest); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrCloneFailed, err)
		}
	}

	// Install dependencies.
	if err := installDeps(ctx, cloneDest); err != nil {
		// Warn but continue — some repos self-contain and don't need npm/pip.
		fmt.Fprintf(os.Stderr, "installer: dep install warning: %v\n", err)
	}

	// Resolve the MCP entry command from the example settings if available.
	mcpEntry, warn, err := resolveMCPStdioEntry(toolName, cloneDest, src.EvalCtx.ExampleSettings)
	if err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrConfigWriteFailed, err)
	}
	if warn != "" {
		fmt.Fprintf(os.Stderr, "installer: %s\n", warn)
	}

	// Write to the target scope settings.json.
	settingsPath := resolveSettingsPath(src.Scope)
	if err := mergeMCPServer(settingsPath, toolName, mcpEntry); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrConfigWriteFailed, err)
	}

	return ccmc.InstallResult{
		Name:       toolName,
		Type:       "stdio",
		SourceURL:  src.URL,
		Scope:      src.Scope,
		ClonePath:  cloneDest,
		ConfigPath: settingsPath,
	}, nil
}

func (i *Installer) installMCPSSE(src InstallSource, toolName string) (ccmc.InstallResult, error) {
	// SSE MCPs are URL-only — no clone, no dep install.
	sseURL := extractSSEURL(src.EvalCtx.ExampleSettings, src.URL)

	entry := map[string]json.RawMessage{
		"url": mustMarshal(sseURL),
	}

	settingsPath := resolveSettingsPath(src.Scope)
	if err := mergeMCPServer(settingsPath, toolName, entry); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrConfigWriteFailed, err)
	}

	return ccmc.InstallResult{
		Name:       toolName,
		Type:       "sse",
		SourceURL:  src.URL,
		Scope:      src.Scope,
		ConfigPath: settingsPath,
	}, nil
}

func (i *Installer) installSkill(ctx context.Context, src InstallSource, toolName string) (ccmc.InstallResult, error) {
	// Resolve source skill dir from the cloned repo or the EvalCtx.
	// For skills, we expect the tool to have been pre-cloned or the files
	// accessible. Since we don't clone for non-MCP types in the install flow,
	// we clone into a temp dir and copy.
	cloneBase := expandTilde(i.cfg.Integrator.CloneDir)
	cloneDest := filepath.Join(cloneBase, toolName)

	if _, err := os.Stat(cloneDest); os.IsNotExist(err) {
		if err := os.MkdirAll(cloneBase, 0o700); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir: %v", ErrCloneFailed, err)
		}
		if err := cloneCmd(ctx, src.URL, cloneDest); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrCloneFailed, err)
		}
	}

	// Determine target directory based on scope.
	targetBase := resolveSkillDir(src.Scope, toolName)
	// allowedSkillsBase is the parent skills directory; used by prepareTargetDir
	// as the RemoveAll safety boundary so a corrupt targetBase cannot escape ~/.claude/.
	allowedSkillsBase := filepath.Dir(targetBase)

	// NF-3 / TOCTOU: prepareTargetDir removes any stale partial install and then
	// atomically creates targetBase via a single mkdir(2) syscall. This eliminates
	// the race window between RemoveAll and the first write. On success, targetBase
	// exists as a real directory owned by this process — no symlink redirect possible.
	if err := prepareTargetDir(targetBase, allowedSkillsBase, src.Force); err != nil {
		return ccmc.InstallResult{}, err
	}

	// Copy the skill dir contents (or the whole repo if no specific skills/ subdir).
	srcDir := findSkillDir(cloneDest, toolName)
	if err := copyDir(srcDir, targetBase); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: copy skill: %v", ErrConfigWriteFailed, err)
	}

	return ccmc.InstallResult{
		Name:       toolName,
		Type:       "skill",
		SourceURL:  src.URL,
		Scope:      src.Scope,
		ClonePath:  cloneDest,
		ConfigPath: targetBase,
	}, nil
}

func (i *Installer) installAgent(ctx context.Context, src InstallSource, toolName string) (ccmc.InstallResult, error) {
	cloneBase := expandTilde(i.cfg.Integrator.CloneDir)
	cloneDest := filepath.Join(cloneBase, toolName)

	if _, err := os.Stat(cloneDest); os.IsNotExist(err) {
		if err := os.MkdirAll(cloneBase, 0o700); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir: %v", ErrCloneFailed, err)
		}
		if err := cloneCmd(ctx, src.URL, cloneDest); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrCloneFailed, err)
		}
	}

	// Find the agent markdown file in the cloned repo.
	agentFile := findAgentFile(cloneDest, toolName)
	if agentFile == "" {
		return ccmc.InstallResult{}, fmt.Errorf("%w: no agent .md file found in repo", ErrConfigWriteFailed)
	}

	// Determine the target agents directory.
	agentDir := resolveAgentDir(src.Scope)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir agents dir: %v", ErrConfigWriteFailed, err)
	}

	destFile := filepath.Join(agentDir, filepath.Base(agentFile))
	if err := copyFile(agentFile, destFile); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: copy agent: %v", ErrConfigWriteFailed, err)
	}

	return ccmc.InstallResult{
		Name:       toolName,
		Type:       "agent",
		SourceURL:  src.URL,
		Scope:      src.Scope,
		ClonePath:  cloneDest,
		ConfigPath: destFile,
	}, nil
}

func (i *Installer) installPlugin(ctx context.Context, src InstallSource, toolName string) (ccmc.InstallResult, error) {
	// H-1: scheme allowlist — only GitHub HTTPS URLs are accepted.
	if !strings.HasPrefix(src.URL, "https://github.com/") {
		return ccmc.InstallResult{}, fmt.Errorf("installPlugin: URL %q is not an allowed GitHub HTTPS URL", src.URL)
	}

	// Prefer claude plugin install if the binary is on PATH.
	if claudePath, err := exec.LookPath("claude"); err == nil {
		cmd := exec.CommandContext(ctx, claudePath, "plugin", "install", src.URL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// Fall through to manual copy.
			fmt.Fprintf(os.Stderr, "installer: claude plugin install failed: %v — falling back to manual copy\n", err)
		} else {
			return ccmc.InstallResult{
				Name:      toolName,
				Type:      "plugin",
				SourceURL: src.URL,
				Scope:     src.Scope,
			}, nil
		}
	}

	// Manual copy fallback: clone and copy to plugins dir.
	cloneBase := expandTilde(i.cfg.Integrator.CloneDir)
	cloneDest := filepath.Join(cloneBase, toolName)

	if _, err := os.Stat(cloneDest); os.IsNotExist(err) {
		if err := os.MkdirAll(cloneBase, 0o700); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir: %v", ErrCloneFailed, err)
		}
		if err := cloneCmd(ctx, src.URL, cloneDest); err != nil {
			return ccmc.InstallResult{}, fmt.Errorf("%w: %v", ErrCloneFailed, err)
		}
	}

	pluginsDir := resolvePluginDir(src.Scope, toolName)
	allowedPluginsBase := filepath.Dir(pluginsDir)
	if err := os.MkdirAll(allowedPluginsBase, 0o700); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: mkdir plugins dir: %v", ErrConfigWriteFailed, err)
	}
	// NF-3: guard against stale partial install before copyDir.
	if err := prepareTargetDir(pluginsDir, allowedPluginsBase, src.Force); err != nil {
		return ccmc.InstallResult{}, err
	}
	if err := copyDir(cloneDest, pluginsDir); err != nil {
		return ccmc.InstallResult{}, fmt.Errorf("%w: copy plugin: %v", ErrConfigWriteFailed, err)
	}

	return ccmc.InstallResult{
		Name:       toolName,
		Type:       "plugin",
		SourceURL:  src.URL,
		Scope:      src.Scope,
		ClonePath:  cloneDest,
		ConfigPath: pluginsDir,
	}, nil
}

// ── Registry helpers ───────────────────────────────────────────────────────────

// isRegistered returns true if tools.json already contains an entry with the
// same SourceURL and Scope. Delegates to Manager so both registry readers share
// the same lstatGuard and format (M-1).
func (i *Installer) isRegistered(sourceURL, scope string) (bool, error) {
	entries, err := i.loadRegistry()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.SourceURL == sourceURL && e.Scope == scope {
			return true, nil
		}
	}
	return false, nil
}

// loadRegistry reads ~/.ccmc/tools.json via Manager and returns the entries.
// Returns an empty slice (not an error) when the file does not exist.
func (i *Installer) loadRegistry() ([]ccmc.ToolRegistryEntry, error) {
	return NewManager(i.registryPath).List()
}


// ── Path resolution helpers ───────────────────────────────────────────────────

// resolveSettingsPath returns the absolute path to settings.json for the given scope.
// scope == "global" uses ~/.claude/settings.json; otherwise scope is the absolute
// project path and we use <scope>/.claude/settings.json.
func resolveSettingsPath(scope string) string {
	if scope == "" || scope == "global" {
		return config.ClaudeSettingsPath()
	}
	return filepath.Join(scope, ".claude", "settings.json")
}

// resolveSkillDir returns the target directory for a skill installation.
func resolveSkillDir(scope, toolName string) string {
	if scope == "" || scope == "global" {
		return expandTilde(filepath.Join("~/.claude/skills", toolName))
	}
	return filepath.Join(scope, ".claude", "skills", toolName)
}

// resolveAgentDir returns the target directory for agent markdown files.
func resolveAgentDir(scope string) string {
	if scope == "" || scope == "global" {
		return expandTilde("~/.claude/agents")
	}
	return filepath.Join(scope, ".claude", "agents")
}

// resolvePluginDir returns the target directory for a manually-copied plugin.
func resolvePluginDir(scope, toolName string) string {
	if scope == "" || scope == "global" {
		return expandTilde(filepath.Join("~/.claude/plugins", toolName))
	}
	return filepath.Join(scope, ".claude", "plugins", toolName)
}

// ── settings.json merge helpers ───────────────────────────────────────────────

// mergeMCPServer reads the current settings.json, merges a new mcpServers entry,
// and writes back via config.WriteSettings (which handles .bak, symlink guard,
// atomic write — the same safety guarantees used by the hooks installer).
func mergeMCPServer(settingsPath, serverName string, entry map[string]json.RawMessage) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return fmt.Errorf("mergeMCPServer: mkdir: %w", err)
	}

	root, err := config.ReadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("mergeMCPServer: read: %w", err)
	}

	// Decode the existing mcpServers block (or start fresh).
	var mcpServers map[string]map[string]json.RawMessage
	if raw, ok := root["mcpServers"]; ok {
		if jsonErr := json.Unmarshal(raw, &mcpServers); jsonErr != nil {
			return fmt.Errorf("mergeMCPServer: parse mcpServers: %w", jsonErr)
		}
	}
	if mcpServers == nil {
		mcpServers = make(map[string]map[string]json.RawMessage)
	}

	// Merge-not-overwrite: only add if not already present.
	if _, exists := mcpServers[serverName]; !exists {
		mcpServers[serverName] = entry
	}

	mcpRaw, err := json.Marshal(mcpServers)
	if err != nil {
		return fmt.Errorf("mergeMCPServer: encode: %w", err)
	}
	root["mcpServers"] = json.RawMessage(mcpRaw)

	return config.WriteSettings(settingsPath, root)
}

// allowedMCPEntryKeys is the set of keys permitted when propagating an MCP
// server entry from a repo's example settings.json into the user's settings.
// Arbitrary keys from untrusted repos are dropped (H-2).
var allowedMCPEntryKeys = map[string]bool{
	"command": true,
	"args":    true,
	"env":     true,
}

// allowedMCPCommands is the set of bare interpreter names that may appear as
// the "command" field in a repo's example settings.json. Absolute paths are
// also permitted if they are under the cloned repo's own directory (H-2).
var allowedMCPCommands = map[string]bool{
	"node":    true,
	"python":  true,
	"python3": true,
	"npx":     true,
	"uvx":     true,
	"bun":     true,
	"deno":    true,
}

// resolveMCPStdioEntry tries to extract the command and args from the repo's
// example settings.json. Falls back to a sensible default using the cloned repo
// path and warns the caller when it cannot parse the example.
//
// H-2: only keys in allowedMCPEntryKeys are propagated; the command value is
// validated against allowedMCPCommands or must be an absolute path under cloneDest.
// Returns (nil, "", error) when the command from the example is unsafe.
func resolveMCPStdioEntry(toolName, cloneDest, exampleSettings string) (map[string]json.RawMessage, string, error) {
	if exampleSettings != "" {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(exampleSettings), &settings); err == nil {
			if raw, ok := settings["mcpServers"]; ok {
				var servers map[string]json.RawMessage
				if err := json.Unmarshal(raw, &servers); err == nil {
					for _, srvRaw := range servers {
						var srv map[string]json.RawMessage
						if err := json.Unmarshal(srvRaw, &srv); err == nil {
							if cmdRaw, hasCmd := srv["command"]; hasCmd {
								// Decode command string value.
								var cmdStr string
								if err := json.Unmarshal(cmdRaw, &cmdStr); err != nil {
									return nil, "", fmt.Errorf("resolveMCPStdioEntry: command is not a string: %w", err)
								}
								// Validate command (H-2).
								if err := validateMCPCommand(cmdStr, cloneDest); err != nil {
									return nil, "", err
								}
								// Propagate only allowlisted keys.
								filtered := make(map[string]json.RawMessage, 3)
								for k, v := range srv {
									if allowedMCPEntryKeys[k] {
										filtered[k] = v
									}
								}
								return filtered, "", nil
							}
						}
					}
				}
			}
		}
	}

	// Fallback: node index.js or python -m <toolName> depending on what we find.
	var cmd string
	var args []json.RawMessage

	if _, err := os.Stat(filepath.Join(cloneDest, "package.json")); err == nil {
		cmd = "node"
		indexCandidates := []string{"index.js", "src/index.js", "dist/index.js", "build/index.js"}
		entrypoint := "index.js"
		for _, c := range indexCandidates {
			if _, err := os.Stat(filepath.Join(cloneDest, c)); err == nil {
				entrypoint = c
				break
			}
		}
		args = []json.RawMessage{mustMarshal(filepath.Join(cloneDest, entrypoint))}
	} else if _, err := os.Stat(filepath.Join(cloneDest, "pyproject.toml")); err == nil {
		cmd = "python"
		args = []json.RawMessage{mustMarshal("-m"), mustMarshal(toolName)}
	} else {
		cmd = filepath.Join(cloneDest, toolName)
	}

	entry := map[string]json.RawMessage{
		"command": mustMarshal(cmd),
	}
	if len(args) > 0 {
		argsJSON, _ := json.Marshal(args)
		entry["args"] = argsJSON
	}

	warn := fmt.Sprintf("could not parse command from example settings.json for %s — using default: %s; verify mcpServers entry is correct", toolName, cmd)
	return entry, warn, nil
}

// validateMCPCommand returns nil when cmd is a known interpreter name or an
// absolute path that is contained within cloneDest. All other values are rejected
// to prevent writing arbitrary executables into the user's settings.json (H-2).
func validateMCPCommand(cmd, cloneDest string) error {
	// Bare known-interpreter names are always accepted.
	if allowedMCPCommands[cmd] {
		return nil
	}
	// Absolute paths must be under the cloned repo.
	if filepath.IsAbs(cmd) {
		cleanDest := filepath.Clean(cloneDest)
		cleanCmd := filepath.Clean(cmd)
		if strings.HasPrefix(cleanCmd, cleanDest+string(filepath.Separator)) || cleanCmd == cleanDest {
			return nil
		}
		return fmt.Errorf("validateMCPCommand: command %q is an absolute path outside the cloned repo (%s) — refusing to write settings.json", cmd, cloneDest)
	}
	// Relative paths and anything else are rejected.
	return fmt.Errorf("validateMCPCommand: command %q is not a known interpreter and is not an absolute path under the repo — refusing to write settings.json", cmd)
}

// extractSSEURL attempts to get the SSE URL from the example settings.json.
// Falls back to the source URL if not found.
func extractSSEURL(exampleSettings, fallbackURL string) string {
	if exampleSettings != "" {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(exampleSettings), &settings); err == nil {
			if raw, ok := settings["mcpServers"]; ok {
				var servers map[string]json.RawMessage
				if err := json.Unmarshal(raw, &servers); err == nil {
					for _, srvRaw := range servers {
						var srv map[string]string
						if err := json.Unmarshal(srvRaw, &srv); err == nil {
							if u, ok := srv["url"]; ok && u != "" {
								return u
							}
						}
					}
				}
			}
		}
	}
	return fallbackURL
}

// ── Dep install ────────────────────────────────────────────────────────────────

// installDeps runs the appropriate package manager in dir based on what manifest
// files are present. Tries npm first (package.json), then pyproject.toml (uv
// sync preferred, pip install -e . fallback).
func installDeps(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return npmInstallCmd(ctx, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		// Try uv first; if not on PATH, fall back to pip.
		if _, err := exec.LookPath("uv"); err == nil {
			return uvSyncCmd(ctx, dir)
		}
		return pipInstallCmd(ctx, dir)
	}
	return nil // no manifest found; nothing to install
}

// ── Filesystem helpers ─────────────────────────────────────────────────────────

// findSkillDir returns the best candidate source directory for a skill installation.
// Checks for skills/<toolName>/ then skills/ then falls back to the repo root.
func findSkillDir(cloneDest, toolName string) string {
	candidates := []string{
		filepath.Join(cloneDest, "skills", toolName),
		filepath.Join(cloneDest, "skills"),
		cloneDest,
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return cloneDest
}

// findAgentFile returns the first *.md file under agents/ in cloneDest, or a
// root *.md whose frontmatter indicates it is an agent, or empty string.
func findAgentFile(cloneDest, toolName string) string {
	// Check agents/ subdir first.
	agentsDir := filepath.Join(cloneDest, "agents")
	if _, err := os.Stat(agentsDir); err == nil {
		pattern := filepath.Join(agentsDir, "*.md")
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return matches[0]
		}
	}

	// Fall back to root *.md files.
	pattern := filepath.Join(cloneDest, "*.md")
	if matches, err := filepath.Glob(pattern); err == nil {
		for _, m := range matches {
			base := strings.ToLower(filepath.Base(m))
			// Skip README.
			if base == "readme.md" || base == "readme" {
				continue
			}
			return m
		}
	}

	// Try <toolName>.md at root.
	candidate := filepath.Join(cloneDest, toolName+".md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	return ""
}

// prepareTargetDir ensures targetDir is ready for a fresh copyDir and atomically
// creates it, closing the TOCTOU symlink-redirect window in the --force path.
//
// Behaviour when targetDir already exists:
//   - force=false: return ErrStalePartialInstall with a guidance message so the
//     user can clean up intentionally (`ccmc tools rm <name>` or --force).
//   - force=true: remove targetDir entirely (subject to a prefix safety check),
//     then atomically create a new real directory at the same path.
//
// In all cases the function guarantees that on success:
//  1. targetDir exists as a real directory (not a symlink).
//  2. The directory was created by this call via os.Mkdir (atomic mkdir(2)),
//     eliminating the window between removal and the caller's first write.
//
// If a symlink appears at targetDir between RemoveAll and os.Mkdir (the race
// window), os.Mkdir fails with EEXIST/ENOTDIR and we return ErrSymlinkRace —
// the install aborts fail-closed rather than writing into the redirect target.
// A post-Mkdir lstat provides defense-in-depth: if os.Mkdir somehow succeeded
// through a symlink (impossible on POSIX mkdir(2) but belt-and-suspenders),
// the lstat check will catch and reject it.
func prepareTargetDir(targetDir, allowedBase string, force bool) error {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("prepareTargetDir: resolve target %s: %w", targetDir, err)
	}
	absAllowed, err := filepath.Abs(allowedBase)
	if err != nil {
		return fmt.Errorf("prepareTargetDir: resolve allowed base %s: %w", allowedBase, err)
	}

	// Check whether targetDir currently exists (using Lstat so a symlink is not
	// followed — we want to know about the symlink itself, not its target).
	fi, statErr := os.Lstat(absTarget)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("prepareTargetDir: lstat %s: %w", absTarget, statErr)
	}

	if exists {
		if !force {
			return fmt.Errorf("%w: stale partial install detected at %s; run `ccmc tools rm <name>` first or re-run with --force",
				ErrStalePartialInstall, absTarget)
		}

		// Safety check: targetDir must be under allowedBase before RemoveAll.
		if !strings.HasPrefix(absTarget, absAllowed+string(filepath.Separator)) && absTarget != absAllowed {
			return fmt.Errorf("prepareTargetDir: %s is outside allowed base %s — refusing RemoveAll", absTarget, absAllowed)
		}

		// Refuse to RemoveAll a symlink directly — it would remove the link, not its
		// target, but we still check as an explicit guard.
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("prepareTargetDir: %s is a symlink — refusing RemoveAll", absTarget)
		}

		if err := os.RemoveAll(absTarget); err != nil {
			return fmt.Errorf("prepareTargetDir: remove stale dir %s: %w", absTarget, err)
		}
	}

	// Ensure the parent directory chain exists. MkdirAll on the parent (not the
	// target leaf) is safe — we only need single-level atomicity at absTarget.
	if err := os.MkdirAll(filepath.Dir(absTarget), 0o700); err != nil {
		return fmt.Errorf("prepareTargetDir: mkdir parent of %s: %w", absTarget, err)
	}

	// Atomically create the directory. os.Mkdir uses a single mkdir(2) syscall:
	// it fails with EEXIST if any path (including a symlink) now occupies absTarget,
	// closing the window between the RemoveAll above and the caller's first write.
	if err := os.Mkdir(absTarget, 0o700); err != nil {
		if os.IsExist(err) {
			// Something appeared in the race window — treat as a symlink-redirect attempt.
			return fmt.Errorf("%w: path %s", ErrSymlinkRace, absTarget)
		}
		return fmt.Errorf("prepareTargetDir: mkdir %s: %w", absTarget, err)
	}

	// Defense-in-depth: lstat the newly created path and confirm it is a real
	// directory, not a symlink. POSIX mkdir(2) cannot create through a symlink, but
	// this check catches any future platform regression or unusual filesystem.
	postFI, err := os.Lstat(absTarget)
	if err != nil {
		return fmt.Errorf("prepareTargetDir: post-mkdir lstat %s: %w", absTarget, err)
	}
	if postFI.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: path %s is a symlink after mkdir — aborting", ErrSymlinkRace, absTarget)
	}
	if !postFI.IsDir() {
		return fmt.Errorf("prepareTargetDir: %s is not a directory after mkdir (mode %s) — aborting", absTarget, postFI.Mode())
	}

	return nil
}

// copyDir recursively copies src directory contents into dst. dst is created
// if it does not exist.
//
// H-3: each entry is Lstat'd before copying. Symlinks in the source are skipped
// with a warning (refusing-the-whole-install for a symlink would be too
// disruptive; skipping is the right balance). Path-traversal entries (where
// filepath.Clean(target) escapes dst) are refused with an error.
func copyDir(src, dst string) error {
	cleanDst := filepath.Clean(dst)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		// Path-traversal guard: ensure target stays inside dst (H-3).
		if !strings.HasPrefix(filepath.Clean(target), cleanDst+string(filepath.Separator)) && filepath.Clean(target) != cleanDst {
			return fmt.Errorf("copyDir: path traversal detected: %q escapes destination %q", path, dst)
		}

		// Use Lstat to detect symlinks before following them (H-3).
		fi, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// Skip symlinks in source with a warning — do not abort the entire install.
			fmt.Fprintf(os.Stderr, "installer: copyDir: skipping symlink %q\n", path)
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target)
	})
}

// copyFile copies a single file from src to dst, creating the destination
// directory if needed.
//
// H-3 source side: O_RDONLY|O_NOFOLLOW prevents following a source symlink.
// H-3 dst side: O_CREATE|O_EXCL|O_NOFOLLOW prevents overwriting via a symlink
// at the destination; if the destination already exists, we remove it first
// (non-symlink regular files only) to achieve idempotent behaviour.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	// Open source with O_NOFOLLOW to refuse symlinks (H-3).
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if isELOOP(err) {
			fmt.Fprintf(os.Stderr, "installer: copyFile: skipping symlink source %q\n", src)
			return nil
		}
		return err
	}
	defer in.Close()

	// Determine source permissions so executable bits are preserved.
	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if srcInfo.Mode()&0o111 != 0 {
		perm = 0o755
	}

	// If dst already exists and is a regular file, remove it so O_EXCL works
	// idempotently. Refuse if dst is itself a symlink (H-3).
	if dstInfo, err := os.Lstat(dst); err == nil {
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("copyFile: destination %q is a symlink — refusing to write", dst)
		}
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("copyFile: remove existing dst %q: %w", dst, err)
		}
	}

	// O_EXCL ensures we only create a new file; combined with O_NOFOLLOW
	// we refuse to write through a symlink that may have appeared between
	// the Lstat above and now (H-3).
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// isELOOP reports whether err indicates a symlink loop (ELOOP), which is what
// O_NOFOLLOW returns when the path itself is a symlink.
func isELOOP(err error) bool {
	// os.PathError wraps the syscall error.
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err == syscall.ELOOP
	}
	return false
}

// toolNameFromURL extracts the repo name from a GitHub URL.
// "https://github.com/owner/my-tool" → "my-tool"
func toolNameFromURL(rawURL string) string {
	_, repo, err := ParseURL(rawURL)
	if err != nil || repo == "" {
		// Fallback: use the last path segment.
		parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
		if len(parts) > 0 {
			return strings.TrimSuffix(parts[len(parts)-1], ".git")
		}
		return "unknown-tool"
	}
	return repo
}

// expandTilde replaces a leading "~/" with the user's home directory.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// mustMarshal JSON-encodes v and panics on error. Only used for literals where
// encode failure is impossible (string, slice of strings, etc.).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return json.RawMessage(b)
}
