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

Start from [config.example.yaml](config.example.yaml).

## Health probes

- `GET /health/liveness`: 200 while the process runs.
- `GET /health/readiness`: 200 once startup is complete, 503 while shutting down.
  It never depends on Redis health.

## Documentation

- [Requirements](docs/requirements.md)
- [Architecture](docs/architecture.md)

## License

[MIT](LICENSE)
