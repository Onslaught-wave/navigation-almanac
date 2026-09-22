package main

import (
	"testing"
	"time"
)

func withClock(t *testing.T, at string) {
	t.Helper()
	fixed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("bad test clock %q: %v", at, err)
	}
	previous := clock
	clock = func() time.Time { return fixed }
	t.Cleanup(func() { clock = previous })
}

// The shapes the twelve coordinators actually publish.
func TestNormalizeIssuedAcceptsEverySourcesStamp(t *testing.T) {
	withClock(t, "2026-09-22T12:00:00Z")
	cases := []struct{ name, raw, text, want string }{
		{"france ISO", "2026-09-18T14:00:00Z", "", "2026-09-18T14:00:00Z"},
		{"spain date only", "2026-09-18", "", "2026-09-18T00:00:00Z"},
		{"broadcast DTG", "181100Z SEP 26", "", "2026-09-18T11:00:00Z"},
		{"DTG with UTC", "161830 UTC SEP 26", "", "2026-09-16T18:30:00Z"},
		{"japan page stamp", "2026/08/27 12", "", "2026-08-27T12:00:00Z"},
		{"chile header", "SEP/08/2026", "", "2026-09-08T00:00:00Z"},
		// The Baltic NAVTEX page omits the year; the warning's own year is all
		// there is to go on.
		{"yearless DTG", "180800 UTC SEP", "", "2026-09-18T08:00:00Z"},
		// Six coordinators publish no date field at all.
		{"from the body", "", "SOMETHING AT 181100Z SEP 26.", "2026-09-18T11:00:00Z"},
		{"nothing anywhere", "", "SHOALS LOCATED AT: 71-21.06N", ""},
	}
	for _, c := range cases {
		if got := normalizeIssued(c.raw, c.text, 2026); got != c.want {
			t.Errorf("%s: normalizeIssued(%q, %q) = %q, want %q",
				c.name, c.raw, c.text, got, c.want)
		}
	}
}

// A publication date that has not happened yet means the source was not
// understood. Both of these are real messages that produced one.
func TestAFutureDateIsRefused(t *testing.T) {
	withClock(t, "2026-09-22T12:00:00Z")

	// Canada, NAVAREA XVII 83/26: no date field, and the body states the
	// hazard period rather than a publication time.
	canada := "1. ROCKET LAUNCH FALLOUT HAZARD FROM 0128 TO 0244 UTC " +
		"DAILY 15 SEP TO 15 OCT 26 IN AREA BOUNDED BY:"
	if got := normalizeIssued("", canada, 2026); got != "" {
		t.Errorf("the hazard period must not become a publication date, got %q", got)
	}

	// A source-stated stamp is no more believable when it is ahead of us.
	if got := normalizeIssued("240800 UTC SEP", "", 2026); got != "" {
		t.Errorf("a stamp two days ahead must be refused, got %q", got)
	}
}

// The tolerance exists for a coordinator's clock running fast, or a message
// published while the build was already running — minutes, not hours.
func TestAStampJustAheadIsStillBelieved(t *testing.T) {
	withClock(t, "2026-09-22T12:00:00Z")
	if got := normalizeIssued("2026-09-22T13:00:00Z", "", 2026); got != "2026-09-22T13:00:00Z" {
		t.Errorf("an hour ahead must survive, got %q", got)
	}
	// Twenty-one hours is the Estonian firing-practice window that got through
	// when the tolerance was a day and a half.
	if got := normalizeIssued("2026-09-23T09:00:00Z", "", 2026); got != "" {
		t.Errorf("twenty-one hours ahead is not clock skew, got %q", got)
	}
}

// The Baltic page relays other countries' warnings verbatim, and those state
// an exercise window in the same shape as a broadcast stamp.
func TestSwedenIgnoresTheTailOfATimeRange(t *testing.T) {
	estonian := "ESTONIAN NAV WARN\n 155/26\nGULF OF FINLAND.\n" +
		"230600-231200 UTC SEP\nFIRING PRACTICE AREA 1B"
	if got := swedenDTG(estonian); got != "" {
		t.Errorf("a firing window is not a publication stamp, got %q", got)
	}
	// A real stamp on its own is still taken.
	swedish := "SWEDISH NAV WARN\n 042/26\n130830 UTC SEP 26\nSOUTHERN BALTIC."
	if got := swedenDTG(swedish); got != "130830 UTC SEP 26" {
		t.Errorf("swedenDTG = %q, want the message's own stamp", got)
	}
	// And a stamp that follows a range elsewhere in the message is reachable.
	both := "230600-231200 UTC SEP\nISSUED 210900 UTC SEP 26"
	if got := swedenDTG(both); got != "210900 UTC SEP 26" {
		t.Errorf("swedenDTG = %q, want the stamp after the range", got)
	}
}

// Japan states its publication date on every page. Reading it is what stops
// the body scan from finding an operational time instead.
func TestJapanPageStampIsPreferredOverTheBody(t *testing.T) {
	withClock(t, "2026-09-22T12:00:00Z")
	text := "NAVAREA XI\n NO.26-0392 Date:2026/08/27 12 UTC \n" +
		"NORTH PACIFIC, NANPO SHOTO.\nGUNNERY. 2300Z TO 0900Z COMMENCING DAILY\n" +
		"31 AUG TO 29 SEP."
	stamp := reJapanDate.FindStringSubmatch(text)
	if stamp == nil {
		t.Fatal("the page stamp was not recognised")
	}
	if got := normalizeIssued(stamp[1], text, 2026); got != "2026-08-27T12:00:00Z" {
		t.Errorf("normalizeIssued = %q, want the page's own 2026-08-27T12:00:00Z", got)
	}
}
