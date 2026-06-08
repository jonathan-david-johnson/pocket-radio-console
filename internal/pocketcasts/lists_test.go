package pocketcasts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// podcastListMsg encodes Api_UserPodcastListResponse { podcasts(1)=repeated
// { uuid(1), title(4) } }.
func podcastListMsg(podcasts ...SubscribedPodcast) []byte {
	var out []byte
	for _, p := range podcasts {
		sub := append(encodeStringField(1, p.UUID), encodeStringField(4, p.Title)...)
		out = append(out, encodeLengthDelimitedField(1, sub)...)
	}
	return out
}

// Behavior 1: PodcastList → fan-out FullEpisodes → filter last 14 days → sorted
// desc. Both endpoints mocked; assert the cutoff and order.
func TestNewReleases_CutoffAndOrder(t *testing.T) {
	now := time.Now().UTC()
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	full := map[string]string{
		"p1": fmt.Sprintf(`{"podcast":{"title":"Pod One","episodes":[
			{"uuid":"e1-recent","title":"recent 1","url":"https://e1","duration":100,"published":%q},
			{"uuid":"e1-old","title":"too old","url":"https://eo","duration":100,"published":%q}
		]}}`, rfc(-2*24*time.Hour), rfc(-40*24*time.Hour)),
		"p2": fmt.Sprintf(`{"podcast":{"title":"Pod Two","episodes":[
			{"uuid":"e2-newest","title":"newest","url":"https://e2","duration":100,"published":%q}
		]}}`, rfc(-1*time.Hour)),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/podcast/list":
			w.Write(podcastListMsg(
				SubscribedPodcast{UUID: "p1", Title: "Pod One"},
				SubscribedPodcast{UUID: "p2", Title: "Pod Two"},
			))
		case strings.HasPrefix(r.URL.Path, "/mobile/podcast/full/"):
			uuid := strings.TrimPrefix(r.URL.Path, "/mobile/podcast/full/")
			w.Write([]byte(full[uuid]))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, CacheBaseURL: srv.URL, HTTP: srv.Client()}
	rel, err := c.NewReleases(context.Background(), "tok", 14)
	if err != nil {
		t.Fatal(err)
	}

	// Old episode excluded by the 14-day cutoff.
	if len(rel) != 2 {
		t.Fatalf("got %d releases, want 2: %+v", len(rel), rel)
	}
	// Sorted by publish date descending: e2-newest (-1h) before e1-recent (-2d).
	if rel[0].UUID != "e2-newest" || rel[1].UUID != "e1-recent" {
		t.Fatalf("wrong order: %s, %s", rel[0].UUID, rel[1].UUID)
	}
	if rel[0].PodcastTitle != "Pod Two" {
		t.Errorf("podcast title not carried: %q", rel[0].PodcastTitle)
	}
}

// Behavior 2: htmlToPlainText strips tags, decodes entities, collapses whitespace.
func TestHTMLToPlainText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>Hello</p><p>World</p>", "Hello\nWorld"},
		{"a<br>b<br/>c", "a\nb\nc"},
		{"Tom &amp; Jerry &lt;3 &quot;hi&quot;", `Tom & Jerry <3 "hi"`},
		{"<a href=\"x\">link</a> text", "link text"},
		{"line1   \nline2", "line1\nline2"},
		{"a\n\n\n\n\nb", "a\n\nb"},
		{"  &nbsp; trim &hellip;  ", "trim …"},
	}
	for _, c := range cases {
		if got := htmlToPlainText(c.in); got != c.want {
			t.Errorf("htmlToPlainText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Behavior 2: ShowNotes decodes the per-podcast response keyed by episode uuid
// and returns plain text + image.
func TestShowNotes_Decode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mobile/show_notes/full/pod1" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"podcast":{"episodes":[
			{"uuid":"other","show_notes":"<p>nope</p>","image":"https://x"},
			{"uuid":"ep1","show_notes":"<p>Hello &amp; welcome</p>","image":"https://img"}
		]}}`))
	}))
	defer srv.Close()

	c := &Client{CacheBaseURL: srv.URL, HTTP: srv.Client()}
	notes, err := c.ShowNotes(context.Background(), "pod1", "ep1")
	if err != nil {
		t.Fatal(err)
	}
	if notes.Description != "Hello & welcome" {
		t.Errorf("description = %q", notes.Description)
	}
	if notes.ImageURL != "https://img" {
		t.Errorf("image = %q", notes.ImageURL)
	}
}
