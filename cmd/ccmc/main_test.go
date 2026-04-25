package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ccmc/internal/config"
	"ccmc/internal/daemon"
	"ccmc/internal/hooks"
	"ccmc/pkg/ccmc"
)

// TestMain detects when the test binary is re-invoked as a daemon helper and
// runs a real daemon on the socket path given by CCMC_DIR. This supports
// TestDaemonStop_PostSIGTERMVerify, which needs an out-of-process daemon so
// that SIGTERM goes to a child process, not the test process itself.
func TestMain(m *testing.M) {
	if os.Getenv("CCMC_TEST_RUN_DAEMON") == "1" {
		runEmbeddedTestDaemon()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runEmbeddedTestDaemon starts a real daemon on the config-standard paths
// (derived from CCMC_DIR) and runs until SIGTERM or idle timeout.
func runEmbeddedTestDaemon() {
	sockPath := config.CcmcSocketPath()
	pidPath := config.CcmcDaemonPidPath()
	dir := config.CcmcDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		os.Stderr.WriteString("ccmc test daemon: mkdir: " + err.Error() + "\n")
		os.Exit(1)
	}

	reg := daemon.NewRegistry(dir + "/r.json")
	srv := daemon.New(reg, embeddedTestHookHandlers(reg),
		daemon.WithSocketPath(sockPath),
		daemon.WithPIDPath(pidPath),
		daemon.WithIdleTimeout(10*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigCh; cancel() }()

	if err := srv.Run(ctx); err != nil {
		os.Stderr.WriteString("ccmc test daemon: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func embeddedTestHookHandlers(reg *daemon.Registry) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"SessionStart":  hooks.HandleSessionStart(reg),
		"SessionEnd":    hooks.HandleSessionEnd(reg),
		"PostToolUse":   hooks.HandlePostToolUse(reg),
		"SubagentStart": hooks.HandleSubagentStart(reg),
		"SubagentStop":  hooks.HandleSubagentStop(reg),
		"Stop":          hooks.HandleStop(reg),
		"Notification":  hooks.HandleNotification(reg),
	}
}

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

// TestSetup_NoTmpFileAfterSuccess verifies F5: atomic write leaves no .ccmc-tmp-*
// file behind after a successful setup.
func TestSetup_NoTmpFileAfterSuccess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", tmp)

	_, _, code := runCmd([]string{"setup"})
	if code != 0 {
		t.Fatalf("setup: expected exit 0, got %d", code)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ccmc-tmp-") {
			t.Errorf("found leftover temp file after setup: %s", e.Name())
		}
	}
}

// ── daemon stop security tests (F1 / F2) ─────────────────────────────────────

// testHookHandlers returns the hook handler map needed to start a test daemon.
func testHookHandlers(reg *daemon.Registry) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"SessionStart":  hooks.HandleSessionStart(reg),
		"SessionEnd":    hooks.HandleSessionEnd(reg),
		"PostToolUse":   hooks.HandlePostToolUse(reg),
		"SubagentStart": hooks.HandleSubagentStart(reg),
		"SubagentStop":  hooks.HandleSubagentStop(reg),
		"Stop":          hooks.HandleStop(reg),
		"Notification":  hooks.HandleNotification(reg),
	}
}

// startTestDaemon boots a real daemon in a background goroutine using the
// config-standard socket and PID paths derived from CCMC_DIR (which callers
// must set via t.Setenv before calling). This ensures runDaemonStop, which
// calls config.CcmcSocketPath() and config.CcmcDaemonPidPath(), dials the
// same socket and reads the same PID file the daemon wrote.
//
// dir must be a short path (≤ ~90 chars) to satisfy macOS's 104-byte SUN_LEN
// socket path limit. Use os.MkdirTemp("", "ccmc") — NOT t.TempDir() whose
// long test-name prefix can push the socket path over the limit.
func startTestDaemon(t *testing.T, dir string) (*daemon.Server, context.CancelFunc, <-chan error) {
	t.Helper()
	sockPath := filepath.Join(dir, "ccmc.sock")
	pidPath := filepath.Join(dir, "daemon.pid")

	reg := daemon.NewRegistry(filepath.Join(dir, "r.json"))
	srv := daemon.New(reg, testHookHandlers(reg),
		daemon.WithSocketPath(sockPath),
		daemon.WithPIDPath(pidPath),
		daemon.WithIdleTimeout(30*time.Minute),
	)

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- srv.Run(ctx) }()

	// Poll until the socket is present.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		cancel()
		t.Fatalf("test daemon did not create socket within 3s: %v", err)
	}
	return srv, cancel, ch
}

