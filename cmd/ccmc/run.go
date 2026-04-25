package main

// run is the testable entry point for the CLI. It accepts the argument slice
// (os.Args[1:] in production) and io.Writer sinks for stdout/stderr, and returns
// a Unix exit code. main() is a one-liner that calls os.Exit(run(...)).
//
// Keeping the dispatch logic here — rather than inline in main — means tests
// can exercise subcommand routing, argument parsing, and output without spawning
// a subprocess or calling os.Exit.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/internal/config"
	"ccmc/internal/daemon"
	"ccmc/internal/hooks"
	"ccmc/internal/lifecycle"
	"ccmc/internal/reference"
	"ccmc/internal/tui"
	"ccmc/pkg/ccmc"
)

// runProgram is the seam used to start the Bubble Tea program. Tests replace
// this to verify the dashboard path is taken without actually running the TUI.
var runProgram = func(p *tea.Program) error {
	_, err := p.Run()
	return err
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runDashboard(stderr)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "ccmc", version)
		return 0

	case "help", "--help", "-h":
		fmt.Fprint(stdout, helpText)
		return 0

	case "ls":
		return runLsCmd(rest, stdout, stderr)

	case "inspect":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "ccmc inspect: missing session-id\nUsage: ccmc inspect <session-id>")
			return 2
		}
		runInspect(rest[0])
		return 0

	case "ref":
		return runRef(rest, stdout, stderr)

	case "daemon":
		return runDaemon(rest, stdout, stderr)

	// daemon-start-internal is the hidden subcommand launched by StartDaemon /
	// StartDaemonWithBinary. It must match pkg/ccmc/api.go's daemonAutoStartSubcommand
	// constant ("daemon-start-internal") exactly so the auto-start fork lands here.
	// This subcommand is intentionally absent from --help output.
	case "daemon-start-internal":
		return runDaemonInternal(stderr)

	case "setup":
		return runSetup(stdout, stderr)

	case "kill":
		return runKill(rest, stdout, stderr)
	case "launch":
		return runLaunch(rest, stdout, stderr)
	case "inventory":
		return runInventory(rest, stdout, stderr)
	case "eval":
		return runEval(rest, stdout, stderr, os.Stdin)
	case "install":
		return runInstall(rest, stdout, stderr)
	case "tools":
		return runTools(rest, stdout, stderr, os.Stdin)
	case "iterm-install":
		fmt.Fprintln(stderr, "ccmc iterm-install: not yet implemented")
		return 2

	default:
		fmt.Fprintf(stderr, "ccmc: unknown command %q\nRun 'ccmc help' for usage.\n", cmd)
		return 2
	}
}

// ── daemon subcommands ────────────────────────────────────────────────────────

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ccmc daemon: missing subcommand (start|stop|status)")
		return 2
	}
	sub := args[0]
	switch sub {
	case "start":
		return runDaemonStart(stdout, stderr)
	case "stop":
		return runDaemonStop(stdout, stderr)
	case "status":
		return runDaemonStatus(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ccmc daemon: unknown subcommand %q (start|stop|status)\n", sub)
		return 2
	}
}

// runDaemonStart forks a detached daemon via pkg/ccmc/api.StartDaemon(), then
// polls the socket for up to 2 seconds to confirm the daemon is alive.
func runDaemonStart(stdout, stderr io.Writer) int {
	if err := ccmc.StartDaemon(); err != nil {
		fmt.Fprintf(stderr, "ccmc daemon start: fork failed: %v\n", err)
		return 1
	}

	socketPath := config.CcmcSocketPath()
	if !waitForSocket(socketPath, 2*time.Second) {
		fmt.Fprintln(stderr, "ccmc daemon start: daemon did not become ready within 2s")
		return 1
	}

	// Ping to get PID from status response for the confirmation message.
	client := ccmc.NewClient()
	ds, err := client.Status()
	if err != nil {
		// Socket appeared but status failed — still running, just report without PID.
		fmt.Fprintln(stdout, "daemon started")
		return 0
	}
	fmt.Fprintf(stdout, "daemon started (pid=%d)\n", ds.PID)
	return 0
}

