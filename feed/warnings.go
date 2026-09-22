// Package main builds the navigational-warnings feed the app downloads.
//
// Every NAVAREA coordinator publishes differently — one REST API, one XML
// file, one plain-text file per warning, and a lot of hand-written HTML — so
// each source gets its own parser here and they all return the same Warning.
//
// Two rules run through this file:
//
//   - A source that breaks must be reported as broken. An empty NAVAREA is
//     indistinguishable, to a mariner, from "no warnings in force", so a
//     parser is never allowed to fail quietly into an empty list.
//   - A feed's own liveness is not trusted. At least one national service
//     answers HTTP 200 with data years out of date, so the newest warning a
//     source returns is what decides whether it is called stale.
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"

// Warning is one navigational warning, normalised across every coordinator.
type Warning struct {
	Source      string       `json:"source"`
	Area        string       `json:"area"` // NAVAREA in roman numerals, or a coastal series
	Number      string       `json:"number"`
	Year        int          `json:"year"`
	Issued      string       `json:"issued"`              // as published; formats differ per country
	IssuedAt    string       `json:"issued_at,omitempty"` // same instant, RFC 3339 UTC; absent if unknown
	Text        string       `json:"text"`
	Coordinates [][2]float64 `json:"coordinates"` // decimal degrees, [lat, lon]
	URL         string       `json:"url"`
}

// Source is one coordinator's parser.
type Source struct {
	Name  string
	Areas string
	Fetch func(*Client) ([]Warning, error)
}

// Blocked records the coordinators a plain HTTP client cannot read, and why.
// Kept in code rather than in a document so the published manifest always
// tells the truth about what is missing.
// Each note records what was actually established, not a guess. Two of these
// are dead ends rather than obstacles: India's file has not been updated since
// July 2025, and South Africa publishes no radio warnings at all.
var Blocked = []struct{ Name, Areas, Why string }{
	{"brazil", "V", "Cloudflare managed challenge (cf-mitigated: challenge) on both " +
		"www. and assets.marinha.mil.br — no header or TLS trick passes it. The data " +
		"behind it is the best of any coordinator: avradio_NN.json, bilingual, with " +
		"decimal coordinates, refreshed continuously. Needs a headless browser."},
	{"newzealand", "XIV", "Cloudflare managed challenge on maritimenz.govt.nz. " +
		"Needs a headless browser."},
	{"india", "VIII", "the 'in-force' PDF at hydrobharat.gov.in was last modified " +
		"2025-07-07 and still says 'as on 07 Jul 2025' — over a year stale. Its text " +
		"is also drawn with per-page subsetted fonts, so reading it would need a full " +
		"PDF resource graph; not worth it for frozen data."},
	{"argentina", "VI", "the SHN radioavisos page carries no warning list — only an " +
		"explanation of the service. No listing found anywhere on hidro.gov.ar."},
	{"southafrica", "VII", "SANHO's 'NAVAREA VII Messages' page is navigation only. " +
		"It publishes monthly Notices to Mariners PDFs, which are a different product " +
		"from radio navigational warnings — there is no warning feed to read."},
	{"russia", "XIII, XX, XXI", "structure.mil.ru and nsr.rosatom.ru time out from " +
		"here; may be reachable from other networks, untested"},
}

// Sources is every coordinator with a working parser, in publication order.
var Sources = []Source{
	{"france", "II + French coastal", fetchFrance},
	{"spain", "III", fetchSpain},
	{"uk", "I + UK coastal", fetchUK},
	{"sweden", "Baltic NAVTEX", fetchSweden},
	{"norway", "XIX", fetchNorway},
	{"australia", "X + AUSCOAST", fetchAustralia},
	{"canada", "XVII, XVIII", fetchCanada},
	{"peru", "XVI", fetchPeru},
	{"pakistan", "IX", fetchPakistan},
	{"japan", "XI", fetchJapan},
	{"chile", "XV", fetchChile},
	{"usa", "IV, XII + HYDRO", fetchUSA},
}

// ---------------------------------------------------------------- fetching

// Client is a retrying HTTP client. Several of these national servers drop
// connections at random, and a single miss would blank out a whole NAVAREA.
type Client struct{ http *http.Client }

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 60 * time.Second}}
}

// Post sends a form body — one coordinator's index is only reachable that way.
func (c *Client) Post(rawURL, form, referer string) ([]byte, error) {
	return c.do("POST", rawURL, form, "text/xml,*/*", referer)
}

func (c *Client) Get(rawURL string, accept string) ([]byte, error) {
	return c.do("GET", rawURL, "", accept, "")
}

func (c *Client) do(method, rawURL, body, accept, referer string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		if accept == "" {
			accept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
		}
		req.Header.Set("Accept", accept)

		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
			continue
		}
		if len(body) == 0 {
			last = fmt.Errorf("empty response from %s", rawURL)
			continue
		}
		return body, nil
	}
	return nil, last
}

// ---------------------------------------------------------------- text and positions

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBreak       = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</tr>|</li>|</h\d>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces      = regexp.MustCompile(`[ \t]+`)
)

