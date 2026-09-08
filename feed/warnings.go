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
	Source      string      `json:"source"`
	Area        string      `json:"area"` // NAVAREA in roman numerals, or a coastal series
	Number      string      `json:"number"`
	Year        int         `json:"year"`
	Issued      string      `json:"issued"` // as published; formats differ per country
	Text        string      `json:"text"`
	Coordinates [][2]float64 `json:"coordinates"` // decimal degrees, [lat, lon]
	URL         string      `json:"url"`
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
var Blocked = []struct{ Name, Areas, Why string }{
	{"brazil", "V", "Cloudflare bot check; the data itself is good JSON " +
		"(avradio_NN.json, bilingual, with coordinates) but only a real browser gets it"},
	{"newzealand", "XIV", "Cloudflare bot check on maritimenz.govt.nz"},
	{"japan", "XI", "kaiho.mlit.go.jp returns 403; current URL not established"},
	{"india", "VIII", "Liferay document library — warnings are PDFs, no direct feed"},
	{"chile", "XV", "shoa.cl radioavisos endpoint returns 500"},
	{"argentina", "VI", "RadioavisosNauticos.asp serves the site home page"},
	{"southafrica", "VII", "monthly Notices to Mariners PDFs only, no per-warning feed"},
	{"russia", "XIII, XX, XXI", "structure.mil.ru and nsr.rosatom.ru time out"},
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
	{"usa", "IV, XII + HYDRO", fetchUSA},
}

// ---------------------------------------------------------------- fetching

// Client is a retrying HTTP client. Several of these national servers drop
// connections at random, and a single miss would blank out a whole NAVAREA.
type Client struct{ http *http.Client }

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) Get(rawURL string, accept string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, err
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
	s := reScriptStyle.ReplaceAllString(string(b), " ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = unescapeEntities(s)
	return reSpaces.ReplaceAllString(s, " ")
}

var entities = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
	"&#39;", "'", "&apos;", "'", "&nbsp;", " ", "&ndash;", "–", "&mdash;", "—",
)

var reNumericEntity = regexp.MustCompile(`&#(\d+);`)

