package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"ccmc/internal/daemon"
	"ccmc/internal/inspector"
	"ccmc/internal/reference"
	"ccmc/pkg/ccmc"
)

// version is injected at build time via ldflags.
var version = "dev"

const helpText = `ccmc — Claude Code Mission Control

Usage: ccmc [command]

Commands:
  (no args)              Launch the dashboard (TUI)
  ls                     List all tracked sessions (table format)
  ls --no-daemon         List sessions using filesystem scan only
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
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

// knownCategories maps the string representation of each RefCategory to its
// typed constant. Used to distinguish category args from free-text queries.
var knownCategories = map[string]ccmc.RefCategory{
	"commands":    ccmc.RefCommands,
	"skills":      ccmc.RefSkills,
	"flags":       ccmc.RefFlags,
	"shortcuts":   ccmc.RefShortcuts,
	"hooks":       ccmc.RefHooks,
	"tools":       ccmc.RefTools,
	"frontmatter": ccmc.RefFrontmatter,
	"envvars":     ccmc.RefEnvVars,
	"filepaths":   ccmc.RefFilePaths,
}

// runRef handles the three invocation shapes for "ccmc ref":
//
//	ccmc ref <query>              — fuzzy search across all categories, top 10
//	ccmc ref <category>           — list all entries in the named category
//	ccmc ref <category> <name>    — full detail for the top fuzzy match within the category
//
// It writes output to out and errors to errOut. Returns a Unix exit code.
func runRef(args []string, out io.Writer, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(errOut, "ccmc ref: missing argument\nUsage: ccmc ref <query|category> [name]\n")
		return 2
	}

	entries, err := reference.LoadAll()
	if err != nil {
		fmt.Fprintf(errOut, "ccmc ref: load reference data: %v\n", err)
		return 1
	}
	eng := reference.NewEngine(entries)

	arg0 := strings.ToLower(strings.TrimSpace(args[0]))
	cat, isCategory := knownCategories[arg0]

	switch {
	case isCategory && len(args) >= 2:
		// Shape 3: ccmc ref <category> <name> — full detail, top fuzzy hit within category
		name := strings.Join(args[1:], " ")
		results := eng.Search(name, &cat, 1)
		if len(results) == 0 {
			fmt.Fprintf(errOut, "ccmc ref: no match for %q in category %q\n", name, arg0)
			return 1
		}
		printRefDetail(out, results[0])

	case isCategory:
		// Shape 2: ccmc ref <category> — list all entries in the category
		results := eng.Search("", &cat, 0)
		if len(results) == 0 {
			fmt.Fprintf(out, "No entries found for category %q.\n", arg0)
			return 0
		}
		printRefList(out, results)

	default:
		// Shape 1: ccmc ref <query> — fuzzy search across all categories, top 10
		query := strings.Join(args, " ")
		results := eng.Search(query, nil, 10)
		if len(results) == 0 {
			fmt.Fprintf(out, "No results for %q.\n", query)
			return 0
		}
		printRefList(out, results)
	}

	return 0
}

// printRefList prints a compact tabular listing of ref entries.
func printRefList(out io.Writer, entries []ccmc.RefEntry) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tDESCRIPTION")
	fmt.Fprintln(w, "----\t--------\t-----------")
	for _, e := range entries {
		desc := e.Description
		if len(desc) > 72 {
			desc = desc[:72] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, string(e.Category), desc)
	}
	w.Flush()
}

// printRefDetail prints all populated fields of a single RefEntry.
func printRefDetail(out io.Writer, e ccmc.RefEntry) {
	sep := "────────────────────────────────────────"
	fmt.Fprintln(out, sep)
	fmt.Fprintf(out, "NAME      %s\n", e.Name)
	fmt.Fprintf(out, "CATEGORY  %s\n", string(e.Category))
	if e.Description != "" {
		fmt.Fprintf(out, "DESC      %s\n", e.Description)
	}
	if e.Usage != "" {
		fmt.Fprintln(out, sep)
		fmt.Fprintf(out, "USAGE\n  %s\n", e.Usage)
	}
	if len(e.Examples) > 0 {
		fmt.Fprintln(out, sep)
		fmt.Fprintln(out, "EXAMPLES")
		for _, ex := range e.Examples {
			fmt.Fprintf(out, "  %s\n", ex)
		}
	}
	if len(e.Related) > 0 {
		fmt.Fprintln(out, sep)
		fmt.Fprintln(out, "RELATED")
		for _, r := range e.Related {
			fmt.Fprintf(out, "  %s\n", r)
		}
	}
	if len(e.Gotchas) > 0 {
		fmt.Fprintln(out, sep)
		fmt.Fprintln(out, "GOTCHAS")
		for _, g := range e.Gotchas {
			fmt.Fprintf(out, "  %s\n", g)
		}
	}
	if e.Detail != "" {
		fmt.Fprintln(out, sep)
		fmt.Fprintln(out, "DETAIL")
		for _, line := range splitLines(e.Detail) {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	fmt.Fprintln(out, sep)
}
