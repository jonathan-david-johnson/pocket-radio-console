// Package radio is the radio surface: favorites (Supabase) + station lookup,
// browse, and search (radio-browser.info), plus station tracklists. HTTP is the
// boundary; base URLs are injectable so tests mock both hosts.
package radio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

const (
	defaultSupabaseURL  = "https://brvtspdculqyvdrmdtef.supabase.co"
	defaultSupabaseKey  = "sb_publishable_1MRvFzvB6O7f2zDPfs2nkA_p18FSLUF"
	defaultRadioBrowser = "https://de1.api.radio-browser.info/json"
	userAgent           = "PocketRadio/1.0"
)

// Station is a radio stream from radio-browser.info.
type Station struct {
	ID        string // station UUID
	Name      string
	StreamURL string
	LogoURL   string
	Country   string
	Language  string
	Tags      string
	Codec     string
	Homepage  string
	Bitrate   int
	Votes     int
}

// Directory is the boundary interface for radio favorites/browse/search.
type Directory interface {
	Favorites(ctx context.Context, userID string) ([]Station, error)
	Top(ctx context.Context, limit int) ([]Station, error)
	Search(ctx context.Context, query string) ([]Station, error)
	AddFavorite(ctx context.Context, userID, stationID string) error
	RemoveFavorite(ctx context.Context, userID, stationID string) error
}

// Client implements Directory and Tracklister over HTTP.
type Client struct {
	SupabaseURL  string
	SupabaseKey  string
	RadioBrowser string
	ITunesBase   string // iTunes Search base (artwork fallback)
	HTTP         *http.Client
}

// NewClient returns a Client pointed at production hosts.
func NewClient() *Client {
	return &Client{
		SupabaseURL:  defaultSupabaseURL,
		SupabaseKey:  defaultSupabaseKey,
		RadioBrowser: defaultRadioBrowser,
		ITunesBase:   defaultITunesBase,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) supabaseURL() string  { return orDefault(c.SupabaseURL, defaultSupabaseURL) }
func (c *Client) supabaseKey() string  { return orDefault(c.SupabaseKey, defaultSupabaseKey) }
func (c *Client) radioBrowser() string { return orDefault(c.RadioBrowser, defaultRadioBrowser) }
func (c *Client) itunesBase() string   { return orDefault(c.ITunesBase, defaultITunesBase) }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// Favorites returns the user's favorite stations: Supabase IDs → parallel
// radio-browser byuuid lookups → sorted by name.
func (c *Client) Favorites(ctx context.Context, userID string) ([]Station, error) {
	ids, err := c.favoriteIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		stations []Station
	)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			st, err := c.lookupStation(ctx, id)
			if err != nil || st == nil {
				return
			}
			mu.Lock()
			stations = append(stations, *st)
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	sort.Slice(stations, func(i, j int) bool { return stations[i].Name < stations[j].Name })
	return stations, nil
}

func (c *Client) favoriteIDs(ctx context.Context, userID string) ([]string, error) {
	u := c.supabaseURL() + "/rest/v1/radio_favorites?select=station_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.supabaseKey())
	req.Header.Set("x-user-uuid", userID)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("supabase favorites: status %d", resp.StatusCode)
	}
	var rows []struct {
		StationID string `json:"station_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.StationID)
	}
	return ids, nil
}

// browserRow is the radio-browser JSON station shape (byuuid + search + topvote).
type browserRow struct {
	StationUUID string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Favicon     string `json:"favicon"`
	Country     string `json:"country"`
	Language    string `json:"language"`
	Tags        string `json:"tags"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	Votes       int    `json:"votes"`
	Homepage    string `json:"homepage"`
}

func (r browserRow) toStation(uuidFallback string) (Station, bool) {
	stream := r.URLResolved
	if stream == "" {
		stream = r.URL
	}
	if r.Name == "" || stream == "" {
		return Station{}, false
	}
	id := r.StationUUID
	if id == "" {
		id = uuidFallback
	}
	return Station{
		ID:        id,
		Name:      r.Name,
		StreamURL: stream,
		LogoURL:   r.Favicon,
		Country:   r.Country,
		Language:  r.Language,
		Tags:      r.Tags,
		Codec:     r.Codec,
		Bitrate:   r.Bitrate,
		Votes:     r.Votes,
		Homepage:  r.Homepage,
	}, true
}

func (c *Client) lookupStation(ctx context.Context, uuid string) (*Station, error) {
	rows, err := c.fetchRows(ctx, c.radioBrowser()+"/stations/byuuid/"+url.PathEscape(uuid))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	st, ok := rows[0].toStation(uuid)
	if !ok {
		return nil, nil
	}
	return &st, nil
}

// Top returns the top-voted stations (default Browse list).
func (c *Client) Top(ctx context.Context, limit int) ([]Station, error) {
	if limit <= 0 {
		limit = 50
	}
	u := fmt.Sprintf("%s/stations/topvote?limit=%d&hidebroken=true", c.radioBrowser(), limit)
	return c.fetchStations(ctx, u)
}

// Search does a free-text station search ordered by votes.
func (c *Client) Search(ctx context.Context, query string) ([]Station, error) {
	q := url.Values{}
	q.Set("name", query)
	q.Set("limit", "40")
	q.Set("hidebroken", "true")
	q.Set("order", "votes")
	q.Set("reverse", "true")
	return c.fetchStations(ctx, c.radioBrowser()+"/stations/search?"+q.Encode())
}

func (c *Client) fetchStations(ctx context.Context, u string) ([]Station, error) {
	rows, err := c.fetchRows(ctx, u)
	if err != nil {
		return nil, err
	}
	stations := make([]Station, 0, len(rows))
	for _, r := range rows {
		if st, ok := r.toStation(""); ok {
			stations = append(stations, st)
		}
	}
	return stations, nil
}

func (c *Client) fetchRows(ctx context.Context, u string) ([]browserRow, error) {
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("radio-browser: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rows []browserRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// AddFavorite upserts a station into the user's Supabase favorites.
func (c *Client) AddFavorite(ctx context.Context, userID, stationID string) error {
	u := c.supabaseURL() + "/rest/v1/radio_favorites"
	body, _ := json.Marshal(map[string]string{"user_uuid": userID, "station_id": stationID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.supabaseKey())
	req.Header.Set("x-user-uuid", userID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal,resolution=merge-duplicates")
	return c.doNoContent(req, "addFavorite")
}

// RemoveFavorite deletes a station from the user's Supabase favorites.
func (c *Client) RemoveFavorite(ctx context.Context, userID, stationID string) error {
	u := fmt.Sprintf("%s/rest/v1/radio_favorites?station_id=eq.%s&user_uuid=eq.%s",
		c.supabaseURL(), url.QueryEscape(stationID), url.QueryEscape(userID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.supabaseKey())
	req.Header.Set("x-user-uuid", userID)
	req.Header.Set("Prefer", "return=minimal")
	return c.doNoContent(req, "removeFavorite")
}

func (c *Client) doNoContent(req *http.Request, op string) error {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: status %d", op, resp.StatusCode)
	}
	return nil
}
