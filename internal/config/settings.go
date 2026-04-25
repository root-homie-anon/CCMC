package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReadSettings returns the JSON-decoded settings at path as a map of raw
// values so the caller can inspect or merge individual keys without losing
// unknown fields. If the file does not exist, an empty map is returned with no
// error. Returns an error if the file exists but is a symlink (refused), or if
// the JSON is malformed or not an object at the top level.
func ReadSettings(path string) (map[string]json.RawMessage, error) {
	if err := lstatGuard(path); err != nil {
		return nil, fmt.Errorf("settings: symlink check %s: %w", path, err)
	}

	b, err := readFileOrEmpty(path)
	if err != nil {
		return nil, fmt.Errorf("settings: read %s: %w", path, err)
	}
	if len(b) == 0 {
		return make(map[string]json.RawMessage), nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("settings: parse %s: %w", path, err)
	}
	return m, nil
}

// WriteSettings atomically writes settings to path. Before writing:
//  1. Both path and path+".bak" are checked for symlinks (refused if found).
//  2. If a .bak already exists and differs byte-for-byte from the current path
//     contents, the existing .bak is rotated to .bak.<unix-ts> first so the
//     historical state is preserved.
//  3. The current path contents are written to .bak.
//  4. The new content is written to a temp file in the same directory as path
//     (glob prefix .ccmc-settings-*), then os.Rename'd into place. Mode 0o600.
//
// The caller is responsible for any caller-specific stale temp cleanup (e.g.
// the installer's .ccmc-install-* glob).
func WriteSettings(path string, settings map[string]json.RawMessage) error {
	tempDir := filepath.Dir(path)
	bakPath := path + ".bak"

	// ── 1. Symlink guards ────────────────────────────────────────────────────
	if err := lstatGuard(path); err != nil {
		return fmt.Errorf("settings: symlink check %s: %w", path, err)
	}
	if err := lstatGuard(bakPath); err != nil {
		return fmt.Errorf("settings: symlink check %s: %w", bakPath, err)
	}

	// ── 2. Read current file bytes for the backup ────────────────────────────
	current, err := readFileOrEmpty(path)
	if err != nil {
		return fmt.Errorf("settings: read current %s: %w", path, err)
	}

	// ── 3. Encode new content ────────────────────────────────────────────────
	newBytes, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: encode %s: %w", path, err)
	}
	newBytes = append(newBytes, '\n')

	// ── 4. Rotate .bak if it differs from current ────────────────────────────
	// Rotation preserves the historical pre-CCMC state; skipped when .bak
	// already matches (overwriting identical bytes loses nothing).
	if existingBak, readErr := os.ReadFile(bakPath); readErr == nil {
		if !bytes.Equal(existingBak, current) {
			ts := time.Now().Unix()
			rotatedPath := fmt.Sprintf("%s.%d", bakPath, ts)
			if renameErr := os.Rename(bakPath, rotatedPath); renameErr == nil {
				fmt.Fprintf(os.Stderr, "settings: %s rotated to %s.%d\n", bakPath, bakPath, ts)
			}
		}
	}

	// ── 5. Write .bak of the pre-write state ────────────────────────────────
	if err := writeAtomic(bakPath, tempDir, current, ".ccmc-settings-*"); err != nil {
		return fmt.Errorf("settings: write backup %s: %w", bakPath, err)
	}

	// ── 6. Atomic write of new settings ─────────────────────────────────────
	if err := writeAtomic(path, tempDir, newBytes, ".ccmc-settings-*"); err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}

	return nil
}

// lstatGuard returns an error if path exists and is a symlink, or if Lstat
// fails with any error other than ENOENT. A missing path is not an error here.
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

// readFileOrEmpty reads path and returns its bytes. A missing file returns
// a nil slice (not an error). Other errors are returned as-is.
func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// writeAtomic writes data to dst by creating a temp file in tempDir with the
// given glob prefix, syncing, then os.Rename'ing into place. The temp file is
// created with mode 0o600. If any step after creation fails, the temp file is
// removed before returning.
func writeAtomic(dst, tempDir string, data []byte, globPrefix string) error {
	f, err := os.CreateTemp(tempDir, globPrefix)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()

	// Enforce 0o600 — CreateTemp already does this on most platforms, but be explicit.
	if chmodErr := os.Chmod(tmpPath, 0o600); chmodErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", chmodErr)
	}

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
