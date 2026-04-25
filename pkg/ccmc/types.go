package ccmc

import "time"

// SessionStatus represents the lifecycle state of a tracked CC session.
type SessionStatus string

const (
	SessionActive   SessionStatus = "active"   // Receiving hook events
	SessionIdle     SessionStatus = "idle"      // No events for >60s
	SessionDead     SessionStatus = "dead"      // Process gone
	SessionArchived SessionStatus = "archived"  // Manually archived
)

// Session is a registry entry for a tracked Claude Code session.
// Fields derived from hook events and filesystem discovery.
type Session struct {
	ID              string        `json:"id"`
	ProjectPath     string        `json:"projectPath"`     // Decoded from ~/.claude/projects/<encoded-cwd>
	ProjectName     string        `json:"projectName"`     // Derived from path or CLAUDE.md
	Status          SessionStatus `json:"status"`
	LastActivity    time.Time     `json:"lastActivity"`
	TaskSummary     string        `json:"taskSummary"`     // From most recent Stop or PostToolUse
	ActiveSubagents []string      `json:"activeSubagents"` // From SubagentStart/SubagentStop tracking
	FilesTouched    []string      `json:"filesTouched"`    // From PostToolUse Write/Edit events
	ContextEstimate int64         `json:"contextEstimate"` // JSONL file size in bytes as proxy
	PID             int           `json:"pid,omitempty"`   // OS process ID, if known
	StartedAt       time.Time     `json:"startedAt"`
}

// HookEventType identifies which CC lifecycle event fired.
type HookEventType string

const (
	HookSessionStart  HookEventType = "SessionStart"
	HookSessionEnd    HookEventType = "SessionEnd"
	HookPostToolUse   HookEventType = "PostToolUse"
	HookSubagentStart HookEventType = "SubagentStart"
	HookSubagentStop  HookEventType = "SubagentStop"
	HookStop          HookEventType = "Stop"
	HookNotification  HookEventType = "Notification"
)

// HookEvent is a parsed event from an HTTP hook POST to the daemon.
type HookEvent struct {
	Type      HookEventType `json:"type"`
	SessionID string        `json:"sessionId"`
	Timestamp time.Time     `json:"timestamp"`
	Payload   map[string]any `json:"payload"` // Event-specific fields vary by type
}

// RefCategory is an enum of reference database categories.
type RefCategory string

const (
	RefCommands    RefCategory = "commands"    // Built-in slash commands
	RefSkills      RefCategory = "skills"      // Bundled skills
	RefFlags       RefCategory = "flags"       // CLI flags
	RefShortcuts   RefCategory = "shortcuts"   // Keyboard shortcuts
	RefHooks       RefCategory = "hooks"       // Hook events with schemas
	RefTools       RefCategory = "tools"       // Tool names for permissions/matchers
	RefFrontmatter RefCategory = "frontmatter" // Skill/command frontmatter fields
	RefEnvVars     RefCategory = "envvars"     // Environment variables
	RefFilePaths   RefCategory = "filepaths"   // ~/.claude/ directory structure
)

// RefEntry is a single reference database entry.
type RefEntry struct {
	Name        string      `json:"name"        yaml:"name"`
	Category    RefCategory `json:"category"    yaml:"category"`
	Description string      `json:"description" yaml:"description"`
	Usage       string      `json:"usage"       yaml:"usage"`
	Examples    []string    `json:"examples"    yaml:"examples"`
	Related     []string    `json:"related"     yaml:"related"`
	Gotchas     []string    `json:"gotchas"     yaml:"gotchas"`
	Detail      string      `json:"detail"      yaml:"detail"` // Extended markdown for hooks, etc.
}

// InventoryScope indicates whether an item is global or project-scoped.
type InventoryScope string

const (
	ScopeGlobal  InventoryScope = "global"
	ScopeProject InventoryScope = "project"
)

// InventoryItemType identifies the kind of CC component.
type InventoryItemType string

