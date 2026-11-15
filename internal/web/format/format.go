// Package format is the only place numbers, dates, durations and IDs are
// turned into display text.
package format

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/mod/module"
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

// Latency formats a queue's latency: tenths of a second under a minute
// ("0.4s", "12.6s", "48.0s"), truncated, and Duration from a minute on
// ("1m 05s"). Negative latencies format as "0.0s".
func Latency(d time.Duration) string {
	if d < time.Minute {
		tenths := max(int64(d/(100*time.Millisecond)), 0)
		return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + "s"
	}
	return Duration(d)
}

// Bytes formats a size in binary units with one decimal: "512 B",
// "3.1 KB", "41.8 MB", "2.0 GB". Negative sizes format as "0 B".
func Bytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(max(n, 0), 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1023.95 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
}

// Compact formats a count for chart axes: as Number under 1,000, then with
// k, M or B and at most one decimal ("1.5k", "70k", "2.5M").
func Compact(n int64) string {
	if n > -1000 && n < 1000 {
		return Number(n)
	}
	v := float64(n)
	for _, u := range []struct {
		div    float64
		suffix string
	}{{1e9, "B"}, {1e6, "M"}, {1e3, "k"}} {
		if math.Abs(v) >= u.div*0.99995 || u.suffix == "k" {
			s := strconv.FormatFloat(v/u.div, 'f', 1, 64)
			return strings.TrimSuffix(s, ".0") + u.suffix
		}
	}
	return Number(n) // not reached
}

// Percent formats part/whole as a percentage with at most two decimals
// ("0.23%", "12.5%", "100%"). A zero whole is "0%".
func Percent(part, whole int64) string {
	if whole == 0 {
		return "0%"
	}
	s := strconv.FormatFloat(float64(part)/float64(whole)*100, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	return s + "%"
}

// Day formats a UTC calendar day for chart labels ("Nov 8").
func Day(t time.Time) string {
	return t.UTC().Format("Jan 2")
}

// WeekdayDay formats a UTC calendar day with its weekday ("Sun, Nov 8").
func WeekdayDay(t time.Time) string {
	return t.UTC().Format("Mon, Jan 2")
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

// Version formats a build version for the sidebar footer and login page.
// Release versions read "v0.1.0" whether or not the build string carries the
// "v". A development build whose prerelease part names a commit (a Go
// pseudo-version, git describe output or a GoReleaser snapshot) reads "dev"
// plus the commit's first 7 hex digits, such as "dev f9b5ecf". Other
// prereleases ("v0.2.0-rc.1") keep their full form; anything else (such as
// "dev") is shown as is.
func Version(v string) string {
	s := strings.TrimPrefix(v, "v")
	if s == "" || s[0] < '0' || s[0] > '9' {
		return v
	}
	if sha := buildCommit(s); sha != "" {
		return "dev " + sha
	}
	return "v" + s
}

// IsShortVersion reports whether Version(v) dropped part of v, so the full
// string belongs in a title.
func IsShortVersion(v string) bool {
	return strings.HasPrefix(Version(v), "dev ")
}

// buildCommit returns the short commit hash named by the prerelease part of
// version s (without its "v"), or "" when it names none.
func buildCommit(s string) string {
	if v := "v" + s; module.IsPseudoVersion(v) {
		rev, _ := module.PseudoVersionRev(v)
		return rev[:min(len(rev), 7)]
	}
	s, _, _ = strings.Cut(s, "+") // build metadata, e.g. "+dirty"
	_, pre, ok := strings.Cut(s, "-")
	if !ok {
		return ""
	}
	parts := strings.Split(pre, "-")
	if parts[len(parts)-1] == "dirty" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return ""
	}
	last, prev := parts[len(parts)-1], parts[len(parts)-2]
	var sha string
	switch {
	case strings.HasPrefix(last, "g") && allHex(last[1:]) && len(last) > 4 && allDigits(prev):
		sha = last[1:] // git describe: <tag>-<count>-g<hash>
	case prev == "SNAPSHOT" && len(last) >= 7 && allHex(last):
		sha = last // GoReleaser snapshot: <version>-SNAPSHOT-<hash>
	default:
		return ""
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return sha
}

func allHex(s string) bool {
	for i := range len(s) {
		if !isHex(s[i]) {
			return false
		}
	}
	return s != ""
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
