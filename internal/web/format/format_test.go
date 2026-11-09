package format

import (
	"math"
	"testing"
	"time"
)

func TestNumber(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{1284, "1,284"},
		{61033, "61,033"},
		{482190, "482,190"},
		{1000000, "1,000,000"},
		{-5, "-5"},
		{-1204, "-1,204"},
		{-999999, "-999,999"},
		{math.MaxInt64, "9,223,372,036,854,775,807"},
		{math.MinInt64, "-9,223,372,036,854,775,808"},
	}
	for _, tt := range tests {
		if got := Number(tt.in); got != tt.want {
			t.Errorf("Number(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-time.Minute, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{time.Second, "1s"},
		{38*time.Second + 900*time.Millisecond, "38s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m 00s"},
		{65 * time.Second, "1m 05s"},
		{4*time.Minute + 12*time.Second, "4m 12s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"},
		{2*time.Hour + 3*time.Minute + 59*time.Second, "2h 03m"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d 0h"},
		{3*24*time.Hour + 4*time.Hour + 30*time.Minute, "3d 4h"},
		{400 * 24 * time.Hour, "400d 0h"},
	}
	for _, tt := range tests {
		if got := Duration(tt.in); got != tt.want {
			t.Errorf("Duration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTime(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	tests := []struct {
		in       time.Time
		wantAttr string
		wantText string
	}{
		{time.Date(2026, 11, 8, 0, 11, 52, 0, time.UTC), "2026-11-08T00:11:52Z", "Nov 8, 00:11:52"},
		{time.Date(2026, 11, 8, 5, 41, 52, 999_000_000, ist), "2026-11-08T00:11:52Z", "Nov 8, 00:11:52"},
		{time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC), "2026-01-31T23:59:59Z", "Jan 31, 23:59:59"},
	}
	for _, tt := range tests {
		if got := TimeAttr(tt.in); got != tt.wantAttr {
			t.Errorf("TimeAttr(%v) = %q, want %q", tt.in, got, tt.wantAttr)
		}
		if got := TimeText(tt.in); got != tt.wantText {
			t.Errorf("TimeText(%v) = %q, want %q", tt.in, got, tt.wantText)
		}
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"123456789012", "123456789012"},
		{"1234567890123", "123456789012…"},
		{"c3e81b90-04aa-4f1e-9d27-6b52e0c8a113", "c3e81b90-04aa…"},
		{"C3E81B90-04AA-4F1E-9D27-6B52E0C8A113", "C3E81B90-04AA…"},
		// Not a UUID: wrong group layout, non-hex digit.
		{"c3e81b90004aa-4f1e-9d27-6b52e0c8a113", "c3e81b90004a…"},
		{"g3e81b90-04aa-4f1e-9d27-6b52e0c8a113", "g3e81b90-04a…"},
		// Counted in characters, not bytes.
		{"ééééééééééééé", "éééééééééééé…"},
		{"éééééééééééé", "éééééééééééé"},
		{"host-1:1234:5a6b7c8d", "host-1:1234:…"},
	}
	for _, tt := range tests {
		if got := ShortID(tt.in); got != tt.want {
			t.Errorf("ShortID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0.1.0", "v0.1.0"},
		{"v0.1.0", "v0.1.0"},
		{"v1.2.3-4-gabcdef-dirty", "v1.2.3-4-gabcdef-dirty"},
		{"dev", "dev"},
		{"abc1234", "abc1234"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Version(tt.in); got != tt.want {
			t.Errorf("Version(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
