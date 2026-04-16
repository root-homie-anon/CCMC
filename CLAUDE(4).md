# CCMC (Claude Code Mission Control) — Master Project File

> **This project inherits global config from `~/.claude/CLAUDE.md`.**
> Do NOT duplicate these sections — they are already loaded globally:
> - Session modes (COLLAB/AUTO)
> - Agent routing (permanent team: Marcus, Tony, Doug, Ava, Nate, Linda, Omar, Elliot, Ray, Chris, Mark, Jared)
> - Git defaults (conventional commits, branch strategy)
> - Security defaults (.env in .gitignore, no hardcoded secrets)
> - KeyMaster (centralized key management)
> - Context management (10% save protocol)
> - Base code standards (TypeScript strict, functional, async/await)
> - Base automation assumptions (stateless agents, parallel execution, errors surface)

---

## System Overview

CCMC is a personal CLI tool and iTerm2 integration that serves as a unified control plane for Claude Code. It solves three problems at once: you can't see all your CC sessions and their state in one place, you can't peek inside a session without interrupting it, and there's no single reference for CC's massive command/tool surface. CCMC gives you a real-time session dashboard, deep session inspection, lifecycle controls (kill/launch), a searchable CC reference engine, a live inventory of your personal CC setup (custom commands, MCPs, skills, agents), and a tool evaluator that takes a GitHub URL, analyzes the repo against your current stack, and tells you if it's worth integrating — then installs it if you say yes. Built as a single Go binary with an optional iTerm2 status bar integration. Solo tool, one user, no auth.

---

## Orchestrator Behavior

This file is the root orchestrator. On session start:

1. Fire the session-start hook
2. Load state from `state/` if it exists
3. Ask the user: continue existing run, start a new one, or initialize a new sub-project
4. Spawn subagents scoped to their domain — they share no state unless explicitly passed
5. @ref-builder and @inventory-scanner can run in parallel on first build
6. @daemon-builder blocks until core registry types are defined
7. @tui-builder blocks until daemon API is stable
8. @iterm-builder runs last — depends on daemon API being finalized

---

## Agent Team

All agents live in `.claude/agents/` and are shared across the project.

| Agent | Role |
|-------|------|
| `@orchestrator` (Vincent) | Drives the session, delegates tasks, manages state |
| `@daemon-builder` | Builds the background daemon: HTTP server, hook receiver, session registry, filesystem scanner |
| `@tui-builder` | Builds the Bubble Tea dashboard: session list, inspector panel, command bar, reference search overlay |
| `@ref-builder` | Scrapes CC docs, structures the reference database, builds the search/lookup engine |
| `@inventory-scanner` | Builds the module that reads `~/.claude/` config across all scopes and produces the live inventory |
| `@tool-integrator` | Builds the eval/install pipeline: GitHub repo analysis, Anthropic API integration, config writer |
| `@iterm-builder` | Builds the iTerm2 Python status bar component and popover integration |
| `@qa` | Integration testing across all modules, CLI ergonomics review |

---

## Core User Flow

```
USER OPENS TERMINAL
    |
    v
iTerm status bar shows "CC: 3 active · 1 idle"  (ambient awareness, always visible)
    |
    v
USER CLICKS STATUS BAR  or  USER RUNS `ccmc`
    |                              |
    v                              v
Quick popover with               Dashboard opens in terminal
session list + status             (Bubble Tea full-screen TUI)
    |                              |
    |   (click a session)          |   (arrow keys to select session)
    v                              v
Opens iTerm tab running           Right panel shows inspector:
`ccmc inspect <id>`               agents, tool calls, files,
                                  todo state, context estimate
    |
    v
USER ACTIONS (from dashboard or CLI):
    |
    ├── `k` or `ccmc kill <id>`     → kills CC session
    ├── `l` or `ccmc launch <dir>`  → launches new CC session in iTerm tab
    ├── `?` or `ccmc ref <query>`   → opens reference search overlay
    ├── `i` or `ccmc inventory`     → shows personal CC setup inventory
    ├── `ccmc eval <github-url>`    → evaluates tool/MCP for fit
    └── `ccmc install <github-url>` → installs and wires up tool/MCP
```

