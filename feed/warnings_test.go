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

// A message has to reach the app in the one form it can display and search.
func TestCleanText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"CRLF from Japan, Pakistan, Peru and Sweden",
			"ESTONIAN NAV WARN\r\n 155/26\r\nGULF OF FINLAND.",
			"ESTONIAN NAV WARN\n 155/26\nGULF OF FINLAND."},
		{"a bare carriage return still ends a line", "A\rB", "A\nB"},
		{"non-breaking space becomes a space", "46-04.0N 033-12.8E", "46-04.0N 033-12.8E"},
		{"a byte-order mark mid-text goes", "SHOALS\uFEFF LOCATED", "SHOALS LOCATED"},
		{"surrounding whitespace goes", "\r\n  BUOY ADRIFT  \r\n", "BUOY ADRIFT"},
		{"text that is already clean is untouched", "BUOY ADRIFT\nIN 24-31N", "BUOY ADRIFT\nIN 24-31N"},
	}
	for _, c := range cases {
		if got := cleanText(c.in); got != c.want {
			t.Errorf("%s: cleanText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The reason cleaning happens before the positions are read.
func TestAPositionWrittenWithANonBreakingSpaceIsStillFound(t *testing.T) {
	w := newWarning("test", "III", "1/26", 2026, "",
		"MINES AREA BOUNDED BY:\n46-04.0N 033-12.8E", "")
	if len(w.Coordinates) != 1 {
		t.Fatalf("got %d positions, want 1 — the non-breaking space hid it", len(w.Coordinates))
	}
	if lat := w.Coordinates[0][0]; lat < 46.06 || lat > 46.07 {
		t.Errorf("latitude %v, want about 46.0667", lat)
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
