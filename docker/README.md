# Docker development stack

The development stack provides PostgreSQL 17, Redis, and Silo. Start it with
`make infra-up`; the command waits for all long-running services to become
healthy and creates the private `boardly-attachments` bucket.

The stack is deliberately infrastructure-only: the API server and the
daemon are **not** containerized in development. Run the server from source
against this stack (below) so code changes take effect on restart without
image rebuilds; the daemon likewise runs from source via
`zen daemon start`. Production containers live in
[`../deploy/`](../deploy/README.md); day-to-day workflows are in
[`../AGENTS.md`](../AGENTS.md).

Default local connection settings:

```text
PostgreSQL  postgresql://boardly:boardly@localhost:5432/boardly?sslmode=disable
Redis       redis://localhost:6379/0
Silo API    http://localhost:9000
Silo UI     http://localhost:9001
S3 key      boardly
S3 secret   boardly-development-secret
S3 bucket   boardly-attachments
```

Host ports can be overridden with `APP_POSTGRES_PORT`,
`APP_REDIS_PORT`, `APP_MINIO_PORT`, and
`APP_MINIO_CONSOLE_PORT` (legacy port override names). `make infra-down` preserves volumes;
`make infra-reset` removes them. These credentials are development-only.

The Silo service reuses the existing `minio-data` volume. Its name is retained
so a previous MinIO development bucket is not silently replaced with an empty
one. `make infra-up` removes the old Compose container while keeping the
volume. Back up any development objects you need before upgrading the storage
image, and check the [Silo migration guide](https://silo.pgsty.com/compatibility/migration/)
for version-specific requirements. `make infra-reset` deletes them.

The simplest way to run the API against this stack is `make server`, which
loads these values from [`dev.env`](./dev.env) and applies migrations at
startup. `make server-local` uses `server/.env`; set `STORAGE_DRIVER=local`

For local Huddle testing, run LiveKit with the webhook-enabled config so
participant joins grant the Huddle chat permission:

```bash
livekit-server --config docker/livekit.dev.yaml
```
to use this Silo service with native PostgreSQL. This preset requires
`APP_ENVIRONMENT=local` and supplies the bucket, region, endpoint, development
credentials, and path-style requests shown below. Explicit `STORAGE_S3_*`
settings override the preset; remove old placeholder values before enabling it.
`STORAGE_LOCAL_ROOT` is not used: bytes live in Silo's Docker volume.
The equivalent explicit
Docker-infra invocation from `server/`:

```bash
APP_DATABASE_URL='postgresql://boardly:boardly@localhost:5432/boardly?sslmode=disable' \
AUTH_CSRF_SECRET='development-only-32-byte-secret!' \
APP_REDIS_URL='redis://localhost:6379/0' \
STORAGE_S3_BUCKET='boardly-attachments' \
STORAGE_S3_REGION='us-east-1' \
STORAGE_S3_ENDPOINT='http://localhost:9000' \
STORAGE_S3_ACCESS_KEY_ID='boardly' \
STORAGE_S3_SECRET_ACCESS_KEY='boardly-development-secret' \
STORAGE_S3_USE_PATH_STYLE='true' \
go run ./cmd/server
```

Zenith Team has one S3-compatible storage adapter. Silo and AWS S3 differ only by
configuration: set the endpoint and path-style mode for Silo, or omit the
endpoint and use virtual-hosted requests for AWS. Every API replica must use
the same bucket and endpoint. Moving existing objects to another backend is an
explicit operator migration performed before switching these coordinates.

Completed uploads that are never linked to a Message are deleted after
`ATTACHMENT_ABANDONED_READY_GRACE_SECONDS` (default `24h`, accepted range
`1h..720h`). Linked attachments are retained.

Redis remains optional while `APP_API_REPLICAS=1`. Setting replicas above
one makes Redis subscription readiness and fail-closed publication mandatory;
all replicas must still be configured against the same object store.
Authentication defaults to a 15-minute access credential and a 7-day rotating
refresh credential; override them with `AUTH_ACCESS_TOKEN_TTL_SECONDS` and
`AUTH_REFRESH_TOKEN_TTL_SECONDS`. Cookie `Secure` is derived from
`AUTH_WEB_ORIGINS`: loopback HTTP development stays usable, HTTPS origins
force secure cookies, and non-loopback HTTP origins are rejected.

Google login is disabled when both the Web group and Desktop pair are empty.
Configure either surface independently, or both. To enable Web login for the
host-run API, register
`http://localhost:8788/api/v1/auth/google/web/callback` as an authorized redirect
URI on a Google OAuth client of type **Web application**, and
inject the provider credentials from your shell or development secret store:

```bash
export AUTH_WEB_APP_ORIGIN='http://localhost:5173'
export AUTH_WEB_ORIGINS='http://localhost:5173,http://127.0.0.1:5173'
export GOOGLE_WEB_REDIRECT_URI='http://localhost:8788/api/v1/auth/google/web/callback'
export GOOGLE_WEB_CLIENT_ID="${GOOGLE_WEB_CLIENT_ID}"
export GOOGLE_WEB_CLIENT_SECRET="${GOOGLE_WEB_CLIENT_SECRET}"
export GOOGLE_DESKTOP_CLIENT_ID="${GOOGLE_DESKTOP_CLIENT_ID}"
export GOOGLE_DESKTOP_CLIENT_SECRET="${GOOGLE_DESKTOP_CLIENT_SECRET}"
```

For Desktop-only login, only the two `GOOGLE_DESKTOP_*` values are
required. Every selected surface must have its complete credential group, and
the Web and Desktop client IDs must differ when both are configured.
`GOOGLE_OAUTH_FLOW_TTL_SECONDS` defaults to `600` and accepts `60..900`;
`GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS` defaults to `10` and accepts `1..30`.
Loopback HTTP callbacks are accepted only when the Web app origin is also
loopback HTTP and secure cookies are not enabled.
Use `http://localhost:5173` for the web app and `http://localhost:8788` for
`VITE_API_URL` so the start request and Google callback share their binding
cookie host. Do not include HTTPS origins in this local configuration: they
force secure cookies and prevent the HTTP callback from being enabled.
For `make server-local`, put these values in the ignored `server/.env` and
restart the API after changing them. `GET /api/v1/auth/providers` should then
report `webEnabled: true` for Google.