---

## Feature Specifications

### 1. Session Observer

**Purpose:** Real-time visibility into all CC sessions across all projects.

**Data sources:**
- HTTP hooks installed in `~/.claude/settings.json` (global scope) — async hooks on SessionStart, SessionEnd, PostToolUse, SubagentStart, SubagentStop, Stop, Notification events POST structured JSON to daemon
- Filesystem scan of `~/.claude/projects/<encoded-cwd>/*.jsonl` for session JSONL transcripts
- `~/.claude/projects/<project-hash>/<session-id>/session-memory/summary.md` for session memory summaries
- `~/.claude/todos/<sessionId>.json` for todo state
- Process table scan for running `claude` processes (fallback discovery)

**Session registry fields:**
- Session ID
- Project path (decoded from the encoded directory name)
- Project name (derived from path or CLAUDE.md if present)
- Status: active (receiving hook events), idle (no events for >60s), dead (process gone)
- Last activity timestamp
- Current task summary (extracted from most recent Stop or PostToolUse event)
- Active subagents (from SubagentStart/SubagentStop tracking)
- Files touched this session (from PostToolUse Write/Edit events)
- Context estimate (from session JSONL size heuristic)

**Dashboard display:**
- Left panel: session list, color-coded status badges (green=active, yellow=idle, red=dead, blue=archived)
- Sorted by last activity, most recent first
- Project name + session snippet preview on each row
- Active session count in header

### 2. Session Inspector

**Purpose:** Deep peek into any session's internals without interrupting it.

**Inspector panel shows (right side of dashboard):**
- Project path and name
- Session start time and duration
- Loaded agents (parsed from session JSONL — look for Agent tool_use blocks)
- Active subagents with their task descriptions
- Recent tool calls (last 20, with tool name, target file/command, and timestamp)
- Files read and files modified (deduplicated lists)
- Todo state (parsed from todo JSON)
- Session memory summary (if exists)
- Context usage estimate (JSONL file size as rough proxy)
- MCP servers active in this session (from session config)
- Custom commands available in this session's project scope

**Data parsing:**
- Read session JSONL line by line, parse each JSON object
- Filter by message type: `tool_use`, `tool_result`, `assistant`, `user`
- Extract tool names from `tool_use` blocks: `Read`, `Write`, `Edit`, `Bash`, `Grep`, `Glob`, `Agent`, `Task`, `SendMessage`, etc.
- Track subagent lifecycle from `Agent` tool_use/tool_result pairs
- Extract file paths from `Read`/`Write`/`Edit` tool inputs

### 3. Session Control

**Kill:** `ccmc kill <session-id>` or `k` key in dashboard
- Finds the CC process by PID (stored in registry or looked up from process table)
- Sends SIGTERM for clean shutdown
- Updates registry status to dead
- Optionally archives the session entry

**Launch:** `ccmc launch <directory>` or `l` key in dashboard (prompts for directory)
- Opens a new iTerm tab (via `osascript` or iTerm Python API)
- Runs `claude` in the specified directory
- Session auto-registers with daemon via the SessionStart hook
- If no iTerm available, launches in current terminal with a subprocess

### 4. CC Reference Engine

**Purpose:** Instant searchable lookup for everything CC can do, so you never leave the terminal to read docs.

**Reference categories:**

**Built-in commands** — every slash command: `/clear`, `/compact`, `/init`, `/model`, `/cost`, `/permissions`, `/config`, `/hooks`, `/mcp`, `/agents`, `/diff`, `/vim`, `/theme`, `/terminal-setup`, `/doctor`, `/status`, `/desktop`, `/upgrade`, `/logout`, `/bug`, `/help`, etc.
Each entry: name, description, usage examples, related commands, gotchas.

**Bundled skills** — `/simplify`, `/batch`, `/debug`, `/loop`, `/claude-api`, `/btw`, etc.
Each entry: name, what it does, when to use it, how it differs from similar commands.

