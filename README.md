<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/logo-dark.svg">
  <img alt="asynqui" src="docs/images/logo-light.svg" width="280">
</picture>

<p>An <a href="https://github.com/hibiken/asynq">asynq</a> dashboard for all your Redis and Valkey instances.</p>

[![CI](https://github.com/BarkinBalci/asynqui/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/BarkinBalci/asynqui/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/BarkinBalci/asynqui)](https://github.com/BarkinBalci/asynqui/releases) [![Go version](https://img.shields.io/github/go-mod/go-version/BarkinBalci/asynqui)](go.mod) [![License](https://img.shields.io/github/license/BarkinBalci/asynqui)](LICENSE)

<img alt="asynqui in use: the Overview, a connection's queues, then three tasks on a queue's Retry tab selected and run" src="docs/images/demo.gif">

</div>

## Features

- Login with basic auth, reverse-proxy headers or OIDC
- Roles per connection for users and groups
- A single static binary or a container image

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

The image reads its config from `/etc/asynqui/config.yaml`:

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/asynqui.yaml:/etc/asynqui/config.yaml:ro" \
  ghcr.io/barkinbalci/asynqui:latest
```

Inside a container, `localhost` is the container itself, so point
`connections[].addr` at a reachable Redis or Valkey host.

### Kubernetes

See the [deployment example](docs/deployment.md#kubernetes-example).

## Limitations

- Standalone Redis or Valkey only: no Sentinel or Cluster.
- No metrics endpoint and no audit log.
- Connections are managed in the config file, not in the UI.
- Only empty queues can be deleted; there is no force delete.
- Active tasks are cancelled by selection; there is no "Cancel all".
- asynq caps the archive at 10,000 tasks and 90 days.
- Scheduler entries are read-only: no "run now".

## Configuration

Every flag, environment variable and config key is in [docs/configuration.md](docs/configuration.md).
asynqui checks GitHub for new releases; [update check](docs/deployment.md#update-check) says how to turn it off.

## Documentation

**Using**

- [Configuration](docs/configuration.md): flags, environment and every config key
- [Authentication](docs/authentication.md): providers, sessions, roles and cookies
- [Deployment](docs/deployment.md): install, Kubernetes, probes and reverse proxies

**Contributing**

- [Contributing](docs/contributing.md): local development and the dev workflow
- [Design](docs/design.md): the behaviour spec
- [Architecture](docs/architecture.md): internals
- [Releasing](docs/releasing.md): cutting a release

## License

[MIT](LICENSE)
