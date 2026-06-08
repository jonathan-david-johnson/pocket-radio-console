// Package resolver turns a mini-mode argument into a play Resolution. It is a
// deep, pure-ish module: all I/O is injected as a search function, so the
// resolution rules are tested without the network.
//
// Resolution order for a non-reserved arg:
//
//	favorite name substring → radio-browser search top hit → NoMatchError.
package resolver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"pocket-radio-console/internal/radio"
)

// Kind is what an argument resolved to.
type Kind int

const (
	// PlayUpNextTop is the reserved word "up_next".
	PlayUpNextTop Kind = iota
	// PlayNewest is the reserved word "new".
	PlayNewest
	// PlayStation is a resolved favorite or search hit.
	PlayStation
)

// Resolution is the result of resolving an argument.
type Resolution struct {
	Kind    Kind
	Station radio.Station // set when Kind == PlayStation
}

// SearchFunc performs a station search (radio-browser). Injected for testing.
type SearchFunc func(ctx context.Context, query string) ([]radio.Station, error)

// NoMatchError is returned when an argument matches nothing.
type NoMatchError struct {
	Arg         string
	Suggestions []string // nearest favorite names
}

func (e *NoMatchError) Error() string {
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("no match for %q", e.Arg)
	}
	return fmt.Sprintf("no match for %q; did you mean: %s", e.Arg, strings.Join(e.Suggestions, ", "))
}

// ReservedWords are mini-mode arguments with fixed meaning.
var ReservedWords = map[string]Kind{
	"up_next": PlayUpNextTop,
	"new":     PlayNewest,
}

// Resolve maps arg to a Resolution. Reserved words resolve without touching the
// network; a favorite substring match skips search; otherwise search runs and
// its top hit wins, or a NoMatchError with nearest-favorite suggestions returns.
func Resolve(ctx context.Context, arg string, favorites []radio.Station, search SearchFunc) (Resolution, error) {
	arg = strings.TrimSpace(arg)

	if kind, ok := ReservedWords[strings.ToLower(arg)]; ok {
		return Resolution{Kind: kind}, nil
	}

	if st, ok := matchFavorite(arg, favorites); ok {
		return Resolution{Kind: PlayStation, Station: st}, nil
	}

	hits, err := search(ctx, arg)
	if err != nil {
		return Resolution{}, err
	}
	if len(hits) > 0 {
		return Resolution{Kind: PlayStation, Station: hits[0]}, nil
	}

	return Resolution{}, &NoMatchError{Arg: arg, Suggestions: suggest(arg, favorites)}
}

// matchFavorite returns the favorite whose name contains arg (case-insensitive).
func matchFavorite(arg string, favorites []radio.Station) (radio.Station, bool) {
	needle := strings.ToLower(arg)
	if needle == "" {
		return radio.Station{}, false
	}
	for _, f := range favorites {
		if strings.Contains(strings.ToLower(f.Name), needle) {
			return f, true
		}
	}
	return radio.Station{}, false
}

// suggest ranks favorite names by edit distance to arg, returning the nearest few.
func suggest(arg string, favorites []radio.Station) []string {
	if len(favorites) == 0 {
		return nil
	}
	type scored struct {
		name string
		dist int
	}
	ranked := make([]scored, 0, len(favorites))
	low := strings.ToLower(arg)
	for _, f := range favorites {
		ranked = append(ranked, scored{f.Name, levenshtein(low, strings.ToLower(f.Name))})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].dist < ranked[j].dist })

	n := 3
	if len(ranked) < n {
		n = len(ranked)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = ranked[i].name
	}
	return out
}

// levenshtein is the classic edit distance between two strings.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
