package daemon

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ccmc/internal/config"
	"ccmc/pkg/ccmc"
)

// ScanSessions discovers CC sessions from the filesystem by scanning
// ~/.claude/projects/<encoded-cwd>/ for *.jsonl files. It reads only
// the last line of each file to extract the most recent activity timestamp.
// Memory usage is O(1) per file regardless of file size.
func ScanSessions() ([]ccmc.Session, error) {
	projectsDir := config.ClaudeProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []ccmc.Session

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		dirPath := filepath.Join(projectsDir, dirName)
		projectPath := decodeProjectDir(dirName)
		projectName := filepath.Base(projectPath)

		jsonlFiles, err := filepath.Glob(filepath.Join(dirPath, "*.jsonl"))
		if err != nil {
			continue
		}

		for _, jf := range jsonlFiles {
			sessionID := strings.TrimSuffix(filepath.Base(jf), ".jsonl")
			info, err := os.Stat(jf)
			if err != nil {
				continue
			}

			lastActivity := info.ModTime()

			// Try to get a more accurate timestamp from the last JSONL line
			if ts, err := lastLineTimestamp(jf); err == nil && !ts.IsZero() {
				lastActivity = ts
			}

			sessions = append(sessions, ccmc.Session{
				ID:              sessionID,
				ProjectPath:     projectPath,
				ProjectName:     projectName,
				Status:          ccmc.SessionIdle, // Filesystem-only: can't determine active vs idle
				LastActivity:    lastActivity,
				ContextEstimate: info.Size(),
			})
		}
	}

	return sessions, nil
}

// lastLineTimestamp reads the last non-empty line of a file using a
// reverse-read approach. It never loads the full file into memory.
func lastLineTimestamp(path string) (time.Time, error) {
	line, err := readLastLine(path)
	if err != nil {
		return time.Time{}, err
	}

	var entry struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return time.Time{}, err
	}
	if entry.Timestamp == "" {
		return time.Time{}, nil
	}

	return time.Parse(time.RFC3339Nano, entry.Timestamp)
}

// readLastLine reads the last non-empty line of a file by seeking from the end.
// Memory usage is bounded by the line length, not the file size.
func readLastLine(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, io.EOF
	}

	// Read backwards in chunks to find the last newline
	const chunkSize = 4096
	buf := make([]byte, 0, chunkSize)
	offset := size

	for offset > 0 {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}

		buf = append(chunk, buf...)

		// Look for a complete last line (skip trailing newlines)
		trimmed := strings.TrimRight(string(buf), "\n\r")
		if idx := strings.LastIndex(trimmed, "\n"); idx >= 0 {
			return []byte(trimmed[idx+1:]), nil
		}
	}

	// Entire file is one line (or we read it all)
	return []byte(strings.TrimRight(string(buf), "\n\r")), nil
}

// decodeProjectDir converts a Claude Code encoded directory name back to a path.
// CC encodes project paths by replacing "/" with "-". This is lossy — project
// names containing dashes are indistinguishable from path separators. The
// decoded path is best-effort for display purposes.
func decodeProjectDir(encoded string) string {
	if len(encoded) == 0 {
		return encoded
	}
	// CC encoding: /Users/foo/bar → -Users-foo-bar
	// Reverse: replace all "-" with "/"
	return strings.ReplaceAll(encoded, "-", "/")
}

// FindSessionJSONL searches all project directories under ~/.claude/projects/
// for a JSONL file matching the given sessionID. Returns the absolute path to
// the JSONL file and the decoded project path, or ("", "", nil) when not found.
// Returns ("", "", err) only for unexpected I/O errors.
func FindSessionJSONL(sessionID string) (jsonlPath, projectPath string, err error) {
	projectsDir := config.ClaudeProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}

	target := sessionID + ".jsonl"

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, entry.Name(), target)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, decodeProjectDir(entry.Name()), nil
		}
	}

	return "", "", nil
}
