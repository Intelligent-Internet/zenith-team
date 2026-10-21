---
title: Authentication and account
summary: Signing in, the session transports, email verification, Google OAuth, desktop handoff, and the caller's own user record
---

Everything that mints, rotates or destroys a Zenith Team credential, plus the user
record behind it — most of this surface is public by necessity, because it is
what an anonymous browser or a freshly installed desktop app calls before it
has anything to authenticate with.

Nothing here is company-scoped, so the 404-instead-of-403 rule does not apply.
Four operations need a credential: `GET /me`, `PATCH /me/profile`,
`DELETE /me`, and `POST /auth/desktop/handoff/start` — and the last one needs
specifically a **cookie** session.

> **Added.** Signup, login, verification and the desktop completions carry
> `result`, the discriminator a client branches on (`"session"` or
> `"pending_verification"`); `POST /auth/refresh` does not.
> The session carries `id` and `issuedAt`, so a client can tell
> whether an `auth.session_revoked` event is about *itself* and can schedule its
> refresh off the server's clock instead of its own. The user carries
> `emailVerifiedAt`, `hasPassword` and `linkedProviders` — the three facts that
> answer "how can this account sign in", none of which had a wire field. The
> pending-verification body carries `attemptsRemaining` and `maxAttempts`, so a
> client can warn before the last wrong code locks the flow forever. A provider
> entry carries `webEnabled` and `desktopEnabled`, because `enabled` alone made
> the desktop app draw a button that answers `503`. Each is explained where it
> is returned.

## Call budget

**Signing in is one request.**

It was two. The client followed a successful `POST /auth/login` with
`GET /companies?limit=1`, purely to decide whether to land on onboarding or on
a company. Every sign-in response now carries the caller's complete company
collection, so that read is gone. Consumers: the web sign-in screen, the desktop
first-launch flow, and desktop handoff redemption. **Before 2, after 1.**

`POST /auth/refresh` deliberately does *not* carry `companies` — see below.

## The session envelope

Every JSON success that mints a session — immediate signup, login, email
verification, desktop OAuth completion, and handoff completion — answers with
`AuthSuccess`. Refresh answers with the smaller `SessionRotation` form: the
same `user` and `session` slots, without `companies`.

| Slot | Carries |
|---|---|
| `result` | `"session"` on every `AuthSuccess`; `"pending_verification"` on the pending body. The branch key the `AuthResult` union discriminates on. Absent from `POST /auth/refresh` (`SessionRotation`) |
| `user` | The `User` entity, byte-identical to what `GET /me` returns |
| `session` | A discriminated union on `transport` |
| `companies` | The complete `GET /api/v1/companies` collection — same envelope and order. Absent from `POST /auth/refresh` |

The browser OAuth callback is the deliberate transport exception. It mints a
cookie session, sets the four cookies, and answers **303 with no body**; the
browser follows `Location` and then reads its authenticated state. The desktop
OAuth and desktop handoff completions are JSON operations and return the full
bearer `AuthSuccess`.

Three slots plus the `result` discriminator; the slots are co-equal, so each
is named — a composite, not a wrapper around one entity. Everything inside them is flat. Nothing in this file
groups scalars behind a key: the phone pair is `phoneCountryIso2` and
`phoneNumber` rather than a `phone` object, and the session's own fields sit
directly on it rather than under a `tokens` or `expiry` sub-object. The only
nesting in the whole file is the `session` value object a client holds whole.

The `session` branch is chosen by the `client` field the caller sent
(`"web"` → cookie, `"desktop"` → bearer), not by anything about the account.

**Branch on `session.transport`. The token fields do not exist in the cookie
branch — they are absent, not null, not empty.**

`"client": "web"` — the tokens are in four `Set-Cookie` headers, never in the
body:

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 3,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-14T17:03:01.442119000Z"
  },
  "session": {
    "transport": "cookie",
    "id": "0197f58e-7d31-7a02-9e44-52c9f1b7c308",
    "issuedAt": "2026-08-16T10:12:31.114882000Z",
    "accessExpiresAt": "2026-08-16T10:27:31.114882000Z",
    "refreshExpiresAt": "2026-08-23T10:12:31.114882000Z"
  },
  "companies": {
    "items": [
      {
        "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
        "name": "Northwind Robotics",
        "slug": "northwind-robotics",
        "taskPrefix": "NWR",
        "businessContext": "Warehouse automation for mid-size distributors.",
        "contactEmail": "ops@northwind.example",
        "phoneCountryIso2": "US",
        "phoneNumber": "4155550142",
        "role": "owner",
        "revision": 7,
        "createdAt": "2026-07-12T03:04:05.123456789Z",
        "updatedAt": "2026-08-14T09:21:00.000000000Z"
      }
    ]
  }
}
```

| Cookie | Path | HttpOnly | Lifetime |
|---|---|---|---|
| `zenith_team_access` | `/` | yes | access TTL, default 15m |
| `zenith_team_refresh` | `/api/v1/auth` | yes | refresh TTL, default 7d |
| `zenith_team_csrf` | `/` | no | access TTL |
| `zenith_team_refresh_csrf` | `/` | no | refresh TTL |

All four are `SameSite=Strict`; `Secure` and `Domain` come from deployment
configuration. The refresh cookie's narrow path is why refresh and logout carry
a *different* CSRF token from every other mutation.

`"client": "desktop"` — the same tokens, in the body, no cookies set:

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 3,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-14T17:03:01.442119000Z"
  },
  "session": {
    "transport": "bearer",
    "id": "0197f58e-9c17-7b45-8d21-6f0a3e5c9147",
    "issuedAt": "2026-08-16T10:12:31.114882000Z",
    "accessToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIwMTk3ZjU4ZS02YTQ0LTdlMTEiLCJleHAiOjE3ODcyMTIwNTF9.Rr8mQ2xV",
    "refreshToken": "brf_Zt8pQ3wV7yB1nM5kC9xF2jH6dL0sA4gR7uT3eW8iO5K",
    "accessExpiresAt": "2026-08-16T10:27:31.114882000Z",
    "refreshExpiresAt": "2026-08-23T10:12:31.114882000Z"
  },
  "companies": { "items": [] }
}
```

The access token is an HS256 JWT and the refresh token is `brf_` followed by
base64url over 32 random bytes; they are the same values the cookie branch puts
in `zenith_team_access` and `zenith_team_refresh`. Treat both as opaque —
`accessExpiresAt` is the supported way to know when to refresh.

`id` is the session's own identifier, and it is **not a credential**: it grants
nothing, it is safe to persist beside the token or in web storage, and it is the
only way a client can recognise itself. Every rotation mints a new session, so
`id` changes on every `POST /auth/refresh` — store the one you were last
handed.

