# Authentication stress validation — 23 October 2026

The authentication hardening passed repeated concurrency tests with the Go race
detector. No additional production change was needed. Two co-located backend
test files were added; the full repository gate and deployed HTTP load tests run
separately.

## Environment and limits

- Go `go1.26.10`, `darwin/arm64`; the host's default Go is older, so every test
  command explicitly selects the patched toolchain.
- `GOMAXPROCS=2`; the password repetition also explicitly tests scheduler widths
  one and two. At most 48 overload callers or 12 database callers run together.
- PostgreSQL through the repository's `tests/testpostgres` harness, image
  `postgres:17-alpine`, local image ID
  `sha256:eee6c6e1b0894fa4fe0075eb054af9fd1cf39a35321c2d4110626cfe506411fb`.
  Both temporary database containers were stopped and removed by the harness.
- The admission stress uses a controlled password-manager test double to hold
  operations in flight without allocating production Argon2 working memory.
  The password/OAuth transaction race uses real Argon2 at a deliberately small
  64 KiB test cost. These are concurrency correctness tests, not capacity or
  production hashing-throughput benchmarks.

## Results

| Check | Work performed | Result |
|---|---|---|
| Default password admission stress | 50 runs; 600 overload/recovery cycles; 31,200 service calls | Passed in 1.612 seconds; no data-race reports |
| Minimal password stress and earlier overload/cancellation regressions | 100 repetitions at each of CPU widths 1 and 2; the new stress case alone exercised 2,400 cycles and 124,800 calls | Passed in 3.451 seconds; no data-race reports |
| Default PostgreSQL OAuth race tests | 20 repetitions of three scenarios; 560 simultaneous-start account requests in total | Passed in 36.266 seconds; no data-race reports |
| Minimal PostgreSQL OAuth race tests plus the original account-collision regression | 10 repetitions; 280 racing requests, plus 20 sequential checks for unverified/legacy-verified accounts | Passed in 24.582 seconds; no data-race reports |
| Auth package lint, including integration tests | Pinned repository linter, new changes relative to HEAD | Passed; zero issues |
| Formatting | Pinned repository formatters on both new test files | Passed |

The password test fills all four work slots with a mix of login, signup and
reset operations, cancels their caller contexts, then launches 48 competing
calls. Half are already cancelled and must return `context.Canceled`; the other
half must return `ErrPasswordWorkBusy` without entering the password manager.
Cancellation of an operation already inside the password manager must not free
its slot early. The test checks an exact peak of four, releases the blocked work,
checks zero work remains active, and repeats twelve times. All 48 intended
operations enter; none of the 576 competing calls does. Successive rounds prove
the budget recovers without leaking slots.

The database scenarios cover:

1. Eight new Google subjects race for the same email: exactly one account,
   identity and session is created; seven calls return
   `ErrOAuthAccountConflict`.
2. Six calls from an already-linked Google subject race with six unlinked
   subjects claiming its email: the existing subject gets six sessions on its
   original account; every unlinked subject is rejected; no extra account or
   identity appears.
3. Four password signups race four new Google subjects for one email: exactly
   one account and one session wins. A Google winner has no inherited password;
   a password winner acquires no Google identity. All losers return their
   established conflict errors. Both winner orderings occurred: default runs
   had 12 Google and 8 password winners; minimal runs had 2 Google and 8 password
   winners.

## Reproduction

Run from `server/` after normal repository tool-content preparation:

```sh
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race \
  -run '^TestPasswordWorkStress' -count=50 -shuffle=on ./internal/auth

GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -tags=zenith_minimal \
  -run 'TestPasswordWorkStress|TestPasswordWorkBudget|TestCancelledRequestsDoNotStartPasswordWork|TestFailedPasswordHashReleasesWorkBudget' \
  -count=100 -cpu=1,2 -shuffle=on ./internal/auth

GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go run ./tests/testpostgres \
  go test -race -tags=integration ./internal/auth \
  -run '^TestOAuthConcurrent' -count=20 -shuffle=on -v

GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go run ./tests/testpostgres \
  go test -race -tags='integration zenith_minimal' ./internal/auth \
  -run '^TestOAuthConcurrent|^TestOAuthLoginNeverLinksAnExistingAccountByEmailAlone$' \
  -count=10 -shuffle=on -v

GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 ../.tools/bin/golangci-lint \
  run --concurrency=2 --new-from-rev=HEAD ./internal/auth

../.tools/bin/golangci-lint fmt --diff \
  internal/auth/password_work_stress_test.go \
  internal/auth/oauth_account_link_stress_integration_test.go
```

## Limits and remaining work

The tests establish the observed invariants under bounded local contention.
They do not establish fairness under a sustained attack, distributed rate
limits, per-account lockouts, or a production requests-per-second target. The
budget remains per API process. Existing accounts previously linked through
the unsafe email-only behavior still need deployment-specific review; these
tests do not migrate or revoke them. See `server-security.md` for those security
findings and the account-linking compatibility tradeoff.

## Independent verification of the security-fix commit

Commit `9db91354bf0383a3e9b7993e773243b5ff986a3b`
(`fix(security): harden authentication and native file boundaries`) was verified
from a fresh `git archive` extraction in a temporary directory. The archive
contains no minimal-build implementation files; the desktop-only Google OAuth
test remains in its original `oauth_service_test.go` location. The only generated
input was the verified Studio tool asset produced by `go run ./cmd/bundlectl prepare`.

Using `GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2`, the exact commit passed:

| Command, run from the archived `server/` directory | Result |
|---|---|
| `go test -p=2 -race ./internal/auth ./internal/handler -count=1` | Passed: auth 1.641 seconds; handler 44.653 seconds |
| `go build -p=2 -o /tmp/zenith-pr1-9db9135-server ./cmd/server` | Passed |
| `go run ./tests/testpostgres go test -p=2 -race -tags=integration ./internal/auth -run '^TestOAuthConcurrent\|^TestOAuthLoginNeverLinksAnExistingAccountByEmailAlone$' -count=3` | Passed in 8.644 seconds; the harness removed its database container |

This independently checks that the security-fix commit's default build and auth
tests work without the subsequent minimal-deployment changes. No source change
was required by this verification.
