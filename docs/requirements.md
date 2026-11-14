# asynqui requirements (v1)

asynqui is an open-source web dashboard for [asynq](https://github.com/hibiken/asynq) that covers **several Redis instances and logical databases** in a single deployment. v1 targets feature parity with asynqmon. The visual reference is the design canvas: <https://claude.ai/artifact/HbfWPswwsqyV498Lrfpfnu>.

This document is the source of truth for behaviour. When this file and the canvas disagree, this file wins; update the canvas afterwards. Every config key and default is listed in §8.

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
- Localisation. The UI is English only, with no i18n framework.
- Server-side session revocation (§3).

## 2. Connections

- **Config only:** connections are defined in YAML (§8) and are read-only in the UI.
- **What a connection is:** a Redis address plus a logical DB, plus a **name**.
- **Name:** required and unique. Duplicates make the binary refuse to start, with an error that names both entries. Names must match `^[a-z0-9][a-z0-9._-]{0,62}$`. The name is used in URLs and RBAC.
- **Standalone Redis only.** Supported options:
  - TLS: on/off, CA file, client cert/key, `insecure_skip_verify`, server name
  - ACL username and password
  - `dial_timeout` (default 5s), `read_timeout` (3s), `write_timeout` (3s), per connection. There is no global Redis timeout.
- **Backend detection:** auto-detected from `INFO server`. If `server_name:valkey` is present, label it "Valkey info" and show `valkey_version` with "Redis-compatible `redis_version`" underneath. Otherwise label it "Redis info" and show `redis_version`.
- **Where the DB number appears:** only in the sidebar's second line, `host:port/db`. Breadcrumbs, headings, titles and Overview cards show the name only.
- **Health:**
  - A background loop sends PING to every connection every 10s, bounded by that connection's timeouts. The first round starts at startup without blocking it.
  - *Reachable* means the last PING succeeded. *Unreachable* means it failed; the error text is kept for display.
  - Until the first PING finishes, the connection is *unknown*: muted dot, no badge.
  - The Overview, sidebar and page badges read the cached result and never PING themselves.
  - "Retry now" (§6.1) runs a check immediately and updates the cache.
- **Load:** see §7 "Redis load".

## 3. Authentication

`auth.provider` is one of `none` (default), `basic`, `proxy` or `oidc`. Startup fails on any provider error, including OIDC discovery (§7 "Startup").

### Common rules
- **Middleware:** the authentication middleware only identifies the user. It never rejects a request. The route wrappers (`view`, `op`, `admin`) decide:
  - no identity, `basic`/`oidc`: 303 to `<base>/login?next=<path>`; for htmx requests, 401 with `HX-Redirect: <base>/login?next=<current page>` so the whole page navigates instead of swapping in a fragment.
  - no identity, or rejected identity headers, `proxy`: 401.
  - Probes and `/static/` need no identity.
- **Return-to (`next`):** accepted only if it is a same-origin relative path under `base_path` (starts with `<base>/`, not `//`, no scheme or host), at most 1 KB, with no backslash, control character or encoded `.`, `/` or `\` (`%2e`, `%2f`, `%5c`). The path is cleaned (`path.Clean`, keeping a trailing slash) before the prefix check, and the cleaned, re-encoded URL is what is used; fragments are dropped. Anything else falls back to `<base>/`. For htmx requests it comes from `HX-Current-URL` (path and query only), or is `<base>/` without one. Challenges send `Cache-Control: no-store`.
- **Logout:** `basic` and `oidc`: Sign out posts to `POST /logout`, which clears the session cookie and redirects to the login page (OIDC: local logout only). `proxy`: Sign out is a plain link straight to `auth.proxy.logout_url`, with no server round trip. (A form whose response redirects to another origin would be blocked by the CSP `form-action 'self'`, §7.)
- **Sign out visibility:** shown for `basic` and `oidc`; for `proxy` only when `logout_url` is set; never for `none`.

### `none`
- No login and no session. Every request is anonymous, so bindings never match and everyone gets `authz.default`.
- `authz.default` is **viewer** unless set explicitly. For full control set `authz.default: admin`.
- The account menu is hidden.
- Startup logs a WARN when the listen address is not loopback (empty host, `0.0.0.0`, `::` or any non-loopback IP or hostname other than `localhost`).

### `basic`
- Login form (username + password). Users are listed in config with **bcrypt** hashes and optional `groups`. All hashes must use the same bcrypt cost (startup fails otherwise; rehash at one cost), so a failed login takes the same time whichever user was tried. Usernames that differ only in ASCII case are rejected at startup.
- Unknown usernames are compared against a dummy hash, so a miss costs the same as a hit.
- **Rate limit:** `POST /login` attempts per client IP (`r.RemoteAddr`, IPv6 per /64) and per username: a burst of 5, then 1 every 12s. Further attempts get 429 and "Too many attempts. Try again later." Behind a proxy every user shares the proxy's IP, so one user's failures can lock out the others. Failed logins are logged with the username only when it exists.
- bcrypt reads at most 72 bytes. Passwords longer than 72 bytes are rejected at login.
- Every request re-checks the session's user against the current config: a user removed from config is signed out, and groups always come from config.
- Invalid credentials show "Invalid username or password."

### `proxy`
- Identity comes from request headers set by a reverse proxy: `user_header` (default `X-Forwarded-User`) and optional `groups_header` (default unset).
- **Trust** is decided from `r.RemoteAddr` only, against `trusted_proxies` (CIDRs or IPs, required: an empty list fails startup; a prefix covering every address, such as `0.0.0.0/0`, logs a WARN). `X-Forwarded-For` is never consulted.
- A request from an untrusted IP that carries either identity header gets 401.
- A trusted request with a missing or empty user header gets 401. So does one where the user or groups header appears more than once (a proxy that appends instead of replacing); this is logged as a WARN.
- Groups: split on `,`, trim spaces, drop empty entries.
- Identity is read on every request. There is no session cookie, so `auth.session` is not needed.
- There is no login page.
- Header stripping and network isolation are the deployer's job: see [deployment.md](deployment.md).

### `oidc`
- Authorization-code flow with **PKCE (S256)**. "Sign in with SSO" is a link to `GET /login/sso?next=…`, which sets the flow cookie and redirects to the IdP. The IdP returns to `GET /auth/callback`. It is a navigation, not a form post, because the CSP `form-action 'self'` (§7) also applies to a form's redirects; the state, nonce and PKCE verifier in the flow cookie are what protect the flow. `POST /login` is the basic form only and is 404 for `oidc`.
- **Flow cookie:** state, nonce, PKCE verifier and `next` are kept in an encrypted cookie `asynqui_oidc`: 10 minute lifetime, `Path=<base>/auth/callback`, `HttpOnly`, `SameSite=Lax`, `Secure` per `cookie_secure`. It is deleted by a callback whose `state` matches; a forged callback leaves it, so it cannot cancel a login in progress.
- **ID token:** verified with go-oidc's Verifier (issuer, audience = `client_id`, expiry, RS256 and other asymmetric algorithms only) and the nonce. `sub` must be non-empty; `azp` must equal `client_id` when present or when `aud` has several entries; `iat` may be at most 5 minutes in the future.
- **Username:** the `username_claim` (default `email`). When it is `email`, `email_verified` must be `true` (the boolean, or the string `"true"` as Amazon Cognito sends it), otherwise login fails. Userinfo claims are used only when the userinfo `sub` is present and equals the ID token's.
- **Groups:** read from `groups_claim` (default `groups`) in the ID token, falling back to the userinfo endpoint. The claim may be a string or an array of strings. On a groups overage (`_claim_names` / `_claim_sources`), log a WARN and treat groups as empty.
- **Scopes:** default `openid profile email`. `groups` is not a default; add it if your IdP needs it. `openid` is always sent.
- `issuer`, `client_id` and `redirect_url` are required. `client_secret` is optional (public clients).
- **Session size:** the session keeps only groups referenced by some `authz.bindings` entry. If the encoded cookie would exceed 3800 bytes, login is refused with "Sign-in failed: too many groups for a session cookie."
- Login failures show "Single sign-on failed." on the login page; details go to the log.

### Session cookie (`basic`, `oidc`)
- Name `asynqui_session`, `Path=<base_path>` (`/` when `base_path` is empty), `HttpOnly`, `SameSite=Lax`, `Secure` unless `auth.session.cookie_secure: false` (default `true`; set `false` only for local plain HTTP).
- Signed and encrypted with `gorilla/securecookie`. Keys are hex in config: `hash_key` decodes to 32 or 64 bytes, `block_key` to 32 bytes. Both are validated at startup and never auto-generated.
- **Rotation:** `previous_keys` (list of `{hash_key, block_key}`) still decode existing cookies; new cookies always use the current pair (`securecookie.CodecsFromPairs`).
- **TTL:** absolute from sign-in, default 12h. There is no sliding renewal.
- **Payload:** identity claims only, never tokens, plus the provider name: a cookie minted by another provider (e.g. a `basic` deployment sharing the keys) is rejected. Basic sessions store no groups. Cookie values over 4096 bytes are rejected before decoding; when several `asynqui_session` cookies arrive, the first valid one is used.
- **No server-side revocation.** Signing out clears the cookie in that browser only. To invalidate every session, rotate the keys without keeping the old pair in `previous_keys`. For `basic`, removing a user from config also signs them out.

### Login page
- The username/password form appears only for `basic`.
- The "Sign in with SSO" link (styled as a button) appears only for `oidc`.
- A user who is already signed in is redirected from the login page to `next`, or to `<base>/`.
- The version is shown under the card.

## 4. Authorization

Roles are ordered **viewer < operator < admin** and scoped per connection. `none` as a role means no access.

```yaml
authz:
  default: none            # role for every user on every connection; see defaults below
  bindings:
    - groups: [platform]
      role: admin
      connections: ["*"]
    - users: [jdoe@example.com]
      role: operator
      connections: ["billing", "main*"]
```

- **Default:** `authz.default` applies to every identified user on every connection.
  - `auth.provider: none`: defaults to **viewer**.
  - `basic`, `proxy`, `oidc`: defaults to **none**, so access comes only from bindings.
- **Matching:** a binding matches if the username is in `users` (case-insensitive for ASCII letters only, so look-alikes such as the Kelvin sign do not match `k`) or the user has a group in `groups` (exact, case-sensitive). `connections` takes globs (`path.Match` syntax).
- **Effective role:** for each connection, the highest role among the matching bindings and `default`.
- **Startup validation:** the binary fails to start on an unknown role, an empty `connections` list, an invalid glob, or a binding with neither users nor groups. A binding's `role` cannot be `none`.
- **Hidden connections:** a connection where the user's effective role is none is invisible to them everywhere (sidebar, Overview, counts). Its URLs return 404, not 403.
- **Server enforcement:** every action is checked on the server. An insufficient role returns 403. Disabled buttons are only a UI hint.

| Capability | viewer | operator | admin |
|---|---|---|---|
| View every page | ✓ | ✓ | ✓ |
| Pause / resume queue | | ✓ | ✓ |
| Run / archive tasks (single, bulk, "all", all of an aggregation group) | | ✓ | ✓ |
| Cancel active tasks (single, bulk) | | ✓ | ✓ |
| **Delete tasks** (single, bulk, "all", all of an aggregation group) | | | ✓ |
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
  - `v{version}` on the left, followed by an accent dot when a newer GitHub release exists. Both link to the release notes. A development build whose version names a commit (Go pseudo-version, `git describe`, GoReleaser snapshot) shows `dev {shortsha}` (at most 7 hex digits) with the full version in the title; other prereleases and `dev` are shown as is.
  - A muted "Source ↗" link on the right.
  - There is no GitHub logo, Docs link or Report-issue link.
  - The update check (§7 "Update check") is cached for 24h, fails silently, and can be disabled with `update_check: false`. With an update, the version and dot link to the newer release's notes; otherwise to the running release's notes.

### Top bar (every page, every control 36px tall)
- **Breadcrumbs:** `Overview / {conn} / {section} / {item}`. Every segment except the last is a link.
- **"Updated Ns ago":** muted text.
- **Auto refresh:** a segmented control with Off / 2s / 5s / 10s. The default comes from config (`refresh.default`, 5s). The choice is stored in a cookie. "Off" means no polling requests at all.
- **Theme:** a segmented control with three icon buttons (System, the default; Light; Dark). The choice is stored in a cookie, and the server renders the `data-theme` attribute so there is no flash on load.
- **Account menu:** `{user}` followed by the muted role for the current connection (the highest role on the Overview), then ▾. The menu holds Sign out. Hidden when auth is `none`.

### Polling
- htmx re-fetches only the data regions of the page at the chosen interval.
- Polling pauses while the tab is hidden and resumes immediately when the tab is visible again.
- It never resets the user's selection, sort, page or open dialog.
- Data is served from the per-connection 1s cache (§7 "Redis load").

### Copy and visual rules
- **Minimal copy:** no helper or explanatory text. Empty states are one short line.
- **No "·" separators anywhere.** Separate items with spacing, weight and muted colour.
- **Links:** always look like links (accent `#3B5BDB` light, `#7C93F5` dark). This includes whole-row links.
- **Monospace** (JetBrains Mono) only for IDs, cron specs, payloads, options, errors, addresses and INFO output. Everything else uses Inter.
- **Shortened IDs:** a UUID shows its first 8-4 hex groups plus "…" (e.g. `c3e81b90-04aa…`). Any other ID longer than 12 characters shows its first 12 characters plus "…"; shorter IDs are shown whole. The full ID is always in `title` and in what Copy copies.
- **Code blocks:** dark block with a copy button pinned top-right, outside the scrolling area (`scrollbar-gutter: stable`).
- **Fonts:** self-hosted and embedded. No requests to third-party domains.
- **Tokens:** colours, spacing and type follow the canvas "Style guide" board. Dark mode swaps CSS custom properties.
- **Errors:** an action that fails shows its error in a toast, bottom right, which goes away after 8s or when dismissed (the dismiss button needs JS). An action request that fails without an answer (network error, 403, 500) shows "Request failed".

### Localization and time
- **English only** in v1. No i18n framework.
- **UI strings** live only in `.templ` files. Go code never builds user-facing text; it returns typed errors or codes that templates map to copy.
- **Formatting:** every number, date and duration is formatted by `internal/web/format`.
- **Timestamps:** rendered as `<time datetime="2026-11-08T00:11:52Z">` (RFC 3339, UTC) whose text is the absolute UTC time ("Nov 8, 00:11:52"). `app.js` converts each to the viewer's local time zone: relative text ("3m ago", "in 2m 40s") with the absolute local time in `title`, or the absolute local time where the design shows an absolute time. Without JS the UTC absolute time stays.
- **Daily counters:** "today" means asynq's UTC calendar day. The label is plain "today", with no UTC note.

### Tables
1. **Select, then act.** A checkbox column plus a toolbar showing "N selected" and the bulk action buttons. There are no per-row action buttons or kebab menus. Read-only tables have no checkboxes.
2. **Row click** opens the detail page. Row links are styled as links.
3. **Gmail-style select all** (task tables): the header checkbox selects the current page and shows a banner, "All N on this page are selected. Select all {total} {state} tasks". Once clicked, the banner reads "All {total} {state} tasks are selected. Clear selection", and actions then apply to the whole state through the `*All*` APIs. Not on the Active tab (no `*All*` cancel in asynq).
4. **Sorting:**
   - The queues table sorts by any column.
   - The scheduled, retry, archived and completed task tables sort by their state's time column only, both ways (§6.3). Active, Pending and Aggregating tables do not sort.
   - Servers: every column except Queues is sortable.
   - Schedulers: every column except Payload and Options is sortable.
   - Sort state lives in the URL query.
5. **Totals:** the Total row sits in `<tfoot>`.
6. **Pagination** (task tables): rows per page 20/50/100, "1–20 of 340", ‹ ›. Page and size live in the URL query.
7. **Destructive or "all" actions** open a confirmation dialog. So does every Archive (§6.3).

## 6. Pages

URLs follow docs/architecture.md and are shown here without `base_path`. Every link the server generates carries `base_path`, and every path segment (connection, queue, task, server and entry IDs) is escaped with `url.PathEscape`. Every page works without JavaScript for reading. htmx enhances it with polling, partial swaps and selection.

### 6.1 Overview `/`
- **Heading:** "All connections". A red "N unreachable" badge appears when N > 0.
- **Cards:** a grid with one card per visible connection, in config order. Health comes from the cached background check (§2).
- **Reachable card:**
  - name (title)
  - the user's role on that connection (chip)
  - address `host:port/db` (mono, muted)
  - Queues, Pending, Active and Retry
  - Processed today, Failed today (red) and **Oldest pending**: the maximum `QueueInfo.Latency` across queues that are not paused (0 when all are paused), as a duration, with no queue name
  - footer: a "Healthy" badge and an "Open →" link
- **Unreachable card:**
  - name, role chip and address
  - "Connection failed", with the error in a mono block
  - footer: an "Unreachable" badge and a "Retry now" button (`POST /c/{conn}/recheck`, viewer) that runs a health check immediately and re-renders the card
- **Unknown card** (first check still running): name, role chip, address and a muted "Checking" line, no badge.
- **Layout:** all card footers line up.

### 6.2 Connection → Queues `/c/{conn}`
- **Header:** "Queues", the address (mono, muted) and a Healthy or Unreachable badge.
- **Toolbar:** a "Filter queues" input (client-side, by name), a "N selected" label (only while rows are selected), and Pause, Resume and Delete.
  - Pause and Resume need operator, Delete needs admin. A role that cannot use one gets it disabled with the native `title` "Requires operator" or "Requires admin".
  - Pause is enabled only if any selected queue is running, and pauses only those; Resume likewise for paused queues. Delete is enabled only when every selected queue is empty (Size 0, which counts completed and aggregating tasks, as asynq reports it); otherwise it is disabled with the native `title` "Queue is not empty".
  - Delete asks first in a dialog listing the selected queues. Only empty queues can be deleted: the server reads the queue's Size afresh and refuses one above 0, then calls asynq's `DeleteQueue` without force (asynq's own check skips completed and aggregating tasks, whose keys a delete would orphan). A queue with tasks fails with "Queue {name} is not empty". There is no force delete. A queue that gains or loses tasks between the page's last refresh and the click shows that state until the next refresh. An aggregating task enqueued between that fresh Size read and the delete is orphaned; the window is a single round trip.
  - Without JS the buttons still post; Delete then answers with a confirmation page. After an action without JS the page redirects back (no re-post on reload), showing any failures as toasts carried in the one-minute `asynqui_flash` cookie.
  - Failures show one toast per queue ("Queue {name} not found", "Queue {name} is not empty", or "Could not pause {name}" with the error in mono). Success just refreshes the table. The selection is kept.
- **Table:** checkbox, then Queue (link), State (Running/Paused badge), Size, Pending, Active, Scheduled, Retry, Archived, Latency, Memory, Processed today, Failed today.
  - Every column sorts, server-side; default Size desc. A column sorts ascending first for Queue and State, descending for numbers; ties go by name. Sort state is `?sort=&dir=` in the URL.
  - Latency shows tenths of a second under a minute ("12.6s"), and "—" for a paused queue, which sorts last. Over 30s it is red. Memory is asynq's sampled `QueueInfo.MemoryUsage`, in binary units ("41.8 MB"). Size is asynq's `QueueInfo.Size`, which in asynq v0.26.0 counts every stored task, completed and aggregating ones included.
  - Retry and Archived are coloured when non-zero, Failed today red when non-zero.
  - The `<tfoot>` Total row sums everything except Latency, which shows "—".
  - Empty state: "No queues".
- **Throughput card:**
  - a range selector: 7d / 14d / 30d / 90d (a button group with `aria-pressed`), default 14d (`?range=`)
  - a Queue picker: "All queues" or one queue (`?queue=`); an unknown queue falls back to all
  - one overlaid line chart: Processed on the left axis, Failed on the right axis, each axis colour-matched to its line; x labels about a week apart, the last one "Today"
  - a hover tooltip per day (the SVG's native `<title>`) with the date, Processed, Failed and Failure rate; the same data is in a visually hidden table
- **Chart rendering:** server-rendered SVG, with the data from `Inspector.History` (summed over the queues for "All queues").
- **Unreachable connection:** the page shows "Connection failed" with the error in place of the table, and no chart. A read that does not finish within 5s shows "Checking" until a poll has it. A history read that misses it shows "Checking" in the chart; another history error shows "Could not load throughput" with the error.
- **Polling** refreshes the table, the chart body, the badge and the Updated time, never the chart controls. When a poll finds the connection failing, the table (and the selection) stays and a banner above it shows "Connection failed" or "Checking".

### 6.3 Queue `/c/{conn}/queues/{queue}?state=…`
- **Header:**
  - the queue name and a Running or Paused badge
  - a "Pause queue" or "Resume queue" button (operator)
  - stat cards: Size, Latency, Memory, Processed today, Failed today
- **Tabs, each with a count:** Active, Pending (default), Aggregating, Scheduled, Retry, Archived, Completed.
- **Find task:** a secondary "Find task" button in the header opens a dialog "Find task in {queue}" with one Task ID field. The lookup is `GetTaskInfo(queue, id)`, an exact ID in this queue: a match opens the Task page, a miss shows "Task not found" inline. `/` opens it outside text fields, the field is focused, Enter submits, Escape closes it and focus returns to the button. Without JS the button links to a page with the same form (`/c/{conn}/queues/{queue}/find`).
- **Table:** checkbox, Task ID (shortened, mono, links to the task), Type, Payload (mono, truncated to one line), then the columns that depend on the tab.
- **Sort:** on the scheduled, retry, archived and completed tabs the state's time column sorts both ways, server-side, with the existing sort header (`?dir=asc|desc`, `aria-sort`). The link flips the direction, goes back to page 1 and keeps the size; pager links and the rows-per-page select keep the direction; tab links reset it. It works without JS.
  - Scheduled: Process at (asynq's process time), soonest first by default.
  - Retry: Next retry, soonest first by default.
  - Archived: Archived at (when asynq archived it), newest first by default.
  - Completed: Result kept until (asynq orders completed tasks by when their result expires, `CompletedAt` + retention, not by completion time), latest first by default.
  - Pending and Active: asynq's order (oldest first), never sorted; `?dir=` is ignored. Aggregating: asynq's order within a group.
  - The Inspector cannot list in reverse, so the sorted tabs read Redis directly: one range read of the state's sorted set for the page, then `GetTaskInfo` per row (architecture: "Direct Redis reads"). A task that leaves the state between the two reads is left out, and so is one whose read fails with an error reply (logged as a WARN), so a page may show fewer rows. A queue that is gone or a server that cannot be reached fails the page as usual.

| Tab | Columns | Bulk actions | "All" actions |
|---|---|---|---|
| Active | Started, Running for (from the servers' worker lists; "—" for a task no server reports) | Cancel | — |
| Pending | Retried | Archive, Delete | Archive all, Delete all |
| Scheduled | Process at, In | Run now, Archive, Delete | Run all, Archive all, Delete all |
| Retry | Next retry, Last error | Run now, Archive, Delete | Run all, Archive all, Delete all |
| Archived | Archived at, Last error | Run now, Delete | Run all, Delete all |
| Completed | Completed at, Result kept until | Delete | Delete all |
| Aggregating, group list | Group (link), Size | Run all, Archive all, Delete all (of the selected groups) | — |
| Aggregating, one group | — | Run now, Archive, Delete | Run all, Archive all, Delete all (of the group) |

- **asynq limits:** `TaskInfo` has no enqueue time, so Pending has no time column. Archived at is the task's score in asynq's archived set, read with the page (a task archived by hand has no Last failed time).
- **Archive:** every Archive and Archive all button is red (danger) and asks first. Its dialog (and the no-JS confirmation page) says: "asynq keeps at most 10,000 archived tasks for 90 days; beyond that, tasks are removed." (asynq v0.26.0 trims the archive to 10,000 tasks and 90 days on every archive.)
- **Active:** no "select all N" and no "Cancel all" (asynq has no `CancelProcessing` for a state); Cancel sends `CancelProcessing` per selected task.

- **Aggregating tab:** lists groups (`Groups(queue)`) with their sizes, each with a checkbox and a link to the group's tasks (`?state=aggregating&group=`). Empty state: "No aggregating tasks".
  - The toolbar's Run all, Archive all and Delete all act on the selected groups, one asynq `RunAllAggregatingTasks(queue, group)`, `ArchiveAllAggregatingTasks` or `DeleteAllAggregatingTasks` per group, after a dialog ("Run all tasks in the selected groups") listing them; without JS the confirmation page is titled "Run all tasks in {group}" or "Run all tasks in N groups". The notice reports the sum asynq returns; a failing group gets a toast ("Could not run all tasks in {group}").
  - A group's tasks (`ListAggregatingTasks(queue, group)`) list like any task table, with "Groups / {group}" above the toolbar linking back. Run now, Archive and Delete act on the selection (asynq's `RunTask`, `ArchiveTask`, `DeleteTask`, which accept aggregating tasks though their docs do not say so; kept as in asynqmon), and "select all N" offers the group's Run all, Archive all and Delete all; its copy names the group ("Select all 8 tasks in {group}", "Run all 8 tasks in {group}", empty state "No tasks in {group}"). The pager counts the group's size.
  - Roles: Delete and Delete all need admin, the others operator. Polling and selection follow the other tabs: a poll swaps the group list (or the group's table, banner and pager), never the toolbar or dialogs.
- **After an action:** an inline result notice ("Archived 12 tasks"). The table refreshes and the selection clears. Failures show one toast per task ("Task {shortId} not found", "Could not run {shortId}" with asynq's error).
- **Pagination:** a page past the end (tasks moved on) shows the last page, or the first when the state has no tasks.
- **"All" actions** run asynq's `*All*` call when the dialog is confirmed: they act on whatever tasks the state holds at that moment (tasks may have arrived or left since the count was shown), and the notice reports the count asynq returns. The "all" selection stays while its dialog is open, whatever a poll changes on the page.
- **Untrusted text:** task IDs, types, queue and group names, payload previews and errors are isolated with `<bdi>`, and bidirectional control characters in IDs and names show as U+FFFD (the title and Copy keep the real value). So do the checkboxes' accessible labels and the names a confirmation dialog lists (each checkbox's `data-label`).

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
  - Timeout, Deadline, Group
  - Completed at and Result kept until, when relevant; Orphaned (Yes/No) for an active task; Headers
  - Missing values show "—".
  - `TaskInfo` has no unique key and no enqueue time, so neither is shown.
- **Actions** follow asynq: Run now for scheduled, retry, archived and aggregating tasks; Archive for pending, scheduled, retry and aggregating ones; Delete for any but active ones; Cancel for active ones. Archive and Delete ask first in a dialog (Archive with the archive warning, §6.3) and are red. Deleting the task returns to its queue's tab (an aggregating task's group).
- **Unknown or deleted ID:** shows "Task not found".
- **IDs "." and "..":** asynq accepts any custom task ID, but a path segment of `.` or `..` is resolved away by browsers and by Go's router, so such a task has no reachable Task page; it still lists and its actions still work from the Queue page.
- **Last error "attempt N":** asynq counts a failure in Retried when it schedules a retry, not when it archives. So it is Retried for a retry task, Retried + 1 for an archived one, and for a task run back to pending Retried + 1 when its retries were used up, else Retried.

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
- **Header:** the host (h1), a state badge, the server ID (mono) with Copy ID. The header is rendered once; polls refresh the badge, the workers and the details.
- **Active workers ({n}) table:** Task ID (links to the task), Type, Queue, Started (absolute), Running for. Sortable by Started and Running for, default Running for desc. Running for ≥10m is shown amber and semibold.
- **Details:** PID, Started, Concurrency, Busy, Queues (with weights), Strict priority (Yes/No).
- **Unknown ID:** shows "Server not found" (404).

### 6.7 Schedulers `/c/{conn}/schedulers`
- **Header:** "Schedulers" and a "Filter entries" input.
- **Read-only table** (no checkboxes):
  - Entry ID (shortened, a row link)
  - Spec: the cron spec in mono
  - Task type
  - Payload (mono, a one-line preview cut on the server, not sortable)
  - Options (`name=value` tokens such as `queue=critical max_retry=3`, not sortable)
  - Next run, Last run: relative, with absolute in the title
- **Default sort:** Next run asc.
- **Entry identity:** entries are identified by **entry ID**, which the scheduler regenerates on restart. Duplicate task types are allowed and shown as separate rows.
- **Pending a user decision:** a muted human description of the spec ("every day at 06:00"). It needs a cron describer (a dependency, or hand-written English in Go), so it is not built.

### 6.8 Scheduler entry `/c/{conn}/schedulers/{id}`
- **Breadcrumb:** ends with the short entry ID.
- **Header:**
  - the task type (h1)
  - the full entry ID (mono) with Copy ID
  - Rendered once, with the payload card; polls refresh the history and the details.
- **Payload card:** indented JSON, with a JSON / Raw toggle (links, `?payload=raw`, so it works without JS) when the payload is JSON; anything else is shown raw. A copy button copies what is shown. An empty payload shows "No payload".
- **Enqueue history table:**
  - Enqueued at (absolute), newest first
  - Task ID (links to the task while it still exists, otherwise plain text). Existence is checked with `GetTaskInfo` in the entry's queue and reused for 10s; a failed check shows plain text.
  - Paginated through `ListSchedulerEnqueueEvents`, 20 per page, "21–40" with ‹ ›. asynq keeps the newest 1000 events, so pages past ceil(1000/20) = 50 are out of range: nothing is read and the page links back to the last one.
- **Details:** Spec, Queue, Options (without the queue), Next run and Last run (absolute, then the muted relative time).
- **Queue:** the entry's `Queue` option. **asynq limitation:** a queue set on the task itself (`asynq.NewTask(…, asynq.Queue("x"))`) is not recorded in the entry, so an entry without a `Queue` option has an unknown queue. It shows "—" (never "default"), and its history task IDs are not linked.
- **Stale or unknown ID:** shows "Entry not found" (404) with a link back to Schedulers.
- **Dropped (no asynq API):**
  - History **Status** and **Error** columns: an asynq enqueue event holds only the task ID and the time, and failed enqueues are not recorded.
  - **Sortable Enqueued at:** asynq lists events newest first and gives no count, so oldest first would mean reading all 1000.
  - **Scheduler (host + pid):** an asynq scheduler entry does not carry its scheduler's host or PID.
  - The spec description is pending (§6.7).

### 6.9 Redis / Valkey info `/c/{conn}/info`
- **Title:** "Redis info" or "Valkey info", per §2, with the address underneath.
- **Info cards:**
  - Version
  - Mode
  - Uptime ("41 days" from a day on)
  - Connected clients
  - Used memory / peak (with maxmemory underneath)
  - maxmemory-policy
  - Keyspace db{n}: keys, plus "with expiry" underneath
  - TLS: On/Off, plus the version of the last handshake (recorded by `tls.Config.VerifyConnection`) and whether the server certificate is verified underneath
  - ACL user ("default" when none is configured)
- **Instance-wide values:** memory, clients and uptime belong to the whole instance. They appear **only** on this page, never on the Overview or Queues pages, because several connections can share one instance.
- **Raw INFO card:** the `INFO` output in a code block (height-capped) with a copy button. The Keyspace and Keysizes lines of other DBs are left out, since other connections may own them. Polls refresh the cards only; the raw block is the page load's snapshot.
- **Errors:** a network error or timeout shows "Connection failed" and the connection down (sidebar included). An error reply, such as `NOPERM` for an ACL user without `INFO`, shows "Could not read INFO" with the error, and the connection stays up. The Servers and Schedulers pages do the same.

## 7. Operational

### Command line
- A single static binary.
- **Flags:** `-config`, `-addr`, `-log-level`, `-version`. `-h` prints usage to stderr and exits 0. `-version` prints `asynqui {version}` to stdout and exits 0 without reading the config.
- **Version:** stamped at build time into `main.version` (`-ldflags "-X main.version=…"`): GoReleaser writes the tag without its `v` (`0.1.0`), `task build` writes `git describe` output. Without `-ldflags` the version is the main module version the go command recorded: the tag for `go install …/cmd/asynqui@v0.1.0`, a pseudo-version for `go build` in a git checkout, and `dev` when there is none (`go run`, `(devel)`).
- **Exit codes:** 0 after a clean shutdown, `-h` or `-version`; 1 on a startup, serve or shutdown error (message on stderr); 2 on a usage error (unknown flag or positional argument).

### Precedence
**flag > env > file > default.** A flag counts as set only if it appears on the command line (detected with `flag.FlagSet.Visit`), even when its value equals the default. An `ASYNQUI_*` variable that is set but empty counts as unset.

| Setting | Flag | Env | File key | Default |
|---|---|---|---|---|
| Config file path | `-config` | `ASYNQUI_CONFIG` | — | `asynqui.yaml` |
| Listen address | `-addr` | `ASYNQUI_ADDR` | `server.addr` | `:8080` |
| Log level | `-log-level` | `ASYNQUI_LOG_LEVEL` | `log.level` | `info` |

These are the only environment variables asynqui reads directly. Everything else comes from the file, which can pull values from any variable through interpolation (§8).

### Startup (fail fast)
1. Parse flags and env.
2. Load, interpolate and validate the config file. A missing file is an error.
3. Initialise authentication, including OIDC discovery, and the authorization policy.
4. Build the connection registry. This opens no Redis connections and never fails on Redis being down.
5. Open the listener and start the health loop and the update check.

Any error in steps 1–5 is printed to stderr and exits 1. The process never serves in a half-initialised state.

### Shutdown
On the first SIGINT or SIGTERM:
1. Signal handling is reset, so a **second signal force-quits** immediately.
2. Readiness turns 503 `shutting down`. This is terminal.
3. Wait `server.shutdown_delay` (default 5s; `0` skips the wait). The listener stays open and serves normally, so load balancers see the 503 and stop routing first.
4. `http.Server.Shutdown` with a **10s** timeout: the listener closes and in-flight requests finish. Request contexts are not canceled by the signal.
5. If the timeout expires, remaining connections are force-closed and the process exits 1.

`shutdown_delay` + 10s must stay below the orchestrator's grace period (Kubernetes `terminationGracePeriodSeconds`; the default 30s fits the default 15s). See [deployment.md](deployment.md).

### Probes
Both need no auth, return `text/plain` (`ok` or the reason) with `Cache-Control: no-store`, live under `<base>/health/` and are excluded from request logs.
- `GET <base>/health/liveness`: 200 whenever the process can serve HTTP. No dependency checks.
- `GET <base>/health/readiness`: 200 while serving, 503 `shutting down` from the start of shutdown until exit. There is no "starting" state, because the listener opens only after startup succeeds. It **never** depends on Redis.

### HTTP
- **Base path:** `server.base_path` applies to every route, including `/health/*` and `/static/`, to cookie paths and to every redirect. Requests outside it get 404.
- **Server timeouts:** read header 10s, read 30s, write 60s, idle 120s.
- **Security headers** on every response:
  - `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'`. There is no `unsafe-inline` or `unsafe-eval`, so templates have no inline `<script>`, `<style>` or `style=""`.
  - `form-action 'self'` also covers the redirects of a form submission, so no form may lead off-origin: SSO sign-in and proxy sign-out are links (§3).
  - `X-Content-Type-Options: nosniff`
  - `Referrer-Policy: same-origin`
  - `Cross-Origin-Opener-Policy: same-origin`
  - `Permissions-Policy` turning off camera, microphone, geolocation, payment, USB and the other device features the UI never uses. Clipboard write stays allowed for the copy buttons.
- **Errors:** unknown paths get 404. Error responses are never cached (`Cache-Control: no-store`). For a signed-in page request they are a page inside the layout (breadcrumb `Overview / Not found`); for htmx and non-GET requests plain text; without an identity a standalone page with no sidebar, so connection names are never shown to strangers.
- **Preference cookies** (`asynqui_theme`, `asynqui_refresh`): `Path=<base_path>`, one year, `SameSite=Lax`, `Secure` per `auth.session.cookie_secure`, readable by `app.js`.
- **Flash cookie** (`asynqui_flash`): the failures of an action posted without JS, for the page after its redirect. `Path=<base>/c/{conn}`, 60s, `HttpOnly`, `SameSite=Lax`, `Secure` per `cookie_secure`; read once, then cleared. It holds display text only and is not signed.
- **CSRF:** `http.CrossOriginProtection` on every unsafe method. Extra allowed origins come from `server.trusted_origins` (each is passed to `AddTrustedOrigin`; an invalid origin fails startup). The proxy must preserve the `Host` header ([deployment.md](deployment.md)).
- **Panics and render failures:** recovered, logged (panics with the stack), answered with 500 "Something went wrong" unless the response has already started.

### Logging
- Structured JSON via `log/slog` to stdout. Level from §7 "Precedence".
- One line per request (method, path, status, duration), except probes.
- Secrets (passwords, keys, client secret, cookies, tokens) are never logged.

### Redis load
- Each connection has a cache with a **1s TTL** that coalesces concurrent fetches, per snapshot. Every viewer shares the same fetches, whatever the number of viewers or their refresh interval.
- Connections are fetched in parallel. One slow or failing connection never blocks a page.
- Polling "Off" sends no polling requests at all.

### Update check
- With `update_check: true` and a release version, the server sends `GET https://api.github.com/repos/BarkinBalci/asynqui/releases/latest` at startup and then every 24h, until shutdown starts. A release version is a semantic version without a prerelease part or build metadata: `dev`, release candidates (`0.2.0-rc.1`), `git describe` or dirty builds from `task build` (`v0.1.0-3-gabc1234-dirty`) and pseudo-versions never check.
- The request sends no credentials, config or user data. GitHub sees the server's IP address (or its proxy's) and the asynqui version, from `User-Agent: asynqui/{version}`. It is server-side, so the CSP is unchanged.
- It times out after 5s. A failure (network, non-200, malformed JSON) is logged at debug level and keeps the last result.
- Drafts, prereleases and tags that are not such a release version are ignored. A release newer than the running version (by major, minor, patch) is remembered by its tag and logged once at info; an equal or older one clears it. The footer links to `https://github.com/BarkinBalci/asynqui/releases/tag/{tag}`, with the tag path-escaped.

### Releases
- A tag matching `v[0-9]+.[0-9]+.[0-9]+*` runs `.github/workflows/release.yml`: a `test` job (`contents: read`, `go test -race`), then a `goreleaser` job that needs it (`contents: write` and `packages: write` only) and runs GoReleaser (`.goreleaser.yaml`). The concurrency group `release` runs one release at a time and never cancels one halfway.
- Releases come only from the main line; there are no backport releases ([contributing.md](contributing.md#releases)).
- Binaries: linux, darwin and windows on amd64 and arm64, `CGO_ENABLED=0`, `-trimpath`, `-s -w`. Archives (`tar.gz`, `zip` on Windows) include LICENSE, README.md and config.example.yaml, with `checksums.txt` (SHA-256). Builds and archives are reproducible: file times are the tagged commit's, and archive entries are owned by root with fixed modes.
- The changelog in the GitHub release is grouped by conventional-commit prefix (features, fixes, performance, documentation, other); `test:`, `ci:`, `chore:` and `build:` commits are left out. Tags with a prerelease part are published as GitHub prereleases.
- Image: `ghcr.io/barkinbalci/asynqui` for linux/amd64 and linux/arm64 on distroless `static-debian12:nonroot` (pinned by digest), tagged `{version}`, `{major}.{minor}` and `latest` (prereleases only `{version}`). The binary is copied with mode 0555; its `created` label is the commit date. It runs as UID 65532, exposes 8080, sets `ASYNQUI_CONFIG=/etc/asynqui/config.yaml`, and has no `HEALTHCHECK` (no shell; use the probes).

## 8. Configuration

YAML, path from §7 "Precedence". [config.example.yaml](../config.example.yaml) shows every key and loads without any environment variables. Unknown keys are an error.

### Interpolation
- Applied to each **parsed scalar value** after YAML parsing and before decoding, so an interpolated value can never change the document's structure. `db: ${REDIS_DB:-0}` works.
- `${NAME}` is replaced by the variable's value. An unset variable is an error naming the variable and its line. A variable set to an empty string is used as is.
- `${NAME:-default}` uses `default` when `NAME` is unset (an empty value is still used as is).
- `$$` produces a literal `$`.
- Any other `$` is left alone, so bcrypt hashes like `$2y$12$…` need no escaping.
- An interpolated value is read as if written unquoted, whatever its quoting, so it fills integer, boolean and duration fields; string fields take it verbatim. An empty value is null and keeps the field's default. Keys are never interpolated.

### Errors
- A YAML syntax, type or unknown-key error, or an interpolation error, is reported on its own, with yaml's message and line. Type errors may quote the offending value.
- Otherwise every validation problem is reported at once, as `location: problem` (e.g. `connections[2].name: duplicate of connections[0]`). Validation messages never contain a value.
- Also rejected: null list entries and values of the wrong type.
- Warnings (logged, not fatal): a binding glob that matches no connection, TLS options set while `tls.enabled` is false, an OIDC `redirect_url` whose path is not `<base_path>/auth/callback`.

### Keys and defaults

| Key | Default | Notes |
|---|---|---|
| `server.addr` | `:8080` | Overridden by `-addr` / `ASYNQUI_ADDR`. |
| `server.base_path` | `""` | Empty or `/segment[/segment…]`, no trailing `/`. `/` is treated as empty. |
| `server.shutdown_delay` | `5s` | `0` disables. |
| `server.trusted_origins` | `[]` | Origins like `https://ops.example.com`. |
| `log.level` | `info` | `debug`, `info`, `warn`, `error`. |
| `refresh.default` | `5s` | `off`, `2s`, `5s`, `10s`. Initial auto-refresh for visitors without the cookie. |
| `update_check` | `true` | GitHub release check at startup and every 24h, fails silently (§7 "Update check"). |
| `connections` | — | Required, at least one. |
| `connections[].name` | — | Required, unique, `^[a-z0-9][a-z0-9._-]{0,62}$`. |
| `connections[].addr` | — | Required, `host:port`. |
| `connections[].db` | `0` | ≥ 0. |
| `connections[].username` | `""` | ACL user. |
| `connections[].password` | `""` | |
| `connections[].tls.enabled` | `false` | |
| `connections[].tls.ca_file` | `""` | Empty uses the system roots. Read at startup. |
| `connections[].tls.cert_file`, `key_file` | `""` | Client certificate; both or neither. Read at startup. |
| `connections[].tls.server_name` | `""` | Overrides SNI and the verified name. |
| `connections[].tls.insecure_skip_verify` | `false` | |
| `connections[].dial_timeout` | `5s` | |
| `connections[].read_timeout` | `3s` | |
| `connections[].write_timeout` | `3s` | |
| `auth.provider` | `none` | `none`, `basic`, `proxy`, `oidc`. |
| `auth.session.hash_key` | — | Hex, 32 or 64 bytes. Required for `basic` and `oidc`. |
| `auth.session.block_key` | — | Hex, 32 bytes, different from `hash_key`. Required for `basic` and `oidc`. |
| `auth.session.previous_keys` | `[]` | List of `{hash_key, block_key}`, decode only. |
| `auth.session.ttl` | `12h` | Absolute. |
| `auth.session.cookie_secure` | `true` | |
| `auth.basic.users[]` | — | Required for `basic`, at least one. |
| `auth.basic.users[].username` | — | Required, unique (case-insensitive). |
| `auth.basic.users[].password_hash` | — | Required, bcrypt. |
| `auth.basic.users[].groups` | `[]` | |
| `auth.proxy.user_header` | `X-Forwarded-User` | |
| `auth.proxy.groups_header` | `""` | Empty disables groups. |
| `auth.proxy.trusted_proxies` | — | Required for `proxy`. CIDRs or IPs; a CIDR must have no host bits set (`10.0.0.0/8`, not `10.1.2.3/8`). |
| `auth.proxy.logout_url` | `""` | Empty hides Sign out. |
| `auth.oidc.issuer` | — | Required for `oidc`. |
| `auth.oidc.client_id` | — | Required for `oidc`. |
| `auth.oidc.client_secret` | `""` | |
| `auth.oidc.redirect_url` | — | Required for `oidc`. `https://<host><base_path>/auth/callback`. |
| `auth.oidc.scopes` | `[openid, profile, email]` | |
| `auth.oidc.username_claim` | `email` | |
| `auth.oidc.groups_claim` | `groups` | |
| `authz.default` | `viewer` with `none`, otherwise `none` | `none`, `viewer`, `operator`, `admin`. |
| `authz.bindings[]` | `[]` | `users` and/or `groups`, `role` (`viewer`/`operator`/`admin`), `connections` (globs, non-empty). |

Sections for providers other than `auth.provider` are not used but are still decoded and interpolated, so an unset `${NAME}` there is an error too. Keep inactive provider blocks commented out.