> **Added.** `id` and `issuedAt` on both branches.
>
> `auth.session_revoked` carries `{sessionId, reason}` and uses the typed
> **user** audience, which means every device the user has open receives every
> revocation, including the ones caused by another device's routine refresh
> rotation. Until now no client had ever been told its own session id, so the
> event could only be handled two ways, both wrong: sign out on any revocation,
> which logs a laptop out because a phone refreshed, or ignore the event, which
> leaves a revoked tab live until its next `401`. With `id` the branch is a
> string comparison.
>
> `issuedAt` is the server's clock at the instant the session was minted. A
> client that schedules its refresh from `accessExpiresAt` alone is trusting the
> device clock: skew of more than the fifteen-minute access TTL makes it refresh
> in a hot loop or never refresh at all, and neither failure is visible from the
> outside. With both timestamps the client has the interval *and* the offset
> between the two clocks, and can refresh at `issuedAt + (expiry - issuedAt)/2`
> measured on its own monotonic clock.

On the way back in, either container is accepted and the **`Authorization`
header wins**: if it is present at all it is used, and a malformed one (no
`Bearer ` prefix, internal whitespace, a comma) is `401 authentication_required`
rather than a fallback to the cookie. Only with no `Authorization` header is
`zenith_team_access` read.

`companies` is the caller's complete active-membership collection, ordered
exactly as `GET /companies` orders it (`createdAt`, then `id`). `items: []` is
authoritative: this user belongs to no company and belongs on onboarding.

**`companies` is absent from the `POST /auth/refresh` response**, and absent
means unchanged — keep what is cached. A rotation every fifteen minutes is not
a reason to resend a company list, and membership changes arrive over the
socket as `auth.company_access_changed`.

> **Changed.** `session` was untyped in the response struct: a union with no
> declared discriminator, so a generated client could not narrow it. It is now
> a real `oneOf` keyed on `transport`. `companies` is new — it is what removes
> the second sign-in request.

Session-minting operations return no `revision`: a session is not a row a
client caches or re-writes, so there is nothing to guard a concurrent edit
against.

## Transport, CSRF, and body limits

Every operation that takes a `client` field cross-checks it against the
**presence of the `Origin` header**, and nothing else:

- `Origin` present → `client` must be `"web"`
- `Origin` absent → `client` must be `"desktop"`

A mismatch is `422 client_transport_mismatch`, decided before any credential is
looked at. This is what stops a browser being handed a bearer token that its
own scripts can read, and stops a native app being handed cookies it cannot
scope. Handoff start and handoff complete run the same check against a
hard-coded `"web"` and `"desktop"`.

`client` is a validated enum on every operation that accepts it: absent, empty
or unrecognized is `422 validation_failed`, and only a well-formed value that
contradicts the `Origin` header reaches `422 client_transport_mismatch`.

> **Changed.** Signup, login and the three email-verification routes used to
> reach the transport check with an unvalidated `client`, so a *missing* field
> reported `client_transport_mismatch` — a code about disagreement, for a value
> that was never there. All five now validate first, as refresh and logout
> already do.

CSRF applies **only when an authentication cookie is present**; bearer callers
never encounter it. Unsafe methods then require `X-CSRF-Token` matching the
cookie:

- `/auth/refresh` and `/auth/logout` validate against **`zenith_team_refresh_csrf`**,
  not `zenith_team_csrf` — they are authenticated by the refresh cookie, whose CSRF
  token is minted against that cookie.
- Signup, login, all three email-verification routes, both password-reset
  routes and `/auth/google/web/start` are *credential-entry* paths: a browser
  holding only `zenith_team_refresh` (lapsed access) still has to pass CSRF there,
  also against `zenith_team_refresh_csrf`.
- Everything else validates `zenith_team_csrf` against `zenith_team_access`.

Request bodies are strict JSON objects. The cap is 64 KiB on every `/auth/*`
route and on `DELETE /me`, and 8 KiB on `PATCH /me/profile`.

These apply to every operation below and are not repeated per operation:

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Body is not JSON, is not an object, or carries an unknown field |
| 403 | `csrf_invalid` | Cookie caller without a matching `X-CSRF-Token` |
| 403 | `origin_forbidden` | `Origin` is not in the allowlist |
| 413 | `request_too_large` | Body over the route's cap |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 500 | `internal_error` | Unexpected failure |

---

## Create an account

```
POST /api/v1/auth/signup
{
  "email": "sam@example.com",
  "password": "correct horse battery staple",
  "displayName": "Sam Lee",
  "client": "web",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "darwin-arm64" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | Lowercased and trimmed, must parse as a bare address, at most 320 bytes |
| `password` | body | yes | 8–128 characters. No composition rules |
| `displayName` | body | yes | Trimmed, 1–120 characters |
| `client` | body | yes | `web` or `desktop` |
| `clientMetadata` | body | no | Free-form JSON. The stored envelope `{client, details}` must serialize under 4 KiB |

Public. **201** either way: with email verification configured the account is
not created yet and the response is the pending-verification body below;
without it, the account is created and the response is the session envelope
with `companies: {"items": []}`.

Which of the two a deployment does is configuration, not a property of the API.
A client discovers it by calling this endpoint and looking at what came back.

**Signup and login answer the pending body under the same status code they use
for success — branch on `result` (`"session"` or `"pending_verification"`),
never on the HTTP status code.** `status` is also present, on the pending body
only.

```json
{
  "result": "pending_verification",
  "status": "pending_verification",
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "expiresAt": "2026-08-16T10:22:31.114882000Z",
  "resendAvailableAt": "2026-08-16T10:13:31.114882000Z",
  "attemptsRemaining": 5,
  "maxAttempts": 5
}
```

There is no `user`, no `session` and no `companies` on this body — no session
was minted. `verificationId` is what the code flow resumes with. `expiresAt` is
the flow's deadline (default 10 minutes) and `resendAvailableAt` is when
`/email-verification/resend` will stop answering `429` (default 60 seconds).

`attemptsRemaining` is how many wrong codes this flow can still absorb, and
`maxAttempts` is the budget it started with — 5 by default, but it is
deployment configuration, so read it rather than assuming it. A fresh or resent
flow starts at the full budget; each wrong code costs one, and a resend puts it
back.

> **Changed.** `expiresAt` and `resendAvailableAt` used to marshal a raw Go
> timestamp straight off the stored value, bypassing the wire-time helper — so
> they carried the server host's UTC offset while every other timestamp in the
> API was UTC. They are now RFC 3339 nanosecond UTC like everything else.

> **Added.** `attemptsRemaining` and `maxAttempts`. The attempt budget is the
> only limit in this file that is **unrecoverable**: exhausting it locks the
> flow permanently, and waiting does not clear it. A client had no way to see it
> coming — the failures are all one `400 verification_code_invalid`, and the
> lock announces itself only as the `429` that has already happened. So the
> screen that could warn "one attempt left before you have to start over" had to
> either count locally, which drifts the moment a second tab or a retry is
> involved, or hardcode 5, which is a configurable per-deployment value. Both
> numbers are columns on the flow row the server already reads on every attempt.

**Errors**

| Status | Code | When |
|---|---|---|
| 409 | `email_already_registered` | The address already has an account. Offer sign-in |
| 422 | `validation_failed` | Email, password, display name, or `client` rejected |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |
| 503 | `email_delivery_unavailable` | The verification mail could not be sent. The account is unaffected |

---

## Sign in

```
POST /api/v1/auth/login
{
  "email": "sam@example.com",
  "password": "correct horse battery staple",
  "client": "desktop",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "darwin-arm64" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | Normalized the same way signup normalizes it |
| `password` | body | yes | |
| `client` | body | yes | `web` or `desktop` |
| `clientMetadata` | body | no | Stamped on the new session |

Public. **200** with the session envelope, or **200** with the
pending-verification body for an account that exists but never finished
verification.

A wrong password, an unknown address, a malformed address and a disabled
account all collapse to one `401 invalid_credentials`, and the unknown-address
path still runs a dummy password verification so the timing matches.

The pending branch is reached only **after** the password verifies, so it
discloses nothing to an attacker. It also re-opens a verification flow: a live
code is kept and no mail is sent, otherwise a new code goes out — which is why
a plain login can fail with `503 email_delivery_unavailable`.

**A successful login is the whole sign-in; do not follow it with a company
read.** `companies` on the response is the same page `GET /companies` returns.

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 3,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-14T17:03:01.442119000Z"
  },
  "session": {
    "transport": "bearer",
    "id": "0197f58e-9c17-7b45-8d21-6f0a3e5c9147",
    "issuedAt": "2026-08-16T10:12:31.114882000Z",
    "accessToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIwMTk3ZjU4ZS02YTQ0LTdlMTEiLCJleHAiOjE3ODcyMTIwNTF9.Rr8mQ2xV",
    "refreshToken": "brf_Zt8pQ3wV7yB1nM5kC9xF2jH6dL0sA4gR7uT3eW8iO5K",
    "accessExpiresAt": "2026-08-16T10:27:31.114882000Z",
    "refreshExpiresAt": "2026-08-23T10:12:31.114882000Z"
  },
  "companies": {
    "items": [
      {
        "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
        "name": "Northwind Robotics",
        "slug": "northwind-robotics",
        "taskPrefix": "NWR",
        "businessContext": "Warehouse automation for mid-size distributors.",
        "contactEmail": "ops@northwind.example",
        "phoneCountryIso2": "US",
        "phoneNumber": "4155550142",
        "role": "owner",
        "revision": 7,
        "createdAt": "2026-07-12T03:04:05.123456789Z",
        "updatedAt": "2026-08-14T09:21:00.000000000Z"
      }
    ]
  }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `invalid_credentials` | Wrong password, unknown or malformed address, or a disabled account. Never retry programmatically |
