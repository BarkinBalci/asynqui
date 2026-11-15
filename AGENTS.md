# AGENTS.md

asynqui is a web dashboard for [hibiken/asynq](https://github.com/hibiken/asynq), written in Go with templ and htmx.

## Commands

Tasks are in `Taskfile.yml`. Run them with `task`, or with `go tool -modfile=tools/go.mod task` if Task isn't installed.

- `task check` runs the CI checks: tidy, templ, vet, lint, tests and build. Run it before you push.
- `task generate` regenerates the templ code. Run it after you edit a `.templ` file, and commit the generated `*_templ.go` files with it.
- `ASYNQUI_E2E_REDIS=localhost:6379 task e2e` runs the browser tests against a throwaway Redis, which they flush. Without the variable they start Redis in Docker. See [CONTRIBUTING.md](CONTRIBUTING.md#browser-tests) for the one-time Chromium install.

## Where to look

- [docs/design.md](docs/design.md): how asynqui behaves. Update it when you change behaviour.
- [docs/architecture.md](docs/architecture.md): how the code is organised and the conventions it follows.
- [CONTRIBUTING.md](CONTRIBUTING.md): local development, tests and commit messages.

## Rules

- Use only asynq's public API. The one exception is the direct reads in `internal/conn/keys.go` ([architecture](docs/architecture.md#data-access-and-redis-load)). If a feature needs more than that, raise it in an issue instead of working around asynq.
- No package-level variables beyond the ones [architecture rule 1](docs/architecture.md#rules) lists.
- Markup must pass the CSP: no inline scripts, styles, `style=""` or `on*=` attributes ([architecture rule 11](docs/architecture.md#rules)).
- Keep UI text minimal, with no helper or explanatory copy ([design](docs/design.md#copy-and-visual-rules)).
- Comments and docs say what something is and how to use it. No asides that justify a choice (those go in the commit message), no lists that go stale, and write for people.
- Commit subjects use [Conventional Commits](CONTRIBUTING.md#commit-messages).
- Tests are table-driven and use small hand-written fakes, with no mock framework ([testing](docs/architecture.md#testing)).
