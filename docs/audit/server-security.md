# Zenith Team server security review — 23 October 2026

This review covers the local checkout's server authentication, request admission,
Company/Project/Run authorization boundaries, selected upload and outbound HTTP
paths, and HTTP listener configuration. It combines source review with the
existing tests and new targeted regressions. It is not an exhaustive proof of
every endpoint, a penetration test of a live deployment, or a certification.
Dependency vulnerability results belong to the wider repository audit.

## Findings and disposition

| Severity | Finding | Evidence | Disposition |
|---|---|---|---|
| High | OAuth email-only linking could retain an attacker's password and sessions when the real mailbox owner signed in | `server/internal/auth/oauth_login.go`; `service.go:createImmediateSignup`; PostgreSQL regression reproduced both unverified and marked-verified cases | Fixed prospectively: new subjects cannot link to an existing account by email alone |
| High for a public API without compensating limits | Public password operations had no shared work budget | `server/internal/handler/auth.go:172`; `server/internal/auth/service.go`; `password.go:29`; reset hashing also preceded token lookup | Fixed resource amplification: four concurrent password operations per API service; excess calls receive 503 + Retry-After |
| Medium | Only headers had an HTTP read deadline; slow body reads could retain connections and handlers | Original `server/cmd/server/app.go:871` set only `ReadHeaderTimeout: 5s`; JSON byte caps do not bound read duration | Mitigated at the minimal proxy with 5s headers / 2m body reads / 2m idle; default direct API listener remains unchanged and slow large uploads have a documented tradeoff |
| Medium, enterprise identity hardening | A no-email configuration accepts public password signup and stamps the supplied email verified | `server/internal/config/email.go:53`; `server/cmd/server/auth.go:newEmailAuthOptions`; `server/internal/auth/service.go:createImmediateSignup` | Still present; restricted enrollment and real mailbox/IdP verification are required before relying on email as an organizational identity |
| Low, conditional exposure | Prometheus metrics have no application authorization | `server/internal/handler/handler.go:192`, `server/cmd/server/app.go:770` | Keep the API/metrics listener private or protect metrics at ingress. Default self-host ports bind loopback and Caddy forwards only `/api/*` |

### OAuth account pre-registration

The original code accepted password signup without an email service and wrote
`email_verified_at` immediately. With Google enabled independently, first Google
sign-in searched for the provider email and attached the new provider subject to
the matching existing account. It preserved the password hash and live sessions.

An attacker could register another person's email before that person arrived.
When the real person later used Google sign-in, both parties controlled the same
Zenith Team account. Any Company access later granted to that account became
available through the attacker's credentials. The attacker did not need the
victim's Google credentials. Preconditions were public signup, signup without
email verification, and a Google flow on the same database; enabling email
verification later would not remove historically created accounts.

The new PostgreSQL regression
`TestOAuthLoginNeverLinksAnExistingAccountByEmailAlone` failed on the original
code for both a null verification timestamp and a populated one: each attempt
returned a successful login. The fix returns the existing
`oauth_account_conflict` error before identity/session writes whenever an
unrecognized provider subject matches an existing account email. The test now
checks no identity or session was added, no password/verification state changed,
and the existing session was not silently revoked. Existing subject-based
Google login and new-account creation retain their normal paths.

Compatibility tradeoff: a password account can no longer acquire a Google link
merely by using the Google sign-in button. An explicit linking flow must prove
control of both the existing account and provider identity before this can be
offered safely; that flow is not implemented here.

The fix does **not** repair previously compromised or implicitly linked accounts.
Operators who enabled this configuration must examine existing password+OAuth
accounts and use controlled recovery, password reset and session revocation when
ownership is uncertain. Automatically deleting links or credentials would be an
unsafe migration without deployment-specific evidence.

### Password resource exhaustion

Login deliberately performs dummy Argon2id verification for invalid or unknown
emails. That is good for timing consistency, but every request previously ran
without a concurrency bound. Each default hash uses 19 MiB of Argon2 memory and
two passes; accepted stored costs can use 64 MiB. Signup and a structurally valid
password-reset token also reach hashing before account/token rejection. Parallel
unauthenticated requests could therefore consume process memory and CPU even
without finding an account.

`password_work.go` now uses a standard-library channel shared by the composed
AuthService for signup, all login verification branches and password reset.
Four admitted operations bound active default Argon2 working memory to roughly
76 MiB, or 256 MiB at the maximum accepted stored memory cost; these figures do
not include general process memory, retained allocations or garbage-collector
overhead. Requests above the budget fail immediately, so an unbounded waiting
queue does not replace the original allocation problem. Cancelled requests do
not start new work. An admitted operation retains its slot until hashing ends,
because Argon2 itself cannot be interrupted by context cancellation.

