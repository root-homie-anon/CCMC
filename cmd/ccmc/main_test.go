package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runRefOut is a test helper that runs runRef with the given arguments and
// returns the captured stdout, captured stderr, and exit code.
func runRefOut(args []string) (stdout string, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = runRef(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// TestRef_NoArgs verifies that calling "ccmc ref" with no arguments exits 2
// and writes a usage message to stderr.
func TestRef_NoArgs(t *testing.T) {
	_, errOut, code := runRefOut(nil)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("expected Usage hint in stderr, got: %q", errOut)
	}
}

// TestRef_Query_FuzzySearch verifies shape 1: a free-text query that does not
// match a known category returns results from across all categories.
func TestRef_Query_FuzzySearch(t *testing.T) {
	// Use real entry names/terms that are present in the embedded YAML data.
	tests := []struct {
		name  string
		query string
	}{
		// "SessionStart" is the first entry in hooks.yaml.
		{"known term from hooks", "SessionStart"},
		// "clear" is a partial match for "/clear" in commands.yaml.
		{"partial name match for /clear", "clear"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{tc.query})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			// Output must contain the table header.
			if !strings.Contains(out, "NAME") {
				t.Errorf("expected table header in output, got:\n%s", out)
			}
			// At least one result row (header + separator + ≥1 result = 3 lines).
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) < 3 {
				t.Errorf("expected at least one result row, got:\n%s", out)
			}
		})
	}
}

// TestRef_Query_UnknownTerm verifies that a query matching nothing exits 0 and
// prints a "no results" message rather than an error.
func TestRef_Query_UnknownTerm(t *testing.T) {
	out, errOut, code := runRefOut([]string{"xyzzy_nonexistent_zz99"})
	if code != 0 {
		t.Fatalf("expected exit 0 for no-results, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(strings.ToLower(out), "no results") {
		t.Errorf("expected 'no results' message, got: %q", out)
	}
}

// TestRef_CategoryOnly verifies shape 2: a known category string lists all
// entries in that category with NAME/CATEGORY/DESCRIPTION columns.
func TestRef_CategoryOnly(t *testing.T) {
	categories := []string{"commands", "hooks"}

	for _, cat := range categories {
		t.Run(cat, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{cat})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			if !strings.Contains(out, "NAME") || !strings.Contains(out, "CATEGORY") {
				t.Errorf("expected tabular header in output for category %q, got:\n%s", cat, out)
			}
			// Every result row should reference the requested category.
			lines := strings.Split(strings.TrimSpace(out), "\n")
			for _, line := range lines[2:] { // skip header + separator
				if line == "" {
					continue
				}
				if !strings.Contains(line, cat) {
					t.Errorf("line %q does not contain category %q", line, cat)
				}
			}
		})
	}
}

// TestRef_CategoryName verifies shape 3: a category + name shows the full
// detail view (NAME, CATEGORY, DESC fields) for the top fuzzy hit.
func TestRef_CategoryName(t *testing.T) {
	tests := []struct {
		category string
		name     string
		wantIn   []string
	}{
		{
			// "SessionStart" is the first entry in hooks.yaml.
			category: "hooks",
			name:     "SessionStart",
			wantIn:   []string{"NAME", "CATEGORY", "hooks"},
		},
		{
			// "/clear" is the first entry in commands.yaml; "clear" fuzzy-matches it.
			category: "commands",
			name:     "clear",
			wantIn:   []string{"NAME", "CATEGORY", "commands"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.category+"/"+tc.name, func(t *testing.T) {
			out, errOut, code := runRefOut([]string{tc.category, tc.name})
			if code != 0 {
				t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in output, got:\n%s", want, out)
				}
			}
		})
	}
}

// TestRef_CategoryName_NoMatch verifies that a category+name with no fuzzy
// match exits 1 with an error on stderr.
func TestRef_CategoryName_NoMatch(t *testing.T) {
	_, errOut, code := runRefOut([]string{"commands", "xyzzy_nonexistent_zz99"})
	if code != 1 {
		t.Errorf("expected exit code 1 for no match, got %d", code)
	}
	if !strings.Contains(errOut, "no match") {
		t.Errorf("expected 'no match' in stderr, got: %q", errOut)
	}
}

