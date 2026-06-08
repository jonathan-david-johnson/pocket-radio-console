// Package full is the alt-screen Bubble Tea model for the full PocketRadio TUI.
// It mirrors the menubar's top section: source pills, now-playing pane with
// album art, transport controls, and a scrub bar.
package full

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pocket-radio-console/internal/art"
	"pocket-radio-console/internal/library"
	"pocket-radio-console/internal/pocketcasts"
	"pocket-radio-console/internal/radio"
	"pocket-radio-console/internal/ui/theme"
)

// Engine is the subset of the library Engine the full UI drives.
type Engine interface {
	State() library.NowPlaying
	Subscribe() <-chan library.NowPlaying
	TogglePlayback()
	SkipForward()
	SkipBack()
	Scrub(d time.Duration)
	StageTarget(t library.Target)
	StagedTarget() library.Target
	CurrentTarget() library.Target
	PlayTarget(ctx context.Context, t library.Target) error
	UpNextList(ctx context.Context) ([]pocketcasts.Episode, error)
	NewReleases(ctx context.Context) ([]pocketcasts.NewRelease, error)
}

// Pill represents one source tab.
type Pill struct {
	Label  string
	Target library.Target
}

// Model is the full alt-screen Bubble Tea model.
type Model struct {
	engine   Engine
	pills    []Pill      // index 0 = Podcast, 1-3 = stream favorites
	selected int         // which pill is highlighted
	now      library.NowPlaying
	sub      <-chan library.NowPlaying
	artCache *art.Cache
	proto    art.Protocol
	artImg   image.Image  // last fetched art
	artURL   string       // URL of artImg
	width    int
	height   int

	// Podcast pane state (shown when the Podcast pill is selected). podcastTab
	// toggles between the Up Next and New Releases sub-tabs.
	podcastTab int // 0 = Up Next, 1 = New Releases
	upNext     []pocketcasts.Episode
	upNextSel  int
	upNextErr  error

	newReleases  []pocketcasts.NewRelease
	newRelSel    int
	newRelErr    error
	newRelLoaded bool
}

const (
	tabUpNext = iota
	tabNewReleases
)

type nowMsg library.NowPlaying

type artMsg struct {
	url string
	img image.Image
}

type upNextMsg struct {
	eps []pocketcasts.Episode
	err error
}

type newReleasesMsg struct {
	rel []pocketcasts.NewRelease
	err error
}

// New builds a full Model. streams is the list of favorite stations to show as
// pills (capped at 3). Call tea.NewProgram with tea.WithAltScreen().
func New(e Engine, streams []radio.Station) Model {
	pills := []Pill{{Label: "Podcast", Target: library.UpNextTop{}}}
	for i, st := range streams {
		if i >= 3 {
			break
		}
		pills = append(pills, Pill{Label: st.Name, Target: library.PlayStation{Station: st}})
	}
	return Model{
		engine:   e,
		pills:    pills,
		now:      e.State(),
		sub:      e.Subscribe(),
		artCache: art.NewCache(),
		proto:    art.Detect(),
		width:    80,
		height:   24,
	}
}

// Init subscribes to engine state and loads the Up Next list.
func (m Model) Init() tea.Cmd {
	return tea.Batch(waitState(m.sub), m.fetchUpNext())
}

func (m Model) fetchUpNext() tea.Cmd {
	return func() tea.Msg {
		eps, err := m.engine.UpNextList(context.Background())
		return upNextMsg{eps: eps, err: err}
	}
}

func (m Model) fetchNewReleases() tea.Cmd {
	return func() tea.Msg {
		rel, err := m.engine.NewReleases(context.Background())
		return newReleasesMsg{rel: rel, err: err}
	}
}

// podcastSelected reports whether the Podcast pill (index 0) is highlighted.
func (m Model) podcastSelected() bool { return m.selected == 0 }

func waitState(sub <-chan library.NowPlaying) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-sub
		if !ok {
			return nil
		}
		return nowMsg(n)
	}
}

