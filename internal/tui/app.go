package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ccmc/internal/reference"
	"ccmc/pkg/ccmc"
)

// pollInterval is how often App fires a daemon refresh. 500ms keeps the display
// feeling live without hammering the unix socket on slow machines.
const pollInterval = 500 * time.Millisecond

// statusLineTTL is how long a transient status message stays visible before
// being cleared. The next polling tick also clears an expired status.
const statusLineTTL = 4 * time.Second

// daemonClient is the test-seam interface for the daemon API client.
// *ccmc.Client satisfies this interface implicitly; tests supply a stub.
// Keeping it narrow (only what App needs) avoids coupling to the full client API.
type daemonClient interface {
	ListSessions() ([]ccmc.Session, error)
	Status() (ccmc.DaemonStatus, error)
}

// killFunc is the lifecycle seam for killing a session. Tests replace this to
// assert the right session ID is passed without invoking the OS process machinery.
var killFunc = func(client daemonClient, id string) error {
	// Production path: type-assert to *ccmc.Client. The real App is always
	// constructed with a *ccmc.Client; only test stubs bypass this.
	cc, ok := client.(*ccmc.Client)
	if !ok {
		return fmt.Errorf("kill: client does not support lifecycle operations")
	}
	// Import lifecycle indirectly via a shim to avoid an import cycle.
	// lifecycle.Kill is called here; the import is at package init below.
	return lifecycleKill(cc, id)
}

// launchFunc is the lifecycle seam for launching a new session. Tests replace
// this to capture the directory argument without invoking osascript.
var launchFunc = func(client daemonClient, dir string) (string, error) {
	cc, ok := client.(*ccmc.Client)
	if !ok {
		return "", fmt.Errorf("launch: client does not support lifecycle operations")
	}
	return lifecycleLaunch(cc, dir)
}

// openInITermFunc is the lifecycle seam for opening a directory in iTerm. Tests
// replace this to capture the directory argument without invoking osascript.
var openInITermFunc = func(dir string) error {
	return lifecycleOpenInITerm(dir)
}

// panel is an enum identifying which panel currently holds keyboard focus.
type panel int

