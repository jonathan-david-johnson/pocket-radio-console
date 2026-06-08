package pocketcasts

// M5 list endpoints: subscribed podcasts, full episode lists (New Releases), and
// episode show notes. Ported from the menubar's APIService.swift. The podcast
// list is protobuf over api.pocketcasts.com; full episodes and show notes are
// JSON over cache.pocketcasts.com (no auth).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SubscribedPodcast is one entry from /user/podcast/list.
type SubscribedPodcast struct {
	UUID  string
	Title string
}

// NewRelease is one recently published episode across subscribed podcasts.
type NewRelease struct {
	UUID         string
	PodcastUUID  string
	PodcastTitle string
	Title        string
	URL          string
	Duration     int // seconds
	Published    time.Time
}

// EpisodeShowNotes is the plain-text description + optional image for an episode.
type EpisodeShowNotes struct {
	Description string // HTML stripped
	ImageURL    string
}

// PodcastList fetches the user's subscribed podcasts.
// POST /user/podcast/list — Api_UserPodcastListRequest { v(1)="2", m(2)="mobile" }.
func (c *Client) PodcastList(ctx context.Context, token string) ([]SubscribedPodcast, error) {
	body := append(encodeStringField(1, "2"), encodeStringField(2, "mobile")...)
	resp, err := c.post(ctx, "/user/podcast/list", token, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return decodePodcastList(data), nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrInvalidCredentials
	default:
		return nil, statusErr(resp.StatusCode, "podcastList")
	}
}

// decodePodcastList decodes Api_UserPodcastListResponse { podcasts(1)=repeated }
// where each podcast has uuid(1) + title(4).
func decodePodcastList(data []byte) []SubscribedPodcast {
	var out []SubscribedPodcast
	walkFields(data, func(f field) {
		if f.number == 1 && f.wireType == 2 {
			if p, ok := decodeUserPodcast(f.bytes); ok {
				out = append(out, p)
			}
		}
	})
	return out
}

func decodeUserPodcast(data []byte) (SubscribedPodcast, bool) {
	var p SubscribedPodcast
	walkFields(data, func(f field) {
		if f.wireType != 2 {
			return
		}
		switch f.number {
		case 1:
			p.UUID = string(f.bytes)
		case 4:
			p.Title = string(f.bytes)
		}
	})
	return p, p.UUID != ""
}

// fullResponse is the JSON shape of cache.pocketcasts.com/mobile/podcast/full/<uuid>.
type fullResponse struct {
	Podcast struct {
		Title    string `json:"title"`
		Episodes []struct {
			UUID      string `json:"uuid"`
			Title     string `json:"title"`
			URL       string `json:"url"`
			Duration  int    `json:"duration"`
			Published string `json:"published"`
		} `json:"episodes"`
	} `json:"podcast"`
}

// FullEpisodes fetches the full episode list for one podcast from the cache
// server. Episodes missing a title, URL, or parseable publish date are dropped.
func (c *Client) FullEpisodes(ctx context.Context, podcastUUID, podcastTitle string) ([]NewRelease, error) {
	u := fmt.Sprintf("%s/mobile/podcast/full/%s", c.cacheBaseURL(), podcastUUID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusErr(resp.StatusCode, "fullEpisodes")
	}
	var fr fullResponse
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return nil, err
	}
	display := podcastTitle
	if fr.Podcast.Title != "" {
		display = fr.Podcast.Title
	}
	var out []NewRelease
	for _, e := range fr.Podcast.Episodes {
		if e.Title == "" || e.URL == "" || e.Published == "" {
			continue
		}
		pub, err := time.Parse(time.RFC3339, e.Published)
		if err != nil {
			continue
		}
		out = append(out, NewRelease{
			UUID:         e.UUID,
			PodcastUUID:  podcastUUID,
			PodcastTitle: display,
			Title:        e.Title,
			URL:          e.URL,
			Duration:     e.Duration,
			Published:    pub,
		})
	}
	return out, nil
}

// NewReleases lists subscribed podcasts, fans out to FullEpisodes, filters to the
// last `days` days, and sorts by publish date descending.
func (c *Client) NewReleases(ctx context.Context, token string, days int) ([]NewRelease, error) {
	podcasts, err := c.PodcastList(ctx, token)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	var (
		mu        sync.Mutex
		collected []NewRelease
		wg        sync.WaitGroup
	)
	for _, p := range podcasts {
		wg.Add(1)
		go func(p SubscribedPodcast) {
			defer wg.Done()
			eps, err := c.FullEpisodes(ctx, p.UUID, p.Title)
			if err != nil {
				return // a single failed podcast must not sink the whole list
			}
			mu.Lock()
			collected = append(collected, eps...)
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	filtered := collected[:0]
	for _, e := range collected {
		if !e.Published.Before(cutoff) {
			filtered = append(filtered, e)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Published.After(filtered[j].Published)
	})
	return filtered, nil
}

// showNotesResponse is the JSON shape of
// cache.pocketcasts.com/mobile/show_notes/full/<podcastUuid>.
type showNotesResponse struct {
	Podcast struct {
		Episodes []struct {
			UUID      string `json:"uuid"`
			ShowNotes string `json:"show_notes"`
			Image     string `json:"image"`
		} `json:"episodes"`
	} `json:"podcast"`
}

// ShowNotes fetches the description + image for one episode. The cache server
// returns notes for the whole podcast keyed by episode uuid.
func (c *Client) ShowNotes(ctx context.Context, podcastUUID, episodeUUID string) (EpisodeShowNotes, error) {
	if podcastUUID == "" {
		return EpisodeShowNotes{}, fmt.Errorf("showNotes: empty podcast uuid")
	}
	u := fmt.Sprintf("%s/mobile/show_notes/full/%s", c.cacheBaseURL(), podcastUUID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return EpisodeShowNotes{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return EpisodeShowNotes{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EpisodeShowNotes{}, statusErr(resp.StatusCode, "showNotes")
	}
	var sr showNotesResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return EpisodeShowNotes{}, err
	}
	for _, e := range sr.Podcast.Episodes {
		if e.UUID == episodeUUID {
			return EpisodeShowNotes{
				Description: htmlToPlainText(e.ShowNotes),
				ImageURL:    e.Image,
			}, nil
		}
	}
	return EpisodeShowNotes{}, fmt.Errorf("showNotes: episode %s not found", episodeUUID)
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

var htmlEntities = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", "\"",
	"&#39;", "'",
	"&apos;", "'",
	"&nbsp;", " ",
	"&hellip;", "…",
)

var (
	trailingSpaceRe = regexp.MustCompile(`[ \t]+\n`)
	manyNewlinesRe  = regexp.MustCompile(`\n{3,}`)
	blockBreakRe    = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>`)
)

// htmlToPlainText drops tags, decodes common entities, and collapses whitespace.
// Block-level breaks become newlines before tags are stripped.
func htmlToPlainText(html string) string {
	s := blockBreakRe.ReplaceAllString(html, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = htmlEntities.Replace(s)
	s = trailingSpaceRe.ReplaceAllString(s, "\n")
	s = manyNewlinesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
