package inspector

import (
	"errors"
	"os"
	"path/filepath"

	"ccmc/internal/config"
)

// ReadMemorySummary searches projectsDir for a session-memory summary file
// belonging to the given sessionID and returns its contents.
//
// Claude Code stores session memory at one of two paths depending on how it
// was written:
//
//	~/.claude/projects/<encoded-cwd>/<session-id>/session-memory/summary.md
//	~/.claude/projects/<hash>/<session-id>/session-memory/summary.md
//
// Both layouts share the same directory structure — only the parent directory
// name differs (encoded path vs opaque hash). This function is layout-agnostic:
// it scans all subdirectories of projectsDir and returns the contents of the
// first summary.md it finds under <subdir>/<sessionID>/session-memory/.
//
// Returns ("", nil) when no summary file is present for the session.
// Returns ("", err) only for unexpected I/O errors (e.g. permission denied).
func ReadMemorySummary(projectsDir, sessionID string) (string, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		candidate := filepath.Join(
			projectsDir,
			entry.Name(),
			sessionID,
			"session-memory",
			"summary.md",
		)

		data, err := os.ReadFile(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}

		return string(data), nil
	}

	return "", nil
}

// ReadMemorySummaryForSession is the production entrypoint. It resolves
// ~/.claude/projects/ via config.ClaudeProjectsDir() and delegates to
// ReadMemorySummary. Returns ("", nil) when no summary exists.
func ReadMemorySummaryForSession(sessionID string) (string, error) {
	return ReadMemorySummary(config.ClaudeProjectsDir(), sessionID)
}
