package tui

// inventory.go — right-panel inventory view (task 49).
// Reads all five Claude component categories from the filesystem via the
// inventory scanner and parsers. Never calls the daemon.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"ccmc/internal/config"
	"ccmc/internal/inventory"
	"ccmc/pkg/ccmc"
)

// inventoryLoadedMsg carries the result of a completed inventory scan back into Update.
type inventoryLoadedMsg struct {
	mcps     []ccmc.MCPEntry
	skills   []ccmc.SkillEntry
	commands []ccmc.CommandEntry
	agents   []ccmc.AgentEntry
	plugins  []ccmc.PluginEntry
	err      error
}

// inventoryLoadFunc is a test seam: tests swap this to return canned results
// without touching the filesystem.
var inventoryLoadFunc = defaultInventoryLoad

// defaultInventoryLoad is the real implementation used in production.
func defaultInventoryLoad() inventoryLoadedMsg {
	claudeDir := config.ClaudeDir()
	raw, err := inventory.NewScanner(claudeDir).Scan()
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory scan: %w", err)}
	}

	mcps, err := inventory.ParseMCPs(raw)
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory parse MCPs: %w", err)}
	}
	skills, err := inventory.ParseSkills(raw)
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory parse skills: %w", err)}
	}
	commands, err := inventory.ParseCommands(raw)
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory parse commands: %w", err)}
	}
	agents, err := inventory.ParseAgents(raw)
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory parse agents: %w", err)}
	}
	plugins, err := inventory.ParsePlugins(raw)
	if err != nil {
		return inventoryLoadedMsg{err: fmt.Errorf("inventory parse plugins: %w", err)}
	}

	return inventoryLoadedMsg{
		mcps:     mcps,
		skills:   skills,
		commands: commands,
		agents:   agents,
		plugins:  plugins,
	}
}

// inventoryModel implements InventoryPanel.
type inventoryModel struct {
	focused  bool
	loading  bool
	loaded   bool // true after the first successful (or error) load
	loadErr  error
	mcps     []ccmc.MCPEntry
	skills   []ccmc.SkillEntry
	commands []ccmc.CommandEntry
	agents   []ccmc.AgentEntry
	plugins  []ccmc.PluginEntry
	viewport viewport.Model
	width    int
	height   int
	ready    bool // viewport initialised with dimensions
}

// NewInventoryPanel constructs an inventory panel with no external dependencies.
// Inventory is resolved from the filesystem via inventory.NewScanner.
func NewInventoryPanel() InventoryPanel {
	return &inventoryModel{}
}

func (m *inventoryModel) Focus() { m.focused = true }
func (m *inventoryModel) Blur()  { m.focused = false }

