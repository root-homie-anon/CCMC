package inspector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// EventType identifies the kind of JSONL event in a session transcript.
type EventType string

const (
	EventUser      EventType = "user"
	EventAssistant EventType = "assistant"
	EventToolUse   EventType = "tool_use"
	EventToolResult EventType = "tool_result"
	EventUnknown   EventType = "unknown"
)

// Event is a parsed line from a CC session JSONL transcript.
type Event struct {
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Raw       map[string]any `json:"-"` // Full parsed JSON for downstream consumers
}

// ParseCallback is called for each successfully parsed event.
// Return false to stop iteration early.
type ParseCallback func(Event) bool

// ParseJSONL reads a JSONL file line-by-line and calls cb for each parsed event.
// Memory usage is O(1) regardless of file size — each line is parsed and discarded.
// Malformed lines are silently skipped (logged to stderr if verbose is true).
func ParseJSONL(path string, verbose bool, cb ParseCallback) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return ParseJSONLReader(f, verbose, cb)
}

// ParseJSONLReader reads JSONL from an io.Reader line-by-line.
func ParseJSONLReader(r io.Reader, verbose bool, cb ParseCallback) error {
	scanner := bufio.NewScanner(r)

	// Increase buffer for potentially long lines (tool results can be large)
	const maxLineSize = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "jsonl: skipping malformed line: %v\n", err)
			}
			continue
		}

		evt := Event{
			Type: classifyEvent(raw),
			Raw:  raw,
		}

		// Parse timestamp if present
		if ts, ok := raw["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				evt.Timestamp = t
			}
		}

		if !cb(evt) {
			return nil // Early stop requested
		}
	}

	return scanner.Err()
}

// classifyEvent determines the EventType from a raw JSON object.
func classifyEvent(raw map[string]any) EventType {
	// Check for "type" field first (common in CC JSONL)
	if t, ok := raw["type"].(string); ok {
		switch t {
		case "human", "user":
			return EventUser
		case "assistant":
			return EventAssistant
		case "tool_use":
			return EventToolUse
		case "tool_result":
			return EventToolResult
		}
	}

	// Heuristic fallback: check for role field
	if role, ok := raw["role"].(string); ok {
		switch role {
		case "human", "user":
			return EventUser
		case "assistant":
			return EventAssistant
		}
	}

	// Check for tool_use content blocks
	if content, ok := raw["content"].([]any); ok {
		for _, block := range content {
			if m, ok := block.(map[string]any); ok {
				if m["type"] == "tool_use" {
					return EventToolUse
				}
				if m["type"] == "tool_result" {
					return EventToolResult
				}
			}
		}
	}

	return EventUnknown
}

// CollectEvents is a convenience that parses all events from a JSONL file
// into a slice. Only suitable for small files or testing — for large files,
// use ParseJSONL with a callback.
func CollectEvents(path string) ([]Event, error) {
	var events []Event
	err := ParseJSONL(path, false, func(e Event) bool {
		events = append(events, e)
		return true
	})
	return events, err
}
