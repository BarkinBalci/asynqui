# Releasing

How a maintainer cuts a release. What a release contains is in [deployment.md](deployment.md#install).

## Cutting a release

Push a tag `vX.Y.Z` (or `vX.Y.Z-rc.N` for a prerelease) on `main`. `.github/workflows/release.yml` runs the tests, then GoReleaser publishes the GitHub release, the archives and the images. The changelog is grouped by the commit prefixes in [CONTRIBUTING.md](../CONTRIBUTING.md#commit-messages).

- **No backport releases.** Cut releases only from the main line, in increasing version order. Each new release becomes GitHub's "latest", which the update check and the `latest` image tag follow, so a `v0.1.5` published after `v0.2.0` would point users backwards. Fix forward instead.
- Builds and archives are reproducible: `.goreleaser.yaml` pins file timestamps and ownership to the commit.
- To bump GoReleaser, change the version in `release.yml` and run `go run github.com/goreleaser/goreleaser/v2@<version> check`.

## First release checklist

1. Push the first tag and wait for the workflow to finish.
2. GHCR creates `ghcr.io/barkinbalci/asynqui` as a private package. In its package settings on GitHub, set the visibility to **Public** and check that it is linked to the repository.
3. Confirm an anonymous pull works from a machine that is not logged in to ghcr.io:

   ```sh
   docker logout ghcr.io
   docker pull ghcr.io/barkinbalci/asynqui:<version>
   ```
