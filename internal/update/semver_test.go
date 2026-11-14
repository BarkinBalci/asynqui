package update

import "testing"

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in   string
		want semver
		ok   bool
	}{
		{"0.1.0", semver{0, 1, 0, ""}, true},
		{"v1.22.333", semver{1, 22, 333, ""}, true},
		{"1.0.0-rc.1", semver{1, 0, 0, "rc.1"}, true},
		{"1.0.0-alpha-1.x", semver{1, 0, 0, "alpha-1.x"}, true},
		{"v0.1.0-3-gabc1234-dirty", semver{0, 1, 0, "3-gabc1234-dirty"}, true},
		{"1.0.0+build.5", semver{1, 0, 0, ""}, true},
		{"1.0.0-rc.1+build", semver{1, 0, 0, "rc.1"}, true},
		{"dev", semver{}, false},
		{"", semver{}, false},
		{"v", semver{}, false},
		{"1.0", semver{}, false},
		{"1.0.0.0", semver{}, false},
		{"01.0.0", semver{}, false},
		{"1.0.0-", semver{}, false},
		{"1.0.0-rc..1", semver{}, false},
		{"1.0.0-01", semver{}, false},
		{"1.0.0+", semver{}, false},
		{"1.0.0-rc_1", semver{}, false},
		{"1.-1.0", semver{}, false},
		{"99999999999999999999.0.0", semver{}, false},
	}
	for _, tt := range tests {
		got, ok := parseSemver(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseSemver(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOlderThan(t *testing.T) {
	tests := []struct {
		v, r string
		want bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.1.9", "0.1.10", true},
		{"0.9.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
	}
	for _, tt := range tests {
		v, _ := parseSemver(tt.v)
		r, _ := parseSemver(tt.r)
		if got := v.olderThan(r); got != tt.want {
			t.Errorf("%s.olderThan(%s) = %v", tt.v, tt.r, got)
		}
	}
}
