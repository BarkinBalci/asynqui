# Authentication and access

`auth.provider` sets how users sign in: `none` (default), `basic`, `proxy` or `oidc`. `authz` then gives each user a role per connection. Every key and its default is listed in [configuration.md](configuration.md#keys-and-defaults). Startup fails on any provider error, including OIDC discovery.

## Providers

### `none`

- No login and no session. Every request is anonymous, so bindings never match and everyone gets `authz.default`.
- `authz.default` is **viewer** unless set explicitly. For full control set `authz.default: admin`.
- Startup logs a WARN when the listen address isn't loopback: an empty host, `0.0.0.0`, `::`, a non-loopback IP, or any hostname other than `localhost`.
- Put asynqui behind something that authenticates, or bind it to `127.0.0.1`.

### `basic`

- A login form with a username and password. Users are listed in `auth.basic.users` with **bcrypt** hashes and optional `groups`. Generate a hash with:

  ```sh
  htpasswd -nbBC 12 "" 'secret' | cut -d: -f2
  ```

- All hashes must use the same bcrypt cost, or startup fails, so a failed login takes the same time whichever user was tried.
- Usernames that differ only in ASCII case are rejected at startup.
- bcrypt reads at most 72 bytes. Passwords longer than 72 bytes are rejected at login.
- **Rate limit:** `POST /login` attempts are limited per client IP and per username: a burst of 5, then 1 every 12s. The client IP is the TCP peer address, with IPv6 addresses grouped by /64. Further attempts get 429 and "Too many attempts. Try again later." Behind a proxy every user shares the proxy's address, so one user's failed attempts can lock out the others.
- Failed logins are logged with the username only when it exists. Invalid credentials show "Invalid username or password."
- Every request re-checks the session's user against the current config: a user removed from config is signed out, and groups always come from config.
- Needs [session keys](#sessions).

### `proxy`

- Identity comes from request headers set by a reverse proxy: `user_header` (default `X-Forwarded-User`) and optional `groups_header` (default unset). Groups are split on `,`, trimmed, and empty entries are dropped.
- **Trust** depends only on the TCP peer address, checked against `trusted_proxies`. That list of CIDRs or IPs is required: an empty list fails startup, and a prefix that covers every address, such as `0.0.0.0/0`, logs a WARN. asynqui never reads `X-Forwarded-For`.
- A request from an untrusted IP that carries either identity header gets 401.
- A trusted request with a missing or empty user header gets 401. So does one where the user or groups header appears more than once, which happens when a proxy appends instead of replacing. That case is also logged as a WARN.
- Identity is read on every request. There is no session cookie, so `auth.session` is not needed.
- Sign out is shown only when `logout_url` is set, as a link to it.

Your deployment must get two things right:

1. **Strip client-supplied identity headers at the proxy.** The proxy must remove any incoming `user_header` and `groups_header` from the client request before setting its own. Otherwise a user can send `X-Forwarded-User: admin` through the trusted proxy.

   ```nginx
   # nginx with oauth2-proxy: set the headers, never pass the client's through
   proxy_set_header X-Forwarded-User   $upstream_http_x_auth_request_user;
   proxy_set_header X-Forwarded-Groups $upstream_http_x_auth_request_groups;
   ```

2. **Only the proxy can reach asynqui.** Keep `trusted_proxies` as narrow as possible: a single `/32` (or `/128`) per proxy instance, never a whole node or pod range. On Kubernetes, either run the proxy as a sidecar with asynqui listening on loopback, as in the [Kubernetes example](deployment.md#kubernetes-example), or add a NetworkPolicy so that only the proxy pods can connect:

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

### `oidc`

- Authorization-code flow with PKCE. Register `https://<host><base_path>/auth/callback` at the IdP and set it as `redirect_url`.
- `issuer`, `client_id` and `redirect_url` are required. `client_secret` is optional, for public clients.
- **Scopes:** `openid profile email` by default. `groups` is not a default; add it if your IdP needs it. `openid` is always sent.
- **Username:** the `username_claim` (default `email`). When it is `email`, `email_verified` must be `true`, either the boolean or the string `"true"` that Amazon Cognito sends. Otherwise login fails.
- **Groups:** read from `groups_claim` (default `groups`) in the ID token, falling back to the userinfo endpoint. The claim may be a string or an array of strings. When the IdP signals a groups overage (`_claim_names` and `_claim_sources`), asynqui logs a WARN and treats groups as empty.
- **Session size:** the session keeps only groups referenced by some `authz.bindings` entry. If the encoded cookie would exceed 3800 bytes, login is refused with "Sign-in failed: too many groups for a session cookie."
- Login failures show "Single sign-on failed." on the login page; details go to the log.
- Signing out is local: it clears the session cookie, not the IdP session.
- Needs [session keys](#sessions).

## OIDC setup

1. Register asynqui at the IdP as a web client using the authorization-code flow, with the redirect URL `https://<host><base_path>/auth/callback`. Set the same URL as `redirect_url`.
2. Set `issuer` to the IdP's issuer URL exactly as its discovery document (`<issuer>/.well-known/openid-configuration`) reports it, trailing slash included. asynqui reads it at startup.
3. Choose the scopes, the username claim and the groups claim as below, then bind users or groups to roles.

```yaml
auth:
  provider: oidc
  session:
    hash_key: ${ASYNQUI_SESSION_HASH_KEY}
    block_key: ${ASYNQUI_SESSION_BLOCK_KEY}
  oidc:
    issuer: https://login.example.com/realms/acme
    client_id: asynqui
    client_secret: ${ASYNQUI_OIDC_CLIENT_SECRET}
    redirect_url: https://asynqui.example.com/auth/callback
    scopes: [openid, profile, email]
    username_claim: email
    groups_claim: groups
authz:
  bindings:
    - groups: [platform]
      role: admin
      connections: ["*"]
```

- **Scopes:** keep `email` while `username_claim` is `email`. Add `groups` to `scopes` when the IdP sends groups only for that scope (Dex, for example).
- **Username claim:** a string claim, read from the ID token, or from userinfo when the ID token lacks it. With `email`, login fails unless `email_verified` is true; any other claim is used as is, so pick one users cannot change themselves.
- **Groups claim:** a string or an array of strings, from the ID token or else userinfo. `groups` in bindings match its values exactly. When the IdP signals a groups overage instead of listing them, the user signs in with no groups and asynqui logs a WARN.
- **Session size:** the session cookie keeps only the groups that some binding names. If those still do not fit in 3800 bytes, login is refused with "Sign-in failed: too many groups for a session cookie."

### Keycloak

The issuer is `https://<host>/realms/<realm>`. Groups are not in tokens by default: add a **Group Membership** mapper to the client's dedicated scope, with token claim name `groups` and "Add to ID token" on. Turn off "Full group path" to get `platform` instead of `/platform`. `email_verified` is the user's "Email verified" flag.

### Microsoft Entra ID

The issuer is `https://login.microsoftonline.com/<tenant-id>/v2.0`. ID tokens often lack `email_verified`, so set `username_claim` to `preferred_username` (or another claim you bind on). Add the groups claim under the app's **Token configuration**; it carries group object IDs, so bind those. A user in more groups than fit in a token gets an overage, so no groups in asynqui; emitting only groups assigned to the application keeps the list short.

### Google

The issuer is `https://accounts.google.com`, and the default `username_claim: email` works. Google sends no groups claim, so bind by `users`.

## oauth2-proxy setup

[oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) signs users in and forwards requests to asynqui with `X-Forwarded-User`, `X-Forwarded-Email`, `X-Forwarded-Groups` (comma-separated) and `X-Forwarded-Preferred-Username`. It removes those headers from the client's request first. Run it next to asynqui, as in the [Kubernetes example](deployment.md#kubernetes-example):

```yaml
server:
  addr: 127.0.0.1:8080
auth:
  provider: proxy
  proxy:
    user_header: X-Forwarded-Email
    groups_header: X-Forwarded-Groups
    trusted_proxies: ["127.0.0.1/32"]
    logout_url: /oauth2/sign_out
```

```sh
oauth2-proxy \
  --http-address=0.0.0.0:4180 \
  --upstream=http://127.0.0.1:8080/ \
  --provider=oidc \
  --oidc-issuer-url=https://login.example.com/realms/acme \
  --client-id=asynqui \
  --redirect-url=https://asynqui.example.com/oauth2/callback \
  --email-domain=example.com \
  --skip-auth-route='GET=^/health/'
```

- Pass the client secret and the cookie secret as `OAUTH2_PROXY_CLIENT_SECRET` and `OAUTH2_PROXY_COOKIE_SECRET`. Generate the cookie secret with `openssl rand -base64 32 | tr -- '+/' '-_'`.
- At the IdP, register `https://<host>/oauth2/callback`, oauth2-proxy's own callback.
- Groups come from the `groups` claim (`--oidc-groups-claim`). When the IdP needs the `groups` scope for it, add `--scope='openid email profile groups'`.
- `--skip-auth-route` lets the probes through without sign-in. Adjust it to `^<base_path>/health/` with a `base_path`.
- Sign out (`/oauth2/sign_out`) clears oauth2-proxy's cookie only; the IdP session stays.

## Sessions

`basic` and `oidc` keep the signed-in user in the signed and encrypted `asynqui_session` cookie ([Cookies](#cookies)).

- **Keys** are hex in config: `hash_key` decodes to 32 or 64 bytes, `block_key` to 32 bytes. Both are validated at startup and never auto-generated. Generate each with `openssl rand -hex 32` and pass them through environment variables (`hash_key: ${ASYNQUI_SESSION_HASH_KEY}`).
- **TTL:** `auth.session.ttl` (default 12h), counted from sign-in. Activity doesn't extend a session.
- **`cookie_secure`** (default `true`) makes every cookie `Secure`. Set it to `false` only for local plain HTTP.
- **No server-side revocation.** Signing out clears the cookie in that browser only. For `basic`, removing a user from config also signs them out.
- **Rotation:** `previous_keys`, a list of `{hash_key, block_key}`, still decode existing cookies. New cookies always use the current pair.
  - To sign everyone out, rotate the keys without keeping the old pair in `previous_keys`.
  - To rotate without signing anyone out, move the old pair to `previous_keys` and remove it after `ttl`.

## Roles and access

Roles are ordered **viewer < operator < admin** and scoped per connection. `none` as a role means no access.

```yaml
authz:
  default: none            # role for every user on every connection; see defaults below
  bindings:
    - groups: [platform]
      role: admin
      connections: ["*"]
    - users: [jdoe@example.com]
      role: operator
      connections: ["billing", "main*"]
```

- **Default:** `authz.default` applies to every identified user on every connection.
  - `auth.provider: none`: defaults to **viewer**.
  - `basic`, `proxy`, `oidc`: defaults to **none**, so access comes only from bindings.
- **Matching:** a binding matches if the username is in `users` or the user has a group in `groups`. Usernames compare case-insensitively for ASCII letters only, so look-alikes such as the Kelvin sign don't match `k`. Groups must match exactly, case included. `connections` takes globs in Go's `path.Match` syntax: `*`, `?`, `[a-z]`.
- **Effective role:** for each connection, the highest role among the matching bindings and `default`.
- **Startup validation:** the binary fails to start on an unknown role, an empty `connections` list, an invalid glob, or a binding with neither users nor groups. A binding's `role` cannot be `none`.
- **Hidden connections:** a connection where the user's effective role is none is invisible to them everywhere (sidebar, Overview, counts). Its URLs return 404, not 403.
- **Server enforcement:** every action is checked on the server. An insufficient role returns 403.

| Capability | viewer | operator | admin |
|---|---|---|---|
| View every page | ✓ | ✓ | ✓ |
| Pause / resume queue | | ✓ | ✓ |
| Run / archive tasks (single, bulk, "all", all of an aggregation group) | | ✓ | ✓ |
| Cancel active tasks (single, bulk) | | ✓ | ✓ |
| **Delete tasks** (single, bulk, "all", all of an aggregation group) | | | ✓ |
| **Delete queue** | | | ✓ |

## Cookies

Every cookie is `SameSite=Lax` and `Secure` unless `auth.session.cookie_secure: false`. `<base>` is `server.base_path`. When it is empty, a path of `<base>` means `/`.

| Cookie | Path | Lifetime | Purpose |
|---|---|---|---|
| `asynqui_session` | `<base>` | `auth.session.ttl` (12h), absolute | The signed-in user (`basic`, `oidc`). Signed and encrypted, `HttpOnly`. |
| `asynqui_oidc` | `<base>/auth/callback` | 10 minutes | A sign-in in progress (`oidc`): state, nonce, PKCE verifier and the return path. Encrypted, `HttpOnly`. |
| `asynqui_theme` | `<base>` | 1 year | The theme choice. Readable by the page's script. |
| `asynqui_refresh` | `<base>` | 1 year | The auto-refresh choice. Readable by the page's script. |
| `asynqui_flash` | `<base>/c/{conn}` | 60s | The failures of an action posted without JS, shown once after its redirect. Display text only, not signed, `HttpOnly`. |