// TestDaemonStop_PIDSymlinkRefused verifies F1: when the PID file is a symlink,
// daemon stop refuses and the symlink target is untouched.
func TestDaemonStop_PIDSymlinkRefused(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)

	// Create an innocent target file and plant a symlink where the PID file lives.
	target := filepath.Join(tmp, "innocent-target")
	if err := os.WriteFile(target, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	pidPath := filepath.Join(tmp, "daemon.pid")
	if err := os.Symlink(target, pidPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, errOut, code := runCmd([]string{"daemon", "stop"})
	if code != 1 {
		t.Fatalf("expected exit 1 for symlink PID file, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(errOut, "symlink") {
		t.Errorf("expected 'symlink' in stderr, got: %q", errOut)
	}

	// The target must be untouched.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after stop: %v", err)
	}
	if string(data) != "do not touch\n" {
		t.Errorf("symlink target was modified: got %q", string(data))
	}
}

// TestDaemonStop_PIDMismatchRefused verifies F1: when the PID file contains a
// different PID than the daemon reports, daemon stop refuses with a clear error
// without signaling anything.
//
// The daemon runs in-process (goroutine) so its self-reported PID is
// os.Getpid(). We plant PID 1 (launchd — always alive but never our daemon)
// in the PID file. The mismatch is detected before any signal is sent, so
// launchd is never signaled and the test process is never at risk.
func TestDaemonStop_PIDMismatchRefused(t *testing.T) {
	// Use os.MkdirTemp for a short path — t.TempDir() names are too long for macOS's
	// 104-byte unix socket path limit.
	tmp, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	t.Setenv("CCMC_DIR", tmp)

	// Start a real daemon in-process so the status dial succeeds and returns a
	// PID (os.Getpid()). Its PID file is written at config.CcmcDaemonPidPath().
	_, cancel, done := startTestDaemon(t, tmp)
	defer func() { cancel(); <-done }()

	// Overwrite the PID file with PID 1 — always alive, always differs from
	// os.Getpid(), and runDaemonStop will refuse before signaling because
	// the mismatch is caught before proc.Signal().
	pidPath := filepath.Join(tmp, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1\n"), 0o600); err != nil {
		t.Fatalf("write fake PID file: %v", err)
	}

	// Install a SIGTERM handler as a safety net: if the mismatch check is
	// broken and SIGTERM is sent to any process, we want to know.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	_, errOut, code := runCmd([]string{"daemon", "stop"})
	if code != 1 {
		t.Fatalf("expected exit 1 for PID mismatch, got %d; stderr: %q", code, errOut)
	}
	if !strings.Contains(errOut, "PID file mismatch") {
		t.Errorf("expected 'PID file mismatch' in stderr, got: %q", errOut)
	}

	// The test process must not have received SIGTERM.
	select {
	case <-sigCh:
		t.Error("test process received SIGTERM — daemon stop signaled the wrong PID")
	default:
	}
}

// TestDaemonStop_StalePIDNoSocket verifies F1: when the PID file points to a
// dead PID and no daemon socket is present, daemon stop prints "no daemon
// running", removes the stale PID file, and exits 0.
func TestDaemonStop_StalePIDNoSocket(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CCMC_DIR", tmp)

	// Write a PID that is guaranteed dead: PID 1 is init (never our daemon),
	// but a more reliable dead PID is to use a negative value. Instead, use
	// /bin/sh's approach: find a PID that cannot exist. On macOS PID_MAX is
	// 99999. We write PID 2 which on macOS/Linux is either kernel or launchd —
	// the dial to our (absent) socket will fail regardless, so Status() returns
	// ErrDaemonUnavailable and we remove the file.
	pidPath := filepath.Join(tmp, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid()+99999)+"\n"), 0o600); err != nil {
		t.Fatalf("write stale PID file: %v", err)
	}

	out, _, code := runCmd([]string{"daemon", "stop"})
	if code != 0 {
		t.Fatalf("expected exit 0 for stale PID, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, "no daemon running") {
		t.Errorf("expected 'no daemon running' in stdout, got: %q", out)
	}

	// PID file must be removed.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("stale PID file was not removed after daemon stop")
	}
}

