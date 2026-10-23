# Local deployment stress results

Validated on 23 October 2026 against the pinned Linux/arm64 minimal stack on
Docker Desktop, using real HTTP, PostgreSQL and default Argon2 settings.
All **10,000 requests** passed their expected authorization/admission/policy checks
at up to **32 concurrent clients**. Total run including fixtures and slow clients
was 133.73 seconds. This is a bounded local correctness/recovery check, not a
production capacity benchmark or long-duration soak. The host also ran backend
tests, so throughput should not be treated as a service-level target.

## Traffic results

| Phase | Requests | HTTP status counts | p95 ms | p99 ms |
| --- | ---: | --- | ---: | ---: |
| proxy-denials | 1200 | 403: 400, 404: 800 | 9.22 | 25.99 |
| authenticated-company-reads | 1600 | 200: 1600 | 11.27 | 13.61 |
| cross-company-access-denials | 800 | 404: 800 | 10.57 | 12.76 |
| password-overload | 2400 | 401: 370, 503: 2030 | 118.08 | 151.17 |
| paced-mixed-traffic | 3200 | 200: 800, 401: 798, 403: 800, 404: 800, 503: 2 | 24.87 | 33.14 |
| post-load-company-reads | 800 | 200: 800 | 10.43 | 13.14 |

The overload phase mixed existing-user wrong passwords and nonexistent users.
Every 503 had code `authentication_busy` and `Retry-After: 1`; every 401 had
`invalid_credentials`. Valid passwords worked immediately after the mixed load.
There were no transport failures or unexpected responses in normal traffic.
The paced phase sustained approximately 122 requests/second for 26.23 seconds while
slow connections remained open; latency excludes the deliberate client pacing.

Each temporary account saw only its own Company, and cross-Company reads were
denied. All accounts/Companies created by the probe were deleted afterward,
and signup was closed again. No agent/provider execution or paid model call ran.

## Slow clients and recovery

- Four incomplete header connections closed at 5.002 seconds.
- Four stalled bodies terminated at 60.003–60.004 seconds.
- Four bodies that sent another byte every 15 seconds still terminated at
  120.003–120.005 seconds: progress did not defeat the whole-body deadline.
- Slow-body requests received deliberate timeout/cancellation responses
  (499/504). These are recorded separately from normal-traffic errors.
- API and web remained healthy, without OOM kills or restart-count changes.

The initial probe incorrectly expected stalled bodies to wait for the 120-second
whole-body ceiling; they were terminated by Caddy's existing 60-second idle-body
default. The final configuration makes `read_body_idle 1m` explicit, and the probe
now independently checks idle and progressing bodies. The earlier expectation
failure is retained in [initial evidence](evidence/http-stress-initial.json).
Caddy documents the distinction in its [server timeout configuration](https://caddyserver.com/docs/caddyfile/options#timeouts).

Docker sampled the API and web 31 times each. Maximum **observed** memory was
207.0 MiB for the API (2 GiB limit) and
26.41 MiB for the proxy (256 MiB limit). Sampling cannot establish the true
instantaneous peak. Image identities and raw samples are retained in the report.

## Path variants

All 19 additional path variants stayed denied, including percent-encoded letters
and separators, repeated separators, dot segments, case variants, trailing
slashes and the roots of disabled path prefixes. Two signup trailing-slash
variants returned 307 to the canonical signup path, whose repeated POST was 403.
There was no reachable signup bypass.

## Reproduction and evidence

With the disposable local stack already running, execute:

```sh
python3 scripts/stress-minimal-deployment.py
```

The script temporarily opens local enrollment, creates and deletes disposable
fixtures, and targets only localhost. Do not run it on a machine hosting company
production data. It leaves services running for inspection; stop API/web after
validation. See [runbook](../../deploy/minimal/README.md).

- [Final HTTP metrics and container samples](evidence/http-stress.json)
- [Additional path variants](evidence/http-path-variants.json)
- [Repeated auth/OAuth race checks](auth-stress.md)
- [Desktop adversarial probes](desktop-stress.md)
- [Dependency-gate failure simulations](security-gate-stress.md)

These results do not resolve the known runtime-image matches, same-user worker
authority, identity/abuse controls, or bootstrap database/storage privileges.
Those remain production release gates in the [main assessment](README.md).