const (
	panelSessions      panel = iota // left column — session list
	panelInspector                  // right column — session inspector
	panelInventory                  // right column — inventory view (switched via 'i')
	panelReference                  // overlay — reference search
	panelCommandBar                 // bottom row
	panelLaunchPrompt               // modal — directory prompt for 'l'
	panelKillConfirm                // modal — kill confirmation prompt for 'k'
	panelHelp                       // modal — keyboard help overlay for 'h'
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

// lifecycleResultMsg carries the result of an async kill or launch operation.
type lifecycleResultMsg struct {
	op  string // "kill" or "launch"
	id  string // session ID (launch: new ID; kill: killed ID)
	err error
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

	// ── Prompt / modal state ──────────────────────────────────────────────────
	launchInput     textinput.Model // active when focused == panelLaunchPrompt
	killTargetID    string          // session ID to kill; set when focused == panelKillConfirm
	killTargetPath  string          // ProjectPath of killTargetID (for the confirm prompt)

	// ── Transient status line ─────────────────────────────────────────────────
	// statusLine is shown above the command bar. Cleared by the next tick after
	// statusExpiry, or immediately when a new status replaces it.
	statusLine   string
	statusExpiry time.Time

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
	ti := textinput.New()
	ti.Placeholder = "directory path"
	ti.CharLimit = 512

	return App{
		client:         cfg.Client,
		focused:        panelSessions,
		launchInput:    ti,
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
//   - Modal keys (l, k, h, r, o) are consumed here when appropriate.
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
		// Clear expired status line on each tick.
		if a.statusLine != "" && time.Now().After(a.statusExpiry) {
			a.statusLine = ""
		}
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

	case lifecycleResultMsg:
		switch msg.op {
		case "kill":
			if msg.err != nil {
				a.setStatus("kill failed: " + msg.err.Error())
			} else {
				a.setStatus("killed session " + msg.id)
			}
		case "launch":
			if msg.err != nil {
				a.setStatus("launch failed: " + msg.err.Error())
			} else {
				a.setStatus("launched session " + msg.id)
			}
		}
		return a, nil

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
		// ── Modal intercepts (launch prompt, kill confirm, help) ──────────────
		// These modes consume all keys, returning to normal focus on close keys.
		if a.focused == panelLaunchPrompt {
			return a.handleLaunchPromptKey(msg)
		}
		if a.focused == panelKillConfirm {
			return a.handleKillConfirmKey(msg)
		}
		if a.focused == panelHelp {
			return a.handleHelpKey(msg)
		}

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

		case "h":
			// Open the keyboard help overlay. Not triggered when the reference
			// overlay is open — the reference panel owns the keyboard in that state
			// so 'h' types into the search box instead.
			if a.focused == panelReference {
				break
			}
			a.blurAllPanels()
			a.focused = panelHelp
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

		case "r":
			// Force an immediate daemon refresh outside the polling tick.
			return a, a.fetchSessions()

		case "l":
			// Open launch prompt, pre-populated with the selected session's ProjectPath.
			if a.focused != panelSessions {
				break
			}
			a.blurAllPanels()
			a.focused = panelLaunchPrompt
			dir := ""
			if sel := a.sessionsModel.Selected(); sel != nil {
				dir = sel.ProjectPath
			}
			a.launchInput.SetValue(dir)
			a.launchInput.Focus()
			a.launchInput.CursorEnd()
			return a, textinput.Blink

		case "k":
			// Open kill confirmation prompt for the selected session.
			if a.focused != panelSessions {
				break
			}
			sel := a.sessionsModel.Selected()
			if sel == nil {
				break
			}
			a.blurAllPanels()
			a.focused = panelKillConfirm
			a.killTargetID = sel.ID
			a.killTargetPath = sel.ProjectPath
			return a, nil

		case "o":
			// Open the selected session's project directory in a new iTerm tab.
			if a.focused != panelSessions {
				break
			}
			sel := a.sessionsModel.Selected()
			if sel == nil {
				break
			}
			dir := sel.ProjectPath
			return a, func() tea.Msg {
				if err := openInITermFunc(dir); err != nil {
					return lifecycleResultMsg{op: "open", err: err}
				}
				return lifecycleResultMsg{op: "open"}
			}
		}
	}

	// Forward all other messages to the focused panel.
	return a.dispatchToFocused(msg)
}

// handleLaunchPromptKey processes keys while the launch directory prompt is open.
func (a App) handleLaunchPromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Cancel prompt — return to sessions panel.
		a.launchInput.Blur()
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
		return a, nil

	case tea.KeyEnter:
		dir := strings.TrimSpace(a.launchInput.Value())
		if dir == "" {
			return a, nil
		}
		a.launchInput.Blur()
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
		a.setStatus("launching…")
		client := a.client
		return a, func() tea.Msg {
			id, err := launchFunc(client, dir)
			return lifecycleResultMsg{op: "launch", id: id, err: err}
		}
	}

	// Forward all other keys to the textinput.
	var cmd tea.Cmd
	a.launchInput, cmd = a.launchInput.Update(msg)
	return a, cmd
}

// handleKillConfirmKey processes keys while the kill confirmation prompt is open.
func (a App) handleKillConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		id := a.killTargetID
		a.killTargetID = ""
		a.killTargetPath = ""
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
		a.setStatus("killing…")
		client := a.client
		return a, func() tea.Msg {
			err := killFunc(client, id)
			return lifecycleResultMsg{op: "kill", id: id, err: err}
		}

	default:
		// Any other key cancels — including 'n', 'N', Esc.
		a.killTargetID = ""
		a.killTargetPath = ""
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
		return a, nil
	}
}

// handleHelpKey processes keys while the help overlay is open. Only Esc closes it.
func (a App) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		a.focused = panelSessions
		a.sessionsModel.Focus()
		a.commandBar.Update(focusChangedMsg{active: panelSessions, width: a.width})
	}
	// All other keys are consumed by the overlay (no forwarding).
	return a, nil
}

