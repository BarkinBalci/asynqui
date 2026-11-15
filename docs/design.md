# Design decisions

Design decisions behind asynqui, and why. For setup, see [configuration.md](configuration.md) and [deployment.md](deployment.md). For how the code is organised, see [architecture.md](architecture.md).

## Scope

- asynqui is a UI over asynq's public Inspector API. When asynq has no API for a feature, asynqui leaves the feature out rather than working around asynq. The one exception is the task tables' time sort, described under [Queues and tasks](#queues-and-tasks).
- asynqui keeps no storage of its own, no database and no files. So there is no audit log and no server-side session revocation: signing out clears the cookie in that browser only.
- Besides the [limitations in the README](../README.md#limitations), these are out of scope: alerting, per-queue roles, connection tags or colours, and localisation. The UI is English only.

## Connections

- Connections are defined only in the config file and can't be edited in the UI. asynqui has nowhere to store edits, and the file can be reviewed and deployed like any other config.
- A connection is identified by its name, which appears in URLs and role bindings, so it must be unique and URL-safe ([keys](configuration.md#keys-and-defaults)). The DB number shows only in the sidebar.
- A connection is marked down only when Redis can't be reached: a network error or a timeout. An error reply, such as `NOAUTH` or `NOPERM`, means Redis answered, so the page shows the error and the connection stays up.
- A slow or down connection never blocks the others, and a Redis that is down never fails [startup](configuration.md#startup) or the [probes](deployment.md#probes). One broken connection doesn't take the dashboard down.
- Redis load doesn't grow with the number of viewers: they share one cached fetch per connection, whatever their refresh interval.
- Memory, clients and uptime belong to the whole Redis instance, and several connections can share one. So they appear only on the Redis info page, never on the Overview or Queues pages, and the raw INFO output leaves out the keyspace lines of other DBs.

## Queues and tasks

- "Today" counts are asynq's daily counters, which roll over at midnight UTC. The label is plain "today".
- The Overview's "Oldest pending" is the largest latency among queues that aren't paused. A paused queue's tasks are meant to wait.
- Task tables sort only by their state's time column, on the scheduled, retry, archived and completed tabs. asynq keeps those states in sorted sets scored by that time, so one range read returns a page in either direction. Any other order would mean reading the whole state.
- That sort reads asynq's sorted sets directly, the only place asynqui goes past the Inspector API ([direct reads](architecture.md#data-access-and-redis-load)). Completed tasks sort by when their result expires, the score asynq keeps, not by when they completed.
- Pending, active and aggregating tasks are listed in asynq's own order.
- A queue can be deleted only when it is empty. The check counts completed and aggregating tasks, which asynq's own emptiness check skips and a delete would orphan. There is no force delete.
- Run now, Archive and Delete work on single aggregating tasks. asynq accepts them, though it doesn't document it.

## Left out because asynq has no API for it

- **Cancel all:** asynq cancels one active task at a time, so the Active tab has no "Cancel all" and no "select all".
- **Task search:** asynq looks a task up only by queue and exact ID, so Find task takes an exact ID within one queue.
- **Enqueue time:** asynq doesn't record one, so Pending has no time column and a task shows no enqueue time. It shows no unique key either.
- **Scheduler entries:** read-only. There is no editing and no "run now".
- **Cron descriptions:** a spec is shown as written, with no human-readable description.
- **Scheduler history:** an enqueue event holds only the task ID and the time. There is no status or error, failed enqueues aren't recorded, and the history can't be sorted.
- **Scheduler host and PID:** an entry doesn't carry them.
- **An entry's queue:** it is the entry's `Queue` option. A queue set on the task itself isn't recorded in the entry, so such an entry shows "—", never "default", and its history doesn't link to tasks.

## Actions and roles

- Roles are per connection. Viewers read, operators also pause, run, archive and cancel, and admins also delete tasks and queues. [authentication.md](authentication.md#roles-and-access) has the full table.
- The server checks every action. A button the user's role doesn't allow stays visible but disabled, and its tooltip names the role it needs.
- Tables work by select, then act: a checkbox column and a toolbar of actions. There are no per-row buttons or menus.
- Destructive and "all" actions ask first.
- Archive always asks first and is styled as destructive, because asynq trims the archive. The dialog warns that asynq keeps at most 10,000 archived tasks for 90 days.
- An "all" action acts on whatever the state holds when the dialog is confirmed, not what the page showed. The result reports the count asynq returns.

## UI conventions

- **Minimal copy:** no helper or explanatory text. An empty state is one short line.
- **Works without JavaScript:** every page reads in full, and every action is a form post that redirects back, so a reload never posts again. Failures come back in a short-lived cookie. Timestamps stay in UTC without JavaScript and show in local time with it. htmx adds polling, partial swaps and selection on top.
- **Polls never replace what the user is using:** the selection, sort, page, filters, toolbars and open dialogs stay as they are ([polling](architecture.md#frontend-conventions)). When a poll finds the connection failing, the last data stays, with a banner saying why.
- **No third-party requests:** fonts and every other asset are embedded in the binary, and the Content-Security-Policy allows only asynqui's own origin.
- **Error pages:** someone who isn't signed in gets a standalone error page with no sidebar, so connection names are never shown to strangers.
