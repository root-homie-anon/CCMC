package lifecycle

import (
	"strings"
	"testing"
)

// realOpenCmd is captured once at test init so cleanup functions can restore the
// seam without importing os/exec directly in the test file.
var realOpenCmd = openCmd

// TestOpenInITerm_HappyPath verifies that OpenInITerm invokes the osascript seam
// with a script containing the quoted directory path.
func TestOpenInITerm_HappyPath(t *testing.T) {
	var capturedScript string
	openCmd = func(script string) error {
		capturedScript = script
		return nil
	}
	t.Cleanup(func() { openCmd = realOpenCmd })

	dir := "/Users/alice/projects/my-project"
	if err := OpenInITerm(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The script must contain the single-quoted path.
	want := "'/Users/alice/projects/my-project'"
	if !strings.Contains(capturedScript, want) {
		t.Errorf("script does not contain quoted path %q:\n%s", want, capturedScript)
	}

	// The script must issue a "cd" command (not "claude").
	if !strings.Contains(capturedScript, `write text "cd `) {
		t.Errorf("script does not contain 'write text \"cd ':\n%s", capturedScript)
	}
	if strings.Contains(capturedScript, "&& claude") {
		t.Errorf("OpenInITerm should not run claude, got:\n%s", capturedScript)
	}
}

// TestOpenInITerm_PathTraversal verifies that a path containing shell
// metacharacters (spaces, quotes, semicolons) is correctly single-quoted so
// that the shell inside iTerm would not interpret them as shell syntax.
//
// The POSIX single-quote rule: everything between ' chars is literal. Any
// literal ' in the value is handled by the '\'' idiom (close quote, escaped
// single-quote, reopen quote). This means none of the metacharacters below
// can be executed by the shell receiving the "cd <arg>" command.
func TestOpenInITerm_PathTraversal(t *testing.T) {
	var capturedScript string
	openCmd = func(script string) error {
		capturedScript = script
		return nil
	}
	t.Cleanup(func() { openCmd = realOpenCmd })

	// Path with a single quote embedded — the dangerous case.
	dir := "/tmp/alice's project; rm -rf /"
	if err := OpenInITerm(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// singleQuoteShell converts the single quote to '\'' so the shell sees it as
	// a literal character, not a closing delimiter. Verify the escape is present.
	if !strings.Contains(capturedScript, `'\''`) {
		t.Errorf("expected escaped single-quote sequence '\\'' in script:\n%s", capturedScript)
	}

	// The entire path must appear in fully escaped form. If singleQuoteShell
	// produces the correct output, the dangerous "; rm -rf /" fragment is inside
	// single quotes and cannot be executed as a separate shell command.
	escaped := singleQuoteShell(dir)
	if !strings.Contains(capturedScript, escaped) {
		t.Errorf("script does not contain fully escaped path %q:\n%s", escaped, capturedScript)
	}
}
