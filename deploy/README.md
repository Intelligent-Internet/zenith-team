# Zenith Team production deployment runbook

The Zenith Team cloud API is one stateless Go binary. It owns every HTTP and
WebSocket surface, runs its own PostgreSQL migrations at startup, and takes
all configuration from domain-prefixed environment variables (`APP_`, `AUTH_`,
`STORAGE_`, `GOOGLE_`, `SOCIAL_`, `GCP_`, `CLOUD_RUN_`, `ATTACHMENT_`) — there are no
config files.
Daemons are **not** deployed here: they run on user machines and connect out
to this API.

This directory contains:

| File | Purpose |
| --- | --- |
| `docker-compose.production.yml` | Single-node production stack: server + bundled PostgreSQL/Redis with volumes and healthchecks |
| `production.env.example` | Environment template; copy to `.env` next to the compose file and fill in |
| `grafana-boardly.json` | Grafana dashboard for the server's Prometheus metrics |

The development stack (PostgreSQL/Redis/Silo only, API from source) lives in
[`../docker/`](../docker/README.md); day-to-day workflows and verification
lanes are in [`../AGENTS.md`](../AGENTS.md).

## Build

Build the image from the repository root (multi-stage Go build, distroless
static runtime, non-root user, ~56 MB):

```bash
docker build -f server/Dockerfile -t boardly-server:$(git rev-parse --short HEAD) .
```

`server/Dockerfile.dockerignore` trims the BuildKit context to the Go module.
CI runs the same build when a `release-<semver>` tag (for example
`release-0.0.1`) is pushed at a commit on `main`
(`.github/workflows/release.yml`) and pushes `ii-boardly/server:<semver>`
and `:latest` to ECR without deploying; merges to `main` build no image,
and the recipe above is for self-hosting. Tag local images with the
commit; `latest` is a convenience, never a deploy target. The image
contains two binaries: `/usr/local/bin/boardly-server`
and `/usr/local/bin/boardly-readyz`, a static probe used by the container
healthcheck (distroless has no shell or curl).

## Deploy

```bash
cd deploy
cp production.env.example .env   # fill in every <...> placeholder
docker compose -f docker-compose.production.yml up -d
docker compose -f docker-compose.production.yml ps    # server must be "healthy"
curl -fsS http://localhost:8788/readyz
```

Compose reads `deploy/.env` automatically. Never commit a filled `.env`;
inject secrets from your secret manager. To use managed PostgreSQL/Redis, set
`APP_DATABASE_URL`/`APP_REDIS_URL` and remove the bundled services.
Run the API behind a TLS-terminating reverse proxy; WebSocket upgrade
passthrough is required for `/api/v1/ws` and `/api/v1/daemon/ws`, and the
proxy's CIDR belongs in `BOARDLY_TRUSTED_PROXIES` so the WebSocket origin
check can trust the forwarded host.

## Migrate

There is no separate migration job. On startup, before serving traffic, the
server applies pending embedded migrations from `server/migrations/` with
golang-migrate (`cmd/server` calls `db.MigrateUp`). Existing databases keep their
applied history; feature migrations append after the latest version on `main`.
Fresh installations apply the complete sequence. Concurrent replicas serialize through
golang-migrate's PostgreSQL lock, and a session statement timeout (5 minutes,
bounded by the startup context) prevents a shut-down peer from wedging the
lock. A failed migration exits the process non-zero and the previous
containers keep serving — with `docker compose` the old container is already
stopped, so keep the previous image tag at hand (see Rollback).

## Configure

Everything is environment-driven and validated at startup; invalid values
fail fast with a named variable in the error. Durations use Go syntax
(`30s`, `24h`). Empty values behave exactly like unset ones.

### Required

| Variable | Rule |
| --- | --- |
| `APP_DATABASE_URL` | PostgreSQL DSN, e.g. `postgresql://boardly:pass@host:5432/boardly` |
| `AUTH_CSRF_SECRET` | At least 32 bytes; signs CSRF, session, and Run capability tokens — rotation invalidates all three |

### Core process

