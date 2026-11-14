package update

import (
	"strings"

	"golang.org/x/mod/semver"
)

// releaseVersion returns v, with a leading "v" added if it had none, and whether
// it is a release version: a canonical semantic version with no
// prerelease part and no build metadata. Git describe output
// ("v0.1.0-3-gabc1234-dirty"), pseudo-versions, "+dirty" builds and "dev"
// are not.
func releaseVersion(v string) (string, bool) {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v, semver.Canonical(v) == v && semver.Prerelease(v) == ""
}
