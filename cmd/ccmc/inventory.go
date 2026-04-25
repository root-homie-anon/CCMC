package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"ccmc/internal/config"
	"ccmc/internal/inventory"
	"ccmc/pkg/ccmc"
)

// runInventory handles "ccmc inventory [mcps|skills|agents|plugins|commands|hooks]".
// One scanner construction; one Scan() call; results dispatched to category renderers.
func runInventory(args []string, stdout, stderr io.Writer) int {
	raw, err := inventory.NewScanner(config.ClaudeDir()).Scan()
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: scan failed: %v\n", err)
		return 1
	}

	if len(args) == 0 {
		return renderAll(raw, stdout, stderr)
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "mcps":
		return renderMCPsOnly(raw, stdout, stderr)
	case "skills":
		return renderSkillsOnly(raw, stdout, stderr)
	case "agents":
		return renderAgentsOnly(raw, stdout, stderr)
	case "plugins":
		return renderPluginsOnly(raw, stdout, stderr)
	case "commands":
		return renderCommandsOnly(raw, stdout, stderr)
	case "hooks":
		return renderHooksOnly(raw, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ccmc inventory: unknown subcommand %q (mcps|skills|agents|plugins|commands|hooks)\n", args[0])
		return 1
	}
}

// ── all-categories view ────────────────────────────────────────────────────────

// renderAll prints every category grouped by scope.
func renderAll(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	mcps, err := inventory.ParseMCPs(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: parse MCPs: %v\n", err)
		return 1
	}
	skills, err := inventory.ParseSkills(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: parse skills: %v\n", err)
		return 1
	}
	commands, err := inventory.ParseCommands(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: parse commands: %v\n", err)
		return 1
	}
	agents, err := inventory.ParseAgents(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: parse agents: %v\n", err)
		return 1
	}
	plugins, err := inventory.ParsePlugins(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory: parse plugins: %v\n", err)
		return 1
	}
	hooks := parseHooksAllScopes(raw)

	// Collect ordered scope keys: global first, then project paths ascending.
	scopes := scopeOrder(raw)

	for i, scope := range scopes {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)

		fmt.Fprintln(stdout, "  MCPs:")
		renderMCPs(stdout, scope, filterMCPs(mcps, scope))

		fmt.Fprintln(stdout, "  Skills:")
		renderSkills(stdout, scope, filterSkills(skills, scope))

		fmt.Fprintln(stdout, "  Commands:")
		renderCommands(stdout, scope, filterCommands(commands, scope))

		fmt.Fprintln(stdout, "  Agents:")
		renderAgents(stdout, scope, filterAgents(agents, scope))

		fmt.Fprintln(stdout, "  Plugins:")
		renderPlugins(stdout, scope, filterPlugins(plugins, scope))

		fmt.Fprintln(stdout, "  Hooks:")
		renderHooks(stdout, scope, hooks[scope])
	}
	return 0
}

// ── single-category views ──────────────────────────────────────────────────────

func renderMCPsOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	entries, err := inventory.ParseMCPs(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory mcps: %v\n", err)
		return 1
	}
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderMCPs(stdout, scope, filterMCPs(entries, scope))
	}
	return 0
}

func renderSkillsOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	entries, err := inventory.ParseSkills(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory skills: %v\n", err)
		return 1
	}
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderSkills(stdout, scope, filterSkills(entries, scope))
	}
	return 0
}

func renderAgentsOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	entries, err := inventory.ParseAgents(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory agents: %v\n", err)
		return 1
	}
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderAgents(stdout, scope, filterAgents(entries, scope))
	}
	return 0
}

func renderPluginsOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	entries, err := inventory.ParsePlugins(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory plugins: %v\n", err)
		return 1
	}
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderPlugins(stdout, scope, filterPlugins(entries, scope))
	}
	return 0
}

func renderCommandsOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	entries, err := inventory.ParseCommands(raw)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc inventory commands: %v\n", err)
		return 1
	}
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderCommands(stdout, scope, filterCommands(entries, scope))
	}
	return 0
}

func renderHooksOnly(raw ccmc.InventoryRaw, stdout, stderr io.Writer) int {
	hooks := parseHooksAllScopes(raw)
	for i, scope := range scopeOrder(raw) {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printScopeHeader(stdout, scope)
		renderHooks(stdout, scope, hooks[scope])
	}
	return 0
}

// ── scope helpers ──────────────────────────────────────────────────────────────

// scopeOrder returns scope identifiers in render order: "global" first, then
// project paths ascending. Mirrors the order guaranteed by scanner.go.
func scopeOrder(raw ccmc.InventoryRaw) []string {
	scopes := make([]string, 0, 1+len(raw.Projects))
	scopes = append(scopes, "global")
	for _, p := range raw.Projects {
		scopes = append(scopes, p.ProjectPath)
	}
	return scopes
}

// printScopeHeader writes the section header for one scope.
func printScopeHeader(w io.Writer, scope string) {
	if scope == "global" {
		fmt.Fprintln(w, "Global scope")
	} else {
		fmt.Fprintf(w, "Project: %s\n", scope)
	}
}