| 422 | `validation_failed` | `client` absent or not `web`/`desktop` |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |
| 503 | `email_delivery_unavailable` | The re-opened verification mail could not be sent |

---

## Rotate the session

```
POST /api/v1/auth/refresh
{
  "client": "desktop",
  "refreshToken": "brf_Zt8pQ3wV7yB1nM5kC9xF2jH6dL0sA4gR7uT3eW8iO5K",
  "rotationId": "0197f591-3aa7-7a20-b404-c45bb29d92ab",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "darwin-arm64" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `client` | body | yes | `web` or `desktop` |
| `refreshToken` | body | desktop only | **Ignored when `client` is `web`** — the web branch reads the `zenith_team_refresh` cookie and never looks at the body |
| `rotationId` | body | no | Idempotency UUID for one rotation. Desktop clients persist it before the first attempt and reuse it until the rotated session is safely stored |
| `clientMetadata` | body | no | Re-stamped on the rotated session |

Public — the refresh credential is the authentication. **200** with the session
envelope and a fully rotated token pair, minus `companies`.

**Refresh tokens are single-use, and desktop rotation is recoverable.** Before
the first desktop attempt, generate a random `rotationId` and atomically store
it beside the current refresh token. If the connection drops after the server
commits but before the response arrives, retry the old refresh token with the
same `rotationId`; the server returns the same child session and reconstructs
the same refresh token. Clear `rotationId` only after the replacement session
is safely stored.

Replaying an already rotated token without its matching `rotationId` revokes
the whole session family and returns `401 session_invalid`; for `client: "web"`
that failure also clears all four auth cookies, so the browser comes back
clean. A concurrent rotation with a different or absent identifier can first
return `409 session_already_rotated`; retry **once** with the credential the
winning request stored.

```json
{
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 3,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-14T17:03:01.442119000Z"
  },
  "session": {
    "transport": "bearer",
    "id": "0197f591-4b62-7c19-a3d7-91e5c2f0a8b6",
    "issuedAt": "2026-08-16T10:27:31.114882000Z",
    "accessToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIwMTk3ZjU4ZS02YTQ0LTdlMTEiLCJleHAiOjE3ODcyMTI5NTF9.9Kd1LpAz",
    "refreshToken": "brf_7hN2sV6yB0mK4xC8fJ1dL5gR9uT3eW7iO2pQ4vZ",
    "accessExpiresAt": "2026-08-16T10:42:31.114882000Z",
    "refreshExpiresAt": "2026-08-23T10:27:31.114882000Z"
  }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `refresh_token_required` | No refresh cookie (web) or no `refreshToken` (desktop) |
| 401 | `session_invalid` | Expired, revoked, or replayed refresh token — the family is now revoked |
| 409 | `session_already_rotated` | Lost a rotation race without a matching `rotationId`. Retry once with the current credential |
| 422 | `validation_failed` | `client` is not `web` or `desktop`, `rotationId` is not a UUID, or `clientMetadata` exceeds its 4 KiB stored envelope |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |

---

## Sign out

