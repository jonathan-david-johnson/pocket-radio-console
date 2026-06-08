package library

import (
	"fmt"

	"pocket-radio-console/internal/pocketcasts"
)

// FormatDuration renders a second count the way the menubar does:
//
//	<60s   → "45s"
//	<1h    → "32m"
//	≥1h    → "1h 5m" (or "2h" when the minute part is zero)
func FormatDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		h := seconds / 3600
		m := (seconds % 3600) / 60
		if m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
}

// TotalTimeRemaining sums the remaining time across the Up Next queue. Returns
// "" when nothing is left (matching the menubar's empty-label behavior).
func TotalTimeRemaining(eps []pocketcasts.Episode) string {
	total := 0
	for _, ep := range eps {
		if ep.Duration > ep.PlayedUpTo {
			total += ep.Duration - ep.PlayedUpTo
		}
	}
	if total <= 0 {
		return ""
	}
	return FormatDuration(total) + " total time remaining"
}

// EpisodeTimeRemaining renders a single episode's per-row label. playing marks
// the episode as the one currently playing, which suppresses the early
// "Finished" flip (the menubar avoids saying "Finished" while audio still runs).
func EpisodeTimeRemaining(ep pocketcasts.Episode, playing bool) string {
	if ep.Duration > 0 {
		remaining := ep.Duration - ep.PlayedUpTo
		if remaining < 0 {
			remaining = 0
		}
		if remaining <= 1 && !playing {
			return "Finished"
		}
		if remaining <= 0 {
			return FormatDuration(0) + " left"
		}
		return FormatDuration(remaining) + " left"
	}
	if ep.PlayedUpTo > 0 {
		return FormatDuration(ep.PlayedUpTo) + " in"
	}
	return ""
}
