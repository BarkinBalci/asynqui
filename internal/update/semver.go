package update

import (
	"strings"

	"golang.org/x/mod/semver"
)

// releaseVersion returns v, with a leading "v" added if it had none, and
// whether it is a release version: a valid semantic version whose
// major.minor.patch is complete and has no prerelease part. Build metadata
// ("+dirty") does not take part. Git describe output
// ("v0.1.0-3-gabc1234-dirty"), pseudo-versions and "dev" are not releases.
func releaseVersion(v string) (string, bool) {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	core, _, _ := strings.Cut(v, "+")
	return v, semver.IsValid(v) && semver.Canonical(core) == core && semver.Prerelease(core) == ""
}