| Variable | Default | Notes |
| --- | --- | --- |
| `APP_HTTP_ADDR` | `:8788` | Listen address |
| `APP_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `APP_SHUTDOWN_TIMEOUT_SECONDS` | `15` | Graceful drain budget; keep below the orchestrator stop grace period |
| `APP_API_REPLICAS` | `1` | Above 1 makes Redis mandatory and readiness fail-closed on relay health |
| `APP_REDIS_URL` | empty | Required when `APP_API_REPLICAS > 1`; optional accelerator for a single node |
| `APP_DAEMON_ID_REQUIRED` | `false` | `true` refuses daemon-plane runtime requests without `X-Boardly-Daemon-ID` (`400 invalid_request`); `false` admits them with a warning log while old daemons upgrade |
| `AUTH_WEB_ORIGINS` | `http://localhost:5173` | Comma-separated exact canonical renderer origins; non-loopback HTTP is rejected and packaged Electron may use only `boardly-app://renderer` |
| `AUTH_WEB_APP_ORIGIN` | empty | Required when email verification or Google OAuth is enabled; must appear in `AUTH_WEB_ORIGINS` |
| `AUTH_COOKIE_SECURE` | derived | Forced `true` by any HTTPS origin (cannot be overridden to `false`) |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS` | `900` | Access credential lifetime (15 minutes) |
| `AUTH_REFRESH_TOKEN_TTL_SECONDS` | `604800` | Rotating refresh credential (7 days); must exceed the access TTL |

### Email verification (optional; all core values together)

Leaving the provider, API key, and verification secret empty preserves the
immediate-signup development flow. Configuring them enables a 6-digit OTP
before the user and first session are created, plus password recovery. Only an
HMAC of the OTP, the already-hashed password, and SHA-256 password-reset token
digests are stored in PostgreSQL. Both Resend templates must be published. The
verification template defines `DISPLAY_NAME` and `OTP_CODE`; the password reset
template defines `DISPLAY_NAME` and `RESET_URL`.

| Variable | Default | Notes |
| --- | --- | --- |
| `AUTH_EMAIL_PROVIDER` | — | Currently `resend`; auth depends on a provider-neutral sender interface |
| `AUTH_RESEND_API_KEY` | — | Secret; inject from the secret manager |
| `AUTH_EMAIL_VERIFICATION_SECRET` | — | Secret, at least 32 bytes; rotating it invalidates pending OTPs |
| `AUTH_EMAIL_VERIFICATION_TEMPLATE_ID` | Zenith Team Verification template | Published Resend template ID |
| `AUTH_EMAIL_VERIFICATION_TTL_SECONDS` | `600` | Range `60..1800` |
| `AUTH_EMAIL_VERIFICATION_RESEND_COOLDOWN_SECONDS` | `60` | Range `10..600`, shorter than TTL |
| `AUTH_EMAIL_PROVIDER_TIMEOUT_SECONDS` | `10` | Range `1..30` |
| `AUTH_PASSWORD_RESET_TEMPLATE_ID` | Zenith Team Password Reset template | Published Resend template ID |
| `AUTH_PASSWORD_RESET_TTL_SECONDS` | `1800` | Range `300..86400` |
| `AUTH_PASSWORD_RESET_RESEND_COOLDOWN_SECONDS` | `60` | Range `10..600`, shorter than TTL |
| `AUTH_INVITATION_TEMPLATE_ID` | — | Published Resend member invitation template ID; email invitations stay disabled when empty |

### Company Mailbox (optional)

Mailbox receiving must use a separate Resend team and API key from auth mail.
Before enabling it, verify the receiving MX for the exact domain and register
`POST /api/v1/webhooks/resend/mailbox` for `email.received`. The API key and
domain enable Company Mailbox; the webhook secret may be omitted during setup,
in which case the backend starts normally but does not expose inbound ingress.
PostgreSQL keeps only the 30-day metadata/read index; bodies and attachment
bytes remain in Resend.

| Variable | Default | Notes |
| --- | --- | --- |
| `MAILBOX_RESEND_API_KEY` | — | Secret from the dedicated Mailbox Resend team |
| `MAILBOX_RESEND_WEBHOOK_SECRET` | empty | Resend `whsec_` signing secret; empty disables only the webhook route |
| `MAILBOX_DOMAIN` | — | Canonical lowercase receiving domain, e.g. `mail.example.com` |
| `MAILBOX_REQUESTS_PER_SECOND` | `4` | Range `1..5`; default leaves headroom below Resend's team-wide limit |
| `MAILBOX_PROVIDER_TIMEOUT_SECONDS` | `10` | Range `1..30` |

Roll out in this order: create the dedicated Resend team, inject its API key and
`MAILBOX_DOMAIN`, then deploy the backend. Before testing inbound mail, verify
the MX, register the production webhook, inject its signing secret, restart the
backend, and canary one message through list/detail/attachment retrieval. Treat
the default five-request-per-second Resend team limit as beta capacity and
request a higher provider limit before public GA.

### Stripe Connect Commerce (optional; all values together)

Commerce keeps every Stripe credential in the Zenith Team backend. Generated Sites
authenticate their own customers and send only short-lived ES256 request
proofs plus approved Offer aliases; they never receive a Stripe key or a
Zenith Team shared Site secret. Site private keys remain in the Site deployment's
secret manager or KMS, while Zenith Team stores only public keys. Use three
distinct restricted keys, store them in the deployment secret manager, and
apply Stripe IP restrictions to the Zenith Team API egress addresses. Register
`POST /api/v1/webhooks/stripe/commerce` as a Connect webhook destination whose
event source is **Connected accounts** (not only the platform account), for the
Checkout, Invoice, Subscription, Charge refund, and Dispute events in the
Commerce runbook.

| Variable | Default | Notes |
| --- | --- | --- |
| `COMMERCE_ENABLED` | `false` | Kill switch for new catalog publishing and Checkout; Portal, entitlement reads, webhook processing, and reconciliation remain available when configured |
| `STRIPE_CONNECT_KEY` | — | Restricted key for Connect account creation, onboarding, and fresh Account readiness reads before Checkout |
| `STRIPE_COMMERCE_KEY` | — | Distinct restricted key for Product, Price, Customer, Checkout, and Portal writes |
| `STRIPE_RECONCILE_KEY` | — | Distinct read-only restricted key for current-object reconciliation; sandbox deployments used for local payment testing also require **Events: Read** for connected accounts |
| `STRIPE_PUBLISHABLE_KEY` | — | Publishable key for Stripe-hosted Connect onboarding UI |
| `STRIPE_WEBHOOK_SECRET` | — | Secret for the Commerce webhook endpoint |
| `STRIPE_PLATFORM_ACCOUNT_ID` | — | Exact platform `acct_` identity; Connected Account direct charges may never use this account |
| `COMMERCE_CUSTOMER_HMAC_KEY` | — | Standard-base64 encoding of exactly 32 random bytes; durable identity material for Site/customer subject mappings and session assertions |
| `COMMERCE_ALLOWED_CONNECTED_COUNTRIES` | `GB` | Release-pinned corridor; must currently be exactly `GB` |
| `COMMERCE_PROVIDER_TIMEOUT_SECONDS` | `10` | Stripe and Site-authority verification request timeout, range `1..30` |

The variable names are mode-neutral. Non-production accepts only Stripe test
credentials; production accepts only live credentials and rejects the legacy
`STRIPE_SECRET_KEY`. Rotate one restricted Stripe key at a time through the
secret manager, restart/canary the API, confirm reconciliation and webhook
health, and then revoke the previous key. Do not rotate
`COMMERCE_CUSTOMER_HMAC_KEY` until a dual-key or key-ID migration exists:
changing it would orphan existing customer subject hashes and entitlements and
replace the session assertion signing identity. Before enabling
`COMMERCE_ENABLED` in production, complete the documented sandbox matrix and
repeat it against a controlled live Site.

Restricted-key permissions are Zenith Team deployment configuration, never a
Company or website-builder question. Before using the local sandbox Event
fallback, verify the sandbox `STRIPE_RECONCILE_KEY` can list test Events while
impersonating a known connected account. Stripe requires restricted-key
permission changes in the Dashboard; Zenith Team cannot grant its own platform key
additional authority through Connect onboarding. A missing permission opens a
one-minute local retry circuit and is reported as
`provider_permission_missing` instead of calling Stripe every poll interval.

### PostgreSQL pool

| Variable | Default | Notes |
| --- | --- | --- |
| `APP_DATABASE_MAX_CONNS` | `25` | Positive int32 |
| `APP_DATABASE_MIN_CONNS` | `5` | Must not exceed max |
| `APP_DATABASE_MAX_CONN_LIFETIME_SECONDS` | `3600` | |
| `APP_DATABASE_MAX_CONN_IDLE_TIME_SECONDS` | `1800` | |
| `APP_DATABASE_HEALTH_CHECK_PERIOD_SECONDS` | `60` | |

### Attachment storage

Zenith Team uses one S3-compatible object-store adapter. AWS S3 and Silo differ
only by endpoint, credential, and path-style configuration. Every replica must
use the same store. Moving objects to a different bucket or backend is an
explicit operational migration: quiesce attachment writes, copy and verify all
objects, update every replica's configuration together, then resume traffic.

`STORAGE_DRIVER=s3` is the default and uses the explicit settings below.
For local development, `STORAGE_DRIVER=local` with `APP_ENVIRONMENT=local`
supplies the bundled Silo defaults: endpoint `http://localhost:9000`, bucket
`boardly-attachments`, region `us-east-1`, the Docker development credentials,
and path-style requests. Run `make infra-up` to start Silo and create the
bucket. Explicit `STORAGE_S3_*` settings override these defaults; custom access
and secret keys must be supplied together. Staging and production reject the
local preset. `STORAGE_LOCAL_ROOT` is unused; local mode still stores bytes
in Silo.

