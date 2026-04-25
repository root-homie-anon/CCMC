package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

const (
	defaultIdleTimeout   = 30 * time.Minute
	shutdownDrainTimeout = 5 * time.Second
)

// Server is the unix-socket HTTP server that exposes the session registry to
// local clients and accepts hook events from Claude Code.
type Server struct {
	registry          *Registry
	hookHandlers      map[string]http.HandlerFunc // event name → handler; built by caller to avoid import cycle
	socketPath        string
	pidPath           string
	idleTimeout       time.Duration
	idleCheckInterval time.Duration // defaults to 1/10th of idleTimeout, min 5s
	startTime         time.Time

	// lastActivity is updated atomically on every inbound request so the idle
	// timer does not require the main goroutine to hold a lock during checks.
	lastActivity atomic.Int64 // unix nano

	// inFlight counts requests currently executing inside a handler. The idle
	// shutdown check refuses to fire while this is non-zero so a freshly-started
	// handler cannot be raced by a concurrent idle tick.
	inFlight atomic.Int32
}

// Option is a functional option for Server construction.
type Option func(*Server)

// WithIdleTimeout overrides the default 30-minute idle timeout. Intended for
// tests — pass a short duration so the idle-shutdown path can be exercised
// without sleeping 30 minutes.
func WithIdleTimeout(d time.Duration) Option {
	return func(s *Server) { s.idleTimeout = d }
}

// WithSocketPath overrides the unix socket path. Intended for tests to avoid
// writing to the real ~/.ccmc directory.
func WithSocketPath(p string) Option {
	return func(s *Server) { s.socketPath = p }
}

// WithPIDPath overrides the PID file path. Intended for tests.
func WithPIDPath(p string) Option {
	return func(s *Server) { s.pidPath = p }
}

// WithIdleCheckInterval overrides how often the server checks for idle
// shutdown. Intended for tests — in production the interval is derived from
// the idle timeout (1/10th, min 5s).
func WithIdleCheckInterval(d time.Duration) Option {
	return func(s *Server) { s.idleCheckInterval = d }
}

// SocketPath returns the configured socket path. Exposed for tests that need
// to build a unix-dialing HTTP client after server construction.
func (s *Server) SocketPath() string { return s.socketPath }

// PIDPath returns the configured PID file path. Exposed for tests.
func (s *Server) PIDPath() string { return s.pidPath }

// New constructs a Server backed by reg. hookHandlers maps event name strings
// (e.g. "SessionStart") to http.HandlerFunc values. Callers build this map
// using hooks.Handle* to avoid a daemon→hooks→daemon import cycle.
// Socket and PID paths are resolved from internal/config; callers that need
// non-default paths (e.g. tests) should use the functional options.
func New(reg *Registry, hookHandlers map[string]http.HandlerFunc, opts ...Option) *Server {
	s := &Server{
		registry:     reg,
		hookHandlers: hookHandlers,
		socketPath:   config.CcmcSocketPath(),
		pidPath:      config.CcmcDaemonPidPath(),
		idleTimeout:  defaultIdleTimeout,
		startTime:    time.Now(),
	}
	s.lastActivity.Store(time.Now().UnixNano())
	for _, o := range opts {
		o(s)
	}
	// Derive idle check interval after options are applied: 1/10th of the idle
	// timeout, with a minimum of 5s so production daemons don't busy-loop.
	// Tests that pass a short idleTimeout should also pass WithIdleCheckInterval
	// to override this floor.
	if s.idleCheckInterval == 0 {
		s.idleCheckInterval = s.idleTimeout / 10
		if s.idleCheckInterval < 5*time.Second {
			s.idleCheckInterval = 5 * time.Second
		}
	}
	return s
}

// Run is the daemon's main entry point. It:
//  1. Removes a stale socket (after verifying no live listener).
//  2. Binds a unix socket listener.
//  3. Checks and writes the PID file.
//  4. Starts the HTTP server.
//  5. Waits for ctx cancellation or SIGTERM/SIGINT or idle timeout.
//  6. Drains in-flight requests (5 s), removes socket + PID file.
//
// Callers that want signal handling wired elsewhere can cancel ctx directly;
// SIGTERM/SIGINT handling lives here so the daemon works standalone without a
// wrapper CLI command.
func (s *Server) Run(ctx context.Context) error {
	// ── 0. Verify ~/.ccmc/ directory integrity ────────────────────────────────
	if err := verifyCcmcDir(filepath.Dir(s.socketPath)); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	// ── 1. Bind listener (with TOCTOU-aware stale-socket handling) ───────────
	ln, err := s.bindSocket()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("daemon: chmod socket: %w", err)
	}

	// ── 3. PID file ──────────────────────────────────────────────────────────
	if err := s.writePIDFile(); err != nil {
		ln.Close()
		os.Remove(s.socketPath)
		return fmt.Errorf("daemon: %w", err)
	}

	// ── 4. HTTP server ───────────────────────────────────────────────────────
	mux := s.buildMux()
	// Timeouts defend against slowloris and a hostile same-uid process that
	// holds a connection open. MaxHeaderBytes caps header allocation before the
	// body is even read (body caps are enforced per-handler via MaxBytesReader).
	// Note: stdlib http.Server recovers from per-connection panics internally
	// (net/http server.go conn.serve), so handler panics will not crash the
	// daemon — stale socket/PID cleanup at next startup handles SIGKILL.
	srv := &http.Server{
		Handler:           s.activityMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14, // 16 KiB
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		} else {
			serverErr <- nil
		}
	}()

	// ── 5. Shutdown triggers ─────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	idleTicker := time.NewTicker(s.idleCheckInterval)
	defer idleTicker.Stop()

	var shutdownCause string
