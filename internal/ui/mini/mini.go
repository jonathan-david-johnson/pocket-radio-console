// Package mini is the one-line Bubble Tea model for mini mode. It binds to the
// engine: it renders NowPlaying and forwards key presses as engine commands.
package mini

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pocket-radio-console/internal/library"
)

// Engine is the subset of the engine the mini UI drives.
type Engine interface {
	State() library.NowPlaying
	Subscribe() <-chan library.NowPlaying
	TogglePlayback()
	SkipForward()
	SkipBack()
}

var (
	// ANSI color 1 = red on any terminal (light or dark theme).
	playStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	// No explicit color — bold inherits the terminal's default foreground.
	titleStyle = lipgloss.NewStyle().Bold(true)
	// ANSI color 8 = bright black / dark gray, readable on both themes.
	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

type nowMsg library.NowPlaying

// Model is the Bubble Tea model.
type Model struct {
	engine Engine
	now    library.NowPlaying
	sub    <-chan library.NowPlaying
}

// New returns a mini Model bound to engine.
func New(e Engine) Model {
	return Model{engine: e, now: e.State(), sub: e.Subscribe()}
}

// Init starts listening for state updates.
func (m Model) Init() tea.Cmd {
	return waitForState(m.sub)
}

func waitForState(sub <-chan library.NowPlaying) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-sub
		if !ok {
			return nil
		}
		return nowMsg(n)
	}
}

// Update handles keys and state messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case nowMsg:
		m.now = library.NowPlaying(msg)
		return m, waitForState(m.sub)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case " ":
			m.engine.TogglePlayback()
		case "right":
			m.engine.SkipForward()
		case "left":
			m.engine.SkipBack()
		}
	}
	return m, nil
}

// View renders the single status line.
func (m Model) View() string {
	icon := "⏸"
	if m.now.Playing {
		icon = "▶"
	}
	title := m.now.Title
	if title == "" {
		title = "(loading…)"
	}

	// Live streams show a [station] source tag; podcasts show a m:ss / m:ss clock.
	var tag string
	if m.now.IsLive {
		src := m.now.Subtitle
		if src == "" {
			src = "Live"
		}
		tag = dimStyle.Render("[" + src + "]")
	} else {
		tag = dimStyle.Render(fmt.Sprintf("%s / %s",
			fmtDuration(m.now.Position), fmtDuration(m.now.Duration)))
	}

	line := fmt.Sprintf("%s %s  %s",
		playStyle.Render(icon), titleStyle.Render(title), tag)
	hint := dimStyle.Render("  ·  space ⏯  ← → skip  q quit")
	return line + hint + "\n"
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