Deterministic regression tests hold four real Login calls inside the injected
password manager, then verify that login, signup and password reset all refuse
excess work without invoking another hash. Additional tests cover cancellation,
slot release after hashing failures and the API's `503 authentication_busy` /
`Retry-After: 1` response. No new dependency was introduced.

This is a resource budget, **not** credential-stuffing protection or fair
scheduling. Sustained attackers can still occupy all four slots; valid-email
login requests still reach the database before password-work admission. Add
deployment-wide IP/account request budgets, signup controls and operational
alerting before exposing an enterprise deployment publicly.

### Request duration and operations visibility

JSON parsing uses `http.MaxBytesReader`, strict object decoding, rejection of
unknown fields and per-route limits. Multipart attachment input has a 100 MiB
file cap, bounded metadata and strict part ordering. Those byte limits do not
stop a peer from sending allowed bytes indefinitely when no socket/body read
deadline exists. The original HTTP server bounded header reads only. The
`RequestTimeout` context middleware is selective and cannot itself interrupt a
blocking read on a request socket.

A deadline fix must account for supported uploads: attachment service deadlines
allow two minutes plus the declared size at 128 KiB/s (up to roughly fifteen
minutes). WebSocket connections have their own read limits and ping/pong
deadlines. A short blanket request timeout would regress legitimate uploads or
streaming; test both ordinary incomplete JSON and the documented large-upload
path.

`/health/realtime` already protects remote access with a configured token or
direct-loopback admission. `/metrics` does not use that guard. Its metrics are
operational counters, not an established credential leak; the concern is process
and activity visibility if an operator exposes the API port directly. Keep
scraping on a management network rather than adding public observability routes.

## Existing controls worth preserving

- Authentication pins JWT algorithm, issuer, audience, purpose and expiration;
  each access token also resolves an active server-side session. Refresh tokens
  contain 32 random bytes and are stored via a keyed hash. Session rotation and
  revocation have transaction/concurrency tests.
- Cookie writes use HttpOnly for credentials, SameSite and configurable Secure;
  unsafe cookie requests require an HMAC-bound double-submit CSRF token. CORS
  uses exact origin matches. Daemon authentication is header-only.
- Company, Project and Run authority are distinct checks on the canonical user
  principal. Route actor declarations reject Run proofs on human-only routes;
  daemon sessions cannot silently become their human. Mutating domain flows
  use membership/Company locks and SQL access rules. Cross-Company and Run-scope
  regression suites exist. No bypass was established in this review.
- WebSocket admission explicitly distinguishes credential paths, uses origin
  checks and trusted-proxy configuration, and bounds frames/read lifetimes.
- Attachment downloads enforce authorization and use attachment disposition,
  `nosniff`, and private/no-store headers. Upload metadata and object keys do not
  grant access on their own.
- GitHub skill import accepts exact GitHub origins, builds requests to fixed
  GitHub API/codeload hosts, refuses redirects and caps response/archive sizes.
  The examined social upload path validates LinkedIn upload hosts and rejects
  redirects. No general attacker-controlled server fetch URL was established in
  these reviewed paths; this is not a claim that all outbound integrations are
  SSRF-proof.
- Cloud publication requires explicit exposure confirmation and service-side
  authorization, has bounded archive size/entry expansion, rejects traversal,
  duplicate paths and symlinks, and uses dedicated configured service accounts.
  Executing published applications is nevertheless a separate trust boundary;
  exclude that integration from a minimal company-control deployment.

## Verification recorded

- Original OAuth regression reproduced against PostgreSQL before the fix: both
  existing-email cases incorrectly returned success.
- `go test -race ./internal/auth`: passed with the password-work fix.
- `go run ./tests/testpostgres go test -race -tags=integration ./internal/auth -count=1`:
  passed after the OAuth fix, including existing session, reset and OAuth suites.
- `go test -race ./internal/middleware`: passed.
- `go test -race ./internal/handler -count=1`: passed after correcting the new
  test's request-id fixture; includes route actor, cross-Company and API contract
  checks.
- `go test -race -tags zenith_minimal ./internal/auth ./internal/middleware ./internal/handler -count=1`:
  passed.
- Final repository gates, container testing and dependency scanning are recorded
  by the parent audit separately.

For enterprise use, the remaining priorities are controlled enrollment, verified
organizational identity with strong authentication, a recovery/linking design,
perimeter rate limits, tenant-aware quotas, tested backup/restore, isolated
agent execution, least-privilege outbound integrations and auditable releases.
Reducing module count complements these controls; it does not substitute for
them.
