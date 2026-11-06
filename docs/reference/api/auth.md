<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Authentication endpoints

Login, logout and the caller's identity.

| Endpoint | Summary |
|---|---|
| [`POST /v1/auth/login`](#login) | Log in with a local user |
| [`POST /v1/auth/logout`](#logout) | End the current session |
| [`GET /v1/me`](#getMe) | The caller's identity, role and CSRF token |
| [`POST /v1/me/password`](#changeOwnPassword) | Change the caller's own password |

### Log in with a local user { #login }

`POST /v1/auth/login`

Accepts: no authentication.

Checks the username and password and starts a session. The response sets the session
cookie `kerf_session` (`HttpOnly; SameSite=Strict; Path=/`, plus `Secure` behind TLS) and
returns the CSRF token the client sends as `X-CSRF-Token` on every write made with the
session. Failed logins are rate limited per client address and per user; after repeated
failures the user is locked for a while (`429` with `Retry-After`, code `account_locked`).
A wrong username and a wrong password give the same `401` (code `invalid_credentials`).
A disabled user gets `401` too.

**Request body** (`application/json`, required): [LoginRequest](schemas.md#loginrequest)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Logged in. | [Me](schemas.md#me) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `429` | Too many failed attempts (code `rate_limited` or `account_locked`). | [Problem](schemas.md#problem) |

### End the current session { #logout }

`POST /v1/auth/logout`

Accepts: session of a user with role `viewer` or higher.

Revokes the session and clears the cookie. Idempotent.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Logged out. |  |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

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
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

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
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `429` | Too many failed attempts (code `rate_limited` or `account_locked`). | [Problem](schemas.md#problem) |
