package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ccmc/internal/config"
)

// ccmcMarker is the sentinel value written into every CCMC hook entry.
// Its presence in the inner hook object is the identity check used to
// recognise existing entries on subsequent installs, making Install idempotent.
const ccmcMarker = "ccmc"

// hookEntry is the inner object inside a hook group's "hooks" array.
// _ccmc is an application-level marker field; CC ignores unknown JSON fields.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	CCMC    string `json:"_ccmc,omitempty"`
}

// hookGroup is one element in the per-event list. Lifecycle events (SessionStart,
// SessionEnd, Stop, SubagentStart, SubagentStop, Notification) carry no matcher
// because they fire unconditionally. PostToolUse also uses no matcher so CCMC
// captures all tool calls regardless of tool name — the daemon handler filters
// by session, not by tool type.
type hookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

// ccmcEvents is the ordered set of event names that CCMC installs hooks for.
// Order matches the EventType constants in events.go and is stable across runs,
// which matters for the byte-for-byte idempotency guarantee.
var ccmcEvents = []string{
	"SessionStart",
	"SessionEnd",
	"PostToolUse",
	"SubagentStart",
	"SubagentStop",
	"Stop",
	"Notification",
}

// InstallerOptions controls path overrides used in tests. Zero value uses the
// real paths resolved by internal/config.
type InstallerOptions struct {
	// SettingsPath overrides the target settings.json path. If empty,
	// config.ClaudeSettingsPath() is used.
	SettingsPath string

	// SocketPath overrides the daemon socket path embedded in hook commands.
	// If empty, config.CcmcSocketPath() is used.
	SocketPath string

	// TempDir overrides the directory used for atomic temp-file writes.
	// If empty, filepath.Dir(SettingsPath) is used. Tests point this at a
	// writable location when simulating write failures in read-only dirs.
	TempDir string
}

// Install merges CCMC's hook entries into ~/.claude/settings.json without
// touching any existing unrelated keys or hooks. It is safe to call multiple
// times: the second run produces a byte-for-byte identical file (verified by
// the installer_test.go idempotency test).
//
// Write safety: the new content is written to a temp file in the same directory
// as settings.json, then os.Rename'd into place. If the process is killed
// mid-write the original file is never touched.
//
// Backup: ~/.claude/settings.json.bak is written before every write, containing
// the exact bytes that were in settings.json before this call. If no write is
// needed (idempotent no-op) no backup is written.
func Install(opts InstallerOptions) error {
	settingsPath := opts.SettingsPath
	if settingsPath == "" {
		settingsPath = config.ClaudeSettingsPath()
	}
	socketPath := opts.SocketPath
	if socketPath == "" {
		socketPath = config.CcmcSocketPath()
	}
	tempDir := opts.TempDir
	if tempDir == "" {
		tempDir = filepath.Dir(settingsPath)
	}

	// ── 0. Purge stale temp files left by a prior SIGKILL ────────────────────
	// A normal install completes in milliseconds; anything older than 60 s is
	// from a killed run and safe to remove. Fresh files (< 60 s) are left alone
	// in case another install is running concurrently.
	if entries, globErr := filepath.Glob(filepath.Join(tempDir, ".ccmc-install-*")); globErr == nil {
		cutoff := time.Now().Add(-60 * time.Second)
		for _, entry := range entries {
			if fi, statErr := os.Lstat(entry); statErr == nil && fi.ModTime().Before(cutoff) {
				if rmErr := os.Remove(entry); rmErr == nil {
					fmt.Fprintf(os.Stderr, "installer: removed stale temp file %s\n", entry)
				}
			}
		}
	}

	// ── 1. Symlink guard on settingsPath before any read ─────────────────────
	if err := lstatGuard(settingsPath); err != nil {
		return fmt.Errorf("installer: settings.json symlink check: %w", err)
	}

	// ── 2. Read existing settings (or start with empty object) ───────────────
	original, err := readFileOrEmpty(settingsPath)
	if err != nil {
		return fmt.Errorf("installer: read settings: %w", err)
	}

	// ── 3. Decode into a generic map so we preserve unknown top-level keys ───
	var root map[string]json.RawMessage
	if len(original) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return fmt.Errorf("installer: parse settings.json: %w", err)
		}
	}
	if root == nil {
		root = make(map[string]json.RawMessage)
	}

	// ── 4. Decode the existing "hooks" block (or start with empty map) ───────
	var hooksMap map[string][]hookGroup
	if raw, ok := root["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooksMap); err != nil {
			return fmt.Errorf("installer: parse hooks block: %w", err)
		}
	}
	if hooksMap == nil {
		hooksMap = make(map[string][]hookGroup)
	}

	// ── 5. Merge CCMC entries ─────────────────────────────────────────────────
	changed := mergeEntries(hooksMap, socketPath)
	if !changed {
		// Already up to date — no write, no backup.
		return nil
	}

	// ── 6. Re-encode hooks block and inject back into root ───────────────────
	hooksRaw, err := json.Marshal(hooksMap)
	if err != nil {
		return fmt.Errorf("installer: encode hooks block: %w", err)
	}
	root["hooks"] = json.RawMessage(hooksRaw)

	// ── 7. Encode full settings with stable key order ─────────────────────────
	newBytes, err := marshalStable(root)
	if err != nil {
		return fmt.Errorf("installer: encode settings: %w", err)
	}

	// ── 8. Symlink guard on bakPath before any read or write ─────────────────
	bakPath := settingsPath + ".bak"
	if err := lstatGuard(bakPath); err != nil {
		return fmt.Errorf("installer: .bak symlink check: %w", err)
	}

	// ── 9. Rotate .bak if it differs from the current settings bytes ─────────
	// A fixed .bak slot destroys the pre-first-CCMC state on every subsequent
	// run. Rotation bounds backup count by the number of meaningful changes:
	// if the existing .bak already matches original, no information is lost by
	// overwriting it, so we skip rotation.
	if existingBak, readErr := os.ReadFile(bakPath); readErr == nil {
		if !bytes.Equal(existingBak, original) {
			ts := time.Now().Unix()
			rotatedPath := fmt.Sprintf("%s.%d", bakPath, ts)
			if renameErr := os.Rename(bakPath, rotatedPath); renameErr == nil {
				fmt.Fprintf(os.Stderr, "installer: settings.json.bak rotated to settings.json.bak.%d\n", ts)
			}
		}
	}

	// ── 10. Write .bak of the pre-write state ────────────────────────────────
	if err := writeAtomic(bakPath, tempDir, original); err != nil {
		return fmt.Errorf("installer: write backup: %w", err)
	}

	// ── 11. Atomic write of new settings ─────────────────────────────────────
	if err := writeAtomic(settingsPath, tempDir, newBytes); err != nil {
		return fmt.Errorf("installer: write settings: %w", err)
	}

	return nil
}

