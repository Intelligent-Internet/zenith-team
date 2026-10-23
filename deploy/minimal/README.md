# Private minimal deployment

This is a tested hardening baseline for a **private pilot**, not a claim that the
whole product is ready for unrestricted enterprise production. It preserves the
existing Company, Project, Task, Run, approval, session and PostgreSQL contracts.
Read the [repository assessment](../../docs/audit/README.md) before production use.
**Current pinned Caddy and Silo images still have known high-severity scanner
matches. Use this for non-sensitive validation; patch/rebuild and rescan before
company data.** See the [image report](../../docs/audit/container-images.md).

## What changes

- The API builds with `zenith_minimal,nomsgpack`: Google OAuth, Google Cloud Run
  publishing and Gin's MessagePack codec are absent from its import graph.
  Configuring either removed Google provider causes startup to fail closed.
- The UI is delivered as static browser files; no Electron or Python/LiveKit
  service runs in this stack. The current full frontend source/lockfile and rich
  browser bundle are retained. This is **not yet a separate minimal UI edition**.
- Five services remain: web, Go API, PostgreSQL, Redis and S3-compatible Silo.
  One short-lived storage initializer creates the bucket. Redis is required by
  scheduling and realtime; S3 stores attachments, outputs and skills. Their removal
  requires architectural work rather than a Compose edit.
- Every running service uses a non-root user, a read-only root filesystem,
  dropped Linux capabilities, no-new-privileges and CPU/memory/process limits.
  Only named data volumes and bounded temporary filesystems are writable.
- The web port binds only `127.0.0.1:8080`. The API has no host port. The web
  container cannot join the data network. The API and data services have only
  internal Docker networks and cannot make ordinary Internet connections.
- Signup is closed by default. Public artifact URLs, Google/social callbacks,
  inbound webhooks and voice workload endpoints are rejected at the web proxy.
  Route ordering is explicit so the denials precede the API proxy.
- Configuration is an explicit allowlist. Host cloud/provider keys and optional
  integrations in `deploy/.env` are not automatically passed to the API.
- All external images, including builders, use registry-verified digests captured
  on 23 October 2026. The Go builder contains 1.26.10. Local output image tags are
  names for builds from this checkout; publish and promote immutable app digests
  through your release process before a production rollout.

## Start and enroll

Use Docker Compose with support for `internal` networks and the service security
options in this file. Run commands from the repository root:

```sh
make selfhost-env                  # once; never overwrites deploy/.env
GOTOOLCHAIN=go1.26.10 make minimal-check
ZENITH_TEAM_ALLOW_SIGNUP=true make minimal-up
```

Open `http://localhost:8080`, create the approved operator account(s), then close
enrollment:

```sh
make minimal-up                    # default ZENITH_TEAM_ALLOW_SIGNUP=false
make minimal-status
```

Keep the flag out of your saved environment. Closing signup affects new accounts;
it does not disable login or revoke existing sessions. This profile does not
verify email or deliver invitations/password-reset email. Treat enrollment as an
operator-controlled action on a private machine, not as proof of mailbox ownership.
Do not give Company access based solely on a claimed email address.

`make minimal-down` stops the stack and preserves its data. The minimal Compose
project uses its own named volumes; it does not migrate an existing production
installation. Never combine this file with the standard production Compose file:
merge rules could restore exposed ports, services or inherited credentials.

For a private remote deployment, terminate TLS at a controlled ingress on the host,
restrict that ingress to your VPN/access network, and set `AUTH_WEB_APP_ORIGIN`
to its exact HTTPS origin in `deploy/.env`. HTTPS origins cause secure cookies.
The backend receives the browser's original `Origin`; do not broaden the origin
allowlist. Configure and validate forwarded-host trust only if your proxy setup
needs it; never trust arbitrary external forwarded headers.

## Connect an execution machine

Agent work still needs the `zen` daemon and its supported provider CLI. Build
those through the repository's normal, verified tool preparation/build process.
Point `zen auth login --server` at the same private origin used by the browser,
then start the daemon as described in the [self-hosting guide](../../SELF_HOSTING.md).
The daemon API is deliberately retained. No provider login, company task or paid
agent execution is performed by this deployment setup.

Run the daemon on a disposable dedicated VM under an unprivileged account with
only the approved project files and provider credentials. Its current sandbox is
**not** a tenant boundary: Linux/Windows and failed sandbox probes can run without
the macOS protection, and the macOS policy is not a whole-host deny-by-default
sandbox. Do not put unrelated Company secrets, production cloud credentials,
SSH agents or the Docker socket on an execution machine. Multi-company shared
workers require a stronger isolation design before use.

## Deliberate restrictions and remaining work

The API's Internet egress restriction disables GitHub skill imports, remote logo
fetching, social publishing, outbound mail and other Internet integrations. The
provider CLI runs on a separate machine and retains its own required network
access. Offline failures can still be visible in the full UI; hiding/removing
those features belongs to the separate minimal frontend edition.

The proxy bounds request headers to 5 seconds, whole request reads to 2 minutes
and idle connections to 2 minutes. A separate 1-minute body-idle deadline
terminates stalled uploads even when the whole-body deadline has not elapsed. Large uploads on slow links can time out before
the normal API's longer size-based deadline; this is intentional for this profile.
Streaming/WebSocket operation is retained. Default API listener body deadlines
outside this profile remain a separate hardening task.

PostgreSQL currently uses its bootstrap superuser for migrations and application
queries. The API also has the Silo bucket's administrative credential. Network
isolation limits exposure but does not turn these into least-privilege credentials.
Before production, split migration/runtime DB roles and provision a bucket-scoped
S3 key, test their permissions, and add encrypted backups with restore drills.
Redis has no authentication inside its isolated network. Docker host administrators
can inspect environment secrets; move production secrets into your managed secret
and workload-identity system with a documented rotation process.

The image pins must be updated and scanned regularly. Scan the final images and
provider CLI/desktop artifacts, retain SBOMs and attestations, and verify restore,
revocation, abuse limits, worker isolation, SSO/MFA and audit export before expanding
access. A smaller dependency graph does not eliminate unknown vulnerabilities.

## Verification

```sh
make minimal-check
python3 scripts/check-minimal-deployment.py
```

The second command is read-only and expects the default closed-signup stack to be
running at `http://localhost:8080` (or `ZENITH_TEAM_MINIMAL_WEB_PORT`). It checks HTTP
policy, actual container restrictions, exposed ports and network membership.
It does not authenticate or start agent work.

## Bounded local stress probe

Run `python3 scripts/stress-minimal-deployment.py` only against this disposable
local stack. It temporarily opens loopback enrollment, creates two temporary
accounts/Companies, closes enrollment, sends 10,000 requests at up to 32 concurrent
workers, checks 12 slow connections, and deletes the temporary accounts. It does
not execute an agent or contact a model provider. The run takes a few minutes and
writes a credential-free JSON report under `docs/audit/evidence/`. It also leaves
the stack running so you can inspect it; stop API/web services after your checks.
These are bounded local correctness/recovery checks, not a production capacity
benchmark or long-duration soak. See [stress results](../../docs/audit/http-stress.md).