// runDaemonStop reads the PID file, verifies the daemon is alive and matches,
// sends SIGTERM, and polls for exit up to 5s.
//
// Security hardening (Elliot review F1/F2):
//   - Lstat + O_NOFOLLOW prevents a symlink at the PID path from redirecting the
//     read to an arbitrary file (mirrors daemon's writePIDFile pattern).
//   - We dial the daemon's /status endpoint before signaling. If it returns
//     ErrDaemonUnavailable the daemon is already gone — we clean up and exit 0.
//     If status.PID differs from the file, we refuse — PID file is stale or
//     tampered, and signaling would hit a victim process.
//   - After SIGTERM we call /status once more. If it returns ErrDaemonUnavailable
//     the daemon received the signal and exited. If it succeeds with a *different*
//     PID the kernel reused the PID — we must not re-signal (F2 race mitigation).
//     The race is unmitigatable on macOS without kernel-level pidfd semantics;
//     a code comment documents this rather than pretending it is fully closed.
func runDaemonStop(stdout, stderr io.Writer) int {
	pidPath := config.CcmcDaemonPidPath()

	// Symlink guard: refuse if the PID path is a symlink, mirroring the daemon's
	// writePIDFile. Any Lstat error other than ENOENT is also surfaced — it is
	// unexpected and should not be silently swallowed.
	fi, lstatErr := os.Lstat(pidPath)
	if lstatErr != nil {
		if os.IsNotExist(lstatErr) {
			fmt.Fprintln(stdout, "no daemon running")
			return 0
		}
		fmt.Fprintf(stderr, "ccmc daemon stop: stat PID file: %v\n", lstatErr)
		return 1
	}
	if fi.Mode().Type()&os.ModeSymlink != 0 {
		fmt.Fprintf(stderr, "ccmc daemon stop: PID file %s is a symlink — refusing\n", pidPath)
		return 1
	}

	// Open with O_NOFOLLOW so the kernel refuses if a symlink races into place
	// between our Lstat check and this open.
	f, err := os.OpenFile(pidPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "no daemon running")
			return 0
		}
		fmt.Fprintf(stderr, "ccmc daemon stop: open PID file: %v\n", err)
		return 1
	}
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	f.Close()

	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if parseErr != nil || pid <= 0 {
		fmt.Fprintf(stderr, "ccmc daemon stop: invalid PID in %s\n", pidPath)
		return 1
	}

	// Dial the daemon before signaling. If it is already gone we treat that as
	// "no daemon running" and remove the stale PID file. If it is alive we
	// compare its self-reported PID to the file — a mismatch means the file is
	// stale or tampered and we refuse to signal.
	client := ccmc.NewClient(ccmc.WithTimeout(2 * time.Second))
	ds, statusErr := client.Status()
	if statusErr != nil {
		if errors.Is(statusErr, ccmc.ErrDaemonUnavailable) {
			// Daemon socket is gone — PID file is stale. Remove and exit cleanly.
			os.Remove(pidPath)
			fmt.Fprintln(stdout, "no daemon running")
			return 0
		}
		fmt.Fprintf(stderr, "ccmc daemon stop: query status: %v\n", statusErr)
		return 1
	}
	if ds.PID != pid {
		fmt.Fprintf(stderr,
			"ccmc daemon stop: PID file mismatch (file=%d daemon=%d) — refusing to signal; remove %s manually if you trust it\n",
			pid, ds.PID, pidPath)
		return 1
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintln(stdout, "no daemon running")
		return 0
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || strings.Contains(err.Error(), "no such process") {
			fmt.Fprintln(stdout, "no daemon running")
			return 0
		}
		fmt.Fprintf(stderr, "ccmc daemon stop: signal: %v\n", err)
		return 1
	}

	// Post-signal liveness check (F2 race mitigation): a SIGTERM at T+0 and a
	// PID reuse at T+1ms are undetectable on macOS without pidfd_open. We call
	// Status() once immediately after signaling. If the daemon is gone
	// (ErrDaemonUnavailable) we treat that as success. If Status() returns a
	// *different* PID the kernel reused the slot — we must not re-signal.
	// This race window is acknowledged and unmitigatable without daemon-side
	// sentinels (e.g. a generation nonce in the status response).
	ds2, postErr := client.Status()
	if postErr != nil && errors.Is(postErr, ccmc.ErrDaemonUnavailable) {
		// Daemon is gone — SIGTERM succeeded.
		fmt.Fprintln(stdout, "daemon stopped")
		return 0
	}
	if postErr == nil && ds2.PID != pid {
		// PID was reused after our SIGTERM — do not re-signal the new process.
		fmt.Fprintln(stdout, "daemon stopped")
		return 0
	}

	// Poll for process exit up to 5s.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Signal 0 failed — process is gone.
			fmt.Fprintln(stdout, "daemon stopped")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Fprintf(stderr, "ccmc daemon stop: process %d did not exit within 5s\n", pid)
	return 1
}

