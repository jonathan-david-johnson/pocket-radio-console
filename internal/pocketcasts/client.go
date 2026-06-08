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

// PocketCasts is the boundary interface the engine depends on. Only the M1
// methods are implemented here; later milestones extend it.
type PocketCasts interface {
	Login(ctx context.Context, email, password string) (Session, error)
	UpNext(ctx context.Context, token, deviceID string) ([]Episode, error)
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