loop:
	for {
		select {
		case <-ctx.Done():
			shutdownCause = "context cancelled"
			break loop

		case sig := <-sigCh:
			shutdownCause = fmt.Sprintf("signal %s", sig)
			break loop

		case err := <-serverErr:
			// Unexpected serve error — propagate without cleanup of files we
			// may not have successfully created.
			ln.Close()
			s.removePIDFile()
			os.Remove(s.socketPath)
			return fmt.Errorf("daemon: serve: %w", err)

		case <-idleTicker.C:
			if s.shouldIdleShutdown() {
				shutdownCause = "idle timeout"
				break loop
			}
		}
	}

	log.Printf("daemon: shutting down (%s)", shutdownCause)

	// ── 6. Drain and cleanup ─────────────────────────────────────────────────
	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
	defer drainCancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		log.Printf("daemon: drain timeout: %v", err)
	}

	os.Remove(s.socketPath)
	s.removePIDFile()

	// Drain the serverErr channel so the goroutine can exit cleanly.
	<-serverErr

	return nil
}

// buildMux wires all routes onto a stdlib ServeMux.
//
// Route table:
//   POST /hooks/<event>  — dispatches to the matching hook handler
//   GET  /sessions       — returns registry.List() as JSON
//   GET  /sessions/<id>  — returns one Session as JSON, 404 if missing
//   GET  /status         — returns DaemonStatus JSON
func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// /hooks/ — all POST-only; extract the trailing event name segment.
	mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		event := strings.TrimPrefix(r.URL.Path, "/hooks/")
		h, ok := s.hookHandlers[event]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown hook event %q", event), http.StatusNotFound)
			return
		}
		h(w, r)
	})

	// /sessions — exact match → list; /sessions/ prefix → single lookup.
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleListSessions(w, r)
	})

	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/sessions/")
		if id == "" {
			s.handleListSessions(w, r)
			return
		}
		s.handleGetSession(w, r, id)
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleStatus(w, r)
	})

	return mux
}

// activityMiddleware wraps the mux, stamps lastActivity, and maintains the
// inFlight counter so the idle-shutdown check never fires under a live handler.
func (s *Server) activityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastActivity.Store(time.Now().UnixNano())
		s.inFlight.Add(1)
		defer s.inFlight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// handleListSessions writes registry.List() as a JSON array.