func (m Model) fetchArt(url string) tea.Cmd {
	return func() tea.Msg {
		img, err := m.artCache.Fetch(context.Background(), url)
		if err != nil {
			return nil
		}
		return artMsg{url: url, img: img}
	}
}

// Update handles keyboard, window size, state, and art messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case nowMsg:
		m.now = library.NowPlaying(msg)
		slog.Debug("full.nowMsg", "title", m.now.Title, "playing", m.now.Playing, "art_url", m.now.ArtURL, "is_live", m.now.IsLive)
		var cmds []tea.Cmd
		cmds = append(cmds, waitState(m.sub))
		if m.now.ArtURL != "" && m.now.ArtURL != m.artURL {
			slog.Debug("full.fetchArt", "url", m.now.ArtURL, "proto", m.proto)
			cmds = append(cmds, m.fetchArt(m.now.ArtURL))
		}
		return m, tea.Batch(cmds...)

	case artMsg:
		slog.Info("full.artReady", "url", msg.url, "has_image", msg.img != nil)
		m.artURL = msg.url
		m.artImg = msg.img

	case upNextMsg:
		m.upNext = msg.eps
		m.upNextErr = msg.err
		if m.upNextSel >= len(m.upNext) {
			m.upNextSel = 0
		}
		slog.Debug("full.upNext", "count", len(m.upNext), "err", msg.err)

	case newReleasesMsg:
		m.newReleases = msg.rel
		m.newRelErr = msg.err
		m.newRelLoaded = true
		if m.newRelSel >= len(m.newReleases) {
			m.newRelSel = 0
		}
		slog.Debug("full.newReleases", "count", len(m.newReleases), "err", msg.err)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "[", "]":
			if m.podcastSelected() {
				m.podcastTab = (m.podcastTab + 1) % 2
				if m.podcastTab == tabNewReleases && !m.newRelLoaded {
					return m, m.fetchNewReleases() // lazy load on first view
				}
			}
			return m, nil
		case "up", "k":
			if m.podcastSelected() {
				m.moveSel(-1)
			}
			return m, nil
		case "down", "j":
			if m.podcastSelected() {
				m.moveSel(1)
			}
			return m, nil
		case " ":
			if m.engine.CurrentTarget() == nil {
				t := m.pills[m.selected].Target
				slog.Info("full.space.play_selected", "pill", m.pills[m.selected].Label)
				go func() { _ = m.engine.PlayTarget(context.Background(), t) }()
			} else {
				slog.Debug("full.space.toggle")
				m.engine.TogglePlayback()
			}
		case "right", "l":
			if m.seekable() { // no skip on live/continuous streams
				m.engine.SkipForward()
			}
		case "left", "h":
			if m.seekable() {
				m.engine.SkipBack()
			}
		case "tab":
			m.selected = (m.selected + 1) % len(m.pills)
			m.engine.StageTarget(m.pills[m.selected].Target)
		case "shift+tab":
			m.selected = (m.selected - 1 + len(m.pills)) % len(m.pills)
			m.engine.StageTarget(m.pills[m.selected].Target)
		case "1", "2", "3", "4", "5":
			idx := int(msg.String()[0]-'1')
			if idx < len(m.pills) {
				m.selected = idx
				m.engine.StageTarget(m.pills[m.selected].Target)
			}
		case "enter":
			target := m.pills[m.selected].Target
			if m.podcastSelected() {
				switch {
				case m.podcastTab == tabUpNext && len(m.upNext) > 0:
					target = library.UpNextAt{Index: m.upNextSel}
				case m.podcastTab == tabNewReleases && len(m.newReleases) > 0:
					target = library.PlayRelease{Release: m.newReleases[m.newRelSel]}
				}
			}
			go func() {
				_ = m.engine.PlayTarget(context.Background(), target)
			}()
		}
	}
	return m, nil
}

// moveSel moves the selection cursor in the active podcast sub-tab by delta,
// clamped to the list bounds.
func (m *Model) moveSel(delta int) {
	switch m.podcastTab {
	case tabUpNext:
		m.upNextSel = clamp(m.upNextSel+delta, len(m.upNext))
	case tabNewReleases:
		m.newRelSel = clamp(m.newRelSel+delta, len(m.newReleases))
	}
}

