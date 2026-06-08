package pocketcasts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Behavior 3: Login against an httptest server — 200 → Session; 401/403 → ErrInvalidCredentials.
func TestLogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("empty login body")
		}
		resp := append(encodeStringField(1, "the-token"), encodeStringField(2, "the-uuid")...)
		resp = append(resp, encodeStringField(3, "u@e.com")...)
		w.Write(resp)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	sess, err := c.Login(context.Background(), "u@e.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Token != "the-token" || sess.UUID != "the-uuid" {
		t.Fatalf("got %+v", sess)
	}
}

func TestLogin_InvalidCredentials(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
		_, err := c.Login(context.Background(), "u@e.com", "bad")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("code %d: want ErrInvalidCredentials, got %v", code, err)
		}
		srv.Close()
	}
}

func TestUpNext_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("missing bearer, got %q", got)
		}
		ep := episodeMsg("E1", "https://e1.mp3", "p1", "u1", time.Time{})
		body := encodeLengthDelimitedField(4, ep)
		body = append(body, encodeLengthDelimitedField(5, syncMsg("u1", 5, 60))...)
		w.Write(body)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	eps, err := c.UpNext(context.Background(), "tok", "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Title != "E1" || eps[0].PlayedUpTo != 5 {
		t.Fatalf("got %+v", eps)
	}
}

func TestUpNext_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := c.UpNext(context.Background(), "tok", "dev"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
}
