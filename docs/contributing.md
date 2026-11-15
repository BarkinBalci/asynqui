# Contributing

Dev tasks live in `Taskfile.yml`. Install [Task](https://taskfile.dev/installation/) or run it as `go tool -modfile=tools/go.mod task`. Run `task check` before pushing; it runs the CI check job.

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

`e2e/` holds the browser smoke tests: the behaviour of `app.js` and htmx (polling, focus and selection across polls, dialogs, copy, theme, login) and an axe-core and CSP check of every page. They sit behind the `e2e` build tag, so `go test ./...` needs no browser. CI runs them in the `e2e` job.

The harness flushes DBs 0 to 2 of a Redis and seeds one realistic fixture that every test uses (`e2e/fixture_test.go`: the connections payments, notifications and reports, with workers and schedulers, plus analytics on a dead port). It builds `cmd/asynqui`, starts it twice on random ports (auth `none` with admin, and basic auth with an admin and a viewer) and drives headless Chromium through [playwright-go](https://github.com/mxschmitt/playwright-go).

1. Install the driver and Chromium once, at the version in `go.mod` (`--with-deps` also installs the system libraries, with sudo):

   ```sh
   go run github.com/mxschmitt/playwright-go/cmd/playwright install --with-deps chromium
   ```

2. Point the tests at a **throwaway** Redis, which they flush, or leave `ASYNQUI_E2E_REDIS` unset to start one with testcontainers (Docker):

   ```sh
   ASYNQUI_E2E_REDIS=localhost:6379 task e2e
   go test -tags e2e ./e2e -run 'TestBrowser/FindTask' -v   # one test
   ```

Every run records a video per test, `e2e/test-results/TestBrowser/<Subtest>.webm`, and a failed test also saves `<Subtest>.trace.zip` next to it. The directory is emptied at the start of each run. In CI, the `e2e` job uploads it as the `e2e-test-results` artifact (kept 7 days), whether the tests pass or fail. Open a trace with:

```sh
go run github.com/mxschmitt/playwright-go/cmd/playwright show-trace e2e/test-results/TestBrowser/<Subtest>.trace.zip
```

or drop the zip on [trace.playwright.dev](https://trace.playwright.dev), which reads it locally in the browser.

These variables change how a run looks, never what it tests:

| Variable | Effect |
|---|---|
| `ASYNQUI_E2E_PACE=1` | Slow motion (400ms after each action), a drawn cursor and a 1200x750 viewport |
| `ASYNQUI_E2E_HEADED=1` | Shows Chromium (on WSL through WSLg); combine with `PACE` to watch |
| `ASYNQUI_E2E_GIF=<Subtest>` | Writes that subtest as `docs/images/demo.gif` if it passes |

The axe-core build is vendored in `e2e/testdata` (never fetched at run time); to upgrade it, replace the directory with the new release's `axe.min.js` and `LICENSE` and update the path in `e2e/a11y_test.go`. The accessibility check fails on any violation, minor ones included.

## Demo GIF

The README's `docs/images/demo.gif` is the `RunRetries` browser test: Overview, payments, the webhooks queue, the Retry tab, three tasks selected and Run now. `task demo-gif` runs that one test with `ASYNQUI_E2E_PACE=1` and `ASYNQUI_E2E_GIF=RunRetries` and overwrites the GIF. It needs the same Chromium and Redis as the browser tests: `ASYNQUI_E2E_REDIS=localhost:6379 task demo-gif`. Regenerate it when a page in the flow changes and watch it before committing.

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