### Output publication (optional)

Setting `GCP_PROJECT_ID` enables the durable Cloud Run publication worker. It
also requires `GCP_SERVICE_ACCOUNT_EMAIL` for Cloud Build and
`CLOUD_RUN_SERVICE_ACCOUNT` for published applications. The server itself uses
Google Application Default Credentials; inject those through the deployment
platform and keep the published-app identity free of Zenith Team data access.

| Variable | Default | Notes |
| --- | --- | --- |
| `GCP_PROJECT_ID` | empty | Empty disables publication |
| `GCP_REGION` | `us-central1` | Cloud Storage, Build, Artifact Registry, and Run region |
| `GCP_SOURCE_BUCKET` | `<project>-app-sources` | Immutable prepared source archives |
| `GCP_ARTIFACT_REGISTRY` | `<region>-docker.pkg.dev/<project>/user-apps` | Target repository |
| `GCP_SERVICE_ACCOUNT_EMAIL` | — | Required dedicated Cloud Build identity |
| `CLOUD_RUN_SERVICE_ACCOUNT` | — | Required dedicated runtime identity |
| `CLOUD_RUN_MAX_INSTANCES` | `10` | Range `1..100` |
| `CLOUD_RUN_MIN_INSTANCES` | `0` | Must not exceed max |
| `CLOUD_RUN_MEMORY` | `512Mi` | Cloud Run memory limit |
| `CLOUD_RUN_CPU` | `1` | `1`, `2`, `4`, or `8` |

