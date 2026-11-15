# Contributing

Dev tasks live in `Taskfile.yml`; run `task` to list them. Install [Task](https://taskfile.dev/installation/) or run it as `go tool -modfile=tools/go.mod task`. Run `task check` before pushing; it runs the CI check job.

Code conventions are in [architecture.md](architecture.md). Each topic has one owning doc: behaviour and configuration in [requirements.md](requirements.md), internals in architecture.md, production in [deployment.md](deployment.md), the dev workflow here. Link to the owner instead of repeating it.

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

## Browser tests

`e2e/` holds the browser tests, behind the `e2e` build tag, so `go test ./...` needs no browser. They start the built `cmd/asynqui` against one shared fixture and drive Chromium through [playwright-go](https://github.com/mxschmitt/playwright-go).

1. Install the driver and Chromium once:

   ```sh
   go run github.com/mxschmitt/playwright-go/cmd/playwright install --with-deps chromium
   ```

2. Run them against a **throwaway** Redis, which they flush:

   ```sh
   ASYNQUI_E2E_REDIS=localhost:6379 task e2e
   go test -tags e2e ./e2e -run 'TestBrowser/FindTask' -v   # one test
   ```

| Variable | Effect |
|---|---|
| `ASYNQUI_E2E_REDIS=host:port` | The Redis to use; unset, testcontainers starts one (Docker) |
| `ASYNQUI_E2E_HEADED=1` | Shows the browser |

Each run leaves a video per test in `e2e/test-results/`, and a `.trace.zip` for each failed test; CI uploads the directory as the `e2e-test-results` artifact. Open a trace with `go run github.com/mxschmitt/playwright-go/cmd/playwright show-trace <file>.trace.zip` or on [trace.playwright.dev](https://trace.playwright.dev).

axe-core is vendored in `e2e/testdata`. To upgrade it, replace the directory with the new release's `axe.min.js` and `LICENSE` and update the path in `e2e/a11y_test.go`. The accessibility check fails on any violation, minor ones included.

## Demo GIF

The README's `docs/images/demo.gif` is recorded from the `RunRetries` browser test. Regenerate it with `ASYNQUI_E2E_REDIS=localhost:6379 task demo-gif` when a page in that flow changes, and watch it before committing.

## Releases

Push a tag `vX.Y.Z` (or `vX.Y.Z-rc.N` for a prerelease) on `main`. `.github/workflows/release.yml` runs the tests, then GoReleaser publishes the GitHub release, the archives and the images.

- **No backport releases.** Cut releases only from the main line, in increasing version order. Each new release becomes GitHub's "latest", which the update check and the `latest` image tag follow, so a `v0.1.5` published after `v0.2.0` would point users backwards. Fix forward instead.
- To bump GoReleaser, change the version in `release.yml` and run `go run github.com/goreleaser/goreleaser/v2@<version> check`.

### First release checklist

1. Push the first tag and wait for the workflow to finish.
2. GHCR creates `ghcr.io/barkinbalci/asynqui` as a private package. In its package settings on GitHub, set the visibility to **Public** and check that it is linked to the repository.
3. Confirm an anonymous pull works from a machine that is not logged in to ghcr.io:

   ```sh
   docker logout ghcr.io
   docker pull ghcr.io/barkinbalci/asynqui:<version>
   ```