// TestRef_Query_LimitTen verifies that a broad free-text query never returns
// more than 10 results (the cap enforced by shape 1).
func TestRef_Query_LimitTen(t *testing.T) {
	// "a" should fuzzily match many entries across all categories.
	out, errOut, code := runRefOut([]string{"a"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Count result rows: total lines minus header (1) and separator (1).
	resultRows := 0
	for _, l := range lines[2:] {
		if strings.TrimSpace(l) != "" {
			resultRows++
		}
	}
	if resultRows > 10 {
		t.Errorf("expected at most 10 results, got %d", resultRows)
	}
}

// ── run() seam tests ──────────────────────────────────────────────────────────

// runCmd is a test helper that calls run() with args and captures output.
func runCmd(args []string) (stdout, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = run(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// TestRun_Version verifies "ccmc version" returns exit 0 and contains "ccmc".
func TestRun_Version(t *testing.T) {
	out, _, code := runCmd([]string{"version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(out, "ccmc") {
		t.Errorf("expected version output to contain 'ccmc', got: %q", out)
	}
}

// TestRun_Help verifies "ccmc help" exits 0 and prints usage.
func TestRun_Help(t *testing.T) {
	out, _, code := runCmd([]string{"help"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected 'Usage:' in help output, got: %q", out)
	}
}

// TestRun_UnknownCmd verifies an unknown command exits 2 with a message.
func TestRun_UnknownCmd(t *testing.T) {
	_, errOut, code := runCmd([]string{"nonexistent-cmd"})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("expected 'unknown command' in stderr, got: %q", errOut)
	}
}

// TestRun_NoArgs verifies "ccmc" with no args exits 2 (dashboard not yet implemented).
func TestRun_NoArgs(t *testing.T) {
	_, _, code := runCmd(nil)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

// ── daemon subcommand routing tests ──────────────────────────────────────────

// TestDaemon_MissingSubcmd verifies "ccmc daemon" with no subcommand exits 2.
func TestDaemon_MissingSubcmd(t *testing.T) {
	_, errOut, code := runCmd([]string{"daemon"})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut, "start|stop|status") {
		t.Errorf("expected subcommand hint in stderr, got: %q", errOut)
	}
}

// TestDaemon_UnknownSubcmd verifies "ccmc daemon bogus" exits 2 with a message.
func TestDaemon_UnknownSubcmd(t *testing.T) {
	_, errOut, code := runCmd([]string{"daemon", "bogus"})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("expected subcommand name in stderr, got: %q", errOut)
	}
}

// TestDaemonStatus_NotRunning verifies "ccmc daemon status" exits 1 and
// prints "daemon not running" when the socket is absent (no real daemon).
func TestDaemonStatus_NotRunning(t *testing.T) {
	// Point CCMC_DIR at an empty temp dir so there is no socket.
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)

	_, errOut, code := runCmd([]string{"daemon", "status"})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(errOut, "daemon not running") {
		t.Errorf("expected 'daemon not running' in stderr, got: %q", errOut)
	}
}

// TestDaemonStop_NoPIDFile verifies "ccmc daemon stop" exits 0 and prints
// "no daemon running" when the PID file is absent.
func TestDaemonStop_NoPIDFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)

	out, _, code := runCmd([]string{"daemon", "stop"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, "no daemon running") {
		t.Errorf("expected 'no daemon running' in stdout, got: %q", out)
	}
}

// ── ccmc ls --no-daemon tests ─────────────────────────────────────────────────

// TestLs_NoDaemonFlag verifies "--no-daemon" routes to filesystem scan and
// never dials the daemon socket. We verify by pointing CCMC_DIR at a temp dir
// with no socket — if the daemon path were taken, any socket errors would appear.
func TestLs_NoDaemonFlag(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)
	// Also point the Claude projects dir at an empty temp dir so the scan
	// returns "no sessions" cleanly without touching the real ~/.claude/projects.
	t.Setenv("CLAUDE_CONFIG_DIR", tmp)

	out, errOut, code := runCmd([]string{"ls", "--no-daemon"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
	}
	// With no sessions the scanner prints "No sessions found."
	if !strings.Contains(out, "No sessions found") {
		t.Errorf("expected 'No sessions found' in output, got: %q", out)
	}
	// stderr must not contain "daemon not running" — we skipped the daemon entirely.
	if strings.Contains(errOut, "daemon not running") {
		t.Errorf("--no-daemon should not emit daemon warning; got stderr: %q", errOut)
	}
}

// TestLs_FallbackWarning verifies that without --no-daemon and with no daemon
// running, the output contains the fallback warning on stderr.
func TestLs_FallbackWarning(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", tmp)

	_, errOut, code := runCmd([]string{"ls"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(errOut, "daemon not running; using filesystem-only mode") {
		t.Errorf("expected fallback warning in stderr, got: %q", errOut)
	}
}

// ── ccmc setup tests ──────────────────────────────────────────────────────────

// TestSetup_CreatesConfigAndDir verifies that setup creates ~/.ccmc/ and
// config.yaml on a clean temp dir, and that a second run is idempotent
// (config.yaml content is unchanged).
func TestSetup_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", tmp)

	// First run: should succeed and create config.yaml.
	out1, errOut1, code1 := runCmd([]string{"setup"})
	if code1 != 0 {
		t.Fatalf("first setup: expected exit 0, got %d; stderr: %q", code1, errOut1)
	}

	configPath := filepath.Join(tmp, "config.yaml")
	data1, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.yaml not created after first setup: %v", err)
	}
	_ = out1

	// Sentinel: second run must not overwrite config.yaml.
	sentinel := "# sentinel"
	if err := os.WriteFile(configPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	out2, errOut2, code2 := runCmd([]string{"setup"})
	if code2 != 0 {
		t.Fatalf("second setup: expected exit 0, got %d; stderr: %q", code2, errOut2)
	}

	data2, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml after second setup: %v", err)
	}
	if string(data2) != sentinel {
		t.Errorf("second run overwrote config.yaml; want sentinel %q, got %q", sentinel, string(data2))
	}

	// Also verify first run actually wrote non-empty content.
	if len(data1) == 0 {
		t.Error("config.yaml was empty after first setup")
	}

	_ = out2
}

// TestSetup_AlreadySetUpMessage verifies the "already set up" path emits the
// right message when config.yaml pre-exists and hooks are already installed.
func TestSetup_AlreadySetUpMessage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", tmp)

	// Run once to lay everything down, then run again.
	if _, _, code := runCmd([]string{"setup"}); code != 0 {
		t.Fatal("first setup failed")
	}
	out, _, code := runCmd([]string{"setup"})
	if code != 0 {
		t.Fatalf("second setup: expected exit 0, got %d", code)
	}
	if !strings.Contains(out, "already set up") {
		t.Errorf("expected 'already set up' message on second run, got: %q", out)
	}
}