// setStatus sets a transient status line that disappears after statusLineTTL.
func (a *App) setStatus(msg string) {
	a.statusLine = msg
	a.statusExpiry = time.Now().Add(statusLineTTL)
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
//	[status line]           — only when statusLine is non-empty
//	[command bar]
//
// When the reference overlay is open it is rendered over the two-column layout.
// When the launch prompt, kill confirm, or help overlay is open they are rendered
// as inline boxes above the command bar.
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
	// Reserve 1 row for the command bar, 1 for warning (if shown), 1 for status.
	const cmdBarHeight = 1
	statusHeight := 0
	if a.statusLine != "" {
		statusHeight = 1
	}
	bodyHeight := a.height - cmdBarHeight - warningHeight - statusHeight

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

		padLeft := (a.width - overlayWidth) / 2
		padTop := (bodyHeight - overlayHeight) / 2
		if padLeft < 0 {
			padLeft = 0
		}
		if padTop < 0 {
			padTop = 0
		}

		overlayLines := strings.Split(overlay, "\n")
		bodyLines := strings.Split(body, "\n")

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

	// ── Launch prompt overlay ─────────────────────────────────────────────────
	if a.focused == panelLaunchPrompt {
		prompt := OverlayPanel.Width(a.width - 8).Render(
			"Launch new session\n\n" +
				"Directory: " + a.launchInput.View() + "\n\n" +
				HelpDesc.Render("[Enter] launch  [Esc] cancel"),
		)
		b.WriteString(prompt + "\n")
	}

	// ── Kill confirm overlay ──────────────────────────────────────────────────
	if a.focused == panelKillConfirm {
		confirm := OverlayPanel.Width(a.width - 8).Render(
			fmt.Sprintf("Kill session %s in %s?\n\n", a.killTargetID, a.killTargetPath) +
				HelpDesc.Render("[y] kill  [any other key] cancel"),
		)
		b.WriteString(confirm + "\n")
	}

	// ── Help overlay ──────────────────────────────────────────────────────────
	if a.focused == panelHelp {
		helpText := helpOverlayText()
		help := OverlayPanel.Width(a.width - 8).Render(helpText)
		b.WriteString(help + "\n")
	}

	// ── Status line ───────────────────────────────────────────────────────────
	if a.statusLine != "" {
		b.WriteString(Muted.Render(a.statusLine) + "\n")
	}

	// ── Command bar ───────────────────────────────────────────────────────────
	b.WriteString(CommandBar.Width(a.width).Render(a.commandBar.View()))

	return b.String()
}

// helpOverlayText returns the keyboard help overlay content grouped by context.
func helpOverlayText() string {
	return strings.Join([]string{
		Title.Render("Keyboard Help"),
		"",
		HelpKey.Render("Global"),
		"  [q] / [ctrl+c]   quit",
		"  [?] / [/]        open reference overlay",
		"  [h]              this help screen",
		"  [Tab]            cycle panel focus",
		"  [Esc]            close overlay / cancel",
		"  [r]              force refresh from daemon",
		"",
		HelpKey.Render("Session List (left panel)"),
		"  [↑] / [k]        move up",
		"  [↓] / [j]        move down",
		"  [Enter]          select session",
		"  [l]              launch new session (directory prompt)",
		"  [k]              kill selected session (confirmation prompt)",
		"  [o]              open project directory in iTerm",
		"  [i]              toggle inspector / inventory panel",
		"",
		HelpKey.Render("Inspector / Inventory (right panel)"),
		"  [↑] / [↓]        scroll",
		"  [Tab]            return to session list",
		"",
		HelpKey.Render("Reference Overlay"),
		"  [type]           fuzzy search",
		"  [↑] / [↓]        navigate results",
		"  [Enter]          show detail",
		"  [Esc]            close overlay",
		"",
		HelpDesc.Render("[Esc] close help"),
	}, "\n")
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
	a.launchInput.Blur()
}
