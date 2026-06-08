// Package theme provides Lip Gloss styles ported from the Pocket Casts palette.
// Colors mirror the menubar's Constants.swift light/dark variants.
package theme

import "github.com/charmbracelet/lipgloss"

// Palette mirrors PocketCastsTheme in the menubar (Constants.swift).
// Each color is an AdaptiveColor so lipgloss resolves light vs. dark automatically.
var (
	// PrimaryUi01 is the main background.
	PrimaryUi01 = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#292B2E"}
	// PrimaryUi04 is the card/surface background.
	PrimaryUi04 = lipgloss.AdaptiveColor{Light: "#F7F9FA", Dark: "#161718"}
	// PrimaryUi05 is used for dividers.
	PrimaryUi05 = lipgloss.AdaptiveColor{Light: "#E0E6EA", Dark: "#393A3C"}
	// PrimaryText01 is the primary text color.
	PrimaryText01 = lipgloss.AdaptiveColor{Light: "#292B2E", Dark: "#FFFFFF"}
	// PrimaryText02 is the muted / secondary text color.
	PrimaryText02 = lipgloss.AdaptiveColor{Light: "#8F97A4", Dark: "#9C9FA4"}
	// PrimaryIcon02 is the inactive icon color.
	PrimaryIcon02 = lipgloss.AdaptiveColor{Light: "#B8C3C9", Dark: "#8F97A4"}
	// Accent is the interactive / selected color (Pocket Casts red).
	Accent = lipgloss.AdaptiveColor{Light: "#F43E37", Dark: "#F44336"}
)

// Styles use ANSI terminal colors so they render correctly on both light and
// dark backgrounds without relying on terminal background detection (which
// fails in alt-screen mode).
//
//   "1"  = ANSI red          (accent)
//   "8"  = ANSI bright-black (secondary / muted)
//   "15" = ANSI bright-white (text on colored background)
var (
	// TitleStyle: bold, no explicit color → inherits terminal default foreground.
	TitleStyle = lipgloss.NewStyle().Bold(true)
	// SubtitleStyle: ANSI bright-black — readable gray on any background.
	SubtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	// AccentStyle: ANSI red, bold.
	AccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	// MutedStyle: dim gray.
	MutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	// DividerStyle: dim gray.
	DividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// PillStyle returns a style for a source pill.
func PillStyle(active bool) lipgloss.Style {
	if active {
		// Red background, bright-white text — readable everywhere.
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("1")).
			Padding(0, 1).
			Bold(true)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Padding(0, 1)
}
