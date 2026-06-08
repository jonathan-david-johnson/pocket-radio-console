package radio

// iTunes Search artwork fallback. Used to fill album art for list rows and
// tracklist entries that don't carry their own image. Ported from the menubar.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const defaultITunesBase = "https://itunes.apple.com"

// Artwork returns a 600×600 album-art URL for "artist title" via the iTunes
// Search API, or "" if nothing is found. Errors are returned for transport
// failures; a successful response with no match yields ("", nil).
func (c *Client) Artwork(ctx context.Context, artist, title string) (string, error) {
	term := strings.TrimSpace(artist + " " + title)
	if term == "" {
		return "", nil
	}
	q := url.Values{
		"term":   {term},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"3"},
	}
	u := c.itunesBase() + "/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("artwork: status %d", resp.StatusCode)
	}

	var body struct {
		Results []struct {
			ArtworkURL100 string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if len(body.Results) == 0 || body.Results[0].ArtworkURL100 == "" {
		return "", nil
	}
	// Upgrade the thumbnail to 600×600.
	return strings.ReplaceAll(body.Results[0].ArtworkURL100, "/100x100bb.", "/600x600bb."), nil
}
