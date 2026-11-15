package format

import (
	"crypto/tls"
	"math"
	"strconv"
	"time"
)

// Int formats an identifier-like integer without group separators: a PID
// ("41", "40213") or a DB number. Counts use Number.
func Int(n int64) string {
	return strconv.FormatInt(n, 10)
}

// Uptime formats how long a server has run: whole days from one day on
// ("1 day", "41 days"), Duration below that ("3h 05m").
func Uptime(d time.Duration) string {
	days := int64(d / (24 * time.Hour))
	switch {
	case days == 1:
		return "1 day"
	case days > 1:
		return Number(days) + " days"
	}
	return Duration(d)
}

// Relative formats t relative to now as app.js does, so the page reads
// the same before and after app.js runs: "38s ago", "3m ago", "2h ago",
// "4d ago" for the past (a second of skew counts as past), and "in 2m
// 40s", "in 3h 05m" for the future.
func Relative(t, now time.Time) string {
	secs := int64(math.Round(now.Sub(t).Seconds()))
	if secs >= -1 {
		secs = max(secs, 0)
		switch {
		case secs < 60:
			return strconv.FormatInt(secs, 10) + "s ago"
		case secs < 3600:
			return strconv.FormatInt(secs/60, 10) + "m ago"
		case secs < 86400:
			return strconv.FormatInt(secs/3600, 10) + "h ago"
		}
		return strconv.FormatInt(secs/86400, 10) + "d ago"
	}
	return "in " + Duration(time.Duration(-secs)*time.Second)
}

// TLSVersion names a negotiated TLS version ("TLS 1.3"); 0 (no handshake
// yet) is "".
func TLSVersion(v uint16) string {
	if v == 0 {
		return ""
	}
	return tls.VersionName(v)
}
