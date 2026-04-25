package ccmc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"ccmc/internal/daemon"
	"ccmc/internal/hooks"
	"ccmc/pkg/ccmc"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// buildHookHandlers returns the standard hook handler map wired to reg.
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

// newTestServer builds a daemon.Server isolated to a short temp dir and returns
// it plus the socket path. Uses os.MkdirTemp with a short prefix to stay under
// macOS's 104-byte SUN_LEN socket path limit.
func newTestServer(t *testing.T, reg *daemon.Registry) (*daemon.Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")
	pidPath := filepath.Join(dir, "d.pid")
	srv := daemon.New(reg, buildHookHandlers(reg),
		daemon.WithSocketPath(sockPath),
		daemon.WithPIDPath(pidPath),
		daemon.WithIdleTimeout(30*time.Minute),
	)
	return srv, sockPath
}

// startServer runs the server in a background goroutine and polls until it
// accepts connections (up to 2 s). Returns a cancel func and a done channel.
func startServer(t *testing.T, srv *daemon.Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", srv.SocketPath())
		if err == nil {
			conn.Close()
			return cancel, ch
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatalf("server did not become ready on %s within 2s", srv.SocketPath())
	return nil, nil
}

// newClient builds a ccmc.Client pointed at the given socket path with a short
// timeout so tests fail fast rather than hanging.
func newClient(socketPath string) *ccmc.Client {
	return ccmc.NewClient(
		ccmc.WithSocketPath(socketPath),
		ccmc.WithTimeout(2*time.Second),
	)
}

// ─── Ping ─────────────────────────────────────────────────────────────────────

func TestClient_Ping_Alive(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	if err := c.Ping(); err != nil {
		t.Fatalf("Ping: unexpected error: %v", err)
	}
}

// ─── Status ───────────────────────────────────────────────────────────────────

func TestClient_Status_CorrectDecoding(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	reg.Add(ccmc.Session{ID: "a1", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})
	reg.Add(ccmc.Session{ID: "d1", Status: ccmc.SessionDead, StartedAt: time.Now(), ActiveSubagents: []string{}})

	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	status, err := c.Status()
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if !status.Running {
		t.Error("status.Running = false, want true")
	}
	if status.PID <= 0 {
		t.Errorf("status.PID = %d, want > 0", status.PID)
	}
	if status.SessionCount != 2 {
		t.Errorf("status.SessionCount = %d, want 2", status.SessionCount)
	}
	if status.ActiveCount != 1 {
		t.Errorf("status.ActiveCount = %d, want 1", status.ActiveCount)
	}
	if status.Uptime == "" {
		t.Error("status.Uptime is empty")
	}
}

// ─── ListSessions ─────────────────────────────────────────────────────────────

func TestClient_ListSessions_ReturnsSessions(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	reg.Add(ccmc.Session{ID: "s1", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})
	reg.Add(ccmc.Session{ID: "s2", Status: ccmc.SessionIdle, StartedAt: time.Now(), ActiveSubagents: []string{}})

	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	sessions, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("ListSessions: got %d sessions, want 2", len(sessions))
	}
}

func TestClient_ListSessions_EmptyRegistryReturnsSlice(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	sessions, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions empty: unexpected error: %v", err)
	}
	// Server returns [] not null — must decode to non-nil empty slice.
	if sessions == nil {
		t.Error("ListSessions: got nil, want empty non-nil slice")
	}
	if len(sessions) != 0 {
		t.Errorf("ListSessions: got %d sessions, want 0", len(sessions))
	}
}

// ─── GetSession ───────────────────────────────────────────────────────────────

func TestClient_GetSession_Found(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	reg.Add(ccmc.Session{
		ID:              "known-id",
		Status:          ccmc.SessionActive,
		ProjectPath:     "/projects/alpha",
		StartedAt:       time.Now(),
		ActiveSubagents: []string{},
	})

	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	sess, err := c.GetSession("known-id")
	if err != nil {
		t.Fatalf("GetSession: unexpected error: %v", err)
	}
	if sess.ID != "known-id" {
		t.Errorf("sess.ID = %q, want %q", sess.ID, "known-id")
	}
	if sess.ProjectPath != "/projects/alpha" {
		t.Errorf("sess.ProjectPath = %q, want %q", sess.ProjectPath, "/projects/alpha")
	}
}

