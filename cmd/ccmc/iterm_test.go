package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// swapItermSeams replaces both CLI-layer seams and restores the originals via
// t.Cleanup. installFn is the stub for itermInstallFn; getDirFn is the stub
// for itermGetInstallDirFn.
func swapItermSeams(t *testing.T, installFn func(context.Context) error, getDirFn func() (string, error)) {
	t.Helper()
	origInstall := itermInstallFn
	origGetDir := itermGetInstallDirFn
	itermInstallFn = installFn
	itermGetInstallDirFn = getDirFn
	t.Cleanup(func() {
		itermInstallFn = origInstall
		itermGetInstallDirFn = origGetDir
	})
}

// TestItermInstall_HappyPath exercises the nominal case: Install succeeds and
// GetStatusbarInstallDir returns a path. Verifies stdout contains the installed
// path and all five numbered instruction lines, and that exit code is 0.
func TestItermInstall_HappyPath(t *testing.T) {
	const fakeDir = "/tmp/test/Library/Application Support/iTerm2/Scripts/AutoLaunch"

	swapItermSeams(t,
		func(_ context.Context) error { return nil },
		func() (string, error) { return fakeDir, nil },
	)

	var outBuf, errBuf bytes.Buffer
	code := runItermInstall(context.Background(), &outBuf, &errBuf)

	if code != 0 {
		t.Errorf("exit code = %d; want 0 (stderr: %q)", code, errBuf.String())
	}

	out := outBuf.String()

	// Must contain the installed path line.
	if !strings.Contains(out, fakeDir) {
		t.Errorf("stdout does not contain install path %q:\n%s", fakeDir, out)
	}

	// Must contain all five numbered steps.
	expectedSteps := []string{
		" 1. Open iTerm2",
		" 2. iTerm2 → Preferences → Profiles → Session",
		" 3. Click 'Configure Status Bar'",
		" 4. Drag the 'CCMC' component into your Active Components",
		" 5. Restart iTerm2 (or open a new tab)",
	}
	for _, step := range expectedSteps {
		if !strings.Contains(out, step) {
			t.Errorf("stdout missing step %q:\n%s", step, out)
		}
	}

	// Nothing should have been written to stderr on the happy path.
	if errBuf.Len() > 0 {
		t.Errorf("unexpected stderr output: %q", errBuf.String())
	}
}

// TestItermInstall_InstallError verifies the error path: when itermInstallFn
// returns an error the command exits 1 and writes a prefixed message to stderr.
// stdout must be empty.
func TestItermInstall_InstallError(t *testing.T) {
	installErr := errors.New("permission denied: /Library/Application Support/iTerm2")

	swapItermSeams(t,
		func(_ context.Context) error { return installErr },
		func() (string, error) { return "", nil }, // not reached
	)

	var outBuf, errBuf bytes.Buffer
	code := runItermInstall(context.Background(), &outBuf, &errBuf)

	if code != 1 {
		t.Errorf("exit code = %d; want 1", code)
	}

	errOut := errBuf.String()
	if !strings.Contains(errOut, "ccmc iterm-install:") {
		t.Errorf("stderr missing expected prefix %q: %q", "ccmc iterm-install:", errOut)
	}
	if !strings.Contains(errOut, installErr.Error()) {
		t.Errorf("stderr does not include original error %q: %q", installErr.Error(), errOut)
	}

	// stdout must be empty on error.
	if outBuf.Len() > 0 {
		t.Errorf("stdout not empty on error: %q", outBuf.String())
	}
}

// TestItermInstall_GetDirError verifies the partial-failure case: Install
// succeeds but GetStatusbarInstallDir fails. The command still exits 0 (the
// script was written) and stdout contains a fallback message indicating the
// path is unavailable.
func TestItermInstall_GetDirError(t *testing.T) {
	dirErr := errors.New("os: home directory unavailable")

	swapItermSeams(t,
		func(_ context.Context) error { return nil },
		func() (string, error) { return "", dirErr },
	)

	var outBuf, errBuf bytes.Buffer
	code := runItermInstall(context.Background(), &outBuf, &errBuf)

	if code != 0 {
		t.Errorf("exit code = %d; want 0 even when GetStatusbarInstallDir fails", code)
	}

	out := outBuf.String()
	if !strings.Contains(out, "path unavailable") {
		t.Errorf("stdout does not contain fallback path-unavailable message:\n%s", out)
	}
	// Steps must still be printed.
	if !strings.Contains(out, " 1. Open iTerm2") {
		t.Errorf("stdout missing step 1 in get-dir-error path:\n%s", out)
	}
}

// TestItermInstall_RunDispatch verifies that "ccmc iterm-install" reaches
// runItermInstall via the main run() dispatcher. This guards against the
// routing stub in run.go being reverted to the "not yet implemented" placeholder.
func TestItermInstall_RunDispatch(t *testing.T) {
	swapItermSeams(t,
		func(_ context.Context) error { return nil },
		func() (string, error) {
			return "/tmp/fake/AutoLaunch", nil
		},
	)

	var outBuf, errBuf bytes.Buffer
	code := run([]string{"iterm-install"}, &outBuf, &errBuf)

	if code != 0 {
		t.Errorf("run dispatch: exit code = %d; want 0 (stderr: %q)", code, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "Installed CCMC statusbar to") {
		t.Errorf("dispatch: stdout does not contain install header:\n%s", outBuf.String())
	}
}

// TestItermInstall_NextStepsFormat ensures "Next steps:" appears before step 1
// and all five steps are in order in the output.
func TestItermInstall_NextStepsFormat(t *testing.T) {
	swapItermSeams(t,
		func(_ context.Context) error { return nil },
		func() (string, error) { return "/tmp/iterm/AutoLaunch", nil },
	)

	var outBuf, errBuf bytes.Buffer
	code := runItermInstall(context.Background(), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0 (stderr: %q)", code, errBuf.String())
	}

	out := outBuf.String()

	// "Next steps:" must appear before " 1. Open iTerm2".
	nextIdx := strings.Index(out, "Next steps:")
	step1Idx := strings.Index(out, " 1. Open iTerm2")
	if nextIdx < 0 {
		t.Fatal(`stdout missing "Next steps:" header`)
	}
	if step1Idx < 0 {
		t.Fatal(`stdout missing " 1. Open iTerm2"`)
	}
	if nextIdx > step1Idx {
		t.Errorf(`"Next steps:" appears after " 1. Open iTerm2" — wrong order`)
	}

	// All five steps must appear in strictly ascending position.
	steps := []string{
		" 1. Open iTerm2",
		" 2. iTerm2 → Preferences → Profiles → Session",
		" 3. Click 'Configure Status Bar'",
		" 4. Drag the 'CCMC' component into your Active Components",
		" 5. Restart iTerm2 (or open a new tab)",
	}
	prev := -1
	for _, step := range steps {
		idx := strings.Index(out, step)
		if idx < 0 {
			t.Errorf("step %q not found in stdout", step)
			continue
		}
		if idx <= prev {
			t.Errorf("step %q is out of order (pos %d, previous pos %d)", step, idx, prev)
		}
		prev = idx
	}
}
