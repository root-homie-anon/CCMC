package hooks

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies which Claude Code lifecycle hook fired.
// Values match the hook names used in settings.json and by the CC runtime.
type EventType string

const (
	EventSessionStart  EventType = "SessionStart"
	EventSessionEnd    EventType = "SessionEnd"
	EventPostToolUse   EventType = "PostToolUse"
	EventSubagentStart EventType = "SubagentStart"
	EventSubagentStop  EventType = "SubagentStop"
	EventStop          EventType = "Stop"
	EventNotification  EventType = "Notification"
)

// HookEvent is the interface implemented by every concrete event struct.
// EventType returns the discriminator value that identifies the event kind.
type HookEvent interface {
	EventType() EventType
}

// UnknownEventTypeError is returned by DecodeEvent when the discriminator
// field does not match any known EventType constant.
type UnknownEventTypeError struct {
	Type string
}

func (e *UnknownEventTypeError) Error() string {
	return fmt.Sprintf("hooks: unknown event type %q", e.Type)
}

// discriminator is the minimal struct used to peek at the "type" field
// before full unmarshalling into the concrete event struct.
type discriminator struct {
	Type string `json:"type"`
}

// DecodeEvent reads the "type" discriminator field from raw and unmarshals
// the payload into the matching concrete HookEvent implementation.
// Returns *UnknownEventTypeError for unrecognised event types.
func DecodeEvent(raw json.RawMessage) (HookEvent, error) {
	var d discriminator
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("hooks: failed to read event discriminator: %w", err)
	}

	switch EventType(d.Type) {
	case EventSessionStart:
		var ev SessionStartEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode SessionStart: %w", err)
		}
		return &ev, nil

	case EventSessionEnd:
		var ev SessionEndEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode SessionEnd: %w", err)
		}
		return &ev, nil

	case EventPostToolUse:
		var ev PostToolUseEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode PostToolUse: %w", err)
		}
		return &ev, nil

	case EventSubagentStart:
		var ev SubagentStartEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode SubagentStart: %w", err)
		}
		return &ev, nil

	case EventSubagentStop:
		var ev SubagentStopEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode SubagentStop: %w", err)
		}
		return &ev, nil

	case EventStop:
		var ev StopEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode Stop: %w", err)
		}
		return &ev, nil

	case EventNotification:
		var ev NotificationEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("hooks: failed to decode Notification: %w", err)
		}
		return &ev, nil

	default:
		return nil, &UnknownEventTypeError{Type: d.Type}
	}
}

// SessionStartEvent fires once per session immediately after the CC process
// starts and the session ID is assigned, before the first user prompt.
//
// JSON schema:
//
//	{ "type": "SessionStart", "session_id": "string", "project_path": "string", "timestamp": "ISO 8601" }
type SessionStartEvent struct {
	Type        EventType `json:"type"`
	SessionID   string    `json:"session_id"`
	ProjectPath string    `json:"project_path"`
	Timestamp   time.Time `json:"timestamp"`
}

func (e *SessionStartEvent) EventType() EventType { return EventSessionStart }

// SessionEndEvent fires when the CC session terminates cleanly.
//
// JSON schema:
//
//	{ "type": "SessionEnd", "session_id": "string", "project_path": "string",
//	  "timestamp": "ISO 8601", "duration_seconds": number }
type SessionEndEvent struct {
	Type            EventType `json:"type"`
	SessionID       string    `json:"session_id"`
	ProjectPath     string    `json:"project_path"`
	Timestamp       time.Time `json:"timestamp"`
	DurationSeconds float64   `json:"duration_seconds"`
}

func (e *SessionEndEvent) EventType() EventType { return EventSessionEnd }

// PostToolUseEvent fires after a tool call completes successfully, before
// Claude processes the result.
//
// JSON schema:
//
//	{ "type": "PostToolUse", "session_id": "string", "project_path": "string",
//	  "tool_name": "string", "tool_input": object, "tool_output": object|string,
//	  "timestamp": "ISO 8601" }
type PostToolUseEvent struct {
	Type        EventType       `json:"type"`
	SessionID   string          `json:"session_id"`
	ProjectPath string          `json:"project_path"`
	ToolName    string          `json:"tool_name"`
	ToolInput   json.RawMessage `json:"tool_input"`
	ToolOutput  json.RawMessage `json:"tool_output"`
	Timestamp   time.Time       `json:"timestamp"`
}

func (e *PostToolUseEvent) EventType() EventType { return EventPostToolUse }

// SubagentStartEvent fires when the parent Claude session spawns a subagent,
// immediately before the subagent begins executing.
//
// JSON schema:
//
//	{ "type": "SubagentStart", "session_id": "string", "project_path": "string",
//	  "agent_id": "string", "agent_name": "string", "task_description": "string",
//	  "timestamp": "ISO 8601" }
type SubagentStartEvent struct {
	Type            EventType `json:"type"`
	SessionID       string    `json:"session_id"`
	ProjectPath     string    `json:"project_path"`
	AgentID         string    `json:"agent_id"`
	AgentName       string    `json:"agent_name"`
	TaskDescription string    `json:"task_description"`
	Timestamp       time.Time `json:"timestamp"`
}

func (e *SubagentStartEvent) EventType() EventType { return EventSubagentStart }

// SubagentStopEvent fires when a subagent finishes its task — whether
// successfully or with an error — and returns to the parent session.
//
// JSON schema:
//
//	{ "type": "SubagentStop", "session_id": "string", "project_path": "string",
//	  "agent_id": "string", "agent_name": "string", "result": "string",
//	  "success": boolean, "timestamp": "ISO 8601" }
type SubagentStopEvent struct {
	Type        EventType `json:"type"`
	SessionID   string    `json:"session_id"`
	ProjectPath string    `json:"project_path"`
	AgentID     string    `json:"agent_id"`
	AgentName   string    `json:"agent_name"`
	Result      string    `json:"result"`
	Success     bool      `json:"success"`
	Timestamp   time.Time `json:"timestamp"`
}

func (e *SubagentStopEvent) EventType() EventType { return EventSubagentStop }

// StopEvent fires after each assistant response completes.
//
// JSON schema:
//
//	{ "type": "Stop", "session_id": "string", "project_path": "string",
//	  "stop_reason": "string", "response_summary": "string",
//	  "tool_calls": ["string", ...], "timestamp": "ISO 8601" }
type StopEvent struct {
	Type            EventType `json:"type"`
	SessionID       string    `json:"session_id"`
	ProjectPath     string    `json:"project_path"`
	StopReason      string    `json:"stop_reason"`
	ResponseSummary string    `json:"response_summary"`
	ToolCalls       []string  `json:"tool_calls"`
	Timestamp       time.Time `json:"timestamp"`
}

func (e *StopEvent) EventType() EventType { return EventStop }

// NotificationEvent fires when CC emits a notification — e.g. requesting user
// attention, reporting a long-running background operation, or signalling a
// status change.
//
// JSON schema:
//
//	{ "type": "Notification", "session_id": "string", "project_path": "string",
//	  "notification_type": "string", "message": "string", "timestamp": "ISO 8601" }
type NotificationEvent struct {
	Type             EventType `json:"type"`
	SessionID        string    `json:"session_id"`
	ProjectPath      string    `json:"project_path"`
	NotificationType string    `json:"notification_type"`
	Message          string    `json:"message"`
	Timestamp        time.Time `json:"timestamp"`
}

func (e *NotificationEvent) EventType() EventType { return EventNotification }
