package hooks

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDecodeEvent(t *testing.T) {
	t.Parallel()

	// canonical timestamp shared across cases
	ts := "2024-06-01T12:00:00Z"
	parsed, _ := time.Parse(time.RFC3339, ts)

	tests := []struct {
		name        string
		raw         string
		wantType    EventType
		assertEvent func(t *testing.T, ev HookEvent)
	}{
		{
			name:     "SessionStart",
			wantType: EventSessionStart,
			raw: `{
				"type": "SessionStart",
				"session_id": "sess-001",
				"project_path": "/home/user/proj",
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*SessionStartEvent)
				if !ok {
					t.Fatalf("expected *SessionStartEvent, got %T", ev)
				}
				if e.SessionID != "sess-001" {
					t.Errorf("SessionID: got %q, want %q", e.SessionID, "sess-001")
				}
				if e.ProjectPath != "/home/user/proj" {
					t.Errorf("ProjectPath: got %q, want %q", e.ProjectPath, "/home/user/proj")
				}
				if !e.Timestamp.Equal(parsed) {
					t.Errorf("Timestamp: got %v, want %v", e.Timestamp, parsed)
				}
			},
		},
		{
			name:     "SessionEnd",
			wantType: EventSessionEnd,
			raw: `{
				"type": "SessionEnd",
				"session_id": "sess-002",
				"project_path": "/home/user/proj",
				"timestamp": "2024-06-01T12:00:00Z",
				"duration_seconds": 42.5
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*SessionEndEvent)
				if !ok {
					t.Fatalf("expected *SessionEndEvent, got %T", ev)
				}
				if e.SessionID != "sess-002" {
					t.Errorf("SessionID: got %q, want %q", e.SessionID, "sess-002")
				}
				if e.DurationSeconds != 42.5 {
					t.Errorf("DurationSeconds: got %v, want 42.5", e.DurationSeconds)
				}
			},
		},
		{
			name:     "PostToolUse",
			wantType: EventPostToolUse,
			raw: `{
				"type": "PostToolUse",
				"session_id": "sess-003",
				"project_path": "/home/user/proj",
				"tool_name": "Write",
				"tool_input": {"file_path": "/tmp/foo.go"},
				"tool_output": "ok",
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*PostToolUseEvent)
				if !ok {
					t.Fatalf("expected *PostToolUseEvent, got %T", ev)
				}
				if e.ToolName != "Write" {
					t.Errorf("ToolName: got %q, want %q", e.ToolName, "Write")
				}
				if e.ToolInput == nil {
					t.Error("ToolInput must not be nil")
				}
				if e.ToolOutput == nil {
					t.Error("ToolOutput must not be nil")
				}
				// Verify ToolInput is valid JSON that can round-trip
				var inp map[string]any
				if err := json.Unmarshal(e.ToolInput, &inp); err != nil {
					t.Errorf("ToolInput is not valid JSON: %v", err)
				}
			},
		},
		{
			name:     "SubagentStart",
			wantType: EventSubagentStart,
			raw: `{
				"type": "SubagentStart",
				"session_id": "sess-004",
				"project_path": "/home/user/proj",
				"agent_id": "agt-abc",
				"agent_name": "doug",
				"task_description": "implement feature X",
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*SubagentStartEvent)
				if !ok {
					t.Fatalf("expected *SubagentStartEvent, got %T", ev)
				}
				if e.AgentID != "agt-abc" {
					t.Errorf("AgentID: got %q, want %q", e.AgentID, "agt-abc")
				}
				if e.AgentName != "doug" {
					t.Errorf("AgentName: got %q, want %q", e.AgentName, "doug")
				}
				if e.TaskDescription != "implement feature X" {
					t.Errorf("TaskDescription: got %q", e.TaskDescription)
				}
			},
		},
		{
			name:     "SubagentStop",
			wantType: EventSubagentStop,
			raw: `{
				"type": "SubagentStop",
				"session_id": "sess-005",
				"project_path": "/home/user/proj",
				"agent_id": "agt-abc",
				"agent_name": "doug",
				"result": "completed successfully",
				"success": true,
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*SubagentStopEvent)
				if !ok {
					t.Fatalf("expected *SubagentStopEvent, got %T", ev)
				}
				if e.Result != "completed successfully" {
					t.Errorf("Result: got %q", e.Result)
				}
				if !e.Success {
					t.Error("Success: got false, want true")
				}
			},
		},
		{
			name:     "Stop",
			wantType: EventStop,
			raw: `{
				"type": "Stop",
				"session_id": "sess-006",
				"project_path": "/home/user/proj",
				"stop_reason": "end_turn",
				"response_summary": "Done.",
				"tool_calls": ["Read", "Write"],
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*StopEvent)
				if !ok {
					t.Fatalf("expected *StopEvent, got %T", ev)
				}
				if e.StopReason != "end_turn" {
					t.Errorf("StopReason: got %q, want %q", e.StopReason, "end_turn")
				}
				if e.ResponseSummary != "Done." {
					t.Errorf("ResponseSummary: got %q", e.ResponseSummary)
				}
				if len(e.ToolCalls) != 2 || e.ToolCalls[0] != "Read" || e.ToolCalls[1] != "Write" {
					t.Errorf("ToolCalls: got %v", e.ToolCalls)
				}
			},
		},
		{
			name:     "Notification",
			wantType: EventNotification,
			raw: `{
				"type": "Notification",
				"session_id": "sess-007",
				"project_path": "/home/user/proj",
				"notification_type": "attention_required",
				"message": "Waiting for user input",
				"timestamp": "2024-06-01T12:00:00Z"
			}`,
			assertEvent: func(t *testing.T, ev HookEvent) {
				t.Helper()
				e, ok := ev.(*NotificationEvent)
				if !ok {
					t.Fatalf("expected *NotificationEvent, got %T", ev)
				}
				if e.NotificationType != "attention_required" {
					t.Errorf("NotificationType: got %q", e.NotificationType)
				}
				if e.Message != "Waiting for user input" {
					t.Errorf("Message: got %q", e.Message)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev, err := DecodeEvent(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("DecodeEvent() unexpected error: %v", err)
			}
			if ev.EventType() != tc.wantType {
				t.Errorf("EventType(): got %q, want %q", ev.EventType(), tc.wantType)
			}
			tc.assertEvent(t, ev)
		})
	}
}

func TestDecodeEvent_UnknownType(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"type": "GhostEvent", "session_id": "s", "project_path": "/p", "timestamp": "2024-01-01T00:00:00Z"}`)
	_, err := DecodeEvent(raw)
	if err == nil {
		t.Fatal("expected error for unknown event type, got nil")
	}

	var ue *UnknownEventTypeError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnknownEventTypeError, got %T: %v", err, err)
	}
	if ue.Type != "GhostEvent" {
		t.Errorf("UnknownEventTypeError.Type: got %q, want %q", ue.Type, "GhostEvent")
	}
}

func TestDecodeEvent_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := DecodeEvent(json.RawMessage(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestDecodeEvent_EmptyType(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"type": "", "session_id": "s"}`)
	_, err := DecodeEvent(raw)
	if err == nil {
		t.Fatal("expected error for empty type, got nil")
	}

	var ue *UnknownEventTypeError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnknownEventTypeError, got %T", err)
	}
}
