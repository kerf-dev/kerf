# Sign-in and access

Kerf shows a map of your production clusters: workloads, namespaces, costs and who changed what. So
**signing in is always required**. A browser without a session sees only the sign-in page, and the API
answers `401` to everything except sign-in itself.

- [How it works](#how-it-works)
- [Users and roles](#users-and-roles)
- [Sign-in protection](#sign-in-protection)
- [Automation: bearer tokens](#automation-bearer-tokens)
- [Behind a load balancer](#behind-a-load-balancer)
- [Upgrading from anonymous read access](#upgrading-from-anonymous-read-access)
- [Troubleshooting](#troubleshooting)

## How it works

```
browser ──(kerf_session cookie only)──► web (nginx) ──(the cookie, nothing else)──► hub
curl / CI / agents ──(Authorization: Bearer <token>)─────────────────────────────► hub
```

- **Sign-in.** `POST /v1/auth/login` checks the username and password and sets the session cookie
  `kerf_session` (`HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure` behind TLS). The hub stores only a
  SHA-256 of the session token. Every write made with a session also needs the `X-CSRF-Token` header,
  which the UI gets at sign-in.
- **The web proxy holds no token.** nginx forwards only the `kerf_session` cookie to the hub. It never
  forwards a browser's `Authorization` header and never adds credentials of its own, so a visitor without
  a session reads nothing.
- **The UI checks the session first.** Until `GET /v1/me` answers, the UI shows a neutral loading screen:
  no navigation, no numbers, no requests for data. Without a session it shows the sign-in page, and after
  sign-in it returns to the page you asked for (`/login?next=/opportunities`). `next` only accepts paths of
  the same site; a link to another site goes to the overview instead.
- **Session end.** A session lasts `hub.auth.sessionTTL` (default `12h`). When it expires or is revoked
  (sign-out in another tab, a password change, an admin disabling the user), the next request, or at the
  latest the check at the session's end time, brings the UI back to the sign-in page with the notice
  "Your session ended". After sign-in you are back on the page you were on.
- **Sign-out** revokes the session on the hub and clears the cookie, also when the session had already
  ended.
- **Every `/v1` endpoint needs credentials**, a session or a bearer token, except sign-in and sign-out.
  The probes (`/healthz`, `/readyz`) and `/metrics` are outside `/v1`. A test in the hub
  (`internal/hub/server_routes_test.go`) lists every route and fails when a new one answers without
  credentials.

## Users and roles

| Role | Can (each includes the ones above) |
|---|---|
| `viewer` | read everything the UI shows: workloads, recommendations, savings, clusters, policies, settings, the audit log |
| `operator` | workload overrides and exclusions, approve once, pause a cluster or the fleet, impact previews |
| `admin` | defaults and policies, recommender and pricing settings, clusters, tokens, users |

- **The first admin** is created on an empty database from a Secret (`hub.auth.bootstrapAdmin`, see
  [Install on Amazon EKS](install/eks.md), section 4.5). Change its password after the first sign-in.
- **More users:** *Admin → Access → Users*. A new user gets a temporary password and must change it at the
  first sign-in. An admin can disable a user (their sessions end at once) or reset a password.
- Everyone who needs to see Kerf needs a user, at least a `viewer`.

## Sign-in protection

- **One answer for every failure.** A wrong password, an unknown user, a disabled user and a locked user
  all get `401` `invalid_credentials`, "Wrong username or password.", after the same password-hashing
  work. Nothing in the answer or its timing tells whether a username exists.
- **Lockout.** After `hub.auth.maxFailedLogins` failures in a row (default 5) a user is locked for
  `hub.auth.lockoutDuration` (default `15m`). While locked, even the right password fails with the same
  answer. An admin's password reset unlocks the user.
- **Rate limits.** At most `hub.auth.loginRateLimit` sign-in attempts per minute per username (default 10)
  and three times that per client address. Beyond them the hub answers `429` `rate_limited`, "Too many
  sign-in attempts. Try again later.", with `Retry-After`. The limits are counted by each hub replica.

## Automation: bearer tokens

Scripts, CI jobs, the pricing service and the agents call the hub API with a bearer token, unchanged:

```bash
curl -sS -H "Authorization: Bearer $KERF_TOKEN" https://<hub>/v1/workloads?limit=10
```

Create tokens in *Admin → Access → Tokens* (shown once). Roles: `ui` (read-only, counts as `viewer`),
`admin`, `pricing`, and `agent` (one cluster). Call the hub directly (its Service or ingress); the web
proxy is for browsers and does not forward `Authorization`.

## Behind a load balancer

The hub limits sign-in attempts per client address. It takes the address from the **last**
`X-Forwarded-For` entry, the one added by the proxy right in front of it; entries a client sends itself are
ignored. The web nginx replaces `X-Forwarded-For` with the client it sees.

Behind an ALB or an ingress controller that client is the load balancer, so all users would share one
limit. List the load balancer's addresses in the web chart's `trustedProxies` (the kerf chart:
`web.trustedProxies`), and nginx takes the client from the entry the load balancer appends:

```yaml
web:
  trustedProxies: [10.0.0.0/16]   # the ALB subnets, or the VPC CIDR
```

Keep the range as narrow as you can: a client inside a trusted range that reaches nginx directly could
choose its own address.

## Upgrading from anonymous read access

Earlier versions showed the whole UI read-only to visitors without a session: the web proxy added a
shared `ui` token (`web.auth.anonymousRead: true`, the default). That is gone, with no switch to bring it
back.

1. **Give every reader a user.** People who used the UI without signing in need a `viewer` user
   (*Admin → Access → Users*).
2. **Remove the old values.** Delete `web.auth.anonymousRead` and `web.token` (with the component chart:
   `auth.anonymousRead` and `token`) from your values. The chart refuses to render while they are set:

   ```
   Error: execution error at (kerf/charts/web/templates/deployment.yaml:2:4): auth.anonymousRead
   (web.auth.anonymousRead in the kerf chart) was removed: Kerf always requires sign-in ...
   ```
3. **Clean up the token.** Revoke the `ui` token the proxy used in *Admin → Access → Tokens*, unless a
   script uses it. Delete its secret (`kerf/hub/ui-token` in Secrets Manager with the EKS guide); the
   chart no longer creates the `<web>-hub-token` Secret or ExternalSecret.
4. **Behind an ALB,** set `web.trustedProxies` (see above).

## Troubleshooting

- **Every page shows the sign-in page right after signing in.** The session cookie does not come back.
  Behind plain HTTP the cookie must not be `Secure`: `hub.auth.cookieSecure: false` (development only).
  Behind TLS, check that nothing strips `Set-Cookie` or the `Cookie` header between the browser and nginx.
- **"Too many sign-in attempts" for everyone.** All users share one client address: set
  `web.trustedProxies`.
- **"Cannot reach the hub".** The UI could not check the session: the hub is down, or nginx cannot
  resolve `web.hub.upstream`. The UI shows nothing else until it can.
- **A script gets `401`.** It needs a bearer token, sent to the hub directly (not through the web proxy).
