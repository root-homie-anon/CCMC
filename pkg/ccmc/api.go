package ccmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"ccmc/internal/config"
)

// ErrDaemonUnavailable is returned when the unix socket is missing or the
// connection is refused. Callers use this to fall back to filesystem-only mode.
var ErrDaemonUnavailable = errors.New("ccmc daemon unavailable")

// defaultCallTimeout is the per-request timeout applied when no ClientOption
// overrides it. 2 seconds keeps the CLI snappy without hanging on a frozen
// daemon — the caller gets ErrDaemonUnavailable rather than blocking forever.
const defaultCallTimeout = 2 * time.Second

// daemonAutoStartSubcommand is the CLI subcommand that starts the daemon loop.
// This is the coupling point between the API client (task 24/26) and the CLI
// wiring that hasn't landed yet (task 25). When task 25 ships, it must register
// a subcommand named exactly this string in cmd/ccmc/main.go. Until then,
// WithAutoStart(true) will fail with ErrDaemonUnavailable if the binary doesn't
// support the subcommand — that error is surfaced to the caller, not swallowed.
const daemonAutoStartSubcommand = "daemon-start-internal"

// Client is the API client for the CCMC daemon. It dials the unix socket and
// wraps the HTTP responses into typed Go values.
//
// Zero value is not usable — construct with NewClient.
type Client struct {
	socketPath  string
	timeout     time.Duration
	httpClient  *http.Client
	autoStart   bool
	binaryPath  string // binary to exec for auto-start; defaults to os.Args[0]
}

// ClientOption is a functional option for Client construction.
type ClientOption func(*Client)

// WithTimeout sets the per-call timeout. Default is 2 seconds.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.timeout = d }
}

// WithAutoStart configures the client to fork a detached daemon process when
// the socket is not reachable, then retry the original request once after the
// socket appears. If the fork fails or the socket does not appear within 2
// seconds, ErrDaemonUnavailable is returned with context.
//
// Auto-start invokes daemonAutoStartSubcommand on the current binary
// (os.Args[0] by default, overridable via WithBinaryPath). Task 25 must wire
// that subcommand before auto-start can succeed in production.
func WithAutoStart(enabled bool) ClientOption {
	return func(c *Client) { c.autoStart = enabled }
}

// WithBinaryPath overrides the binary used for auto-start. Primarily for
// testing — supply a test helper binary that starts a real daemon on the
// expected socket path.
func WithBinaryPath(path string) ClientOption {
	return func(c *Client) { c.binaryPath = path }
}

// WithSocketPath overrides the unix socket path. Primarily for testing.
func WithSocketPath(path string) ClientOption {
	return func(c *Client) {
		c.socketPath = path
		c.httpClient = buildHTTPClient(path)
	}
}

