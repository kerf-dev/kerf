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
| [`GET /v1/auth/saml/metadata`](#samlMetadata) | Kerf's SAML service provider metadata |
| [`GET /v1/auth/saml/login`](#samlLogin) | Start a sign-in with the SAML identity provider |
| [`POST /v1/auth/saml/acs`](#samlACS) | The assertion consumer service (the IdP's response) |
| [`GET /v1/auth/saml/finish`](#samlFinish) | Start the session of a verified SAML sign-in |
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
break-glass admin) and the identity providers that are configured (`oidc`, `saml`).

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

### Kerf's SAML service provider metadata { #samlMetadata }

`GET /v1/auth/saml/metadata`

Accepts: no authentication.

The SP metadata to import at the SAML identity provider: the entity ID
(`auth.saml.entityID`, default this URL), the assertion consumer service (HTTP-POST binding,
`auth.saml.acsURL`), the requested NameID format and, with an SP key, the certificate that
signs the authentication requests. `404` when SAML sign-in is not configured.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The metadata (an `EntityDescriptor`). | string |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Start a sign-in with the SAML identity provider { #samlLogin }

`GET /v1/auth/saml/login`

Accepts: no authentication.

Redirects the browser to the IdP's single sign-on service (HTTP-Redirect binding) with an
`AuthnRequest` (signed when an SP key is configured) and a random `RelayState`, and sets the
`kerf_saml` cookie (`HttpOnly; SameSite=Lax; Path=/v1/auth/saml`, 10 minutes) that holds the
same RelayState and binds the sign-in to this browser. When the IdP's metadata cannot be read
it redirects to `/login?error=saml_unavailable` instead.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `next` | query | string | Path to return to after sign-in (same site only; anything else gives `/`). |

**Responses**

| Status | Description | Body |
|---|---|---|
| `302` | To the IdP, or to the sign-in page with an error. |  |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `429` | Too many attempts (code `rate_limited`). | [Problem](schemas.md#problem) |

### The assertion consumer service (the IdP's response) { #samlACS }

`POST /v1/auth/saml/acs`

Accepts: no authentication.

The IdP posts its `SAMLResponse` here with the `RelayState` of a sign-in started at
`/v1/auth/saml/login` (HTTP-POST binding; IdP-initiated sign-ins are refused). The hub
verifies the XML signature (the assertion must be signed unless
`auth.saml.requireSignedAssertions` is false; then a signed response suffices), the issuer, the
audience, the destination, `InResponseTo`, `NotBefore` and `NotOnOrAfter` (with
`auth.saml.clockSkew`), and records the assertion ID so it is accepted once by every replica.
It stores the identity and redirects (`303`) to `/v1/auth/saml/finish`; it starts no session
itself. On failure it redirects to `/login?error=<code>`: `saml_expired` (unknown, used or
expired RelayState), `saml_failed` (an invalid, unsigned or replayed response, or the IdP
refused), `saml_unavailable`.

**Request body** (`application/x-www-form-urlencoded`, required): object

| Field | Type | Constraints | Description |
|---|---|---|---|
| `SAMLResponse` | string, required |  | The base64-encoded `samlp:Response`. |
| `RelayState` | string, required |  |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `303` | To `/v1/auth/saml/finish`, or to the sign-in page with an error. |  |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `429` | Too many attempts (code `rate_limited`). | [Problem](schemas.md#problem) |

### Start the session of a verified SAML sign-in { #samlFinish }

`GET /v1/auth/saml/finish`

Accepts: no authentication.

Needs the `kerf_saml` cookie of the browser that started the sign-in: takes its verified
identity (once), maps the groups to a role, creates or updates the user (`source: saml`) and
starts a session (cookie `kerf_session`, as `POST /v1/auth/login`; it ends at `sessionTTL` or
at the IdP's `SessionNotOnOrAfter`, whichever is first), then redirects to `next`. On failure
it redirects to `/login?error=<code>`: `saml_expired` (no answered sign-in for this browser),
`saml_denied` (no mapped group and no default role, or a disabled user; the user's other
sessions end), `saml_conflict` (the username belongs to a local user or to a user of the other
identity provider).

**Responses**

| Status | Description | Body |
|---|---|---|
| `302` | To `next` with a session cookie, or to the sign-in page with an error. |  |
| `303` | To the sign-in page with an error. |  |
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
