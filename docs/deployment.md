# Deploying asynqui

Behaviour is specified in [requirements.md](requirements.md) (§7 Operational, §8 Configuration). This page covers what the deployment around asynqui must get right.

## Probes

Probe paths include `server.base_path`.

```yaml
livenessProbe:
  httpGet: { path: /health/liveness, port: 8080 }   # <base_path>/health/liveness
readinessProbe:
  httpGet: { path: /health/readiness, port: 8080 }
  periodSeconds: 2
```

Readiness never depends on Redis, so a Redis outage does not unroute or restart replicas.

## Graceful shutdown

On SIGTERM asynqui turns readiness 503, keeps serving for `server.shutdown_delay` (default 5s), then shuts down with a 10s timeout. A second SIGTERM or SIGINT force-quits.

- **`terminationGracePeriodSeconds` must exceed `shutdown_delay` + 10s.** The Kubernetes default of 30s fits the default 15s. If you raise `shutdown_delay`, raise the grace period too.
- `shutdown_delay` should be at least readiness `periodSeconds` × `failureThreshold` plus your load balancer's deregistration lag, so no new requests arrive after the listener closes.
- Set `shutdown_delay: 0` only when nothing routes by readiness (e.g. a single local instance).

## Reverse proxy

### Preserve Host

CSRF protection (`http.CrossOriginProtection`) compares the browser's `Origin` with the request's `Host`. The proxy must pass the original `Host` header through (nginx: `proxy_set_header Host $host;`; most ingress controllers do this by default). If the UI is reached under a different origin than the Host asynqui sees, list that origin in `server.trusted_origins`.

### Sub-path

With `server.base_path: /asynq`, forward `/asynq/...` **without** stripping the prefix. asynqui serves every route, including probes and static files, under the prefix.

### `auth.provider: proxy`

asynqui trusts identity headers only when the TCP peer (`r.RemoteAddr`) is in `auth.proxy.trusted_proxies`. `X-Forwarded-For` is never used. Both of the following are required, not optional:

1. **Strip client-supplied identity headers at the proxy.** The proxy must remove any incoming `user_header` and `groups_header` from the client request before setting its own. Otherwise a user can send `X-Forwarded-User: admin` through the trusted proxy.

   ```nginx
   # nginx + oauth2-proxy: overwrite, never pass through
   proxy_set_header X-Forwarded-User   $upstream_http_x_auth_request_user;
   proxy_set_header X-Forwarded-Groups $upstream_http_x_auth_request_groups;
   ```

2. **Only the proxy can reach asynqui.** Keep `trusted_proxies` as narrow as possible: a single `/32` (or `/128`) per proxy instance, never a whole node or pod range. On Kubernetes, add a NetworkPolicy so that only the proxy pods can connect:

   ```yaml
   apiVersion: networking.k8s.io/v1
   kind: NetworkPolicy
   metadata:
     name: asynqui-from-proxy-only
   spec:
     podSelector:
       matchLabels: { app: asynqui }
     policyTypes: [Ingress]
     ingress:
       - from:
           - podSelector:
               matchLabels: { app: auth-proxy }
         ports:
           - port: 8080
   ```

   The kubelet's probes are not affected by this policy on most CNIs; check yours.

Requests from an untrusted address that carry identity headers get 401, as do trusted requests with an empty user header.

### `auth.provider: basic`

The login rate limit (5 attempts per minute) is keyed by `r.RemoteAddr`. Behind a proxy every user shares the proxy's address, so one user's failed attempts can lock out others for up to a minute.

## Cookies and TLS

- Serve over HTTPS. Session cookies are `Secure` unless `auth.session.cookie_secure: false`, which is only for local plain HTTP.
- Generate keys with `openssl rand -hex 32` and pass them through environment variables (`hash_key: ${ASYNQUI_SESSION_HASH_KEY}`).
- Sessions cannot be revoked server-side. To sign everyone out, rotate the keys without keeping the old pair in `previous_keys`. To rotate without signing anyone out, move the old pair to `previous_keys` and remove it after `ttl` (default 12h).

## `auth.provider: none`

Everyone gets `authz.default` (viewer unless set). asynqui logs a WARN at startup when it listens on a non-loopback address with no authentication. Put it behind something that authenticates, or bind it to `127.0.0.1`.