func (m *inventoryModel) Update(msg tea.Msg) (InventoryPanel, tea.Cmd) {
	switch msg := msg.(type) {

	case paneSizeMsg:
		m.width = msg.w
		m.height = msg.h
		if !m.ready {
			m.viewport = viewport.New(msg.w, msg.h)
			m.ready = true
		} else {
			m.viewport.Width = msg.w
			m.viewport.Height = msg.h
		}
		m.viewport.SetContent(m.buildContent())
		// Trigger the initial load the first time we have dimensions.
		if !m.loaded && !m.loading {
			m.loading = true
			return m, m.runLoad()
		}
		return m, nil

	case inventoryLoadedMsg:
		m.loading = false
		m.loaded = true
		if msg.err != nil {
			m.loadErr = msg.err
		} else {
			m.loadErr = nil
			m.mcps = msg.mcps
			m.skills = msg.skills
			m.commands = msg.commands
			m.agents = msg.agents
			m.plugins = msg.plugins
		}
		m.viewport.SetContent(m.buildContent())
		m.viewport.GotoTop()
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		switch msg.String() {
		case "r":
			// Manual refresh — re-runs the full scan.
			m.loading = true
			m.loadErr = nil
			return m, m.runLoad()
		}
		// Forward all other keys (scroll) to the viewport.
		if m.ready {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	// Let viewport handle its own messages (e.g. mouse wheel).
	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// runLoad returns a Cmd that runs the inventory load off the UI goroutine.
func (m *inventoryModel) runLoad() tea.Cmd {
	return func() tea.Msg {
		return inventoryLoadFunc()
	}
}

func (m *inventoryModel) View() string {
	if !m.ready {
		return Muted.Render("(loading inventory…)")
	}
	return m.viewport.View()
}

// buildContent assembles the inventory into the viewport content string.
// Five categories: MCPs, Skills, Commands, Agents, Plugins — each grouped by scope.
func (m *inventoryModel) buildContent() string {
	var sb strings.Builder

	sb.WriteString(Title.Render("Inventory"))
	sb.WriteString("\n\n")

	if m.loading {
		sb.WriteString(Muted.Render("loading…"))
		return sb.String()
	}

	if m.loadErr != nil {
		sb.WriteString(Muted.Render(fmt.Sprintf("error: %s", m.loadErr.Error())))
		return sb.String()
	}

	if !m.loaded {
		sb.WriteString(Muted.Render("(press r to load)"))
		return sb.String()
	}

	// ── MCPs ──────────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("MCP Servers"))
	sb.WriteString("\n")
	if len(m.mcps) == 0 {
		sb.WriteString(Muted.Render("  (none)"))
	} else {
		for _, e := range m.mcps {
			sb.WriteString(renderMCPEntry(e))
		}
	}
	sb.WriteString("\n\n")

	// ── Skills ────────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Skills"))
	sb.WriteString("\n")
	if len(m.skills) == 0 {
		sb.WriteString(Muted.Render("  (none)"))
	} else {
		for _, e := range m.skills {
			sb.WriteString(renderSkillEntry(e))
		}
	}
	sb.WriteString("\n\n")

	// ── Commands ──────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Commands"))
	sb.WriteString("\n")
	if len(m.commands) == 0 {
		sb.WriteString(Muted.Render("  (none)"))
	} else {
		for _, e := range m.commands {
			sb.WriteString(renderCommandEntry(e))
		}
	}
	sb.WriteString("\n\n")

	// ── Agents ────────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Agents"))
	sb.WriteString("\n")
	if len(m.agents) == 0 {
		sb.WriteString(Muted.Render("  (none)"))
	} else {
		for _, e := range m.agents {
			sb.WriteString(renderAgentEntry(e))
		}
	}
	sb.WriteString("\n\n")

	// ── Plugins ───────────────────────────────────────────────────────────────
	sb.WriteString(Title.Render("Plugins"))
	sb.WriteString("\n")
	if len(m.plugins) == 0 {
		sb.WriteString(Muted.Render("  (none)"))
	} else {
		for _, e := range m.plugins {
			sb.WriteString(renderPluginEntry(e))
		}
	}
	sb.WriteString("\n")

	return sb.String()
}

func renderMCPEntry(e ccmc.MCPEntry) string {
	scope := scopeLabel(e.Scope)
	detail := e.Type
	if e.Command != "" {
		detail += "  " + e.Command
	} else if e.URL != "" {
		detail += "  " + e.URL
	}
	return fmt.Sprintf("  %s  %s  %s\n",
		e.Name,
		Subtitle.Render(scope),
		Muted.Render(detail),
	)
}

func renderSkillEntry(e ccmc.SkillEntry) string {
	scope := scopeLabel(e.Scope)
	desc := e.Description
	if desc == "" {
		desc = "—"
	}
	return fmt.Sprintf("  %s  %s  %s\n",
		e.Name,
		Subtitle.Render(scope),
		Muted.Render(desc),
	)
}

func renderCommandEntry(e ccmc.CommandEntry) string {
	scope := scopeLabel(e.Scope)
	desc := e.Description
	if desc == "" {
		desc = "—"
	}
	return fmt.Sprintf("  %s  %s  %s\n",
		e.Name,
		Subtitle.Render(scope),
		Muted.Render(desc),
	)
}

func renderAgentEntry(e ccmc.AgentEntry) string {
	scope := scopeLabel(e.Scope)
	desc := e.Description
	if desc == "" {
		desc = "—"
	}
	return fmt.Sprintf("  %s  %s  %s\n",
		e.Name,
		Subtitle.Render(scope),
		Muted.Render(desc),
	)
}

func renderPluginEntry(e ccmc.PluginEntry) string {
	scope := scopeLabel(e.Scope)
	var parts []string
	if len(e.Skills) > 0 {
		parts = append(parts, fmt.Sprintf("skills:%d", len(e.Skills)))
	}
	if len(e.Agents) > 0 {
		parts = append(parts, fmt.Sprintf("agents:%d", len(e.Agents)))
	}
	if len(e.MCPs) > 0 {
		parts = append(parts, fmt.Sprintf("mcps:%d", len(e.MCPs)))
	}
	detail := strings.Join(parts, "  ")
	if detail == "" {
		detail = "—"
	}
	return fmt.Sprintf("  %s  %s  %s\n",
		e.Name,
		Subtitle.Render(scope),
		Muted.Render(detail),
	)
}

// scopeLabel converts a scope string to a short display label.
// "global" stays as-is; an absolute project path is truncated to its basename.
func scopeLabel(scope string) string {
	if scope == "global" || scope == "" {
		return "global"
	}
	// Project scope is an encoded path; show only the last segment for readability.
	parts := strings.Split(strings.TrimRight(scope, "/"), "/")
	return parts[len(parts)-1]
}