```
POST /api/v1/auth/logout
{
  "client": "web",
  "refreshToken": "brf_Zt8pQ3wV7yB1nM5kC9xF2jH6dL0sA4gR7uT3eW8iO5K"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `client` | body | yes | `web` or `desktop` |
| `refreshToken` | body | desktop only | Ignored when `client` is `web`; the refresh cookie is used instead |

Public. Revokes the session behind the presented refresh credential and answers
**204** with an empty body. For `client: "web"` it always clears the four auth
cookies, whether or not there was a session to revoke.

**Logout is idempotent on both transports.** Signing out with no live
credential — no refresh cookie, or no `refreshToken` — is a `204`, not an
error: the caller asked to end up signed out and that is where they are.

> **Changed.** The two branches used to disagree: a web caller with no cookie
> got `204`, a desktop caller with no token got `400 refresh_token_required`.
> Identical situation, two contracts. `refresh_token_required` is now raised
> only by `/auth/refresh`, where the credential is the point of the call.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | `client` is not `web` or `desktop` |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |

---

## Start the daemon's own session

```
POST /api/v1/auth/daemon-session
Authorization: Bearer <the person's accessToken>
{}
```

Starts a new session for the signed-in person, held by their daemon.
The desktop app calls it after sign-in and before it starts its daemon; `zen
daemon start` calls it when its home holds no `daemon-auth.json`. Both store
the answer there, and the daemon renews it with `POST /auth/refresh`
(`client: "desktop"`); a refresh keeps it a daemon session. The body is empty
or `{}`.

**201**:

```json
{
  "client": "daemon",
  "user": { "id": "019fa834-a515-79b2-b1bd-19e6bd4f2771", "email": "mia@example.com", "displayName": "Mia" },
  "session": {
    "transport": "bearer",
    "id": "019fa834-a515-79b2-b1bd-19e6bd4f2799",
    "issuedAt": "2026-10-05T09:00:00Z",
    "accessToken": "eyJhbGciOiJIUzI1NiJ9…",
    "refreshToken": "brf_Zt8pQ3wV7yB1nM5kC9xF2jH6dL0sA4gR7uT3eW8iO5K",
    "accessExpiresAt": "2026-10-05T09:15:00Z",
    "refreshExpiresAt": "2026-11-04T09:00:00Z"
  }
}
```

A daemon session is never its person. The server accepts it on the daemon
plane (`/api/v1/daemon/*`) and, beside exactly one live
`X-Zenith-Team-Run-Token` whose Run works for the same person, on the routes that
accept a Run. Everywhere else it is `403 run_proof_required` — including
`GET /me` and this route. It is a session of its own: signing the person out
of the app does not end it; `zen desktop-session clear` and `zen auth logout`
end it with `POST /auth/logout`.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required`, `session_invalid` | No live bearer session |
| 403 | `run_proof_required` | The caller is itself a daemon session |
| 403 | `human_actor_required` | The request carries a Run proof |
| 422 | `client_transport_mismatch` | A browser request (an `Origin` header, or the cookie session): the answer is a bearer pair |

---

## Request a password reset email

```
POST /api/v1/auth/password-reset/request
{ "email": "sam@example.com" }
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | Must parse as a bare address. No `client` field, so no transport check |

Public. **202** for any well-formed address.

```json
{ "status": "accepted" }
```

`status` is the constant string `accepted`. It is a receipt that the request
was taken, and nothing more: it is the same body for a delivered mail, an
unknown address, a disabled account, and a repeat inside the cooldown.

**This endpoint is deliberately non-enumerable, so its response can never carry
a timestamp, a flow id, or anything else derived from whether the account
exists.** A mail-provider failure is swallowed here and the flow is marked for
retry — unlike signup, which surfaces the same failure, because signup has
already told the caller the address is free.

The token is 32 random bytes, base64url, delivered as a link to
`/login/reset?token=…` on the web app origin. Default lifetime 30 minutes,
resend cooldown 60 seconds.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | The address is not a parseable email |
| 503 | `password_reset_unavailable` | Reset delivery is not configured, or is down. Retry with backoff |

---

## Set a new password from a reset link

```
POST /api/v1/auth/password-reset/confirm
{
  "token": "3q2-7_8AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkA",
  "newPassword": "a new correct horse"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `token` | body | yes | The opaque value from the emailed link. Single-use |
| `newPassword` | body | yes | 8–128 characters, same rule as signup |

Public. **200**. Consumes the token, replaces the password, and revokes every
live session for that user — including the one that asked, if the caller was
signed in.

```json
{ "status": "password_reset" }
```

`status` is the constant string `password_reset`. Like the request receipt it
is not an entity: there is no row to return, because the caller now holds no
credential. **Every session is gone, so the next step is always a fresh login,
never a refresh.**

Password rules are validated **before** the token, so a weak password submitted
on an expired link reports the password problem rather than sending the user
back for a new link they will fail again.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `password_reset_invalid` | Token malformed, unknown, already used, or past its lifetime. Start a new reset; do not retry |
| 422 | `validation_failed` | New password outside 8–128 characters |
| 503 | `password_reset_unavailable` | Reset is not configured, or is down |

There is no separate expiry code here. An expired reset link is
`password_reset_invalid`, the same as a forged one, because the two are
indistinguishable to the holder and both need the same next step.

---

## Submit the email verification code

```
POST /api/v1/auth/email-verification/verify
{
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "code": "418302",
  "client": "web"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | Must match the flow's address after normalization |
| `verificationId` | body | yes | UUID string from the pending body |
| `code` | body | yes | Exactly six ASCII digits |
| `client` | body | yes | Must match the client that started the flow |

Public. **200** with the session envelope — this is where a verified signup
actually gets its session, so `companies` is present and, for a new account,
empty.

Wrong codes count against a per-flow budget of 5. **The attempt that exhausts
the budget answers `429 verification_code_locked`, not
`400 verification_code_invalid`, and the flow stays locked from then on** —
a locked flow cannot be unlocked by waiting, only replaced by a new one.

The error envelope carries no counter, so the remaining budget is read from
`attemptsRemaining` on the pending body — from the last resend, or from
`/email-verification/status`, which reports the live count without spending an
attempt.

> **Changed.** The wire field was `otp`. Every error code, the domain input and
> the mail itself call it a code; `otp` was the only spelling of it anywhere on
> the wire. It is now `code`. `verificationId` is also validated as a UUID
> string rather than decoded as a typed field, so a non-UUID is
> `422 validation_failed` here and on the desktop OAuth flow alike, instead of
> `400` on one and `422` on the other.

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-16T10:12:31.114882000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "",
    "phoneCountryIso2": null,
    "phoneNumber": null,
    "revision": 1,
    "createdAt": "2026-08-16T10:12:31.114882000Z",
    "updatedAt": "2026-08-16T10:12:31.114882000Z"
  },
  "session": {
    "transport": "cookie",
    "id": "0197f590-2ae8-7f13-b8c5-4d71e60a9f23",
    "issuedAt": "2026-08-16T10:12:31.114882000Z",
    "accessExpiresAt": "2026-08-16T10:27:31.114882000Z",
    "refreshExpiresAt": "2026-08-23T10:12:31.114882000Z"
  },
  "companies": { "items": [] }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `verification_code_invalid` | The code does not match. Prompt again |
| 403 | `account_disabled` | The flow was opened by a login against an account that has since been disabled |
| 404 | `verification_not_found` | No live flow for this email, id and client |
| 409 | `email_already_registered` | The address was claimed by another signup between opening this flow and consuming it |
| 410 | `verification_code_expired` | Past the flow's TTL, default 10 minutes. Request a new code |
| 422 | `validation_failed` | `code` is not six digits, `verificationId` is not a UUID, or `client` is not `web`/`desktop` |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |
| 429 | `verification_code_locked` | Attempt budget exhausted. Stop; a new code is required |

---

## Send the verification code again

```
POST /api/v1/auth/email-verification/resend
{
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "client": "web"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | The flow's address |
| `verificationId` | body | yes | UUID string; the flow keeps its id across resends |
| `client` | body | yes | Must match the client that started the flow |

Public. **200** with the pending-verification body. Mints a *new* code and
resets both the TTL and the cooldown. **The previous code stops working the
moment this returns** — a user who has both mails open must use the newer one.

```json
{
  "result": "pending_verification",
  "status": "pending_verification",
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "expiresAt": "2026-08-16T10:32:44.771903000Z",
  "resendAvailableAt": "2026-08-16T10:23:44.771903000Z",
  "attemptsRemaining": 5,
  "maxAttempts": 5
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `verification_not_found` | No such flow. Start a new verification |
| 410 | `verification_code_expired` | The flow itself expired; start over rather than resending |
| 422 | `validation_failed` | `verificationId` is not a UUID, or `client` is not `web`/`desktop` |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |
| 429 | `verification_resend_too_soon` | Inside the cooldown. Wait until `resendAvailableAt` from the last response |
| 429 | `verification_code_locked` | Attempt budget exhausted; resending cannot unlock it |
| 503 | `email_delivery_unavailable` | The new code could not be sent |

---

## Check whether a verification is still live

```
POST /api/v1/auth/email-verification/status
{
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "client": "web"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `email` | body | yes | |
| `verificationId` | body | yes | UUID string |
| `client` | body | yes | |

Public. Always **200**, with a body keyed on `status` — the same discriminator
the pending body carries:

| `status` | Carries | Means |
|---|---|---|
| `pending_verification` | `email`, `verificationId`, `expiresAt`, `resendAvailableAt`, `attemptsRemaining`, `maxAttempts` | Live; resume the code flow |
| `expired` | nothing else | Expired **or** locked out; start a new flow |
| `not_found` | nothing else | Unknown id, wrong email, mismatched client, or a malformed lookup |

The pending branch is byte-identical to the body signup and resend return, so a
client that resumes a flow — a reopened tab, a desktop app restarted mid-signup
— writes it into the same slot with no reshaping:

```json
{
  "result": "pending_verification",
  "status": "pending_verification",
  "email": "sam@example.com",
  "verificationId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "expiresAt": "2026-08-16T10:22:31.114882000Z",
  "resendAvailableAt": "2026-08-16T10:13:31.114882000Z",
  "attemptsRemaining": 3,
  "maxAttempts": 5
}
```

```json
{ "status": "expired" }
```

**This endpoint never reports a lookup failure as an error.** A wrong triple is
`not_found` inside a `200`, deliberately, so the endpoint cannot be used to
enumerate live verifications — which is also why it is a `POST` with a body
rather than a `GET`: the lookup triple is credential-grade and must not land in
a URL, a proxy log, or a `Referer` header. That is the file's one deviation
from the actions-are-verbs rule.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | `verificationId` is not a UUID, or `client` is not `web`/`desktop` |
| 422 | `client_transport_mismatch` | A valid `client` disagreeing with the `Origin` header |

---

## List the identity providers

```
GET /api/v1/auth/providers
```

| Parameter | In | Required | Description |
|---|---|---|---|
| — | — | — | None. Any query parameter at all is `422 validation_failed` |

Public and unauthenticated. Tells a sign-in screen which buttons to draw.

```json
{
  "items": [
    {
      "provider": "google",
      "enabled": true,
      "webEnabled": true,
      "desktopEnabled": false
    }
  ]
}
```

One item per provider this build knows about; `enabled` says whether this
deployment has it configured at all. The complete build-time set is returned.

**Draw a button on the flag for your own client — `webEnabled` in a browser,
`desktopEnabled` in the native app.** `enabled` is the deployment-level switch
and is exactly `webEnabled || desktopEnabled`; it answers "does this build have
Google at all", which is the question a support page or a settings list asks,
and it is not a sign-in offer on its own. No provider identity, client id, or
credential detail is ever exposed here.

> **Changed.** The body was `{"providers": {"google": {"enabled": true}}}` —
> two levels of nesting around one boolean, and a resource-named collection key
> holding an object map that no generated `Page<T>` could type. It is now the
> standard list envelope, which is also what lets a second provider appear
> without a shape change.

> **Added.** `webEnabled` and `desktopEnabled`, flat on the entry rather than a
> `clients: {web, desktop}` object, because two booleans are two booleans.
>
> The web and desktop OAuth clients are configured independently — a separate
> client id, secret and redirect per surface — and a deployment very commonly
> configures only the web one. `enabled` was the OR of the two, so the desktop
> app read `true`, drew the Google button, and every press failed at
> `POST /auth/google/desktop/start` with `503 oauth_not_configured`: a dead
> control on the first screen a new user ever sees, and the only diagnosis was a
> `503` the client is told to treat as "hide the button" *after* showing it. The
> per-client flags are the same two config predicates the start handlers already
> branch on.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | Any query parameter |
| 500 | `internal_error` | Unexpected. This route has no failure of its own — a deployment with no OAuth at all answers `200` with all three flags `false` |

---

## Begin the browser Google flow

```
POST /api/v1/auth/google/web/start
{
  "returnPath": "/companies/acme/inbox",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "web" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `returnPath` | body | yes | Same-origin path the callback redirects to. Must start with a single `/`; no `\`, `#`, control characters, host or userinfo; at most 2048 bytes |
| `clientMetadata` | body | no | Exactly two accepted keys, `appVersion` and `platform`, each at most 255 bytes with no control characters. Any other key is `400 invalid_request` from the strict decoder |

Public. **200**. Creates a server-side flow, sets a browser-binding cookie, and
returns the URL to send the browser to.

```json
{
  "authorizationUrl": "https://accounts.google.com/o/oauth2/v2/auth?client_id=41f2.apps.googleusercontent.com&response_type=code&state=kQ7bT2xN9pL4vR8sW1zY6cF3jH5mA0dG2eK7nP9qS4U",
  "expiresAt": "2026-08-16T10:22:31.114882000Z"
}
```

The response also sets `zenith_team_oauth_google_web`: HttpOnly, `SameSite=Lax`,
scoped to the callback path only. **It binds the flow to this browser, and the
callback fails without it** — so a flow started in one browser cannot be
completed in another, and a mail-forwarded callback URL is inert. Flow lifetime
defaults to 10 minutes.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | `returnPath` or `clientMetadata` rejected |
| 502 | `oauth_provider_unavailable` | The provider would not issue an authorization URL. Retry with backoff |
| 503 | `oauth_not_configured` | Google is not configured for the web client. Hide the button |

---

## Finish the browser Google flow

```
GET /api/v1/auth/google/web/callback?state=kQ7bT2xN9pL4vR8sW1zY6cF3jH5mA0dG2eK7nP9qS4U&code=4/0AeanS0b7Yh2Kd8vQ1nR5xT3wZ9cF6jL0mP4sA8uG2eK
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `state` | query | yes | Opaque 43-character value from the authorization URL |
| `code` | query | exactly one of | Google's authorization code |
| `error` | query | exactly one of | Only the literal `access_denied` is accepted |

Public, and called by the browser as a redirect target from Google — never by
application code. On success it sets the four auth cookies and answers **303**
with `Location: <webAppOrigin><returnPath>`. Redirects have no response body.

The query string must be non-empty and at most 8 KiB, with `state` and
exactly one of `code`/`error`, each appearing once with a non-empty value.
Additional Google response parameters such as `scope`, `authuser`, `hd` and
`prompt` are ignored; they cannot supply identity or a return path. A repeated
authorization parameter, malformed encoding or an unrecognized `error` value
fails before the flow is looked up. `zenith_team_oauth_google_web` must be present
exactly once and is cleared on every response through this path, so a failed
callback cannot be retried by reloading.

**An expected OAuth failure redirects too.** The browser is sent to
`<webAppOrigin>/login?auth_error=<code>` with the code from the table below, so
the user lands on the sign-in screen and the client branches on `auth_error`
exactly as it would on an error envelope's `code`.

> **Changed.** Every failure used to return the JSON error envelope with its
> HTTP status — to a browser sitting on a redirect from Google, which rendered
> raw JSON. The success path redirected and the failure path did not.

**Errors**

| Status | Code | When |
|---|---|---|
| 303 | `oauth_flow_invalid` | Bad query shape, missing or duplicated binding cookie, unknown or expired flow, nonce mismatch. Restart the flow |
| 303 | `oauth_provider_denied` | The user declined at Google. Not an error to report |
| 303 | `oauth_identity_invalid` | The provider returned an unusable subject, email or name |
| 303 | `oauth_email_unverified` | Google reports the address as unverified |
| 303 | `account_disabled` | The matched account is disabled |
| 303 | `oauth_account_conflict` | The identity belongs to another account. Sign in with the original method and link |
| 303 | `oauth_provider_unavailable` | Token exchange failed |
| 303 | `oauth_not_configured` | Google is not configured for the web client |
| 500 | `internal_error` | Missing web app origin or CSRF configuration, an unsafe stored return path, or an unexpected internal error. These retain the error envelope |

---

## Begin the desktop Google flow

```
POST /api/v1/auth/google/desktop/start
{
  "redirectUri": "http://127.0.0.1:53127/",
  "codeChallenge": "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "darwin-arm64" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `redirectUri` | body | yes | Exactly `http://127.0.0.1:<port>/` — trailing slash required, no path, no `localhost`, no IPv6 form — with the port in 1024–65535 |
| `codeChallenge` | body | yes | S256 challenge: exactly 43 characters of unpadded base64url decoding to 32 bytes |
| `clientMetadata` | body | no | The same two-key allowlist as the web start |

Public. PKCE flow for the native app, which listens on a loopback port. **200**.

```json
{
  "flowId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "state": "kQ7bT2xN9pL4vR8sW1zY6cF3jH5mA0dG2eK7nP9qS4U",
  "authorizationUrl": "https://accounts.google.com/o/oauth2/v2/auth?client_id=41f2.apps.googleusercontent.com&response_type=code&code_challenge_method=S256&state=kQ7bT2xN9pL4vR8sW1zY6cF3jH5mA0dG2eK7nP9qS4U",
  "expiresAt": "2026-08-16T10:22:31.114882000Z"
}
```

**The desktop client receives `flowId` and `state` in the body, because there
is no browser to hold a binding cookie.** PKCE is what replaces it: the
verifier never leaves the app, so possession of `state` alone is not enough to
complete the flow.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | Redirect URI, code challenge, or metadata rejected |
| 502 | `oauth_provider_unavailable` | The provider would not issue an authorization URL |
| 503 | `oauth_not_configured` | Google is not configured for the desktop client |

---

## Finish the desktop Google flow

```
POST /api/v1/auth/google/desktop/complete
{
  "flowId": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "redirectUri": "http://127.0.0.1:53127/",
  "state": "kQ7bT2xN9pL4vR8sW1zY6cF3jH5mA0dG2eK7nP9qS4U",
  "code": "4/0AeanS0b7Yh2Kd8vQ1nR5xT3wZ9cF6jL0mP4sA8uG2eK",
  "verifier": "M25iVXpKU3puUjFaYWg3T1NDTDQtcW1ROF2VfSU4tS0"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `flowId` | body | yes | UUID string from start |
| `redirectUri` | body | yes | Must equal the one the flow was started with |
| `state` | body | yes | Echoed from the provider |
| `code` | body | exactly one of | Authorization code |
| `error` | body | exactly one of | Only `access_denied` passes validation |
| `verifier` | body | yes | PKCE verifier, 43–128 unreserved characters; its S256 challenge must match the stored one |

Public. The app posts what its loopback listener caught. **200** with the
session envelope, **always in the bearer branch** regardless of anything the
caller sends — and this operation runs no transport check, so it works with or
without an `Origin` header.

Sending both `code` and `error`, or neither, is `422 validation_failed`.

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-16T10:18:02.664301000Z",
    "hasPassword": false,
    "linkedProviders": ["google"],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "",
    "phoneCountryIso2": null,
    "phoneNumber": null,
    "revision": 1,
    "createdAt": "2026-08-16T10:18:02.664301000Z",
    "updatedAt": "2026-08-16T10:18:02.664301000Z"
  },
  "session": {
    "transport": "bearer",
    "id": "0197f590-6b91-7d28-9a3e-15c8f4b20e77",
    "issuedAt": "2026-08-16T10:18:02.664301000Z",
    "accessToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIwMTk3ZjU4ZS02YTQ0LTdlMTEiLCJleHAiOjE3ODcyMTMxMDJ9.Lm4vXq7T",
    "refreshToken": "brf_Qw3eR7tY1uI5oP9aS2dF6gH0jK4lZ8xC1vB5nM7",
    "accessExpiresAt": "2026-08-16T10:33:02.664301000Z",
    "refreshExpiresAt": "2026-08-23T10:18:02.664301000Z"
  },
  "companies": { "items": [] }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `oauth_flow_invalid` | Unknown, expired or already-consumed flow, or a present-but-malformed `state`, `redirectUri` or `verifier`. Restart the flow |
| 401 | `oauth_provider_denied` | `error=access_denied` |
| 401 | `oauth_identity_invalid` | Unusable provider identity |
| 403 | `oauth_email_unverified` | Google reports the address as unverified |
| 403 | `account_disabled` | The matched account is disabled |
| 409 | `oauth_account_conflict` | The identity belongs to another account |
| 422 | `validation_failed` | The `code`/`error` rule, a non-UUID `flowId`, or a missing required field |
| 502 | `oauth_provider_unavailable` | Token exchange failed |
| 503 | `oauth_not_configured` | Google is not configured for the desktop client |

---

## Mint a desktop handoff code

```
POST /api/v1/auth/desktop/handoff/start
```

| Parameter | In | Required | Description |
|---|---|---|---|
| — | — | — | No request body. The caller is identified by the cookie session |

**Cookie session only.** This is the one operation a valid bearer token cannot
call: the point is for a logged-in browser to hand a login to the native app,
so the handler re-checks that the principal was authenticated by session cookie
and requires an `Origin` header — and therefore a valid `X-CSRF-Token`. **200**.

```json
{
  "code": "Xy4mB8kQ2vR6tN0wZ7sD3fG9hJ1lP5cU8aE2bY6nT4W",
  "expiresAt": "2026-08-16T10:13:31.114882000Z"
}
```

The code is 43 characters of unpadded base64url over 32 random bytes, lives
**60 seconds**, and is single-use. Only its SHA-256 digest is stored.
**Minting a new code deletes any code the user already holds**, so at most one
is live per user — a second tab cannot leave a stale code redeemable.

> **Changed.** A bearer caller used to get `401 authentication_required`, which
> says the credential was missing when it was in fact valid and simply the
> wrong container. It is now `422 client_transport_mismatch`, the code the
> vocabulary defines for exactly this — a bearer client on a cookie flow.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential at all |
| 401 | `session_invalid` | The cookie's session is expired or revoked |
| 422 | `client_transport_mismatch` | A bearer credential, or a cookie credential with no `Origin` header |

---

## Redeem a desktop handoff code

```
POST /api/v1/auth/desktop/handoff/complete
{
  "code": "Xy4mB8kQ2vR6tN0wZ7sD3fG9hJ1lP5cU8aE2bY6nT4W",
  "clientMetadata": { "appVersion": "1.4.0", "platform": "darwin-arm64" }
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `code` | body | yes | The 43-character code from handoff start |
| `clientMetadata` | body | no | The same two-key allowlist as the OAuth flows |

Public — the desktop app has no credential yet. **200** with the session
envelope in the bearer branch. An `Origin` header on this call is
`422 client_transport_mismatch`: a browser has no business redeeming a handoff.

**The code is consumed before its expiry is checked**, so it can never be
redeemed twice even if the first attempt arrives late: a second attempt sees
`400 desktop_handoff_invalid`, while a late first attempt sees
`410 desktop_handoff_expired`.

```json
{
  "result": "session",
  "user": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 3,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-14T17:03:01.442119000Z"
  },
  "session": {
    "transport": "bearer",
    "id": "0197f590-8c04-7e35-b1f9-27d6a3e58c40",
    "issuedAt": "2026-08-16T10:13:02.117440000Z",
    "accessToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIwMTk3ZjU4ZS02YTQ0LTdlMTEiLCJleHAiOjE3ODcyMTMyODJ9.Bd6nHs2W",
    "refreshToken": "brf_5tG9hJ2kL7mN0pQ4rS8vW1xY3zA6cD9fH2jK5nP",
    "accessExpiresAt": "2026-08-16T10:28:02.117440000Z",
    "refreshExpiresAt": "2026-08-23T10:13:02.117440000Z"
  },
  "companies": {
    "items": [
      {
        "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
        "name": "Northwind Robotics",
        "slug": "northwind-robotics",
        "taskPrefix": "NWR",
        "businessContext": "Warehouse automation for mid-size distributors.",
        "contactEmail": "ops@northwind.example",
        "phoneCountryIso2": "US",
        "phoneNumber": "4155550142",
        "role": "owner",
        "revision": 7,
        "createdAt": "2026-07-12T03:04:05.123456789Z",
        "updatedAt": "2026-08-14T09:21:00.000000000Z"
      }
    ]
  }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `desktop_handoff_invalid` | Malformed, unknown, or already-consumed code. Request a new one |
| 403 | `account_disabled` | The account behind the code is disabled |
| 410 | `desktop_handoff_expired` | The code was live but past its 60 seconds |
| 422 | `validation_failed` | `clientMetadata` rejected |
| 422 | `client_transport_mismatch` | An `Origin` header was present |

---

## Read the current user

```
GET /api/v1/me
```

| Parameter | In | Required | Description |
|---|---|---|---|
| — | — | — | None. Any query parameter is `422 validation_failed` |

Any user credential, cookie or bearer.

**A user read returns the `User` entity itself — there is no `{"user": …}`
wrapper.** That is the one shape, shared by this read, by both `PATCH`es below,
and by the `user` slot of the session envelope and of the `user.updated` event.

```json
{
  "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "email": "sam@example.com",
  "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
  "hasPassword": true,
  "linkedProviders": [],
  "displayName": "Sam Lee",
  "avatarUrl": null,
  "biography": "Ships the desktop shell.",
  "phoneCountryIso2": "VN",
  "phoneNumber": "912345678",
  "revision": 3,
  "createdAt": "2026-08-01T09:12:44.918273000Z",
  "updatedAt": "2026-08-14T17:03:01.442119000Z"
}
```

- `avatarUrl`, `phoneCountryIso2` and `phoneNumber` are nullable; `biography` is
  a string that is `""` when unset, never null. No operation in this file writes
  `avatarUrl`.
- `emailVerifiedAt` is when the address was proven, and `null` when it never
  was. A Google account is verified at creation; a code signup is verified the
  moment the code is consumed; a deployment that runs no verification leaves it
  `null` forever, so **`null` is not a prompt to nag** — pair it with
  `hasPassword` before offering anything.
- `hasPassword` is `false` on an account created through Google and never given
  a password. Such an account cannot use `POST /auth/login` at all.
- `linkedProviders` is the identity providers linked to this account, `[]` when
  there are none. The values are the same slugs `GET /auth/providers` uses.
- `updatedAt` falls back to `createdAt` on a row that has never been updated, so
  it is never zero or null.
- `revision` is a plain JSON number, well under 2^53, and is the value
  `PATCH /me/profile` expects back as `expectedRevision`.

> **Changed.** This read answered `{"user": …}` while every sibling read in the
> API returns the bare entity, so the one wire type had two spellings and a
> client had to unwrap here and not elsewhere. The wrapper is gone from all
> three `/me` operations; the session envelope keeps a `user` key because it is
> a composite of three named slots, not a wrapper around one.

> **Added.** `emailVerifiedAt`, `hasPassword` and `linkedProviders` — between
> them, how this account can authenticate.
>
> Nothing on the wire said any of it. There is no read of an account's sign-in
> methods anywhere in the API, so a settings screen could not label its own
> button: "Change password" is wrong for a Google account that has none, "Set a
> password" is wrong for everyone else, and "Connect Google" is wrong for the
> account that already did. The same three fields are what makes unlinking
> safe to reason about — an account with `hasPassword: false` and one linked
> provider has exactly one way in, and a client can refuse to remove it rather
> than discovering the problem at the next sign-in.
>
> All three come from auth's one canonical User projection query:
> `emailVerifiedAt` and the password are columns on the user row, while
> `linkedProviders` is aggregated from the account's indexed linked identities
> in the same read. They travel on the `User` wire type, so `GET /me`, both
> `PATCH`es and the `user.updated` event carry them identically.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential presented |
| 401 | `session_invalid` | Expired, revoked, or belonging to a disabled account |
| 422 | `validation_failed` | Any query parameter |

---

## Update the profile

```
PATCH /api/v1/me/profile
{
  "expectedRevision": 3,
  "displayName": "Sam Lee-Nguyen",
  "biography": "Ships the desktop shell.",
  "phoneCountryIso2": null,
  "phoneNumber": null
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `expectedRevision` | body | yes | 1..9007199254740991; the `revision` you last read |
| `displayName` | body | yes | Trimmed, 1–120 characters and at most 480 UTF-8 bytes |
| `biography` | body | yes | Trimmed, CRLF normalized to LF; at most 1200 characters, 4800 bytes and 12 lines; no NUL. `""` clears it |
| `phoneCountryIso2` | body | yes | Supported ISO-2 code, or `null` |
| `phoneNumber` | body | yes | 1–15 digits, no separators or `+`, or `null` |

Any user credential. This is a **whole-profile write**: all four editable
fields are required on every request. An explicit `null` clears a nullable
phone field. The phone pair is validated together, so the stored country and
number are always both set or both null.

`expectedRevision` is checked **first, on every call**, including one that
would change nothing. A stale revision is `409` whether or not the values
happen to match what is stored.

Success is **200** with the authoritative User at the top level. Post-commit,
`user.updated` carries the same complete projection.

```json
{
  "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
  "email": "sam@example.com",
  "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
  "hasPassword": true,
  "linkedProviders": [],
  "displayName": "Sam Lee-Nguyen",
  "avatarUrl": null,
  "biography": "Ships the desktop shell.",
  "phoneCountryIso2": null,
  "phoneNumber": null,
  "revision": 4,
  "createdAt": "2026-08-01T09:12:44.918273000Z",
  "updatedAt": "2026-08-16T08:41:12.005311000Z"
}
```

A revision mismatch is **409** with the standard envelope plus the server's
current state, so the client can re-render without a refetch. **The key is
`currentUser`, not `user`** — a client that reads one key unconditionally finds
nothing there on a conflict.

```json
{
  "detail": "profile changed elsewhere",
  "code": "profile_revision_conflict",
  "requestId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "currentUser": {
    "id": "0197f58e-6a44-7e11-9c2f-3f1c0a9d4b21",
    "email": "sam@example.com",
    "emailVerifiedAt": "2026-08-01T09:12:44.918273000Z",
    "hasPassword": true,
    "linkedProviders": [],
    "displayName": "Sam Lee",
    "avatarUrl": null,
    "biography": "Ships the desktop shell.",
    "phoneCountryIso2": "VN",
    "phoneNumber": "912345678",
    "revision": 5,
    "createdAt": "2026-08-01T09:12:44.918273000Z",
    "updatedAt": "2026-08-16T08:39:44.771903000Z"
  }
}
```

Missing or wrong-typed profile fields report `400 invalid_request`; normalized
values that fail a domain rule report `422 validation_failed`. The response
uses the dedicated `profile_revision_conflict` code and carries `currentUser`.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | A required editable field is omitted or has the wrong JSON type |
| 401 | `authentication_required` | No credential presented |
| 401 | `session_invalid` | Expired or revoked credential |
| 403 | `account_disabled` | The account was disabled or deleted underneath the request |
| 409 | `profile_revision_conflict` | Stale `expectedRevision`. Carries `currentUser`; re-apply against it |
| 422 | `validation_failed` | A field fails its length/format rule, `expectedRevision` is absent or out of range, or the phone pair is half-set |

---

## Delete the account

```
DELETE /api/v1/me
{ "confirmation": "DELETE" }
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `confirmation` | body | yes | The exact literal string `DELETE`. Case-sensitive; `"delete"` is rejected |

Any user credential. Soft-deletes the account, cancels every run the user
triggered, revokes every live session, and clears the web auth cookies.
**204** with an empty body.

This `DELETE` takes a request body — the deviation exists so the confirmation
is a typed field rather than a query parameter that a link or a prefetch could
fire. Because the body goes through the same strict reader, a bodyless call is
`415 unsupported_media_type`.

The cascade runs in one transaction: every run the user authored across every
company they belong to moves to `cancelled` with reason `account_deleted`, the
runs' daemon tokens are deleted, and the session revocations fan out as
`auth.session_revoked`. **Daemon authority dies with those sessions**, so an
in-flight agent turn stops rather than outliving its author.

This does not recall Agent Mail already committed to durable asynchronous
delivery. Queueing that mail is its authorization point; only reviewable drafts
remain editable or deletable.

A repeat call from a credential the cascade already revoked cannot re-run it:
it fails authentication first and reports `401 session_invalid`, which is the
converged state rather than an error to retry.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential presented, or the delete's own session row is already gone |
| 401 | `session_invalid` | The credential could not be authenticated |
| 409 | `account_conflict` | The row moved off the expected revision, or the transaction committed zero session revocations. Re-read the account |
| 422 | `validation_failed` | `confirmation` is not exactly `DELETE` |

---

## Realtime Events

All four use the authenticated **user** audience and never a Company audience.
See [realtime.md](realtime.md).

| Event | Carries | Kind |
|---|---|---|
| `user.updated` | `User` — the entity this file defines, identical bytes to `GET /me` | Cache |
| `auth.session_revoked` | `{sessionId, reason}` | Cache — sign out |
| `auth.company_access_changed` | `{companyId, role, reason}` | Cache |
| `auth.company_access_revoked` | `{companyId, reason}` | Cache — drop the company |

`user.updated` is raised by `PATCH /me/profile` and carries the whole user, so
another open tab or the desktop app writes it straight into cache with no read.

`auth.session_revoked` is raised by logout, refresh rotation, refresh-token
reuse, password reset confirmation, account disable and account deletion.
`reason` is one of `refresh_rotated`, `logout`, `refresh_token_reuse`,
`administrative_revocation`, `password_reset`, `user_disabled`. **A client that
receives it for its own session must stop using the credential** — the next
request would be `401 session_invalid` anyway.

Its `sessionId` is the `session.id` the sign-in or the last rotation handed
back. **Compare the two before acting**: the event goes to the user's audience, so
every device sees every revocation, and the most common one by far is
`refresh_rotated` — another device's routine fifteen-minute refresh, which says
nothing at all about this one.

The two `auth.company_access_*` events are raised by the membership domain, not
by anything in this file; they are listed here because they carry the caller's
own access and are what keeps the `companies` page a sign-in delivered correct
without a re-read. `reason` is one of `membership_granted`,
`membership_reactivated`, `role_changed`, `membership_removed`,
`membership_left`, `company_deleted`.

## Lifecycles

```
Email verification flow

  opened ─────────► pending ────── correct code ──────► consumed ─► session
  (signup, or a       │  ▲
   login against      │  └─── resend: new code, TTL and cooldown reset,
   an unverified      │              the previous code stops working
   account)           │
                      ├─── 10 minutes elapse ─────────► expired    410
                      └─── 5 wrong codes ─────────────► locked     429

  expired and locked both read as "expired" from the status endpoint;
  neither is recoverable — open a new flow.
```

```
Session

  minted ─────► active ────── refresh ──────► rotated ──► new session active
  (signup,        │                              │
   login,         │                              └─ replayed ─► family revoked  401
   verify,        │
   OAuth,         ├─ logout ────────────────────────────────► revoked           204
   handoff)       ├─ password reset confirm ────────────────► all revoked       200
                  ├─ account disabled ──────────────────────► all revoked
                  └─ DELETE /me ────────────────────────────► all revoked       204

  Every revocation emits auth.session_revoked with its reason.
```

```
Account

  ∅
  ├─ signup, verification configured ─► pending_verification ─ verify ─┐
  └─ signup, verification not configured ──────────────────────────────┤
                                                                       ▼
                                                                    active
                                       ┌───────────────────────────────┤
                                       ▼                               ▼
                                    disabled                        deleted
                             (403 account_disabled;             (DELETE /me,
                              support can re-enable)             terminal)

  active is the only state that authenticates. pending_verification holds no
  user row — the account is created when the code is consumed. deleted is a
  soft delete: every session is revoked and the address stays taken.
```
