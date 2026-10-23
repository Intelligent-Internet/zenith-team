# Zenith Team: security audit and minimal edition

**Assessment date:** 23 October 2026. **Baseline:**
`cdce9db946b739c0068bdd2faff7192bca48931a` from
[Intelligent-Internet/zenith-team](https://github.com/Intelligent-Internet/zenith-team).
The accompanying changes are prepared for review on `fix/auth-and-native-boundaries`
and `feat/minimal-company-deployment`. The reduced deployment depends on the
security fixes. No release or production deployment is part of this work.

## Decision

**Yes: build a smaller edition from the existing Go core.** Preserve its Company,
Project, Task, Run, approval and transaction rules. Reduce optional capabilities,
shipped parsers and worker privileges before considering a rewrite. This audit
implements an initial restricted deployment and real dependency removal, plus
three security fixes. It does **not** establish unrestricted production readiness
or claim that every route and dependency is free of vulnerabilities.

The most consequential remaining boundary is agent execution. An agent running
with a person's host authority can read/use resources on that host even when all
package scanners are green. Use a dedicated disposable execution VM for a private
pilot. Production workers need fail-closed isolation, narrow capabilities and
controlled network access before different Companies share infrastructure. **The scanned Caddy and Silo
images also retain known high-severity matches and need patched rebuilds before
sensitive company use.** The deployment below is a non-sensitive validation baseline.

Start with the [private deployment runbook](../../deploy/minimal/README.md).
Detailed reviews: [server](server-security.md),
[daemon/desktop/browser](daemon-frontend-security.md),
[dependencies and supply chain](dependencies.md),
[actual container images](container-images.md),
[HTTP stress and recovery](http-stress.md), [auth race tests](auth-stress.md),
[desktop adversarial probes](desktop-stress.md) and
[scanner failure simulations](security-gate-stress.md).

## What the CVE increase tells us

The linked [tracker](https://cve.0xosprey.com) describes counts of **published CVE
records**, grouped by publication date, from
[CVEProject/cvelistV5](https://github.com/CVEProject/cvelistV5). Its
[raw dataset](https://cve.0xosprey.com/data.json), generated 22 October 2026, records
4,319 in September 2025 and 14,934 in September 2026: roughly **3.46 times** as many.
October's 12,688 records are incomplete; the displayed month-end estimate is a
projection. [Captured evidence](evidence/cve-tracker-snapshot.json).

That supports improving patch throughput and reducing exposed software, but is
not an estimate of this application's vulnerability rate. Reporting volume does
not tell us which installed versions are affected, whether a vulnerable symbol
is reachable, what authority it has, or whether it is being exploited. Prioritize
actual deployed artifacts, reachability and privilege. Go's
[scanner](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) explicitly uses the
build configuration and call graph; it is still limited by its database and
static analysis.

## Architecture and dependency surface

| Layer | Existing responsibility | Minimal deployment disposition |
| --- | --- | --- |
| Go API | Identity, tenancy, permissions, tasks/runs, approvals, schedules, history | Retained; Google OAuth/cloud publishing compiled out |
| PostgreSQL | Authoritative product state, integrity and permission queries | Retained |
| Redis/Asynq | Durable scheduler/worker lifecycle and realtime relay | Retained; no safe drop-in removal yet |
| S3-compatible storage | Attachments, output assets and skill versions | Retained on a private network |
| Browser app/Caddy | Static app, rich previews, same-origin API proxy | Retained; restricted ingress, full browser bundle still shipped |
| Electron | Desktop integration, native file opening, daemon launch | Not deployed in the minimal server stack; patched separately |
| Python voice/LiveKit | Optional huddles and voice workers | Not deployed |
| `zen` daemon + provider CLI | Executes agent work on a separate machine | Still required; dedicated VM recommended, not included in API container |
| Plugins/cloud integrations | Skills, managed tools, mail/social/publication | Outbound API integrations unavailable in isolated deployment; daemon tools remain a separate trust boundary |

The manifest contains 43 direct and 108 indirect Go requirements. The full
resolved module build list contains 341 modules, but the actual default server
binary records 85. The frontend lock has 1,117 package snapshots (824 in the
production/optional closure); that is not a count of browser-bundled packages.
The optional voice lock adds 75 runtime packages and one development tool.

Measured with Go 1.26.10, Linux/amd64, CGO disabled and identical stripped build
flags:

| Metric | Default API | Minimal API | Reduction |
| --- | ---: | ---: | ---: |
| Third-party modules in binary build info | 85 | 67 | 18 / 21.2% |
| Imported third-party Go packages | 516 | 333 | 183 / 35.5% |
| Stripped executable bytes | 76,681,378 | 62,898,338 | 18.0% |
| Required state services | PostgreSQL + Redis + S3 | Same | None claimed |
| Browser dependency manifest | Full | Full | None claimed |

[Import-graph evidence](evidence/minimal-dependency-delta.json),
[binary build-info evidence](evidence/binary-comparison.json).
The browser production output measured about 56 MiB on disk. It still includes
rich editing, office/PDF previews, diagrams, voice UI and other optional code.
This is the largest remaining dependency-reduction opportunity.

## Findings that change the deployment decision

| Priority | Finding and conditions | Action taken / remaining |
| --- | --- | --- |
| High | Unverified password signup plus Google email-only account linking allowed account pre-registration takeover; the original password/sessions survived the victim's Google sign-in | Reproduced against PostgreSQL. New email-only links now fail with `oauth_account_conflict`; existing provider-subject login remains. Historical mixed password/OAuth accounts need operator review. |
| High for an exposed API | Public login/signup/reset could run unbounded concurrent Argon2 work | Added a shared four-operation budget; excess work gets 503 and Retry-After. Does not replace per-account/IP abuse control. |
| High conditional desktop escalation | Renderer-supplied `resourceId=..` escaped the native resource cache; native action handlers lacked the existing main-frame guard | Strict path/identifier validation, cache containment, atomic private writes and trusted-frame checks added. Renderer-supplied OS opening remains a separate privileged capability. |
| High production architecture gap | Worker isolation can fall back to unsandboxed execution; macOS uses selected exclusions rather than a full host allowlist | Not redesigned here. Dedicated isolated workers are a production prerequisite. |
| Medium | Default API socket body/idle lifetimes are not bounded globally | Minimal proxy adds header/body/idle deadlines; 2-minute reads deliberately restrict slow uploads. The direct default API listener is unchanged. |
| Medium | Email is marked verified when verification is unconfigured; public signup and operational endpoints need controlled exposure | Minimal enrollment closed by default, API has no published port, public shares/callbacks blocked. Real identity verification/SSO/MFA remains work. |
| Medium | Full deployment shares powerful bootstrap PostgreSQL and storage credentials with the API | Preserved functionality for pilot. Split migration/runtime roles and bucket policies before production. |
| Supply-chain gap | Baseline Docker tags are mutable and dependency scans do not all gate PRs | Minimal images pinned; full/minimal Go, npm and Python checks added to required `ci-gate` and daily CI. Patch/update/promotion ownership still necessary. |

These are qualified severities based on stated preconditions, not assigned CVSS
scores or newly issued CVEs. Review details and exact code paths in the supporting
reports. No external organization's production systems were tested.

## Known-advisory results

- **Go 1.26.10 server, default and minimal:** no known symbol-reachable or
  package-level findings in the scanned Linux/amd64 configuration. One module-only
  OpenPGP advisory remains; that package is not imported by the server.
- **Local Go 1.26.2:** the original macOS whole-module scan returned 36 advisories,
  30 with symbol traces, all such traces in the standard library. This is a reason
  to build with a patched toolchain, not evidence that the current release images
  contain all those vulnerabilities.
- **npm:** one moderate `uuid@8.3.2` advisory through ExcelJS. Published ExcelJS
  source uses `v4()` without a caller buffer; the advisory affects other UUID
  APIs/invalid buffer offsets. No affected call was established. Retained in the
  report; no untested major-version override was added.
- **Python voice lock:** OSV returned no version matches for 76 registry packages.
  Native contents and interpreter/image vulnerabilities require separate scans.

These are dated results against the databases available at audit time. Keep the
raw reports in [evidence](evidence/) and rerun on the artifact being released.
GitHub separately listed 46 open Dependabot alerts on `main`: 40 referenced installed
versions now outside affected ranges, 5 referenced removed packages, and 1 matched
the current moderate uuid finding. The remote main lockfile matched the audited
file exactly; independent OSV queries agreed for both old and current versions.
No alert was dismissed or dependency changed merely to suppress these counts.
See [complete reconciliation](dependabot-reconciliation.md).

The new CI blocks npm high/critical findings and retains moderate results; it does
not make every lower-severity finding an automatic merge blocker. Scanner/network
failures fail the checks rather than being interpreted as a clean scan.

The **actual image scans add material release blockers** beyond those source scans:
Caddy has 5 high and Silo has 17 high package/advisory instances, including older
Go/network components. The API image has no high/critical matches; Redis has none.
PostgreSQL's image has a critical version match in its auxiliary `gosu`, not the
PostgreSQL engine; binary triage excludes the cited critical TLS symbol and narrows
the relevant helper finding. These are package/advisory instances, not a count of
proven remotely exploitable bugs. [Exact images, applicability and remediation](container-images.md).

## Implemented controls

The `zenith_minimal` Go build excludes optional Google provider implementations;
small adapters refuse their configuration and operations. It does not implement
custom cryptography or a replacement OAuth protocol. The independent `nomsgpack`
tag excludes Gin's unused MessagePack codec. A checked import boundary prevents
accidental reintroduction of the Google SDK/gRPC and MessagePack dependency path.

The standalone Compose file has an explicit environment allowlist, private API
and data networks, loopback-only ingress, non-root users, read-only filesystems,
dropped capabilities, no-new-privileges, quotas, bounded logs and pinned image
inputs. Public share/callback/workload routes and default signup are blocked before
proxying. Caddy's unused low-port file capability was removed so these restrictions
work on the actual container. The baseline production Compose file remains a
separate deployment option.

The [deployment smoke check](../../scripts/check-minimal-deployment.py) verifies
actual container settings and denial responses, including encoded signup paths.
The [minimal check](../../scripts/check-minimal.sh) verifies the import boundary
and relevant backend tests. New dependency workflows participate in the existing
required gate. Review pull requests trigger GitHub-hosted validation separately;
use their current check status. Actual branch-protection settings were not changed.

## Recommended next edition

1. **Isolate execution first.** Fail closed when the required sandbox is absent.
   Use a fresh unprivileged VM/microVM or equivalent audited isolation per trust
   domain/run, read-only input snapshots, a narrow output channel, explicit network
   destinations and short-lived connector capabilities. Test credential theft,
   cross-run access, teardown and prompt-injection-driven tool misuse.
2. **Create a true core web build.** Separate browser and Electron manifests/entry
   points. Keep company/project/task lists, plain text conversations, approvals,
   run logs and download-only attachments. Remove office/PDF preview, rich canvas,
   diagram/chart stacks, voice UI and cloud publishing from this edition's build
   graph. Add optional renderers/services behind separate origins and lifecycles.
3. **Complete enterprise operation.** Verified enrollment and IdP/MFA, explicit
   secure account linking, session/connector revocation, per-account/IP abuse
   budgets, least-privilege DB/S3 identities, secret rotation, immutable external
   audit export, backup/restore and incident-response exercises.
4. **Simplify state infrastructure only with parity evidence.** A PostgreSQL job
   queue/outbox could eventually replace Redis/Asynq for single-node operation,
   but needs leases, retries, scheduled firing, cancellation, idempotency and
   crash/restart tests. Keeping maintained infrastructure is safer than a rushed
   scheduler rewrite. Keep PostgreSQL as the authority; do not add a second local
   ledger merely to claim fewer services.
5. **Maintain the reduced edition.** Record a dependency budget and exceptions,
   scan exact binaries/images, retain SBOMs/provenance, review new dependencies and
   ship digest updates. Dependency minimization is an ongoing release discipline.

## Review coverage

| Area | Work performed | Boundary of the conclusion |
| --- | --- | --- |
| Identity | Password/session/CSRF/OAuth source review, existing tests and new reproduced takeover/resource regressions | No external IdP penetration test or historical account recovery |
| Authorization | Company/Project/Run middleware, route actor policy, transaction/revocation fences and tests reviewed | No claim every endpoint was manually exploited |
| Input/output | Selected upload, skill/archive, SSRF/proxy, public sharing and body limit paths reviewed | Parsers and every media format were not fuzzed |
| Execution | Daemon sandbox/fallbacks, local run channel, credentials and plugin/tool release paths reviewed | No malicious-agent isolation qualification on every OS |
| Desktop/browser | Native file/IPC boundary fixed; navigation/renderer risks reviewed; static/build checks run | No signed installer or interactive OS opener acceptance run |
| Dependencies | Go source/binary graphs, npm/Python locks, current advisories and image/binary scans | Different architectures and host/provider CLI installations need their own scans |
| Delivery | CI, action pins, build secrets, images, deployment/network restrictions and real startup reviewed | Hosted repository settings and production deployment not accessed |
| Business operation | Local account/Company create/read/delete smoke flow | No live agent/provider execution, real-company workload or recovery exercise |

## Validation and limits

See [validation record](validation.md) for commands and results. The assessment
combines source review, lockfile/advisory scans, normal repository checks, targeted
security regressions, real PostgreSQL tests and a local Docker deployment smoke
test. It is not a full external penetration test, formal verification, regulatory
certification or review of every possible configuration and plugin.

The follow-up stress run exercised 10,000 local HTTP requests, 12 slow connections,
repeated authentication/OAuth races and scanner failure paths. Temporary fixtures
were removed; this is bounded correctness/recovery evidence rather than production
capacity qualification.

No actual provider sign-in, paid model call, end-to-end agent task, production
migration or real-company data processing was performed. Automatic cloud
publication, voice services and desktop installation were not exercised. Those
boundaries need validation for the edition and platform you intend to ship.
