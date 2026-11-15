# Contributing

Development tasks are in `Taskfile.yml`, and `task` lists them. Install [Task](https://taskfile.dev/installation/) or run it as `go tool -modfile=tools/go.mod task`. Before you push, run `task check`. It runs the same checks as CI.

## Reporting issues

Report bugs and request features with the [issue forms](https://github.com/BarkinBalci/asynqui/issues/new/choose). Report security issues as [SECURITY.md](SECURITY.md) describes, not in an issue.

## Documentation

| Document | Covers |
|---|---|
| [configuration.md](docs/configuration.md) | Flags, environment variables, the config file and every key |
| [authentication.md](docs/authentication.md) | Sign-in providers, sessions, roles, cookies |
| [deployment.md](docs/deployment.md) | Installing and running asynqui in production |
| [design.md](docs/design.md) | The design decisions behind asynqui, and why |
| [architecture.md](docs/architecture.md) | How the code is organised and the conventions it follows |
| [releasing.md](docs/releasing.md) | Cutting a release |

When you change behaviour, update the document that describes it. If another document needs the same information, link to it rather than copying it.

## Local development

You need:

- Go, at the version in `go.mod`
- Docker, for the integration tests (they skip without it)
- a Redis or Valkey to run against

`task run` starts asynqui against `localhost:6379` with `config.dev.yaml`, which has no login and gives you full control.

`task demo` fills a **throwaway** Redis with the browser tests' fixture and serves asynqui on it until you press Ctrl-C. The fixture's workers and schedulers keep running meanwhile. It flushes DBs 0–2.

```sh
ASYNQUI_E2E_REDIS=localhost:6379 task demo   # http://127.0.0.1:8080
task demo ADDR=127.0.0.1:9000                # without ASYNQUI_E2E_REDIS, testcontainers starts Redis in Docker
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
| anything else, such as `refactor:` | Other |

A scope and `!` for breaking changes are fine: `feat(auth)!: drop the session cookie fallback`. Keep the subject short, in the imperative, without a trailing period.

## Browser tests

The browser tests are in `e2e/`, behind the `e2e` build tag, so `go test ./...` doesn't need a browser. They start the built `cmd/asynqui` against one shared fixture and drive Chromium through [playwright-go](https://github.com/mxschmitt/playwright-go).

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
| `ASYNQUI_E2E_REDIS=host:port` | The Redis to use. If unset, testcontainers starts one in Docker. |
| `ASYNQUI_E2E_HEADED=1` | Shows the browser |

Each run leaves a video per test in `e2e/test-results/`, and a `.trace.zip` for each failed test. CI uploads the directory as the `e2e-test-results` artifact. Open a trace with `go run github.com/mxschmitt/playwright-go/cmd/playwright show-trace <file>.trace.zip` or on [trace.playwright.dev](https://trace.playwright.dev).

axe-core is vendored in `e2e/testdata`. To upgrade it, replace the directory with the new release's `axe.min.js` and `LICENSE` and update the path in `e2e/a11y_test.go`. The accessibility check fails on any violation, minor ones included.

## Version stamping

`task build` stamps `main.version` with the output of `git describe`. Set `VERSION=x` to override it. Release builds stamp the tag without its `v`. The browser tests stamp the latest release tag, so the README images show a released version. A build without a stamped version reports the module version that the go command recorded: the tag for `go install …/cmd/asynqui@v0.1.0`, a pseudo-version for `go build` in a git checkout, and `dev` when there is none, as with `go run`.