**CLI flags** — every flag including undocumented ones: `--model`, `--system-prompt`, `--system-prompt-file`, `--append-system-prompt`, `--append-system-prompt-file`, `--output-format`, `--resume`, `--continue`, `--no-session-persistence`, `--verbose`, `--agent`, `--add-dir`, `--worktree`, `--effort`, `-p` (print mode), etc.
Each entry: flag, description, valid values, examples.

**Keyboard shortcuts** — all interactive mode shortcuts: `Ctrl+C` (interrupt), `Ctrl+O` (transcript viewer), `Ctrl+F` (kill background agents), `Ctrl+B` (background a command), `Ctrl+R` (history search), `Shift+Enter` (multiline), `Space`/`Enter`/`Escape` (dismiss side question), `Tab`/`Right` (accept suggestion), `!` prefix (bash mode), `@` (file mention), etc.

**Hook events** — all 21 lifecycle events: SessionStart, SessionEnd, PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, UserPromptSubmit, Stop, StopFailure, SubagentStart, SubagentStop, PreCompact, Notification, CwdChanged, FileChanged, ConfigChanged, InstructionsLoaded, TeammateIdle, TaskCompleted, Setup, WorktreeCreated.
Each entry: event name, when it fires, available matchers, JSON input schema, supported handler types, exit code behavior, example config.

**Tool names** — all built-in tools for use in permission rules and hook matchers: `Read`, `Write`, `Edit`, `MultiEdit`, `Bash`, `Grep`, `Glob`, `Agent`, `Task`, `SendMessage`, `TeamCreate`, `TeamDelete`, `TaskCreate`, `TaskUpdate`, `TaskGet`, `TaskList`, `WebFetch`, `WebSearch`, `Notebook.*`, `LSP`, `Monitor`, `Skill`, `ExitPlanMode`, `PowerShell`, etc.

**Skill/command frontmatter** — all YAML frontmatter fields: `name`, `description`, `command`, `allowed-tools`, `argument-hint`, `model`, `context`, `agent`, `disable-model-invocation`, `user-invocable`, `when-to-use`, `hooks`, `shell`, `mcpServers`, etc.

**Environment variables** — `CLAUDE_ENV_FILE`, `CLAUDE_PROJECT_DIR`, `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_SKIP_PROMPT_HISTORY`, `CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR`, `CLAUDE_CODE_USE_POWERSHELL_TOOL`, `SLASH_COMMAND_TOOL_CHAR_BUDGET`, `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD`, `DISABLE_TELEMETRY`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, etc.

**File locations** — complete `~/.claude/` directory map: `settings.json`, `CLAUDE.md`, `commands/`, `skills/`, `agents/`, `plugins/`, `projects/<encoded-cwd>/*.jsonl`, `projects/<hash>/<session>/session-memory/summary.md`, `todos/<sessionId>.json`, `shell-snapshots/`, `backups/`, per-project `.claude/` structure.

**Reference storage format:**
- Structured YAML files in `internal/refdata/`, one per category
- Compiled into the binary at build time via `embed`
- Fuzzy search via a lightweight in-memory index (no external dependencies)

**Access:**
- CLI: `ccmc ref <query>` — fuzzy matches across all categories, shows top results
- CLI: `ccmc ref hooks SessionStart` — scoped search within a category
- Dashboard: press `?` or `/` to open search overlay, type to filter, `Enter` to view detail

### 5. Your CC Inventory

**Purpose:** Show everything installed and configured in your CC setup, across all scopes.

**What it reads:**

**Global scope (`~/.claude/`):**
- `settings.json` → global hooks, permission rules, MCP servers, env vars
- `commands/` → personal custom slash commands
- `skills/` → personal skills (parse SKILL.md frontmatter for name, description)
- `agents/` → personal subagents (parse frontmatter for name, description, tools)
- `plugins/` → installed plugins
- `CLAUDE.md` → global memory/instructions

**Project scope (`.claude/` in each project):**
- `settings.json` → project-specific hooks, permissions, MCP servers
- `commands/` → project slash commands
- `skills/` → project skills
- `agents/` → project subagents
- `CLAUDE.md` → project memory

