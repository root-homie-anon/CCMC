package iterm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Sentinel errors returned by Install.
var (
	// ErrInstallDirIsSymlink is returned when the iTerm2 AutoLaunch directory (or
	// any path component at or above it) is found to be a symlink. Refused to
	// defend against pre-existing-symlink redirect attacks.
	ErrInstallDirIsSymlink = errors.New("iterm: install dir is a symlink — refusing to write")

	// ErrScriptDstIsSymlink is returned when the destination path for
	// ccmc_statusbar.py is found to be a symlink at write time. Never write
	// through a symlink at the file level.
	ErrScriptDstIsSymlink = errors.New("iterm: script destination is a symlink — refusing to write")
)

//go:embed statusbar.py
var statusbarScript []byte

// readmeContent is the enable-in-iTerm instructions written next to the script.
// Inlined as a const — the content is short and stable; no separate template file needed.
const readmeContent = `# CCMC iTerm2 Status Bar

## What this is

ccmc_statusbar.py is a Python script that connects to the CCMC daemon via its
Unix socket and renders a live session summary in the iTerm2 status bar:

  CC: 2 active · 1 idle   (green when active sessions exist)
  CC: 0 active · 3 idle   (yellow when all sessions are idle)
  CC: —                   (red when the CCMC daemon is unreachable)

## Enabling the status bar component

1. Open iTerm2 Preferences (Cmd+,).
2. Go to Profiles → select your profile → Session tab.
3. Enable "Status bar enabled" if it is not already on.
4. Click "Configure Status Bar".
5. In the components list, find "CCMC Sessions" and drag it into the active bar.
6. Click OK.

The component polls the CCMC daemon every 5 seconds. Start the daemon with:

  ccmc daemon start

## Requirements

- iTerm2 3.4 or later
- CCMC daemon running (ccmc daemon start)
- Python 3.10+ (provided by iTerm2's built-in Python runtime)
`

// installDirFn is the function used to resolve the iTerm2 AutoLaunch directory.
// Tests replace this var to redirect writes into a temp directory.
var installDirFn = defaultInstallDir

// defaultInstallDir returns the canonical iTerm2 AutoLaunch directory for the
// current user. It uses os.UserHomeDir so it never hardcodes a path.
func defaultInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("iterm: resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "iTerm2", "Scripts", "AutoLaunch"), nil
}

// GetStatusbarInstallDir returns the path where ccmc_statusbar.py will be
// installed. Exported so cmd/ccmc/iterm.go (#62) can include it in post-install
// output without duplicating the path logic.
func GetStatusbarInstallDir() (string, error) {
	return installDirFn()
}

// Install copies the embedded statusbar.py to the iTerm2 AutoLaunch directory
// and writes a README.md next to it explaining how to enable the component.
//
// Behaviour:
//   - Creates the AutoLaunch directory tree if absent (idempotent, 0o700).
//   - If ccmc_statusbar.py already exists, renames it to ccmc_statusbar.py.bak
//     BEFORE writing the new copy (overwrite-with-backup pattern).
//   - If ccmc_statusbar.py.bak already exists it is silently replaced by the
//     previous version (one generation of backup is sufficient).
//   - If README.md already exists it is replaced in-place (no backup needed for
//     generated docs).
//   - Returns nil on success; a structured error on any failure.
//
// Security posture (matches internal/integrator/installer.go):
//   - Lstat the install dir before creating it: if a symlink is already at that
//     path, return ErrInstallDirIsSymlink without touching anything.
//   - Use O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW for each new file write so the
//     call never follows a symlink at the destination.
//   - Set 0o700 on the directory, 0o600 on all written files.
//   - Never call RemoveAll on anything outside the install dir.
func Install(_ context.Context) error {
	installDir, err := installDirFn()
	if err != nil {
		return err
	}

	// ── 1. Symlink guard on install dir ────────────────────────────────────────
	//
	// Lstat the target directory (not Stat — we want info about the path itself,
	// not a potential symlink target). If a symlink sits at installDir or any
	// ancestor we would normally create, refuse immediately.
	if err := lstatGuardDir(installDir); err != nil {
		return err
	}

	// ── 2. Create the directory tree if absent ─────────────────────────────────
	//
	// MkdirAll is idempotent and safe here because we just confirmed (via lstatGuardDir)
	// that no symlink occupies the terminal path. We use 0o700 so the directory is
	// owner-only, matching the integrator's posture for user-scoped install dirs.
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("iterm: create install dir %s: %w", installDir, err)
	}

	// ── 3. Install ccmc_statusbar.py (with .bak rotation) ─────────────────────
	scriptDst := filepath.Join(installDir, "ccmc_statusbar.py")
	if err := writeWithBackup(scriptDst, statusbarScript); err != nil {
		return fmt.Errorf("iterm: write script: %w", err)
	}

	// ── 4. Write README.md ─────────────────────────────────────────────────────
	readmeDst := filepath.Join(installDir, "README.md")
	if err := writeFileNoFollow(readmeDst, []byte(readmeContent)); err != nil {
		return fmt.Errorf("iterm: write README: %w", err)
	}

	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

