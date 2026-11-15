# Releasing

How to cut a release. [deployment.md](deployment.md#install) describes what a release contains.

## Cutting a release

Before tagging, if the UI changed, regenerate the README images with the upcoming version: `VERSION=0.1.2 task screenshots` and `VERSION=0.1.2 task demo-gif`.

Push a tag `vX.Y.Z` (or `vX.Y.Z-rc.N` for a prerelease) on `main`. `.github/workflows/release.yml` runs the tests, then GoReleaser publishes the GitHub release, the archives and the images. The changelog is grouped by the commit prefixes in [CONTRIBUTING.md](../CONTRIBUTING.md#commit-messages).

- Builds and archives are reproducible: `.goreleaser.yaml` takes file timestamps and ownership from the commit.
- To bump GoReleaser, change the version in `release.yml` and run `go run github.com/goreleaser/goreleaser/v2@<version> check`.
