package main

import "testing"

// The area name is what the app groups by, so a coordinator spelling its own
// NAVAREA differently must not split one ocean area into two list entries.
func TestNormalizeArea(t *testing.T) {
	cases := []struct{ in, want string }{
		// The three spellings actually observed in the live sources.
		{"II", "II"},                 // UK, Spain, Pakistan, Japan, …
		{"NAVAREA II", "II"},         // France's ocean series
		{"NAVAREA IV", "IV"},         // NGA's daily bulletins
		{" NAVAREA XVIII ", "XVIII"}, // stray whitespace from HTML sources
		// Series that are not NAVAREAs keep their published name.
		{"HYDROLANT", "HYDROLANT"},
		{"AUSCOAST", "AUSCOAST"},
		{"UK Coastal", "UK Coastal"},
		{"AVURNAV LOCAL CHERBOURG", "AVURNAV LOCAL CHERBOURG"},
		{"NAVTEX: Gulf of Riga", "NAVTEX: Gulf of Riga"},
		// The word alone, or followed by something that is not a numeral, is
		// not evidence of an area and must survive untouched.
		{"NAVAREA", "NAVAREA"},
		{"NAVAREA BREST", "NAVAREA BREST"},
	}
	for _, c := range cases {
		if got := normalizeArea(c.in); got != c.want {
			t.Errorf("normalizeArea(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsRomanArea(t *testing.T) {
	// All 21 NAVAREAs exist; there is no XXII, and lowercase is not a numeral.
	for _, s := range []string{"I", "XXI", "XIII"} {
		if !isRomanArea(s) {
			t.Errorf("isRomanArea(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"XXII", "i", "", "IIII", "A"} {
		if isRomanArea(s) {
			t.Errorf("isRomanArea(%q) = true, want false", s)
		}
	}
}
