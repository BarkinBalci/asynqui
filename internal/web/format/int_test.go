package format

import (
	"crypto/tls"
	"testing"
	"time"
)

func TestInt(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 41: "41", 40213: "40213", -1: "-1"} {
		if got := Int(n); got != want {
			t.Errorf("Int(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestUptime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{3*time.Hour + 5*time.Minute, "3h 05m"},
		{24 * time.Hour, "1 day"},
		{47 * time.Hour, "1 day"},
		{41*24*time.Hour + 2*time.Hour, "41 days"},
		{1200 * 24 * time.Hour, "1,200 days"},
	}
	for _, tt := range tests {
		if got := Uptime(tt.d); got != tt.want {
			t.Errorf("Uptime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestRelative(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		d    time.Duration // t = now + d
		want string
	}{
		{0, "0s ago"},
		{time.Second, "0s ago"}, // skew
		{-38 * time.Second, "38s ago"},
		{-3*time.Minute - 59*time.Second, "3m ago"},
		{-18 * time.Hour, "18h ago"},
		{-50 * time.Hour, "2d ago"},
		{2*time.Minute + 40*time.Second, "in 2m 40s"},
		{5*time.Hour + 45*time.Minute, "in 5h 45m"},
		{1500 * time.Millisecond, "in 2s"},
	}
	for _, tt := range tests {
		if got := Relative(now.Add(tt.d), now); got != tt.want {
			t.Errorf("Relative(now%+v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTLSVersion(t *testing.T) {
	if TLSVersion(0) != "" || TLSVersion(tls.VersionTLS13) != "TLS 1.3" || TLSVersion(tls.VersionTLS12) != "TLS 1.2" {
		t.Errorf("TLSVersion = %q %q", TLSVersion(tls.VersionTLS13), TLSVersion(tls.VersionTLS12))
	}
}