**Derived data:**
- MCP servers: name, type (stdio/sse), status (connected/configured), exposed tools (if discoverable from config)
- Custom commands: name, description (from frontmatter), scope (global/project), file path
- Skills: name, description, invocation mode (user/auto/both), scope
- Subagents: name, description, model, allowed tools, scope
- Plugins: name, source, components (skills, agents, hooks, MCPs)
- Hooks: event → handler mappings, scope

**Display:**
- CLI: `ccmc inventory` — full inventory dump, grouped by scope
- CLI: `ccmc inventory mcps` — just MCP servers
- CLI: `ccmc inventory skills` — just skills
- Dashboard: press `i` to switch right panel to inventory view
- Per-session: inspector shows which inventory items are active for that specific session

### 6. Tool Integrator

**Purpose:** Evaluate and install CC tools/MCPs from GitHub without manual config wrangling.

**Evaluate flow (`ccmc eval <github-url>`):**
1. Fetch the GitHub repo README via the GitHub API (raw content)
2. Also fetch: repo description, topics/tags, `package.json` or `pyproject.toml` if present, any `settings.json` example in the repo
3. Load the user's current inventory (MCPs, tools, skills, project list)
4. Send to Anthropic API (Claude Sonnet for cost efficiency):
   - System prompt: "You are a CC tool evaluator. Given a GitHub repo's README and the user's current CC setup, assess whether this tool would be useful. Consider: what capability it adds, whether it overlaps with existing tools, which of the user's projects would benefit, any risks or dependencies, and how to configure it."
   - User content: README + repo metadata + current inventory summary
5. Parse response and display:
   - What it does (one line)
   - Capability gap it fills (or overlap it creates)
   - Which projects would benefit
   - Dependencies required
   - Risk assessment (permissions it needs, data it accesses)
   - Recommendation: install / skip / investigate further
6. Prompt: "Install? (y/n/project-only)"

**Install flow (`ccmc install <github-url>` or `y` after eval):**
1. Determine tool type from repo analysis:
   - MCP server (stdio) → clone repo, install deps, add to `settings.json` mcpServers
   - MCP server (SSE) → add URL-based config to `settings.json` mcpServers
   - Plugin → `claude plugin install` if marketplace-compatible, else manual setup
   - Skill → copy to `~/.claude/skills/` or `.claude/skills/`
   - Subagent → copy to `~/.claude/agents/` or `.claude/agents/`
2. Scope selection: global (`~/.claude/settings.json`) or project-specific (`.claude/settings.json`)
3. Write config to the appropriate `settings.json`
4. Verify: check if the MCP server responds / skill loads / agent is discoverable
5. Report: "Installed mcp-postgres globally. Available in all sessions. Exposed tools: query, execute, list_tables."

**Manage:**
- `ccmc tools ls` — list all installed tools/MCPs with status
- `ccmc tools rm <name>` — remove from config, optionally delete cloned repo
- `ccmc tools update <name>` — git pull on cloned repos, re-verify

### 7. iTerm2 Status Bar Integration

**Purpose:** Ambient awareness without opening anything. Always-visible session status in the iTerm status bar.

**Implementation:** Python script using the iTerm2 Python API, lives in `~/Library/Application Support/iTerm2/Scripts/AutoLaunch/ccmc_statusbar.py`.

**Status bar component:**
- Shows: `CC: 3 active · 1 idle` (or `CC: —` when daemon not running)
- Updates every 5 seconds by querying daemon's HTTP API
- Color-coded: green when all sessions healthy, yellow when any idle, red when any stalled

**Click behavior:**
- Opens a popover with a compact session list: project name, status badge, last activity time
- Each session row is clickable — opens a new iTerm tab running `ccmc inspect <session-id>`
- "Launch new session" button at bottom — prompts for directory, opens new iTerm tab with `claude`

**Installation:** `ccmc iterm-install` copies the Python script to the AutoLaunch folder and provides instructions for enabling the status bar component in iTerm preferences.

---

## Project Structure