func TestClient_GetSession_NotFound(t *testing.T) {
	reg := daemon.NewRegistry(filepath.Join(t.TempDir(), "r.json"))
	srv, sockPath := newTestServer(t, reg)
	cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	c := newClient(sockPath)
	_, err := c.GetSession("no-such-id")
	if err == nil {
		t.Fatal("GetSession missing id: expected error, got nil")
	}
	// Should contain "not found" messaging.
	if !containsStr(err.Error(), "not found") {
		t.Errorf("GetSession error = %q, want 'not found' in message", err.Error())
	}
}

// ─── ErrDaemonUnavailable — socket file missing ───────────────────────────────

func TestClient_ErrDaemonUnavailable_SocketMissing(t *testing.T) {
	// Point the client at a socket path that doesn't exist.
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "nonexistent.sock")

	c := newClient(sockPath)

	if err := c.Ping(); !errors.Is(err, ccmc.ErrDaemonUnavailable) {
		t.Errorf("Ping missing socket: got %v, want ErrDaemonUnavailable", err)
	}

	_, err := c.Status()
	if !errors.Is(err, ccmc.ErrDaemonUnavailable) {
		t.Errorf("Status missing socket: got %v, want ErrDaemonUnavailable", err)
	}

	_, err = c.ListSessions()
	if !errors.Is(err, ccmc.ErrDaemonUnavailable) {
		t.Errorf("ListSessions missing socket: got %v, want ErrDaemonUnavailable", err)
	}

	_, err = c.GetSession("any")
	if !errors.Is(err, ccmc.ErrDaemonUnavailable) {
		t.Errorf("GetSession missing socket: got %v, want ErrDaemonUnavailable", err)
	}
}

// ─── Per-call timeout fires when socket exists but nobody accepts ─────────────

// TestClient_Timeout_StubListenerNeverAccepts creates a unix listener that
// accepts the network connection at the socket level but never reads or
// responds. The client's per-call timeout must fire and return an error.
func TestClient_Timeout_StubListenerNeverAccepts(t *testing.T) {
	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "stub.sock")

	// Bind the socket so the file exists and the OS accepts the TCP-layer
	// connection — but we never call ln.Accept(), so the HTTP request hangs.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	c := ccmc.NewClient(
		ccmc.WithSocketPath(sockPath),
		ccmc.WithTimeout(150*time.Millisecond), // short so the test is fast
	)

	start := time.Now()
	err = c.Ping()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ping against non-responding server: expected error, got nil")
	}
	// Elapsed must be ~≥ timeout but not wildly over it (5x as generous ceiling).
	if elapsed > 5*150*time.Millisecond {
		t.Errorf("Ping took %v, expected timeout near 150ms", elapsed)
	}
}

// ─── Auto-start happy path ────────────────────────────────────────────────────

// TestClient_AutoStart_HappyPath compiles a small helper binary that starts a
// real daemon on the expected socket path, then verifies the client auto-starts
// it and retries successfully.
//
// The helper binary is the current test binary re-invoked with the
// CCMC_TEST_DAEMON_SOCKET env var set and the "daemon-start-internal"
// subcommand. The TestMain entry point detects this and runs the daemon loop.
func TestClient_AutoStart_HappyPath(t *testing.T) {
	// Build the helper binary from this package's test binary.
	helperBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	// Set the env var the re-invoked binary will read to know where to bind.
	t.Setenv("CCMC_TEST_DAEMON_SOCKET", sockPath)

	c := ccmc.NewClient(
		ccmc.WithSocketPath(sockPath),
		ccmc.WithTimeout(2*time.Second),
		ccmc.WithAutoStart(true),
		ccmc.WithBinaryPath(helperBin),
	)

	// The socket doesn't exist yet — auto-start should fork the helper, wait
	// for the socket, and retry.
	status, err := c.Status()
	if err != nil {
		t.Fatalf("Status with auto-start: unexpected error: %v", err)
	}
	if !status.Running {
		t.Error("auto-started daemon: status.Running = false, want true")
	}
}

