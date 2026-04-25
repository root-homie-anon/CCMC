package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ccmc/pkg/ccmc"
)

// ErrSessionNotFound is returned when the registry has no session with the given ID.
var ErrSessionNotFound = errors.New("session not found")

// ErrPermissionDenied is returned when SIGTERM is refused because the process
// is not owned by the current user (EPERM).
var ErrPermissionDenied = errors.New("permission denied: process not owned by current user")

// ErrProcessTimeout is returned when the process does not exit within 5 seconds
// of receiving SIGTERM.
var ErrProcessTimeout = errors.New("process did not exit within 5 seconds")

// Kill looks up the session identified by id via the daemon API client, resolves
// the OS process ID, sends SIGTERM, and polls for exit up to 5 seconds.
//
// PID resolution strategy:
//  1. Prefer Session.PID from the registry — populated only if the daemon was
//     extended to capture it. Currently the SessionStart hook payload does not
//     include the parent PID, so Session.PID is 0 for all registry entries.
//  2. Fall back to scanning the OS process table via "ps aux". This is the only
//     portable approach on macOS (no /proc filesystem). We match on the process's
//     working directory using "ps -o pid,command" and cross-reference with the
//     session's ProjectPath. The heuristic is: find a process whose command
//     contains "claude" and whose working directory matches ProjectPath.
//     On macOS, lsof or "ps -o pid,wdir" could also work, but "ps aux" +
//     command-line matching is available without root and avoids the lsof
//     dependency. The process name check (" claude") is intentionally narrow to
//     avoid matching ccmc itself or unrelated tools.
//
// After SIGTERM, Kill polls signal(0) every 100ms for up to 5 seconds.
// Updating the registry to "dead" is the daemon's responsibility (SessionEnd hook);
// Kill does not touch the registry directly.
func Kill(client *ccmc.Client, id string) error {
	sess, err := client.GetSession(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrSessionNotFound
		}
		return fmt.Errorf("kill: get session: %w", err)
	}

	pid, err := resolveSessionPID(sess)
	if err != nil {
		return fmt.Errorf("kill: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		// os.FindProcess on Unix never returns an error — it only fills in the
		// struct. This branch is here for completeness and future portability.
		return fmt.Errorf("kill: find process %d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrPermission) || isEPERM(err) {
			return ErrPermissionDenied
		}
		if errors.Is(err, os.ErrProcessDone) || isESRCH(err) {
			// Process already gone — treat as success.
			return nil
		}
		return fmt.Errorf("kill: signal SIGTERM to pid %d: %w", pid, err)
	}

	return pollProcessGone(proc, pid, 5*time.Second)
}

// resolveSessionPID returns the OS PID for the session. It first checks the
// registry's Session.PID field (which is currently always 0 because the
// SessionStart hook payload does not include the parent PID). If PID is 0, it
// falls back to scanning the process table.
func resolveSessionPID(sess ccmc.Session) (int, error) {
	if sess.PID > 0 {
		return sess.PID, nil
	}
	// Registry has no PID. Scan the process table for a claude process whose
	// working directory matches the session's ProjectPath.
	return findClaudeProcessForProject(sess.ProjectPath)
}

// findClaudeProcessForProject scans the process table looking for a process
// whose command contains "claude" and whose working directory matches projectPath.
// Uses "ps -axo pid=,comm=,command=" on macOS (no /proc, no lsof dependency).
// The "=" suffix suppresses column headers, giving raw values.
//
// Why "ps" over lsof: ps is always available on macOS without elevated privileges,
// produces stable columnar output, and the comm/command fields are sufficient to
// identify the claude process. lsof can identify CWD but requires an additional
// subprocess and root is sometimes needed for other users' processes.
func findClaudeProcessForProject(projectPath string) (int, error) {
	// Resolve the project path to its canonical form before comparing so that
	// symlinks in the path don't cause false mismatches.
	canonicalPath, err := canonicalizePath(projectPath)
	if err != nil {
		// If we can't canonicalize, use the raw path — better than failing outright.
		canonicalPath = projectPath
	}

	// Use lsof to find the CWD for claude processes. This is the most reliable
	// approach on macOS: lsof -a -c claude -d cwd -Fn lists all processes whose
	// comm matches "claude" and prints their CWDs. No root required for same-user
	// processes.
	//
	// We also accept a ps-based fallback below if lsof isn't available.
	pid, err := findViaCWDScan(canonicalPath)
	if err == nil {
		return pid, nil
	}

	// lsof fallback or no match — try ps with command-line matching. This is
	// less precise (we match on command args rather than CWD) but covers the
	// common case where the user ran: claude (cwd = projectPath).
	return findViaPSScan(canonicalPath)
}

// findViaCWDScan uses lsof to enumerate all claude processes and match on CWD.
func findViaCWDScan(canonicalPath string) (int, error) {
	// lsof -a -c claude -d cwd -Fn
	// -a: AND the filters (comm AND cwd)
	// -c claude: comm starts with "claude"
	// -d cwd: show only cwd file-descriptors
	// -Fn: output field "n" (filename/path) in parseable format
	out, err := exec.Command("lsof", "-a", "-c", "claude", "-d", "cwd", "-Fn").Output()
	if err != nil {
		return 0, fmt.Errorf("lsof: %w", err)
	}

	// lsof -Fn output format (one entry per line):
	// pPID\n   (process record, p = pid field)
	// nPATH\n  (file record, n = name field)
	var currentPID int
	for _, rawLine := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err == nil {
				currentPID = pid
			}
		case 'n':
			cwd := line[1:]
			if cwd == canonicalPath && currentPID > 0 {
				return currentPID, nil
			}
		}
	}
	return 0, fmt.Errorf("no claude process found with CWD %q via lsof", canonicalPath)
}

// findViaPSScan falls back to "ps aux" and matches processes named "claude"
// whose command line contains the project path. Less reliable than CWD matching
// but requires no lsof.
func findViaPSScan(projectPath string) (int, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return 0, fmt.Errorf("ps aux: %w", err)
	}

	// ps aux columns: USER PID %CPU %MEM VSZ RSS TT STAT STARTED TIME COMMAND
	// We look for lines where PID > 0, COMMAND contains "claude", and the full
	// command line also contains the project path.
	projectBase := filepath.Base(projectPath)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		// COMMAND is field index 10 onwards (joined).
		command := strings.Join(fields[10:], " ")
		if !strings.Contains(command, "claude") {
			continue
		}
		if !strings.Contains(command, projectPath) && !strings.Contains(command, projectBase) {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		return pid, nil
	}
	return 0, fmt.Errorf("no claude process found for project %q via ps", projectPath)
}

// canonicalizePath resolves symlinks and returns the absolute canonical path.
func canonicalizePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// pollProcessGone polls signal(0) every 100ms until the process exits or the
// deadline elapses. Signal(0) succeeds while the process exists and fails with
// ESRCH once it is gone. Returns nil when the process exits, ErrProcessTimeout
// when the deadline elapses.
func pollProcessGone(proc *os.Process, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := proc.Signal(syscall.Signal(0))
		if err != nil {
			// Any error from signal(0) means the process is gone (ESRCH or ErrProcessDone).
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%w (pid=%d)", ErrProcessTimeout, pid)
}

func isEPERM(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EPERM
	}
	return false
}

func isESRCH(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ESRCH
	}
	return false
}