```
ccmc/
├── CLAUDE.md                        ← this file, root orchestrator
├── .env                             ← secrets (ANTHROPIC_API_KEY), never committed
├── .env.example                     ← committed env template
├── .claude/
│   └── agents/                      ← project-specific agents
│       ├── orchestrator.md
│       ├── daemon-builder.md
│       ├── tui-builder.md
│       ├── ref-builder.md
│       ├── inventory-scanner.md
│       ├── tool-integrator.md
│       ├── iterm-builder.md
│       └── qa.md
├── cmd/
│   └── ccmc/
│       └── main.go                  ← single binary entry point, subcommand routing
├── internal/
│   ├── daemon/
│   │   ├── server.go                ← HTTP server on unix socket, hook event receiver
│   │   ├── registry.go              ← session registry: discovery, tracking, state machine
│   │   └── scanner.go               ← filesystem scanner for ~/.claude/projects/
│   ├── hooks/
│   │   ├── installer.go             ← one-time hook injection into ~/.claude/settings.json
│   │   ├── handlers.go              ← HTTP handlers for each hook event type
│   │   └── events.go                ← event type definitions and parsing
│   ├── inspector/
│   │   ├── jsonl.go                 ← JSONL session transcript parser
│   │   ├── session.go               ← session state aggregator (tools, files, agents, todos)
│   │   └── memory.go                ← session-memory summary reader
│   ├── lifecycle/
│   │   ├── kill.go                  ← process termination
│   │   └── launch.go                ← session launcher (iTerm tab or subprocess)
│   ├── reference/
│   │   ├── engine.go                ← search engine: fuzzy match, category filtering
│   │   ├── loader.go                ← loads embedded YAML reference data
│   │   └── data/                    ← embedded reference YAML files
│   │       ├── commands.yaml        ← built-in slash commands
│   │       ├── skills.yaml          ← bundled skills
│   │       ├── flags.yaml           ← CLI flags
│   │       ├── shortcuts.yaml       ← keyboard shortcuts
│   │       ├── hooks.yaml           ← hook events with schemas
│   │       ├── tools.yaml           ← tool names for permissions/matchers
│   │       ├── frontmatter.yaml     ← skill/command frontmatter fields
│   │       ├── envvars.yaml         ← environment variables
│   │       └── filepaths.yaml       ← ~/.claude/ directory structure
│   ├── inventory/
│   │   ├── scanner.go               ← reads ~/.claude/ and .claude/ configs
│   │   ├── mcp.go                   ← MCP server inventory
│   │   ├── skills.go                ← skills/commands inventory
│   │   ├── agents.go                ← subagent inventory
│   │   └── plugins.go               ← plugin inventory
│   ├── integrator/
│   │   ├── evaluator.go             ← GitHub repo fetcher + Anthropic API eval
│   │   ├── installer.go             ← tool/MCP installer and config writer
│   │   ├── manager.go               ← list, remove, update installed tools
│   │   └── github.go                ← GitHub API client for repo metadata + README
│   ├── tui/
│   │   ├── app.go                   ← Bubble Tea app model, top-level update/view
│   │   ├── sessions.go              ← session list panel component
│   │   ├── inspector.go             ← inspector panel component
│   │   ├── inventory.go             ← inventory panel component
│   │   ├── reference.go             ← reference search overlay component
│   │   ├── commandbar.go            ← bottom command bar component
│   │   └── styles.go                ← Lip Gloss style definitions
│   ├── iterm/
│   │   ├── statusbar.py             ← iTerm2 Python API status bar script
│   │   └── install.go               ← copies script to AutoLaunch, prints instructions
│   └── config/
│       ├── paths.go                 ← ~/.claude/ path constants and resolution
│       ├── settings.go              ← settings.json reader/writer
│       └── config.go                ← CCMC's own config (~/.ccmc/config.yaml)
├── pkg/
│   └── ccmc/
│       ├── types.go                 ← shared types: Session, HookEvent, RefEntry, InventoryItem
│       └── api.go                   ← daemon API client (used by CLI, TUI, and iTerm script)
├── state/                           ← runtime agent state, gitignored
├── go.mod
├── go.sum
└── Makefile                         ← build, install, test targets
```

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.22+ |
| TUI framework | Bubble Tea (charmbracelet/bubbletea) |
| TUI styling | Lip Gloss (charmbracelet/lipgloss) |
| TUI components | Bubbles (charmbracelet/bubbles) — list, viewport, textinput, spinner |
| HTTP server | net/http (stdlib) — unix socket listener |
| JSON parsing | encoding/json (stdlib) — streaming JSONL decoder |
| Embedded data | embed (stdlib) — reference YAML compiled into binary |
| YAML parsing | gopkg.in/yaml.v3 |
| Fuzzy search | sahilm/fuzzy |
| Process management | os/exec, syscall (stdlib) |
| GitHub API | net/http (stdlib) — raw REST calls, no SDK needed |
| Anthropic API | net/http (stdlib) — direct /v1/messages POST |
| iTerm integration | Python 3 + iterm2 pip package |
| Build | Make + go build with ldflags for version |

