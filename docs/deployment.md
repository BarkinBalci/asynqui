# Deploying asynqui

What the deployment around asynqui must get right. Behaviour and every config key are in [requirements.md](requirements.md).

## Container image

`ghcr.io/barkinbalci/asynqui` is multi-arch (linux/amd64, linux/arm64) and built on `gcr.io/distroless/static-debian12:nonroot`. Tags: the exact version (`0.1.0`), `0.1` and `latest`; prereleases get only their exact version. Pin the exact version in production.

- Runs as UID/GID 65532 and listens on 8080.
- Reads its config from `/etc/asynqui/config.yaml` (`ASYNQUI_CONFIG`; `-config` overrides it). Mount the file read-only.
- UID 65532 must be able to read it: mode `0644`, or `0640` with group 65532. On SELinux hosts add `:z` to the bind mount. For a Kubernetes Secret with a restrictive `defaultMode`, set `fsGroup: 65532`.
- Flags go after the image name, e.g. `docker run ... ghcr.io/barkinbalci/asynqui:0.1.0 -log-level debug`.
- There is no shell, so there is no Docker `HEALTHCHECK`. Use the HTTP probes.

## Kubernetes example

Secrets come from the environment through interpolation (`${NAME}`), so the ConfigMap holds no secrets.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: asynqui
data:
  config.yaml: |
    server:
      shutdown_delay: 5s
    connections:
      - name: prod
        addr: redis.prod.svc.cluster.local:6379
        password: ${REDIS_PASSWORD}
    auth:
      provider: proxy
      proxy:
        trusted_proxies: ["10.0.0.10/32"]
    authz:
      bindings:
        - groups: [platform]
          role: admin
          connections: ["*"]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: asynqui
  labels: { app: asynqui }
spec:
  replicas: 2 # stateless: replicas need only the same config
  selector:
    matchLabels: { app: asynqui }
  template:
    metadata:
      labels: { app: asynqui }
    spec:
      terminationGracePeriodSeconds: 30 # see Graceful shutdown
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: asynqui
          image: ghcr.io/barkinbalci/asynqui:0.1.0
          ports:
            - name: http
              containerPort: 8080
          env:
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef: { name: asynqui, key: redis-password }
          volumeMounts:
            - name: config
              mountPath: /etc/asynqui
              readOnly: true
          livenessProbe:
            httpGet: { path: /health/liveness, port: http }
          readinessProbe:
            httpGet: { path: /health/readiness, port: http }
            periodSeconds: 2
            failureThreshold: 2
          resources:
            requests: { cpu: 10m, memory: 32Mi }
            limits: { memory: 128Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: [ALL] }
      volumes:
        - name: config
          configMap: { name: asynqui }
---
apiVersion: v1
kind: Service
metadata:
  name: asynqui
spec:
  selector: { app: asynqui }
  ports:
    - name: http
      port: 80
      targetPort: http
```

## Probes

`/health/liveness` and `/health/readiness` ([requirements §7](requirements.md#probes)) live under `server.base_path`: with `base_path: /asynq` they are `/asynq/health/liveness` and `/asynq/health/readiness`. Neither depends on Redis, so a Redis outage does not unroute or restart replicas.

## Graceful shutdown

On SIGTERM asynqui turns readiness 503, keeps serving for `server.shutdown_delay`, then shuts down with a 10s timeout ([requirements §7](requirements.md#shutdown)).

- **`terminationGracePeriodSeconds` must exceed `shutdown_delay` + 10s.** If you raise `shutdown_delay`, raise the grace period too.
- `shutdown_delay` should be at least readiness `periodSeconds` × `failureThreshold` plus your load balancer's deregistration lag, so no new requests arrive after the listener closes.
- Set `shutdown_delay: 0` only when nothing routes by readiness.

## Reverse proxy

### Preserve Host

CSRF protection compares the browser's `Origin` with the request's `Host`, so the proxy must pass the original `Host` header through (nginx: `proxy_set_header Host $host;`). If the UI is reached under a different origin than the Host asynqui sees, list that origin in `server.trusted_origins`.

### Sub-path

With `server.base_path: /asynq`, forward `/asynq/...` **without** stripping the prefix. asynqui serves every route, including probes and static files, under the prefix.

### `auth.provider: proxy`

asynqui trusts identity headers only when the TCP peer is in `auth.proxy.trusted_proxies` ([requirements §3](requirements.md#proxy)). Both of the following are required:

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

   Check that your CNI still lets the kubelet's probes through.

### `auth.provider: basic`

The login rate limit ([requirements §3](requirements.md#basic)) counts per TCP peer address as well as per username. Behind a proxy every user shares the proxy's address, so one user's failed attempts can lock out the others.

## Cookies and TLS

- Serve over HTTPS. Session cookies are `Secure` unless `auth.session.cookie_secure: false`, which is only for local plain HTTP.
- Generate keys with `openssl rand -hex 32` and pass them through environment variables (`hash_key: ${ASYNQUI_SESSION_HASH_KEY}`).
- Sessions cannot be revoked server-side. To sign everyone out, rotate the keys without keeping the old pair in `previous_keys`. To rotate without signing anyone out, move the old pair to `previous_keys` and remove it after `ttl`.

## `auth.provider: none`

There is no login, and everyone gets `authz.default`. Put asynqui behind something that authenticates, or bind it to `127.0.0.1`.

## Update check

A release build asks GitHub for the latest release at startup and then daily, and shows a dot next to the version in the sidebar when a newer one exists. What it sends and when is in [requirements §7](requirements.md#update-check).

- The server makes the request. It needs egress to `api.github.com:443` and honours `HTTPS_PROXY`.
- Failures are logged at debug level and change nothing.
- Set `update_check: false` to disable it, e.g. where egress is restricted.