| Variable | Default | Notes |
| --- | --- | --- |
| `STORAGE_DRIVER` | `s3` | `s3` for explicit configuration; `local` for the development Silo preset |
| `STORAGE_S3_BUCKET` | empty | Required in `s3` mode |
| `STORAGE_S3_REGION` | empty | Required in `s3` mode |
| `STORAGE_S3_ENDPOINT` | empty | Custom S3-compatible endpoint; omit for AWS S3 |
| `STORAGE_S3_ACCESS_KEY_ID` | empty | Set together with the secret key; omit both for ambient credentials |
| `STORAGE_S3_SECRET_ACCESS_KEY` | empty | |
| `STORAGE_S3_SESSION_TOKEN` | empty | For temporary credentials |
| `STORAGE_S3_USE_PATH_STYLE` | `false` | Required `true` for the local Silo endpoint |
| `ATTACHMENT_ABANDONED_READY_GRACE_SECONDS` | `86400` | Range `3600..2592000` (1h..30d); reclaim delay for completed-but-unlinked uploads |

### Custom publication URLs (optional)

After publishing an Output, its owner can choose a name in the **Custom URL**
section of the result page. Configure all four values together:

| Variable | Purpose |
| --- | --- |
| `CLOUDFLARE_API_TOKEN` | Token with Workers KV Storage Write permission for the routing namespace |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account identifier |
| `CLOUDFLARE_KV_NAMESPACE_ID` | Dedicated namespace for this Zenith Team database |
| `CLOUDFLARE_BASE_DOMAIN` | Hostname suffix, e.g. `apps.example.com`; no scheme, wildcard, path, or trailing dot |

