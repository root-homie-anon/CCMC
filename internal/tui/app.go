package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ccmc/internal/reference"
	"ccmc/pkg/ccmc"
)

// pollInterval is how often App fires a daemon refresh. 500ms keeps the display
// feeling live without hammering the unix socket on slow machines.
const pollInterval = 500 * time.Millisecond

// daemonClient is the test-seam interface for the daemon API client.
// *ccmc.Client satisfies this interface implicitly; tests supply a stub.
// Keeping it narrow (only what App needs) avoids coupling to the full client API.
type daemonClient interface {
	ListSessions() ([]ccmc.Session, error)
	Status() (ccmc.DaemonStatus, error)
}

// panel is an enum identifying which panel currently holds keyboard focus.
type panel int

const (
	panelSessions   panel = iota // left column — session list
	panelInspector               // right column — session inspector
	panelInventory               // right column — inventory view (switched via 'i')
	panelReference               // overlay — reference search
	panelCommandBar              // bottom row
)

// tickMsg fires every pollInterval to trigger a daemon data refresh.
type tickMsg time.Time

// sessionsRefreshedMsg carries the result of a daemon poll back into Update.
// err is non-nil when the daemon was unreachable; the previous sessions slice is
// preserved in that case so the UI continues to render last-known-good state.
type sessionsRefreshedMsg struct {
	sessions []ccmc.Session
	status   ccmc.DaemonStatus
	err      error
}

// AppConfig bundles all App dependencies. Using a config struct rather than
// positional parameters makes it safe to extend (adding the reference engine
// here avoids breaking the existing tests that call NewApp directly).
type AppConfig struct {
	Client daemonClient
	Engine *reference.Engine // nil is safe — reference panel shows no results
}

// App is the top-level Bubble Tea model. It owns the terminal lifecycle,
// routes key events to the focused panel, and manages daemon polling.
type App struct {
	client        daemonClient
	sessions      []ccmc.Session
	daemonStatus  ccmc.DaemonStatus
	daemonErr     error // last poll error; nil when daemon is healthy
	focused       panel
	width         int
	height        int
	inventoryMode bool // true when 'i' has toggled right panel to inventory view

	// sub-models — each is an interface so tasks 33-36 and 47 can replace stubs
	sessionsModel  SessionsPanel
	inspectorModel InspectorPanel
	inventoryModel InventoryPanel
	referenceModel ReferencePanel
	commandBar     CommandBarPanel
}

// NewApp constructs an App backed by the supplied daemon client.
// The client is typically *ccmc.Client; tests pass a stub satisfying daemonClient.
// Engine may be nil; the reference overlay will render with no results.
func NewApp(client daemonClient) App {
	return NewAppWithConfig(AppConfig{Client: client})
}

// NewAppWithConfig constructs an App from a full config. Use this when wiring the
// reference engine from the entry-point (task 41).
func NewAppWithConfig(cfg AppConfig) App {
	return App{
		client:         cfg.Client,
		focused:        panelSessions,
		sessionsModel:  NewSessionsPanel(),
		inspectorModel: NewInspectorPanel(cfg.Client),
		inventoryModel: &stubInventory{},
		referenceModel: NewReferencePanel(cfg.Engine),
		commandBar:     NewCommandBarPanel(),
	}
}

