package main

// Normalising the date a warning was issued.
//
// Every coordinator stamps its warnings differently: France and Spain emit
// ISO-8601, Pakistan names the file after the date, and the rest use the radio
// date-time group a message is actually broadcast with — "161314Z SEP 26" or
// "161830 UTC sep 26". Six of the twelve sources publish no machine-readable
// date at all and only carry a DTG inside the message text.
//
// Warning.Issued keeps whatever the source said, because that is what a
// navigator will compare against a printed bulletin. Warning.IssuedAt is the
// same instant in RFC 3339 UTC, and is what the app sorts and ages by. It is
// empty when no date could be established — an invented timestamp on a
// navigational warning is worse than none.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// 161314Z SEP 26 — day, hour, minute, month, two-digit year.
	reDTG = regexp.MustCompile(`(?i)\b(\d{2})(\d{2})(\d{2})Z?\s*(?:UTC\s*)?([A-Z]{3})\s*(\d{2}|\d{4})\b`)
	// Japan's detail pages: Date:2026/09/09 12 UTC
	reSlashDate = regexp.MustCompile(`(\d{4})/(\d{2})/(\d{2})(?:\s+(\d{1,2}))?`)
	// Chile's bulletin header: DATE: SEP/08/2026
	reMonthSlash = regexp.MustCompile(`(?i)\b([A-Z]{3})/(\d{2})/(\d{4})\b`)
	// The same group with the year left off: "101105 UTC SEP". Tried only after
	// the full form, so a message carrying a year is never misread.
	reDTGNoYear = regexp.MustCompile(`(?i)\b(\d{2})(\d{2})(\d{2})Z?\s*(?:UTC\s*)?([A-Z]{3})\b`)
)

var months = map[string]time.Month{
	"JAN": time.January, "FEB": time.February, "MAR": time.March,
	"APR": time.April, "MAY": time.May, "JUN": time.June,
	"JUL": time.July, "AUG": time.August, "SEP": time.September,
	"OCT": time.October, "NOV": time.November, "DEC": time.December,
}

// clock is time.Now, replaceable in tests.
var clock = time.Now

// How far ahead of the build a stamp may sit and still be believed. A message
// broadcast an hour before the build can legitimately read as slightly ahead
// once clocks and rounding are allowed for; a day and a half is generous
// enough to never reject a real one.
const futureTolerance = 36 * time.Hour

// normalizeIssued turns whatever a source gave into RFC 3339 UTC.
//
// `raw` is the source's own stamp, `text` the warning body to fall back on,
// and `year` the year the warning belongs to — needed because a date-time
// group carries no century, and some carry no year at all.
//
// A date in the future is refused. Messages state operational times as well as
// publication times — "DAILY 15 SEP TO 15 OCT", "231100Z TO 231700Z SEP" — and
// a scan of the body can pick up the wrong one. Whatever the cause, a
// publication date that has not happened yet means the source was not
// understood, and the app would show it under "Latest" as due in three weeks.
// An empty date is honest; the source's own stamp is published verbatim
// alongside it either way.
func normalizeIssued(raw, text string, year int) string {
	horizon := clock().UTC().Add(futureTolerance)
	for _, candidate := range []string{raw, text} {
		if t, ok := parseAnyDate(candidate, year); ok && t.UTC().Before(horizon) {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func parseAnyDate(s string, year int) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// The ISO shapes France, Spain and Pakistan already produce.
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05.999999Z", "2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if m := reDTG.FindStringSubmatch(s); m != nil {
		if t, ok := fromDTG(m, year); ok {
			return t, true
		}
	}
	// The Baltic NAVTEX page stamps its messages "101105 UTC SEP", with no
	// year at all — the warning's own year is the only thing to go on.
	if m := reDTGNoYear.FindStringSubmatch(s); m != nil {
		if t, ok := fromDTG([]string{m[0], m[1], m[2], m[3], m[4], ""}, year); ok {
			return t, true
		}
	}
	if m := reMonthSlash.FindStringSubmatch(s); m != nil {
		if mon, ok := months[strings.ToUpper(m[1])]; ok {
			day, _ := strconv.Atoi(m[2])
			y, _ := strconv.Atoi(m[3])
			return time.Date(y, mon, day, 0, 0, 0, 0, time.UTC), true
		}
	}
	if m := reSlashDate.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		hour, _ := strconv.Atoi(m[4])
		if y > 1990 && mo >= 1 && mo <= 12 && day >= 1 && day <= 31 {
			return time.Date(y, time.Month(mo), day, hour, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

func fromDTG(m []string, year int) (time.Time, bool) {
	day, _ := strconv.Atoi(m[1])
	hour, _ := strconv.Atoi(m[2])
	minute, _ := strconv.Atoi(m[3])
	mon, ok := months[strings.ToUpper(m[4])]
	if !ok || day < 1 || day > 31 || hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	y, _ := strconv.Atoi(m[5])
	switch {
	case y >= 1990: // already four digits
	case y > 0:
		y += 2000
	default:
		y = year
	}
	if y < 1990 || y > 2100 {
		return time.Time{}, false
	}
	return time.Date(y, mon, day, hour, minute, 0, 0, time.UTC), true
}

// ageDays is how old a normalised timestamp is, for reporting.
func ageDays(rfc3339 string, now time.Time) (int, bool) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return 0, false
	}
	return int(now.Sub(t).Hours() / 24), true
}