// ─── Auto-start failure path ──────────────────────────────────────────────────

// TestClient_AutoStart_BinaryNotExist verifies that when the binary path is
// invalid the client returns ErrDaemonUnavailable within the timeout, not hang.
func TestClient_AutoStart_BinaryNotExist(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")

	c := ccmc.NewClient(
		ccmc.WithSocketPath(sockPath),
		ccmc.WithTimeout(500*time.Millisecond),
		ccmc.WithAutoStart(true),
		ccmc.WithBinaryPath("/nonexistent-binary-path-ccmc-test"),
	)

	start := time.Now()
	_, err := c.Status()
	elapsed := time.Since(start)

	if !errors.Is(err, ccmc.ErrDaemonUnavailable) {
		t.Errorf("Status with bad binary: got %v, want ErrDaemonUnavailable", err)
	}
	// Must not hang beyond the socket-wait timeout (~2s) plus some buffer.
	// In practice fork fails immediately, so this is fast.
	if elapsed > 5*time.Second {
		t.Errorf("Status with bad binary took %v — should fail fast", elapsed)
	}
}

// ─── TestMain: daemon loop for auto-start helper ─────────────────────────────

// TestMain detects when the test binary is re-invoked as a daemon helper and
// runs a real daemon on the socket path specified by CCMC_TEST_DAEMON_SOCKET.
// This implements the auto-start seam required by TestClient_AutoStart_HappyPath
// without a separate compiled binary or a fixture file.
func TestMain(m *testing.M) {
	// Detect re-invocation as a daemon helper. The client invokes:
	//   <test-binary> daemon-start-internal
	// with CCMC_TEST_DAEMON_SOCKET set to the socket path.
	if len(os.Args) >= 2 && os.Args[1] == "daemon-start-internal" {
		runEmbeddedDaemon()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runEmbeddedDaemon starts a real daemon on the socket path from the env var.
// This runs inside the re-invoked test binary process (the "auto-start child").
func runEmbeddedDaemon() {
	sockPath := os.Getenv("CCMC_TEST_DAEMON_SOCKET")
	if sockPath == "" {
		os.Stderr.WriteString("ccmc test daemon: CCMC_TEST_DAEMON_SOCKET not set\n")
		os.Exit(1)
	}

	dir := filepath.Dir(sockPath)
	pidPath := filepath.Join(dir, "d.pid")

	reg := daemon.NewRegistry(filepath.Join(dir, "r.json"))
	srv := daemon.New(reg, buildHookHandlers(reg),
		daemon.WithSocketPath(sockPath),
		daemon.WithPIDPath(pidPath),
		daemon.WithIdleTimeout(10*time.Second), // short so it exits after the test
	)

	// Run until idle or signal.
	if err := srv.Run(context.Background()); err != nil {
		os.Stderr.WriteString("ccmc test daemon: " + err.Error() + "\n")
		os.Exit(1)
	}
}

// ─── StartDaemon smoke test ───────────────────────────────────────────────────

// TestStartDaemon_BinaryPathEmpty verifies StartDaemonWithBinary returns a
// clear error when binaryPath is empty, without panicking.
func TestStartDaemon_BinaryPathEmpty(t *testing.T) {
	err := ccmc.StartDaemonWithBinary("", "/tmp/nonexistent.sock")
	if err == nil {
		t.Fatal("StartDaemonWithBinary empty path: expected error, got nil")
	}
	if !containsStr(err.Error(), "binary path") {
		t.Errorf("error = %q, want mention of 'binary path'", err.Error())
	}
}

// TestStartDaemon_NonExistentBinary verifies StartDaemonWithBinary returns an
// error when the binary doesn't exist, rather than panicking.
func TestStartDaemon_NonExistentBinary(t *testing.T) {
	err := ccmc.StartDaemonWithBinary("/no/such/binary", "/tmp/nonexistent.sock")
	if err == nil {
		t.Fatal("StartDaemonWithBinary bad path: expected error, got nil")
	}
}

// ─── go vet / build check for unused import ───────────────────────────────────

// Ensure exec is used (imported for TestMain daemon helper command checks).
var _ = exec.Command

// ─── helpers ──────────────────────────────────────────────────────────────────

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
