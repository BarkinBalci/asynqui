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
│   ├── conn/                  # Registry: name → inspector/redis; health loop; per-connection cache
│   ├── auth/                  # none | basic | proxy | oidc; session cookie
│   ├── authz/                 # roles and per-connection policy: Role(user, conn), Can(...)
│   └── web/
│       ├── server.go          # NewServer(deps...) http.Handler
│       ├── routes.go          # every URL, in one file, with its required role
│       ├── handle_*.go        # one file per area: queues, tasks, servers, schedulers, info, auth, health
│       ├── middleware.go
│       ├── format/            # the only place numbers, dates and durations are formatted
│       ├── chart/             # SVG chart geometry: nice axis scales, path data (no text)
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
                 ↘ chart
                 ↘ views → format
```

- Nothing imports `web`.
- `views` imports only its own components and `web/format`.
- `format`, `chart` and `config` import no other internal package.

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
		Handler:     handler, // web.NewServer(web.Deps{Logger, Config, Auth, Policy, Conns: reg, Ready, Version}); fails on a bad trusted origin
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
	mux.Handle("POST /login", handleLoginSubmit(d))            // basic form; 404 for every other provider
	mux.Handle("GET /login/sso", handleSSO(d))                 // oidc "Sign in with SSO" link: starts the flow
	mux.Handle("GET /auth/callback", handleOIDCCallback(d))
	mux.Handle("POST /logout", handleLogout(d))

	// preferences: no-JS fallback of the theme and refresh controls (no auth)
	mux.Handle("POST /prefs", handlePrefs(d))

	// pages (viewer)
	mux.Handle("GET /{$}", view(d, handleOverview(d)))
	mux.Handle("GET /c/{conn}", view(d, handleQueues(d)))                        // ?sort=size&dir=desc&range=14d&queue=
	mux.Handle("GET /c/{conn}/queues/{queue}", view(d, handleQueue(d)))           // ?state=pending&page=2&size=20&sort=
	mux.Handle("GET /c/{conn}/queues/{queue}/tasks/{id}", view(d, handleTask(d)))
	mux.Handle("GET /c/{conn}/servers", view(d, handleServers(d)))
	mux.Handle("GET /c/{conn}/servers/{id}", view(d, handleServer(d)))
	mux.Handle("GET /c/{conn}/schedulers", view(d, handleSchedulers(d)))
	mux.Handle("GET /c/{conn}/schedulers/{id}", view(d, handleSchedulerEntry(d)))
	mux.Handle("GET /c/{conn}/info", view(d, handleRedisInfo(d)))
	mux.Handle("POST /c/{conn}/recheck", view(d, handleRecheck(d)))              // Overview "Retry now"

	// actions (operator)
	mux.Handle("POST /c/{conn}/queues/pause", op(d, handleQueueAction(d, pages.VerbPause)))  // form: queue=a&queue=b&next=
	mux.Handle("POST /c/{conn}/queues/resume", op(d, handleQueueAction(d, pages.VerbResume)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/run", op(d, handleTasksRun(d)))          // ids or all=1&state=
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/archive", op(d, handleTasksArchive(d)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/cancel", op(d, handleTasksCancel(d)))

	// admin
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/delete", admin(d, handleTasksDelete(d)))
	mux.Handle("POST /c/{conn}/queues/delete", admin(d, handleQueueAction(d, pages.VerbDelete))) // + confirm=1

	// assets
	mux.Handle("GET /static/", handleStatic())

	// everything else: 404 through renderError
	mux.Handle("/", handleNotFound(d))
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
| Queue data and actions | `github.com/hibiken/asynq`: Inspector for reads and actions |
| PING, INFO | `github.com/redis/go-redis/v9` (already a dependency of asynq) |
| HTML components | `github.com/a-h/templ`; codegen runs through `go tool -modfile=tools/go.mod templ` |
| Config | `go.yaml.in/yaml/v3`, the maintained successor to the archived `gopkg.in/yaml.v3` |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| Basic-auth hashes | `golang.org/x/crypto/bcrypt` |
| Session cookie | `github.com/gorilla/securecookie` |
| CSRF | stdlib `http.CrossOriginProtection` (Go 1.25+), based on `Sec-Fetch-Site`, so no tokens are needed |
| Routing, logging, flags | stdlib `net/http`, `log/slog`, `flag` |
| Coalescing | a small hand-rolled single-flight in `conn` (typed keys, panic recovery) |

**Frontend** (vendored and embedded; no npm and no build step)

- **htmx 2.x:** one vendored file.
- **Charts:** server-rendered SVG from templ, with no chart library. `web/chart` computes the scales and path data. The SVG stretches to its box (`preserveAspectRatio="none"`, non-scaling strokes); axis labels are HTML at fixed fractions of the height (every axis has three equal steps of a round size); colours come from CSS classes; each day has a hover band with a native `<title>` tooltip; a visually hidden table carries the same data.
- **Fonts:** Inter and JetBrains Mono, self-hosted (OFL).
- **`app.js`:** one small file for:
  - copy to clipboard
  - row selection: the "N selected" toolbar, enabling its buttons, select-all (and Gmail-style "select all N" for tasks)
  - confirmation dialogs (`<dialog>`)
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

- **`conn.Registry`:** one per process. It holds a `*asynq.Inspector` and a `redis.UniversalClient` per connection, sharing one go-redis client (one pool), all created at startup without Redis I/O. asynqui never enqueues, so it has no `asynq.Client`.
- **Timeouts:** each connection's `dial_timeout` (5s), `read_timeout` (3s) and `write_timeout` (3s) go to its Redis client. There is no global Redis timeout. Our own commands (PING, INFO) also run under a context deadline; Inspector calls take no context, so only these socket timeouts bound them.
- **Per-connection cache:** each snapshot (Overview stats, queue list, servers, …) is cached for **1s**, keyed by connection and snapshot, and concurrent fetches of it are shared. N viewers polling the same connection cost at most one Redis round per second.
- **Health loop:** one goroutine per connection sends PING every 10s and stores the result (reachable, unreachable + error, or unknown before the first result). The Overview, sidebar and badges only read it. `handleRecheck` runs a check synchronously and stores it.
- **Concurrency:** connections are fetched in parallel. A failing connection renders "Connection failed" and does not block the page.
- **Page budget:** the Overview waits at most 1.5s for summaries (`context.WithTimeout`). A card that misses it renders as "Checking"; the fetch goes on in conn's cache, so the next poll has it. If a reachable connection's Summary fails, the card and the sidebar both show it down.
- **Overview cost:** `GetQueueInfo` per queue (one snapshot, shared with the Connection page's table), plus `Servers()` per connection. Instance-wide INFO is fetched only by the Info page.
- **Connection page cost:** the same queue snapshot, plus `History` per queue for the chart (one snapshot per queue and range). The two are read in parallel and the page waits at most 5s for them; a miss renders "Checking" (in the chart body for the history). A successful pause, resume or delete drops the connection's cached snapshots, so the next read shows it.

## Frontend conventions

- **`views/layout.templ`:** the page shell (sidebar, top bar, theme attribute from the cookie).
- **`views/components/`:** shared pieces (table, toolbar, badge, stat card, code block, dialog, toast, sidebar, time). These map 1:1 to the style guide.
- **`views/pages/`:** one file per page. Each takes one view-model struct.
- **Forms:** actions are `<form method="post">`, enhanced with `hx-post`. Destructive and "all" actions go through a confirmation dialog.
- **htmx config** (set in `app.js`, before any request): `htmx.config.allowEval = false`, `htmx.config.includeIndicatorStyles = false` (it would inject an inline `<style>`). No `hx-on` or `js:` attributes.
- **Polling:** `hx-trigger="every 5s [document.visibilityState === 'visible']"` cannot be used, because trigger filters need eval. `app.js` owns visibility gating instead: it starts the interval timers, skips ticks while `document.hidden`, and fires one refresh on `visibilitychange` to visible. With "Off", no timer runs.
- **Polled regions keep focus:** a poll never replaces a whole region. The Overview grid has `hx-swap="none"`, and the response replaces each card, the heading badge, the Updated time and the sidebar list out of band by id (`hx-swap-oob`). htmx refocuses the element with the same id after a swap, so every focusable element in a polled region has a stable id; ids use one prefix per element kind (`card-`, `open-`, `retry-`, `nav-`), so connection names cannot make them collide.
- **Connection page requests:** `#queues-page` wraps the page and is the single `hx-sync` scope of its requests. The poll (`hx-sync="#queues-page:abort"`) is dropped while another request is in flight and aborted when a user request starts; sorts, range and picker changes inherit `this:queue last`; actions use `queue all`, so none is cut off.
- **Connection page polling:** a hidden `#queues-poll` element polls the page URL with `hx-swap="none"`; the server recognises it by `HX-Trigger`. Every answer replaces, out of band, the poll element itself (so its URL follows the sort and chart state), the health badge, the state banner, the Updated time and the sidebar list. A poll then replaces the table region and only the chart body (`#tp-chart`), never the chart controls, so an open select stays open. When a poll finds no data while the page shows a table, the table and its selection stay and the banner says why. A page rendered without data polls with `?poll=init`, which gets every region. Sort links, the range buttons and the queue picker (user requests) get every region, controls included, and push their URL, so all state is in the query (`?sort=&dir=&range=&queue=`) and bookmarkable. The toolbar, filter input and dialog sit outside the swapped regions.
- **Selection survives polls without server state:** row checkboxes (and select-all) carry `hx-preserve` and a stable id (`qsel-` plus the hex of the queue name), so htmx keeps the very same elements, checked state included, when it swaps in a new table; `app.js` recomputes the toolbar right after each swap, re-applies the filter at once, and gives focus back to the element that had it (htmx briefly takes preserved elements out of the document, which drops their focus). The checkboxes belong to an empty action form through their `form` attribute, so the table need not sit inside a form.
- **Actions:** toolbar buttons submit that form (`formaction`, plus `hx-post` with `hx-swap="none"`). The htmx answer is a user-request answer plus error toasts appended to `#toasts` out of band. Without JS it is always a redirect back (post, redirect, get); failures ride in the short-lived `asynqui_flash` cookie, which the page shows as toasts once and clears, so a reload never posts again. A delete opens a `<dialog>` whose confirm button posts `confirm=1`; without JS the first post answers with a confirmation page.
- **No cross-origin forms:** the CSP `form-action 'self'` also applies to a form's redirects, so anything that leaves the origin (SSO, proxy sign out) is a link.
- **Time:** the `time` component renders `<time datetime="RFC3339 UTC">` with the absolute UTC text from `format`. `app.js` rewrites it to local relative text with the absolute local time in `title`, and re-renders relative text on each tick.
- **Assets:** served with content-hashed filenames and `Cache-Control: immutable`. `//go:embed files` (without `all:`), so dotfiles are never embedded.

## Testing

1. **Component tests:** render templ components with fixture view models and assert on HTML with goquery (columns, badges, empty states, disabled buttons per role).
2. **Handler tests:** `httptest` through the full stack against a real Redis from testcontainers. They cover:
   - full page vs `HX-Request` fragment
   - role enforcement (403/404)
   - actions mutating Redis
   - "not found" and "Connection failed" states
3. **Browser smoke tests** (about 10, a separate CI job, milestone M7): playwright-go for the behaviour that lives in `app.js` and htmx and that Go tests cannot run (selection across polls, focus, dialogs, `hx-sync`); handler and component tests only check the markup that drives it:
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
