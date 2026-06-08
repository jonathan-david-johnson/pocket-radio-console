package library

import (
	"testing"
	"time"

	"pocket-radio-console/internal/pocketcasts"
)

// Behavior 6: time-remaining strings match the menubar's formatting rules.
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		secs int
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{59, "59s"},
		{60, "1m"},
		{120, "2m"},
		{3599, "59m"},
		{3600, "1h"},
		{3900, "1h 5m"},
		{7200, "2h"},
		{-10, "0s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.secs); got != c.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

func TestEpisodeTimeRemaining(t *testing.T) {
	cases := []struct {
		name    string
		ep      pocketcasts.Episode
		playing bool
		want    string
	}{
		{"partway", pocketcasts.Episode{Duration: 1800, PlayedUpTo: 300}, false, "25m left"},
		{"unstarted", pocketcasts.Episode{Duration: 600, PlayedUpTo: 0}, false, "10m left"},
		{"finished when stopped", pocketcasts.Episode{Duration: 600, PlayedUpTo: 600}, false, "Finished"},
		{"not finished while playing", pocketcasts.Episode{Duration: 600, PlayedUpTo: 600}, true, "0s left"},
		{"overshoot stopped", pocketcasts.Episode{Duration: 600, PlayedUpTo: 999}, false, "Finished"},
		{"no duration but progress", pocketcasts.Episode{Duration: 0, PlayedUpTo: 120}, false, "2m in"},
		{"no info", pocketcasts.Episode{}, false, ""},
	}
	for _, c := range cases {
		if got := EpisodeTimeRemaining(c.ep, c.playing); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRelativeDate(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{30 * 24 * time.Hour, "May 9"},
	}
	for _, c := range cases {
		if got := RelativeDate(now.Add(-c.ago), now); got != c.want {
			t.Errorf("ago=%v: got %q, want %q", c.ago, got, c.want)
		}
	}
	if got := RelativeDate(time.Time{}, now); got != "" {
		t.Errorf("zero time: got %q, want empty", got)
	}
}

func TestTotalTimeRemaining(t *testing.T) {
	eps := []pocketcasts.Episode{
		{Duration: 1800, PlayedUpTo: 300}, // 1500
		{Duration: 600, PlayedUpTo: 0},    // 600
		{Duration: 600, PlayedUpTo: 600},  // 0
	}
	if got := TotalTimeRemaining(eps); got != "35m total time remaining" {
		t.Errorf("got %q", got)
	}
	if got := TotalTimeRemaining(nil); got != "" {
		t.Errorf("empty queue: got %q, want empty", got)
	}
}
