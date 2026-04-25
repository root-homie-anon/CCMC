package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccmc/internal/daemon"
	"ccmc/internal/hooks"
	"ccmc/pkg/ccmc"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// newReg returns an in-memory registry isolated to a temp dir.
func newReg(t *testing.T) *daemon.Registry {
	t.Helper()
	return daemon.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
}

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

// newTestServer builds a Server isolated to a temp dir with a configurable idle
// timeout. The server's socket and PID paths are placed inside a short temp dir
// to stay within macOS's 104-byte unix socket path limit.
func newTestServer(t *testing.T, reg *daemon.Registry, idleTimeout time.Duration) *daemon.Server {
	t.Helper()
	// Use os.MkdirTemp with a short prefix so the resulting path (including
	// the socket filename) stays under the 104-byte macOS SUN_LEN limit.
	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return daemon.New(reg, buildHookHandlers(reg),
		daemon.WithSocketPath(filepath.Join(dir, "d.sock")),
		daemon.WithPIDPath(filepath.Join(dir, "d.pid")),
		daemon.WithIdleTimeout(idleTimeout),
	)
}

// startServer runs the server in a background goroutine, polls until the
// socket is dialable (up to 2 s), then returns a cancel func and a done
// channel. Callers must drain done before returning.
func startServer(t *testing.T, s *daemon.Server) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- s.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", s.SocketPath())
		if err == nil {
			conn.Close()
			return cancel, ch
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not become ready on %s within 2s", s.SocketPath())
	return nil, nil
}

// unixClient returns an http.Client that dials sockPath for every connection.
func unixClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// do sends an HTTP request through the unix-socket client. The URL host is
// ignored; only the path matters.
func do(t *testing.T, client *http.Client, method, path, body string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://ccmc"+path, bodyReader)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// slurp reads and closes the response body, returning it as a string.
func slurp(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// ─── POST /hooks/SessionStart round-trip ─────────────────────────────────────

func TestServer_HookSessionStart_RoundTrip(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	body := `{"type":"SessionStart","session_id":"test-sess-1","project_path":"/projects/alpha","timestamp":"2026-04-25T10:00:00Z"}`
	resp := do(t, client, http.MethodPost, "/hooks/SessionStart", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /hooks/SessionStart: got %d, want 204; body: %s", resp.StatusCode, slurp(t, resp))
	}

	sess, ok := reg.Get("test-sess-1")
	if !ok {
		t.Fatal("session not in registry after SessionStart hook")
	}
	if sess.Status != ccmc.SessionActive {
		t.Fatalf("session status = %q, want %q", sess.Status, ccmc.SessionActive)
	}
	if sess.ProjectPath != "/projects/alpha" {
		t.Fatalf("session ProjectPath = %q, want %q", sess.ProjectPath, "/projects/alpha")
	}
}

// ─── GET /sessions ────────────────────────────────────────────────────────────

func TestServer_GetSessions_ReturnsList(t *testing.T) {
	reg := newReg(t)
	reg.Add(ccmc.Session{ID: "s1", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})
	reg.Add(ccmc.Session{ID: "s2", Status: ccmc.SessionDead, StartedAt: time.Now(), ActiveSubagents: []string{}})

	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/sessions", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions: got %d, want 200", resp.StatusCode)
	}

	var sessions []ccmc.Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
}

// ─── GET /sessions/:id ───────────────────────────────────────────────────────

func TestServer_GetSession_Found(t *testing.T) {
	reg := newReg(t)
	reg.Add(ccmc.Session{ID: "sess-known", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})

	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/sessions/sess-known", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions/sess-known: got %d, want 200", resp.StatusCode)
	}

	var sess ccmc.Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sess.ID != "sess-known" {
		t.Fatalf("session ID = %q, want %q", sess.ID, "sess-known")
	}
}

func TestServer_GetSession_NotFound(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/sessions/no-such-session", "")
	slurp(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /sessions/no-such-session: got %d, want 404", resp.StatusCode)
	}
}

// ─── GET /status ─────────────────────────────────────────────────────────────

func TestServer_GetStatus_SaneShape(t *testing.T) {
	reg := newReg(t)
	reg.Add(ccmc.Session{ID: "active-1", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})
	reg.Add(ccmc.Session{ID: "dead-1", Status: ccmc.SessionDead, StartedAt: time.Now(), ActiveSubagents: []string{}})

	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/status", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /status: got %d, want 200", resp.StatusCode)
	}

	var status ccmc.DaemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.PID <= 0 {
		t.Fatalf("status.PID = %d, want > 0", status.PID)
	}
	if status.SessionCount != 2 {
		t.Fatalf("status.SessionCount = %d, want 2", status.SessionCount)
	}
	if status.ActiveCount != 1 {
		t.Fatalf("status.ActiveCount = %d, want 1", status.ActiveCount)
	}
	if !status.Running {
		t.Fatal("status.Running = false, want true")
	}
}

// ─── Unknown hook event → 404 ────────────────────────────────────────────────

func TestServer_HookUnknownEvent_404(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodPost, "/hooks/MadeUpEvent", `{"type":"MadeUpEvent"}`)
	slurp(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /hooks/MadeUpEvent: got %d, want 404", resp.StatusCode)
	}
}

