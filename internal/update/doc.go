// Package update checks GitHub for a newer asynqui release.
//
// New builds a Checker for the running version. It refuses builds that are
// not a release, which are never checked: anything that is not a semantic
// version (such as "dev") or has a prerelease part (release candidates, git
// describe and dirty builds). Run asks the GitHub releases API for the
// latest release once at startup and then every 24h, until its context
// ends. Latest reports the newest release seen,
// when it is newer than the running version.
//
// The request sends no credentials, config or user data; GitHub sees the
// server's IP address and, from the User-Agent, the running version.
//
// The check is best effort: a failed request, an unexpected answer or a
// release that is a draft, a prerelease or not a semantic version is logged
// at debug level and leaves the last result in place.
package update
