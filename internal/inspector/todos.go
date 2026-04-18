package inspector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"ccmc/internal/config"
)

// Todo represents a single task item from a CC session's todo list.
// Fields mirror the JSON shape written by Claude Code to ~/.claude/todos/<session-id>.json.
type Todo struct {
	// Title is the human-readable task description.
	Title string `json:"content"`

	// Status is one of: "pending", "in_progress", "completed".
	Status string `json:"status"`

	// ActiveForm is an optional free-form hint about the active state (e.g. sub-step label).
	// Present only in some CC versions; omitted when empty.
	ActiveForm string `json:"activeForm,omitempty"`
}

// todosFile is the raw JSON structure stored at ~/.claude/todos/<session-id>.json.
// CC writes a top-level object with a "todos" array.
type todosFile struct {
	Todos []Todo `json:"todos"`
}

// ReadTodos reads the todo list for the given sessionID from
// ~/.claude/todos/<session-id>.json. Returns an empty slice (not nil) and nil
// error when the file does not exist — same convention as ReadMemorySummary.
// Returns nil, err only for unexpected I/O or parse errors.
func ReadTodos(sessionID string) ([]Todo, error) {
	todosDir := config.ClaudeTodosDir()
	path := filepath.Join(todosDir, sessionID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Todo{}, nil
		}
		return nil, err
	}

	var tf todosFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, err
	}

	if tf.Todos == nil {
		return []Todo{}, nil
	}
	return tf.Todos, nil
}