// NewClient constructs a Client dialing the socket at config.CcmcSocketPath().
// Use functional options to override behaviour.
func NewClient(opts ...ClientOption) *Client {
	socketPath := config.CcmcSocketPath()
	c := &Client{
		socketPath: socketPath,
		timeout:    defaultCallTimeout,
		httpClient: buildHTTPClient(socketPath),
		binaryPath: binaryPath(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// buildHTTPClient returns an *http.Client whose transport dials the given unix
// socket path. The HTTP request URL host field is ignored by the transport —
// callers use "http://ccmc-daemon/<path>" as a placeholder that is never
// resolved via DNS.
func buildHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: defaultCallTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		// Disable connection pooling reuse across calls — each CLI invocation
		// is short-lived, and keeping idle connections to the daemon open buys
		// nothing while making test teardown harder.
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: transport}
}

// binaryPath returns os.Args[0] when available, or an empty string. An empty
// string means auto-start cannot be attempted, and a clear error is surfaced.
func binaryPath() string {
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}

// Ping issues GET /status and discards the body. Returns nil on 200 OK,
// ErrDaemonUnavailable when the socket is unreachable.
func (c *Client) Ping() error {
	_, err := c.Status()
	return err
}

// Status returns the daemon's health and registry summary. Returns
// ErrDaemonUnavailable when the socket is unreachable.
//
// When WithAutoStart(true) is configured and the socket is missing, Status
// attempts to fork the daemon, waits for the socket to appear, then retries
// once before returning ErrDaemonUnavailable.
func (c *Client) Status() (DaemonStatus, error) {
	return withAutoStartRetry(c, func() (DaemonStatus, error) {
		return c.doStatus()
	})
}

func (c *Client) doStatus() (DaemonStatus, error) {
	var ds DaemonStatus
	if err := c.get("/status", &ds); err != nil {
		return DaemonStatus{}, err
	}
	return ds, nil
}

// ListSessions returns all sessions the daemon currently knows about.
// Returns ErrDaemonUnavailable when the socket is unreachable.
func (c *Client) ListSessions() ([]Session, error) {
	return withAutoStartRetry(c, func() ([]Session, error) {
		return c.doListSessions()
	})
}

func (c *Client) doListSessions() ([]Session, error) {
	var sessions []Session
	if err := c.get("/sessions", &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// GetSession returns a single session by ID. Returns ErrDaemonUnavailable
// when the socket is unreachable. Returns a wrapped error containing "not found"
// when the server returns 404.
func (c *Client) GetSession(id string) (Session, error) {
	return withAutoStartRetry(c, func() (Session, error) {
		return c.doGetSession(id)
	})
}

func (c *Client) doGetSession(id string) (Session, error) {
	var sess Session
	if err := c.get("/sessions/"+id, &sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// get performs a GET request to the given path, decodes JSON into dst, and
// returns ErrDaemonUnavailable for socket-level failures or a descriptive
// wrapped error for non-200 responses.
func (c *Client) get(path string, dst any) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ccmc-daemon"+path, nil)
	if err != nil {
		// NewRequest only fails on malformed method/URL — should never happen in practice.
		return fmt.Errorf("api: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Any transport-level error (ENOENT, ECONNREFUSED, timeout while dialing)
		// is surfaced as ErrDaemonUnavailable so callers can fall back gracefully.
		if isSocketError(err) || errors.Is(err, context.DeadlineExceeded) {
			return ErrDaemonUnavailable
		}
		return fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("api: %s not found", path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("api: unexpected status %d from %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("api: decode response from %s: %w", path, err)
	}
	return nil
}

// isSocketError reports whether err is a network error caused by a missing or
// refusing unix socket (ENOENT, ECONNREFUSED). These are the two conditions
// that indicate the daemon is not running.
func isSocketError(err error) bool {
	var netErr *net.OpError
	if !errors.As(err, &netErr) {
		return false
	}
	var syscallErr *os.SyscallError
	if errors.As(netErr, &syscallErr) {
		errno, ok := syscallErr.Err.(syscall.Errno)
		if !ok {
			return false
		}
		return errno == syscall.ENOENT || errno == syscall.ECONNREFUSED
	}
	return false
}

// withAutoStartRetry runs fn. If fn returns ErrDaemonUnavailable and the
// client has autoStart enabled, it calls StartDaemon (with the configured
// binary), waits for the socket to appear, then runs fn once more.
//
// Using a generic helper keeps the per-method code free of the retry logic
// while still providing typed return values.
func withAutoStartRetry[T any](c *Client, fn func() (T, error)) (T, error) {
	result, err := fn()
	if err == nil || !errors.Is(err, ErrDaemonUnavailable) || !c.autoStart {
		return result, err
	}

	// Daemon is unavailable and auto-start is requested. Fork a detached
	// daemon, then wait up to 2 seconds for the socket to appear before retrying.
	if startErr := StartDaemonWithBinary(c.binaryPath, c.socketPath); startErr != nil {
		var zero T
		return zero, fmt.Errorf("%w: auto-start failed: %v", ErrDaemonUnavailable, startErr)
	}

	if !waitForSocket(c.socketPath, 2*time.Second) {
		var zero T
		return zero, fmt.Errorf("%w: socket did not appear after auto-start", ErrDaemonUnavailable)
	}

	// One retry — if this also fails, surface as-is (not another auto-start loop).
	return fn()
}

// StartDaemon forks a detached daemon process using the current binary
// (os.Args[0]) and the daemonAutoStartSubcommand subcommand, redirecting
// stdout/stderr to ~/.ccmc/daemon.log.
//
// This is the intended entry point for external callers (e.g. the future `ccmc
// daemon start` CLI command in task 25). The socket path used is
// config.CcmcSocketPath().
//
// The actual daemon loop is wired in task 25. Until that subcommand exists in
// the binary, this call will succeed at fork but the child will exit
// immediately, causing waitForSocket to time out and returning ErrDaemonUnavailable.
func StartDaemon() error {
	return StartDaemonWithBinary(binaryPath(), config.CcmcSocketPath())
}

// StartDaemonWithBinary is the low-level fork. It is separated from StartDaemon
// so the Client can supply a test-binary path and an explicit socket path during
// testing. Production callers should use StartDaemon.
//
// Coupling note: the child is invoked as:
//
//	<binaryPath> daemonAutoStartSubcommand
//
// Task 25 must register that subcommand in cmd/ccmc/main.go. The constant
// daemonAutoStartSubcommand documents the exact string expected.
func StartDaemonWithBinary(binaryPath, socketPath string) error {
	if binaryPath == "" {
		return fmt.Errorf("auto-start: cannot determine binary path (os.Args[0] is empty)")
	}

	logPath, err := daemonLogPath()
	if err != nil {
		return fmt.Errorf("auto-start: resolve log path: %w", err)
	}
	if err := os.MkdirAll(config.CcmcDir(), 0o700); err != nil {
		return fmt.Errorf("auto-start: create ccmc dir: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("auto-start: open daemon log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(binaryPath, daemonAutoStartSubcommand) //nolint:gosec
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	// Detach from the current process group and terminal so the daemon is not a
	// child of the CLI invocation. On macOS/Linux, Setsid creates a new session;
	// the forked process becomes the session leader and is no longer bound to
	// the parent's controlling terminal or process group. This is the standard
	// Unix daemon pattern.
	//
	// Windows is not currently supported — CCMC is a macOS tool.
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("auto-start: fork daemon: %w", err)
	}

	// Release the child immediately — we do not wait for it. The socket-poll
	// loop (waitForSocket) determines when the daemon is ready.
	if err := cmd.Process.Release(); err != nil {
		// Non-fatal: the process is running; we just can't track its exit.
		// Log to stderr so it's visible in the parent's terminal (pre-daemon-start,
		// so not yet redirected to daemon.log).
		_, _ = fmt.Fprintf(os.Stderr, "ccmc: warning: release daemon process: %v\n", err)
	}

	return nil
}

// waitForSocket polls os.Stat on socketPath every 50 ms until the file appears
// or the deadline elapses. Returns true when the socket is present.
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

// daemonLogPath returns ~/.ccmc/daemon.log, creating the directory if needed.
func daemonLogPath() (string, error) {
	return config.CcmcDir() + "/daemon.log", nil
}
