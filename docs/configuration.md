# Configuration

asynqui reads one YAML file, plus a few flags and environment variables. [config.example.yaml](../config.example.yaml) shows every key and loads without any environment variables. Authentication and roles are in [authentication.md](authentication.md).

## Command line

- **Flags:** `-config`, `-addr`, `-log-level`, `-version`.
- `-h` prints usage to stderr and exits 0.
- `-version` prints `asynqui {version}` to stdout and exits 0 without reading the config. Release builds report the tag without its `v` (`0.1.0`).
- **Exit codes:** 0 after a clean shutdown, `-h` or `-version`; 1 on a startup, serve or shutdown error (message on stderr); 2 on a usage error (unknown flag or positional argument).

## Precedence

**flag > env > file > default.** A flag counts as set only if it appears on the command line, even when its value equals the default. An `ASYNQUI_*` variable that is set but empty counts as unset.

| Setting | Flag | Env | File key | Default |
|---|---|---|---|---|
| Config file path | `-config` | `ASYNQUI_CONFIG` | — | `asynqui.yaml` |
| Listen address | `-addr` | `ASYNQUI_ADDR` | `server.addr` | `:8080` |
| Log level | `-log-level` | `ASYNQUI_LOG_LEVEL` | `log.level` | `info` |

These are the only environment variables asynqui reads directly. Everything else comes from the file, which can pull values from any variable through [interpolation](#interpolation).

## Startup

asynqui fails fast:

1. Parse flags and env.
2. Load, interpolate and validate the config file. A missing file is an error.
3. Initialise authentication, including OIDC discovery, and the authorization policy.
4. Build the connection registry. This opens no Redis connections, so a Redis that is down never fails startup.
5. Open the listener and start the health loop and the update check.

Any error in steps 1–5 is printed to stderr and exits 1. The process never serves in a half-initialised state.

## Logging

- Structured JSON to stdout, at the level from [Precedence](#precedence).
- One line per request (method, path, status, duration), except probes.
- Secrets (passwords, keys, client secret, cookies, tokens) are never logged.

## Config file

YAML, at the path from [Precedence](#precedence). Unknown keys are an error.

### Interpolation

- `${NAME}` is replaced by the variable's value. An unset variable is an error naming the variable and its line. A variable set to an empty string is used as is.
- `${NAME:-default}` uses `default` when `NAME` is unset (an empty value is still used as is).
- `$$` produces a literal `$`.
- Any other `$` is left alone, so bcrypt hashes like `$2y$12$…` need no escaping.
- Only values are interpolated, never keys, so an interpolated value can never change the document's structure.
- An interpolated value is read as if written unquoted, whatever its quoting, so it fills integer, boolean and duration fields: `db: ${REDIS_DB:-0}` works. String fields take it verbatim. An empty value keeps the field's default.

### Errors and warnings

- A YAML syntax, type or unknown-key error, or an interpolation error, is reported on its own, with yaml's message and line. Type errors may quote the offending value.
- Otherwise every validation problem is reported at once, as `location: problem` (e.g. `connections[2].name: duplicate of connections[0]`). Validation messages never contain a value.
- Also rejected: null list entries and values of the wrong type.
- Warnings (logged, not fatal): a binding glob that matches no connection, TLS options set while `tls.enabled` is false, an OIDC `redirect_url` whose path is not `<base_path>/auth/callback`.

### Keys and defaults

| Key | Default | Notes |
|---|---|---|
| `server.addr` | `:8080` | Overridden by `-addr` / `ASYNQUI_ADDR`. |
| `server.base_path` | `""` | Empty or `/segment[/segment…]`, no trailing `/`. `/` is treated as empty. |
| `server.shutdown_delay` | `5s` | `0` disables. See [Graceful shutdown](deployment.md#graceful-shutdown). |
| `server.trusted_origins` | `[]` | Origins like `https://ops.example.com`. See [Reverse proxy](deployment.md#preserve-host). |
| `log.level` | `info` | `debug`, `info`, `warn`, `error`. |
| `refresh.default` | `5s` | `off`, `2s`, `5s`, `10s`. Initial auto-refresh for visitors without the cookie. |
| `update_check` | `true` | See [Update check](deployment.md#update-check). |
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
| `auth.provider` | `none` | `none`, `basic`, `proxy`, `oidc`. See [Providers](authentication.md#providers). |
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
| `authz.default` | `viewer` with `none`, otherwise `none` | `none`, `viewer`, `operator`, `admin`. See [Roles and access](authentication.md#roles-and-access). |
| `authz.bindings[]` | `[]` | `users` and/or `groups`, `role` (`viewer`/`operator`/`admin`), `connections` (globs, non-empty). |

Sections for providers other than `auth.provider` are not used but are still decoded and interpolated, so an unset `${NAME}` there is an error too. Keep inactive provider blocks commented out.
