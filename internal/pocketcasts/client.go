// Package pocketcasts is the Pocket Casts API client (HTTP boundary). It is
// SDK-style: one method per endpoint, so each can be mocked independently. The
// protobuf wire format matches the menubar app exactly.
package pocketcasts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "https://api.pocketcasts.com"
	userAgent      = "PocketRadio/1.0"
)

// ErrInvalidCredentials is returned for a 401/403 from login or a 401 elsewhere.
var ErrInvalidCredentials = errors.New("invalid email or password")

// Session is the result of a successful login.
type Session struct {
	Token string
	UUID  string
	Email string
}

// Episode is one Up Next podcast episode.
type Episode struct {
	UUID        string
	Title       string
	URL         string
	PodcastUUID string
	PlayedUpTo  int       // seconds of progress (merged from sync records)
	Duration    int       // total seconds (merged from sync records)
	Published   time.Time // zero if unknown
}

// PlaybackInfo is per-episode progress from /user/podcast/episodes.
type PlaybackInfo struct {
	UUID       string
	PlayedUpTo int // seconds
	Duration   int // seconds
}

// EpisodeStatus is the Pocket Casts playing status.
type EpisodeStatus int

const (
	// StatusNotPlayed = 1.
	StatusNotPlayed EpisodeStatus = 1
	// StatusInProgress = 2.
	StatusInProgress EpisodeStatus = 2
	// StatusCompleted = 3.
	StatusCompleted EpisodeStatus = 3
)

// EpisodeUpdate is a position write-back for /sync/update_episode.
type EpisodeUpdate struct {
	UUID        string
	PodcastUUID string
	Position    int // seconds
	Duration    int // seconds
	Status      EpisodeStatus
}

// Skip holds the user's synced skip amounts (seconds).
type Skip struct {
	Back    int
	Forward int
}

// PocketCasts is the boundary interface the engine depends on.
type PocketCasts interface {
	Login(ctx context.Context, email, password string) (Session, error)
	UpNext(ctx context.Context, token, deviceID string) ([]Episode, error)
	PodcastEpisodes(ctx context.Context, token, podcastUUID string) ([]PlaybackInfo, error)
	UpdateEpisode(ctx context.Context, token string, u EpisodeUpdate) error
	PlayNow(ctx context.Context, token, deviceID string, ep Episode) error
	RemoveFromUpNext(ctx context.Context, token, deviceID string, ep Episode) error
	SkipSettings(ctx context.Context, token string) (Skip, error)
}

// Client is the concrete HTTP implementation of PocketCasts.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a Client pointed at the production API.
func NewClient() *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) post(ctx context.Context, path, token string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.httpClient().Do(req)
}

// Login posts email+password to /user/login and returns the Session.
// Api_UserLoginRequest { email(1), password(2), scope(3) }.
func (c *Client) Login(ctx context.Context, email, password string) (Session, error) {
	body := encodeLoginRequest(email, password, "mobile")
	resp, err := c.post(ctx, "/user/login", "", body)
	if err != nil {
		return Session{}, fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return Session{}, fmt.Errorf("read login response: %w", err)
		}
		s, ok := decodeLoginResponse(data)
		if !ok {
			return Session{}, errors.New("unexpected login response")
		}
		return s, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return Session{}, ErrInvalidCredentials
	default:
		return Session{}, fmt.Errorf("login: unexpected status %d", resp.StatusCode)
	}
}

// UpNext posts to /up_next/sync and returns the queue in order (first = top).
func (c *Client) UpNext(ctx context.Context, token, deviceID string) ([]Episode, error) {
	body := encodeUpNextRequest(deviceID)
	resp, err := c.post(ctx, "/up_next/sync", token, body)
	if err != nil {
		return nil, fmt.Errorf("up_next request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read up_next response: %w", err)
		}
		return decodeUpNextResponse(data), nil
	case http.StatusUnauthorized:
		return nil, ErrInvalidCredentials
	default:
		return nil, fmt.Errorf("up_next: unexpected status %d", resp.StatusCode)
	}
}

