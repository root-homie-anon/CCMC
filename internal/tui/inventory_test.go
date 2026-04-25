package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ccmc/pkg/ccmc"
)

// swapInventoryLoadFunc replaces inventoryLoadFunc for the duration of a test
// and restores the original on cleanup.
func swapInventoryLoadFunc(t *testing.T, fn func() inventoryLoadedMsg) {
	t.Helper()
	orig := inventoryLoadFunc
	inventoryLoadFunc = fn
	t.Cleanup(func() { inventoryLoadFunc = orig })
}

// makeInventory constructs an InventoryPanel with a known terminal size so the
// internal viewport is initialised and the initial load Cmd is returned.
// It does NOT execute the load Cmd — callers that need loaded content should
// call runInventoryLoad.
func makeInventory() (InventoryPanel, tea.Cmd) {
	p := NewInventoryPanel()
	p, cmd := p.Update(paneSizeMsg{w: 100, h: 40})
	return p, cmd
}

// runInventoryLoad executes the Cmd returned by the panel (the load Cmd) and
// feeds the resulting msg back in, then returns the updated panel and its View.
func runInventoryLoad(p InventoryPanel, cmd tea.Cmd) (InventoryPanel, string) {
	if cmd != nil {
		msg := cmd()
		p, _ = p.Update(msg)
	}
	return p, p.View()
}

// oneOfEach returns an inventoryLoadedMsg with one entry in every category.
func oneOfEach() inventoryLoadedMsg {
	return inventoryLoadedMsg{
		mcps: []ccmc.MCPEntry{
			{Name: "mcp-test", Scope: "global", Type: "stdio"},
		},
		skills: []ccmc.SkillEntry{
			{Name: "skill-test", Scope: "global", Description: "a skill"},
		},
		commands: []ccmc.CommandEntry{
			{Name: "cmd-test", Scope: "global", Description: "a command"},
		},
		agents: []ccmc.AgentEntry{
			{Name: "agent-test", Scope: "global", Description: "an agent"},
		},
		plugins: []ccmc.PluginEntry{
			{Name: "plugin-test", Scope: "global"},
		},
	}
}

// TestInventory_LoadsOnFocus verifies that after paneSizeMsg the panel fires a
// load Cmd and, after execution, View reflects the loaded entries.
func TestInventory_LoadsOnFocus(t *testing.T) {
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg { return oneOfEach() })

	p, cmd := makeInventory()
	if cmd == nil {
		t.Fatal("expected a non-nil load Cmd after first paneSizeMsg")
	}
	p, view := runInventoryLoad(p, cmd)
	_ = p

	if !strings.Contains(view, "mcp-test") {
		t.Errorf("View() missing loaded MCP entry 'mcp-test'\nView:\n%s", view)
	}
}

// TestInventory_RendersAllCategories asserts that all five category headers are
// present after a load that returns one entry of each type.
func TestInventory_RendersAllCategories(t *testing.T) {
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg { return oneOfEach() })

	p, cmd := makeInventory()
	_, view := runInventoryLoad(p, cmd)

	headers := []string{"MCP Servers", "Skills", "Commands", "Agents", "Plugins"}
	for _, h := range headers {
		if !strings.Contains(view, h) {
			t.Errorf("View() missing category header %q\nView:\n%s", h, view)
		}
	}
}

// TestInventory_ScrollsWithViewport feeds a down-arrow key after loading long
// content and asserts the viewport's YOffset advances.
func TestInventory_ScrollsWithViewport(t *testing.T) {
	// Produce enough content to require scrolling inside a 100×10 viewport.
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg {
		msg := inventoryLoadedMsg{}
		for i := 0; i < 30; i++ {
			msg.skills = append(msg.skills, ccmc.SkillEntry{
				Name:  fmt.Sprintf("skill-%02d", i),
				Scope: "global",
			})
		}
		return msg
	})

	p, cmd := NewInventoryPanel(), tea.Cmd(nil)
	p, cmd = p.Update(paneSizeMsg{w: 100, h: 10})
	p, _ = runInventoryLoad(p, cmd)
	p.Focus()

	// Capture the viewport offset before scrolling.
	m := p.(*inventoryModel)
	beforeOffset := m.viewport.YOffset

	// Feed a down key.
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = p.(*inventoryModel)

	if m.viewport.YOffset <= beforeOffset {
		t.Errorf("viewport YOffset did not advance after ↓: before=%d after=%d",
			beforeOffset, m.viewport.YOffset)
	}
}

// TestInventory_RefreshKeyReruns verifies that pressing 'r' while focused
// triggers a second call to the load function.
func TestInventory_RefreshKeyReruns(t *testing.T) {
	calls := 0
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg {
		calls++
		return oneOfEach()
	})

	p, cmd := makeInventory()
	p, _ = runInventoryLoad(p, cmd) // first load
	if calls != 1 {
		t.Fatalf("expected 1 load call after initial paneSizeMsg, got %d", calls)
	}

	p.Focus()
	// Press 'r' — must be focused.
	var rCmd tea.Cmd
	p, rCmd = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if rCmd == nil {
		t.Fatal("expected a non-nil Cmd after pressing 'r'")
	}
	rCmd() // execute load (calls the load func)
	if calls != 2 {
		t.Fatalf("expected 2 load calls after 'r' refresh, got %d", calls)
	}
}

// TestInventory_ErrorRendersInline stubs the load function to return an error
// and asserts the error message appears in View().
func TestInventory_ErrorRendersInline(t *testing.T) {
	swapInventoryLoadFunc(t, func() inventoryLoadedMsg {
		return inventoryLoadedMsg{err: errors.New("fs read failed")}
	})

	p, cmd := makeInventory()
	_, view := runInventoryLoad(p, cmd)

	if !strings.Contains(view, "fs read failed") {
		t.Errorf("View() missing error text 'fs read failed'\nView:\n%s", view)
	}
}
