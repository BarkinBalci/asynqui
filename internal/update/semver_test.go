package update

import "testing"

func TestReleaseVersion(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"0.1.0", "v0.1.0", true},
		{"v1.22.333", "v1.22.333", true},
		{"1.0.0-rc.1", "v1.0.0-rc.1", false},
		{"v0.1.0-3-gabc1234-dirty", "v0.1.0-3-gabc1234-dirty", false},
		{"v0.1.1-0.20261114015959-c1a81a5e0d4f", "v0.1.1-0.20261114015959-c1a81a5e0d4f", false},
		{"v0.1.0+dirty", "v0.1.0+dirty", false},
		{"1.0.0+build.5", "v1.0.0+build.5", false},
		{"dev", "vdev", false},
		{"", "v", false},
		{"1.0", "v1.0", false},
		{"1.0.0.0", "v1.0.0.0", false},
		{"01.0.0", "v01.0.0", false},
		{"1.0.0-", "v1.0.0-", false},
		{"1.-1.0", "v1.-1.0", false},
	}
	for _, tt := range tests {
		if got, ok := releaseVersion(tt.in); got != tt.want || ok != tt.ok {
			t.Errorf("releaseVersion(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
