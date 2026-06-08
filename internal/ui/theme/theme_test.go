package theme

import "testing"

// Behavior 4: hex values match the menubar palette (Constants.swift).
func TestPaletteHexValues(t *testing.T) {
	cases := []struct {
		name       string
		light, dark string
	}{
		{"PrimaryUi01", "#FFFFFF", "#292B2E"},
		{"PrimaryUi04", "#F7F9FA", "#161718"},
		{"PrimaryUi05", "#E0E6EA", "#393A3C"},
		{"PrimaryText01", "#292B2E", "#FFFFFF"},
		{"PrimaryText02", "#8F97A4", "#9C9FA4"},
		{"PrimaryIcon02", "#B8C3C9", "#8F97A4"},
		{"Accent", "#F43E37", "#F44336"},
	}
	actuals := map[string][2]string{
		"PrimaryUi01":   {PrimaryUi01.Light, PrimaryUi01.Dark},
		"PrimaryUi04":   {PrimaryUi04.Light, PrimaryUi04.Dark},
		"PrimaryUi05":   {PrimaryUi05.Light, PrimaryUi05.Dark},
		"PrimaryText01": {PrimaryText01.Light, PrimaryText01.Dark},
		"PrimaryText02": {PrimaryText02.Light, PrimaryText02.Dark},
		"PrimaryIcon02": {PrimaryIcon02.Light, PrimaryIcon02.Dark},
		"Accent":        {Accent.Light, Accent.Dark},
	}
	for _, c := range cases {
		got := actuals[c.name]
		if got[0] != c.light {
			t.Errorf("%s light: got %q, want %q", c.name, got[0], c.light)
		}
		if got[1] != c.dark {
			t.Errorf("%s dark: got %q, want %q", c.name, got[1], c.dark)
		}
	}
}
