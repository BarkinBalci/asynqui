# Contributing

Dev tasks live in `Taskfile.yml`; run `task` to list them. Install [Task](https://taskfile.dev/installation/) or run it as `go tool -modfile=tools/go.mod task`. Run `task check` before pushing; it runs the CI check job.

Code conventions are in [architecture.md](architecture.md). Each topic has one owning doc; link to the owner instead of repeating it.

| Doc | Owns |
|---|---|
| [configuration.md](configuration.md) | flags, environment, precedence, startup, logging, the config file and every key |
| [authentication.md](authentication.md) | providers, sessions, roles and bindings, cookies |
| [deployment.md](deployment.md) | install, Kubernetes, probes, shutdown, reverse proxy, update check |
| [design.md](design.md) | the behaviour spec: pages, UI rules, sign-in flow, HTTP behaviour |
| [architecture.md](architecture.md) | code layout, rules and internals |
| contributing.md | the dev workflow |
| [releasing.md](releasing.md) | cutting a release |

## Local development

You need:

- Go, at the version in `go.mod`
- Docker, for the integration tests (they skip without it)
- a Redis or Valkey to run against

`task run` starts asynqui against `localhost:6379` with `config.dev.yaml`: no login, full control.

`task demo` fills a **throwaway** Redis with the browser tests' fixture and serves asynqui on it, with the fixture's workers and schedulers running, until Ctrl-C. It flushes DBs 0–2 and shows four connections, one of them unreachable.

```sh
ASYNQUI_E2E_REDIS=localhost:6379 task demo   # http://127.0.0.1:8080
task demo ADDR=127.0.0.1:9000                # without ASYNQUI_E2E_REDIS, testcontainers starts a Redis (Docker)
```

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

## Version stamping

`task build` stamps `main.version` with the `git describe` output (override with `VERSION=x`); release builds stamp the tag without its `v`. A build without a stamped version reports the module version the go command recorded: the tag for `go install …/cmd/asynqui@v0.1.0`, a pseudo-version for `go build` in a git checkout, and `dev` when there is none (`go run`).
