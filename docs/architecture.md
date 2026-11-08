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
├── cmd/asynqui/
│   ├── main.go                # tiny main; run(ctx, args, lookupEnv, stdout, stderr): wiring and lifecycle
│   └── flags.go               # flags + ASYNQUI_* env, precedence
├── internal/
│   ├── config/                # YAML load, ${ENV} interpolation, defaults, validation
│   ├── conn/                  # Registry: name → inspector/client/redis; health loop; per-connection cache
│   ├── auth/                  # none | basic | proxy | oidc; session cookie
│   ├── authz/                 # roles and per-connection policy: Role(user, conn), Can(...)
│   └── web/
│       ├── server.go          # NewServer(deps...) http.Handler
│       ├── routes.go          # every URL, in one file, with its required role
│       ├── handle_*.go        # one file per area: queues, tasks, servers, schedulers, info, auth, health
│       ├── middleware.go
│       ├── format/            # the only place numbers, dates and durations are formatted
│       ├── views/             # .templ: layout.templ, components/, pages/ (+ generated *_templ.go, committed)
│       └── static/            # files/: css, htmx, app.js, fonts, logo; //go:embed files
├── tools/go.mod               # templ and golangci-lint, run with go tool -modfile=tools/go.mod
├── docs/
├── config.example.yaml        # every key; loads without env vars
├── config.dev.yaml            # make run: localhost:6379, auth none, admin
├── Makefile
└── .github/workflows/ci.yml
```

### Dependency direction (one way, no cycles)

```
cmd/asynqui → web → conn, auth, authz → config
                 ↘ views → format