// runDaemonStatus queries the daemon's /status endpoint and prints a summary.
func runDaemonStatus(stdout, stderr io.Writer) int {
	client := ccmc.NewClient()
	ds, err := client.Status()
	if err != nil {
		if errors.Is(err, ccmc.ErrDaemonUnavailable) {
			fmt.Fprintln(stderr, "daemon not running")
			return 1
		}
		fmt.Fprintf(stderr, "ccmc daemon status: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "pid           %d\n", ds.PID)
	fmt.Fprintf(stdout, "uptime        %s\n", ds.Uptime)
	fmt.Fprintf(stdout, "active        %d\n", ds.ActiveCount)
	fmt.Fprintf(stdout, "total         %d\n", ds.SessionCount)
	fmt.Fprintf(stdout, "socket        %s\n", ds.SocketPath)
	return 0
}

// runDaemonInternal is the daemon event loop. It is invoked by StartDaemonWithBinary
// as the hidden "daemon-start-internal" subcommand. stderr is the only output
// sink since stdout is redirected to daemon.log when running detached.
//
// This subcommand is NOT in --help output; it's an implementation detail of the
// daemon auto-start mechanism. The subcommand name must match the constant
// daemonAutoStartSubcommand in pkg/ccmc/api.go.
func runDaemonInternal(stderr io.Writer) int {
	// Route daemon log to the log package so all daemon output lands in daemon.log
	// when running detached (stdout/stderr were redirected by StartDaemonWithBinary).
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetPrefix("[daemon] ")

	reg := daemon.NewRegistry("")

	// Load previous snapshot so the registry survives a restart. LoadFromSnapshot
	// logs internally and never returns an error — it silently skips a missing
	// or corrupt snapshot file.
	reg.LoadFromSnapshot()

	// Build hook handler map — one entry per CC hook event.
	handlerMap := buildHookHandlers(reg)

	srv := daemon.New(reg, handlerMap)

	// Context cancelled by SIGTERM/SIGINT — Server.Run also listens for these
	// signals, but we cancel the context too so the select in Run unblocks first
	// on context cancellation. Either path is fine; belt-and-suspenders.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()
	defer cancel()

	// Start periodic snapshot loop (30s interval in production). The loop writes
	// a final snapshot on ctx cancellation so state is preserved across restarts.
	reg.StartSnapshotLoop(ctx, 30*time.Second)

	if err := srv.Run(ctx); err != nil {
		log.Printf("error: %v", err)
		fmt.Fprintf(stderr, "ccmc daemon-start-internal: %v\n", err)
		return 1
	}
	return 0
}

// buildHookHandlers constructs the event-name → http.HandlerFunc map that
// daemon.New expects. This wires internal/hooks handler functions to the server
// without creating an import cycle (daemon ← hooks ← daemon would cycle;
// the map is assembled here in the CLI layer).
func buildHookHandlers(reg *daemon.Registry) map[string]http.HandlerFunc {
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

// ── ls upgrade (task 29) ──────────────────────────────────────────────────────

// runLsCmd handles "ccmc ls [--no-daemon]". It prefers daemon data when
// available (status badge column) and falls back to filesystem scan on
// ErrDaemonUnavailable or when --no-daemon is passed.
func runLsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	noDaemon := fs.Bool("no-daemon", false, "skip daemon; use filesystem scan only")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if !*noDaemon {
		// Attempt daemon with 1s timeout — don't block the shell forever.
		client := ccmc.NewClient(ccmc.WithTimeout(1 * time.Second))
		sessions, err := client.ListSessions()
		if err == nil {
			printLsWithStatus(stdout, sessions)
			return 0
		}
		if !errors.Is(err, ccmc.ErrDaemonUnavailable) {
			fmt.Fprintf(stderr, "ccmc ls: daemon error: %v\n", err)
			return 1
		}
		// ErrDaemonUnavailable — fall through to filesystem scan.
		fmt.Fprintln(stderr, "daemon not running; using filesystem-only mode")
	}

	sessions, err := daemon.ScanSessions()
	if err != nil {
		fmt.Fprintf(stderr, "ccmc ls: %v\n", err)
		return 1
	}

	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No sessions found.")
		return 0
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSESSION\tLAST ACTIVITY\tSIZE")
	fmt.Fprintln(w, "-------\t-------\t-------------\t----")
	for _, s := range sessions {
		age := formatAge(s.LastActivity)
		size := formatSize(s.ContextEstimate)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ProjectName, truncate(s.ID, 12), age, size)
	}
	w.Flush()
	return 0
}

// printLsWithStatus prints the session table with a STATUS column. Daemon data
// carries live status so we include the badge here. No terminal colours — that's
// Phase 4 TUI work.
func printLsWithStatus(stdout io.Writer, sessions []ccmc.Session) {
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No sessions found.")
		return
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSESSION\tSTATUS\tLAST ACTIVITY\tSIZE")
	fmt.Fprintln(w, "-------\t-------\t------\t-------------\t----")
	for _, s := range sessions {
		age := formatAge(s.LastActivity)
		size := formatSize(s.ContextEstimate)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.ProjectName, truncate(s.ID, 12), string(s.Status), age, size)
	}
	w.Flush()
}