---

## Project-Specific Standards

- **Go idioms only** — no framework abstractions over stdlib where stdlib suffices. net/http for servers, encoding/json for parsing, os/exec for processes. External deps only for TUI (Bubble Tea) and fuzzy search.
- **No database** — all state is the filesystem. Session registry is in-memory with periodic JSON dump to `~/.ccmc/registry.json` for daemon restarts. Reference data is embedded YAML. Inventory is read live from `~/.claude/`.
- **Single binary distribution** — `go build` produces one binary. No Docker, no npm, no Python runtime required for the core tool. The iTerm script is the only Python component and it's optional.
- **Daemon is optional** — every CLI command works without the daemon running. Without daemon, session list comes from filesystem scan only (no real-time hook events). Dashboard starts the daemon automatically if not running.
- **Config writer safety** — when CCMC writes to `settings.json` (hook installation, tool integration), it always reads the current file, merges changes, and writes back. Never overwrite. Always create a `.bak` before modifying.
- **JSONL parsing is read-only and defensive** — session transcripts can be large. Stream-parse line by line, never load full file into memory. Skip malformed lines silently. Never write to session JSONL files.
- **Anthropic API usage is eval-only** — the only feature that calls the Anthropic API is `ccmc eval`. Uses Claude Sonnet for cost efficiency. API key stored in `~/.ccmc/config.yaml` or `ANTHROPIC_API_KEY` env var.

---

## Project-Specific Automation

- **Hook installation is idempotent** — `ccmc setup` can be run repeatedly. It checks if hooks are already present before adding them. It never duplicates hook entries.
- **Reference data updates** — `ccmc ref-update` scrapes the official CC docs (code.claude.com) and regenerates the YAML reference files. This is a dev-time operation, not user-facing. Updated reference ships with the next binary release.
- **Daemon auto-start** — when the dashboard or any command that needs real-time data launches, it checks for the daemon unix socket. If not found, starts the daemon as a background process. Daemon writes its PID to `~/.ccmc/daemon.pid`.
- **Daemon auto-stop** — daemon exits cleanly after 30 minutes of no connected clients and no active CC sessions. Restarts on next need.

---

## config.json Schema

```yaml
# ~/.ccmc/config.yaml
daemon:
  socket: ~/.ccmc/ccmc.sock          # unix socket path
  auto_start: true                   # start daemon automatically when needed
  auto_stop_minutes: 30              # stop daemon after this many idle minutes
  scan_interval_seconds: 10          # filesystem scan frequency

hooks:
  installed: false                   # whether global hooks have been set up
  events:                            # which events to hook (all async)
    - SessionStart
    - SessionEnd
    - PostToolUse
    - SubagentStart
    - SubagentStop
    - Stop
    - Notification

reference:
  version: "2026.04"                 # reference data version (matches CC version)

integrator:
  anthropic_api_key: ""              # or use ANTHROPIC_API_KEY env var
  model: "claude-sonnet-4-6"         # model for eval calls
  clone_dir: ~/.ccmc/tools/          # where cloned tool repos live

iterm:
  installed: false                   # whether iTerm script is in AutoLaunch
  poll_interval_seconds: 5           # status bar refresh rate
```

