package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ccmc/pkg/ccmc"
)

// osascriptCmd is the seam used to invoke osascript. Tests replace this with a
// stub so the iTerm AppleScript path can be exercised without launching iTerm.
var osascriptCmd = func(script string) error {
	return exec.Command("osascript", "-e", script).Run()
}

// claudeSubprocessCmd is the seam used to spawn a claude subprocess when the
// iTerm path is unavailable. Tests replace this to capture the exec arguments.
var claudeSubprocessCmd = func(dir string) error {
	cmd := exec.Command("claude") //nolint:gosec
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// Launch opens a new iTerm tab running "claude" in the given directory, then
// polls the daemon for up to 3 seconds for a session whose ProjectPath matches
// the resolved directory. Returns the new session ID once registered.
//
// iTerm path: osascript drives iTerm2's AppleScript interface to create a new
// window and write the "cd <dir> && claude" command. The directory is passed
// via fmt.Sprintf with single-quoting of the shell-expanded path — the string
// is embedded into the AppleScript literal, so single-quoting prevents shell
// metacharacters inside the path from being interpreted by the shell inside iTerm.
// Double-quoting is not used because the AppleScript string itself uses double
// quotes as its delimiter, which would require escaping.
//
// Fallback: if osascript returns a non-zero exit code (iTerm not running, or
// osascript not available), we spawn "claude" as a subprocess of the current
// terminal with cwd = dir and inherited stdio. This is simpler than opening
// Terminal.app (no extra AppleScript, no dependency on Terminal), works in CI,
// and is the minimum viable path to get a claude session started.
//
// The "found by ProjectPath" match uses filepath.Abs + filepath.EvalSymlinks on
// the input so that symlinked directories compare correctly with what the daemon
// registered from the hook event's project_path field.
func Launch(client *ccmc.Client, dir string) (string, error) {
	resolved, err := resolveDir(dir)
	if err != nil {
		return "", fmt.Errorf("launch: resolve dir %q: %w", dir, err)
	}

	if err := launchSession(resolved); err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}

	id, err := pollForSession(client, resolved, 3*time.Second)
	if err != nil {
		return "", err
	}
	return id, nil
}

// resolveDir returns the absolute, symlink-resolved form of dir.
// EvalSymlinks requires the path to exist — if the path doesn't exist it was
// already rejected by the caller's directory validation (ValidateDir).
func resolveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// launchSession attempts the iTerm AppleScript path first, then falls back to
// spawning a subprocess.
func launchSession(dir string) error {
	// Shell-quote the directory path by wrapping it in single quotes and escaping
	// any literal single quotes in the path (replace ' with '\''). This is the
	// standard POSIX sh quoting strategy. The resulting string is embedded into
	// the AppleScript `write text` argument, which passes it to a shell inside
	// iTerm. Using %q (Go's double-quoted escaping) is not appropriate here
	// because the AppleScript string delimiter is double-quotes — embedding
	// double-quoted content inside double-quotes would require additional escaping
	// of the AppleScript layer.
	quotedDir := singleQuoteShell(dir)
	script := fmt.Sprintf(
		`tell application "iTerm"
  create window with default profile
  tell current session of current window
    write text "cd %s && claude"
  end tell
end tell`,
		quotedDir,
	)

	if err := osascriptCmd(script); err != nil {
		// osascript failed — fall back to spawning a subprocess in the current terminal.
		// Fallback rationale: spawning in the current terminal is simpler than opening
		// Terminal.app (no additional AppleScript, no assumption about which terminal
		// emulator the user has), works without a GUI (CI environments), and doesn't
		// require osascript at all. The tradeoff is that the session shares the current
		// terminal rather than opening a new window.
		return claudeSubprocessCmd(dir)
	}
	return nil
}

// singleQuoteShell wraps s in single quotes for POSIX sh, escaping any literal
// single quotes within s using the '\'' idiom.
func singleQuoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pollForSession polls client.ListSessions every 100ms for up to timeout,
// looking for a session whose ProjectPath matches resolvedDir. Returns the
// session ID when found, or an error if the poll times out.
func pollForSession(client *ccmc.Client, resolvedDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sessions, err := client.ListSessions()
		if err == nil {
			for _, s := range sessions {
				// Canonicalize the session's project path before comparing — the hook
				// event writes the path as received from the CC runtime, which should
				// already be absolute but may not have resolved symlinks.
				sessPath, err := canonicalizePath(s.ProjectPath)
				if err != nil {
					sessPath = s.ProjectPath
				}
				if sessPath == resolvedDir {
					return s.ID, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("launch: session may have started but did not register within 3s; check daemon hooks (dir=%s)", resolvedDir)
}

// ValidateDir reports whether dir is an existing directory. Used by CLI callers
// to fail fast before calling Launch.
func ValidateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%q: no such directory", dir)
		}
		return fmt.Errorf("stat %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%q: not a directory", dir)
	}
	return nil
}
