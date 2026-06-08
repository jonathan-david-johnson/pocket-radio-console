package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	kcrwTracklistURL = "https://tracklist-api.kcrw.com/Music/all/1?page_size=10"
	kexpTracklistURL = "https://api.kexp.org/v2/plays/?limit=10"
)

// Track is one entry in a station's now-playing history.
type Track struct {
	Title    string
	Artist   string
	Album    string
	PlayedAt time.Time
}

// Label renders "Song — Artist" for the now-playing line.
func (t Track) Label() string {
	if t.Artist == "" {
		return t.Title
	}
	return t.Title + " — " + t.Artist
}

// Tracklister fetches a station's recent tracks. The engine polls it.
type Tracklister interface {
	// HasTracklist reports whether this station publishes a tracklist.
	HasTracklist(s Station) bool
	// Tracklist returns recent tracks, newest first; empty if unsupported.
	Tracklist(ctx context.Context, s Station) ([]Track, error)
}

// HasTracklist reports whether the station has a supported tracklist endpoint.
func (c *Client) HasTracklist(s Station) bool { return tracklistURL(s) != "" }

// tracklistURL returns the tracklist endpoint for a station, or "" if none.
func tracklistURL(s Station) string {
	name := strings.ToLower(s.Name)
	switch {
	case strings.Contains(name, "kcrw"):
		return kcrwTracklistURL
	case strings.Contains(name, "kexp"):
		return kexpTracklistURL
	default:
		return ""
	}
}

// Tracklist fetches and parses the station's tracklist (newest first).
func (c *Client) Tracklist(ctx context.Context, s Station) ([]Track, error) {
	u := tracklistURL(s)
	if u == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tracklist: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(s.Name)
	if strings.Contains(name, "kcrw") {
		return parseKCRW(body), nil
	}
	return parseKEXP(body), nil
}

// parseKCRW parses the tracklist-api.kcrw.com body, dropping [BREAK] entries.
func parseKCRW(data []byte) []Track {
	var rows []struct {
		Title           string `json:"title"`
		Artist          string `json:"artist"`
		Album           string `json:"album"`
		AlbumImage      string `json:"albumImage"`
		AlbumImageLarge string `json:"albumImageLarge"`
		Datetime        string `json:"datetime"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return nil
	}
	var tracks []Track
	for _, r := range rows {
		if r.Title == "" || r.Artist == "" || r.Artist == "[BREAK]" {
			continue
		}
		tracks = append(tracks, Track{
			Title:    r.Title,
			Artist:   r.Artist,
			Album:    r.Album,
			PlayedAt: parseKCRWDate(r.Datetime),
		})
	}
	return tracks
}

func parseKCRWDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseKEXP parses api.kexp.org plays, keeping only trackplay rows.
func parseKEXP(data []byte) []Track {
	var resp struct {
		Results []struct {
			PlayType  string `json:"play_type"`
			Song      string `json:"song"`
			Artist    string `json:"artist"`
			Album     string `json:"album"`
			Thumbnail string `json:"thumbnail_uri"`
			Airdate   string `json:"airdate"`
		} `json:"results"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil
	}
	var tracks []Track
	for _, p := range resp.Results {
		if p.PlayType != "trackplay" || p.Song == "" || p.Artist == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, p.Airdate)
		tracks = append(tracks, Track{
			Title:    p.Song,
			Artist:   p.Artist,
			Album:    p.Album,
			PlayedAt: at,
		})
	}
	return tracks
}