func clamp(i, n int) int {
	if i < 0 {
		return 0
	}
	if n > 0 && i >= n {
		return n - 1
	}
	return i
}

// View renders the full TUI layout.
func (m Model) View() string {
	var b strings.Builder

	// ── Pills row ────────────────────────────────────────────
	b.WriteString(m.pillsRow())
	b.WriteString("\n")
	b.WriteString(divider(m.width))
	b.WriteString("\n")

	// ── Now-playing pane ─────────────────────────────────────
	const artWidth = 12
	const artRows = 6 // matches placeholder box height (1 + artWidth/2-2 + 1)

	if m.inlineImage() {
		// Inline-image protocols (iTerm2/kitty) emit a single escape that, when
		// rendered, advances the cursor by the reserved row count. Zipping that
		// beside text and blank-padding to height double-counts those rows (the
		// escape advances them AND the padding lines emit newlines), which throws
		// off Bubble Tea's line-diff renderer and ghosts the now-playing block on
		// the next redraw. Instead stack the image on its own reserved rows —
		// painted with cursor save/restore so it nets zero advance — then the
		// title/subtitle below. Logical lines now equal physical rows.
		for _, l := range m.inlineArtLines(artWidth, artRows) {
			b.WriteString(l)
			b.WriteString("\n")
		}
		for _, l := range m.textBlock(m.width - 2) {
			b.WriteString(l)
			b.WriteString("\n")
		}
	} else {
		// Side-by-side: chafa block art and the placeholder box are real cells,
		// so Bubble Tea's accounting is correct.
		artLines := m.artBlock(artWidth, artRows)
		textLines := m.textBlock(m.width - artWidth - 2)

		rows := max(len(artLines), len(textLines))
		for i := 0; i < rows; i++ {
			artCell := ""
			if i < len(artLines) {
				artCell = artLines[i]
			} else {
				artCell = strings.Repeat(" ", artWidth)
			}
			textCell := ""
			if i < len(textLines) {
				textCell = textLines[i]
			}
			b.WriteString(artCell)
			b.WriteString("  ")
			b.WriteString(textCell)
			b.WriteString("\n")
		}
	}

	// ── Transport (progress meter + controls) ────────────────
	b.WriteString("\n")
	b.WriteString(m.transportRow())
	b.WriteString("\n")

	// ── Podcast lists (Up Next / New Releases sub-tabs) ──────
	if m.podcastSelected() {
		b.WriteString(divider(m.width))
		b.WriteString("\n")
		b.WriteString(m.podcastTabsRow())
		b.WriteString("\n")
		if m.podcastTab == tabNewReleases {
			b.WriteString(m.newReleasesSection())
		} else {
			b.WriteString(m.upNextSection())
		}
	}

	b.WriteString(divider(m.width))
	b.WriteString("\n")
	hint := "q quit · tab/1-4 select · enter play · space ⏯ · ← → skip"
	if m.podcastSelected() {
		hint = "q quit · tab select · [ ] tabs · ↑↓ move · enter play · space ⏯"
	}
	b.WriteString(theme.MutedStyle.Render(hint))

	return b.String()
}

// podcastTabsRow renders the Up Next / New Releases sub-tab header.
func (m Model) podcastTabsRow() string {
	return "  " +
		theme.PillStyle(m.podcastTab == tabUpNext).Render("Up Next") + " " +
		theme.PillStyle(m.podcastTab == tabNewReleases).Render("New Releases")
}

