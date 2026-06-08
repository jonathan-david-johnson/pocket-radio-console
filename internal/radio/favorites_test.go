package radio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Behavior 3: AddFavorite/RemoveFavorite hit Supabase with the right method +
// headers (apikey, x-user-uuid, Prefer) against httptest.
func TestAddRemoveFavorite_MethodAndHeaders(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		hdr       http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		hdr = r.Header
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{SupabaseURL: srv.URL, SupabaseKey: "key-123", HTTP: srv.Client()}

	t.Run("add", func(t *testing.T) {
		if err := c.AddFavorite(context.Background(), "user-1", "st-1"); err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodPost {
			t.Errorf("method = %s, want POST", gotMethod)
		}
		if gotPath != "/rest/v1/radio_favorites" {
			t.Errorf("path = %s", gotPath)
		}
		if hdr.Get("apikey") != "key-123" {
			t.Errorf("apikey = %q", hdr.Get("apikey"))
		}
		if hdr.Get("x-user-uuid") != "user-1" {
			t.Errorf("x-user-uuid = %q", hdr.Get("x-user-uuid"))
		}
		if !strings.Contains(hdr.Get("Prefer"), "resolution=merge-duplicates") {
			t.Errorf("Prefer = %q, want upsert", hdr.Get("Prefer"))
		}
	})

	t.Run("remove", func(t *testing.T) {
		if err := c.RemoveFavorite(context.Background(), "user-1", "st-1"); err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", gotMethod)
		}
		if !strings.Contains(gotQuery, "station_id=eq.st-1") || !strings.Contains(gotQuery, "user_uuid=eq.user-1") {
			t.Errorf("query = %q", gotQuery)
		}
		if hdr.Get("apikey") != "key-123" || hdr.Get("x-user-uuid") != "user-1" {
			t.Errorf("missing auth headers: %v", hdr)
		}
	})
}

// Behavior 4: a reorder persists to state.json; re-applying the saved order on
// reload preserves it, with unknown (newly favorited) stations appended.
func TestFavoritesReorderPersistAndReapply(t *testing.T) {
	a := Station{ID: "A", Name: "Alpha"}
	b := Station{ID: "B", Name: "Bravo"}
	c := Station{ID: "C", Name: "Charlie"}

	// User moves C (index 2) to the top.
	moved := MoveFavorite([]Station{a, b, c}, 2, -2)
	if got := FavoriteIDs(moved); !reflect.DeepEqual(got, []string{"C", "A", "B"}) {
		t.Fatalf("after move: %v", got)
	}
	order := FavoriteIDs(moved)

	// On reload the server returns favorites in its own order, plus a brand-new
	// favorite D that isn't in the saved order yet.
	d := Station{ID: "D", Name: "Delta"}
	server := []Station{a, b, c, d}
	got := FavoriteIDs(OrderFavorites(server, order))
	if !reflect.DeepEqual(got, []string{"C", "A", "B", "D"}) {
		t.Fatalf("after reapply: %v", got)
	}
}
