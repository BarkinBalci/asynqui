# asynqui architecture

How the code is organised and the conventions it follows. For how asynqui behaves, see [design.md](design.md).

**References:**
- Go's [Organizing a Go module](https://go.dev/doc/modules/layout), the "server project" layout.
- Mat Ryer, [How I write HTTP services in Go after 13 years](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/).
- The [Google Go Style Guide](https://google.github.io/styleguide/go/), on top of Effective Go.

asynqui does **not** follow `golang-standards/project-layout` or Clean/hexagonal architecture: asynq holds the domain logic, and asynqui is a UI on top of it.

## Layout

```
cmd/asynqui/        main, run(): flags, wiring and lifecycle
internal/
  config/           YAML load, interpolation, defaults, validation
  conn/             connection registry, health loop, per-connection cache, asynq reads and actions
  auth/             none | basic | proxy | oidc, session cookie
  authz/            roles and per-connection policy
  update/           GitHub release check
  web/              server, routes, middleware, one handle_*.go per area
    format/         the only place numbers, dates and durations are formatted
    chart/          SVG chart geometry (no text)
    views/          templ: layout, components/, pages/ (generated *_templ.go committed)
    static/         embedded css, htmx, app.js, fonts, logo
e2e/                browser tests (build tag e2e)
tools/go.mod        dev tools, run with go tool -modfile=tools/go.mod
```

### Dependency direction (one way, no cycles)

```
cmd/asynqui → web → conn, auth, authz → config
            ↘ update     ↘ chart
                         ↘ views → format
```

- Nothing imports `web`.
- `web` sees `update` only through its `UpdateSource` interface.
- `views` imports only its own components and `web/format`.
- `format`, `chart`, `config` and `update` import no other internal package.

## Rules

1. **No globals. Pass dependencies explicitly.** The only package-level variables are `main.version` (set by `-ldflags`), the embedded `static.FS` and sentinel errors (`var ErrX = errors.New(...)`). Handlers are constructors that return `http.Handler`. Any per-handler setup runs once, inside the closure.
2. **`routes.go` is the map.** Every method, path and required role lives in that one file.
3. **Packages are named for what they do.** No `utils`, `common`, `helpers`, `models` or `pkg/`.
4. **Thin layers.** A handler fetches through `conn`, builds a plain view-model struct and renders a templ component. Templates never call Redis and contain no business logic.
5. **One handler per page, for full pages and fragments.** With `HX-Request` the handler renders only the fragment, otherwise the full page. Every URL works without htmx and is bookmarkable.
6. **Small interfaces, declared where they're used,** and only where a test needs a fake.
7. **Errors and logs:** wrap errors with `%w`, log with `log/slog`, run every Redis call under the request context. Never log secrets.
8. **UI text lives in templ.** Go code never builds user-facing strings. It returns typed errors or codes (`errors.Is`/`errors.As`) that templates map to copy.
9. **Formatting goes through `web/format`.** No `fmt.Sprintf("%d")`, `time.Format` or `humanize` calls in handlers or templates.
10. **CSS:** one hand-written stylesheet, `app.css`, whose custom properties are the design tokens. Dark mode swaps tokens under `[data-theme=dark]`, and `[data-theme=system]` follows `prefers-color-scheme`. No Tailwind and no CSS build step.
11. **CSP-clean markup.** No inline `<script>`, `<style>`, `style=""` or `on*=` attributes. Use classes, SVG attributes and `app.js` instead. No form may lead off-origin ([design §7](design.md#7-http)), so SSO sign-in and proxy sign-out are links.
12. **Minimal dependencies.** Prefer the standard library. No third-party router, DI framework, mock framework or assertion library. The frontend is vendored and embedded: no npm, no build step, no chart library.

## Wiring and lifecycle

- `main` calls `run(ctx, args, lookupEnv, stdout, stderr)`, which builds every dependency and passes it down explicitly. Everything that can fail runs before the listener opens.
- Use `lookupEnv`, not `getenv`, so interpolation can tell an unset variable from an empty one.
- `BaseContext` is `context.WithoutCancel(ctx)`: in-flight requests keep their context until `Shutdown` drains them.
- `web.Readiness` has one transition, `Drain()`. Its zero value is ready.
- Flags count as set only when `flag.FlagSet.Visit` reports them, so a flag given with its default value still wins over the environment.
- Shutdown is `http.Server.Shutdown` under the 10s timeout, then `Close`.

[configuration.md](configuration.md#startup) covers startup and precedence, and [deployment.md](deployment.md#graceful-shutdown) covers shutdown.

## Authentication

- **Cookies:** the session and OIDC flow cookies are signed and encrypted with `gorilla/securecookie`. `previous_keys` become extra codecs (`securecookie.CodecsFromPairs`) that only decode.
- **OIDC:** go-oidc's `Verifier` checks the ID token's issuer, audience, expiry and signature; the nonce and the other claim checks in [design §3](design.md#oidc) run after it.
- **Client address:** proxy trust and the login rate limit use `r.RemoteAddr`, never `X-Forwarded-For`.
- **Return-to:** `next` is cleaned with `path.Clean` before the prefix check.

## Routing and middleware

- **Base path:** `NewServer` mounts the app mux under `<base>/` with `http.StripPrefix`; everything outside it is 404.
- **Links:** one helper, `d.url(segments ...string)`, builds every link, redirect and cookie path. It prefixes `base_path` and escapes each segment with `url.PathEscape`. Templates never concatenate paths; query strings use `url.Values`.
- **Auth middleware** only identifies the user and never rejects a request. A proxy request with untrusted identity headers is marked as rejected.
- **Wrappers:** `view`, `op` and `admin` read that identity and resolve `{conn}`. A request with no identity gets `authn.Challenge`, a hidden or unknown connection gets 404, and an insufficient role gets 403.
- **Global middleware**, outermost first: request logging, panic recovery, security headers, `http.CrossOriginProtection`, authentication, app mux.

## Data access and Redis load

- **`conn.Registry`** holds a `*asynq.Inspector` and a go-redis client per connection, created at startup without Redis I/O. asynqui uses no `asynq.Client`.
- **Timeouts:** Inspector calls take no context, so only the connection's socket timeouts bound them. Our own commands (PING, INFO) also run under a context deadline.
- **Snapshots:** every read goes through the connection's 1s cache, keyed by snapshot, with concurrent fetches shared. Every action drops the connection's cached snapshots.
- **Scheduler history:** the task-existence lookups behind its task links are cached for 10s per task.
- **Page budget:** a page waits a bounded time for its reads. If they don't finish in time, the page renders "Checking" and the fetch carries on in the cache, so the next poll has the data.
- **Down vs error:** only a network error or timeout (`conn.Unreachable`) marks a connection down. An error reply is shown inline.
- **Health:** one goroutine per connection stores the PING result. Pages only read it, and `handleRecheck` runs a check synchronously. The health loop also detects the backend (Redis or Valkey) from `INFO server`.
- **Direct Redis reads:** the Inspector cannot list a state in reverse, so the task tables' time sort reads asynq's sorted sets directly. This happens only in `internal/conn/keys.go`, which documents each key and its score. The key format is copied from asynq's `internal/base`, never imported. Only plain range reads are allowed: one ZRANGE/ZREVRANGE per page, then each ID is hydrated with the public `GetTaskInfo`. No writes, scripts, scans or client-side sorting. `TestIntegrationKeys` checks the layout against the asynq version in `go.mod`. When you bump asynq, re-check both.

## Frontend conventions

- **Views:** `views/layout.templ` is the page shell. `views/components/` holds the shared pieces. `views/pages/` has one file per page, each taking one view-model struct.
- **htmx config:** `app.js` sets `allowEval = false` and `includeIndicatorStyles = false` before any request. The indicator styles would inject an inline `<style>`. No `hx-on` or `js:` attributes.
- **Polling timers:** trigger filters such as `every 5s [document.visibilityState === 'visible']` need eval, so `app.js` runs the timers itself. It skips ticks while the tab is hidden and fires one refresh when it becomes visible. With "Off" no timer runs.
- **Polling (`pages.Live`):** every connection page has a `*-page` wrapper that is the `hx-sync` scope of its requests, and a hidden `*-poll` element that polls with `hx-swap="none"` and `hx-sync="#…-page:abort"`, so a poll yields to any user request. The server recognises a poll by its `HX-Trigger` header. The poll's answer replaces, out of band and by id, the poll element itself, the data regions, the state banner, the Updated time and the sidebar list. User requests (sort, tab, page, range) use `this:queue last`, and actions use `queue all`. The Overview grid works the same way.
- **Polls never replace what the user is using:** controls, filters, toolbars, dialogs, the payload card and the raw INFO block sit outside the swapped regions or are rendered once. A poll that finds no data keeps the regions and shows the banner. A page rendered without its regions polls with `?poll=init` and gets the whole body.
- **Stable ids:** htmx refocuses the element with the same id after a swap, so every focusable element in a polled region has a stable id, with one prefix per element kind so names cannot collide.
- **Selection:** row checkboxes carry `hx-preserve` and a stable id, so a swapped-in table keeps them checked. They belong to an empty action form through their `form` attribute. `app.js` recomputes the toolbar after each swap and restores focus. An action's answer renders the checkboxes without `hx-preserve`, so the selection clears.
- **Actions:** `<form method="post">`, enhanced with `hx-post` and `hx-swap="none"`. Error toasts are appended to `#toasts` out of band. Without JS the answer is always a redirect back, with failures in the `asynqui_flash` cookie. A confirm button posts `confirm=1`; without JS the first post answers with a confirmation page.
- **Time:** render timestamps with the `time` component; `app.js` localises them.
- **Assets:** served under `/static/<hash>/…` with `Cache-Control: immutable`, the hash covering every embedded file. `//go:embed files` (without `all:`), so dotfiles are never embedded.

## Testing

1. **Component tests:** render templ components with fixture view models and assert on the HTML with goquery.
2. **Integration tests** (`internal/conn`, testcontainers): reads and actions against real Redis, Valkey and asynq. They skip without Docker unless `ASYNQUI_REQUIRE_DOCKER=1`, which makes them fail instead; CI and the release workflow set it.
3. **Handler tests:** `httptest` through the full stack: full page vs fragment, role enforcement, actions, "not found" and "Connection failed" states. The checks every page shares (access by role, CSRF, ids of focusables in polled regions, answer shape, `hx-sync` scopes, path escaping) run over the route table in `internal/web/routes_test.go`; add new routes there.
4. **Browser tests** (`e2e/`, see [contributing](../CONTRIBUTING.md#browser-tests)): the `app.js` and htmx behaviour Go tests cannot run, plus an axe-core and CSP check of every page.

Other conventions:
- Use small hand-written fakes, and a fake OIDC provider built on `httptest.Server`.
- Inject `now func() time.Time` where output depends on the clock.
- Tests are table-driven. Subtests never share mutable state, so any of them can run alone or in parallel.
- `run()` has an end-to-end test on `127.0.0.1:0`, including the drain.

## Tooling

- Dev tools and their versions are listed in `tools/go.mod` as `tool` directives. Run them with `go tool -modfile=tools/go.mod <tool>`. The main `go.mod` lists only runtime and test dependencies.
- `task check` runs the same checks as the CI check job. When you change `Taskfile.yml` or `.github/workflows/ci.yml`, update the other to match.
