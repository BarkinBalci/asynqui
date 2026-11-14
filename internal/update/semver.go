package update

import (
	"strconv"
	"strings"
)

// semver is a parsed semantic version. Build metadata is dropped, since it
// does not take part in precedence.
type semver struct {
	major, minor, patch uint64
	pre                 string // "" for a release
}

// parseSemver parses a semantic version 2.0.0 string with an optional
// leading "v", as git tags and GoReleaser's {{.Version}} write it.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	s, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !validIdents(build, false) {
		return semver{}, false
	}
	core, pre, hasPre := strings.Cut(s, "-")
	if hasPre && !validIdents(pre, true) {
		return semver{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]uint64
	for i, p := range parts {
		if !numeric(p) || len(p) > 1 && p[0] == '0' {
			return semver{}, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, true
}

// validIdents reports whether s is a dot-separated list of non-empty
// [0-9A-Za-z-] identifiers. In a prerelease, numeric identifiers must not
// have leading zeros.
func validIdents(s string, pre bool) bool {
	for id := range strings.SplitSeq(s, ".") {
		if id == "" {
			return false
		}
		for i := range len(id) {
			if !identChar(id[i]) {
				return false
			}
		}
		if pre && numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func identChar(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '-'
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// olderThan reports whether release v precedes release r. Both have no
// prerelease part: New refuses one for v, and check ignores one for r.
func (v semver) olderThan(r semver) bool {
	if v.major != r.major {
		return v.major < r.major
	}
	if v.minor != r.minor {
		return v.minor < r.minor
	}
	if v.patch != r.patch {
		return v.patch < r.patch
	}
	return false
}
