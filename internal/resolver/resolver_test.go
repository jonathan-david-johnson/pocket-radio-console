package resolver

import (
	"context"
	"errors"
	"testing"

	"pocket-radio-console/internal/radio"
)

func favs() []radio.Station {
	return []radio.Station{
		{ID: "1", Name: "KEXP 90.3 FM", StreamURL: "https://kexp"},
		{ID: "2", Name: "KCRW Eclectic24", StreamURL: "https://kcrw"},
		{ID: "3", Name: "Radio Paradise", StreamURL: "https://rp"},
	}
}

func noSearch(t *testing.T) SearchFunc {
	return func(context.Context, string) ([]radio.Station, error) {
		t.Fatal("search should not be called")
		return nil, nil
	}
}

// Behavior 1: reserved words resolve to their Targets without the network.
func TestResolve_ReservedWords(t *testing.T) {
	for word, want := range map[string]Kind{"up_next": PlayUpNextTop, "new": PlayNewest} {
		res, err := Resolve(context.Background(), word, favs(), noSearch(t))
		if err != nil {
			t.Fatalf("%s: %v", word, err)
		}
		if res.Kind != want {
			t.Fatalf("%s: kind %v, want %v", word, res.Kind, want)
		}
	}
}

// Behavior 2: a favorite substring match resolves without a search call.
func TestResolve_FavoriteHit(t *testing.T) {
	res, err := Resolve(context.Background(), "kcrw", favs(), noSearch(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != PlayStation || res.Station.ID != "2" {
		t.Fatalf("got %+v", res)
	}
}

// Behavior 3: a favorite miss falls through to search and returns the top hit.
func TestResolve_FavMissToSearch(t *testing.T) {
	called := false
	search := func(ctx context.Context, q string) ([]radio.Station, error) {
		called = true
		if q != "jazz24" {
			t.Fatalf("search query %q", q)
		}
		return []radio.Station{
			{ID: "top", Name: "Jazz24"},
			{ID: "other", Name: "Jazz FM"},
		}, nil
	}
	res, err := Resolve(context.Background(), "jazz24", favs(), search)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("search not called")
	}
	if res.Kind != PlayStation || res.Station.ID != "top" {
		t.Fatalf("want top hit, got %+v", res)
	}
}

// Behavior 4: no match returns NoMatchError with nearest favorite names.
func TestResolve_NoMatch(t *testing.T) {
	search := func(context.Context, string) ([]radio.Station, error) { return nil, nil }
	_, err := Resolve(context.Background(), "zzzz", favs(), search)
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("want NoMatchError, got %v", err)
	}
	if len(nm.Suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	// Every suggestion must be a real favorite name.
	names := map[string]bool{}
	for _, f := range favs() {
		names[f.Name] = true
	}
	for _, s := range nm.Suggestions {
		if !names[s] {
			t.Fatalf("suggestion %q is not a favorite name", s)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Fatalf("levenshtein(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
