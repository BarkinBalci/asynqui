package update

import (
	"strings"

	"golang.org/x/mod/semver"
)

// releaseVersion returns v, with a leading "v" added if it had none, and
// whether it is a release version: a valid semantic version with a complete
// major.minor.patch and no prerelease part. Build metadata ("+dirty") does not
// take part.
func releaseVersion(v string) (string, bool) {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	core, _, _ := strings.Cut(v, "+")
	return v, semver.IsValid(v) && semver.Canonical(core) == core && semver.Prerelease(core) == ""
}