const (
	ItemMCP     InventoryItemType = "mcp"
	ItemSkill   InventoryItemType = "skill"
	ItemCommand InventoryItemType = "command"
	ItemAgent   InventoryItemType = "agent"
	ItemPlugin  InventoryItemType = "plugin"
	ItemHook    InventoryItemType = "hook"
)

// InventoryItem is a generic entry for any CC component found during inventory scan.
type InventoryItem struct {
	Name        string            `json:"name"`
	Type        InventoryItemType `json:"type"`
	Scope       InventoryScope    `json:"scope"`
	Description string            `json:"description"`
	FilePath    string            `json:"filePath"`    // Where this item is defined
	ProjectPath string            `json:"projectPath"` // Which project scope, empty for global
	Extra       map[string]any    `json:"extra"`       // Type-specific fields (e.g. MCP transport, agent model)
}

// EvalResult is the structured output from the tool evaluator (Anthropic API call).
type EvalResult struct {
	ToolName       string   `json:"toolName"`
	RepoURL        string   `json:"repoUrl"`
	Capability     string   `json:"capability"`     // What it does (one line)
	GapFilled      string   `json:"gapFilled"`      // Capability gap it fills, or overlap it creates
	ProjectsBenefit []string `json:"projectsBenefit"` // Which user projects would benefit
	Dependencies   []string `json:"dependencies"`    // Required deps
	RiskAssessment string   `json:"riskAssessment"`  // Permissions needed, data accessed
	Recommendation string   `json:"recommendation"`  // install | skip | investigate
}

// ScopeFiles holds the raw file paths collected for one inventory scope (global or one project).
type ScopeFiles struct {
	ProjectPath  string   // Empty for global scope; encoded project dir name for project scope.
	SettingsPath string   // Absolute path to settings.json; empty if not present.
	ClaudeMDPath string   // Absolute path to CLAUDE.md; empty if not present.
	Commands     []string // Absolute paths to commands/*.md files.
	Skills       []string // Absolute paths to skills/*/SKILL.md files (dirs without SKILL.md are skipped).
	Agents       []string // Absolute paths to agents/*.md files.
	Plugins      []string // Absolute paths to plugins/* entries (may be dirs or files).
}

// InventoryRaw is the unprocessed output of the filesystem scanner.
// Downstream per-type modules (mcp.go, skills.go, agents.go, plugins.go) consume this.
type InventoryRaw struct {
	Global   ScopeFiles
	Projects []ScopeFiles // Sorted ascending by ProjectPath.
}

// EvalContext is the evidence bundle the evaluator passes to the Anthropic API.
// All fields come from the GitHub API; supplementary fields are empty string when absent.
type EvalContext struct {
	Owner           string   `json:"owner"`
	Repo            string   `json:"repo"`
	Description     string   `json:"description"`     // From repo metadata
	Topics          []string `json:"topics"`          // GitHub topic tags
	DefaultBranch   string   `json:"defaultBranch"`
	ReadmeMarkdown  string   `json:"readmeMarkdown"`  // Raw text of first README hit
	PackageJSON     string   `json:"packageJson"`     // Raw text if present at root, else ""
	PyprojectTOML   string   `json:"pyprojectToml"`   // Raw text if present at root, else ""
	ExampleSettings string   `json:"exampleSettings"` // settings.json at root or examples/, else ""
}

// DaemonStatus is the daemon health and registry summary returned by GET /status.
type DaemonStatus struct {
	Running       bool      `json:"running"`
	PID           int       `json:"pid"`
	Uptime        string    `json:"uptime"`
	SessionCount  int       `json:"sessionCount"`
	ActiveCount   int       `json:"activeCount"`
	IdleCount     int       `json:"idleCount"`
	LastEventAt   time.Time `json:"lastEventAt"`
	RegistryPath  string    `json:"registryPath"`
	SocketPath    string    `json:"socketPath"`
}
