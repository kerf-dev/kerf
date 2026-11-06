<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Authentication endpoints

Login, logout and the caller's identity.

| Endpoint | Summary |
|---|---|
| [`POST /v1/auth/login`](#login) | Log in with a local user |
| [`POST /v1/auth/logout`](#logout) | End the current session |
| [`GET /v1/auth/providers`](#getAuthProviders) | How the sign-in page offers to sign in |
| [`GET /v1/auth/oidc/login`](#oidcLogin) | Start a sign-in with the identity provider |
| [`GET /v1/auth/oidc/callback`](#oidcCallback) | The provider's redirect back after sign-in |
| [`GET /v1/me`](#getMe) | The caller's identity, role and CSRF token |
| [`POST /v1/me/password`](#changeOwnPassword) | Change the caller's own password |

### Log in with a local user { #login }

`POST /v1/auth/login`

Accepts: no authentication.

Checks the username and password and starts a session. The response sets the session
cookie `kerf_session` (`HttpOnly; SameSite=Strict; Path=/`, plus `Secure` behind TLS) and
returns the CSRF token the client sends as `X-CSRF-Token` on every write made with the
session. Every other `/v1` endpoint needs a session or a bearer token.

A wrong password and an unknown, disabled or locked user all get the same `401` (code
`invalid_credentials`), so the answer never tells whether a user exists. After repeated
failures a user is locked for a while; while locked even the right password gets that
`401`. Attempts are rate limited per username and per client address (the last
`X-Forwarded-For` entry, else the connection): `429` code `rate_limited` with
`Retry-After` and one message for both.

**Request body** (`application/json`, required): [LoginRequest](schemas.md#loginrequest)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Logged in. | [Me](schemas.md#me) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `429` | Too many attempts (code `rate_limited`). | [Problem](schemas.md#problem) |

### End the current session { #logout }

`POST /v1/auth/logout`

Accepts: no authentication.

Revokes the session and clears the cookie. A live session needs its `X-CSRF-Token`
(`403` `csrf_invalid` without it). Without a live session (none, expired or revoked) it
only clears the cookie. Always `204` otherwise; returns no data.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Logged out. |  |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### How the sign-in page offers to sign in { #getAuthProviders }

`GET /v1/auth/providers`

Accepts: no authentication.

Whether every local user may sign in with a password (`local`; false = only the
break-glass admin) and the identity provider, when one is configured.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The sign-in options. | [AuthProviders](schemas.md#authproviders) |

### Start a sign-in with the identity provider { #oidcLogin }

`GET /v1/auth/oidc/login`

Accepts: no authentication.

Redirects the browser to the provider (OpenID Connect authorization code flow with PKCE,
`state` and `nonce`) and sets the `kerf_oidc` cookie (`HttpOnly; SameSite=Lax;
Path=/v1/auth/oidc`, 10 minutes) that binds the sign-in to this browser. When the provider
cannot be reached it redirects to `/login?error=oidc_unavailable` instead.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `next` | query | string | Path to return to after sign-in (same site only; anything else gives `/`). |

**Responses**

| Status | Description | Body |
|---|---|---|
| `302` | To the provider, or to the sign-in page with an error. |  |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `429` | Too many attempts (code `rate_limited`). | [Problem](schemas.md#problem) |

### The provider's redirect back after sign-in { #oidcCallback }

`GET /v1/auth/oidc/callback`

Accepts: no authentication.

Needs the `kerf_oidc` cookie of the browser that started the sign-in and its one-time
`state`. Exchanges the code (with the PKCE verifier), verifies the ID token and its nonce,
maps the groups to a role, creates or updates the user and starts a session (cookie
`kerf_session`, as `POST /v1/auth/login`), then redirects to `next`. On failure it redirects
to `/login?error=<code>`: `oidc_expired` (unknown, used or expired state; another browser),
`oidc_failed` (the provider refused or the exchange failed), `oidc_denied` (no mapped group
and no default role, or a disabled user), `oidc_conflict` (the username belongs to a local
user), `oidc_unavailable`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `code` | query | string |  |
| `state` | query | string |  |
| `error` | query | string | Set by the provider when the sign-in failed there. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `302` | To `next` with a session cookie, or to the sign-in page with an error. |  |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### The caller's identity, role and CSRF token { #getMe }

`GET /v1/me`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

For a session: the user, the role and the CSRF token. For a bearer token (`ui` or `admin`):
the token's role mapped to a user role (`ui` → `viewer`, `admin` → `admin`) and no CSRF
token (bearer requests need none). The UI uses it to decide what to show.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Identity. | [Me](schemas.md#me) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Change the caller's own password { #changeOwnPassword }

`POST /v1/me/password`

Accepts: session of a user with role `viewer` or higher.

Requires the current password. Clears `mustChangePassword`. Other sessions of the user are
revoked; the current one stays.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PasswordChange](schemas.md#passwordchange)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Password changed. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `429` | Too many attempts (code `rate_limited`). | [Problem](schemas.md#problem) |
