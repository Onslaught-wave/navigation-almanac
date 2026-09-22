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

// The tolerance exists so that a message broadcast just before the build is
// still believed once clocks and rounding are allowed for.
func TestAStampJustAheadIsStillBelieved(t *testing.T) {
	withClock(t, "2026-09-22T12:00:00Z")
	if got := normalizeIssued("2026-09-22T23:00:00Z", "", 2026); got != "2026-09-22T23:00:00Z" {
		t.Errorf("an hours-ahead stamp must survive, got %q", got)
	}
	if got := normalizeIssued("2026-09-25T00:00:00Z", "", 2026); got != "" {
		t.Errorf("three days ahead is not rounding, got %q", got)
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