func unescapeEntities(s string) string {
	s = entities.Replace(s)
	return reNumericEntity.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.Atoi(m[2 : len(m)-1])
		if err != nil || n > 0x10FFFF {
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
	reDDM = regexp.MustCompile(`(?i)(\d{1,3})[-\x{00B0}]\s?(\d{1,2}(?:[.,]\d+)?)\s*([NSEW])`)
)

type fix struct {
	pos   int
	deg   float64
	hemi  byte
}

// parseCoordinates pulls decimal positions out of a warning's free text.
//
// Values are only taken in pairs, and only when a N/S is immediately followed
// by an E/W: a lone latitude in these messages is far more often a chart
// number, a bearing or a depth than half a position.
func parseCoordinates(text string) [][2]float64 {
	if text == "" {
		return nil
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
	coords := parseCoordinates(text)
	if coords == nil {
		coords = [][2]float64{}
	}
	return Warning{source, area, number, year, issued, strings.TrimSpace(text), coords, u}
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
					NameOfSeries    string `json:"nameOfSeries"`
					WarningNumber   int    `json:"warningNumber"`
					Year            int    `json:"year"`
					PublicationTime string `json:"publicationTime"`
					FeatureParts    []struct {
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
				if v := strings.TrimSpace(part.Information["en"]); v != "" {
					parts = append(parts, v)
				} else if v := strings.TrimSpace(part.Information["fr"]); v != "" {
					parts = append(parts, v)
				}
				for _, g := range part.Geometries {
					coords = append(coords, flattenGeoJSON(g.Coordinates)...)
				}
			}
			text := strings.Join(parts, "\n\n")
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
	html := string(body)
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
			// The panel runs to the next row; cut it there rather than with a
			// lookahead, which RE2 does not have.
			body := m[1]
			if idx := strings.Index(body, "<tr "); idx > 0 {
				body = body[:idx]
			}
			if full := strings.TrimSpace(textOf([]byte(body))); len(full) > len(text) {
				text = full
			}
		}
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
func fetchSweden(c *Client) ([]Warning, error) {
	pages := []struct{ url, tag string }{
		{"https://navvarn.sjofartsverket.se/en/Navigationsvarningar/Navtex", "NAVTEX"},
		{"https://navvarn.sjofartsverket.se/en/Navigationsvarningar", "Swedish"},
	}
	reSection := regexp.MustCompile(`<div\s+id="display-area-\d+"`)
	reHeading := regexp.MustCompile(`(?s)<h\d[^>]*>(.*?)</h\d>`)
	reWarn := regexp.MustCompile(`([A-Z][A-Z ]*NAV WARN)\s*(\d+/\d+)`)

	var out []Warning
	for _, page := range pages {
		body, err := c.Get(page.url, "")
		if err != nil {
			return nil, err
		}
		// The opening tag is split across lines, so sections are cut on the id.
		parts := reSection.Split(string(body), -1)
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
			for i, m := range marks {
				end := len(text)
				if i+1 < len(marks) {
					end = marks[i+1][0]
				}
				area := page.tag
				if sea != "" {
					area = page.tag + ": " + sea
				}
				out = append(out, newWarning("sweden", area, text[m[4]:m[5]],
					yearFrom(text[m[4]:m[5]]), "", strings.TrimSpace(text[m[0]:end]), page.url))
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
		out = append(out, newWarning("australia", area, m[2], yearFrom(m[2]), "",
			strings.TrimSpace(chunk), u))
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
	for _, block := range reTable.Split(string(body), -1)[1:] {
		if idx := strings.Index(block, "</table>"); idx >= 0 {
			block = block[:idx]
		}
		m := reRef.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		text := ""
		if b := reBody.FindStringSubmatch(block); b != nil {
			text = strings.TrimSpace(textOf([]byte(b[1])))
		}
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
	for _, m := range reHref.FindAllStringSubmatch(string(body), -1) {
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
		if m := reRef.FindStringSubmatch(string(text)); m != nil {
			number = m[1]
		}
		if m := reStamp.FindStringSubmatch(name); m != nil {
			year, _ = strconv.Atoi(m[1])
			issued = m[1] + "-" + m[2] + "-" + m[3]
		}
		out = append(out, newWarning("pakistan", "IX", number, year, issued,
			string(text), ref.String()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pakistan: no warning files fetched")
	}
	return out, nil
}

// ---------------------------------------------------------------- USA (IV, XII)

// Machine-readable and permissively licensed — and, at the time of writing,
// serving nothing newer than 2024-05-10 while returning HTTP 200. The staleness
// check applied to every source is the reason this one is safe to include.
func fetchUSA(c *Client) ([]Warning, error) {
	const base = "https://msi.nga.mil/api/publications/broadcast-warn"
	body, err := c.Get(base+"?status=active&output=json", "application/json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Warnings []struct {
			MsgYear   int    `json:"msgYear"`
			MsgNumber int    `json:"msgNumber"`
			NavArea   string `json:"navArea"`
			Text      string `json:"text"`
			IssueDate string `json:"issueDate"`
		} `json:"broadcast-warn"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	names := map[string]string{"4": "IV", "12": "XII", "A": "HYDROARC",
		"P": "HYDROPAC", "C": "HYDROLANT"}
	var out []Warning
	for _, r := range doc.Warnings {
		area := names[r.NavArea]
		if area == "" {
			area = r.NavArea
		}
		out = append(out, newWarning("usa", area,
			fmt.Sprintf("%d/%02d", r.MsgNumber, r.MsgYear%100),
			r.MsgYear, r.IssueDate, r.Text, base))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usa: no warnings in response")
	}
	return out, nil
}
