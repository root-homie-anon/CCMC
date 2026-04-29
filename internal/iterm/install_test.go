package iterm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// swapInstallDir replaces installDirFn with one that returns dir, and
// restores the original on test cleanup.
func swapInstallDir(t *testing.T, dir string) {
	t.Helper()
	orig := installDirFn
	installDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { installDirFn = orig })
}

// TestInstall_HappyPath verifies the end-to-end success case:
//   - Install creates the install directory with mode 0o700.
//   - ccmc_statusbar.py is written with the embedded script bytes and mode 0o600.
//   - README.md is written with the expected header line and mode 0o600.
func TestInstall_HappyPath(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "AutoLaunch")
	swapInstallDir(t, installDir)

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// ── install dir must exist with 0o700 ──────────────────────────────────────
	dirFI, err := os.Lstat(installDir)
	if err != nil {
		t.Fatalf("install dir not created: %v", err)
	}
	if !dirFI.IsDir() {
		t.Fatalf("install dir is not a directory (mode %s)", dirFI.Mode())
	}
	if dirFI.Mode().Perm() != 0o700 {
		t.Errorf("install dir perm = %04o; want 0700", dirFI.Mode().Perm())
	}

	// ── ccmc_statusbar.py must match embedded bytes ────────────────────────────
	scriptPath := filepath.Join(installDir, "ccmc_statusbar.py")
	written, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ccmc_statusbar.py not found: %v", err)
	}
	if !bytes.Equal(written, statusbarScript) {
		t.Errorf("ccmc_statusbar.py content mismatch: got %d bytes, want %d bytes",
			len(written), len(statusbarScript))
	}
	scriptFI, _ := os.Lstat(scriptPath)
	if scriptFI.Mode().Perm() != 0o600 {
		t.Errorf("script perm = %04o; want 0600", scriptFI.Mode().Perm())
	}

	// ── README.md must exist with expected content and 0o600 ──────────────────
	readmePath := filepath.Join(installDir, "README.md")
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README.md not found: %v", err)
	}
	if string(readmeBytes) != readmeContent {
		t.Errorf("README.md content mismatch:\ngot:  %q\nwant: %q",
			string(readmeBytes), readmeContent)
	}
	readmeFI, _ := os.Lstat(readmePath)
	if readmeFI.Mode().Perm() != 0o600 {
		t.Errorf("README perm = %04o; want 0600", readmeFI.Mode().Perm())
	}
}

// TestInstall_Idempotent verifies the overwrite-with-backup pattern:
//   - First Install writes ccmc_statusbar.py with the real embedded content.
//   - Second Install renames the existing file to ccmc_statusbar.py.bak and
//     writes a fresh copy.
//   - .bak contains the bytes that existed before the second Install ran.
func TestInstall_Idempotent(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "AutoLaunch")
	swapInstallDir(t, installDir)

	// First install.
	if err := Install(context.Background()); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	// Overwrite the installed script with sentinel content so we can verify the
	// .bak was created from this pre-second-install state.
	scriptPath := filepath.Join(installDir, "ccmc_statusbar.py")
	sentinelContent := []byte("# sentinel — old version\n")
	if err := os.WriteFile(scriptPath, sentinelContent, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second install.
	if err := Install(context.Background()); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	// .bak must exist and contain the sentinel (pre-second-install) bytes.
	bakPath := scriptPath + ".bak"
	bakBytes, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("ccmc_statusbar.py.bak not found after second install: %v", err)
	}
	if !bytes.Equal(bakBytes, sentinelContent) {
		t.Errorf(".bak content = %q; want sentinel %q", bakBytes, sentinelContent)
	}

	// Active script must be the newly-written embedded bytes.
	activeBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ccmc_statusbar.py not found after second install: %v", err)
	}
	if !bytes.Equal(activeBytes, statusbarScript) {
		t.Errorf("active script content mismatch after second install")
	}
}

// TestInstall_SymlinkAttackOnDir verifies the symlink-redirect defence:
//   - A symlink pre-placed at the install dir path pointing to an attacker
//     directory causes Install to return ErrInstallDirIsSymlink.
//   - The attacker directory is left empty — nothing is written into it.
func TestInstall_SymlinkAttackOnDir(t *testing.T) {
	parent := t.TempDir()
	installDir := filepath.Join(parent, "AutoLaunch")
	attackerDir := t.TempDir()

	// Pre-place a symlink at the install dir pointing to the attacker's directory.
	if err := os.Symlink(attackerDir, installDir); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}
	swapInstallDir(t, installDir)

	err := Install(context.Background())
	if err == nil {
		t.Fatal("expected error when install dir is a symlink, got nil")
	}
	// Must surface ErrInstallDirIsSymlink (or at minimum contain "symlink").
	if !isSymlinkErr(err) {
		t.Errorf("expected symlink error, got: %v", err)
	}

	// Attacker dir must remain empty — nothing was written into it.
	entries, readErr := os.ReadDir(attackerDir)
	if readErr != nil {
		t.Fatalf("ReadDir attacker dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("attacker dir has %d entries after refused install — symlink redirect succeeded", len(entries))
	}
}

// TestInstall_Permissions verifies the exact permission bits set on the
// install directory, script, and README after a fresh install. This is a
// focused perms-only check that does not depend on the broader happy-path
// assertions in TestInstall_HappyPath.
func TestInstall_Permissions(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "AutoLaunch")
	swapInstallDir(t, installDir)

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	cases := []struct {
		label    string
		path     string
		wantPerm os.FileMode
	}{
		{"install dir", installDir, 0o700},
		{"script", filepath.Join(installDir, "ccmc_statusbar.py"), 0o600},
		{"README", filepath.Join(installDir, "README.md"), 0o600},
	}
	for _, tc := range cases {
		fi, err := os.Lstat(tc.path)
		if err != nil {
			t.Errorf("%s: lstat: %v", tc.label, err)
			continue
		}
		if fi.Mode().Perm() != tc.wantPerm {
			t.Errorf("%s: perm = %04o; want %04o", tc.label, fi.Mode().Perm(), tc.wantPerm)
		}
	}
}

// TestGetStatusbarInstallDir verifies that GetStatusbarInstallDir returns a
// path containing the expected iTerm2 AutoLaunch path fragment when using the
// real (unswapped) defaultInstallDir. This ensures the exported helper returns
// a usable path string for post-install output in #62.
func TestGetStatusbarInstallDir(t *testing.T) {
	// Use the real installDirFn (defaultInstallDir) to confirm the format.
	// On macOS this must end with /Library/Application Support/iTerm2/Scripts/AutoLaunch
	got, err := GetStatusbarInstallDir()
	if err != nil {
		t.Fatalf("GetStatusbarInstallDir: %v", err)
	}
	if got == "" {
		t.Fatal("GetStatusbarInstallDir returned empty string")
	}
	// Must be an absolute path.
	if !filepath.IsAbs(got) {
		t.Errorf("GetStatusbarInstallDir returned non-absolute path: %q", got)
	}
	// Must end with the canonical AutoLaunch segment.
	const wantSuffix = "Application Support/iTerm2/Scripts/AutoLaunch"
	if len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
		t.Errorf("GetStatusbarInstallDir path %q does not end with %q", got, wantSuffix)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────

// isSymlinkErr returns true when err is or wraps ErrInstallDirIsSymlink, or
// when the error message contains the word "symlink". The latter covers
// ErrScriptDstIsSymlink and any other symlink-rejection path.
func isSymlinkErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrInstallDirIsSymlink) || errors.Is(err, ErrScriptDstIsSymlink) {
		return true
	}
	return containsSubstring(err.Error(), "symlink")
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
