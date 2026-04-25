package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — AdaptiveColor pairs ensure readability on both dark and light
// terminals. The dark value is shown on dark backgrounds, light value on light.
// All colors are ANSI 256-compatible; no true-color-only values.
var (
	// colorActive is the green used for live sessions and border focus.
	colorActive = lipgloss.AdaptiveColor{Dark: "10", Light: "28"}
	// colorIdle is the yellow used for idle/warning states.
	colorIdle = lipgloss.AdaptiveColor{Dark: "11", Light: "130"}
	// colorDead is the red used for terminated sessions and errors.
	colorDead = lipgloss.AdaptiveColor{Dark: "9", Light: "160"}
	// colorUnknown is the gray used when session state cannot be determined.
	colorUnknown = lipgloss.AdaptiveColor{Dark: "8", Light: "244"}
	// colorFocus is the bright blue used on the focused panel border.
	colorFocus = lipgloss.AdaptiveColor{Dark: "12", Light: "27"}
	// colorMuted is the dimmed gray used for secondary text.
	colorMuted = lipgloss.AdaptiveColor{Dark: "240", Light: "250"}
	// colorOverlay is the dark background used for the reference overlay backdrop.
	colorOverlay = lipgloss.AdaptiveColor{Dark: "235", Light: "254"}
	// colorSelected is the highlight background for selected list items.
	colorSelected = lipgloss.AdaptiveColor{Dark: "17", Light: "189"}
)

// ── Status badges ─────────────────────────────────────────────────────────────
// Used by the sessions panel to mark each session row.

// BadgeActive renders a green "active" label for live sessions.
var BadgeActive = lipgloss.NewStyle().
	Foreground(colorActive).
	Bold(true)

// BadgeIdle renders a yellow "idle" label for sessions with no recent events.
var BadgeIdle = lipgloss.NewStyle().
	Foreground(colorIdle).
	Bold(true)

// BadgeDead renders a red "dead" label for terminated sessions.
var BadgeDead = lipgloss.NewStyle().
	Foreground(colorDead).
	Bold(true)

// BadgeUnknown renders a gray "?" label when session status cannot be determined.
var BadgeUnknown = lipgloss.NewStyle().
	Foreground(colorUnknown)

// ── Borders ───────────────────────────────────────────────────────────────────
// Applied to the outer frame of each panel. Width/height are set dynamically by
// App.View() via lipgloss style chaining — these are base styles only.

// BorderFocused is the panel border style when the panel holds keyboard focus.
var BorderFocused = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colorFocus)

// BorderUnfocused is the panel border style for all panels that are not focused.
var BorderUnfocused = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colorUnknown)

// ── Panel layout ──────────────────────────────────────────────────────────────
// PanelLeft and PanelRight are the two main column base styles. App.View() sets
// Width and Height on these at render time from the current terminal dimensions.
// The helper methods below avoid repeating the chaining pattern in View().

// PanelLeft is the base style for the left session-list panel (~40% width).
var PanelLeft = lipgloss.NewStyle().Padding(0, 1)

// PanelRight is the base style for the right inspector/inventory panel (~60% width).
var PanelRight = lipgloss.NewStyle().Padding(0, 1)

// WithWidth returns a copy of s with the given width set.
// Used when width is only known at render time (terminal resize).
func WithWidth(s lipgloss.Style, w int) lipgloss.Style { return s.Width(w) }

// WithHeight returns a copy of s with the given height set.
// Used when height is only known at render time (terminal resize).
func WithHeight(s lipgloss.Style, h int) lipgloss.Style { return s.Height(h) }

// ── Text roles ────────────────────────────────────────────────────────────────

// Title is the panel header line (e.g. "Sessions", "Inspector").
var Title = lipgloss.NewStyle().Bold(true).Padding(0, 1)

// Subtitle is the secondary heading within a panel (e.g. project path under the session title).
var Subtitle = lipgloss.NewStyle().Foreground(colorMuted)

// Muted is dimmed text for metadata that should recede visually (timestamps, sizes).
var Muted = lipgloss.NewStyle().Foreground(colorMuted)

// Selected highlights the currently selected list item in the sessions panel.
var Selected = lipgloss.NewStyle().
	Background(colorSelected).
	Bold(true)

// HelpKey is the key label in help/command-bar entries (e.g. "[q]", "[?]").
var HelpKey = lipgloss.NewStyle().Bold(true)

// HelpDesc is the description text paired with a HelpKey in the command bar.
var HelpDesc = lipgloss.NewStyle().Foreground(colorMuted)

// ── Reference overlay ────────────────────────────────────────────────────────
// The overlay renders modally over the two-column layout when the user presses ?
// or /. It must visually pop above the dashboard content.

// OverlayBackdrop is the full-width backdrop that dims the panels behind the overlay.
var OverlayBackdrop = lipgloss.NewStyle().
	Background(colorOverlay)

// OverlayPanel is the floating panel that contains the search input and results.
var OverlayPanel = lipgloss.NewStyle().
	Border(lipgloss.DoubleBorder()).
	BorderForeground(colorFocus).
	Padding(1, 2)

// ── Command bar ───────────────────────────────────────────────────────────────

// CommandBar is the bottom row that displays contextual key hints.
// It spans the full terminal width and is rendered below the two-column layout.
var CommandBar = lipgloss.NewStyle().
	Foreground(colorMuted).
	Padding(0, 1)