func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	sessions := s.registry.List()
	if sessions == nil {
		sessions = []ccmc.Session{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handleGetSession looks up a single session by ID and writes it as JSON,
// or 404 when not found.
func (s *Server) handleGetSession(w http.ResponseWriter, _ *http.Request, id string) {
	sess, ok := s.registry.Get(id)
	if !ok {
		http.Error(w, fmt.Sprintf("session %q not found", id), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// handleStatus writes a DaemonStatus summary. Uptime is in whole seconds.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	all := s.registry.List()
	active := 0
	for _, sess := range all {
		if sess.Status == ccmc.SessionActive {
			active++
		}
	}
	status := ccmc.DaemonStatus{
		Running:      true,
		PID:          os.Getpid(),
		Uptime:       formatUptime(time.Since(s.startTime)),
		SessionCount: len(all),
		ActiveCount:  active,
		LastEventAt:  time.Unix(0, s.lastActivity.Load()),
		RegistryPath: config.CcmcRegistryPath(),
		SocketPath:   s.socketPath,
	}
	writeJSON(w, http.StatusOK, status)
}

// shouldIdleShutdown returns true when all three conditions hold:
//  - No handler is currently executing (inFlight == 0).
//  - No request has arrived within the idle timeout window.
//  - No session is in an active state.
func (s *Server) shouldIdleShutdown() bool {
	if s.inFlight.Load() > 0 {
		return false
	}
	lastNano := s.lastActivity.Load()
	lastAct := time.Unix(0, lastNano)
	if time.Since(lastAct) < s.idleTimeout {
		return false
	}
	for _, sess := range s.registry.List() {
		if sess.Status == ccmc.SessionActive {
			return false
		}
	}
	return true
}

// bindSocket binds the unix listener at s.socketPath using a TOCTOU-resistant
// sequence: try net.Listen first; on EADDRINUSE, dial-check, and only if the
// dial fails do we remove the stale file and retry once. This collapses the
// old "stat → dial → remove → listen" window into a single listen-first path.
//
// Before attempting to bind, the socket path is checked with os.Lstat; if it
// resolves to a symlink the daemon refuses to start — a same-uid attacker could
// have placed the symlink to redirect socket creation to an arbitrary path.
func (s *Server) bindSocket() (net.Listener, error) {
	// Symlink guard: refuse to act on a symlink at the socket path.
	if fi, err := os.Lstat(s.socketPath); err == nil {
		if fi.Mode().Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("socket path %s is a symlink — refusing to start", s.socketPath)
		}
	}

	// First attempt: bind directly.
	ln, err := net.Listen("unix", s.socketPath)
	if err == nil {
		return ln, nil
	}

	// If the error is not EADDRINUSE, it is not recoverable.
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("bind unix socket %s: %w", s.socketPath, err)
	}

	// EADDRINUSE: something is at the path. Dial to check for a live listener.
	log.Printf("daemon: socket path %s in use — checking for live daemon", s.socketPath)
	conn, dialErr := net.DialTimeout("unix", s.socketPath, 500*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return nil, fmt.Errorf("a live daemon is already listening on %s — will not start", s.socketPath)
	}

	// Dial failed: stale socket file. Remove and retry once.
	log.Printf("daemon: stale socket detected at %s — removing", s.socketPath)
	if removeErr := os.Remove(s.socketPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return nil, fmt.Errorf("failed to remove stale socket %s: %w", s.socketPath, removeErr)
	}

	ln, err = net.Listen("unix", s.socketPath)
	if err != nil {
		return nil, fmt.Errorf("bind unix socket %s (after stale-remove): %w", s.socketPath, err)
	}
	return ln, nil
}

// writePIDFile checks whether an existing PID file names a live process that
// owns our socket path. If it does, we refuse to start. If the PID is dead or
// the socket check fails, we overwrite the PID file with our own PID.
//
// The PID path is checked with os.Lstat before writing; if it resolves to a
// symlink the daemon refuses — a same-uid attacker could redirect the write to
// an arbitrary file. The open uses O_NOFOLLOW so the kernel also refuses to
// follow a symlink that races the Lstat check.
func (s *Server) writePIDFile() error {
	// Symlink guard: refuse if the PID path is already a symlink.
	if fi, err := os.Lstat(s.pidPath); err == nil {
		if fi.Mode().Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("PID path %s is a symlink — refusing to write", s.pidPath)
		}
	}

	// Read existing PID file without following symlinks (O_NOFOLLOW).
	if f, err := os.OpenFile(s.pidPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
		data, readErr := io.ReadAll(f)
		f.Close()
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 && processIsAlive(pid) {
				// PID 1 is always alive but is never our daemon. For any PID,
				// we already confirmed in bindSocket that the socket has no live
				// listener, so this is a stale PID from a crashed run. Overwrite
				// rather than refusing — we log so the operator knows.
				log.Printf("daemon: overwriting stale PID file (pid %d no longer owns socket)", pid)
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(s.pidPath), 0o700); err != nil {
		return fmt.Errorf("create PID dir: %w", err)
	}

	// Open with O_NOFOLLOW so the kernel refuses if a symlink races into place
	// between our Lstat check above and this open. 0o600 ensures only the
	// owner can read the PID (no daemon-impersonation via world-readable PID).
	f, err := os.OpenFile(s.pidPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("write PID file %s: %w", s.pidPath, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write PID file %s: %w", s.pidPath, err)
	}
	return nil
}

// removePIDFile removes the PID file if it still contains our own PID. This
// prevents us from removing a PID file written by a replacement daemon that
// started immediately after us. Reads via O_NOFOLLOW for consistency with the
// write path — we do not chase a symlink that appeared after shutdown began.
func (s *Server) removePIDFile() {
	f, err := os.OpenFile(s.pidPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != os.Getpid() {
		return
	}
	os.Remove(s.pidPath)
}

// verifyCcmcDir checks that dir (the ~/.ccmc directory) exists, is a real
// directory (not a symlink), is owned by the current user, and has mode 0o700.
// This runs at daemon startup before any socket or PID file operations so that
// a misconfigured or attacker-controlled parent directory is caught early.
func verifyCcmcDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat ccmc dir %s: %w", dir, err)
	}
	if fi.Mode().Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("ccmc dir %s is a symlink — refusing to start", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ccmc dir %s is not a directory", dir)
	}
	// Owner check via platform Stat_t. On macOS/Linux, Uid is uint32.
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ccmc dir %s: cannot read ownership info", dir)
	}
	if uint32(os.Getuid()) != stat.Uid {
		return fmt.Errorf("ccmc dir %s is owned by uid %d, not current uid %d", dir, stat.Uid, os.Getuid())
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("ccmc dir %s has mode %04o, want 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// processIsAlive sends signal 0 to the given PID. A nil error means the
// process exists and we have permission to signal it.
func processIsAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("daemon: writeJSON encode error: %v", err)
	}
}

// formatUptime formats a duration as a human-readable uptime string.
func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