// Init starts the polling ticker. Bubble Tea calls Init once before the first
// Render; returning tea.Every here means App.Update will receive a tickMsg
// every pollInterval for the lifetime of the program.
func (a App) Init() tea.Cmd {
	a.sessionsModel.Focus()
	return tea.Every(pollInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles all incoming messages. Key routing:
//   - Global keys (q, Ctrl+C, ?, /, Esc, Tab, i) are consumed here.
//   - All other keys are forwarded to the focused panel.
//   - tickMsg fires a daemon refresh command.
//   - sessionsRefreshedMsg merges new daemon data into App state.
//   - tea.WindowSizeMsg updates width/height for layout calculations.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		// Propagate dimensions to panels so they can truncate and layout correctly.
		return a, a.dispatchSizeMsg(msg.Width, msg.Height)

	case tickMsg:
		return a, a.fetchSessions()

	case sessionsRefreshedMsg:
		if msg.err != nil {
			// Daemon unavailable — keep last-known sessions, surface warning.
			if errors.Is(msg.err, ccmc.ErrDaemonUnavailable) {
				a.daemonErr = msg.err
			} else {
				a.daemonErr = msg.err
			}
			return a, nil
		}
		a.daemonErr = nil
		a.sessions = msg.sessions
		a.daemonStatus = msg.status
		// Push refreshed session list into the sessions panel.
		updated, cmd := a.sessionsModel.Update(msg)
		a.sessionsModel = updated
		return a, cmd

	case SessionSelectedMsg:
		// Route cursor-change notifications to the inspector regardless of focus.
		updated, cmd := a.inspectorModel.Update(msg)
		a.inspectorModel = updated
		return a, cmd

	case inspectorLoadedMsg:
		// Route completed aggregation result to the inspector.
		updated, cmd := a.inspectorModel.Update(msg)
		a.inspectorModel = updated
		return a, cmd

	case closeOverlayMsg:
		// Reference overlay requested its own close.
		a.referenceModel.Blur()
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
		return a, nil

	case tea.KeyMsg:
		switch msg.String() {

		case "q", "ctrl+c":
			return a, tea.Quit

		case "?", "/":
			// Open reference overlay regardless of current focus.
			a.blurAllPanels()
			a.focused = panelReference
			a.referenceModel.Focus()
			a.commandBar.Update(focusChangedMsg{active: panelReference, width: a.width})
			return a, nil

		case "esc":
			if a.focused == panelReference {
				a.referenceModel.Blur()
				a.focused = panelSessions
				a.sessionsModel.Focus()
				a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
			}
			return a, nil

		case "tab":
			// Cycle between sessions and inspector (inventory if inventoryMode is on).
			switch a.focused {
			case panelSessions:
				a.sessionsModel.Blur()
				if a.inventoryMode {
					a.focused = panelInventory
					a.inventoryModel.Focus()
					a.commandBar.Update(focusChangedMsg{active: panelInventory, width: a.width})
				} else {
					a.focused = panelInspector
					a.inspectorModel.Focus()
					a.commandBar.Update(focusChangedMsg{active: panelInspector, width: a.width})
				}
			default:
				// Any right-panel focus returns to sessions on Tab.
				a.blurAllPanels()
				a.focused = panelSessions
				a.sessionsModel.Focus()
				a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
			}
			return a, nil

		case "i":
			// Toggle right panel between inspector and inventory views.
			// This is a no-op for the live panel until task 47 ships; the flag is
			// tracked so View() can render the correct placeholder.
			a.inventoryMode = !a.inventoryMode
			if a.focused == panelInspector {
				a.inspectorModel.Blur()
				a.focused = panelInventory
				a.inventoryModel.Focus()
				a.commandBar.Update(focusChangedMsg{active: panelInventory, width: a.width})
			} else if a.focused == panelInventory {
				a.inventoryModel.Blur()
				a.focused = panelInspector
				a.inspectorModel.Focus()
				a.commandBar.Update(focusChangedMsg{active: panelInspector, width: a.width})
			}
			return a, nil
		}
	}

	// Forward all other messages to the focused panel.
	return a.dispatchToFocused(msg)
}

// dispatchSizeMsg broadcasts terminal dimensions to all panels so each can
// calculate truncation and viewport heights without querying the terminal.
func (a App) dispatchSizeMsg(w, h int) tea.Cmd {
	leftW := (w * 40) / 100
	rightW := w - leftW

	const cmdBarH = 1
	bodyH := h - cmdBarH

	pane := paneSizeMsg{w: leftW - 4, h: bodyH - 4}
	a.sessionsModel.Update(pane)
	a.inspectorModel.Update(paneSizeMsg{w: rightW - 4, h: bodyH - 4})
	a.referenceModel.Update(pane)
	a.commandBar.Update(focusChangedMsg{active: a.focused, width: w})
	return nil
}

// dispatchToFocused forwards msg to whichever panel has focus and merges the
// updated sub-model back into App state.
func (a App) dispatchToFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.focused {
	case panelSessions:
		updated, cmd := a.sessionsModel.Update(msg)
		a.sessionsModel = updated
		return a, cmd
	case panelInspector:
		updated, cmd := a.inspectorModel.Update(msg)
		a.inspectorModel = updated
		return a, cmd
	case panelInventory:
		updated, cmd := a.inventoryModel.Update(msg)
		a.inventoryModel = updated
		return a, cmd
	case panelReference:
		updated, cmd := a.referenceModel.Update(msg)
		a.referenceModel = updated
		return a, cmd
	case panelCommandBar:
		updated, cmd := a.commandBar.Update(msg)
		a.commandBar = updated
		return a, cmd
	}
	return a, nil
}