Configure wildcard DNS, TLS coverage, and a Worker route for
`*.apps.example.com/*`, then bind that Worker to the same KV namespace.
Zenith Team writes the **full lowercase hostname** as the key:

```text
key:   my-app.apps.example.com
value: {"originUrl":"https://boardly-…run.app"}
```

Your Worker should:

1. Look up `new URL(request.url).hostname.toLowerCase()` in KV as JSON.
2. Return 404 when the key is missing. The value contains only `originUrl`;
   it contains no Zenith Team account data or credentials.
3. Proxy to `originUrl`, preserving the request path, query, method, and body.
   Use the origin hostname for the upstream Host header and TLS connection;
   pass the incoming hostname in `X-Forwarded-Host` and `https` in
   `X-Forwarded-Proto` for apps that support proxy-aware URL generation.
4. Forward responses and WebSocket upgrades. Keep redirects manual; handle
   origin-specific redirects/cookie domains in the app or your Worker if needed.

The KV update is durable and retried by Zenith Team. A Cloudflare write failure
never rebuilds the app; its original Cloud Run URL remains available. Name
claims use PostgreSQL uniqueness, not a read-before-write KV check. Names stay
reserved for their Output after unpublish and return on republish. Renaming or
moving a name to a different Output is not supported in this version.

