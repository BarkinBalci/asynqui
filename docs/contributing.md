# Contributing

## Commit messages

Start each commit subject with a [Conventional Commits](https://www.conventionalcommits.org/) prefix. GoReleaser groups the release changelog by it:

| Prefix | Changelog group |
|---|---|
| `feat:` | Features |
| `fix:` | Bug fixes |
| `perf:` | Performance |
| `docs:` | Documentation |
| `test:`, `ci:`, `chore:`, `build:` | left out |
| anything else (e.g. `refactor:`) | Other |

A scope and `!` for breaking changes are fine: `feat(auth)!: drop the session cookie fallback`. Keep the subject short, in the imperative, without a trailing period.

## Releases

Push a tag `vX.Y.Z` (or `vX.Y.Z-rc.N` for a prerelease) on `main`. `.github/workflows/release.yml` runs the tests, then GoReleaser publishes the GitHub release, the archives and the images.

- **No backport releases.** Releases are cut only from the main line, in increasing version order. GitHub marks each new release as "latest", and the update check and the `latest` image tag follow it, so a `v0.1.5` published after `v0.2.0` would become "latest" and point users backwards. Fix forward in the next release instead.
- **GoReleaser version:** pinned in the workflow and in the header of `.goreleaser.yaml`; change both together and run `go run github.com/goreleaser/goreleaser/v2@<version> check`.

### First release checklist

1. Push the first tag and wait for the workflow to finish.
2. GHCR creates `ghcr.io/barkinbalci/asynqui` as a private package. In the package settings on GitHub, set its visibility to **Public** (and check that it is linked to the repository).
3. Confirm an anonymous pull works from a machine that is not logged in to ghcr.io:

   ```sh
   docker logout ghcr.io
   docker pull ghcr.io/barkinbalci/asynqui:<version>
   ```