// PodcastEpisodes fetches per-episode progress for one podcast. This is where
// the real playedUpTo/duration live (up_next/sync often reports 0).
// Request Api_UuidRequest { v(1)="2", m(2)="mobile", uuid(3)=podcastUUID }.
func (c *Client) PodcastEpisodes(ctx context.Context, token, podcastUUID string) ([]PlaybackInfo, error) {
	body := encodeStringField(1, "2")
	body = append(body, encodeStringField(2, "mobile")...)
	body = append(body, encodeStringField(3, podcastUUID)...)

	resp, err := c.post(ctx, "/user/podcast/episodes", token, body)
	if err != nil {
		return nil, fmt.Errorf("podcast episodes request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return decodeSyncEpisodes(data), nil
	case http.StatusUnauthorized:
		return nil, ErrInvalidCredentials
	default:
		return nil, fmt.Errorf("podcast episodes: status %d", resp.StatusCode)
	}
}

// UpdateEpisode writes playback position back to Pocket Casts.
// Api_UpdateEpisodeRequest { uuid(1), podcast(2), position(3 Int32Value),
// status(4 varint), duration(5 varint) }.
func (c *Client) UpdateEpisode(ctx context.Context, token string, u EpisodeUpdate) error {
	body := encodeUpdateEpisode(u)
	resp, err := c.post(ctx, "/sync/update_episode", token, body)
	if err != nil {
		return fmt.Errorf("update episode request: %w", err)
	}
	defer resp.Body.Close()
	return statusErr(resp.StatusCode, "update episode")
}

// PlayNow bubbles an episode to the top of Up Next (change action=1).
func (c *Client) PlayNow(ctx context.Context, token, deviceID string, ep Episode) error {
	return c.upNextChange(ctx, token, deviceID, ep, 1)
}

// RemoveFromUpNext removes an episode from Up Next (change action=4).
func (c *Client) RemoveFromUpNext(ctx context.Context, token, deviceID string, ep Episode) error {
	return c.upNextChange(ctx, token, deviceID, ep, 4)
}

func (c *Client) upNextChange(ctx context.Context, token, deviceID string, ep Episode, action int64) error {
	body := encodeUpNextChange(deviceID, ep, action)
	resp, err := c.post(ctx, "/up_next/sync", token, body)
	if err != nil {
		return fmt.Errorf("up_next change request: %w", err)
	}
	defer resp.Body.Close()
	return statusErr(resp.StatusCode, "up_next change")
}

// SkipSettings reads the user's synced skip amounts. The named_settings/update
// endpoint returns current settings even with no settings to change, so sending
// only the device field is effectively read-only.
// Response { skipForward(5), skipBack(6) }, each Int32Setting{ value(1)=Int32Value }.
func (c *Client) SkipSettings(ctx context.Context, token string) (Skip, error) {
	body := encodeStringField(2, "PocketRadio")
	resp, err := c.post(ctx, "/user/named_settings/update", token, body)
	if err != nil {
		return Skip{}, fmt.Errorf("named_settings request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Skip{}, fmt.Errorf("named_settings: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Skip{}, err
	}
	return decodeSkipSettings(data), nil
}

func statusErr(code int, op string) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized:
		return ErrInvalidCredentials
	default:
		return fmt.Errorf("%s: status %d", op, code)
	}
}

// encodeUpdateEpisode builds the /sync/update_episode body.
func encodeUpdateEpisode(u EpisodeUpdate) []byte {
	positionWrapper := encodeLengthDelimitedField(3, encodeVarintField(1, int64(u.Position)))
	body := encodeStringField(1, u.UUID)
	body = append(body, encodeStringField(2, u.PodcastUUID)...)
	body = append(body, positionWrapper...)
	body = append(body, encodeVarintField(4, int64(u.Status))...)
	body = append(body, encodeVarintField(5, int64(u.Duration))...)
	return body
}

// encodeUpNextChange builds an Api_UpNextSyncRequest carrying a single Change.
// Change { uuid(1), action(2), modified(3), title(4), url(5), podcast(6) };
// wrapped as Api_UpNextChanges{ changes(2) } under field 4 of the request.
func encodeUpNextChange(deviceID string, ep Episode, action int64) []byte {
	nowMillis := time.Now().UnixMilli()
	change := encodeStringField(1, ep.UUID)
	change = append(change, encodeVarintField(2, action)...)
	change = append(change, encodeVarintField(3, nowMillis)...)
	change = append(change, encodeStringField(4, ep.Title)...)
	change = append(change, encodeStringField(5, ep.URL)...)
	change = append(change, encodeStringField(6, ep.PodcastUUID)...)

	upNextChanges := encodeLengthDelimitedField(2, change)

	body := encodeVarintField(1, nowMillis)
	body = append(body, encodeStringField(2, "2")...)
	body = append(body, encodeLengthDelimitedField(4, upNextChanges)...)
	body = append(body, encodeStringField(6, deviceID)...)
	return body
}

// decodeSyncEpisodes parses Api_SyncEpisodesResponse { episodes(1) repeated }.
// Each top-level EpisodeSyncResponse uses PLAIN int32 fields (not Int32Value):
// uuid(1), playedUpTo(3 varint), duration(6 varint).
func decodeSyncEpisodes(data []byte) []PlaybackInfo {
	var out []PlaybackInfo
	walkFields(data, func(f field) {
		if f.number != 1 || f.wireType != 2 {
			return
		}
		var info PlaybackInfo
		walkFields(f.bytes, func(g field) {
			switch {
			case g.number == 1 && g.wireType == 2:
				info.UUID = string(g.bytes)
			case g.number == 3 && g.wireType == 0:
				info.PlayedUpTo = int(g.varint)
			case g.number == 6 && g.wireType == 0:
				info.Duration = int(g.varint)
			}
		})
		if info.UUID != "" {
			out = append(out, info)
		}
	})
	return out
}

