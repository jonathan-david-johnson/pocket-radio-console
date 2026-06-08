package library

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/pocketcasts"
)

// fakeAPI is a per-method-mockable PocketCasts.
type fakeAPI struct {
	loginCalls   int32
	upNextCalls  int32
	upNextResult []pocketcasts.Episode
	upNextErrSeq []error // returned in order, then last repeats
	session      pocketcasts.Session
}

func (f *fakeAPI) Login(ctx context.Context, email, password string) (pocketcasts.Session, error) {
	atomic.AddInt32(&f.loginCalls, 1)
	return f.session, nil
}

func (f *fakeAPI) UpNext(ctx context.Context, token, deviceID string) ([]pocketcasts.Episode, error) {
	n := atomic.AddInt32(&f.upNextCalls, 1)
	if len(f.upNextErrSeq) > 0 {
		idx := int(n-1)
		if idx >= len(f.upNextErrSeq) {
			idx = len(f.upNextErrSeq) - 1
		}
		if err := f.upNextErrSeq[idx]; err != nil {
			return nil, err
		}
	}
	return f.upNextResult, nil
}

// fakeAuth implements Auth with controllable token + relogin.
type fakeAuth struct {
	token        string
	hasToken     bool
	reloginCalls int32
	reloginToken string
}

func (a *fakeAuth) Token() (string, bool) { return a.token, a.hasToken }
func (a *fakeAuth) DeviceID() string      { return "dev-test" }
func (a *fakeAuth) Relogin(ctx context.Context) (string, error) {
	atomic.AddInt32(&a.reloginCalls, 1)
	a.token = a.reloginToken
	a.hasToken = true
	return a.reloginToken, nil
}

// Behavior 6: PlayTarget(UpNextTop) loads the first episode URL and State().Title matches.
func TestPlayTargetUpNextTop(t *testing.T) {
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{
		{UUID: "u1", Title: "First Episode", URL: "https://1.mp3", Duration: 1800},
		{UUID: "u2", Title: "Second", URL: "https://2.mp3"},
	}}
	p := player.NewFake()
	auth := &fakeAuth{token: "tok", hasToken: true}
	e := New(api, p, auth)
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	if p.LoadedURL != "https://1.mp3" {
		t.Fatalf("loaded %q, want first episode URL", p.LoadedURL)
	}
	if got := e.State().Title; got != "First Episode" {
		t.Fatalf("title %q, want First Episode", got)
	}
	if got := e.State().Subtitle; got != "Up Next" {
		t.Fatalf("subtitle %q", got)
	}
}

func TestPlayTargetEmptyQueue(t *testing.T) {
	api := &fakeAPI{upNextResult: nil}
	e := New(api, player.NewFake(), &fakeAuth{token: "t", hasToken: true})
	defer e.Close()
	if err := e.PlayTarget(context.Background(), UpNextTop{}); err == nil {
		t.Fatal("want error on empty queue")
	}
}

// Behavior 5a: with a cached token the engine does NOT call Login/Relogin.
func TestAuthCachedTokenNoRelogin(t *testing.T) {
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{{UUID: "u", Title: "T", URL: "x"}}}
	auth := &fakeAuth{token: "cached", hasToken: true}
	e := New(api, player.NewFake(), auth)
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	if auth.reloginCalls != 0 {
		t.Fatalf("relogin called %d times with valid token", auth.reloginCalls)
	}
	if api.upNextCalls != 1 {
		t.Fatalf("UpNext called %d times", api.upNextCalls)
	}
}

// Behavior 5b: on a 401 the engine re-logs-in once and retries.
func TestAuthReloginOn401(t *testing.T) {
	api := &fakeAPI{
		upNextResult: []pocketcasts.Episode{{UUID: "u", Title: "T", URL: "x"}},
		upNextErrSeq: []error{pocketcasts.ErrInvalidCredentials, nil},
	}
	auth := &fakeAuth{token: "stale", hasToken: true, reloginToken: "fresh"}
	e := New(api, player.NewFake(), auth)
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	if auth.reloginCalls != 1 {
		t.Fatalf("relogin called %d times, want 1", auth.reloginCalls)
	}
	if api.upNextCalls != 2 {
		t.Fatalf("UpNext called %d times, want 2 (initial + retry)", api.upNextCalls)
	}
}

// Behavior 7: SkipForward seeks by the default 45s.
func TestSkipForwardBack(t *testing.T) {
	p := player.NewFake()
	e := New(&fakeAPI{}, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	_ = p.Seek(100 * time.Second)
	e.SkipForward()
	if got := p.State().Position; got != 145*time.Second {
		t.Fatalf("after skip forward: %v, want 145s", got)
	}
	e.SkipBack()
	if got := p.State().Position; got != 100*time.Second {
		t.Fatalf("after skip back: %v, want 100s", got)
	}
	// clamp at zero
	_ = p.Seek(10 * time.Second)
	e.SkipBack()
	if got := p.State().Position; got != 0 {
		t.Fatalf("skip back below zero: %v, want 0", got)
	}
}
