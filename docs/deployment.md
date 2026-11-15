# Deploying and operating asynqui

What the deployment around asynqui must get right. Flags, environment variables and every config key are in [configuration.md](configuration.md); providers, sessions and roles in [authentication.md](authentication.md).

## Install

### Binary archives

Each release on [GitHub Releases](https://github.com/BarkinBalci/asynqui/releases) has static binaries for linux, darwin and windows on amd64 and arm64, as archives (`tar.gz`, `zip` on Windows) with LICENSE, README.md and config.example.yaml, plus `checksums.txt` (SHA-256). Check an archive before unpacking it:

```sh
sha256sum --ignore-missing -c checksums.txt
```

Tags with a prerelease part (`0.2.0-rc.1`) are published as GitHub prereleases.

### Container image

`ghcr.io/barkinbalci/asynqui` is multi-arch (linux/amd64, linux/arm64) and built on `gcr.io/distroless/static-debian12:nonroot`. Tags: the exact version (`0.1.0`), `0.1` and `latest`; prereleases get only their exact version. Pin the exact version in production.

- Runs as UID/GID 65532 and listens on 8080.
- Reads its config from `/etc/asynqui/config.yaml` (`ASYNQUI_CONFIG`; `-config` overrides it). Mount the file read-only.
- UID 65532 must be able to read it: mode `0644`, or `0640` with group 65532. On SELinux hosts add `:z` to the bind mount. For a Kubernetes Secret with a restrictive `defaultMode`, set `fsGroup: 65532`.
- Flags go after the image name, e.g. `docker run ... ghcr.io/barkinbalci/asynqui:0.1.0 -log-level debug`.
- There is no shell, so there is no Docker `HEALTHCHECK`. Use the HTTP probes.

## Kubernetes example

[oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) runs as a sidecar in the same pod and signs users in; asynqui uses `auth.provider: proxy` and listens on loopback, so only the sidecar can reach it. The Service and the probes go through the sidecar, which lets `/health/` through without sign-in. Secrets come from the environment through interpolation (`${NAME}`), so the ConfigMap holds no secrets.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: asynqui
data:
  config.yaml: |
    server:
      addr: 127.0.0.1:8080
      shutdown_delay: 5s
    connections:
      - name: prod
        addr: redis.prod.svc.cluster.local:6379
        password: ${REDIS_PASSWORD}
    auth:
      provider: proxy
      proxy:
        user_header: X-Forwarded-Email
        groups_header: X-Forwarded-Groups
        trusted_proxies: ["127.0.0.1/32"]
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
      initContainers:
        # A native sidecar: it starts first and stops after asynqui.
        - name: oauth2-proxy
          image: quay.io/oauth2-proxy/oauth2-proxy:v7.15.6
          restartPolicy: Always
          args:
            - --http-address=0.0.0.0:4180
            - --upstream=http://127.0.0.1:8080/
            - --provider=oidc
            - --oidc-issuer-url=https://login.example.com/realms/acme
            - --client-id=asynqui
            - --redirect-url=https://asynqui.example.com/oauth2/callback
            - --email-domain=example.com
            - --skip-auth-route=GET=^/health/
          env:
            - name: OAUTH2_PROXY_CLIENT_SECRET
              valueFrom:
                secretKeyRef: { name: asynqui, key: oidc-client-secret }
            - name: OAUTH2_PROXY_COOKIE_SECRET
              valueFrom:
                secretKeyRef: { name: asynqui, key: cookie-secret }
          ports:
            - name: http
              containerPort: 4180
          resources:
            requests: { cpu: 10m, memory: 32Mi }
            limits: { memory: 128Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: [ALL] }
      containers:
        - name: asynqui
          image: ghcr.io/barkinbalci/asynqui:0.1.0
          env:
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef: { name: asynqui, key: redis-password }
          volumeMounts:
            - name: config
              mountPath: /etc/asynqui
              readOnly: true
          livenessProbe:
            httpGet: { path: /health/liveness, port: 4180 }
          readinessProbe:
            httpGet: { path: /health/readiness, port: 4180 }
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
      targetPort: 4180
```

- Native sidecars (`restartPolicy: Always` on an init container) need Kubernetes 1.29 or later.
- oauth2-proxy replaces the `X-Forwarded-*` identity headers it sets, so a client cannot send its own.
- The probes and the Service use the sidecar's port, 4180, so they reach asynqui through the sidecar.

## Probes

Both need no auth, return `text/plain` (`ok` or the reason) with `Cache-Control: no-store`, live under `server.base_path` and are excluded from request logs. With `base_path: /asynq` they are `/asynq/health/liveness` and `/asynq/health/readiness`.

- `GET <base>/health/liveness`: 200 whenever the process can serve HTTP. No dependency checks.
- `GET <base>/health/readiness`: 200 while serving, 503 `shutting down` from the start of shutdown until exit. There is no "starting" state, because the listener opens only after [startup](configuration.md#startup) succeeds.

Neither depends on Redis, so a Redis outage does not unroute or restart replicas.

## Graceful shutdown

On the first SIGINT or SIGTERM:

1. Signal handling is reset, so a **second signal force-quits** immediately.
2. Readiness turns 503 `shutting down`. This is terminal.
3. Wait `server.shutdown_delay` (default 5s; `0` skips the wait). The listener stays open and serves normally, so load balancers see the 503 and stop routing first.
4. Shut down with a **10s** timeout: the listener closes and in-flight requests finish. Request contexts are not canceled by the signal.
5. If the timeout expires, remaining connections are force-closed and the process exits 1.

- **`terminationGracePeriodSeconds` must exceed `shutdown_delay` + 10s.** If you raise `shutdown_delay`, raise the grace period too.
- `shutdown_delay` should be at least readiness `periodSeconds` × `failureThreshold` plus your load balancer's deregistration lag, so no new requests arrive after the listener closes.
- Set `shutdown_delay: 0` only when nothing routes by readiness.

## Reverse proxy

### Preserve Host

Cross-origin protection compares the browser's `Origin` with the request's `Host` on every unsafe method, so the proxy must pass the original `Host` header through (nginx: `proxy_set_header Host $host;`). If the UI is reached under a different origin than the Host asynqui sees, list that origin in `server.trusted_origins`. An invalid origin there fails startup.

### Sub-path

With `server.base_path: /asynq`, forward `/asynq/...` **without** stripping the prefix. asynqui serves every route, including probes and static files, under the prefix.

### Timeouts

asynqui's server timeouts are: read header 10s, read 30s, write 60s, idle 120s. A response never takes longer than the write timeout, so a proxy read timeout of 60s or more never cuts one off. Keep the proxy's idle keep-alive timeout towards asynqui below 120s.

## TLS

Serve over HTTPS. Cookies are `Secure` unless `auth.session.cookie_secure: false`, which is only for local plain HTTP ([Cookies](authentication.md#cookies)).

## Security headers

asynqui sends a strict Content-Security-Policy with `frame-ancestors 'none'`, so the UI cannot be embedded in an iframe. Don't inject or override the CSP at the proxy.

## Update check

A release build asks GitHub for the latest release at startup and then daily, and shows a dot next to the version in the sidebar when a newer one exists.

- With `update_check: true` and a release version, the server sends `GET https://api.github.com/repos/BarkinBalci/asynqui/releases/latest` at startup and then every 24h, until shutdown starts. A release version is a semantic version without a prerelease part: `dev`, release candidates (`0.2.0-rc.1`), `git describe` or dirty builds (`v0.1.0-3-gabc1234-dirty`) and pseudo-versions never check.
- The request sends no credentials, config or user data. GitHub sees the server's IP address (or its proxy's) and the asynqui version, from `User-Agent: asynqui/{version}`. It is server-side, so the CSP is unchanged.
- It needs egress to `api.github.com:443` and honours `HTTPS_PROXY`.
- It times out after 5s. A failure (network, non-200, malformed JSON) is logged at debug level, changes nothing and keeps the last result.
- Drafts, prereleases and tags that are not a semantic version without a prerelease part are ignored. A release newer than the running version (by major, minor, patch) is remembered by its tag and logged once at info; an equal or older one clears it. The footer links to `https://github.com/BarkinBalci/asynqui/releases/tag/{tag}`.
- Set `update_check: false` to disable it, e.g. where egress is restricted.