// decodeSkipSettings unwraps forward(5)/back(6) Int32Setting{ Int32Value(1) }.
func decodeSkipSettings(data []byte) Skip {
	s := Skip{Back: 10, Forward: 45}
	if fwd := firstSubmessage(firstSubmessage(data, 5), 1); fwd != nil {
		s.Forward = decodeInt32Value(fwd)
	}
	if back := firstSubmessage(firstSubmessage(data, 6), 1); back != nil {
		s.Back = decodeInt32Value(back)
	}
	return s
}

// encodeLoginRequest builds the POST /user/login body.
func encodeLoginRequest(email, password, scope string) []byte {
	out := encodeStringField(1, email)
	out = append(out, encodeStringField(2, password)...)
	out = append(out, encodeStringField(3, scope)...)
	return out
}

// decodeLoginResponse parses Api_UserLoginResponse { token(1), uuid(2), email(3) }.
func decodeLoginResponse(data []byte) (Session, bool) {
	var s Session
	walkFields(data, func(f field) {
		if f.wireType != 2 {
			return
		}
		switch f.number {
		case 1:
			s.Token = string(f.bytes)
		case 2:
			s.UUID = string(f.bytes)
		case 3:
			s.Email = string(f.bytes)
		}
	})
	if s.Token == "" || s.UUID == "" {
		return Session{}, false
	}
	return s, true
}

// encodeUpNextRequest builds the /up_next/sync body:
//
//	field 1: deviceTime (int64 varint), field 2: version "2", field 6: deviceID.
func encodeUpNextRequest(deviceID string) []byte {
	millis := time.Now().UnixMilli()
	out := encodeVarintField(1, millis)
	out = append(out, encodeStringField(2, "2")...)
	out = append(out, encodeStringField(6, deviceID)...)
	return out
}

// decodeUpNextResponse parses Api_UpNextResponse: episodes (field 4) merged with
// sync records (field 5) by UUID for playedUpTo/duration.
func decodeUpNextResponse(data []byte) []Episode {
	var episodes []Episode
	type sync struct{ playedUpTo, duration int }
	syncData := map[string]sync{}

	walkFields(data, func(f field) {
		switch {
		case f.number == 4 && f.wireType == 2:
			if ep, ok := decodeEpisodeResponse(f.bytes); ok {
				episodes = append(episodes, ep)
			}
		case f.number == 5 && f.wireType == 2:
			if uuid, p, d, ok := decodeEpisodeSync(f.bytes); ok {
				syncData[uuid] = sync{p, d}
			}
		}
	})

	for i := range episodes {
		if s, ok := syncData[episodes[i].UUID]; ok {
			episodes[i].PlayedUpTo = s.playedUpTo
			episodes[i].Duration = s.duration
		}
	}
	return episodes
}

// decodeEpisodeResponse parses one EpisodeResponse sub-message:
//
//	title(1), url(2), podcast(3), uuid(4), published(5 Timestamp).
func decodeEpisodeResponse(data []byte) (Episode, bool) {
	var ep Episode
	walkFields(data, func(f field) {
		if f.wireType != 2 {
			return
		}
		switch f.number {
		case 1:
			ep.Title = string(f.bytes)
		case 2:
			ep.URL = string(f.bytes)
		case 3:
			ep.PodcastUUID = string(f.bytes)
		case 4:
			ep.UUID = string(f.bytes)
		case 5:
			ep.Published = decodeTimestamp(f.bytes)
		}
	})
	if ep.UUID == "" {
		return Episode{}, false
	}
	return ep, true
}

// decodeEpisodeSync parses EpisodeSyncResponse: uuid(1), playedUpTo(6), duration(7).
func decodeEpisodeSync(data []byte) (uuid string, playedUpTo, duration int, ok bool) {
	walkFields(data, func(f field) {
		if f.wireType != 2 {
			return
		}
		switch f.number {
		case 1:
			uuid = string(f.bytes)
		case 6:
			playedUpTo = decodeInt32Value(f.bytes)
		case 7:
			duration = decodeInt32Value(f.bytes)
		}
	})
	if uuid == "" {
		return "", 0, 0, false
	}
	return uuid, playedUpTo, duration, true
}

// decodeTimestamp parses google.protobuf.Timestamp { seconds(1) }; nanos ignored.
func decodeTimestamp(data []byte) time.Time {
	var seconds int64
	walkFields(data, func(f field) {
		if f.number == 1 && f.wireType == 0 {
			seconds = int64(f.varint)
		}
	})
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}
