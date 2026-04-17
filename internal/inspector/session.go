package inspector

import (
	"io"
	"time"
)

// SessionView is the aggregated state of a CC session, built from
// parsing its JSONL transcript. It captures what happened in the session
// without loading the full transcript into memory.
type SessionView struct {
	// Agents loaded during the session (from Agent tool_use blocks)
	Agents []string

	// ActiveSubagents currently running (started but not stopped)
	ActiveSubagents []SubagentInfo

	// RecentToolCalls holds the last N tool calls observed
	RecentToolCalls []ToolCall

	// FilesRead is a deduplicated list of files read during the session
	FilesRead []string

	// FilesModified is a deduplicated list of files written/edited during the session
	FilesModified []string

	// MCPs active in this session (extracted from tool names with mcp__ prefix)
	MCPs []string

	// ContextEstimate is the total bytes of JSONL processed
	ContextEstimate int64

	// EventCount is the total number of parsed events
	EventCount int

	// Duration from first to last event timestamp
	StartedAt time.Time
	EndedAt   time.Time
}

// SubagentInfo tracks a spawned subagent.
type SubagentInfo struct {
	Name        string
	Description string
}

// ToolCall records a single tool invocation.
type ToolCall struct {
	Tool      string
	Target    string // file path or command, depending on tool
	Timestamp time.Time
}

const maxRecentToolCalls = 20

// AggregateSession reads a JSONL transcript from r and produces a SessionView.
// Memory usage is proportional to the number of unique files/agents/MCPs, not
// the transcript size.
func AggregateSession(r io.Reader) (*SessionView, error) {
	view := &SessionView{}
	fileReadSet := make(map[string]bool)
	fileModSet := make(map[string]bool)
	agentSet := make(map[string]bool)
	mcpSet := make(map[string]bool)
	subagentMap := make(map[string]SubagentInfo) // track active subagents

	err := ParseJSONLReader(r, false, func(e Event) bool {
		view.EventCount++

		// Track session time boundaries
		if !e.Timestamp.IsZero() {
			if view.StartedAt.IsZero() || e.Timestamp.Before(view.StartedAt) {
				view.StartedAt = e.Timestamp
			}
			if e.Timestamp.After(view.EndedAt) {
				view.EndedAt = e.Timestamp
			}
		}

		switch e.Type {
		case EventToolUse:
			tc := extractToolCall(e)
			if tc.Tool != "" {
				// Maintain sliding window of recent tool calls
				if len(view.RecentToolCalls) >= maxRecentToolCalls {
					view.RecentToolCalls = view.RecentToolCalls[1:]
				}
				view.RecentToolCalls = append(view.RecentToolCalls, tc)

				// Track files
				switch tc.Tool {
				case "Read", "Glob", "Grep":
					if tc.Target != "" && !fileReadSet[tc.Target] {
						fileReadSet[tc.Target] = true
						view.FilesRead = append(view.FilesRead, tc.Target)
					}
				case "Write", "Edit", "MultiEdit":
					if tc.Target != "" && !fileModSet[tc.Target] {
						fileModSet[tc.Target] = true
						view.FilesModified = append(view.FilesModified, tc.Target)
					}
				case "Agent":
					name := extractStringField(e.Raw, "name")
					if name == "" {
						name = extractStringField(e.Raw, "description")
					}
					if name != "" && !agentSet[name] {
						agentSet[name] = true
						view.Agents = append(view.Agents, name)
					}
				}

				// Detect MCP tools (prefixed with mcp__)
				if len(tc.Tool) > 5 && tc.Tool[:5] == "mcp__" {
					mcpName := extractMCPName(tc.Tool)
					if mcpName != "" && !mcpSet[mcpName] {
						mcpSet[mcpName] = true
						view.MCPs = append(view.MCPs, mcpName)
					}
				}
			}
		default:
			// Other event types don't need special handling for aggregation
		}

		return true
	})

	if err != nil {
		return nil, err
	}

	// Convert active subagent map to slice
	for _, sa := range subagentMap {
		view.ActiveSubagents = append(view.ActiveSubagents, sa)
	}

	return view, nil
}

// extractToolCall pulls tool name and target from a tool_use event's raw JSON.
func extractToolCall(e Event) ToolCall {
	tc := ToolCall{Timestamp: e.Timestamp}

	// Try top-level "name" field
	tc.Tool = extractStringField(e.Raw, "name")

	// Try nested in content blocks
	if tc.Tool == "" {
		if content, ok := e.Raw["content"].([]any); ok {
			for _, block := range content {
				if m, ok := block.(map[string]any); ok {
					if m["type"] == "tool_use" {
						tc.Tool = extractStringField(m, "name")
						if input, ok := m["input"].(map[string]any); ok {
							tc.Target = extractTarget(tc.Tool, input)
						}
						break
					}
				}
			}
		}
	}

	// Extract target from top-level input
	if tc.Target == "" {
		if input, ok := e.Raw["input"].(map[string]any); ok {
			tc.Target = extractTarget(tc.Tool, input)
		}
	}

	return tc
}

// extractTarget pulls the most relevant path/command from a tool's input.
func extractTarget(tool string, input map[string]any) string {
	switch tool {
	case "Read", "Write", "Edit", "MultiEdit":
		return extractStringField(input, "file_path")
	case "Bash":
		cmd := extractStringField(input, "command")
		if len(cmd) > 80 {
			return cmd[:80] + "..."
		}
		return cmd
	case "Grep":
		return extractStringField(input, "pattern")
	case "Glob":
		return extractStringField(input, "pattern")
	case "Agent":
		return extractStringField(input, "description")
	default:
		return ""
	}
}

// extractMCPName pulls the MCP server name from a tool name like "mcp__servername__toolname".
func extractMCPName(toolName string) string {
	// Format: mcp__<server>__<tool>
	if len(toolName) <= 5 {
		return ""
	}
	rest := toolName[5:] // strip "mcp__"
	for i, c := range rest {
		if c == '_' && i+1 < len(rest) && rest[i+1] == '_' {
			return rest[:i]
		}
	}
	return rest
}

func extractStringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
