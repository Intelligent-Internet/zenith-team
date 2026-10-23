# Desktop and authentication adversarial checks

Checked on 23 October 2026 against the working hardening changes based on
`cdce9db946b739c0068bdd2faff7192bca48931a`. This supplements
[the desktop/daemon audit](daemon-frontend-security.md). No new regression or
renderer-only containment bypass was found. A deterministic directory-swap probe
confirmed the already documented same-OS-user race limitation; the desktop cache
must not be presented as a sandbox against a local agent process.

## Desktop methods and results

The probes ran with Node.js v22.22.2 on macOS. They loaded the actual
`frontend/electron/main/native-actions.cjs` module into temporary scripts outside
the repository. Filesystem work used fresh `mkdtemp` directories and was deleted
after each run. OS openers, drag delivery, dialogs, clipboard and Electron IPC
registration were stubs: no real application was opened and no existing user
files were modified. No frontend test files or frontend test commands were used.

| Check | Work performed | Result |
| --- | --- | --- |
| Known malformed payloads | 40 cases covering dot segments, absolute paths, both separators, NUL, colon, whitespace, overlong IDs, Windows device-name spellings, trailing dots, empty/missing bytes and a payload above the 100 MiB limit | All refused |
| Deterministic input fuzzing | 5,000 resource-ID/filename pairs, seed `0x5eed`, including ASCII punctuation, whitespace, Unicode and NUL; accepted normalized paths checked for exactly two segments beneath the cache | No accepted traversal |
| Ordinary writes | 100 async open preparations and 100 synchronous drag preparations, with content and POSIX permission checks | Correct bytes; mode `0600` |
| Planted links | Final-file symlink and hardlink, resource-directory symlink and cache-root symlink, each through async and sync paths | Final links replaced without modifying their external target; directory links refused before writing through them |
| Sender boundaries | 45 invocations across all five registered channels: absent event/frame, foreign WebContents, wrong process, wrong frame token, wrong URL and destroyed main window | All refused without side effects; the exact main frame succeeded |
| Path collision contention | 256 simultaneous 8 KiB writes to one resource and filename | All completed; final content was one complete submitted buffer; no temporary files remained |
| Filename aliases | `a:b.txt` and `a?b.txt` normalize to the same basename | Confirmed shared-cache behavior; no path escape |

The relevant checks are in `native-actions.cjs:35` (IPC), `:310` and `:351`
(async/sync cache creation), `:427` (lexical containment), and `:467`/`:484`
(filename/ID parsing). Sender identity is supplied by Electron, not by an
untrusted payload. This manual harness does not substitute for testing actual
Electron frame lifecycle behavior.

## Confirmed limits

**The directory checks are not an atomic filesystem authority boundary.** A
second, temporary-only probe replaced the checked resource directory with a
symlink immediately before the async writer opened its randomized temporary
file. A deterministic filesystem hook supplied that scheduling point. The write
then reached the sibling temporary directory. Both `realpath` and subsequent
path-based operations are correct individually, but the directory can change
between them (`native-actions.cjs:328–343`; the synchronous path also uses
separate filesystem operations).

This requires another process able to mutate the desktop user's directories.
The exposed resource bridge itself only writes regular file bytes and does not
provide the required symlink primitive. The result is therefore a confirmed
local race limit, not evidence of a remote renderer-only bypass. Rechecking a
path again would still leave a race. Protecting against a hostile same-user
daemon needs OS-enforced isolation or native handle-relative filesystem
operations, and an opening mechanism that retains that authority. Do not claim
the present patch provides that guarantee.

**The cache is mutable.** A resource ID plus sanitized filename selects a shared
destination. Concurrent calls, equivalent sanitized filenames and filesystem
case folding may select the same file. Atomic replacement prevents partial
content, but does not bind an eventual OS open to the exact caller's bytes.
Resource/version identity and immutable content addressing would be separate
product work. No cross-resource authorization boundary should depend on this
cache.

**A compromised trusted renderer retains the OS opener.**
`openResourceFile` accepts supplied bytes and calls `shell.openPath`
(`native-actions.cjs:99`). The sender guard blocks foreign frames; it does not
inspect active file content, guarantee a user gesture or provide a native
confirmation. OS file associations determine the consequences. This remains a
reason to keep the private minimal deployment browser-only.

Windows path/device behavior, packaged Electron IPC, actual file associations,
filesystem exhaustion, process termination during writes and sustained
multi-gigabyte resource traffic were not exercised. The 100 MiB check is per
request, not a global memory or disk budget. These results do not establish a
desktop denial-of-service bound.

## Authentication review and repeated concurrency checks

The OAuth change was independently reviewed for existing-subject behavior,
email collision and transaction races. Existing provider/subject identity still
wins before email lookup (`server/internal/auth/oauth_login.go:127`). A new
subject cannot adopt an existing account by matching its email (`:167`). The
case-insensitive email and OAuth uniqueness constraints remain the final race
fence, and conflicts roll back the transaction (`:201`). No new linking bypass
was found. Existing password users must continue with their original sign-in
method until an explicit proof-of-both-identities linking flow exists. Previously
auto-linked accounts are not retrospectively repaired by this change.

The shared password semaphore was reviewed across signup, existing/unknown-user
login, invalid-email dummy verification and password reset. It releases slots
after the password manager returns, including errors; cancellation of an
already-running operation does not prematurely release its slot. The busy
sentinel is preserved through wrapped errors and mapped to HTTP 503 with
`Retry-After: 1`.

Executed from `server/`:

```sh
GOTOOLCHAIN=go1.26.10 go test -race -count=20 \
  -run 'TestPasswordWork|TestCancelledRequestsDoNotStartPasswordWork|TestFailedPasswordHashReleasesWorkBudget' \
  ./internal/auth
```

Passed. The repeated stress case alone covered 960 admitted operations and
11,520 excess/cancelled attempts, with peak concurrency four and recovery after
each round. These use a blocking password-manager stub to control scheduling;
they do not measure Argon2 throughput or a distributed attack. The semaphore
bounds local expensive work; it is not per-user/IP rate limiting, fair admission
or a global limit across replicas. Upstream request-abuse controls remain needed.
Database-backed OAuth checks are reported separately by the server audit work.

## Private deployment source review

The reviewed Caddy configuration puts signup/public/integration denials inside
an explicit `route` before the reverse proxy. It denies signup for every method
unless the operator flag is exactly `true`. The actual route-variant and load
probes are owned by the deployment stress work; this review does not claim
independent HTTP execution coverage.

Compose exposes only the web service on host loopback, separates the web/API
network from the data network, and gives the API/data services only internal
networks. Container users, read-only roots, dropped capabilities, privilege
restrictions and resource limits are explicit. The web container still has an
external network. Docker host administrators remain trusted, and named volume
permissions are required for correct non-root operation.

No new source-level policy bypass was identified. Production limitations remain
explicit in `deploy/minimal/README.md`: the API uses the PostgreSQL bootstrap
superuser and storage administrative key, Redis has no authentication on the
private data network, the full browser dependency graph remains, and the CSP
only provides frame-ancestor protection. The private profile is not a replacement
for least-privilege database/storage credentials, strong worker isolation or
patching the remaining image findings.