// View composes the full-screen layout:
//
//	[daemon warning]        — only when daemonErr is set
//	[left panel | right panel]
//	[command bar]
//
// When the reference overlay is open it is rendered over the two-column layout.
// No dimensions are hardcoded — everything derives from a.width / a.height.
func (a App) View() string {
	if a.width == 0 || a.height == 0 {
		// Terminal size not yet known; defer rendering.
		return ""
	}

	var b strings.Builder

	// ── Daemon warning line ───────────────────────────────────────────────────
	warningHeight := 0
	if a.daemonErr != nil {
		warning := lipgloss.NewStyle().Foreground(colorDead).Render(
			"[daemon unavailable — showing last known state]",
		)
		b.WriteString(warning + "\n")
		warningHeight = 1
	}

	// ── Layout dimensions ─────────────────────────────────────────────────────
	// Reserve 1 row for the command bar and 1 for the warning (if shown).
	// Border styles add 2 rows (top+bottom) and 2 cols (left+right) each.
	const cmdBarHeight = 1
	bodyHeight := a.height - cmdBarHeight - warningHeight

	leftWidth := (a.width * 40) / 100
	rightWidth := a.width - leftWidth

	// Inner dimensions (content inside the border).
	leftInner := leftWidth - 2   // subtract border columns
	rightInner := rightWidth - 2
	innerHeight := bodyHeight - 2 // subtract border rows

	// Clamp to sane minimums so layout doesn't panic on tiny terminals.
	if leftInner < 1 {
		leftInner = 1
	}
	if rightInner < 1 {
		rightInner = 1
	}
	if innerHeight < 1 {
		innerHeight = 1
	}

	// ── Left panel ────────────────────────────────────────────────────────────
	leftBorder := BorderUnfocused
	if a.focused == panelSessions {
		leftBorder = BorderFocused
	}
	leftContent := WithWidth(WithHeight(PanelLeft, innerHeight), leftInner).
		Render(a.sessionsModel.View())
	leftPanel := leftBorder.Width(leftWidth - 2).Height(innerHeight).Render(leftContent)

	// ── Right panel ───────────────────────────────────────────────────────────
	rightFocused := a.focused == panelInspector || a.focused == panelInventory
	rightBorder := BorderUnfocused
	if rightFocused {
		rightBorder = BorderFocused
	}

	var rightContent string
	if a.inventoryMode {
		rightContent = a.inventoryModel.View()
	} else {
		rightContent = a.inspectorModel.View()
	}
	rightContentStyled := WithWidth(WithHeight(PanelRight, innerHeight), rightInner).
		Render(rightContent)
	rightPanel := rightBorder.Width(rightWidth - 2).Height(innerHeight).Render(rightContentStyled)

	// ── Join panels side by side ──────────────────────────────────────────────
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)

	// ── Reference overlay ─────────────────────────────────────────────────────
	if a.focused == panelReference {
		// Render the overlay at 60% width centred over the body.
		overlayWidth := (a.width * 60) / 100
		overlayHeight := (bodyHeight * 70) / 100
		if overlayWidth < 20 {
			overlayWidth = 20
		}
		if overlayHeight < 5 {
			overlayHeight = 5
		}

		overlayContent := a.referenceModel.View()
		overlay := OverlayPanel.Width(overlayWidth - 4).Height(overlayHeight - 2).Render(overlayContent)

		// Simple centering: pad the overlay with spaces to approximate centre
		// position. lipgloss.Place is not yet available in all versions.
		padLeft := (a.width - overlayWidth) / 2
		padTop := (bodyHeight - overlayHeight) / 2
		if padLeft < 0 {
			padLeft = 0
		}
		if padTop < 0 {
			padTop = 0
		}

		// Build lines: blank rows above, then the overlay rows indented, then
		// the rest of the screen left blank. This paints "over" body.
		overlayLines := strings.Split(overlay, "\n")
		bodyLines := strings.Split(body, "\n")

		for i := 0; i < padTop && i < len(bodyLines); i++ {
			// leave body lines unchanged above the overlay
		}
		indent := strings.Repeat(" ", padLeft)
		for i, line := range overlayLines {
			targetRow := padTop + i
			if targetRow < len(bodyLines) {
				bodyLines[targetRow] = indent + line
			}
		}
		body = strings.Join(bodyLines, "\n")
	}

	b.WriteString(body)
	b.WriteString("\n")

	// ── Command bar ───────────────────────────────────────────────────────────
	b.WriteString(CommandBar.Width(a.width).Render(a.commandBar.View()))

	return b.String()
}

// fetchSessions returns a Cmd that queries the daemon for sessions and status,
// then dispatches a sessionsRefreshedMsg. Each call uses a 1s per-request
// timeout to keep the UI responsive even when the daemon is sluggish.
func (a App) fetchSessions() tea.Cmd {
	client := a.client
	return func() tea.Msg {
		sessions, err := client.ListSessions()
		if err != nil {
			return sessionsRefreshedMsg{err: err}
		}
		status, err := client.Status()
		if err != nil {
			// Sessions arrived but status failed — surface partial data.
			return sessionsRefreshedMsg{sessions: sessions, err: fmt.Errorf("status: %w", err)}
		}
		return sessionsRefreshedMsg{sessions: sessions, status: status}
	}
}

// blurAllPanels calls Blur on every sub-model. Called before focus is moved to
// a new panel to ensure exactly one panel is focused at a time.
func (a *App) blurAllPanels() {
	a.sessionsModel.Blur()
	a.inspectorModel.Blur()
	a.inventoryModel.Blur()
	a.referenceModel.Blur()
	a.commandBar.Blur()
}