// ── setup (task 28) ───────────────────────────────────────────────────────────

// defaultConfigYAML is the content written to ~/.ccmc/config.yaml on first run.
// It matches the schema in CLAUDE(4).md exactly. The anthropic_api_key field is
// left empty — the user supplies the key via env var or keymaster.
const defaultConfigYAML = `# ~/.ccmc/config.yaml
daemon:
  socket: ~/.ccmc/ccmc.sock
  auto_start: true
  auto_stop_minutes: 30
  scan_interval_seconds: 10

hooks:
  installed: false
  events:
    - SessionStart
    - SessionEnd
    - PostToolUse
    - SubagentStart
    - SubagentStop
    - Stop
    - Notification

reference:
  version: "2026.04"

integrator:
  anthropic_api_key: ""
  model: "claude-sonnet-4-6"
  clone_dir: ~/.ccmc/tools/

iterm:
  installed: false
  poll_interval_seconds: 5
`

func runSetup(stdout, stderr io.Writer) int {
	ccmcDir := config.CcmcDir()
	configPath := config.CcmcConfigPath()
	settingsPath := config.ClaudeSettingsPath()

	alreadySetUp := true

	// ── 1. Create ~/.ccmc/ if missing ────────────────────────────────────────
	if _, err := os.Stat(ccmcDir); os.IsNotExist(err) {
		if err := os.MkdirAll(ccmcDir, 0o700); err != nil {
			fmt.Fprintf(stderr, "ccmc setup: create %s: %v\n", ccmcDir, err)
			return 1
		}
		alreadySetUp = false
	}

	// ── 2. Write default config.yaml if missing ───────────────────────────────
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := writeAtomicFile(configPath, []byte(defaultConfigYAML), 0o600); err != nil {
			fmt.Fprintf(stderr, "ccmc setup: write config.yaml: %v\n", err)
			return 1
		}
		alreadySetUp = false
	}

	// ── 3. Install hooks into ~/.claude/settings.json ─────────────────────────
	bakPath := settingsPath + ".bak"
	bakExistedBefore := fileExists(bakPath)

	if err := hooks.Install(hooks.InstallerOptions{}); err != nil {
		fmt.Fprintf(stderr, "ccmc setup: install hooks: %v\n", err)
		return 1
	}

	// Determine whether the installer wrote anything (it's a no-op if hooks are
	// current). We detect a new write by checking whether .bak appeared or changed.
	if !bakExistedBefore && fileExists(bakPath) {
		fmt.Fprintf(stdout, "hooks installed (backup: %s)\n", bakPath)
		alreadySetUp = false
	} else if !alreadySetUp {
		fmt.Fprintf(stdout, "hooks installed (backup: %s)\n", bakPath)
	}

	// ── 4. Print outcome ──────────────────────────────────────────────────────
	if alreadySetUp {
		fmt.Fprintln(stdout, "already set up; run `ccmc daemon start` to start the daemon.")
		return 0
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Setup complete. Next steps:")
	fmt.Fprintln(stdout, "  ccmc daemon start    — start the background daemon")
	fmt.Fprintln(stdout, "  ccmc daemon status   — verify it is running")
	return 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeAtomicFile writes data to dst by first writing to a temp file in the same
// directory, then calling os.Rename. This ensures dst is never partially written
// even if the process is killed mid-write (mirrors hooks/installer.go writeAtomic).
func writeAtomicFile(dst string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, ".ccmc-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()

	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
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

// waitForSocket polls socketPath every 50 ms until the file appears or deadline.
// Mirrors the function in pkg/ccmc/api.go; kept local to avoid reaching into the
// package internals from the CLI layer.
func waitForSocket(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ── kill / launch (task 37–39) ────────────────────────────────────────────────

// runKill implements "ccmc kill <session-id>". It resolves the session PID from
// the daemon registry (or OS process table fallback), sends SIGTERM, and polls
// for exit up to 5 seconds. Exits 1 on failure.
func runKill(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "ccmc kill: missing session-id\nUsage: ccmc kill <session-id>")
		return 2
	}
	id := args[0]

	client := ccmc.NewClient(ccmc.WithAutoStart(true))
	if err := lifecycle.Kill(client, id); err != nil {
		fmt.Fprintf(stderr, "ccmc kill: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "killed session %s\n", id)
	return 0
}

// runLaunch implements "ccmc launch <dir>". It validates the directory, opens a
// new iTerm tab running "claude" (with subprocess fallback), and polls the daemon
// for the new session ID. Exits 1 on failure.
func runLaunch(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "ccmc launch: missing directory\nUsage: ccmc launch <dir>")
		return 2
	}
	dir := args[0]

	if err := lifecycle.ValidateDir(dir); err != nil {
		fmt.Fprintf(stderr, "ccmc launch: %v\n", err)
		return 1
	}

	client := ccmc.NewClient(ccmc.WithAutoStart(true))
	id, err := lifecycle.Launch(client, dir)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc launch: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "launched session %s in %s\n", id, dir)
	return 0
}

