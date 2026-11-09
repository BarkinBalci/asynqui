// Package format is the only place numbers, dates, durations and IDs are
// turned into display text. Templates and handlers call it instead of
// fmt, strconv or time.Format, so every page renders values the same way.
package format

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Number formats n with a comma between each group of three digits:
// 1284 → "1,284", -1204 → "-1,204".
func Number(n int64) string {
	s := strconv.FormatInt(n, 10)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return sign + s
	}
	var b strings.Builder
	b.Grow(len(sign) + len(s) + len(s)/3)
	b.WriteString(sign)
	head := len(s) % 3
	if head == 0 {
		head = 3
	}
	b.WriteString(s[:head])
	for i := head; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// Duration formats d with its two largest units, truncated to whole
// seconds: "0s", "38s", "1m 05s", "4m 12s", "2h 03m", "3d 4h". The second
// unit of minutes and hours is zero-padded so columns of values align.
// Negative durations format as "0s".
func Duration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	secs := int64(d / time.Second)
	days, secs := secs/86400, secs%86400
	hours, secs := secs/3600, secs%3600
	mins, secs := secs/60, secs%60
	switch {
	case days > 0:
		return strconv.FormatInt(days, 10) + "d " + strconv.FormatInt(hours, 10) + "h"
	case hours > 0:
		return strconv.FormatInt(hours, 10) + "h " + pad2(mins) + "m"
	case mins > 0:
		return strconv.FormatInt(mins, 10) + "m " + pad2(secs) + "s"
	}
	return strconv.FormatInt(secs, 10) + "s"
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// TimeAttr formats t for a <time datetime> attribute: RFC 3339 in UTC,
// to the second ("2026-11-08T00:11:52Z").
func TimeAttr(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// TimeText formats t as the absolute UTC text shown inside <time> until
// app.js rewrites it in the viewer's time zone ("Nov 8, 00:11:52").
func TimeText(t time.Time) string {
	return t.UTC().Format("Jan 2, 15:04:05")
}

// ShortID shortens an ID for display. A UUID keeps its first two groups
// ("c3e81b90-04aa…"); any other ID longer than 12 characters keeps its first
// 12 characters; shorter IDs are returned whole. Show the full ID in a title
// and copy the full ID.
func ShortID(id string) string {
	if isUUID(id) {
		return id[:13] + "…"
	}
	if utf8.RuneCountInString(id) <= 12 {
		return id
	}
	n := 0
	for i := range id {
		if n == 12 {
			return id[:i] + "…"
		}
		n++
	}
	return id
}

// isUUID reports whether s is a canonical 8-4-4-4-12 hex UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// Version formats a build version for the sidebar footer and login page:
// release versions read "v0.1.0" whether or not the build string carries
// the "v"; anything else (such as "dev") is shown as is.
func Version(v string) string {
	s := strings.TrimPrefix(v, "v")
	if s != "" && '0' <= s[0] && s[0] <= '9' {
		return "v" + s
	}
	return v
}