// lstatGuardDir checks whether installDir (or the final path segment under its
// parent) is a symlink. If a symlink exists at installDir, returns
// ErrInstallDirIsSymlink. A missing path is acceptable — it will be created by
// MkdirAll. Any unexpected Lstat error is surfaced as-is.
func lstatGuardDir(installDir string) error {
	fi, err := os.Lstat(installDir)
	if err == nil {
		// Path exists — check whether it is a symlink.
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrInstallDirIsSymlink, installDir)
		}
		// It's a real directory (or file, which MkdirAll will fail on) — fine.
		return nil
	}
	if os.IsNotExist(err) {
		// Directory does not yet exist — will be created by MkdirAll.
		return nil
	}
	// Unexpected lstat error (permission denied, etc.).
	return fmt.Errorf("iterm: lstat install dir %s: %w", installDir, err)
}

// writeWithBackup writes data to dst. If dst already exists as a regular file,
// it is renamed to dst+".bak" first so the old content is preserved. If a
// symlink exists at dst, returns ErrScriptDstIsSymlink.
//
// Uses O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW so the write never follows a symlink
// that may appear between the Lstat check and the open(2) syscall.
func writeWithBackup(dst string, data []byte) error {
	// Check what currently lives at dst.
	fi, err := os.Lstat(dst)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("lstat %s: %w", dst, err)
	}
	if err == nil {
		// Something exists at dst.
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrScriptDstIsSymlink, dst)
		}
		// Regular file: rename to .bak before creating the new file.
		// If .bak already exists it is silently overwritten by the rename
		// (atomic replacement; one generation of backup suffices).
		bakDst := dst + ".bak"
		if err := os.Rename(dst, bakDst); err != nil {
			return fmt.Errorf("rename %s to %s: %w", dst, bakDst, err)
		}
	}

	// dst no longer exists (either it never did, or we renamed it away).
	// Open with O_EXCL|O_NOFOLLOW: fail if any path (including a symlink)
	// now occupies dst — closes the TOCTOU window between Lstat and open.
	return openAndWrite(dst, data, 0o600)
}

// writeFileNoFollow writes data to dst, replacing it if it already exists.
// Like writeWithBackup but without the .bak rotation — used for generated
// documentation files where a backup adds no value.
// Refuses to write through a symlink at dst.
func writeFileNoFollow(dst string, data []byte) error {
	fi, err := os.Lstat(dst)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("lstat %s: %w", dst, err)
	}
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("iterm: README destination %s is a symlink — refusing to write", dst)
		}
		// Regular file: remove it so O_EXCL creates a fresh file.
		if rmErr := os.Remove(dst); rmErr != nil {
			return fmt.Errorf("remove existing %s: %w", dst, rmErr)
		}
	}
	return openAndWrite(dst, data, 0o600)
}

// openAndWrite opens dst with O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW and writes
// data with the given permission bits. Returns an error if the open fails or
// any write-side error occurs. The file is closed before returning.
func openAndWrite(dst string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() {
		// Best-effort close on the error path; the caller already has the error.
		_ = f.Close()
	}()

	if _, werr := f.Write(data); werr != nil {
		return fmt.Errorf("write %s: %w", dst, werr)
	}
	if serr := f.Sync(); serr != nil {
		return fmt.Errorf("sync %s: %w", dst, serr)
	}
	return f.Close()
}
