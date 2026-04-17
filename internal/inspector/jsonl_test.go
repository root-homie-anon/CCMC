package inspector

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseJSONLReader_HappyPath(t *testing.T) {
	input := `{"type":"user","timestamp":"2026-04-16T09:00:00Z","content":"hello"}
{"type":"assistant","timestamp":"2026-04-16T09:01:00Z","content":"hi"}
{"type":"tool_use","timestamp":"2026-04-16T09:02:00Z","name":"Read"}
{"type":"tool_result","timestamp":"2026-04-16T09:02:01Z","output":"data"}
`
	var events []Event
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	wantTypes := []EventType{EventUser, EventAssistant, EventToolUse, EventToolResult}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, want)
		}
	}

	wantTS := time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC)
	if !events[0].Timestamp.Equal(wantTS) {
		t.Errorf("event[0].Timestamp = %v, want %v", events[0].Timestamp, wantTS)
	}
}

func TestParseJSONLReader_SkipsMalformed(t *testing.T) {
	input := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
this is not json
{"type":"assistant","timestamp":"2026-04-16T09:01:00Z"}
`
	var count int
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		count++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("got %d events, want 2 (malformed line should be skipped)", count)
	}
}

func TestParseJSONLReader_EmptyLines(t *testing.T) {
	input := `{"type":"user"}

{"type":"assistant"}

`
	var count int
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		count++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("got %d events, want 2", count)
	}
}

func TestParseJSONLReader_EarlyStop(t *testing.T) {
	input := `{"type":"user"}
{"type":"assistant"}
{"type":"tool_use"}
`
	var count int
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		count++
		return count < 2 // Stop after 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("got %d events, want 2 (should stop early)", count)
	}
}

func TestParseJSONLReader_UnknownType(t *testing.T) {
	input := `{"type":"something_new","timestamp":"2026-04-16T09:00:00Z"}
`
	var events []Event
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventUnknown {
		t.Errorf("Type = %q, want %q", events[0].Type, EventUnknown)
	}
}

func TestParseJSONLReader_RoleBasedClassification(t *testing.T) {
	input := `{"role":"human","content":"test"}
{"role":"assistant","content":"reply"}
`
	var events []Event
	err := ParseJSONLReader(strings.NewReader(input), false, func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != EventUser {
		t.Errorf("event[0].Type = %q, want %q", events[0].Type, EventUser)
	}
	if events[1].Type != EventAssistant {
		t.Errorf("event[1].Type = %q, want %q", events[1].Type, EventAssistant)
	}
}

func TestCollectEvents_FromFile(t *testing.T) {
	content := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
{"type":"assistant","timestamp":"2026-04-16T09:01:00Z"}
`
	f, err := os.CreateTemp(t.TempDir(), "test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	events, err := CollectEvents(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}