// textOf turns HTML into plain text while keeping line structure — warnings
// are line-oriented and collapsing them loses the message boundaries.
func textOf(b []byte) string {
	s := reScriptStyle.ReplaceAllString(decodeText(b), " ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = unescapeEntities(s)
	return reSpaces.ReplaceAllString(s, " ")
}

var entities = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
	"&#39;", "'", "&apos;", "'", "&nbsp;", " ", "&ndash;", "–", "&mdash;", "—",
)

// Both forms occur: Sweden's NAVTEX page carries its line breaks as &#xD;&#xA;,
// which stayed in the text verbatim while only the decimal form was handled.
var reNumericEntity = regexp.MustCompile(`&#(x[0-9A-Fa-f]+|\d+);`)

func unescapeEntities(s string) string {
	s = entities.Replace(s)
	return reNumericEntity.ReplaceAllStringFunc(s, func(m string) string {
		digits := m[2 : len(m)-1]
		base := 10
		if digits[0] == 'x' || digits[0] == 'X' {
			digits, base = digits[1:], 16
		}
		n, err := strconv.ParseInt(digits, base, 32)
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
}

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Positions are written either as degrees and decimal minutes
// (43-03.75N 006-00.00E) or degrees-minutes-seconds (12-02-46.8S
// 077-09-20.2W). Both appear, sometimes within one country.
var (
	reDMS = regexp.MustCompile(`(?i)(\d{1,3})[-\x{00B0}]\s?(\d{1,2})[-']\s?(\d{1,2}(?:[.,]\d+)?)"?\s*([NSEW])`)
	// Canada separates degrees from minutes with a space rather than a hyphen
	// — "71 21.06N 096 53.90W" — so a single space is accepted too. The
	// hemisphere letter must follow the minutes immediately, which is what
	// keeps chart numbers and distances out.
	reDDM = regexp.MustCompile(`(?i)(\d{1,3})[-\x{00B0} ](\d{1,2}(?:[.,]\d+)?)\s*([NSEW])`)
)

type fix struct {
	pos  int
	deg  float64
	hemi byte
}

// parseCoordinates pulls decimal positions out of a warning's free text.
//
// Values are only taken in pairs, and only when a N/S is immediately followed
// by an E/W: a lone latitude in these messages is far more often a chart
// number, a bearing or a depth than half a position.
// Australia sometimes states an area as two ranges rather than as corners —
// "BOUNDED BY 38-23S TO 38-37S AND 148-25E TO 148-37E". Pairing values in
// sequence finds only one corner of that box, so a hazard area would be drawn
// as a single point. This expands the form into all four corners.
var reBoundingBox = regexp.MustCompile(
	`(?i)(\d{1,3})-(\d{1,2}(?:[.,]\d+)?)\s*([NS])\s+TO\s+(\d{1,3})-(\d{1,2}(?:[.,]\d+)?)\s*([NS])` +
		`\s+AND\s+(\d{1,3})-(\d{1,2}(?:[.,]\d+)?)\s*([EW])\s+TO\s+(\d{1,3})-(\d{1,2}(?:[.,]\d+)?)\s*([EW])`)

func boundingBoxCorners(text string) [][2]float64 {
	var out [][2]float64
	for _, m := range reBoundingBox.FindAllStringSubmatch(text, -1) {
		value := func(deg, min, hemi string) float64 {
			d, _ := strconv.ParseFloat(deg, 64)
			v, _ := strconv.ParseFloat(strings.Replace(min, ",", ".", 1), 64)
			d += v / 60
			if h := strings.ToUpper(hemi); h == "S" || h == "W" {
				d = -d
			}
			return d
		}
		lats := []float64{value(m[1], m[2], m[3]), value(m[4], m[5], m[6])}
		lons := []float64{value(m[7], m[8], m[9]), value(m[10], m[11], m[12])}
		for _, lat := range lats {
			for _, lon := range lons {
				if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
					out = append(out, [2]float64{round6(lat), round6(lon)})
				}
			}
		}
	}
	return out
}

func parseCoordinates(text string) [][2]float64 {
	if text == "" {
		return nil
	}
	if corners := boundingBoxCorners(text); len(corners) > 0 {
		return corners
	}
	for _, attempt := range []struct {
		re      *regexp.Regexp
		convert func([]string) float64
	}{
		{reDMS, func(g []string) float64 {
			d, _ := strconv.ParseFloat(g[1], 64)
			m, _ := strconv.ParseFloat(g[2], 64)
			s, _ := strconv.ParseFloat(strings.Replace(g[3], ",", ".", 1), 64)
			return d + m/60 + s/3600
		}},
		{reDDM, func(g []string) float64 {
			d, _ := strconv.ParseFloat(g[1], 64)
			m, _ := strconv.ParseFloat(strings.Replace(g[2], ",", ".", 1), 64)
			return d + m/60
		}},
	} {
		matches := attempt.re.FindAllStringSubmatchIndex(text, -1)
		found := make([]fix, 0, len(matches))
		for _, m := range matches {
			groups := make([]string, 0, 5)
			for i := 0; i*2 < len(m); i++ {
				if m[i*2] < 0 {
					groups = append(groups, "")
					continue
				}
				groups = append(groups, text[m[i*2]:m[i*2+1]])
			}
			hemi := strings.ToUpper(groups[len(groups)-1])[0]
			found = append(found, fix{m[0], attempt.convert(groups), hemi})
		}

		var out [][2]float64
		for i := 0; i+1 < len(found); i++ {
			a, b := found[i], found[i+1]
			if (a.hemi != 'N' && a.hemi != 'S') || (b.hemi != 'E' && b.hemi != 'W') {
				continue
			}
			lat, lon := a.deg, b.deg
			if a.hemi == 'S' {
				lat = -lat
			}
			if b.hemi == 'W' {
				lon = -lon
			}
			if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				continue
			}
			out = append(out, [2]float64{round6(lat), round6(lon)})
		}
		// A DMS pattern also matches DDM text; if it found pairs those are the
		// right reading, so don't run the looser pattern over the same string.
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func round6(v float64) float64 {
	return float64(int64(v*1e6+copysign(0.5, v))) / 1e6
}

func copysign(v, sign float64) float64 {
	if sign < 0 {
		return -v
	}
	return v
}

var reYear = regexp.MustCompile(`/(\d{2,4})`)

func yearFrom(ref string) int {
	m := reYear.FindStringSubmatch(ref)
	if m == nil {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	if y > 1000 {
		return y
	}
	return 2000 + y
}

func newWarning(source, area, number string, year int, issued, text, u string) Warning {
	// Always an array, never null: the client decodes this field unconditionally
	// and a nil slice would marshal to JSON null.
	// Merging a warning's language variants can repeat the same position, and
	// two identical markers on a chart are noise.
	coords := [][2]float64{}
	seen := map[[2]float64]bool{}
	for _, p := range parseCoordinates(text) {
		if !seen[p] {
			seen[p] = true
			coords = append(coords, p)
		}
	}
	body := strings.TrimSpace(text)
	// Six coordinators publish no date field at all and only carry the
	// broadcast date-time group inside the message, so the body is searched
	// when the source itself gave nothing.
	return Warning{source, normalizeArea(area), number, year, issued,
		normalizeIssued(issued, body, year), body, coords, u}
}

// normalizeArea reduces a NAVAREA to its roman numeral alone.
//
// Coordinators do not agree on how to write their own area: most publish the
// bare numeral, France names its ocean series "NAVAREA II", and NGA writes
// "NAVAREA IV". Left alone, the same ocean area would appear two or three
// times in the app's list under spellings a navigator would have to reconcile.
// Only a genuine numeral is stripped — a coastal series whose name merely
// starts with the word (there is none today, but the sources change) keeps it.
func normalizeArea(area string) string {
	trimmed := strings.TrimSpace(area)
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "NAVAREA"))
	if rest == trimmed || !isRomanArea(rest) {
		return trimmed
	}
	return rest
}

// isRomanArea reports whether s is one of the 21 NAVAREA numerals.
func isRomanArea(s string) bool {
	switch s {
	case "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X",
		"XI", "XII", "XIII", "XIV", "XV", "XVI", "XVII", "XVIII", "XIX", "XX", "XXI":
		return true
	}
	return false
}

// ---------------------------------------------------------------- France (II + coastal)

// France is the only coordinator with a real public REST API: NAVAREA II plus
// 23 coastal series worldwide, bilingual text, hazard classification.
//
// Two quirks, both learned the hard way: the server content-negotiates and
// answers XML unless asked for JSON, and it silently ignores every filter
// parameter, so the whole set is paged through and split by series here.
func fetchFrance(c *Client) ([]Warning, error) {
	const base = "https://services.ping-info-nautique.fr/api/warnings/detailed-table"
	var out []Warning
	pages := 1
	for page := 0; page < pages; page++ {
		body, err := c.Get(fmt.Sprintf("%s?page=%d&size=200", base, page), "application/json")
		if err != nil {
			continue // a dropped page costs 200 warnings, not the whole run
		}
		var doc struct {
			Embedded struct {
				Items []struct {
					NameOfSeries             string `json:"nameOfSeries"`
					WarningHazardTypeGeneral string `json:"warningHazardTypeGeneral"`
					WarningNumber            int    `json:"warningNumber"`
					Year                     int    `json:"year"`
					PublicationTime          string `json:"publicationTime"`
					FeatureParts             []struct {
						Information map[string]string `json:"information"`
						Geometries  []struct {
							Coordinates json.RawMessage `json:"coordinates"`
						} `json:"geometries"`
					} `json:"featureParts"`
				} `json:"items"`
			} `json:"_embedded"`
			Page struct {
				TotalPages int `json:"totalPages"`
			} `json:"page"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			continue
		}
		pages = doc.Page.TotalPages
		for _, it := range doc.Embedded.Items {
			// A warning can carry several featureParts, one per affected zone.
			// Each part's text has to be kept: dropping the others would hide
			// entire areas the warning covers. English where it exists, the
			// French original otherwise — part by part, not warning by warning.
			var parts []string
			coords := [][2]float64{}
			for _, part := range it.FeatureParts {
				// The two language fields are not translations of each other.
				// Either can be the shorter, either can be the one carrying the
				// position, and the "en" field is frequently still in French.
				// Picking one silently dropped vessel names and — worse —
				// whole positions, so both are merged and nothing is lost.
				parts = append(parts, mergeLines(
					strings.TrimSpace(part.Information["fr"]),
					strings.TrimSpace(part.Information["en"])))
				for _, g := range part.Geometries {
					coords = append(coords, flattenGeoJSON(g.Coordinates)...)
				}
			}
			text := strings.TrimSpace(strings.Join(parts, "\n\n"))
			// France classifies every warning, and some of them carry nothing
			// else: a newly discovered danger is published as a bare position,
			// which on its own tells a navigator nothing about what is there.
			if kind := hazardLabel(it.WarningHazardTypeGeneral); kind != "" {
				text = kind + "\n" + text
			}
			w := newWarning("france", it.NameOfSeries,
				fmt.Sprintf("%d/%02d", it.WarningNumber, it.Year%100),
				it.Year, it.PublicationTime, text, base)
			if len(coords) > 0 {
				w.Coordinates = coords
			}
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("france: no warnings parsed")
	}
	return out, nil
}

// flattenGeoJSON reduces any GeoJSON coordinate nesting to [lat, lon] pairs.
func flattenGeoJSON(raw json.RawMessage) [][2]float64 {
	if len(raw) == 0 {
		return nil
	}
	var pair []float64
	if err := json.Unmarshal(raw, &pair); err == nil && len(pair) == 2 {
		return [][2]float64{{round6(pair[1]), round6(pair[0])}} // GeoJSON is lon,lat
	}
	var nested []json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil
	}
	var out [][2]float64
	for _, n := range nested {
		out = append(out, flattenGeoJSON(n)...)
	}
	return out
}

// ---------------------------------------------------------------- Spain (III)

func fetchSpain(c *Client) ([]Warning, error) {
	const u = "https://armada.defensa.gob.es/ihm/XML/navareas_crudo.xml"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Records []struct {
			Number    string `xml:"nnunaf"`
			Issued    string `xml:"nfemi"`
			PlaceES   string `xml:"nlocae"`
			PlaceEN   string `xml:"nlocai"`
			TextES    string `xml:"ntees"`
			TextEN    string `xml:"ntein"`
			SubjectES string `xml:"nasun"`
			SubjectEN string `xml:"nasuni"`
		} `xml:"NAVAREASVIGOR"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []Warning
	for _, r := range doc.Records {
		place, subject, text := pick(r.PlaceEN, r.PlaceES), pick(r.SubjectEN, r.SubjectES), pick(r.TextEN, r.TextES)
		year := 0
		if len(r.Issued) >= 4 {
			year, _ = strconv.Atoi(r.Issued[:4])
		}
		out = append(out, newWarning("spain", "III", r.Number, year, r.Issued,
			strings.TrimSpace(place+". "+subject+"\n"+text), u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("spain: no records in XML")
	}
	return out, nil
}

func pick(preferred, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	return strings.TrimSpace(fallback)
}

// ---------------------------------------------------------------- UK (I + coastal)

// The Admiralty page is a server-rendered table, one row per warning, with the
// full text in a collapse panel keyed to the row index.
func fetchUK(c *Client) ([]Warning, error) {
	const u = "https://msi.admiralty.co.uk/RadioNavigationalWarnings"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	html := decodeText(body)
	cell := func(name string, i int) string {
		re := regexp.MustCompile(fmt.Sprintf(`(?s)<td id="%s_%d"[^>]*>(.*?)</td>`, name, i))
		if m := re.FindStringSubmatch(html); m != nil {
			return strings.TrimSpace(textOf([]byte(m[1])))
		}
		return ""
	}
	var out []Warning
	for i := 0; i < 400; i++ {
		ref := cell("Reference", i)
		if ref == "" {
			if i > 5 {
				break
			}
			continue
		}
		text := cell("Description", i)
		panel := regexp.MustCompile(fmt.Sprintf(`(?s)id="collapse_%d"(.*?)</tbody>`, i))
		if m := panel.FindStringSubmatch(html); m != nil {
			body := m[1]
			// The match starts inside the opening tag, so the rest of its
			// attributes would otherwise be read as warning text.
			if idx := strings.Index(body, ">"); idx >= 0 {
				body = body[idx+1:]
			}
			// The panel runs to the next row; cut it there rather than with a
			// lookahead, which RE2 does not have.
			if idx := strings.Index(body, "<tr "); idx > 0 {
				body = body[:idx]
			}
			if full := strings.TrimSpace(textOf([]byte(body))); len(full) > len(text) {
				text = full
			}
		}
		// The panel repeats the series, reference and time that already head
		// the record; drop them so the body starts at the message itself.
		text = trimRepeatedHeader(text, ref, cell("DateTimeGroupRnwFormat", i))
		area := "UK Coastal"
		if strings.HasPrefix(strings.ToUpper(ref), "NAVAREA") {
			area = "I"
		}
		fields := strings.Fields(ref)
		out = append(out, newWarning("uk", area, fields[len(fields)-1], yearFrom(ref),
			cell("DateTimeGroupRnwFormat", i), ref+"\n"+text, u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("uk: no rows matched")
	}
	return out, nil
}

// ---------------------------------------------------------------- Sweden (Baltic)

// The Baltic NAVTEX page carries Danish, German and Baltic-wide warnings
// alongside the Swedish ones, already grouped by named sea area — which gives
// area filtering without parsing a single coordinate.
// trimRepeatedHeader drops leading lines that only restate the warning's own
// reference or time, which several pages print again above the message body.
func trimRepeatedHeader(text, ref, dtg string) string {
	known := map[string]bool{strings.ToUpper(ref): true, strings.ToUpper(dtg): true}
	for _, field := range strings.Fields(ref) {
		if strings.EqualFold(field, "NAVAREA") {
			known["NAVAREA 1"] = true
			known["NAVAREA I"] = true
		}
	}
	out := lines(text)
	for len(out) > 0 && known[strings.ToUpper(strings.TrimSpace(out[0]))] {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}

func fetchSweden(c *Client) ([]Warning, error) {
	pages := []struct{ url, tag string }{
		{"https://navvarn.sjofartsverket.se/en/Navigationsvarningar/Navtex", "NAVTEX"},
		{"https://navvarn.sjofartsverket.se/en/Navigationsvarningar", "Swedish"},
	}
	reSection := regexp.MustCompile(`<div\s+id="display-area-\d+"`)
	reHeading := regexp.MustCompile(`(?s)<h\d[^>]*>(.*?)</h\d>`)
	reWarn := regexp.MustCompile(`([A-Z][A-Z ]*NAV WARN)\s*(\d+/\d+)`)
	// "130830 UTC SEP 26" or "281030 UTC AUG" — the year is often absent.
	reSwedenDTG := regexp.MustCompile(`(?i)\b\d{6}\s*UTC\s+[A-Z]{3}(?:\s+\d{2})?\b`)

	var out []Warning
	for _, page := range pages {
		body, err := c.Get(page.url, "")
		if err != nil {
			return nil, err
		}
		// The opening tag is split across lines, so sections are cut on the id.
		parts := reSection.Split(decodeText(body), -1)
		for _, block := range parts[1:] {
			if idx := strings.Index(block, "<div"); idx > 0 {
				block = block[:idx]
			}
			sea := ""
			if m := reHeading.FindStringSubmatch(block); m != nil {
				sea = strings.TrimSpace(textOf([]byte(m[1])))
			}
			text := textOf([]byte(block))
			marks := reWarn.FindAllStringSubmatchIndex(text, -1)
			// Each warning appears twice in a section: once as a short index
			// entry that carries the date-time group, and once in full without
			// it. Neither half is complete, so they are merged — the longer
			// text, and the date from whichever variant has one.
			type merged struct{ text, issued string }
			best := map[string]merged{}
			order := []string{}
			for i, m := range marks {
				end := len(text)
				if i+1 < len(marks) {
					end = marks[i+1][0]
				}
				ref := text[m[4]:m[5]]
				chunk := strings.TrimSpace(text[m[0]:end])
				prev, seen := best[ref]
				if !seen {
					order = append(order, ref)
				}
				next := merged{text: prev.text, issued: prev.issued}
				if len(chunk) > len(next.text) {
					next.text = chunk
				}
				if next.issued == "" {
					if m := reSwedenDTG.FindString(chunk); m != "" {
						next.issued = m
					}
				}
				best[ref] = next
			}
			area := page.tag
			if sea != "" {
				area = page.tag + ": " + sea
			}
			for _, ref := range order {
				out = append(out, newWarning("sweden", area, ref, yearFrom(ref),
					best[ref].issued, best[ref].text, page.url))
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sweden: no warnings matched")
	}
	return out, nil
}

// ---------------------------------------------------------------- Norway (XIX)

// Number / Date / Warning triplets, label and value on the same line.
func fetchNorway(c *Client) ([]Warning, error) {
	const u = "https://kyvreports.kystverket.no/NavcoReport/navareaxixvarsler.aspx"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	ls := lines(textOf(body))
	var out []Warning
	for i := 0; i < len(ls); {
		if !strings.HasPrefix(ls[i], "Number:") {
			i++
			continue
		}
		number := strings.TrimSpace(strings.TrimPrefix(ls[i], "Number:"))
		date, text := "", []string{}
		j := i + 1
		for ; j < len(ls) && !strings.HasPrefix(ls[j], "Number:"); j++ {
			switch {
			case strings.HasPrefix(ls[j], "Date:"):
				date = strings.TrimSpace(strings.TrimPrefix(ls[j], "Date:"))
			case strings.HasPrefix(ls[j], "Warning:"):
			default:
				text = append(text, ls[j])
			}
		}
		out = append(out, newWarning("norway", "XIX", number, yearFrom(number), date,
			strings.Join(text, "\n"), u))
		i = j
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("norway: no Number: blocks found")
	}
	return out, nil
}

// ---------------------------------------------------------------- Australia (X)

// AMSA publishes broadcast-format messages between SECURITE and NNNN markers.
func fetchAustralia(c *Client) ([]Warning, error) {
	const u = "https://www.operations.amsa.gov.au/AMSA.Web.MSIPublication/Home"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	text := textOf(body)
	reRef := regexp.MustCompile(`((?:NAVAREA X|AUSCOAST)[A-Z ]*)\s*(\d+/\d+)`)
	var out []Warning
	seen := map[string]bool{}
	for _, chunk := range strings.Split(text, "SECURITE")[1:] {
		if idx := strings.Index(chunk, "NNNN"); idx >= 0 {
			chunk = chunk[:idx]
		}
		m := reRef.FindStringSubmatch(chunk)
		if m == nil {
			continue
		}
		area := "AUSCOAST"
		if strings.Contains(m[1], "NAVAREA") {
			area = "X"
		}
		body := strings.TrimSpace(chunk)
		// A warning can appear in more than one section of the page; the same
		// message twice is noise, not two warnings.
		if seen[area+m[2]+body] {
			continue
		}
		seen[area+m[2]+body] = true
		out = append(out, newWarning("australia", area, m[2], yearFrom(m[2]), "", body, u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("australia: no SECURITE blocks found")
	}
	return out, nil
}

// ---------------------------------------------------------------- Canada (XVII, XVIII)

// One <table class="navarea-text-style"> per warning: the reference is the
// link in its heading row, the body the paragraph beneath.
func fetchCanada(c *Client) ([]Warning, error) {
	const u = "https://nis.ccg-gcc.gc.ca/public/rest/messages/en/search-navareas?page=1&maxHits=500"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	reTable := regexp.MustCompile(`<table[^>]*class="navarea-text-style"`)
	reRef := regexp.MustCompile(`>\s*(NAVAREA\s+XVI{1,3})\s+(\d+)/(\d{4})\s*<`)
	reBody := regexp.MustCompile(`(?s)<p>(.*?)</p>`)

	var out []Warning
	for _, block := range reTable.Split(decodeText(body), -1)[1:] {
		if idx := strings.Index(block, "</table>"); idx >= 0 {
			block = block[:idx]
		}
		m := reRef.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		// A warning can run to several paragraphs, and the first is often only
		// a heading — "SHOALS LOCATED AT:" with the positions in the next one.
		// Taking just the first dropped the positions entirely.
		var paras []string
		for _, b := range reBody.FindAllStringSubmatch(block, -1) {
			if t := strings.TrimSpace(textOf([]byte(b[1]))); t != "" {
				paras = append(paras, t)
			}
		}
		text := strings.Join(paras, "\n")
		fields := strings.Fields(m[1])
		year, _ := strconv.Atoi(m[3])
		out = append(out, newWarning("canada", fields[len(fields)-1],
			fmt.Sprintf("%s/%s", m[2], m[3][2:]), year, "", text, u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("canada: no navarea-text-style tables found")
	}
	return out, nil
}

// ---------------------------------------------------------------- Peru (XVI)

// The reference repeats in the accordion header, the panel and the
// cross-references, so one entry is kept per number — the longest text being
// the actual panel rather than a mention.
func fetchPeru(c *Client) ([]Warning, error) {
	const u = "https://www.dhn.mil.pe/portal/navarea/radioavisos-warnings"
	body, err := c.Get(u, "")
	if err != nil {
		return nil, err
	}
	text := textOf(body)
	reRef := regexp.MustCompile(`NAVAREA XVI\s+(\d+/\d+)`)
	marks := reRef.FindAllStringSubmatchIndex(text, -1)
	best := map[string]string{}
	for i, m := range marks {
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		ref := text[m[2]:m[3]]
		if chunk := strings.TrimSpace(text[m[0]:end]); len(chunk) > len(best[ref]) {
			if len(chunk) > 2000 {
				chunk = chunk[:2000]
			}
			best[ref] = chunk
		}
	}
	refs := make([]string, 0, len(best))
	for ref := range best {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var out []Warning
	for _, ref := range refs {
		out = append(out, newWarning("peru", "XVI", ref, yearFrom(ref), "", best[ref], u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("peru: no NAVAREA XVI references found")
	}
	return out, nil
}

// ---------------------------------------------------------------- Pakistan (IX)

// Every warning is its own plain-text file, named with the date it was issued.
// All of them are fetched: Pakistan keeps some in force for years, and
// filtering on the date in the filename would quietly hide live warnings.
func fetchPakistan(c *Client) ([]Warning, error) {
	const index = "https://hydrography.paknavy.gov.pk/navarea-ix-warnings/"
	body, err := c.Get(index, "")
	if err != nil {
		return nil, err
	}
	reHref := regexp.MustCompile(`href="([^"]*custom_uploaded_warnings_for_navarea/[^"]+\.txt)"`)
	seen := map[string]bool{}
	var hrefs []string
	for _, m := range reHref.FindAllStringSubmatch(decodeText(body), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			hrefs = append(hrefs, m[1])
		}
	}
	sort.Strings(hrefs)

	base, _ := url.Parse(index)
	reRef := regexp.MustCompile(`NAVAREA IX\s*\(?\.?\)?\s*(\d+/\d+)`)
	reStamp := regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})`)

	var out []Warning
	for _, href := range hrefs {
		// Several filenames contain spaces and parentheses; they must be
		// re-quoted or the server answers 404.
		decoded, err := url.PathUnescape(href)
		if err != nil {
			decoded = href
		}
		ref, err := base.Parse((&url.URL{Path: decoded}).EscapedPath())
		if err != nil {
			continue
		}
		text, err := c.Get(ref.String(), "")
		if err != nil {
			continue
		}
		name := decoded[strings.LastIndex(decoded, "/")+1:]
		number, issued, year := name, "", 0
		if m := reRef.FindStringSubmatch(decodeText(text)); m != nil {
			number = m[1]
		}
		if m := reStamp.FindStringSubmatch(name); m != nil {
			year, _ = strconv.Atoi(m[1])
			issued = m[1] + "-" + m[2] + "-" + m[3]
		}
		if year == 0 {
			year = yearFrom(number) // hand-named files carry the date only in the text
		}
		out = append(out, newWarning("pakistan", "IX", number, year, issued,
			decodeText(text), ref.String()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pakistan: no warning files fetched")
	}
	return out, nil
}

// ---------------------------------------------------------------- Japan (XI)

// The Japanese Hydrographic and Oceanographic Department publishes through a
// pair of CGI endpoints its own page calls: one returns the year's index as
// XML, the other renders a single warning. Warnings stay in force across the
// new year, so the previous year is fetched too.
func fetchJapan(c *Client) ([]Warning, error) {
	const cgi = "https://www1.kaiho.mlit.go.jp/TUHO/keiho/cgi/"
	const referer = "https://www1.kaiho.mlit.go.jp/TUHO/keiho/navarea11_en.html"

	type member struct {
		Category string `xml:"categoly"` // the site's own spelling
		Number   string `xml:"number"`
		Tana     string `xml:"tana"`
		Title    string `xml:"title"`
	}
	var out []Warning
	year := time.Now().UTC().Year()
	// The index is per year but lists only what is still in force, so the
	// archive is small — and it reaches back further than one might expect:
	// warnings from 2019 are still current. Taking only the last year or two
	// would silently drop live warnings, so the whole range the site offers
	// is walked.
	for y := 2016; y <= year; y++ {
		body, err := c.Post(cgi+"warnings.cgi",
			fmt.Sprintf("YEAR=%d&TYPE=NAVAREA11&LANG=EG", y), referer)
		if err != nil {
			continue
		}
		var doc struct {
			Members []member `xml:"Member"`
		}
		if err := xml.Unmarshal(body, &doc); err != nil {
			continue
		}
		for _, m := range doc.Members {
			url := fmt.Sprintf("%sdisp_warnings.cgi?TYPE=NAVAREA11&TANA=%s&LANG=EG", cgi, m.Tana)
			text := strings.TrimSpace(m.Title)
			if page, err := c.Get(url, ""); err == nil {
				if full := strings.TrimSpace(collapseBlank(textOf(page))); len(full) > len(text) {
					text = full
				}
			}
			if m.Category != "" {
				text = m.Category + "\n" + text
			}
			out = append(out, newWarning("japan", "XI",
				fmt.Sprintf("%s/%02d", m.Number, y%100), y, "", text, url))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("japan: no warnings returned by warnings.cgi")
	}
	return out, nil
}

var reBlankLines = regexp.MustCompile(`\n\s*\n+`)

func collapseBlank(s string) string { return reBlankLines.ReplaceAllString(s, "\n") }

// ---------------------------------------------------------------- Chile (XV)

// SHOA publishes NAVAREA XV as a generated PDF and nothing else. The text is
// in literal strings with an ASCII encoding, so it extracts cleanly — but the
// result is checked before use, because a PDF that switched to subsetted fonts
// would otherwise be published as a page of mojibake labelled "warning".
func fetchChile(c *Client) ([]Warning, error) {
	const u = "https://www.shoa.cl/php/radioAvisosPDF.php?documento=NAVAREA&tipo=3"
	body, err := c.Get(u, "")
	if err != nil {
		// shoa.cl answers 403 to datacenter addresses while serving the same
		// URL normally from a residential one. Saying so keeps a blocked
		// build host from reading like a broken parser.
		if strings.Contains(err.Error(), "HTTP 403") {
			return nil, fmt.Errorf("chile: shoa.cl refuses this host (HTTP 403). " +
				"It serves the same URL from a residential address, so this is " +
				"address-based blocking rather than a parser fault")
		}
		return nil, err
	}
	if !bytes.HasPrefix(body, []byte("%PDF")) {
		return nil, fmt.Errorf("chile: expected a PDF, got %q", firstBytes(body, 40))
	}
	text := pdfText(body)
	if !pdfLooksExtractable(text) {
		return nil, fmt.Errorf("chile: the PDF's text is not extractable — " +
			"it has probably switched to subsetted fonts")
	}

	reRef := regexp.MustCompile(`NAVAREA XV\s+(\d{3,4})`)
	marks := reRef.FindAllStringSubmatchIndex(text, -1)
	// The document opens with an "in force" index that repeats every number;
	// warnings proper start at the first "YEAR NNNN" heading.
	year := time.Now().UTC().Year()
	reYearHead := regexp.MustCompile(`YEAR\s+(\d{4})`)

	best := map[string]string{}
	yearOf := map[string]int{}
	for i, m := range marks {
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		ref := text[m[2]:m[3]]
		chunk := strings.TrimSpace(text[m[0]:end])
		if len(chunk) <= len(best[ref]) {
			continue
		}
		best[ref] = chunk
		// The nearest preceding "YEAR nnnn" heading gives the warning's year.
		yearOf[ref] = year
		if head := reYearHead.FindAllStringSubmatchIndex(text[:m[0]], -1); len(head) > 0 {
			last := head[len(head)-1]
			if y, err := strconv.Atoi(text[last[2]:last[3]]); err == nil && y > 2000 {
				yearOf[ref] = y
			}
		}
	}

	refs := make([]string, 0, len(best))
	for ref := range best {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var out []Warning
	for _, ref := range refs {
		y := yearOf[ref]
		out = append(out, newWarning("chile", "XV",
			fmt.Sprintf("%s/%02d", ref, y%100), y, "", best[ref], u))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("chile: no NAVAREA XV references in the PDF text")
	}
	return out, nil
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// ---------------------------------------------------------------- USA (IV, XII)

// NGA's obvious route — the documented /broadcast-warn JSON API — returns
// HTTP 200 with nothing newer than 2024-05-10, and has done for over two
// years; its own /inforce list and its own website agree. Every guide to this
// data points at that API, and every one of them is now wrong.
//
// The live publication is a set of daily plain-text bulletins listed by the
// media endpoint, one per series, refreshed each afternoon. They carry the
// same messages in broadcast form, which is what the coordinator actually
// transmits.
//
// The file list is fetched rather than hard-coded because the storage key
// carries a content id that will eventually change.
func fetchUSA(c *Client) ([]Warning, error) {
	const media = "https://msi.nga.mil/api/media?type=Nav%20Warnings"
	const download = "https://msi.nga.mil/api/publications/download?key=%s&type=view"

	index, err := c.Get(media, "application/json")
	if err != nil {
		return nil, err
	}
	var files []struct {
		DisplayName string `json:"displayName"`
		S3Key       string `json:"s3Key"`
		Extension   string `json:"fileExtension"`
	}
	if err := json.Unmarshal(index, &files); err != nil {
		return nil, err
	}

	// "NAVAREA IV" -> "IV"; HYDROLANT and friends stay as they are.
	area := func(display string) string {
		return strings.TrimSpace(strings.TrimPrefix(display, "NAVAREA"))
	}
	// A message opens with its series and number on a line of its own; the
	// date-time group sits on the line before it.
	reHead := regexp.MustCompile(`^(NAVAREA [IVX]+|HYDROLANT|HYDROPAC|HYDROARC)\s+(\d+/\d+)\.?\s*$`)
	reDTG := regexp.MustCompile(`^\d{6}Z\s+[A-Z]{3}\s+\d{2}\s*$`)

	var out []Warning
	for _, f := range files {
		if !strings.EqualFold(f.Extension, "txt") {
			continue // the set also holds a Google Earth overlay
		}
		url := fmt.Sprintf(download, f.S3Key)
		body, err := c.Get(url, "")
		if err != nil {
			continue
		}
		src := lines(decodeText(body))

		starts := []int{}
		for i, l := range src {
			if reHead.MatchString(l) {
				starts = append(starts, i)
			}
		}
		for n, at := range starts {
			end := len(src)
			if n+1 < len(starts) {
				end = starts[n+1]
			}
			head := reHead.FindStringSubmatch(src[at])
			issued := ""
			// Walk back over the previous message's tail to its date-time group.
			for j := at - 1; j >= 0 && j > at-3; j-- {
				if reDTG.MatchString(src[j]) {
					issued = src[j]
					break
				}
			}
			text := strings.Join(src[at:end], "\n")
			if issued != "" {
				// Trim the next message's DTG off the end of this one.
				if last := end - 1; last > at && reDTG.MatchString(src[last]) {
					text = strings.Join(src[at:last], "\n")
				}
			}
			out = append(out, newWarning("usa", area(f.DisplayName), head[2],
				yearFrom(head[2]), issued, text, url))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usa: no messages parsed from the daily bulletins")
	}
	return out, nil
}

// mergeLines joins two versions of the same message, keeping every line that
// appears in either and dropping the repeats. Comparison ignores case and
// spacing so a line reworded only in punctuation is not printed twice.
func mergeLines(primary, secondary string) string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			key := strings.ToLower(reSpaces.ReplaceAllString(line, " "))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, line)
		}
	}
	add(primary)
	add(secondary)
	return strings.Join(out, "\n")
}

// hazardLabel turns France's hazard enum into something readable —
// NEWLY_DISCOVERED_DANGERS becomes "NEWLY DISCOVERED DANGERS".
func hazardLabel(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || strings.EqualFold(code, "OTHER") {
		return ""
	}
	return strings.ReplaceAll(code, "_", " ")
}