// ── hooks extraction ───────────────────────────────────────────────────────────

// parseHooksAllScopes reads every settings.json referenced in raw and extracts
// hook event names (keys under the top-level "hooks" JSON object). Returns a
// map from scope string to sorted event name slice. Missing or malformed files
// yield nil slices; errors are logged as warnings but never propagate.
func parseHooksAllScopes(raw ccmc.InventoryRaw) map[string][]string {
	result := make(map[string][]string)

	if raw.Global.SettingsPath != "" {
		result["global"] = extractHookEvents(raw.Global.SettingsPath)
	}
	for _, proj := range raw.Projects {
		if proj.SettingsPath != "" {
			result[proj.ProjectPath] = extractHookEvents(proj.SettingsPath)
		}
	}
	return result
}

// extractHookEvents reads one settings.json and returns sorted hook event names
// from the "hooks" object. Returns nil when hooks are absent or the file is unreadable.
func extractHookEvents(settingsPath string) []string {
	m, err := config.ReadSettings(settingsPath)
	if err != nil {
		// Non-fatal: caller collects as empty.
		return nil
	}
	raw, ok := m["hooks"]
	if !ok {
		return nil
	}
	var hooksObj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &hooksObj); err != nil {
		return nil
	}
	if len(hooksObj) == 0 {
		return nil
	}
	events := make([]string, 0, len(hooksObj))
	for k := range hooksObj {
		events = append(events, k)
	}
	sort.Strings(events)
	return events
}

// ── per-category filter helpers ────────────────────────────────────────────────

func filterMCPs(entries []ccmc.MCPEntry, scope string) []ccmc.MCPEntry {
	var out []ccmc.MCPEntry
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

func filterSkills(entries []ccmc.SkillEntry, scope string) []ccmc.SkillEntry {
	var out []ccmc.SkillEntry
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

func filterCommands(entries []ccmc.CommandEntry, scope string) []ccmc.CommandEntry {
	var out []ccmc.CommandEntry
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

func filterAgents(entries []ccmc.AgentEntry, scope string) []ccmc.AgentEntry {
	var out []ccmc.AgentEntry
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

func filterPlugins(entries []ccmc.PluginEntry, scope string) []ccmc.PluginEntry {
	var out []ccmc.PluginEntry
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

// ── per-category render helpers ────────────────────────────────────────────────

func renderMCPs(w io.Writer, _ string, entries []ccmc.MCPEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		switch e.Type {
		case "stdio":
			cmdStr := e.Command
			if len(e.Args) > 0 {
				cmdStr += " " + strings.Join(e.Args, " ")
			}
			fmt.Fprintf(tw, "    %s\ttype=stdio\tcommand=%s\n", e.Name, cmdStr)
		case "sse":
			fmt.Fprintf(tw, "    %s\ttype=sse\turl=%s\n", e.Name, e.URL)
		default:
			fmt.Fprintf(tw, "    %s\ttype=%s\t\n", e.Name, e.Type)
		}
	}
	tw.Flush()
}

func renderSkills(w io.Writer, _ string, entries []ccmc.SkillEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		invocable := ""
		if e.UserInvocable {
			invocable = "user-invocable"
		}
		desc := truncate(e.Description, 60)
		fmt.Fprintf(tw, "    %s\t%s\tdesc=%s\n", e.Name, invocable, desc)
	}
	tw.Flush()
}

func renderCommands(w io.Writer, _ string, entries []ccmc.CommandEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		desc := truncate(e.Description, 60)
		fmt.Fprintf(tw, "    %s\tdesc=%s\n", e.Name, desc)
	}
	tw.Flush()
}

func renderAgents(w io.Writer, _ string, entries []ccmc.AgentEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		model := e.Model
		if model == "" {
			model = "default"
		}
		desc := truncate(e.Description, 60)
		fmt.Fprintf(tw, "    %s\tmodel=%s\tdesc=%s\n", e.Name, model, desc)
	}
	tw.Flush()
}

func renderPlugins(w io.Writer, _ string, entries []ccmc.PluginEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		parts := []string{}
		if len(e.Skills) > 0 {
			parts = append(parts, "skills="+strings.Join(e.Skills, ","))
		}
		if len(e.Agents) > 0 {
			parts = append(parts, "agents="+strings.Join(e.Agents, ","))
		}
		if len(e.Hooks) > 0 {
			parts = append(parts, "hooks="+strings.Join(e.Hooks, ","))
		}
		if len(e.MCPs) > 0 {
			parts = append(parts, "mcps="+strings.Join(e.MCPs, ","))
		}
		detail := strings.Join(parts, "  ")
		if detail == "" {
			detail = filepath.Base(e.Path)
		}
		fmt.Fprintf(tw, "    %s\t%s\n", e.Name, detail)
	}
	tw.Flush()
}

func renderHooks(w io.Writer, _ string, events []string) {
	if len(events) == 0 {
		fmt.Fprintln(w, "    (no entries)")
		return
	}
	for _, ev := range events {
		fmt.Fprintf(w, "    %s\n", ev)
	}
}
