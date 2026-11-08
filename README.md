# asynqui

A web dashboard for [asynq](https://github.com/hibiken/asynq) that works across
**multiple Redis instances and logical databases** from one deployment. Think
asynqmon, plus:

- many connections side by side, with an overview of all of them
- authentication (basic, reverse-proxy headers, OIDC) and per-connection roles
- Valkey detection, dark mode, and a single static binary with no external assets

> Status: under active development towards v0.1.0.

## Quickstart

_Coming with the first release._ It will look like:

```sh
docker run -p 8080:8080 -v $PWD/asynqui.yaml:/asynqui.yaml ghcr.io/barkinbalci/asynqui
```

Start from [config.example.yaml](config.example.yaml). It loads as is (no login,
read-only `viewer` role); set `authz.default: admin` for full control without
login, or configure an auth provider.

## Configuration

| Setting | Flag | Env | Config key | Default |
|---|---|---|---|---|
| Config file | `-config` | `ASYNQUI_CONFIG` | — | `asynqui.yaml` |
| Listen address | `-addr` | `ASYNQUI_ADDR` | `server.addr` | `:8080` |
| Log level | `-log-level` | `ASYNQUI_LOG_LEVEL` | `log.level` | `info` |

Precedence is flag > env > file > default. Any other value can come from the
environment through `${NAME}` or `${NAME:-default}` in the YAML. Every key is
listed in [requirements §8](docs/requirements.md#8-configuration).

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

Needs Go 1.26+. Tools are pinned in `tools/go.mod`.

```sh
make help       # list targets
make run        # localhost:6379, no login, admin (config.dev.yaml)
make lint test
make test-race  # needs cgo and a C compiler
```

## Documentation

- [Requirements](docs/requirements.md)
- [Architecture](docs/architecture.md)
- [Deployment](docs/deployment.md)

## License

[MIT](LICENSE)