// TestDaemonStop_PostSIGTERMVerify verifies F2: starting a real out-of-process
// daemon and running daemon stop returns exit 0 and the daemon is gone.
//
// The daemon is a subprocess (re-invocation of the test binary via TestMain
// with CCMC_TEST_RUN_DAEMON=1) so that SIGTERM from runDaemonStop targets the
// child process, not the test process. An in-process goroutine cannot be used
// here because proc.Signal(SIGTERM) with the test-process PID would terminate
// the entire test run.
func TestDaemonStop_PostSIGTERMVerify(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Use os.MkdirTemp for a short path to satisfy macOS's 104-byte socket limit.
	tmp, err2 := os.MkdirTemp("", "ccmc")
	if err2 != nil {
		t.Fatalf("MkdirTemp: %v", err2)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	t.Setenv("CCMC_DIR", tmp)

	sockPath := filepath.Join(tmp, "ccmc.sock")
	client := ccmc.NewClient(ccmc.WithSocketPath(sockPath), ccmc.WithTimeout(2*time.Second))

	// Fork the daemon subprocess: re-invoke the test binary with CCMC_TEST_RUN_DAEMON=1.
	// The child binds on config-standard paths (derived from CCMC_DIR=tmp).
	// We do NOT call Release() so that daemonCmd.Wait() in the goroutine below reaps
	// the zombie promptly after the child exits. This allows the proc.Signal(0) poll
	// loop in runDaemonStop to see ESRCH (process gone) after the zombie is reaped.
	// If we called Release(), the zombie would persist until init reaps it, and the
	// poll loop would spin for up to 5s without seeing ESRCH.
	daemonCmd := buildDaemonHelperCmd(exe, tmp)
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("start daemon subprocess: %v", err)
	}
	// Goroutine to reap the child promptly when it exits, so the poll loop in
	// runDaemonStop sees ESRCH quickly after SIGTERM is processed. The goroutine
	// is buffered so it can complete without blocking if nobody reads procDone.
	procDone := make(chan struct{})
	go func() {
		_ = daemonCmd.Wait()
		close(procDone)
	}()
	t.Cleanup(func() {
		// Safety net: kill the child if still running.
		_ = daemonCmd.Process.Kill()
		// Give the Wait goroutine up to 2s to reap the child.
		select {
		case <-procDone:
		case <-time.After(2 * time.Second):
			// Best-effort — the test is done anyway.
		}
	})

	// Wait for the socket to appear (up to 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("daemon subprocess did not create socket within 3s")
	}

	// Confirm the daemon is alive via the client.
	if _, err := client.Status(); err != nil {
		t.Fatalf("daemon not alive before stop: %v", err)
	}

	// daemon stop must succeed.
	out, errOut, code := runCmd([]string{"daemon", "stop"})
	if code != 0 {
		t.Fatalf("daemon stop: expected exit 0, got %d; stdout: %q stderr: %q", code, out, errOut)
	}
	if !strings.Contains(out, "daemon stopped") {
		t.Errorf("expected 'daemon stopped' in stdout, got: %q", out)
	}

	// Wait for the subprocess to be reaped (the goroutine closes procDone after
	// Wait() returns, which happens after SIGTERM from runDaemonStop).
	select {
	case <-procDone:
	case <-time.After(6 * time.Second):
		t.Error("daemon subprocess did not exit within 6s after stop")
	}

	// The daemon must no longer be reachable.
	if _, err := client.Status(); err == nil {
		t.Error("daemon still reachable after daemon stop")
	}
}

// buildDaemonHelperCmd returns an exec.Cmd that re-invokes the current test
// binary as a daemon subprocess. The child detects CCMC_TEST_RUN_DAEMON=1 in
// TestMain and runs a real daemon on the config-standard paths under dir.
// stdout/stderr are explicitly redirected to os.DevNull so that the subprocess
// does not inherit the test harness's I/O pipes — an inherited pipe would block
// cmd.Wait() from returning until the child's inherited pipe end closes.
func buildDaemonHelperCmd(exe, dir string) *exec.Cmd {
	devnull, _ := os.Open(os.DevNull)
	cmd := exec.Command(exe, "-test.run=^$") //nolint:gosec — exe is os.Executable()
	cmd.Env = append(os.Environ(),
		"CCMC_TEST_RUN_DAEMON=1",
		"CCMC_DIR="+dir,
	)
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.Stdin = devnull
	return cmd
}
