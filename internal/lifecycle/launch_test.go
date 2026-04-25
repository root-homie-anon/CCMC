package lifecycle

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccmc/pkg/ccmc"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newLaunchTestClient builds a client dialing a stub HTTP server that returns
// sessions from the provided slice. The slice is returned verbatim from
// GET /sessions.
func newLaunchTestClient(t *testing.T, sessions func() []ccmc.Session) *ccmc.Client {
	t.Helper()
	tmp, err := os.MkdirTemp("", "ccmclaunch")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	sockPath := filepath.Join(tmp, "launch.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		sess := sessions()
		w.Header().Set("Content-Type", "application/json")
		// Build minimal JSON array.
		if len(sess) == 0 {
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		// Use simple manual JSON so we avoid importing encoding/json in the test helper.
		var parts []string
		for _, s := range sess {
			parts = append(parts, fmt.Sprintf(
				`{"id":%q,"projectPath":%q,"status":"active","lastActivity":"0001-01-01T00:00:00Z","startedAt":"0001-01-01T00:00:00Z"}`,
				s.ID, s.ProjectPath,
			))
		}
		body := "[" + strings.Join(parts, ",") + "]"
		w.Write([]byte(body)) //nolint:errcheck
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"running":true,"pid":1}`)) //nolint:errcheck
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { server.Close() })

	return ccmc.NewClient(ccmc.WithSocketPath(sockPath), ccmc.WithTimeout(3*time.Second))
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestLaunch_HappyPath verifies that when osascript succeeds and the daemon
// registers a matching session, Launch returns the session ID.
func TestLaunch_HappyPath(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)

	// Capture osascript args.
	var gotScript string
	origOsascript := osascriptCmd
	t.Cleanup(func() { osascriptCmd = origOsascript })
	osascriptCmd = func(script string) error {
		gotScript = script
		return nil
	}

	client := newLaunchTestClient(t, func() []ccmc.Session {
		return []ccmc.Session{{ID: "new-sess-1", ProjectPath: resolved}}
	})

	id, err := Launch(client, dir)
	if err != nil {
		t.Fatalf("Launch: unexpected error: %v", err)
	}
	if id != "new-sess-1" {
		t.Errorf("expected session id 'new-sess-1', got %q", id)
	}

	// The directory must appear in the osascript, shell-quoted.
	quotedDir := singleQuoteShell(resolved)
	if !strings.Contains(gotScript, quotedDir) {
		t.Errorf("expected quoted dir %q in osascript, got:\n%s", quotedDir, gotScript)
	}
}

// TestLaunch_FallbackPath verifies that when osascript fails, the claudeSubprocessCmd
// seam is invoked with the correct directory.
func TestLaunch_FallbackPath(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)

	// Make osascript fail.
	origOsascript := osascriptCmd
	t.Cleanup(func() { osascriptCmd = origOsascript })
	osascriptCmd = func(_ string) error {
		return fmt.Errorf("iTerm not running")
	}

	// Capture fallback subprocess args.
	var gotDir string
	origClaude := claudeSubprocessCmd
	t.Cleanup(func() { claudeSubprocessCmd = origClaude })
	claudeSubprocessCmd = func(d string) error {
		gotDir = d
		return nil
	}

	client := newLaunchTestClient(t, func() []ccmc.Session {
		return []ccmc.Session{{ID: "fallback-sess", ProjectPath: resolved}}
	})

	id, err := Launch(client, dir)
	if err != nil {
		t.Fatalf("Launch fallback: unexpected error: %v", err)
	}
	if id != "fallback-sess" {
		t.Errorf("expected 'fallback-sess', got %q", id)
	}
	if gotDir != resolved {
		t.Errorf("fallback subprocess cwd: expected %q, got %q", resolved, gotDir)
	}
}

// TestLaunch_DaemonPollTimeout verifies that when the daemon never registers a
// matching session, Launch returns the timeout error message.
func TestLaunch_DaemonPollTimeout(t *testing.T) {
	dir := t.TempDir()

	// osascript succeeds immediately.
	origOsascript := osascriptCmd
	t.Cleanup(func() { osascriptCmd = origOsascript })
	osascriptCmd = func(_ string) error { return nil }

	// Daemon always returns empty sessions.
	client := newLaunchTestClient(t, func() []ccmc.Session { return nil })

	// Use a very short poll window to keep the test fast. We swap the timeout
	// by temporarily replacing pollForSession — but since it's an internal function,
	// we instead shorten the timeout at the Launch level by overriding the seam.
	// The simplest approach: we cannot inject the timeout, so we rely on the
	// 3-second default. To keep the test fast we instead call pollForSession
	// directly with a 300ms timeout.
	_, err := pollForSession(client, dir, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not register") {
		t.Errorf("expected 'did not register' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "check daemon hooks") {
		t.Errorf("expected 'check daemon hooks' in error, got: %v", err)
	}
}

// TestLaunch_PathTraversal verifies that a directory path containing shell
// metacharacters is single-quoted correctly so that the characters are not
// interpreted by a shell inside iTerm.
func TestLaunch_PathTraversal(t *testing.T) {
	// We cannot create a directory whose name contains $(), but we can verify the
	// quoting strategy itself with a crafted path string.
	maliciousPath := "/tmp/foo$(rm -rf /).dir"

	quoted := singleQuoteShell(maliciousPath)

	// The result must start and end with single quotes.
	if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
		t.Errorf("quoted path must be wrapped in single quotes, got: %q", quoted)
	}

	// $() must not appear outside of single quotes in the result.
	// Within single quotes, shell metacharacters are inert.
	// We verify: the malicious portion is inside the single-quoted section.
	inner := quoted[1 : len(quoted)-1] // strip outer quotes
	if !strings.Contains(inner, "$(rm -rf /)") {
		t.Errorf("expected malicious string inside single quotes, got inner: %q", inner)
	}

	// The opening single quote must not be closed before the metachar appears.
	// A correct quoting: '/tmp/foo$(rm -rf /).dir'
	// An incorrect one would be: '/tmp/foo'$(rm -rf /)'.dir' — which would
	// execute the metachar. Check that the only single quotes are the wrappers.
	// (For paths without embedded single quotes, inner has no quotes at all.)
	if strings.Contains(inner, "'") {
		// If the path itself had single quotes they would be escaped as '\''
		// which produces ' + backslash + '' + ' sequences. Verify that the
		// result is the standard POSIX idiom and not a raw unescaped quote.
		if !strings.Contains(quoted, `'\''`) {
			t.Errorf("embedded single quote not escaped via POSIX idiom in: %q", quoted)
		}
	}

	// Additional: a path with an embedded single quote must still be safe.
	pathWithQuote := "/tmp/it's here"
	quotedWithQuote := singleQuoteShell(pathWithQuote)
	// Expected: '/tmp/it'\''s here'
	expected := `'/tmp/it'\''s here'`
	if quotedWithQuote != expected {
		t.Errorf("single-quote escaping: expected %q, got %q", expected, quotedWithQuote)
	}
}

// TestValidateDir verifies that ValidateDir accepts existing directories and
// rejects missing paths and files.
func TestValidateDir(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "notadir.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name    string
		dir     string
		wantErr bool
	}{
		{"existing dir", tmp, false},
		{"missing path", filepath.Join(tmp, "nosuchdir"), true},
		{"regular file", f, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDir(tc.dir)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
