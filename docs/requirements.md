# asynqui requirements (v1)

asynqui is an open-source web dashboard for [asynq](https://github.com/hibiken/asynq) that covers **several Redis instances and logical databases** in a single deployment. v1 targets feature parity with asynqmon. The visual reference is the design canvas: <https://claude.ai/artifact/HbfWPswwsqyV498Lrfpfnu>.

This document is the source of truth for behaviour. When this file and the canvas disagree, this file wins; update the canvas afterwards.

## 1. Scope

**In scope:** everything asynqmon does (queues, tasks, servers, schedulers, Redis info, daily stats), plus:
- multiple connections
- an overview across connections
- authentication
- per-connection RBAC
- Valkey detection
- dark mode

**Non-goals for v1:**
- Adding or editing connections from the UI.
- Redis Sentinel and Cluster.
- Any persistent storage of our own (database, files).
- Audit log.
- Metrics or Prometheus integration.
- Alerting.
- Per-queue RBAC. The config shape leaves room for it later.
- Editing scheduler entries. asynq has no API for it.
- Connection env tags or colours.

## 2. Connections

- **Config only:** connections are defined in YAML with `${ENV}` interpolation and are read-only in the UI.
- **What a connection is:** a Redis address plus a logical DB, plus a **name**.
- **Name:** required and unique. Duplicates make the binary refuse to start, with an error that names both entries. Names must be URL-safe: `^[a-z0-9][a-z0-9._-]{0,62}$`. The name is used in URLs and RBAC.
- **Standalone Redis only.** Supported options:
  - TLS: on/off, CA file, client cert/key, `insecure_skip_verify`, server name
  - ACL username and password
  - dial/read/write timeouts
- **Backend detection:** auto-detected from `INFO server`. If `server_name:valkey` is present, label it "Valkey info" and show `valkey_version` with "Redis-compatible `redis_version`" underneath. Otherwise label it "Redis info" and show `redis_version`.
- **Where the DB number appears:** only in the sidebar's second line, `host:port/db`. Breadcrumbs, headings, titles and Overview cards show the name only.
- **Health:** a connection is *reachable* if a PING succeeds within the timeout. Otherwise it is *unreachable*, and the error text is kept for display.

## 3. Authentication

`auth.provider` is one of:

| Provider | Behaviour |
|---|---|
| `none` | No login. Everyone gets the `authz.default` role, which defaults to `admin` when the provider is `none`. The account menu is hidden. |
| `basic` | Login form (username + password). Users are listed in config with **bcrypt** password hashes. |
| `proxy` | Trust identity headers from a reverse proxy: a user header, plus an optional groups header (comma-separated). Only accept them from configured trusted proxy CIDRs. |
| `oidc` | Authorization-code flow with PKCE, callback `GET /auth/callback`. Configured with issuer, client ID and secret, scopes, and the claim names for username (default `email`) and groups (default `groups`). |

- **Sessions:** a signed and encrypted cookie (`gorilla/securecookie`). Keys come from config/env, are required for every provider except `none`, and are never auto-generated. Cookies are `HttpOnly`, `SameSite=Lax`, and `Secure` when served over HTTPS. Session TTL is configurable, default 12h.
- **Logout:** `POST /logout`. It clears the session and, for OIDC, does a local logout only.
- **Login page:**
  - The username/password form appears only for `basic`.
  - The "Sign in with SSO" button appears only for `oidc`.
  - Invalid credentials show "Invalid username or password."
  - The version is shown under the card.

## 4. Authorization

Roles are ordered **viewer < operator < admin** and scoped per connection.

```yaml
authz:
  default: viewer          # role for any authenticated user with no binding; "none" = no access
  bindings:
    - groups: [platform]
      role: admin
      connections: ["*"]
    - users: [jdoe@example.com]
      role: operator
      connections: ["billing", "main*"]
```

- **Matching:** a binding matches if the user is in `users`, or shares a group with `groups`. `connections` takes globs (`path.Match` syntax).
- **Effective role:** for each connection, the highest role among the matching bindings and `default`.
- **Startup validation:** the binary fails to start on an unknown role, an empty `connections` list, an invalid glob, or a binding with neither users nor groups.
- **Hidden connections:** a connection where the user's effective role is none is invisible to them everywhere (sidebar, Overview, counts). Its URLs return 404, not 403.
- **Server enforcement:** every action is checked on the server. Disabled buttons are only a UI hint.

| Capability | viewer | operator | admin |
|---|---|---|---|
| View every page | ✓ | ✓ | ✓ |
| Pause / resume queue | | ✓ | ✓ |
| Run / archive / cancel tasks (single, bulk, "all") | | ✓ | ✓ |
| Scheduler entry "Run now" | | ✓ | ✓ |
| **Delete tasks** (single, bulk, "all") | | | ✓ |
| **Delete queue** | | | ✓ |

- **Insufficient role:** the button renders **disabled**, with a native `title` tooltip of "Requires admin" ("Requires admin on {conn}" on the Task page). There is no lock icon and no explanatory text.
- **Viewers:** buttons the user can never use are still shown, disabled. This keeps the layout stable.

## 5. Global UI

### Layout
- A sidebar on the left and the main column on the right.
- The main column has a top bar (breadcrumbs on the left, controls on the right), with the content below it.

### Sidebar (identical on every page)
- **Pinned top:** the brand (logo + "asynq" + accent "ui").
- **Global nav:** Overview.
- **Connections header:** "Connections" with the visible count.
- **Filter:** a "Filter connections" input that filters client-side by name or address.
- **Connection list:** scrolls. Each item shows:
  - a status dot (green reachable, red unreachable) after the name
  - the name
  - a second line `host:port/db` in muted mono, ellipsised
- **Current connection:** expanded inside a tinted container with its sub-nav: Queues, Servers, Schedulers, and "Redis info" or "Valkey info".
- **Active item:** tinted background, semibold, no accent bar. Hover and active backgrounds are inset on both sides.
- **Pinned footer:**
  - `v{version}` on the left, followed by an accent dot when a newer GitHub release exists. Both link to the release notes.
  - A muted "Source ↗" link on the right.
  - There is no GitHub logo, Docs link or Report-issue link.
  - The update check is cached for 24h, fails silently, and can be disabled with `update_check: false`.

### Top bar (every page, every control 36px tall)
- **Breadcrumbs:** `Overview / {conn} / {section} / {item}`. Every segment except the last is a link.
- **"Updated Ns ago":** muted text.
- **Auto refresh:** a segmented control with Off / 2s / 5s / 10s. The default comes from config (`refresh.default`, 5s). The choice is stored in a cookie.
- **Theme:** a segmented control with three icon buttons (System, the default; Light; Dark). The choice is stored in a cookie, and the server renders the `data-theme` attribute so there is no flash on load.
- **Account menu:** `{user}` followed by the muted role for the current connection (the highest role on the Overview), then ▾. The menu holds Sign out. Hidden when auth is `none`.

### Polling
- htmx re-fetches only the data regions of the page at the chosen interval.
- Polling pauses while the tab is hidden and resumes immediately when the tab is visible again.
- It never resets the user's selection, sort, page or open dialog.

### Copy and visual rules
- **Minimal copy:** no helper or explanatory text. Empty states are one short line.
- **No "·" separators anywhere.** Separate items with spacing, weight and muted colour.
- **Links:** always look like links (accent `#3B5BDB` light, `#7C93F5` dark). This includes whole-row links.
- **Monospace** (JetBrains Mono) only for IDs, cron specs, payloads, options, errors, addresses and INFO output. Everything else uses Inter.
- **Shortened IDs:** the first 8-4 hex groups plus "…" (e.g. `c3e81b90-04aa…`) everywhere an ID is shortened, with the full ID in `title`.
- **Times:** relative ("3m ago", "in 2m 40s") with the absolute time in `title`, or absolute as "Nov 8, 00:11:52" where the design shows it.
- **Daily counters:** "today" means asynq's UTC calendar day. The label is plain "today".
- **Code blocks:** dark block with a copy button pinned top-right, outside the scrolling area (`scrollbar-gutter: stable`).
- **Fonts:** self-hosted and embedded. No requests to third-party domains.
- **Tokens:** colours, spacing and type follow the canvas "Style guide" board. Dark mode swaps CSS custom properties.

### Tables
1. **Select, then act.** A checkbox column plus a toolbar showing "N selected" and the bulk action buttons. There are no per-row action buttons or kebab menus. Read-only tables have no checkboxes.
2. **Row click** opens the detail page. Row links are styled as links.
3. **Gmail-style select all** (task tables): the header checkbox selects the current page and shows a banner, "All N on this page are selected. Select all {total} {state} tasks". Once clicked, the banner reads "All {total} {state} tasks are selected. Clear selection", and actions then apply to the whole state through the `*All*` APIs.
4. **Sorting:**
   - The queues table sorts by any column.
   - Task tables sort only by their time column.
   - Servers: every column except Queues is sortable.
   - Schedulers: every column except Payload and Options is sortable.
   - Sort state lives in the URL query.
5. **Totals:** the Total row sits in `<tfoot>`.
6. **Pagination** (task tables): rows per page 20/50/100, "1–20 of 340", ‹ ›. Page and size live in the URL query.
7. **Destructive or "all" actions** open a confirmation dialog.

## 6. Pages

URLs follow docs/architecture.md. Every page works without JavaScript for reading. htmx enhances it with polling, partial swaps and selection.

### 6.1 Overview `/`
- **Heading:** "All connections". A red "N unreachable" badge appears when N > 0.
- **Cards:** a grid with one card per visible connection, in config order.
- **Reachable card:**
  - name (title)
  - the user's role on that connection (chip)
  - address `host:port/db` (mono, muted)
  - Queues, Pending, Active and Retry
  - Processed today, Failed today (red) and **Oldest pending**: the maximum `QueueInfo.Latency` across queues, as a duration, with no queue name
  - footer: a "Healthy" badge and an "Open →" link
- **Unreachable card:**
  - name, role chip and address
  - "Connection failed", with the error in a mono block
  - footer: an "Unreachable" badge and a "Retry now" button that re-checks health immediately
- **Layout:** all card footers line up.

### 6.2 Connection → Queues `/c/{conn}`
- **Header:** "Queues", the address (mono, muted) and a Healthy or Unreachable badge.
- **Toolbar:** a "Filter queues" input (client-side, by name), a "N selected" label, and Pause, Resume and Delete (admin).
  - Pause is enabled only if any selected queue is running.
  - Resume is enabled only if any selected queue is paused.
- **Table:** checkbox, then Queue (link), State (Running/Paused badge), Size, Pending, Active, Scheduled, Retry, Archived, Latency, Memory, Processed today, Failed today. Every column sorts, default Size desc. The `<tfoot>` Total row sums everything except Latency, which shows "—".
- **Throughput card:**
  - a range selector: 7d / 14d / 30d / 90d
  - a Queue picker: "All queues" or one queue
  - one overlaid line chart: Processed on the left axis, Failed on the right axis, each axis colour-matched to its line
  - a hover tooltip showing date, Processed, Failed and Failure rate
- **Chart rendering:** server-rendered SVG, with the data from `Inspector.History`.
- **Unreachable connection:** the page shows "Connection failed" with the error in place of the table.

### 6.3 Queue `/c/{conn}/queues/{queue}?state=…`
- **Header:**
  - the queue name and a Running or Paused badge
  - a "Pause queue" or "Resume queue" button (operator)
  - stat cards: Size, Latency, Memory, Processed today, Failed today
- **Tabs, each with a count:** Active, Pending (default), Aggregating, Scheduled, Retry, Archived, Completed.
- **Search:** "Search by ID or type".
  - An exact task ID jumps to that task, via `GetTaskInfo`.
  - Any other input filters the current page by type (client-side).
- **Table:** checkbox, Task ID (shortened, mono, links to the task), Type, Payload (mono, truncated to one line), then two columns that depend on the tab. Only the time column sorts.

| Tab | Column A (sortable) | Column B | Bulk actions | "All" actions |
|---|---|---|---|---|
| Active | Started | Running for | Cancel | Cancel all |
| Pending | Enqueued | Retried | Archive, Delete | Archive all, Delete all |
| Scheduled | Process at | In | Run now, Archive, Delete | Run all, Archive all, Delete all |
| Retry | Next retry | Last error | Run now, Archive, Delete | Run all, Archive all, Delete all |
| Archived | Archived at | Last error | Run now, Delete | Run all, Delete all |
| Completed | Completed at | Result kept until | Delete | Delete all |
| Aggregating | — | — | — | — |

- **Cancel all:** asynq has no bulk cancel. "Cancel all" lists the active tasks and calls `CancelProcessing` for each, reporting the count.
- **Aggregating tab:** lists groups (`Groups(queue)`) with their sizes. Empty state: "No aggregating tasks".
- **After an action:** an inline result notice ("Archived 12 tasks"). The table refreshes and the selection clears.

### 6.4 Task `/c/{conn}/queues/{queue}/tasks/{id}`
- **Breadcrumb:** `Overview / {conn} / {queue} / {state} / {shortId}`.
- **Header:**
  - the task type (h1) and a state badge
  - the full ID (mono) with a "Copy ID" button
  - actions valid for the state: Run now, Archive, Cancel (active), Delete (admin)
- **Payload card:**
  - a JSON / Raw toggle (JSON pretty-printed when valid, otherwise Raw only)
  - a copy button
- **Last error card:** only when present. Shows the time and "attempt N", with the error in mono.
- **Result card:** only when a result was written.
- **Details card:**
  - Queue, State, Retried `n / max`, Last failed
  - Next process at, as absolute plus relative
  - Timeout, Deadline, Unique key, Group, Enqueued at
  - Completed at and Result kept until, when relevant
  - Missing values show "—".
- **Unknown or deleted ID:** shows "Task not found".

### 6.5 Servers `/c/{conn}/servers`
- **Header:** "Servers" and a "Filter servers" input.
- **Read-only table** (no checkboxes):
  - Host (a row link, the hostname; full server ID in the title)
  - PID
  - State: "Active" or "Stopped", from `ServerInfo.Status`
  - Busy: `n / concurrency` with a small bar
  - Queues: `name weight` tokens, not sortable
  - Started: relative
- **Default sort:** Busy desc.
- **Empty state:** "No servers running".

### 6.6 Server detail `/c/{conn}/servers/{id}`
- **Header:** the host (h1), a state badge, the server ID (mono) with Copy ID.
- **Active workers ({n}) table:** Task ID (links to the task), Type, Queue, Started, Running for. Sortable by Started and Running for, default Running for desc. Running for ≥10m is shown amber and semibold.
- **Details:** PID, Started, Concurrency, Busy, Queues (with weights), Strict priority (Yes/No).
- **Unknown ID:** shows "Server not found".

### 6.7 Schedulers `/c/{conn}/schedulers`
- **Header:** "Schedulers", a "Filter entries" input, "N selected", and "Run now" (operator).
- **Table:**
  - checkbox
  - Entry ID (shortened, a row link)
  - Spec: cron spec in mono plus a muted human description, e.g. "every day at 06:00"
  - Task type
  - Payload (mono, truncated, not sortable)
  - Options (space-separated tokens, not sortable)
  - Next run, Last run: relative, with absolute in the title
- **Default sort:** Next run asc.
- **Entry identity:** entries are identified by **entry ID**, which the scheduler regenerates on restart. Duplicate task types are allowed and shown as separate rows.
- **Run now:**
  - A confirmation dialog first.
  - It enqueues a **one-off copy** of the entry's task (type, payload and options) via `asynq.Client`. It never touches the scheduler.
  - Result notice: "Enqueued task {shortId} in {queue}" with "View task →". For several entries: "Enqueued N tasks".
  - These tasks do not appear in Enqueue history, because only the scheduler process writes it.

### 6.8 Scheduler entry `/c/{conn}/schedulers/{id}`
- **Breadcrumb:** ends with the short entry ID.
- **Header:**
  - the task type (h1)
  - the full entry ID (mono) with Copy ID
  - "Run now" (operator, same behaviour as 6.7)
- **Payload card:** JSON / Raw toggle and copy button.
- **Enqueue history table:**
  - Enqueued at (sortable, default desc)
  - Task ID (links to the task while it still exists, otherwise plain text)
  - Status ("Enqueued" or "Error")
  - Error
  - Paginated through `ListSchedulerEnqueueEvents`.
- **Details:** Spec with its description, Queue, Options, Next run, Last run, Scheduler (host + pid).
- **Stale or unknown ID:** shows "Entry not found" with a link back to Schedulers.

### 6.9 Redis / Valkey info `/c/{conn}/info`
- **Title:** "Redis info" or "Valkey info", per §2, with the address underneath.
- **Info cards:**
  - Version
  - Mode
  - Uptime
  - Connected clients
  - Used memory / peak (with maxmemory underneath)
  - maxmemory-policy
  - Keyspace db{n}: keys, plus "with expiry" underneath
  - TLS: On/Off, plus version and verification underneath
  - ACL user
- **Instance-wide values:** memory, clients and uptime belong to the whole instance. They appear **only** on this page, never on the Overview or Queues pages, because several connections can share one instance.
- **Raw INFO card:** the full `INFO` output in a code block with a copy button.

## 7. Operational

- **Binary:** a single static binary.
  - Flags: `-config path` (default `asynqui.yaml`) and `-addr` (default `:8080`). Env vars override flags.
  - Graceful shutdown on SIGINT/SIGTERM, with a 10s timeout.
- **Probes:** both need no auth, return plain text (`ok` or the reason) and are excluded from request logs (everything under `/health/`).
  - `GET /health/liveness` returns 200 whenever the process is running. It has no dependency checks.
  - `GET /health/readiness` returns 200 once startup has finished: config loaded and the auth provider initialised (e.g. OIDC discovery succeeded). It returns 503 as soon as graceful shutdown begins, so load balancers drain traffic. It **never** depends on Redis connection health.
- **Base path:** `base_path` config, so the app can be served under a sub-path behind a proxy.
- **Logging:** structured JSON via `log/slog`. Level is configurable. Secrets are never logged.
- **Redis load:**
  - One shared fetch per connection per refresh interval, whatever the number of viewers (cache + singleflight).
  - Connections are fetched in parallel, each with its own timeout.
  - One slow connection never blocks a page.
- **Releases:** GoReleaser builds linux/darwin/windows for amd64 and arm64, plus a multi-arch Docker image (distroless).
