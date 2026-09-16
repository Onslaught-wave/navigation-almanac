package main

// Decoding whatever byte soup a coordinator serves.
//
// Pakistan publishes its warnings as Windows-1252: the curly quotes around a
// vessel's name — "ZH" PLATFORM, "HALUL 41" — are bytes 0x93 and 0x94, which
// are not valid UTF-8. Treating those bytes as UTF-8 turns each one into a
// replacement character, so the vessel names in seven in-force warnings
// reached the app as garbage.
//
// Nothing declares its encoding reliably, so the rule is: if the bytes are
// valid UTF-8, they are UTF-8 — that is overwhelmingly the common case and
// valid UTF-8 is very unlikely to be accidental. Otherwise fall back to
// Windows-1252, which is a superset of Latin-1 and covers every western
// single-byte page these sites actually use.

import (
	"strings"
	"unicode/utf8"
)

// The 0x80–0x9F range is where Windows-1252 differs from Latin-1; everything
// else maps one byte to one code point.
var cp1252High = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

// decodeText turns a fetched body into a Go string, repairing the encoding
// when it is not UTF-8.
func decodeText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b) + len(b)/4)
	for _, c := range b {
		switch {
		case c < 0x80:
			out.WriteByte(c)
		case c < 0xA0:
			out.WriteRune(cp1252High[c-0x80])
		default:
			out.WriteRune(rune(c))
		}
	}
	return out.String()
}
