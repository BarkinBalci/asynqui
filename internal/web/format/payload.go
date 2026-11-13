package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text returns b as display text: invalid UTF-8 sequences and control
// characters other than tab, newline and carriage return become U+FFFD,
// so binary payloads (protobuf, msgpack) render as text instead of
// breaking the page. It never interprets the bytes.
func Text(b []byte) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return '\uFFFD'
		}
		return r
	}, s)
}

// PrettyJSON indents b by two spaces when it is valid JSON, and reports
// whether it was. Numbers and strings are kept as written.
func PrettyJSON(b []byte) (string, bool) {
	if len(bytes.TrimSpace(b)) == 0 || !json.Valid(b) {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return "", false
	}
	return out.String(), true
}

// Preview is a one-line preview of a payload or error text (pass bytes
// through Text first): whitespace and control characters (newlines, tabs)
// become single spaces, and text longer than n runes is cut to n runes
// plus "…".
func Preview(s string, n int) string {
	var sb strings.Builder
	runes, space := 0, false
	add := func(r rune) bool {
		if runes == n {
			sb.WriteString("…")
			return false
		}
		sb.WriteRune(r)
		runes++
		return true
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = runes > 0
			continue
		}
		if space {
			space = false
			if !add(' ') {
				break
			}
		}
		if !add(r) {
			break
		}
	}
	return sb.String()
}

// PreviewBytes is Preview of a payload read from a bounded prefix, so a
// large payload costs no more than a small one. A cut prefix ends with
// "…".
func PreviewBytes(b []byte, n int) string {
	limit := 4*n + 4 // enough bytes for n runes of UTF-8, plus a space
	if len(b) <= limit {
		return Preview(Text(b), n)
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	s := Preview(Text(b[:cut]), n)
	if !strings.HasSuffix(s, "…") {
		s += "…"
	}
	return s
}

// VisibleID is an ID (or other name) for display with its bidirectional
// control characters (U+061C, U+200E, U+200F, U+202A–U+202E,
// U+2066–U+2069) replaced by U+FFFD, so they cannot reorder the text
// around them. Titles and copy buttons keep the real ID.
func VisibleID(s string) string {
	if !strings.ContainsFunc(s, isBidiControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isBidiControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

func isBidiControl(r rune) bool {
	switch {
	case r == '\u061C', r == '\u200E', r == '\u200F':
		return true
	case r >= '\u202A' && r <= '\u202E', r >= '\u2066' && r <= '\u2069':
		return true
	}
	return false
}