---

## Shared Resources

### API Keys

Run: `keymaster require CCMC ANTHROPIC_API_KEY`

- `ANTHROPIC_API_KEY` — required only for `ccmc eval` feature. Not needed for session observation, reference, or inventory.

### Shared Types

All types shared across modules live in `pkg/ccmc/types.go`. Never duplicate types — always import from `pkg/ccmc`.

Key types:
- `Session` — registry entry for a tracked CC session
- `HookEvent` — parsed event from an HTTP hook POST
- `RefEntry` — single reference database entry (command, hook, flag, etc.)
- `RefCategory` — enum of reference categories
- `InventoryItem` — generic inventory entry (MCP, skill, agent, command, plugin)
- `EvalResult` — structured output from the tool evaluator
- `DaemonStatus` — daemon health and registry summary

---

## CLI Command Map

```
ccmc                                 → launches dashboard (TUI)
ccmc ls                              → list all tracked sessions (table format)
ccmc inspect <session-id>            → detailed session inspection (scrollable output)
ccmc kill <session-id>               → kill a CC session
ccmc launch <directory>              → launch new CC session (iTerm tab or subprocess)

ccmc ref <query>                     → fuzzy search across all reference categories
ccmc ref commands                    → list all built-in commands
ccmc ref hooks                       → list all hook events
ccmc ref hooks SessionStart          → detail on a specific hook event
ccmc ref flags                       → list all CLI flags
ccmc ref shortcuts                   → list all keyboard shortcuts
ccmc ref tools                       → list all tool names
ccmc ref frontmatter                 → list all frontmatter fields
ccmc ref env                         → list all environment variables
ccmc ref files                       → show ~/.claude/ directory map

ccmc inventory                       → full inventory of your CC setup
ccmc inventory mcps                  → just MCP servers
ccmc inventory skills                → just skills and custom commands
ccmc inventory agents                → just subagents
ccmc inventory plugins               → just plugins
ccmc inventory hooks                 → just hook configurations

ccmc eval <github-url>               → evaluate a tool/MCP for fit
ccmc install <github-url>            → install and wire up a tool/MCP
ccmc tools ls                        → list installed tools (via ccmc)
ccmc tools rm <name>                 → remove installed tool
ccmc tools update [name]             → update installed tools (git pull)

ccmc setup                           → one-time setup: install hooks, create config
ccmc iterm-install                   → install iTerm status bar script
ccmc daemon start                    → manually start daemon
ccmc daemon stop                     → manually stop daemon
ccmc daemon status                   → daemon health check
```

---

## Dashboard Keyboard Map

```
NAVIGATION
  ↑/↓ or j/k        Navigate session list
  Tab                Cycle right panel: inspector → inventory → reference
  Enter              Expand selected session in inspector
  Esc                Close overlay / return to session list

ACTIONS
  l                  Launch new session (prompts for directory)
  k                  Kill selected session (confirms first)
  r                  Refresh session list

PANELS
  i                  Switch right panel to inventory
  ?  or  /           Open reference search overlay
  o                  Open selected session's project in a new iTerm tab

REFERENCE SEARCH (when overlay open)
  Type to search     Fuzzy match across all categories
  ↑/↓               Navigate results
  Enter              View detail
  Esc                Close overlay

GENERAL
  q                  Quit dashboard
  d                  Toggle daemon status display
  h                  Show keyboard help
```

---

## Initialization Checklist

- [ ] Clone repo and run `go mod download`
- [ ] Copy `.env.example` → `.env` and add `ANTHROPIC_API_KEY` (only needed for eval feature)
- [ ] Run `make build` to produce the `ccmc` binary
- [ ] Run `ccmc setup` to install global hooks into `~/.claude/settings.json`
- [ ] Run `ccmc` to verify dashboard launches and discovers existing sessions
- [ ] (Optional) Run `ccmc iterm-install` to set up iTerm status bar integration
- [ ] (Optional) Configure `~/.ccmc/config.yaml` to customize daemon behavior
