package main

// A deliberately small PDF text extractor.
//
// One coordinator (Chile) publishes its warnings only as a generated PDF, and
// pulling in a full PDF library for one source would cost more than the source
// is worth. This handles the case that actually occurs: a machine-generated
// document whose content streams are Flate-compressed and whose text is in
// literal strings, with the font's own encoding being plain ASCII.
//
// It does NOT handle subsetted fonts that map glyphs through a /ToUnicode CMap
// — India's warnings are published that way, and come out as mojibake rather
// than text. `pdfLooksExtractable` exists so a caller can tell the difference
// and report the source as unreadable instead of publishing gibberish as a
// navigational warning.

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var (
	reStream   = regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	reLiteral  = regexp.MustCompile(`\((?:\\.|[^()\\])*\)`)
	reNewline  = regexp.MustCompile(`(?m)\s*(?:Td|TD|T\*|ET)\s*$`)
	reOctal    = regexp.MustCompile(`\\([0-7]{1,3})`)
	reBlankRun = regexp.MustCompile(`\n{3,}`)
)

// pdfText returns whatever readable text the document carries.
func pdfText(data []byte) string {
	var out strings.Builder
	for _, m := range reStream.FindAllSubmatch(data, -1) {
		body := inflate(m[1])
		if !bytes.Contains(body, []byte("Tj")) && !bytes.Contains(body, []byte("TJ")) {
			continue
		}
		// Text-positioning operators are where line breaks live; without them
		// every warning in the file runs into one paragraph.
		for _, line := range strings.Split(string(body), "\n") {
			var text strings.Builder
			for _, lit := range reLiteral.FindAllString(line, -1) {
				text.WriteString(unescapePDF(lit[1 : len(lit)-1]))
			}
			if s := text.String(); strings.TrimSpace(s) != "" {
				out.WriteString(s)
			}
			if reNewline.MatchString(line) {
				out.WriteString("\n")
			}
		}
		out.WriteString("\n")
	}
	return reBlankRun.ReplaceAllString(out.String(), "\n\n")
}

// pdfLooksExtractable reports whether pdfText is likely to return real words
// rather than the glyph indices of a subsetted font.
//
// The test is crude on purpose: readable warnings are mostly ASCII letters and
// digits, while glyph-coded text decodes to a dense run of control bytes.
func pdfLooksExtractable(text string) bool {
	if len(text) < 200 {
		return false
	}
	var readable, total int
	for _, r := range text {
		total++
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == ' ' || r == '\n' || r == '.' || r == ',' || r == '-' {
			readable++
		}
	}
	return total > 0 && float64(readable)/float64(total) > 0.9
}

func inflate(raw []byte) []byte {
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return raw // some streams are stored uncompressed
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil && len(out) == 0 {
		return raw
	}
	return out
}

func unescapePDF(s string) string {
	s = reOctal.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.ParseInt(m[1:], 8, 16)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
	return strings.NewReplacer(
		`\(`, "(", `\)`, ")", `\\`, `\`,
		`\n`, "\n", `\r`, "\r", `\t`, "\t",
	).Replace(s)
}