// mergeEntries adds CCMC hook entries to hooksMap for each event in ccmcEvents.
// Returns true if any entry was added (i.e. if a write is needed).
// Each CCMC entry is a hookGroup with no matcher and a single inner hookEntry
// whose _ccmc field is set to ccmcMarker. The presence of that marker in an
// existing entry is the idempotency check — if found, the entry is replaced
// in-place so command updates (e.g. socket path changes) are applied cleanly.
func mergeEntries(hooksMap map[string][]hookGroup, socketPath string) bool {
	changed := false
	for _, event := range ccmcEvents {
		cmd := buildCommand(event, socketPath)
		entry := hookEntry{
			Type:    "command",
			Command: cmd,
			CCMC:    ccmcMarker,
		}
		group := hookGroup{Hooks: []hookEntry{entry}}

		groups := hooksMap[event]
		idx := findCcmcGroup(groups)
		if idx >= 0 {
			// Replace the existing CCMC group only if the command changed.
			existing := groups[idx].Hooks
			if len(existing) == 1 && existing[0].Command == cmd {
				continue // already current — no change
			}
			groups[idx] = group
			hooksMap[event] = groups
			changed = true
		} else {
			hooksMap[event] = append(groups, group)
			changed = true
		}
	}
	return changed
}

// findCcmcGroup returns the index of the first hookGroup in groups whose first
// hook entry carries the ccmcMarker, or -1 if no such group exists.
func findCcmcGroup(groups []hookGroup) int {
	for i, g := range groups {
		if len(g.Hooks) > 0 && g.Hooks[0].CCMC == ccmcMarker {
			return i
		}
	}
	return -1
}

// buildCommand returns the shell command string for a given event name. The
// command uses curl with --unix-socket to POST to the daemon over the unix
// socket. `-s` suppresses progress output; `-o /dev/null` discards the response
// body; `--data @-` reads the hook payload from stdin as CC pipes it. The `-f`
// flag is omitted intentionally: a non-2xx from the daemon should not cause CC
// to treat the hook as failed — CCMC is an observer, not a gatekeeper.
//
// Both socketPath and event are shell-quoted via shellQuote (single-quote with
// '\'' escaping) so metacharacters in a custom $CCMC_DIR or a future dynamic
// event name cannot inject shell commands when CC executes this via /bin/sh -c.
func buildCommand(event, socketPath string) string {
	return fmt.Sprintf(
		`curl -s --unix-socket %s -X POST -H "Content-Type: application/json" --data @- http://localhost/hooks/%s`,
		shellQuote(socketPath),
		shellQuote(event),
	)
}

// marshalStable encodes a map[string]json.RawMessage into indented JSON with
// keys in a deterministic order. The key order is: "hooks" is moved to appear
// after any alphabetically earlier keys, and the rest are sorted. In practice
// for settings.json the exact order is whatever json.Marshal produces for a
// sorted-key map — Go's json.Marshal sorts map keys alphabetically, which is
// stable across runs and sufficient for the byte-for-byte idempotency guarantee.
func marshalStable(root map[string]json.RawMessage) ([]byte, error) {
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// readFileOrEmpty reads the file at path and returns its bytes. If the file
// does not exist it returns a nil slice (not an error). Any other error
// (permission denied, I/O failure) is returned as-is.
func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// writeAtomic writes data to dst by first writing to a temp file in tempDir,
// then calling os.Rename. This guarantees the destination is never partially
// written even if the process is killed mid-write.
func writeAtomic(dst, tempDir string, data []byte) error {
	f, err := os.CreateTemp(tempDir, ".ccmc-install-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp to %s: %w", dst, err)
	}
	return nil
}

// lstatGuard checks path with os.Lstat and returns an error if:
//   - the path exists and is a symlink (mirrors daemon's bindSocket / writePIDFile pattern)
//   - Lstat returns any error other than ENOENT
//
// A missing path (ENOENT) is not an error here — callers handle that case themselves.
func lstatGuard(path string) error {
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode().Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %s is a symlink — refusing to proceed", path)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("lstat %s: %w", path, err)
}

// shellQuote wraps s in single quotes safe for use in a POSIX sh -c string.
// Any single-quote character inside s is escaped via the '\'' idiom so the
// quoting is robust against arbitrary input (including paths with spaces,
// semicolons, dollar signs, or single quotes). This prevents a socketPath or
// event value from injecting shell metacharacters into the hook command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HashFile returns the SHA-256 hex digest of the file at path.
// Used by tests to assert byte-for-byte idempotency.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
