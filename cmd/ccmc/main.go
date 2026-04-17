package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"ccmc/internal/daemon"
	"ccmc/internal/inspector"
)

// version is injected at build time via ldflags.
var version = "dev"

const helpText = `ccmc — Claude Code Mission Control

Usage: ccmc [command]

Commands:
  (no args)              Launch the dashboard (TUI)
  ls                     List all tracked sessions (table format)
  inspect <session-id>   Detailed session inspection
  kill <session-id>      Kill a CC session
  launch <directory>     Launch new CC session (iTerm tab or subprocess)

  ref <query>            Fuzzy search across all reference categories
  ref commands           List all built-in commands
  ref hooks              List all hook events
  ref hooks <name>       Detail on a specific hook event
  ref flags              List all CLI flags
  ref shortcuts          List all keyboard shortcuts
  ref tools              List all tool names
  ref frontmatter        List all frontmatter fields
  ref env                List all environment variables
  ref files              Show ~/.claude/ directory map

  inventory              Full inventory of your CC setup
  inventory mcps         Just MCP servers
  inventory skills       Just skills and custom commands
  inventory agents       Just subagents
  inventory plugins      Just plugins
  inventory hooks        Just hook configurations

  eval <github-url>      Evaluate a tool/MCP for fit
  install <github-url>   Install and wire up a tool/MCP
  tools ls               List installed tools
  tools rm <name>        Remove installed tool
  tools update [name]    Update installed tools

  setup                  One-time setup: install hooks, create config
  iterm-install          Install iTerm status bar script
  daemon start           Start the background daemon
  daemon stop            Stop the background daemon
  daemon status          Show daemon status

  version                Print version
  help                   Print this help

Flags:
  --help, -h             Print this help
  --version, -v          Print version
`

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		// No args: launch dashboard (TUI) — wired in Phase 4
		fmt.Fprintln(os.Stderr, "ccmc: dashboard not yet implemented")
		os.Exit(2)
	}

	cmd := args[0]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("ccmc", version)

	case "help", "--help", "-h":
		fmt.Print(helpText)

	case "ls":
		runLs()
	case "inspect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "ccmc inspect: missing session-id\nUsage: ccmc inspect <session-id>")
			os.Exit(2)
		}
		runInspect(args[1])
	case "kill":
		notImplemented("kill")
	case "launch":
		notImplemented("launch")
	case "ref":
		notImplemented("ref")
	case "inventory":
		notImplemented("inventory")
	case "eval":
		notImplemented("eval")
	case "install":
		notImplemented("install")
	case "tools":
		notImplemented("tools")
	case "setup":
		notImplemented("setup")
	case "iterm-install":
		notImplemented("iterm-install")
	case "daemon":
		notImplemented("daemon")

	default:
		fmt.Fprintf(os.Stderr, "ccmc: unknown command %q\nRun 'ccmc help' for usage.\n", cmd)
		os.Exit(2)
	}
}

func runLs() {
	sessions, err := daemon.ScanSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc ls: %v\n", err)
		os.Exit(1)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	// Sort by last activity, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSESSION\tLAST ACTIVITY\tSIZE")
	fmt.Fprintln(w, "-------\t-------\t-------------\t----")

	for _, s := range sessions {
		age := formatAge(s.LastActivity)
		size := formatSize(s.ContextEstimate)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ProjectName, truncate(s.ID, 12), age, size)
	}
	w.Flush()
}

func runInspect(sessionID string) {
	// 1. Locate JSONL via scanner
	jsonlPath, projectPath, err := daemon.FindSessionJSONL(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc inspect: %v\n", err)
		os.Exit(2)
	}
	if jsonlPath == "" {
		fmt.Fprintf(os.Stderr, "ccmc inspect: session %q not found\n", sessionID)
		os.Exit(1)
	}

	// 2. Aggregate session from JSONL
	f, err := os.Open(jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc inspect: open transcript: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()

	info, statErr := f.Stat()
	var contextBytes int64
	if statErr == nil {
		contextBytes = info.Size()
	}

	view, err := inspector.AggregateSession(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc inspect: aggregate: %v\n", err)
		os.Exit(2)
	}

	// 3. Read memory summary
	memorySummary, err := inspector.ReadMemorySummaryForSession(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc inspect: memory: %v\n", err)
		os.Exit(2)
	}

	// 4. Read todos
	todos, err := inspector.ReadTodos(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccmc inspect: todos: %v\n", err)
		os.Exit(2)
	}

	// 5. Print report
	printInspectReport(sessionID, projectPath, view, todos, memorySummary, contextBytes)
}

func printInspectReport(
	sessionID string,
	projectPath string,
	view *inspector.SessionView,
	todos []inspector.Todo,
	memorySummary string,
	contextBytes int64,
) {
	sep := "────────────────────────────────────────"

	// --- Header ---
	fmt.Println(sep)
	fmt.Printf("SESSION  %s\n", sessionID)
	fmt.Printf("PROJECT  %s\n", projectPath)
	if !view.StartedAt.IsZero() {
		fmt.Printf("STARTED  %s\n", view.StartedAt.Format(time.RFC3339))
		if !view.EndedAt.IsZero() && view.EndedAt.After(view.StartedAt) {
			dur := view.EndedAt.Sub(view.StartedAt).Round(time.Second)
			fmt.Printf("DURATION %s\n", dur)
		}
	}
	fmt.Println(sep)

	// --- Agents ---
	fmt.Println("\nAGENTS")
	if len(view.Agents) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, a := range view.Agents {
			fmt.Printf("  %s\n", a)
		}
	}

	// --- Recent Tool Calls ---
	fmt.Printf("\nRECENT TOOL CALLS (last %d)\n", len(view.RecentToolCalls))
	if len(view.RecentToolCalls) == 0 {
		fmt.Println("  (none)")
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, tc := range view.RecentToolCalls {
			ts := ""
			if !tc.Timestamp.IsZero() {
				ts = tc.Timestamp.Format("15:04:05")
			}
			target := tc.Target
			if len(target) > 60 {
				target = target[:60] + "..."
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", ts, tc.Tool, target)
		}
		w.Flush()
	}

	// --- Files ---
	fmt.Println("\nFILES")
	if len(view.FilesRead) > 0 {
		fmt.Println("  Read:")
		for _, f := range view.FilesRead {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(view.FilesModified) > 0 {
		fmt.Println("  Modified:")
		for _, f := range view.FilesModified {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(view.FilesRead) == 0 && len(view.FilesModified) == 0 {
		fmt.Println("  (none)")
	}

	// --- Todos ---
	fmt.Println("\nTODOS")
	if len(todos) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, t := range todos {
			marker := "[ ]"
			switch t.Status {
			case "completed":
				marker = "[x]"
			case "in_progress":
				marker = "[~]"
			}
			fmt.Printf("  %s %s\n", marker, t.Title)
		}
	}

	// --- Memory Summary ---
	fmt.Println("\nMEMORY SUMMARY")
	if memorySummary == "" {
		fmt.Println("  (none)")
	} else {
		// Indent each line of the summary
		for _, line := range splitLines(memorySummary) {
			fmt.Printf("  %s\n", line)
		}
	}

	// --- Context Estimate ---
	fmt.Println("\nCONTEXT ESTIMATE")
	fmt.Printf("  JSONL size: %s (%d bytes)\n", formatSize(contextBytes), contextBytes)
	fmt.Println(sep)
}

// splitLines splits s on newlines, preserving empty lines except a trailing one.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func notImplemented(cmd string) {
	fmt.Fprintf(os.Stderr, "ccmc %s: not yet implemented\n", cmd)
	os.Exit(2)
}
