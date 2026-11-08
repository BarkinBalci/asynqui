# asynqui architecture

How the code is organised and the rules it follows. Product behaviour is specified in [requirements.md](requirements.md).

**References:**
- Go's [Organizing a Go module](https://go.dev/doc/modules/layout), the "server project" layout.
- Mat Ryer, [How I write HTTP services in Go after 13 years](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/).
- The [Google Go Style Guide](https://google.github.io/styleguide/go/), on top of Effective Go.

We do **not** follow `golang-standards/project-layout` or Clean/hexagonal architecture. asynq already owns the domain, and asynqui is a UI over it.

## Layout

```
asynqui/
├── cmd/asynqui/main.go        # tiny: calls run(ctx, args, getenv, stdout) and exits
├── internal/
│   ├── config/                # YAML load, ${ENV} interpolation, validation
│   ├── conn/                  # Registry: name → inspector/client/redis; health; shared per-connection cache
│   ├── auth/                  # none | basic | proxy | oidc; session cookie
│   ├── authz/                 # roles and per-connection policy: Role(user, conn), Can(...)
│   └── web/
│       ├── server.go          # NewServer(deps...) http.Handler
│       ├── routes.go          # every URL, in one file, with its required role
│       ├── handle_*.go        # one file per area: queues, tasks, servers, schedulers, info, auth
│       ├── middleware.go
│       ├── views/             # .templ: layout.templ, components/, pages/ (+ generated *_templ.go, committed)
│       └── static/            # css, htmx, app.js, fonts, logo; embedded with go:embed
├── docs/
├── config.example.yaml
├── Makefile
└── .github/workflows/ci.yml
```

### Dependency direction (one way, no cycles)

```
cmd/asynqui → web → conn, auth, authz → config
                 ↘ views (templ; plain view-model structs only)
```

- Nothing imports `web`.
- `views` imports nothing of ours except its own components.
- `config` imports no other internal package.

## Rules

1. **No globals; dependencies are explicit.** Handlers are constructors that return `http.Handler`, e.g. `func handleQueueList(reg *conn.Registry, …) http.Handler`. Per-handler setup runs once, inside the closure.
2. **`routes.go` is the map.** Every method, path and required role lives in that one file.
3. **Packages are named for what they do.** No `utils`, `common`, `helpers`, `models` or `pkg/`.
4. **Thin layers.** A handler fetches through `conn`, builds a plain view-model struct and renders a templ component. Templates never call Redis and contain no business logic.
5. **One handler per page, for full pages and fragments.** When `HX-Request` is set (polling, sorting, paging), the handler renders only the fragment. Otherwise it renders the full page. Every URL works without htmx and is bookmarkable.
6. **Small interfaces, declared where they're used,** and only where a test needs a fake.
7. **Errors and logs:** wrap errors with `%w`, log with `log/slog` (structured), and give every Redis call a `context` with a timeout. Never log secrets.
8. **CSS:** one hand-written stylesheet using CSS custom properties taken from the canvas style guide. Dark mode swaps tokens under `[data-theme=dark]`, and `[data-theme=system]` follows `prefers-color-scheme`. No Tailwind and no CSS build step.
9. **Minimal dependencies.** Use the standard library where it is good enough (see Libraries).

## Wiring

All dependencies are built in `run()` and passed down explicitly. There is no DI framework (no wire, fx or dig).

```go
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(args, getenv)
	if err != nil { return err }
	reg := conn.NewRegistry(cfg.Connections, logger)
	defer reg.Close()
	authn, err := auth.New(ctx, cfg.Auth)
	if err != nil { return err }
	srv := web.NewServer(logger, cfg, reg, authn, authz.New(cfg.Authz))
	// http.Server with Read/Write/Idle timeouts, ListenAndServe, graceful Shutdown on ctx.Done()
}
```

## Routes

Go 1.22+ `http.ServeMux` patterns only. There is no third-party router.

```go
func addRoutes(mux *http.ServeMux, d deps) {
	// probes (no auth, not logged)
	mux.Handle("GET /health/liveness", handleLive())
	mux.Handle("GET /health/readiness", handleReady(d))

	// auth
	mux.Handle("GET /login", handleLogin(d))
	mux.Handle("POST /login", handleLoginSubmit(d))
	mux.Handle("GET /auth/callback", handleOIDCCallback(d))
	mux.Handle("POST /logout", handleLogout(d))

	// pages (viewer)
	mux.Handle("GET /{$}", view(d, handleOverview(d)))
	mux.Handle("GET /c/{conn}", view(d, handleQueues(d)))
	mux.Handle("GET /c/{conn}/queues/{queue}", view(d, handleQueue(d)))           // ?state=pending&page=2&size=20&sort=
	mux.Handle("GET /c/{conn}/queues/{queue}/tasks/{id}", view(d, handleTask(d)))
	mux.Handle("GET /c/{conn}/servers", view(d, handleServers(d)))
	mux.Handle("GET /c/{conn}/servers/{id}", view(d, handleServer(d)))
	mux.Handle("GET /c/{conn}/schedulers", view(d, handleSchedulers(d)))
	mux.Handle("GET /c/{conn}/schedulers/{id}", view(d, handleSchedulerEntry(d)))
	mux.Handle("GET /c/{conn}/info", view(d, handleRedisInfo(d)))
	mux.Handle("GET /c/{conn}/throughput", view(d, handleThroughput(d)))          // chart fragment: ?range=7d&queue=
	mux.Handle("POST /c/{conn}/recheck", view(d, handleRecheck(d)))              // Overview "Retry now"

	// actions (operator)
	mux.Handle("POST /c/{conn}/queues/pause", op(d, handlePause(d)))               // form: queue=a&queue=b
	mux.Handle("POST /c/{conn}/queues/resume", op(d, handleResume(d)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/run", op(d, handleTasksRun(d)))          // ids or all=1&state=
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/archive", op(d, handleTasksArchive(d)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/cancel", op(d, handleTasksCancel(d)))
	mux.Handle("POST /c/{conn}/schedulers/run", op(d, handleSchedulerRunNow(d)))   // form: id=…&id=…

	// admin
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/delete", admin(d, handleTasksDelete(d)))
	mux.Handle("POST /c/{conn}/queues/delete", admin(d, handleQueuesDelete(d)))

	// assets
	mux.Handle("GET /static/", handleStatic())
}
```

- **Wrappers:** `view`, `op` and `admin` require a session and resolve `{conn}`. A connection the user cannot see returns 404. An insufficient role returns 403.
- **Global middleware**, applied once in `NewServer`: request logging (skipped for `/health/*`), panic recovery, `http.CrossOriginProtection`, authentication and security headers.
- **Gotchas:** `GET /static/` is a prefix match, while `GET /c/{conn}` is exact. `GET /{$}` matches only `/`. A more specific pattern wins regardless of registration order. Conflicting patterns panic at startup.

### Probes

- `/health/liveness` answers "is the process alive?". If it fails, a restart might help.
- `/health/readiness` answers "should this instance receive traffic?". It turns 503 when shutdown starts, so load balancers drain before the listener closes.
- Neither checks Redis. A Redis outage is shown in the UI per connection; it must not make the orchestrator restart or unroute every replica.

## Libraries

**Runtime (Go)**

| Need | Library |
|---|---|
| Queue data and actions | `github.com/hibiken/asynq`: Inspector for reads and actions, Client for scheduler "Run now" |
| PING, INFO | `github.com/redis/go-redis/v9` (already a dependency of asynq) |
| HTML components | `github.com/a-h/templ`; codegen runs through `go tool templ` |
| Config | `go.yaml.in/yaml/v3`, the maintained successor to the archived `gopkg.in/yaml.v3` |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| Basic-auth hashes | `golang.org/x/crypto/bcrypt` |
| Session cookie | `github.com/gorilla/securecookie` |
| CSRF | stdlib `http.CrossOriginProtection` (Go 1.25+), based on `Sec-Fetch-Site`, so no tokens are needed |
| Routing, logging, flags | stdlib `net/http`, `log/slog`, `flag` |
| Coalescing | `golang.org/x/sync/singleflight` |

**Frontend** (vendored and embedded; no npm and no build step)

- **htmx 2.x:** one vendored file.
- **Charts:** server-rendered SVG from templ. Fall back to uPlot only if hover tooltips can't be done well in SVG plus a little JS.
- **Fonts:** Inter and JetBrains Mono, self-hosted (OFL).
- **`app.js`:** one small file for:
  - copy to clipboard
  - Gmail-style select-all
  - the theme toggle
  - pausing polling while the tab is hidden

**Test and tooling**

| Need | Library |
|---|---|
| Real Redis | `github.com/testcontainers/testcontainers-go` (+ redis module) |
| HTML assertions | `github.com/PuerkitoBio/goquery` |
| Diffs | `github.com/google/go-cmp` (no assertion library) |
| Browser smoke tests | `github.com/playwright-community/playwright-go` |
| Lint | golangci-lint v2, pinned with a `tool` directive in `go.mod` |
| Releases | GoReleaser |

## Data access and Redis load

- **`conn.Registry`:** one per process. It holds a `*asynq.Inspector`, an `*asynq.Client` and a `redis.UniversalClient` per connection, all created at startup.
- **Per-connection cache:** each snapshot (Overview stats, queue list) is cached for `min(refresh interval, 2s)` behind `singleflight`. N viewers polling the same connection cost one Redis round per interval.
- **Concurrency:** connections are fetched in parallel, each with its own timeout (`redis.timeout`, default 2s). A failing connection renders "Connection failed" and does not block the page.
- **Hidden tabs:** polling pauses (`hx-trigger="every 5s [document.visibilityState === 'visible']"` plus a `visibilitychange` refresh).
- **Overview cost:** `GetQueueInfo` per queue, plus `Servers()` per connection. Instance-wide INFO is fetched only by the Info page.

## Frontend conventions

- **`views/layout.templ`:** the page shell (sidebar, top bar, theme attribute from the cookie).
- **`views/components/`:** shared pieces (table, toolbar, badge, stat card, code block, dialog, sidebar). These map 1:1 to the style guide.
- **`views/pages/`:** one file per page. Each takes one view-model struct.
- **Forms:** actions are `<form method="post">`, enhanced with `hx-post`. Destructive and "all" actions go through a confirmation dialog.
- **Assets:** served with content-hashed filenames and `Cache-Control: immutable`.

## Testing

1. **Component tests:** render templ components with fixture view models and assert on HTML with goquery (columns, badges, empty states, disabled buttons per role).
2. **Handler tests:** `httptest` through the full stack against a real Redis from testcontainers. They cover:
   - full page vs `HX-Request` fragment
   - role enforcement (403/404)
   - actions mutating Redis
   - "not found" and "Connection failed" states
3. **Browser smoke tests** (about 10, a separate CI job): playwright-go for:
   - select-all then bulk action
   - copy
   - theme without a flash
   - polling
   - an axe-core accessibility check per page

Other conventions:
- **No mock frameworks.** Use small hand-written fakes for error paths and a fake OIDC provider built on `httptest.Server`.
- **Time:** inject `now func() time.Time` where output depends on the clock.
- **Style:** tests are table-driven. CI runs `go test -race`.
- **Out of scope for v1:** pixel-diff visual regression.

## Documentation plan

- **README.md:** what asynqui is, a screenshot, and the quickstart.
- **`config.example.yaml`:** every option with comments. CI checks that it parses and validates.
- **`docs/`:** configuration, authentication, authorization, deployment, architecture (this file), requirements and contributing.
- **`CHANGELOG.md`:** generated by GoReleaser.
- **pkg.go.dev:** shows only the `cmd/asynqui` package comment, which points to the README.
- **A docs site:** Hugo on GitHub Pages, added only once `docs/` outgrows plain Markdown.
