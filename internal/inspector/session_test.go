package inspector

import (
	"strings"
	"testing"
)

func TestAggregateSession_ToolCounts(t *testing.T) {
	input := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/a.go"},"timestamp":"2026-04-16T09:01:00Z"}
{"type":"tool_result","timestamp":"2026-04-16T09:01:01Z"}
{"type":"tool_use","name":"Edit","input":{"file_path":"/tmp/a.go"},"timestamp":"2026-04-16T09:02:00Z"}
{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/b.go"},"timestamp":"2026-04-16T09:03:00Z"}
{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."},"timestamp":"2026-04-16T09:04:00Z"}
{"type":"assistant","timestamp":"2026-04-16T09:05:00Z"}
`
	view, err := AggregateSession(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	if view.EventCount != 7 {
		t.Errorf("EventCount = %d, want 7", view.EventCount)
	}
	if len(view.RecentToolCalls) != 4 {
		t.Errorf("RecentToolCalls = %d, want 4", len(view.RecentToolCalls))
	}
	// /tmp/a.go read once
	if len(view.FilesRead) != 1 || view.FilesRead[0] != "/tmp/a.go" {
		t.Errorf("FilesRead = %v, want [/tmp/a.go]", view.FilesRead)
	}
	// /tmp/a.go edited + /tmp/b.go written = 2 modified
	if len(view.FilesModified) != 2 {
		t.Errorf("FilesModified = %v, want 2 entries", view.FilesModified)
	}
}

func TestAggregateSession_DeduplicatesFiles(t *testing.T) {
	input := `{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x.go"},"timestamp":"2026-04-16T09:00:00Z"}
{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x.go"},"timestamp":"2026-04-16T09:01:00Z"}
{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x.go"},"timestamp":"2026-04-16T09:02:00Z"}
`
	view, err := AggregateSession(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.FilesRead) != 1 {
		t.Errorf("FilesRead has %d entries, want 1 (should deduplicate)", len(view.FilesRead))
	}
}

func TestAggregateSession_MCPDetection(t *testing.T) {
	input := `{"type":"tool_use","name":"mcp__magic__21st_component","timestamp":"2026-04-16T09:00:00Z"}
{"type":"tool_use","name":"mcp__magic__logo_search","timestamp":"2026-04-16T09:01:00Z"}
{"type":"tool_use","name":"mcp__notion__search","timestamp":"2026-04-16T09:02:00Z"}
`
	view, err := AggregateSession(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.MCPs) != 2 {
		t.Errorf("MCPs = %v, want 2 entries (magic, notion)", view.MCPs)
	}
}

func TestAggregateSession_TimeBoundaries(t *testing.T) {
	input := `{"type":"user","timestamp":"2026-04-16T09:00:00Z"}
{"type":"assistant","timestamp":"2026-04-16T09:30:00Z"}
{"type":"user","timestamp":"2026-04-16T10:00:00Z"}
`
	view, err := AggregateSession(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if view.StartedAt.Hour() != 9 || view.StartedAt.Minute() != 0 {
		t.Errorf("StartedAt = %v, want 09:00", view.StartedAt)
	}
	if view.EndedAt.Hour() != 10 || view.EndedAt.Minute() != 0 {
		t.Errorf("EndedAt = %v, want 10:00", view.EndedAt)
	}
}

func TestAggregateSession_SlidingWindow(t *testing.T) {
	// Generate 25 tool_use events — window should keep only last 20
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, `{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/f.go"},"timestamp":"2026-04-16T09:00:00Z"}`)
	}
	input := strings.Join(lines, "\n") + "\n"

	view, err := AggregateSession(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RecentToolCalls) != maxRecentToolCalls {
		t.Errorf("RecentToolCalls = %d, want %d (sliding window)", len(view.RecentToolCalls), maxRecentToolCalls)
	}
}

func TestAggregateSession_Empty(t *testing.T) {
	view, err := AggregateSession(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if view.EventCount != 0 {
		t.Errorf("EventCount = %d, want 0", view.EventCount)
	}
}

func TestExtractMCPName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mcp__magic__logo_search", "magic"},
		{"mcp__notion__search", "notion"},
		{"mcp__x__y", "x"},
		{"mcp__standalone", "standalone"},
		{"short", ""},
	}
	for _, tt := range tests {
		got := extractMCPName(tt.input)
		if got != tt.want {
			t.Errorf("extractMCPName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