```

- Nothing imports `web`.
- `views` imports only its own components and `web/format`.
- `format` and `config` import no other internal package.

## Rules

1. **No globals; dependencies are explicit.** The only package-level variables are `main.version` (set by `-ldflags`) and the embedded `static.FS`. Sentinel errors (`var ErrX = errors.New(...)`) count as constants and are allowed; nothing else is. Handlers are constructors that return `http.Handler`, e.g. `func handleQueueList(reg *conn.Registry, …) http.Handler`. Per-handler setup runs once, inside the closure.
2. **`routes.go` is the map.** Every method, path and required role lives in that one file.
3. **Packages are named for what they do.** No `utils`, `common`, `helpers`, `models` or `pkg/`.
4. **Thin layers.** A handler fetches through `conn`, builds a plain view-model struct and renders a templ component. Templates never call Redis and contain no business logic.
5. **One handler per page, for full pages and fragments.** When `HX-Request` is set (polling, sorting, paging), the handler renders only the fragment. Otherwise it renders the full page. Every URL works without htmx and is bookmarkable.
6. **Small interfaces, declared where they're used,** and only where a test needs a fake.
7. **Errors and logs:** wrap errors with `%w`, log with `log/slog` (structured), and run every Redis call under the request context. Never log secrets.
8. **UI text lives in templ.** Go code never builds user-facing strings. It returns typed errors or codes (`errors.Is`/`errors.As`) that templates map to copy. English only.
9. **Formatting goes through `web/format`.** No `fmt.Sprintf("%d")`, `time.Format` or `humanize` calls in handlers or templates outside it.
10. **CSS:** one hand-written stylesheet using CSS custom properties taken from the canvas style guide. Dark mode swaps tokens under `[data-theme=dark]`, and `[data-theme=system]` follows `prefers-color-scheme`. No Tailwind and no CSS build step.
11. **CSP-clean markup.** No inline `<script>`, `<style>`, `style=""` or `on*=` attributes; use classes, SVG attributes and `app.js`.
12. **Minimal dependencies.** Use the standard library where it is good enough (see Libraries).

## Wiring and lifecycle

All dependencies are built in `run()` and passed down explicitly. There is no DI framework (no wire, fx or dig). Product rules for startup, shutdown and precedence are in [requirements.md §7](requirements.md#7-operational).

```go
func main() {
	err := run(context.Background(), os.Args, os.LookupEnv, os.Stdout, os.Stderr)
	// nil → exit 0; usageError → exit 2 (flag already printed); else print to stderr, exit 1
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts, err := parseFlags(args, lookupEnv, stderr) // flag > env; -h → opts.help, return nil
	// --- fail fast: everything below can fail and runs before Listen ---
	cfg, err := config.Load(opts.configPath, lookupEnv, overrides) // file > default; flags/env override
	authn, err := auth.New(ctx, …)                               // OIDC discovery happens here
	reg, err := conn.NewRegistry(cfg.Connections, logger)          // no Redis I/O; fails only on unreadable TLS files
	defer reg.Close()                                             // stops the health loops, then closes the clients
	srv := &http.Server{
		Handler:     web.NewServer(logger, cfg, reg, authn, policy, &ready),
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
		// ReadHeaderTimeout 10s, ReadTimeout 30s, WriteTimeout 60s, IdleTimeout 120s
	}
	ln, err := lc.Listen(ctx, "tcp", cfg.Server.Addr)
	go srv.Serve(ln); reg.Start(ctx)                         // health loops in the background

	<-ctx.Done() // or a Serve error
	stop()                           // a second signal now force-quits
	ready.Drain()                    // readiness 503, terminal
	time.Sleep(cfg.Server.ShutdownDelay) // default 5s, 0 skips
	srv.Shutdown(10s timeout)        // on timeout: srv.Close(), return error
}
```

- `lookupEnv` (not `getenv`) so interpolation can tell an unset variable from an empty one.
- `overrides` carries only the flags seen by `fs.Visit` and the non-empty `ASYNQUI_*` variables, so the file wins over defaults but not over them.
- `BaseContext` is detached from the signal: in-flight requests keep their context until `Shutdown` drains them.
- `web.Readiness` has one transition, `Drain()`. Its zero value is ready.

## Routes

Go 1.22+ `http.ServeMux` patterns only. There is no third-party router. Paths below are relative to `base_path`.

```go
func addRoutes(mux *http.ServeMux, d deps) {
	// probes (no auth, not logged)
	mux.Handle("GET /health/liveness", handleLiveness())
	mux.Handle("GET /health/readiness", handleReadiness(d.ready))

	// auth
	mux.Handle("GET /login", handleLogin(d))
	mux.Handle("POST /login", handleLoginSubmit(d))            // basic form; oidc "Sign in with SSO" starts the flow
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

- **Base path:** `NewServer` mounts the app mux on an outer mux at `<base>/` with `http.StripPrefix(base, …)`. Everything else under the outer mux is 404. With an empty base the app mux is the root.
- **Links:** one helper, `d.url(segments ...string) string` (a method, so it does not shadow `net/url`), builds every link, redirect and cookie path: it prefixes `base_path` and escapes each segment with `url.PathEscape`. Templates never concatenate paths. Query strings use `url.Values`.
- **Wrappers:** `view`, `op` and `admin` read the identity set by the auth middleware and resolve `{conn}`.
  - No identity: `authn.Challenge` (303 to login, or 401 + `HX-Redirect` for htmx, or 401 for `proxy`/`none`).
  - Hidden connection or unknown name: 404.
  - Insufficient role: 403.
- **Auth middleware** only identifies; it never rejects. A proxy request with untrusted identity headers is marked as rejected, and the wrappers answer it with 401.
- **Global middleware**, applied once in `NewServer`, outermost first: request logging (skips `/health/*`) → panic recovery (logs `debug.Stack()`, writes 500 only if the header is unsent) → security headers (CSP, nosniff, Referrer-Policy) → `http.CrossOriginProtection` (+ `server.trusted_origins` via `AddTrustedOrigin`) → authentication → app mux.
- **Gotchas:** `GET /static/` is a prefix match, while `GET /c/{conn}` is exact. `GET /{$}` matches only `/`. A more specific pattern wins regardless of registration order. Conflicting patterns panic at startup.

### Probes

- `/health/liveness` answers "is the process alive?". If it fails, a restart might help.
- `/health/readiness` answers "should this instance receive traffic?". It is 200 while serving and turns 503 for good when shutdown starts, `shutdown_delay` before the listener closes, so load balancers drain first.
- Neither checks Redis. A Redis outage is shown in the UI per connection; it must not make the orchestrator restart or unroute every replica.

## Libraries

**Runtime (Go)**

| Need | Library |
|---|---|
| Queue data and actions | `github.com/hibiken/asynq`: Inspector for reads and actions, Client for scheduler "Run now" |
| PING, INFO | `github.com/redis/go-redis/v9` (already a dependency of asynq) |
| HTML components | `github.com/a-h/templ`; codegen runs through `go tool -modfile=tools/go.mod templ` |
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
  - polling timers, paused while the tab is hidden
  - local-time rendering of `<time>` elements
  - toasts

**Test and tooling**

| Need | Library |
|---|---|
| Real Redis | `github.com/testcontainers/testcontainers-go` (+ redis module) |
| HTML assertions | `github.com/PuerkitoBio/goquery` |
| Diffs | `github.com/google/go-cmp` (no assertion library) |
| Browser smoke tests | `github.com/playwright-community/playwright-go` |
| Lint | golangci-lint v2, pinned with a `tool` directive in `tools/go.mod` |
| Releases | GoReleaser |

## Data access and Redis load

- **`conn.Registry`:** one per process. It holds a `*asynq.Inspector`, an `*asynq.Client` and a `redis.UniversalClient` per connection, all sharing one go-redis client (one pool) and created at startup without Redis I/O.
- **Timeouts:** each connection's `dial_timeout` (5s), `read_timeout` (3s) and `write_timeout` (3s) go to its Redis client. There is no global Redis timeout. Every call also runs under the request context.
- **Per-connection cache:** each snapshot (Overview stats, queue list, servers, …) is cached for **1s** behind `singleflight`, keyed by connection and snapshot. N viewers polling the same connection cost at most one Redis round per second.
- **Health loop:** one goroutine per connection sends PING every 10s and stores the result (reachable, unreachable + error, or unknown before the first result). The Overview, sidebar and badges only read it. `handleRecheck` runs a check synchronously and stores it.
- **Concurrency:** connections are fetched in parallel. A failing connection renders "Connection failed" and does not block the page.
- **Overview cost:** `GetQueueInfo` per queue, plus `Servers()` per connection. Instance-wide INFO is fetched only by the Info page.

## Frontend conventions

- **`views/layout.templ`:** the page shell (sidebar, top bar, theme attribute from the cookie).
- **`views/components/`:** shared pieces (table, toolbar, badge, stat card, code block, dialog, toast, sidebar, time). These map 1:1 to the style guide.
- **`views/pages/`:** one file per page. Each takes one view-model struct.
- **Forms:** actions are `<form method="post">`, enhanced with `hx-post`. Destructive and "all" actions go through a confirmation dialog.
- **htmx config** (set in `app.js`, before any request): `htmx.config.allowEval = false`, `htmx.config.includeIndicatorStyles = false` (it would inject an inline `<style>`). No `hx-on` or `js:` attributes.
- **Polling:** `hx-trigger="every 5s [document.visibilityState === 'visible']"` cannot be used, because trigger filters need eval. `app.js` owns visibility gating instead: it starts the interval timers, skips ticks while `document.hidden`, and fires one refresh on `visibilitychange` to visible. With "Off", no timer runs.
- **Time:** the `time` component renders `<time datetime="RFC3339 UTC">` with the absolute UTC text from `format`. `app.js` rewrites it to local relative text with the absolute local time in `title`, and re-renders relative text on each tick.
- **Assets:** served with content-hashed filenames and `Cache-Control: immutable`. `//go:embed files` (without `all:`), so dotfiles are never embedded.

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
- **Style:** tests are table-driven. Subtests never share mutable state: each builds its own fixtures, so any of them can run alone or in parallel.
- **`run()`** has an end-to-end test on `127.0.0.1:0`: it reads the bound address from the "listening" log line, cancels the context and checks that readiness is 503 while the listener still serves during `shutdown_delay`, then that the listener closes and `run` returns nil.
- **Race detector:** CI runs `go test -race`. It needs cgo and a C compiler, so locally it is `make test-race`; `make test` runs without it.
- **Out of scope for v1:** pixel-diff visual regression.

## Tooling and CI

- **Tools module:** `tools/go.mod` pins templ and golangci-lint with `tool` directives. Run them as `go tool -modfile=tools/go.mod <tool>`; the main `go.mod` lists only runtime and test dependencies.
- **Go version:** `go 1.26.0` with `toolchain go1.26.10` in both `go.mod` files. CI installs `1.26.x`.
- **Makefile:** `make help` lists the targets (`generate`, `lint`, `test`, `test-race`, `run`, `build`, `tidy`).
- **CI** (`.github/workflows/ci.yml`, actions pinned by commit SHA): `go mod tidy -diff` for both modules, templ generate + `git status --porcelain` must be empty, vet, golangci-lint, `go test -race`, static build.

## Documentation plan

- **README.md:** what asynqui is, a screenshot, and the quickstart.
- **`config.example.yaml`:** every option with comments. CI checks that it parses and validates.
- **`docs/`:** requirements (behaviour, config reference), architecture (this file) and [deployment](deployment.md) now; authentication, authorization and contributing guides later.
- **`CHANGELOG.md`:** generated by GoReleaser.
- **pkg.go.dev:** shows only the `cmd/asynqui` package comment, which points to the README.
- **A docs site:** Hugo on GitHub Pages, added only once `docs/` outgrows plain Markdown.
