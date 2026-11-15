# asynqui design

The behaviour spec for contributors: the code and tests follow it. To run asynqui, see [configuration.md](configuration.md), [authentication.md](authentication.md) and [deployment.md](deployment.md).

## 1. Scope

**In scope:** queues, tasks, servers, schedulers, Redis info and daily stats, plus:
- multiple connections
- an overview across connections
- authentication
- per-connection RBAC
- Valkey detection
- dark mode

**Non-goals:**
- Adding or editing connections from the UI.
- Redis Sentinel and Cluster.
- Any persistent storage of our own (database, files).
- Audit log.
- Metrics or Prometheus integration.
- Alerting.
- Per-queue RBAC.
- Editing scheduler entries (no asynq API).
- Connection env tags or colours.
- Localisation. The UI is English only, with no i18n framework.
- Server-side session revocation ([authentication](authentication.md#sessions)).

## 2. Connections

- **Config only:** connections are defined in YAML ([configuration](configuration.md#keys-and-defaults)) and are read-only in the UI.
- **What a connection is:** a Redis address plus a logical DB, plus a **name**.
- **Name:** required and unique. Duplicates make the binary refuse to start, with an error that names both entries. Names must match `^[a-z0-9][a-z0-9._-]{0,62}$`. The name is used in URLs and RBAC.
- **Standalone Redis only.** Supported options:
  - TLS: on/off, CA file, client cert/key, `insecure_skip_verify`, server name
  - ACL username and password
  - `dial_timeout` (default 5s), `read_timeout` (3s), `write_timeout` (3s), per connection. There is no global Redis timeout.
- **Backend detection:** auto-detected from `INFO server`. If `server_name:valkey` is present, label it "Valkey info" and show `valkey_version` with "Redis-compatible `redis_version`" underneath. Otherwise, and until the first detection, label it "Redis info" and show `redis_version`.
- **Where the DB number appears:** only in the sidebar's second line, `host:port/db`. Breadcrumbs, headings, titles and Overview cards show the name only.
- **Health:**
  - A background loop sends PING to every connection every 10s, bounded by that connection's timeouts. The first round starts at startup without blocking it.
  - *Reachable* means the last PING succeeded. *Unreachable* means it failed; the error text is kept for display.
  - Until the first PING finishes, the connection is *unknown*: muted dot, no badge.
  - The Overview, sidebar and page badges read the cached result and never PING themselves.
  - "Retry now" (§6.1) runs a check immediately and updates the cache.
- **Redis load:** every viewer shares the same fetches per connection, whatever the number of viewers or their refresh interval ([architecture](architecture.md#data-access-and-redis-load)). Connections are fetched in parallel. One slow or failing connection never blocks a page.

## 3. Authentication

Providers, sessions, keys and roles are configured as described in [authentication.md](authentication.md). This section is how sign-in behaves.

### Common rules
- **No identity:**
  - `basic`/`oidc`: 303 to `<base>/login?next=<path>`; for htmx requests, 401 with `HX-Redirect: <base>/login?next=<current page>`, so the whole page navigates instead of swapping in a fragment.
  - `proxy` (no identity, or rejected identity headers): 401.
  - Probes and `/static/` need no identity.
- **Return-to (`next`):** accepted only if it is a same-origin relative path under `base_path` (starts with `<base>/`, not `//`, no scheme or host), at most 1 KB, with no backslash, control character or encoded `.`, `/` or `\` (`%2e`, `%2f`, `%5c`). The path is cleaned (dot segments and repeated slashes resolved, keeping a trailing slash) before the prefix check, and the cleaned, re-encoded URL is what is used; fragments are dropped. Anything else falls back to `<base>/`. For htmx requests it comes from `HX-Current-URL` (path and query only), or is `<base>/` without one. Challenges send `Cache-Control: no-store`.
- **Sign out:** for `basic` and `oidc` it posts to `POST /logout`, which clears the session cookie and redirects to the login page (OIDC: local logout only). For `proxy` it is shown only when `logout_url` is set, as a plain link to it. It is never shown for `none`.

### `basic`
- Unknown usernames are compared against a dummy hash, so a miss costs the same as a hit.

### `proxy`
- There is no login page.

### `oidc`
- Authorization-code flow with **PKCE (S256)**. "Sign in with SSO" is a link to `GET /login/sso?next=…`, which sets the flow cookie and redirects to the IdP. The IdP returns to `GET /auth/callback`. `POST /login` is 404 for every provider but `basic`.
- **Flow cookie:** state, nonce, PKCE verifier and `next` are kept in the encrypted `asynqui_oidc` cookie ([cookies](authentication.md#cookies)). It is deleted by a callback whose `state` matches; a forged callback leaves it, so it cannot cancel a login in progress.
- **ID token:** the issuer, the audience (`client_id`), the expiry and the signature (RS256 and other asymmetric algorithms only) are verified, and so is the nonce. `sub` must be non-empty; `azp` must equal `client_id` when present or when `aud` has several entries; `iat` may be at most 5 minutes in the future.
- **Userinfo:** its claims are used only when the userinfo `sub` is present and equals the ID token's.

### Session cookie (`basic`, `oidc`)
- **Payload:** identity claims only, never tokens, plus the provider name: a cookie minted by another provider (e.g. a `basic` deployment sharing the keys) is rejected. Basic sessions store no groups. Cookie values over 4096 bytes are rejected before decoding; when several `asynqui_session` cookies arrive, the first valid one is used.

### Login page
- The username/password form appears only for `basic`.
- The "Sign in with SSO" link (styled as a button) appears only for `oidc`.
- A user who is already signed in is redirected from the login page to `next`, or to `<base>/`.
- The version is shown under the card.

## 4. Authorization

Roles, bindings, hidden connections and the capability table are in [authentication.md](authentication.md#roles-and-access). Every action is checked on the server; disabled buttons are only a UI hint.

- **Insufficient role:** the button is still shown, **disabled**, with a native `title` of "Requires {role}" ("Requires {role} on {conn}" on the Task page). There is no lock icon and no explanatory text.

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
  - `v{version}` on the left, followed by an accent dot when a newer GitHub release exists ([update check](deployment.md#update-check)). With an update, both link to the newer release's notes; otherwise to the running release's notes. A development build whose version names a commit (Go pseudo-version, `git describe`, GoReleaser snapshot) shows `dev {shortsha}` (at most 7 hex digits) with the full version in the title; other prereleases and `dev` are shown as is.
  - A muted "Source ↗" link on the right.
  - There is no GitHub logo, Docs link or Report-issue link.

### Top bar (every page, every control 36px tall)
- **Breadcrumbs:** `Overview / {conn} / {section} / {item}`. Every segment except the last is a link.
- **"Updated Ns ago":** muted text.
- **Auto refresh:** a segmented control with Off / 2s / 5s / 10s. The default comes from `refresh.default`. The choice is stored in a cookie ([cookies](authentication.md#cookies)). "Off" means no polling requests at all.
- **Theme:** a segmented control with three icon buttons (System, the default; Light; Dark). The choice is stored in a cookie, and the server renders the chosen theme, so there is no flash on load.
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
- **Shortened IDs:** a UUID shows its first 8-4 hex groups plus "…" (e.g. `c3e81b90-04aa…`). Any other ID longer than 12 characters shows its first 12 characters plus "…"; shorter IDs are shown whole. The full ID is always in `title` and in what Copy copies.
- **Code blocks:** dark block with a copy button pinned top-right, outside the scrolling area.
- **Fonts:** self-hosted and embedded. No requests to third-party domains.
- **Tokens:** colours, spacing and type are the custom properties in `app.css`. Dark mode swaps them.
- **Errors:** an action that fails shows its error in a toast, bottom right, which goes away after 8s or when dismissed (the dismiss button needs JS). An action request that fails without an answer (network error, 403, 500) shows "Request failed".

### Time
- **Timestamps:** the server renders the absolute UTC time ("Nov 8, 00:11:52") with a machine-readable RFC 3339 UTC value. In the browser each is shown in the viewer's local time zone: relative text ("3m ago", "in 2m 40s") with the absolute local time in `title`, or the absolute local time where the design shows an absolute time. Without JS the UTC absolute time stays.
- **Daily counters:** "today" means asynq's UTC calendar day. The label is plain "today", with no UTC note.

### Tables
1. **Select, then act.** A checkbox column plus a toolbar showing "N selected" and the bulk action buttons. There are no per-row action buttons or kebab menus. Read-only tables have no checkboxes.
2. **Row click** opens the detail page. Row links are styled as links.
3. **Gmail-style select all** (task tables): the header checkbox selects the current page and shows a banner, "All N on this page are selected. Select all {total} {state} tasks". Once clicked, the banner reads "All {total} {state} tasks are selected. Clear selection", and actions then apply to the whole state through the `*All*` APIs. Not on the Active tab (no `*All*` cancel in asynq).
4. **Sorting:**
   - Queues: every column.
   - Task tables: only the scheduled, retry, archived and completed tables, by their state's time column, both ways (§6.3).
   - Servers: every column except Queues. Server workers: Started and Running for.
   - Schedulers: every column except Payload and Options.
   - Sort state lives in the URL query.
5. **Totals:** the Total row sits at the foot of the table.
6. **Pagination** (task tables): rows per page 20/50/100, "1–20 of 340", ‹ ›. Page and size live in the URL query.
7. **Destructive or "all" actions** open a confirmation dialog. So does every Archive (§6.3).

## 6. Pages

Paths are shown without `base_path` (§7). Every page works without JavaScript for reading. htmx enhances it with polling, partial swaps and selection.

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
  - Pause and Resume need operator, Delete needs admin ([roles](authentication.md#roles-and-access)).
  - Pause is enabled only if any selected queue is running, and pauses only those; Resume likewise for paused queues. Delete is enabled only when every selected queue is empty (Size 0); otherwise it is disabled with the native `title` "Queue is not empty".
  - Delete asks first in a dialog listing the selected queues. The server reads the queue's Size afresh and refuses one above 0 with "Queue {name} is not empty", then calls asynq's `DeleteQueue` without force. asynq's own emptiness check skips completed and aggregating tasks, whose keys a delete would orphan, so the Size check comes first. There is no force delete. A queue that gains or loses tasks after the page's last refresh shows its old state until the next one. An aggregating task enqueued between the Size read and the delete is orphaned.
  - Without JS the buttons still post; Delete then answers with a confirmation page. After an action without JS the page redirects back (no re-post on reload), showing any failures as toasts carried in the one-minute `asynqui_flash` cookie ([cookies](authentication.md#cookies)).
  - Failures show one toast per queue ("Queue {name} not found", "Queue {name} is not empty", or "Could not pause {name}" with the error in mono). Success just refreshes the table. The selection is kept.
- **Table:** checkbox, then Queue (link), State (Running/Paused badge), Size, Pending, Active, Scheduled, Retry, Archived, Latency, Memory, Processed today, Failed today.
  - Every column sorts, server-side; default Size desc. A column sorts ascending first for Queue and State, descending for numbers; ties go by name. Sort state is `?sort=&dir=` in the URL.
  - Latency shows tenths of a second under a minute ("12.6s"), and "—" for a paused queue, which sorts last. Over 30s it is red. Memory is asynq's sampled `QueueInfo.MemoryUsage`, in binary units ("41.8 MB"). Size is asynq's `QueueInfo.Size`, which counts every stored task, completed and aggregating ones included.
  - Retry and Archived are coloured when non-zero, Failed today red when non-zero.
  - The Total row sums everything except Latency, which shows "—".
  - Empty state: "No queues".
- **Throughput card:**
  - a range selector: 7d / 14d / 30d / 90d (a toggle-button group), default 14d (`?range=`)
  - a Queue picker: "All queues" or one queue (`?queue=`); an unknown queue falls back to all
  - one overlaid line chart: Processed on the left axis, Failed on the right axis, each axis colour-matched to its line; x labels about a week apart, the last one "Today"
  - a native hover tooltip per day with the date, Processed, Failed and Failure rate; the same data is in a visually hidden table
- **Chart rendering:** server-rendered SVG, with the data from `Inspector.History` (summed over the queues for "All queues").
- **Unreachable connection:** the page shows "Connection failed" with the error in place of the table, and no chart. A read that does not finish within 5s shows "Checking" until a poll has it. A history read that misses it shows "Checking" in the chart; another history error shows "Could not load throughput" with the error.
- **Errors:** a network error or timeout shows "Connection failed" and the connection down (badge and sidebar included). An error reply, such as `NOAUTH`, shows "Could not load queues" with the error, and the connection stays up.
- **Polling** refreshes the table, the chart body, the badge and the Updated time, never the chart controls. When a poll finds the connection failing, the table (and the selection) stays and a banner above it shows why ("Connection failed", the error reply, or "Checking").

### 6.3 Queue `/c/{conn}/queues/{queue}?state=…`
- **Header:**
  - the queue name and a Running or Paused badge
  - a "Pause queue" or "Resume queue" button (operator)
  - stat cards: Size, Latency, Memory, Processed today, Failed today
- **Tabs, each with a count:** Active, Pending (default), Aggregating, Scheduled, Retry, Archived, Completed.
- **Find task:** a secondary "Find task" button in the header opens a dialog "Find task in {queue}" with one Task ID field. The lookup is `GetTaskInfo(queue, id)`, an exact ID in this queue: a match opens the Task page, a miss shows "Task not found" inline. `/` opens it outside text fields, the field is focused, Enter submits, Escape closes it and focus returns to the button. Without JS the button links to a page with the same form (`/c/{conn}/queues/{queue}/find`).
- **Table:** checkbox, Task ID (shortened, mono, links to the task), Type, Payload (mono, truncated to one line), then the columns that depend on the tab.
- **Sort:** on the scheduled, retry, archived and completed tabs the state's time column sorts both ways, server-side (`?dir=asc|desc`), and the sorted column is announced as sorted. The link flips the direction, goes back to page 1 and keeps the size; pager links and the rows-per-page select keep the direction; tab links reset it. It works without JS.
  - Scheduled: Process at (asynq's process time), soonest first by default.
  - Retry: Next retry, soonest first by default.
  - Archived: Archived at (when asynq archived it), newest first by default.
  - Completed: Result kept until (asynq orders completed tasks by when their result expires, `CompletedAt` + retention, not by completion time), latest first by default.
  - Pending and Active: asynq's order (oldest first), never sorted; `?dir=` is ignored. Aggregating: asynq's order within a group.
  - The sorted tabs read one page of the state's sorted set, then `GetTaskInfo` per row ([architecture](architecture.md#data-access-and-redis-load)). A task that leaves the state between the two reads is left out, and so is one whose read fails with an error reply (logged as a WARN), so a page may show fewer rows. A queue that is gone or a server that cannot be reached fails the page as usual.

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
- **Archive:** every Archive and Archive all button is red (danger) and asks first. Its dialog (and the no-JS confirmation page) says: "asynq keeps at most 10,000 archived tasks for 90 days; beyond that, tasks are removed."
- **Active:** no "select all N" and no "Cancel all" (asynq has no `CancelProcessing` for a state); Cancel sends `CancelProcessing` per selected task.

- **Aggregating tab:** lists groups (`Groups(queue)`) with their sizes, each with a checkbox and a link to the group's tasks (`?state=aggregating&group=`). Empty state: "No aggregating tasks".
  - The toolbar's Run all, Archive all and Delete all act on the selected groups, one asynq `RunAllAggregatingTasks(queue, group)`, `ArchiveAllAggregatingTasks` or `DeleteAllAggregatingTasks` per group, after a dialog ("Run all tasks in the selected groups") listing them; without JS the confirmation page is titled "Run all tasks in {group}" or "Run all tasks in N groups". The notice reports the sum asynq returns; a failing group gets a toast ("Could not run all tasks in {group}").
  - A group's tasks (`ListAggregatingTasks(queue, group)`) list like any task table, with "Groups / {group}" above the toolbar linking back. Run now, Archive and Delete act on the selection (asynq's `RunTask`, `ArchiveTask` and `DeleteTask` accept aggregating tasks, though undocumented), and "select all N" offers the group's Run all, Archive all and Delete all; its copy names the group ("Select all 8 tasks in {group}", "Run all 8 tasks in {group}", empty state "No tasks in {group}"). The pager counts the group's size.
  - Roles: Delete and Delete all need admin, the others operator. Polling and selection follow the other tabs: a poll swaps the group list (or the group's table, banner and pager), never the toolbar or dialogs.
- **After an action:** an inline result notice ("Archived 12 tasks"). The table refreshes and the selection clears. Failures show one toast per task ("Task {shortId} not found", "Could not run {shortId}" with asynq's error).
- **Pagination:** a page past the end (tasks moved on) shows the last page, or the first when the state has no tasks.
- **"All" actions** run asynq's `*All*` call when the dialog is confirmed: they act on whatever tasks the state holds at that moment (tasks may have arrived or left since the count was shown), and the notice reports the count asynq returns. The "all" selection stays while its dialog is open, whatever a poll changes on the page.
- **Untrusted text:** task IDs, types, queue and group names, payload previews and errors are isolated as bidirectional text, and bidirectional control characters in IDs and names show as U+FFFD (the title and Copy keep the real value). So do the checkboxes' accessible labels and the names a confirmation dialog lists.

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
- **Unknown or deleted ID:** shows "Task not found". A task deleted while its page is open shows it on the next poll, and polling stops.
- **IDs "." and "..":** asynq accepts any custom task ID, but a path segment of `.` or `..` is resolved away by browsers and by Go's router, so such a task has no reachable Task page; it still lists and its actions still work from the Queue page.
- **Last error "attempt N":** asynq counts a failure in Retried when it schedules a retry, not when it archives. So it is Retried for a retry task, Retried + 1 for an archived one, and for a task run back to pending Retried + 1 when its retries were used up, else Retried.

### 6.5 Servers `/c/{conn}/servers`
- **Header:** "Servers" and a "Filter servers" input.
- **Read-only table** (no checkboxes):
  - Host (a row link, the hostname; full server ID in the title)
  - PID
  - State: "Active" or "Stopped", from `ServerInfo.Status`
  - Busy: `n / concurrency` with a small bar
  - Queues: `name weight` tokens
  - Started: relative
- **Default sort:** Busy desc.
- **Empty state:** "No servers running".

### 6.6 Server detail `/c/{conn}/servers/{id}`
- **Header:** the host (h1), a state badge, the server ID (mono) with Copy ID. The header is rendered once; polls refresh the badge, the workers and the details.
- **Active workers ({n}) table:** Task ID (links to the task), Type, Queue, Started (absolute), Running for. Default sort Running for desc. Running for ≥10m is shown amber and semibold.
- **Details:** PID, Started, Concurrency, Busy, Queues (with weights), Strict priority (Yes/No).
- **Unknown ID:** shows "Server not found" (404).

### 6.7 Schedulers `/c/{conn}/schedulers`
- **Header:** "Schedulers" and a "Filter entries" input.
- **Read-only table** (no checkboxes):
  - Entry ID (shortened, a row link)
  - Spec: the cron spec in mono
  - Task type
  - Payload (mono, a one-line preview cut on the server)
  - Options (`name=value` tokens such as `queue=critical max_retry=3`)
  - Next run, Last run: relative, with absolute in the title
- **Default sort:** Next run asc.
- **Entry identity:** entries are identified by **entry ID**, which the scheduler regenerates on restart. Duplicate task types are allowed and shown as separate rows.
- There is no human-readable description of the spec; asynq does not provide one.

### 6.8 Scheduler entry `/c/{conn}/schedulers/{id}`
- **Breadcrumb:** ends with the short entry ID.
- **Header:**
  - the task type (h1)
  - the full entry ID (mono) with Copy ID
  - Rendered once, with the payload card; polls refresh the history and the details.
- **Payload card:** indented JSON, with a JSON / Raw toggle (links, `?payload=raw`, so it works without JS) when the payload is JSON; anything else is shown raw. A copy button copies what is shown. An empty payload shows "No payload".
- **Enqueue history table:**
  - Enqueued at (absolute), newest first
  - Task ID (links to the task while it still exists, otherwise plain text). Existence is checked with `GetTaskInfo` in the entry's queue; a failed check shows plain text.
  - Paginated through `ListSchedulerEnqueueEvents`, 20 per page, "21–40" with ‹ ›. asynq keeps the newest 1000 events, so a page past the 50th reads nothing and links back to the last one.
- **Details:** Spec, Queue, Options (without the queue), Next run and Last run (absolute, then the muted relative time).
- **Queue:** the entry's `Queue` option. **asynq limitation:** a queue set on the task itself (`asynq.NewTask(…, asynq.Queue("x"))`) is not recorded in the entry, so an entry without a `Queue` option has an unknown queue. It shows "—" (never "default"), and its history task IDs are not linked.
- **Stale or unknown ID:** shows "Entry not found" (404) with a link back to Schedulers.
- **Not shown (asynq has no data for them):**
  - History **Status** and **Error**: an enqueue event holds only the task ID and the time, and failed enqueues are not recorded.
  - **Sorting by Enqueued at:** asynq lists events newest first and gives no count.
  - **Scheduler host and PID:** an entry does not carry them.

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
  - TLS: On/Off, plus the version of the last handshake and whether the server certificate is verified underneath
  - ACL user ("default" when none is configured)
- **Instance-wide values:** memory, clients and uptime belong to the whole instance. They appear **only** on this page, never on the Overview or Queues pages, because several connections can share one instance.
- **Raw INFO card:** the `INFO` output in a code block (height-capped) with a copy button. The Keyspace and Keysizes lines of other DBs are left out, since other connections may own them. Polls refresh the cards only; the raw block is the page load's snapshot.
- **Errors:** a network error or timeout shows "Connection failed" and the connection down (sidebar included). An error reply, such as `NOPERM` for an ACL user without `INFO`, shows "Could not read INFO" with the error, and the connection stays up. The Servers and Schedulers pages do the same.

## 7. HTTP

Startup, flags and logging are in [configuration.md](configuration.md); probes, shutdown and the update check in [deployment.md](deployment.md).

- **Base path:** `server.base_path` applies to every route, including `/health/*` and `/static/`, to cookie paths and to every redirect. Requests outside it get 404.
- **Paths:** every generated link carries `base_path`, and every path segment (connection, queue, task, server and entry IDs) is escaped.
- **Security headers** on every response:
  - `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'`. There is no `unsafe-inline` or `unsafe-eval`, so pages have no inline script or style.
  - `form-action 'self'` also covers the redirects of a form submission, so no form may lead off-origin: SSO sign-in and proxy sign-out are links.
  - `X-Content-Type-Options: nosniff`
  - `Referrer-Policy: same-origin`
  - `Cross-Origin-Opener-Policy: same-origin`
  - `Permissions-Policy` turning off camera, microphone, geolocation, payment, USB and the other device features the UI never uses. Clipboard write stays allowed for the copy buttons.
- **Errors:** unknown paths get 404. Error responses are never cached (`Cache-Control: no-store`). For a signed-in page request they are a page inside the layout (breadcrumb `Overview / Not found`); for htmx and non-GET requests plain text; without an identity a standalone page with no sidebar, so connection names are never shown to strangers.
- **CSRF:** cross-origin requests with an unsafe method are rejected. Extra allowed origins come from `server.trusted_origins` ([reverse proxy](deployment.md#preserve-host)).
- **Panics and render failures:** recovered, logged (panics with the stack), answered with 500 "Something went wrong" unless the response has already started.