// newReleasesSection renders the New Releases list: podcast title + episode +
// relative publish date, selectable and scrollable.
func (m Model) newReleasesSection() string {
	if m.newRelErr != nil {
		return theme.MutedStyle.Render("  New Releases unavailable") + "\n"
	}
	if !m.newRelLoaded {
		return theme.MutedStyle.Render("  Loading…") + "\n"
	}
	if len(m.newReleases) == 0 {
		return theme.MutedStyle.Render("  No releases in the last 14 days") + "\n"
	}

	var b strings.Builder
	visible := m.height - 16
	if visible < 3 {
		visible = 3
	}
	start, end := scrollWindow(m.newRelSel, len(m.newReleases), visible)
	now := time.Now()
	for i := start; i < end; i++ {
		r := m.newReleases[i]
		cursor := "  "
		if i == m.newRelSel {
			cursor = theme.AccentStyle.Render("▸ ")
		}
		date := library.RelativeDate(r.Published, now)
		titleW := m.width - 4 - len(date) - 1
		if titleW < 10 {
			titleW = 10
		}
		title := truncate(r.Title, titleW)
		titleStyled := title
		if i == m.newRelSel {
			titleStyled = theme.TitleStyle.Render(title)
		}
		pad := m.width - 2 - lipgloss.Width(cursor) - lipgloss.Width(title) - len(date)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(cursor)
		b.WriteString(titleStyled)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(theme.MutedStyle.Render(date))
		b.WriteString("\n")
		// Second line: podcast title, dimmed.
		if r.PodcastTitle != "" {
			b.WriteString("  ")
			b.WriteString(theme.SubtitleStyle.Render(truncate(r.PodcastTitle, m.width-4)))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// upNextSection renders the Up Next header (total time remaining) and a
// scrollable, selectable episode list sized to the remaining terminal height.
func (m Model) upNextSection() string {
	var b strings.Builder

	if m.upNextErr != nil {
		return theme.MutedStyle.Render("  Up Next unavailable") + "\n"
	}
	if len(m.upNext) == 0 {
		return theme.MutedStyle.Render("  Up Next is empty") + "\n"
	}

	if total := library.TotalTimeRemaining(m.upNext); total != "" {
		b.WriteString(theme.SubtitleStyle.Render("  " + total))
		b.WriteString("\n")
	}

	// Reserve rows for everything above + the header line + bottom divider/help.
	visible := m.height - 16
	if visible < 3 {
		visible = 3
	}
	start, end := scrollWindow(m.upNextSel, len(m.upNext), visible)

	current := m.engine.CurrentTarget()
	for i := start; i < end; i++ {
		ep := m.upNext[i]
		cursor := "  "
		if i == m.upNextSel {
			cursor = theme.AccentStyle.Render("▸ ")
		}
		playing := false
		if _, ok := current.(library.UpNextAt); ok {
			playing = m.now.Title == ep.Title && m.now.Playing
		}
		left := library.EpisodeTimeRemaining(ep, playing)

		titleW := m.width - 4 - len(left) - 1
		if titleW < 10 {
			titleW = 10
		}
		title := truncate(ep.Title, titleW)
		titleStyled := title
		if i == m.upNextSel {
			titleStyled = theme.TitleStyle.Render(title)
		}
		pad := m.width - 2 - lipgloss.Width(cursor) - lipgloss.Width(title) - len(left)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(cursor)
		b.WriteString(titleStyled)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(theme.MutedStyle.Render(left))
		b.WriteString("\n")
	}
	return b.String()
}

// scrollWindow returns the [start, end) slice of a list of length n that keeps
// sel visible within a window of the given size.
func scrollWindow(sel, n, size int) (int, int) {
	if n <= size {
		return 0, n
	}
	start := sel - size/2
	if start < 0 {
		start = 0
	}
	if start+size > n {
		start = n - size
	}
	return start, start + size
}

func (m Model) pillsRow() string {
	parts := make([]string, 0, len(m.pills)+1)
	for i, p := range m.pills {
		label := truncate(p.Label, 14)
		parts = append(parts, theme.PillStyle(i == m.selected).Render(label))
	}
	parts = append(parts, theme.MutedStyle.Render("[Browse]"))
	return strings.Join(parts, " ")
}

// inlineImage reports whether the active protocol paints a real bitmap via a
// cursor-advancing escape (iTerm2/kitty) and an image is available to paint.
// chafa block art and the no-art placeholder are ordinary cells and use the
// side-by-side path.
func (m Model) inlineImage() bool {
	if m.artImg == nil {
		return false
	}
	return m.proto == art.ProtocolKitty || m.proto == art.ProtocolITerm2
}

// inlineArtLines returns height lines that reserve a width×height region for an
// inline image. The escape is wrapped in DECSC/DECRC (\x1b7 … \x1b8) so the
// terminal paints the bitmap but the cursor returns to where it started; the
// trailing blank lines then advance exactly height physical rows, keeping
// Bubble Tea's line count in sync with the screen.
func (m Model) inlineArtLines(width, height int) []string {
	lines := make([]string, height)
	rendered := art.Render(m.artImg, m.proto, width, height)
	if len(rendered) == 0 {
		return lines // all empty — render failed; reserve blank rows
	}
	lines[0] = "\x1b7" + string(rendered) + "\x1b8"
	return lines
}

func (m Model) artBlock(width, height int) []string {
	if m.artImg == nil || m.proto == art.ProtocolNone {
		// placeholder box — always exactly height lines
		top := "┌" + strings.Repeat("─", width-2) + "┐"
		mid := "│" + strings.Repeat(" ", width-2) + "│"
		bot := "└" + strings.Repeat("─", width-2) + "┘"
		lines := []string{top}
		for i := 0; i < height-2; i++ {
			lines = append(lines, mid)
		}
		lines = append(lines, bot)
		return lines
	}
	rendered := art.Render(m.artImg, m.proto, width, height)
	if len(rendered) == 0 {
		// Render failed — return blank lines matching height
		blank := strings.Repeat(" ", width)
		lines := make([]string, height)
		for i := range lines {
			lines[i] = blank
		}
		return lines
	}
	// For block-character protocols (chafa/sixel), the output contains newlines
	// and already has the right row count. For kitty/iTerm2, the escape is one
	// line; pad to height so Bubble Tea clears the correct number of rows.
	lines := strings.Split(strings.TrimRight(string(rendered), "\n"), "\n")
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines[:height]
}

func (m Model) textBlock(width int) []string {
	title := m.now.Title
	if title == "" {
		title = "(nothing playing)"
	}
	sub := m.now.Subtitle

	lines := []string{
		theme.TitleStyle.Render(truncate(title, width)),
		theme.SubtitleStyle.Render(truncate(sub, width)),
	}
	return lines
}

// meterWidth is the fixed cell width of the progress meter.
const meterWidth = 20

// seekable reports whether the current source has a known duration (podcasts and
// finite/duration-reporting streams). Only seekable sources get the progress
// meter and skip controls; live/continuous streams show an elapsed clock and
// play/pause only.
func (m Model) seekable() bool { return m.now.Duration > 0 }

// transportRow renders the transport controls centered above the progress meter
// (or elapsed clock for live/continuous sources).
func (m Model) transportRow() string {
	icon := "▶"
	if m.now.Playing {
		icon = "⏸"
	}

	var meterLine, controls string
	if m.seekable() {
		frac := float64(m.now.Position) / float64(m.now.Duration)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		filled := int(frac * meterWidth)
		if filled > meterWidth {
			filled = meterWidth
		}
		meter := theme.AccentStyle.Render(strings.Repeat("█", filled)) +
			theme.MutedStyle.Render(strings.Repeat("░", meterWidth-filled))
		elapsed := fmtDur(m.now.Position)
		remain := "-" + fmtDur(m.now.Duration-m.now.Position)
		meterLine = theme.MutedStyle.Render(elapsed) + " " + meter + " " + theme.MutedStyle.Render(remain)
		controls = fmt.Sprintf("⏮  %s  ⏭", icon)
	} else {
		// Live/continuous: elapsed time since playback started; play/pause only.
		meterLine = theme.MutedStyle.Render(fmtDur(m.now.Position) + " elapsed")
		controls = icon
	}

	// Center the controls over the meter line's span.
	controlsLine := lipgloss.PlaceHorizontal(
		lipgloss.Width(meterLine), lipgloss.Center, theme.AccentStyle.Render(controls),
	)
	return controlsLine + "\n" + meterLine
}

func divider(width int) string {
	return theme.DividerStyle.Render(strings.Repeat("─", width))
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	t := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	return string(runes[:max-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