// ─── Method not allowed ───────────────────────────────────────────────────────

func TestServer_WrongMethod_405(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	client := unixClient(s.SocketPath())

	// GET on a POST-only route.
	resp := do(t, client, http.MethodGet, "/hooks/SessionStart", "")
	slurp(t, resp)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /hooks/SessionStart: got %d, want 405", resp.StatusCode)
	}

	// POST on a GET-only route.
	resp2 := do(t, client, http.MethodPost, "/sessions", `{}`)
	slurp(t, resp2)
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /sessions: got %d, want 405", resp2.StatusCode)
	}
}

// ─── Graceful shutdown ────────────────────────────────────────────────────────

func TestServer_GracefulShutdown(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)
	cancel, done := startServer(t, s)

	// Verify alive.
	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/status", "")
	slurp(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pre-shutdown GET /status: got %d, want 200", resp.StatusCode)
	}

	// Trigger graceful shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down within 10s after context cancel")
	}

	// Socket file must be gone.
	if _, err := os.Stat(s.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after shutdown: %s", s.SocketPath())
	}

	// PID file must be gone.
	if _, err := os.Stat(s.PIDPath()); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists after shutdown: %s", s.PIDPath())
	}
}

// ─── Idle timeout shutdown ────────────────────────────────────────────────────

// TestServer_IdleTimeout_TriggersShutdown verifies that the server exits on its
// own when idle (no active sessions, no recent requests) for the idle window.
// Uses a 200ms idle timeout and 50ms check interval so the test completes fast.
func TestServer_IdleTimeout_TriggersShutdown(t *testing.T) {
	reg := newReg(t)
	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s := daemon.New(reg, buildHookHandlers(reg),
		daemon.WithSocketPath(filepath.Join(dir, "d.sock")),
		daemon.WithPIDPath(filepath.Join(dir, "d.pid")),
		daemon.WithIdleTimeout(200*time.Millisecond),
		daemon.WithIdleCheckInterval(50*time.Millisecond),
	)

	ch := make(chan error, 1)
	go func() { ch <- s.Run(context.Background()) }()

	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("idle shutdown returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not idle-shutdown within 5s")
	}

	if _, err := os.Stat(s.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after idle shutdown: %s", s.SocketPath())
	}
	if _, err := os.Stat(s.PIDPath()); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists after idle shutdown: %s", s.PIDPath())
	}
}

// TestServer_IdleTimeout_ActiveSessionBlocks verifies that an active session
// prevents idle-shutdown even after the idle window elapses.
func TestServer_IdleTimeout_ActiveSessionBlocks(t *testing.T) {
	reg := newReg(t)
	reg.Add(ccmc.Session{ID: "alive", Status: ccmc.SessionActive, StartedAt: time.Now(), ActiveSubagents: []string{}})

	dir, err := os.MkdirTemp("", "ccmc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Very short idle timeout + check interval — would fire immediately without active sessions.
	s := daemon.New(reg, buildHookHandlers(reg),
		daemon.WithSocketPath(filepath.Join(dir, "d.sock")),
		daemon.WithPIDPath(filepath.Join(dir, "d.pid")),
		daemon.WithIdleTimeout(100*time.Millisecond),
		daemon.WithIdleCheckInterval(50*time.Millisecond),
	)
	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	// Let several idle-check ticks elapse.
	time.Sleep(600 * time.Millisecond)

	// Server must still respond.
	client := unixClient(s.SocketPath())
	resp := do(t, client, http.MethodGet, "/status", "")
	slurp(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /status with active session: got %d, want 200 (active session should block idle shutdown)", resp.StatusCode)
	}
}

// ─── Stale PID file handling ──────────────────────────────────────────────────

// TestServer_StalePIDFile_Overwrite verifies that a PID file from a prior
// crashed run (pointing to PID 1 which is alive but not our daemon — confirmed
// by clearStaleSocket having found no live socket) is overwritten at startup.
//
// Limitation: the live-socket check is the primary gate. Once clearStaleSocket
// passes (socket absent or dead), any named PID is treated as stale and
// overwritten, regardless of whether that PID is still alive. This means we
// cannot distinguish "PID 1 owns the socket" from "PID 1 happens to be alive"
// at the PID-file level — which is correct behaviour given that clearStaleSocket
// already confirmed no live listener.
func TestServer_StalePIDFile_Overwrite(t *testing.T) {
	reg := newReg(t)
	s := newTestServer(t, reg, 30*time.Minute)

	// Write a fake PID file pointing to PID 1 (init — always alive on any Unix).
	if err := os.MkdirAll(filepath.Dir(s.PIDPath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.PIDPath(), []byte(fmt.Sprintf("%d\n", 1)), 0o600); err != nil {
		t.Fatalf("write fake PID file: %v", err)
	}

	cancel, done := startServer(t, s)
	defer func() { cancel(); <-done }()

	// PID file must now contain our PID.
	data, err := os.ReadFile(s.PIDPath())
	if err != nil {
		t.Fatalf("read PID file after start: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "1" {
		t.Fatal("PID file still contains PID 1 — stale PID was not overwritten")
	}
	if content != fmt.Sprintf("%d", os.Getpid()) {
		t.Fatalf("PID file contains %q, want %d", content, os.Getpid())
	}
}