Unpublish removes the route before deleting the origin. Cloudflare KV is
[eventually consistent](https://developers.cloudflare.com/kv/concepts/how-kv-works/):
changes, including deletions and previously cached misses, can take 60 seconds
or longer to appear at other locations. Avoid adding a longer Worker cache TTL
unless that delay is acceptable. Keep the account, namespace, and base domain
configuration stable for existing reservations; token rotation is supported.

### Google OAuth (optional; Web, Desktop, or both)

Leaving both client groups empty disables Google login. The Web group contains
its ID, secret, and redirect URI; the Desktop group contains its ID and secret.
Either group can be configured independently, but partial groups fail startup.
Web and Desktop client IDs must differ when both are enabled, and the Web
callback must be the exact absolute URI
`<api origin>/api/v1/auth/google/web/callback` over HTTPS (loopback HTTP is
accepted only in loopback development).

| Variable | Default | Notes |
| --- | --- | --- |
| `GOOGLE_WEB_CLIENT_ID` | — | |
| `GOOGLE_WEB_CLIENT_SECRET` | — | Secret |
| `GOOGLE_WEB_REDIRECT_URI` | — | Exact absolute callback URI |
| `GOOGLE_DESKTOP_CLIENT_ID` | — | Must differ from the web client ID |
| `GOOGLE_DESKTOP_CLIENT_SECRET` | — | Secret |
| `GOOGLE_OAUTH_FLOW_TTL_SECONDS` | `600` | Range `60..900` |
| `GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS` | `10` | Range `1..30` |

### Personal social accounts and immediate publishing (optional)

The server alone handles provider OAuth and stores encrypted grants in PostgreSQL.
Credential maintenance runs inside the API process; no extra queue or service is needed. The production
Compose file forwards all `SOCIAL_*` values to the server. Calendar requires none
of them. Set these in the deployment's secret store or environment, keeping the
encryption key identical across API replicas and stable across releases:

| Variable | Default | Notes |
| --- | --- | --- |
| `SOCIAL_ENABLED_PLATFORMS` | `x` | Comma-separated `x,facebook,instagram,linkedin`; do not enable providers before their app is ready |
| `SOCIAL_CALLBACK_ORIGIN` | — | Public HTTPS origin serving browser `/api/v1`, without a path, e.g. `https://app.example.com` |
| `SOCIAL_TOKEN_ENCRYPTION_KEY` | — | Base64 encoding of exactly 32 random bytes; required when a provider app is configured |
| `SOCIAL_X_CLIENT_ID`, `SOCIAL_X_CLIENT_SECRET` | — | Dedicated Zenith Team OAuth 2 web app |
| `SOCIAL_FACEBOOK_CLIENT_ID`, `SOCIAL_FACEBOOK_CLIENT_SECRET` | — | Optional Meta app pair |
| `SOCIAL_INSTAGRAM_CLIENT_ID`, `SOCIAL_INSTAGRAM_CLIENT_SECRET` | — | Optional Instagram app pair |
| `SOCIAL_LINKEDIN_CLIENT_ID`, `SOCIAL_LINKEDIN_CLIENT_SECRET` | — | Optional LinkedIn app pair |
| `SOCIAL_META_GRAPH_VERSION` | `v23.0` | Meta Graph version used by the configured apps |

Register the exact callback `${SOCIAL_CALLBACK_ORIGIN}/api/v1/social/oauth/x/callback`
in the dedicated Zenith Team X app. Use the same browser-visible API host that sets
the OAuth binding cookie: when the web app proxies `/api` at `https://app.example.com`,
set `SOCIAL_CALLBACK_ORIGIN=https://app.example.com`, not a different backend hostname.
`AUTH_WEB_APP_ORIGIN` must be the public web origin and appear in `AUTH_WEB_ORIGINS`.
Electron opens the system browser through a single-use launch URL, then returns
through a random loopback port; provider consoles still register only the server
callback, never the random desktop URL. Desktop connect uses the current desktop
session and does not require another web login.

Keep `SOCIAL_TOKEN_ENCRYPTION_KEY` and every `SOCIAL_*_CLIENT_SECRET` in the secret
store. Client IDs, callback origin, enabled platforms and Graph version are ordinary
deployment configuration (keeping client IDs in the secret store is also fine).
Do not put these values in frontend `VITE_*` variables. Use a separate X app for
Zenith Team so another product's permissions and grants remain independent.

Provider consent now requests publishing permissions:

| Provider | Required scopes |
| --- | --- |
| X | `users.read tweet.read tweet.write media.write offline.access` |
| Facebook | `public_profile pages_show_list pages_read_engagement pages_manage_posts` |
| Instagram | `instagram_business_basic instagram_business_content_publish` |
| LinkedIn | `openid profile w_member_social` |

Existing accounts must reconnect once to grant the new scopes. Facebook,
Instagram and LinkedIn have implemented adapters but need their app credentials,
provider product/permission access, and explicit enablement. Apps awaiting public
review can only authorize permitted test accounts; keep public rollout X-only.

The server applies migration `000034_social` through normal startup. Deploy the
server and updated desktop/daemon/MCP bundle together, then:

1. Verify `/api/v1/me/social/platforms` reports X available for a signed-in user.
2. Connect or reconnect in **More → Social accounts**, and verify browser return
   focuses the app and updates the account.
3. Start a fresh agent chat with Work coordination enabled. Ask it to list available
   social accounts. Only when ready for a real public post, explicitly instruct it
   to publish a chosen test message to the chosen account. A `published` receipt
   returns the provider ID/link; other statuses are not confirmation of posting.
4. Media publishing uses existing attachment storage. X accepts JPEG/PNG images
   or one MP4 video up to 512 MB. X/LinkedIn upload bytes;
   Meta needs externally accessible HTTPS signed object URLs. Local-only MinIO
   URLs cannot be fetched by Meta.

X refresh tokens rotate automatically; Instagram refreshes long-lived access
tokens. LinkedIn programmatic refresh only works when its app receives a refresh
token; Facebook requires reconnect when a grant expires. Revoked grants also
require reconnect. DB leases coordinate API replicas, and transient errors retry
without deleting credentials. Preserve the encryption key across releases and
replicas; changing it requires credential migration or reconnecting all accounts.
No new environment variables or scheduler infrastructure are required for refresh
or immediate posting beyond the existing `SOCIAL_*` configuration.

### Daemon coordination (server side)

There are no server-side tuning variables for daemon coordination. The
runtime sweeper runs on a fixed 30-second loop
(`server/cmd/server/runtime_sweeper.go`), and the claim/prepare-lease
timings are constants in the run kernel. Everything an operator can change
lives on the user's machine as `BOARDLY_*` environment variables read by the
daemon (`zen daemon start --help`, or `zen config show`).

## Operate

### Restore a self-deleted account

Account deletion is indefinite soft deletion. It retains Companies,
memberships, messages, Runs, attachments, and attribution, while revoking all
human sessions and daemon credentials. There is no automatic purge and no
public Admin restore endpoint.

A trusted internal `account.Service.RestoreAccount(ctx, userID)` operation is
proposed in the [admin console spec](../docs/specs/admin-console/PRD.md) and is
not implemented yet; until it lands the SQL path below is the only restore path.
When it exists, prefer it. Before invoking it, identify the User by immutable UUID and confirm
that `deleted_at` and `disabled_at` are both non-null and exactly equal. The
service refuses any other state so an independent or later administrative
disable cannot be cleared accidentally.

For an approved break-glass database restore, take a backup and run the
equivalent transaction interactively, substituting the exact UUID:

```sql
BEGIN;

SELECT id, email, deleted_at, disabled_at, version
FROM users
WHERE id = '00000000-0000-7000-8000-000000000000'::uuid
FOR UPDATE;

UPDATE users
SET deleted_at = NULL,
    disabled_at = NULL,
    updated_at = clock_timestamp(),
    version = version + 1
WHERE id = '00000000-0000-7000-8000-000000000000'::uuid
  AND deleted_at IS NOT NULL
  AND disabled_at = deleted_at
  AND version < 9007199254740991
RETURNING id, email, version;
```

Proceed only when the `UPDATE ... RETURNING` emits exactly one row, then issue
`COMMIT`; otherwise issue `ROLLBACK` and investigate the lifecycle state.
Restore does not modify
`auth_sessions`, `run_tokens`, memberships, or business data. The user must
log in again, and every daemon must re-authenticate with `zen auth login`.

### Health endpoints

| Endpoint | Meaning |
| --- | --- |
| `GET /healthz` | Liveness: `200 {"status":"ok"}` whenever the process serves |
| `GET /readyz` | Readiness: `200 {"status":"ready"}`, or `503` with error code `service_not_ready` |
| `GET /metrics` | Prometheus text format |

`/readyz` fails when PostgreSQL is unreachable, when the applied migration
version does not match the version embedded in the binary (or is marked
dirty), or when a required dependency's health check fails. Successful checks
are cached briefly so probe traffic does not amplify into the database. During
shutdown the server drains readiness first, then finishes
in-flight requests within `APP_SHUTDOWN_TIMEOUT_SECONDS`.

Point liveness probes at `/healthz` and load-balancer/traffic probes at
`/readyz`. The container healthcheck already probes `/readyz` via
`boardly-readyz` (override its target with `APP_READYZ_URL`).

### Logs

One JSON object per line on stdout (`log/slog`); bootstrap failures may land
on stderr. Every HTTP request logs `msg="HTTP request"` with `method`,
`route` (template, not raw path), `status`, `duration_ms`, and `request_id`;
probe paths (`/healthz`, `/readyz`, `/metrics`) are not logged. Background
components log with `component` and `error_code` fields. Logs never contain
tokens, provider credentials, attachment keys, or local filesystem paths.
The production compose caps json-file logs at 5 × 50 MB per container.

### Metrics and dashboard

Import `grafana-boardly.json` into Grafana and point it at the Prometheus
that scrapes `/metrics`. Application series (all low-cardinality by
construction):

| Metric | Labels |
| --- | --- |
| `boardly_http_requests_total` | `method`, `route`, `status` |
| `boardly_http_request_duration_seconds` | `method`, `route` (histogram) |
| `boardly_run_tokens_total` | `provider`, `kind` |
| `boardly_run_terminal_total` | `provider`, `outcome`, `failure_kind` |

Standard Go (`go_*`) and process (`process_*`) collectors are registered on
the same endpoint. Useful alerts: `/readyz` failing for more than a minute,
5xx ratio, p99 latency, `rate(boardly_run_terminal_total{outcome="failed",
failure_kind="infrastructure"}[15m]) > 0`.

## Upgrade

1. Build and tag the new image; update `APP_SERVER_IMAGE` in `deploy/.env`.
2. `docker compose -f docker-compose.production.yml pull server` (or rebuild).
3. `docker compose -f docker-compose.production.yml up -d server` — the new
   container applies migrations, passes its healthcheck, and serves.
4. Verify `/readyz`, then watch the dashboard for error-ratio or latency
   regressions.

Before upgrading, take a database backup and inspect `schema_migrations`
(`version`, `dirty`). Compare pending migrations with the deployed version;
an older deployment may have additional migrations from `main` to apply before
the feature migration. Preserve previously applied files and existing data;
ordinary feature upgrades do not recreate PostgreSQL.

Social migration `000034` upgrades version 33 by creating four tables and their
indexes only. Calendar migration `000035` then adds custom recurrence fields with
defaults that preserve existing event schedules. Calendar reminders continue to
run inside the API process; no additional environment variables or workers are
required. Deploy the matching server, web bundle and Desktop/daemon/MCP
bundle. Readiness requires the binary's exact embedded schema version, so do
not assume older replicas remain ready after a schema upgrade or promise a
zero-downtime rollout without checking that transition. Multi-node deployments
continue to use shared Redis and object storage; concurrent starters serialize
migrations through the PostgreSQL lock.

## Rollback

- **Binary rollback**: point `APP_SERVER_IMAGE` back at the previous tag only
  when its required schema version and public wire contract still match.
  A version-33 or version-34 binary fails readiness against schema version 35
  even when the schema changes are additive.
- **Schema rollback**: review the specific down migration and the writes made
  since deployment. Social's down migration drops Social records; do not run
  it automatically to roll back an application image. Calendar's version-35 down
  migration removes custom recurrence settings, so existing custom series would
  fall back to their base frequency. A backup restore also
  discards writes made after that backup and requires a coordinated recovery
  with the matching server/web/Desktop set. Prefer a forward fix that keeps
  the current schema when possible.
- A rollback must restore configuration that can read the same object keys.
  Treat a storage-backend migration as a separate, explicit procedure rather
  than changing `STORAGE_*` coordinates during binary rollback.

## Backup and restore

PostgreSQL is the only structured-state authority; attachments live in
object storage. Back up PostgreSQL first, then objects — an object created
after the dump is harmless on restore, while restoring a newer database
against older object storage would reference missing attachment bodies.

```bash
# PostgreSQL (bundled service shown; use your managed snapshot tooling otherwise)
docker compose -f docker-compose.production.yml exec postgres \
  pg_dump -U boardly -d boardly --format=custom --file=/tmp/boardly.dump
docker compose -f docker-compose.production.yml cp postgres:/tmp/boardly.dump ./boardly-$(date +%F).dump

# Restore into a fresh database, then start the server
pg_restore --clean --if-exists -U boardly -d boardly ./boardly-YYYY-MM-DD.dump
```

For attachments, prefer bucket versioning plus lifecycle rules or cross-region
replication; `aws s3 sync`/`mc mirror` works for periodic copies.
Redis holds only transient relay and claim-cache state and needs no backup; a
flush costs at most a reconnect and a brief fanout gap.

## Retention defaults

Current retention behavior, restated for operators:

- Conversation messages and terminal Run outcomes persist until their
  company is deleted.
- Structured Run transcript items are retained with their Run. The owned-child
  foreign key removes them if the Run is deleted; Runs currently have no
  deletion endpoint. Ephemeral same-machine `run.preview.delta` frames are
  never stored or replayed.
- Provider adapters emit ordered deltas, completed items, session checkpoints,
  and one terminal event. The daemon sends each completed item separately over
  its authenticated WebSocket; PostgreSQL commits and acknowledges it before
  the next item. The server applies canonical redaction and enforces the 1 MiB
  completed-item limit before persistence.
- Message-linked attachments persist until deletion; pending uploads expire
  after 1 hour of initiate inactivity; completed-but-unlinked uploads are
  reclaimed after `ATTACHMENT_ABANDONED_READY_GRACE_SECONDS` (default 24h).
- Immutable `run_usage` facts are retained indefinitely. Reports aggregate
  those facts directly; there is no usage pruning or rollup worker.
- Raw provider events are never uploaded or persisted; bounded local
  Daemon logs rotate by size today (10 MiB, one `.old` generation, rotated at
  daemon start; an age-based policy is Planned in the
  [observability spec](../docs/specs/observability/PRD.md)); opt-in crash/freeze reports
  retain 30 days and contain no prompt, message, path, email, or tool
  payload.
