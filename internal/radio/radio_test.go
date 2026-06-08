package radio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Behavior 5: Favorites — Supabase IDs → radio-browser byuuid lookups → sorted.
func TestFavorites_MockBothHosts(t *testing.T) {
	// radio-browser host: byuuid/<id> returns a one-station array.
	browser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/stations/byuuid/")
		switch id {
		case "id-z":
			w.Write([]byte(`[{"stationuuid":"id-z","name":"Zeta Radio","url_resolved":"https://z","favicon":"f","bitrate":128}]`))
		case "id-a":
			w.Write([]byte(`[{"stationuuid":"id-a","name":"Alpha Radio","url_resolved":"https://a"}]`))
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer browser.Close()

	supabase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") == "" || r.Header.Get("x-user-uuid") != "user-1" {
			t.Errorf("missing supabase headers: %v", r.Header)
		}
		w.Write([]byte(`[{"station_id":"id-z"},{"station_id":"id-a"}]`))
	}))
	defer supabase.Close()

	c := &Client{
		SupabaseURL:  supabase.URL,
		SupabaseKey:  "key",
		RadioBrowser: browser.URL,
		HTTP:         http.DefaultClient,
	}
	stations, err := c.Favorites(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(stations), stations)
	}
	// Sorted by name → Alpha before Zeta.
	if stations[0].Name != "Alpha Radio" || stations[1].Name != "Zeta Radio" {
		t.Fatalf("not sorted: %+v", stations)
	}
	if stations[1].StreamURL != "https://z" || stations[1].Bitrate != 128 {
		t.Fatalf("metadata not mapped: %+v", stations[1])
	}
}

func TestSearch_TopHitOrdering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "jazz" {
			t.Errorf("query name=%q", r.URL.Query().Get("name"))
		}
		w.Write([]byte(`[{"stationuuid":"1","name":"Jazz24","url_resolved":"https://j"},{"stationuuid":"2","name":"No Stream"}]`))
	}))
	defer srv.Close()
	c := &Client{RadioBrowser: srv.URL, HTTP: http.DefaultClient}
	got, err := c.Search(context.Background(), "jazz")
	if err != nil {
		t.Fatal(err)
	}
	// Row without a stream URL is dropped.
	if len(got) != 1 || got[0].Name != "Jazz24" {
		t.Fatalf("got %+v", got)
	}
}

// Behavior 6: KCRW parser drops [BREAK]; KEXP keeps only trackplay rows.
func TestParseKCRW(t *testing.T) {
	body := []byte(`[
	  {"title":"Song A","artist":"Artist A","album":"Alb","datetime":"2026-06-07T10:00:00Z"},
	  {"title":"Break","artist":"[BREAK]"},
	  {"title":"Song B","artist":"Artist B"}
	]`)
	tracks := parseKCRW(body)
	if len(tracks) != 2 {
		t.Fatalf("want 2 (BREAK dropped), got %d: %+v", len(tracks), tracks)
	}
	if tracks[0].Label() != "Song A — Artist A" {
		t.Fatalf("label %q", tracks[0].Label())
	}
	if tracks[0].PlayedAt.IsZero() {
		t.Fatal("datetime not parsed")
	}
}

func TestParseKEXP(t *testing.T) {
	body := []byte(`{"results":[
	  {"play_type":"trackplay","song":"S1","artist":"A1","airdate":"2026-06-07T10:00:00Z"},
	  {"play_type":"airbreak"},
	  {"play_type":"trackplay","song":"S2","artist":"A2"}
	]}`)
	tracks := parseKEXP(body)
	if len(tracks) != 2 {
		t.Fatalf("want 2 trackplays, got %d: %+v", len(tracks), tracks)
	}
	if tracks[0].Label() != "S1 — A1" {
		t.Fatalf("label %q", tracks[0].Label())
	}
}

func TestHasTracklist(t *testing.T) {
	c := NewClient()
	if !c.HasTracklist(Station{Name: "KCRW Eclectic"}) {
		t.Fatal("KCRW should have tracklist")
	}
	if !c.HasTracklist(Station{Name: "KEXP 90.3"}) {
		t.Fatal("KEXP should have tracklist")
	}
	if c.HasTracklist(Station{Name: "Radio Paradise"}) {
		t.Fatal("Radio Paradise should not")
	}
}