// ── dashboard (task 41) ───────────────────────────────────────────────────────

// runDashboard launches the Bubble Tea TUI dashboard. It constructs a daemon
// client with auto-start so the daemon is transparently started if not running,
// loads the embedded reference engine, and runs the program in alt-screen mode.
//
// The daemon is NOT stopped on exit — its own idle-timeout reaps it eventually.
// This is intentional: the user may have other tools or CLI commands still using
// the daemon.
func runDashboard(stderr io.Writer) int {
	// Auto-start the daemon if it is not already running so the dashboard has
	// live session data from the first render.
	client := ccmc.NewClient(ccmc.WithAutoStart(true))

	// Load the embedded reference YAML data. A failure here means the binary
	// was built with a missing or corrupt embed — surface it immediately.
	entries, err := reference.LoadAll()
	if err != nil {
		fmt.Fprintf(stderr, "ccmc: load reference data: %v\n", err)
		return 1
	}
	engine := reference.NewEngine(entries)

	app := tui.NewAppWithConfig(tui.AppConfig{
		Client: client,
		Engine: engine,
	})

	p := tea.NewProgram(app,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if err := runProgram(p); err != nil {
		fmt.Fprintf(stderr, "ccmc: dashboard: %v\n", err)
		return 1
	}
	return 0
}
