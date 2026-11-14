<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/logo-dark.svg">
  <img alt="asynqui" src="docs/images/logo-light.svg" width="280">
</picture>

<p>A web dashboard for <a href="https://github.com/hibiken/asynq">asynq</a> across many Redis instances and databases.</p>

[![CI](https://github.com/BarkinBalci/asynqui/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/BarkinBalci/asynqui/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/BarkinBalci/asynqui)](https://github.com/BarkinBalci/asynqui/releases) [![Go version](https://img.shields.io/github/go-mod/go-version/BarkinBalci/asynqui)](go.mod) [![License](https://img.shields.io/github/license/BarkinBalci/asynqui)](LICENSE)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/overview-dark.png">
  <img alt="The Overview page: one card per connection with queue counts, today's throughput and health, and an unreachable connection with its error" src="docs/images/overview-light.png">
</picture>

</div>

Status: under active development towards v0.1.0.

## Features

- Many Redis instances and logical databases from one deployment
- An overview of every connection: queues, pending, active and retry counts, today's throughput and health
- Authentication with basic login, reverse-proxy headers or OIDC
- Per-connection roles (viewer, operator, admin) for users and groups
- Valkey detection
- Light and dark themes
- A single static binary with no external assets

<img alt="Opening a connection and a queue, selecting three tasks on the Retry tab and running them" src="docs/images/demo.gif">

## Screenshots

### Connection

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/connection-dark.png">
  <img alt="The Connection page: the queues table with per-state counts and the 14-day throughput chart" src="docs/images/connection-light.png">
</picture>

### Queue

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/queue-dark.png">
  <img alt="The Queue page on the Retry tab, with three tasks selected and the Run now, Archive and Delete actions" src="docs/images/queue-light.png">
</picture>

### Task

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/task-dark.png">
  <img alt="The Task page: the JSON payload, the last error and the task details" src="docs/images/task-light.png">
</picture>

## Quickstart

Start from [config.example.yaml](config.example.yaml). It loads as is, with no
login and the read-only `viewer` role. Set `authz.default: admin` for full
control without login, or configure an auth provider.

### Binary

Download an archive for your platform from
[Releases](https://github.com/BarkinBalci/asynqui/releases), check it against
`checksums.txt`, then:

```sh
cp config.example.yaml asynqui.yaml   # edit connections
./asynqui -config asynqui.yaml        # http://localhost:8080
```

### Docker

The image reads its config from `/etc/asynqui/config.yaml` and runs as a
non-root user:

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/asynqui.yaml:/etc/asynqui/config.yaml:ro" \
  ghcr.io/barkinbalci/asynqui:latest
```

Inside a container, `localhost` is the container itself, so point
`connections[].addr` at a reachable Redis host.

### Kubernetes

See [deployment](docs/deployment.md) for a Deployment and Service with probes
and graceful shutdown.

## Configuration

| Setting | Flag | Env | Config key | Default |
|---|---|---|---|---|
| Config file | `-config` | `ASYNQUI_CONFIG` | — | `asynqui.yaml` |
| Listen address | `-addr` | `ASYNQUI_ADDR` | `server.addr` | `:8080` |
| Log level | `-log-level` | `ASYNQUI_LOG_LEVEL` | `log.level` | `info` |

`asynqui -version` prints the version.

Precedence is flag > env > file > default. Any other value can come from the
environment through `${NAME}` or `${NAME:-default}` in the YAML. Every key is
listed in [requirements §8](docs/requirements.md#8-configuration).

asynqui checks GitHub for a newer release at startup and then daily, and shows a
dot next to the version in the sidebar. Set `update_check: false` to turn it off.

## Health probes

Both live under `server.base_path`:

- `GET /health/liveness`: 200 while the process runs.
- `GET /health/readiness`: 200 while serving, 503 from the start of shutdown.
  It never depends on Redis health.

On SIGTERM, readiness turns 503 for `server.shutdown_delay` (5s) before the
listener closes, then in-flight requests get 10s to finish. Keep
`terminationGracePeriodSeconds` above the sum. See
[deployment](docs/deployment.md).

## Development

Needs Go 1.27+. Tools are pinned in `tools/go.mod`. Tasks run with
[Task](https://taskfile.dev/installation/), or without installing it as
`go tool -modfile=tools/go.mod task`.

```sh
task              # list tasks
task run          # localhost:6379, no login, admin (config.dev.yaml)
task build        # bin/asynqui, version from git describe
task docker       # asynqui:dev image from source (Dockerfile.dev)
task lint test
task test-race    # needs cgo and a C compiler
task check        # the CI check job
task e2e          # browser tests
task screenshots  # regenerate docs/images
```

The browser tests and the screenshots need Chromium and a throwaway Redis; see
[contributing](docs/contributing.md).

## Documentation

- [Requirements](docs/requirements.md)
- [Architecture](docs/architecture.md)
- [Deployment](docs/deployment.md)
- [Contributing](docs/contributing.md)

## License

[MIT](LICENSE)
