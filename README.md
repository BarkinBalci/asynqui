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

## Configuration

Every key is listed in [requirements §8](docs/requirements.md#8-configuration),
and the flags and environment variables in
[§7](docs/requirements.md#precedence). Values can come from the environment
through `${NAME}` in the YAML.

asynqui checks GitHub for new releases; set `update_check: false` to turn it
off ([deployment](docs/deployment.md#update-check)).

## Documentation

- [Requirements](docs/requirements.md): behaviour and configuration
- [Deployment](docs/deployment.md): running it in production
- [Architecture](docs/architecture.md): internals, for contributors
- [Contributing](docs/contributing.md): the dev workflow

## License

[MIT](LICENSE)
