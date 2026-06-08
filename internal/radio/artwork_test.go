package radio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Behavior 8 (M5 scope): iTunes Search fallback returns a 600×600 art URL and
// passes the artist+title as the query term.
func TestArtwork_UpgradesTo600(t *testing.T) {
	var gotTerm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTerm = r.URL.Query().Get("term")
		w.Write([]byte(`{"results":[
			{"artworkUrl100":"https://is1.example/a/b/100x100bb.jpg"}
		]}`))
	}))
	defer srv.Close()

	c := &Client{ITunesBase: srv.URL, HTTP: srv.Client()}
	got, err := c.Artwork(context.Background(), "Bonobo", "Kerala")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://is1.example/a/b/600x600bb.jpg"; got != want {
		t.Errorf("art url = %q, want %q", got, want)
	}
	if gotTerm != "Bonobo Kerala" {
		t.Errorf("term = %q", gotTerm)
	}
}

func TestArtwork_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	c := &Client{ITunesBase: srv.URL, HTTP: srv.Client()}
	got, err := c.Artwork(context.Background(), "Nobody", "Nothing")
	if err != nil || got != "" {
		t.Fatalf("got %q, err %v; want empty", got, err)
	}
}
