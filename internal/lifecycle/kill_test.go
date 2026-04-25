package lifecycle

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccmc/internal/daemon"
	"ccmc/internal/hooks"
	"ccmc/pkg/ccmc"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newKillTestClient builds a ccmc.Client dialing a real in-process daemon loaded
// with the given session. The server starts in a goroutine; cleanup shuts it down.
func newKillTestClient(t *testing.T, sess ccmc.Session) *ccmc.Client {
	t.Helper()

	tmp, err := os.MkdirTemp("", "ccmclife")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	sockPath := filepath.Join(tmp, "d.sock")
	pidPath := filepath.Join(tmp, "d.pid")
	reg := daemon.NewRegistry(filepath.Join(tmp, "r.json"))
	reg.Add(sess)

	srv := daemon.New(reg, killTestHandlers(reg),
		daemon.WithSocketPath(sockPath),
		daemon.WithPIDPath(pidPath),
		daemon.WithIdleTimeout(30*time.Minute),
	)

	ctx := t.Context()
	go func() { _ = srv.Run(ctx) }()

	// Poll for the socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("test daemon socket did not appear: %v", err)
	}

	return ccmc.NewClient(ccmc.WithSocketPath(sockPath), ccmc.WithTimeout(3*time.Second))
}

func killTestHandlers(reg *daemon.Registry) map[string]http.HandlerFunc {
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

// newNotFoundClient returns a client dialing a server that returns 404 for every
// session lookup — used to test the ErrSessionNotFound path without a real registry.
func newNotFoundClient(t *testing.T) *ccmc.Client {
	t.Helper()
	tmp, err := os.MkdirTemp("", "ccmclife404")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	sockPath := filepath.Join(tmp, "d404.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"running":true,"pid":1}`)) //nolint:errcheck
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(ln) }() //nolint:errcheck
	t.Cleanup(func() { server.Close() })

	return ccmc.NewClient(ccmc.WithSocketPath(sockPath), ccmc.WithTimeout(3*time.Second))
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestKill_HappyPath spawns a real sleep child, registers it in the daemon with
// its PID, then calls Kill and verifies the process is gone within 5 seconds.
//
// We run child.Wait() in a goroutine so the zombie is reaped promptly after
// SIGTERM — without the reap, the zombie process continues to respond to
// signal(0) with success (the standard Unix zombie behaviour), which would cause
// pollProcessGone to time out even though the process has exited. In real
// production use, Kill targets claude processes whose parent is a shell or iTerm
// terminal (not ccmc), so the kernel or launchd reaps the zombie without ccmc
// involvement. The goroutine here mirrors that reap behaviour in the test.
func TestKill_HappyPath(t *testing.T) {
	// Spawn a child process we own.
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	childPID := child.Process.Pid

	// Reap the child when it exits so signal(0) returns ESRCH promptly.
	waitDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(waitDone)
	}()

	t.Cleanup(func() {
		// Safety net: ensure the child is dead and reaped.
		_ = child.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
	})

	sess := ccmc.Session{
		ID:           "test-happy",
		ProjectPath:  t.TempDir(),
		Status:       ccmc.SessionActive,
		PID:          childPID,
		StartedAt:    time.Now(),
		LastActivity: time.Now(),
	}

	client := newKillTestClient(t, sess)

	if err := Kill(client, "test-happy"); err != nil {
		t.Fatalf("Kill: unexpected error: %v", err)
	}

	// Wait for the reap goroutine to confirm the child exited.
	select {
	case <-waitDone:
	case <-time.After(6 * time.Second):
		t.Error("process still alive 6s after Kill")
	}
}

// TestKill_EPERMPath verifies that attempting to SIGTERM PID 1 (launchd/init —
// always alive, never owned by the test user) returns ErrPermissionDenied.
func TestKill_EPERMPath(t *testing.T) {
	// Skip if running as root — root can signal PID 1.
	if os.Getuid() == 0 {
		t.Skip("running as root; EPERM test meaningless")
	}

	sess := ccmc.Session{
		ID:          "test-eperm",
		ProjectPath: t.TempDir(),
		Status:      ccmc.SessionActive,
		PID:         1, // launchd/init — not owned by us
		StartedAt:   time.Now(),
		LastActivity: time.Now(),
	}

	client := newKillTestClient(t, sess)

	err := Kill(client, "test-eperm")
	if err == nil {
		t.Fatal("expected error for EPERM, got nil")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got: %v", err)
	}
}

// TestKill_SessionNotFound verifies that Kill returns ErrSessionNotFound when
// the registry has no entry with the given ID.
func TestKill_SessionNotFound(t *testing.T) {
	client := newNotFoundClient(t)

	err := Kill(client, "nonexistent-session-id")
	if err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got: %v", err)
	}
}

// TestKill_ProcessAlreadyDead spawns a child, immediately kills it externally,
// then calls Kill. Expects nil (already gone) or a clear error — not a panic.
func TestKill_ProcessAlreadyDead(t *testing.T) {
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	childPID := child.Process.Pid

	// Kill it externally before calling Kill.
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("pre-kill child: %v", err)
	}
	// Wait to reap the zombie so signal(0) returns ESRCH quickly.
	_ = child.Wait()

	sess := ccmc.Session{
		ID:          "test-dead",
		ProjectPath: t.TempDir(),
		Status:      ccmc.SessionActive,
		PID:         childPID,
		StartedAt:   time.Now(),
		LastActivity: time.Now(),
	}

	client := newKillTestClient(t, sess)

	err := Kill(client, "test-dead")
	// Either nil (SIGTERM saw ESRCH — already gone) or ErrProcessTimeout
	// (very unlikely: kernel reused PID before we got there). Both are
	// acceptable; what is not acceptable is a panic or a non-structured error.
	if err != nil {
		// ErrProcessTimeout is acceptable when PID was recycled; any other error
		// should mention the PID or be a known sentinel.
		if !errors.Is(err, ErrProcessTimeout) && !strings.Contains(err.Error(), "kill:") {
			t.Errorf("unexpected error for already-dead process: %v", err)
		}
	}
}
