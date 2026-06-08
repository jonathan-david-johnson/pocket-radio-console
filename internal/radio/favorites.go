package radio

import "sort"

// FavoriteIDs returns the station IDs in their current display order. Persist
// this to state.json to remember a user's manual ordering.
func FavoriteIDs(stations []Station) []string {
	ids := make([]string, len(stations))
	for i, s := range stations {
		ids[i] = s.ID
	}
	return ids
}

// OrderFavorites returns stations sorted to match order (a slice of station IDs).
// Stations absent from order are appended in their original relative order, so a
// newly favorited station that predates the saved order still shows up.
func OrderFavorites(stations []Station, order []string) []Station {
	if len(order) == 0 {
		return stations
	}
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}
	known := make([]Station, 0, len(stations))
	unknown := make([]Station, 0)
	for _, s := range stations {
		if _, ok := pos[s.ID]; ok {
			known = append(known, s)
		} else {
			unknown = append(unknown, s)
		}
	}
	sort.SliceStable(known, func(i, j int) bool {
		return pos[known[i].ID] < pos[known[j].ID]
	})
	return append(known, unknown...)
}

// MoveFavorite moves the station at index by delta positions (negative = up),
// clamping to the bounds of the slice. Returns a new slice; the input is
// unchanged.
func MoveFavorite(stations []Station, index, delta int) []Station {
	n := len(stations)
	if index < 0 || index >= n {
		return stations
	}
	target := index + delta
	if target < 0 {
		target = 0
	}
	if target >= n {
		target = n - 1
	}
	if target == index {
		return stations
	}
	out := make([]Station, 0, n)
	out = append(out, stations...)
	s := out[index]
	out = append(out[:index], out[index+1:]...)
	// re-insert at target
	out = append(out, Station{})
	copy(out[target+1:], out[target:])
	out[target] = s
	return out
}
