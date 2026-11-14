// Package update checks GitHub for a newer asynqui release.
//
// New builds a Checker for the running version; it refuses builds that are
// not a semantic version (such as "dev"), which are never checked. Run asks
// the GitHub releases API for the latest release once at startup and then
// every 24h, until its context ends. Latest reports the newest release seen,
// when it is newer than the running version.
//
// The check is best effort: a failed request, an unexpected answer or a
// release that is a draft, a prerelease or not a semantic version is logged
// at debug level and leaves the last result in place.
package update
