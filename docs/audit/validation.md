# Validation record

Performed locally on 23 October 2026 against baseline
`cdce9db946b739c0068bdd2faff7192bca48931a` plus the audit/hardening changes.
This record describes local validation. GitHub CI and CodeQL were also triggered
by the review pull requests; their live status is separate from these local results.
No public deployment or provider agent run was initiated.

## Completed checks

| Check | Result / interpretation |
| --- | --- |
| `GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 make gate` with Node 24.15.0 / pnpm 9.14.2 | Passed after the stress-test additions: API checks, 90 Python tests, Go shape/sqlc/build/vet and full PostgreSQL integration/race suite, frontend lint/types/production build |
| `GOTOOLCHAIN=go1.26.10 make go-unit` | Passed: shape rules, verified tool-content preparation, pinned sqlc drift check, full build, default/integration-tag vet and full race-enabled unit suite |
| `GOTOOLCHAIN=go1.26.10 make minimal-check` | Passed: excluded-dependency assertion plus minimal auth, Cloud Run, handler and composition-root tests |
| Default + minimal auth/middleware/handler race tests | Passed, including concurrency admission and API 503 mapping |
| Full PostgreSQL auth integration suite with `-race` | Passed; new OAuth takeover regression first failed on original behavior and then passed with the fix |
| `GOTOOLCHAIN=go1.26.10 make go-integration` | Passed the complete cross-package PostgreSQL integration and unit suite with race detection; the test harness stopped and removed its temporary database container |
| `golangci-lint fmt --diff ./...` (pinned repository tool) | Passed after formatting the new tests |
| `GOTOOLCHAIN=go1.26.10 make go-lint GO_LINT_ARGS=--new-from-rev=HEAD` | Passed, zero newly introduced lint issues |
| Frontend `pnpm lint`, `pnpm typecheck`, `pnpm build` | Passed using pinned pnpm 9.14.2; production bundle still has large-chunk warnings and is about 56 MiB on disk |
| API generation, `make api-lint api-types-check` | Passed; generated types updated. One existing OpenAPI warning remains |
| `pnpm electron:security:scan` | Passed all 7 rules; this is a static rule scan, not a complete desktop penetration test |
| Native-action manual Node probes and `node --check` | Passed: traversal/identifier rejection, cache links, private atomic writes, foreign-frame rejection and valid opening through a stub. No interactive OS application was launched |
| `make voice-gate` | Passed ruff lint/format and 90 Python tests |
| Full/minimal Go advisory scan, Go 1.26.10 / Linux amd64 | No reachable or package-level findings; module-only OpenPGP advisory retained |
| Full/production npm audit | 1 moderate, 0 high/critical; affected-call applicability reviewed in dependency report |
| Python lock OSV batch | 76 package versions checked, no matching advisory |
| npm/OSV scanner failure-path simulations | Network, parse, incomplete-report and affected-package cases fail closed as intended |
| `actionlint` on changed workflows | Passed; hosted execution and remote branch protection remain unverified |
| Five actual Linux/arm64 container image scans | Completed successfully; **not a clean vulnerability result**. See [image findings](container-images.md) |
| Minimal Docker builds and startup | Server, web and derived non-root storage built; PostgreSQL, Redis, storage and API healthy, initializer exits 0, browser returns 200 |
| `python3 scripts/check-minimal-deployment.py` | Passed actual non-root/read-only/capability/no-new-privileges/resource/port/network checks, default signup403 including encoded path and blocked-route404 exact responses |
| API outbound HTTPS probe from internal network | Failed to reach the external endpoint as expected; network membership separately verifies internal-only networks |
| Local enrollment/company lifecycle smoke | Signup201, Companies read200, Company create201, read200, test account deletion204; signup then closed again |
| Local HTTP stress | Passed 10,000 requests at up to 32 concurrent clients, 12 slow connections, 19 extra path variants; no transport failures in normal traffic, OOM kills or restarts; fixtures removed and signup closed |
| Repeated auth/OAuth race stress | Passed; see [auth stress](auth-stress.md), including isolated verification of security commit `9db9135` |
| Desktop adversarial probes | Passed 5,000 generated payloads, link/frame probes and 256 concurrent writes; same-user directory-race limitation documented |
| Scanner failure harness | 116/116 offline checks passed after fixes; actionlint and offline zizmor passed |
| Final Go format/lint | `make go-fmt-check go-lint GO_LINT_ARGS=--new-from-rev=origin/main` passed, zero issues |
| Staged secret scanning | Gitleaks 8.30.1, official archive digest verified, all staged changes passed commit-hook scans with redacted output; module inventories use separate path/version fields to avoid two false positives on a public Go pseudo-version |
| `git diff --check` | Passed |

The initial assessment ran component commands. The follow-up also ran the complete
aggregate `make gate` successfully on the final code and new backend stress tests.
The repository's existing large-chunk/OpenAPI warnings remain. Branch-protection
settings were not changed; use GitHub for the current hosted check results.

## What was not validated

- Real-company or production data, production migrations, backup/restore and disaster recovery.
- End-to-end provider login, actual agent task execution, external connectors or paid model calls.
- Full desktop packaging/signing/notarization and native opening on all operating systems.
- Live voice/LiveKit, Google sign-in, cloud publishing or social/email services in the restricted profile.
- Historical account compromise remediation, organizational IdP/MFA or explicit two-identity linking.
- Every vulnerability's exploitability, every API route by manual attack, and other CPU architectures/host images.

No frontend tests were added or executed, in accordance with repository guidance;
frontend validation used lint, types, build, static security checks and focused
manual Node probes. Application npm lifecycle scripts were disabled on install.
The full frontend/voice dependencies were installed only for development checks;
these are not extra services in the minimal runtime.

## Runtime cleanup

After validation the API and web containers are stopped. Infrastructure containers
and generated data volumes are retained, as repository guidance requests. The
private stack can be started with `make minimal-up`. `deploy/.env` was generated
with random secrets, mode0600, and remains ignored. No credentials, image archives,
scanner database or generated application build output are tracked.
No local listener remained on ports 8080, 8788 or 5173 after cleanup.
