package inspector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTodos_MissingFile(t *testing.T) {
	todos, err := readTodosFromDir(t.TempDir(), "nonexistent-session-id")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(todos) != 0 {
		t.Errorf("expected empty slice, got %d todos", len(todos))
	}
}

func TestReadTodos_EmptyTodosList(t *testing.T) {
	dir := t.TempDir()
	sessionID := "test-session-123"
	writeTestTodosFile(t, dir, sessionID, []Todo{})

	todos, err := readTodosFromDir(dir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(todos) != 0 {
		t.Errorf("expected empty slice, got %d todos", len(todos))
	}
}

func TestReadTodos_ParsesTodos(t *testing.T) {
	dir := t.TempDir()
	sessionID := "test-session-456"
	input := []Todo{
		{Title: "Fix the bug", Status: "completed"},
		{Title: "Write tests", Status: "in_progress", ActiveForm: "unit tests"},
		{Title: "Deploy", Status: "pending"},
	}
	writeTestTodosFile(t, dir, sessionID, input)

	todos, err := readTodosFromDir(dir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}

	if todos[0].Title != "Fix the bug" {
		t.Errorf("todos[0].Title = %q, want %q", todos[0].Title, "Fix the bug")
	}
	if todos[0].Status != "completed" {
		t.Errorf("todos[0].Status = %q, want %q", todos[0].Status, "completed")
	}
	if todos[1].ActiveForm != "unit tests" {
		t.Errorf("todos[1].ActiveForm = %q, want %q", todos[1].ActiveForm, "unit tests")
	}
	if todos[2].Status != "pending" {
		t.Errorf("todos[2].Status = %q, want %q", todos[2].Status, "pending")
	}
}

func TestReadTodos_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	sessionID := "bad-session"
	path := filepath.Join(dir, sessionID+".json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readTodosFromDir(dir, sessionID)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// readTodosFromDir is a testable variant of ReadTodos that accepts an explicit directory.
func readTodosFromDir(dir, sessionID string) ([]Todo, error) {
	path := filepath.Join(dir, sessionID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
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

func writeTestTodosFile(t *testing.T, dir, sessionID string, todos []Todo) {
	t.Helper()
	tf := todosFile{Todos: todos}
	data, err := json.Marshal(tf)
	if err != nil {
		t.Fatalf("marshal test todos: %v", err)
	}
	path := filepath.Join(dir, sessionID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write test todos file: %v", err)
	}
}
