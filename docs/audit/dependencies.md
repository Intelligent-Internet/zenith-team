# Dependency and supply-chain assessment

Assessed 23 October 2026 against commit `cdce9db946b739c0068bdd2faff7192bca48931a`. Counts below describe the checked-in dependency declarations and the initial default Linux/amd64 import graph. This assessment is source and lockfile analysis, not certification of any deployed image or desktop installer.

## Results that matter

The application dependency scans do not present a large confirmed vulnerability backlog. A subsequent [container scan](container-images.md) did identify high-severity matches in bundled Caddy and Silo executables and an old auxiliary gosu binary. The larger concern is the number of separately privileged components, parsers and release paths that must stay patched. A smaller edition should remove optional capabilities from the compiled artifact and its dependency closure, while retaining the current authorization and data-integrity boundaries.

- **Go server:** a source scan with Go **1.26.10**, `govulncheck` **v1.9.0**, Linux/amd64 and CGO disabled found **no symbol-reachable advisories** in `cmd/server`. It reported one module-only advisory for the unmaintained `golang.org/x/crypto/openpgp` package, which is absent from the server's import graph. The vulnerability database reported an update time of `2026-10-21T00:27:29Z`. This is a point-in-time result, not proof of absence of vulnerabilities. [Evidence](evidence/govulncheck-server-go1.26.10-linux-amd64.json).
- **Local compiler:** the initially installed Go **1.26.2** produced **36 unique advisory matches, 30 with symbol traces**, when scanning `./...` on macOS. All symbol-traced matches were in the standard library, with fixes through Go 1.26.10. These are findings about that local build configuration; the release workflows already ask for the latest 1.26 patch. Do not attribute them to a deployed image without inspecting its build information. [Evidence](evidence/govulncheck-summary.json).
- **JavaScript:** both the complete lockfile audit and production-only audit reported **one moderate advisory**, `CVE-2026-41907` / `GHSA-w5hq-g745-h8pq`, on `uuid@8.3.2` through `exceljs@4.4.0`. No high or critical advisory was returned. [Full scan](evidence/pnpm-audit-all.json), [production scan](evidence/pnpm-audit-production.json).
- **Python voice service:** OSV returned **no matches across all 76 locked third-party package versions**. This includes the 75 runtime packages and the development linter. This was a package/version query, without package installation or reachability analysis. It does not assess Python itself, native library contents, the base image or the separate LiveKit server. [Evidence](evidence/voice-osv-query.json).

The official [Go download feed](https://go.dev/dl/?mode=json) listed Go 1.26.10 when this assessment ran. `govulncheck -json` **returns zero even when it reports vulnerabilities**; findings must be interpreted, or the ordinary text-mode command used as the blocking gate. See the [official scanner documentation](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck).

## What the dependency numbers mean

| Surface | Measured dependency count | Interpretation |
| --- | ---: | --- |
| Go manifest | 43 direct + 108 indirect = **151** | Requirements explicitly written in `server/go.mod`, including development/test dependencies |
| Go resolved build list | **341** third-party modules | `go list -m all`; includes modules whose packages are not linked into a production command |
| Default Linux server | **85 modules / 516 third-party packages** | Import graph of `cmd/server`, excluding repository packages and the standard library |
| Minimal Linux server | **67 modules / 333 third-party packages** | Same target/compiler with `zenith_minimal,nomsgpack` in this change |
| Linux MCP command | **30 modules / 135 third-party packages** | Import graph of `cmd/mcp` |
| Frontend direct | **72 production / 23 development / 1 optional** | Manifest entries; Electron itself is declared as a development dependency despite being shipped in desktop installers |
| Frontend lockfile | **1,117 package versions / snapshots** | All lockfile entries, not the number of browser-bundled dependencies |
| Frontend production + optional closure | **824 snapshots** | Closure of declared production/optional roots, including platform alternatives and shared code |
| Frontend development-only closure | **293 snapshots** | In the dev closure but not the production/optional closure; another 230 snapshots are shared |
| Voice worker | **75 runtime + 1 development** | Registry packages in `uv.lock`, excluding the project itself |

[Machine-readable inventory](evidence/dependency-inventory.json) records commands and definitions. Go import graphs are not binary SBOMs: linker elimination and the target build configuration affect the finished artifact. A binary build-info inventory and image SBOM should be attached to each release.

`cmd/zen` initially could not produce a complete `go list` result because the generated `internal/daemon/tool_releases/generated/*.json` assets were missing. Its partial count is deliberately excluded from the table. Run `make tools-prepare` before measuring or building that command. The frontend was not installed or bundled for this inventory, so the browser bundle size and exact shipped browser module set were not measured.

The implemented minimal server tags remove **18 of 85 modules (21.2%)** and **183 of 516 third-party packages (35.5%)** from the Linux server import graph, with no added modules. The removed modules include the Google Cloud/API/gRPC stacks, their OpenTelemetry/logging dependencies, and the MessagePack codec. Both default and minimal server scans passed using Go 1.26.10. This is a server reduction; it does not claim that the current frontend or optional voice/desktop packages have been removed. [Exact module delta](evidence/minimal-dependency-delta.json), [default scan](evidence/govulncheck-full-text.txt), [minimal scan](evidence/govulncheck-minimal-text.txt).

## JavaScript finding: package presence versus vulnerable use

The [upstream uuid advisory](https://github.com/uuidjs/uuid/security/advisories/GHSA-w5hq-g745-h8pq) concerns the `v3`, `v5` and `v6` APIs with caller-provided buffers and invalid offsets. It is an output-integrity issue, not a blanket remote-code-execution finding against every uuid user.

Inspection of the published ExcelJS 4.4.0 archive found only `v4()` calls without an output buffer in its `lib/` source, all in `xlsx/xform/sheet/cf-ext/cf-rule-ext-xform.js`. Zenith Team's spreadsheet worker imports ExcelJS and calls `Workbook.xlsx.load`; no use of the affected uuid APIs was identified. [Archive inspection](evidence/exceljs-uuid-usage.json), [application worker](../../frontend/src/shared/chat/spreadsheet-preview.worker.ts).

This supports a narrowly scoped, time-limited exception for the present `exceljs@4.4.0 → uuid@8.3.2` path, subject to revalidation when either package or its use changes. It does not justify ignoring all uuid advisories. For a minimal edition, removing inline spreadsheet preview removes this path entirely. For the full edition, prefer an upstream-compatible fix or tested patch; forcing uuid from major version 8 to 11 solely to make the audit green is not established as safe here.

## Supply-chain controls already present

- All **69** examined workflow/composite-action `uses:` references are local or pinned to a full 40-character commit SHA. Checkout credentials are not persisted in the examined build lanes.
- Image publishing emits SBOM/provenance information and build attestations. The build/publish split avoids registry credentials in the image-build job. Signed releases avoid restoring build caches from other runs.
- The Go release gate scans with the latest 1.26 patch and fails closed unless a maintainer configures a bypass for the exact release tag. Nightly jobs repeat Go scanning.
- Go sums, the pnpm lockfile and the uv lockfile fix source versions/content hashes. Production web image installation uses `--frozen-lockfile --ignore-scripts`.
- CI runs workflow security analysis, Electron-specific checks, Go tests/analysis and CodeQL. The server runtime image is distroless and non-root. Platform content archives have a separate verification contract.

These observations come from repository files. GitHub organization settings, actual branch rules, secret-scanning enablement, environment approvals and past attestation verification were not independently established by this dependency audit. Comments claiming a setting is enabled are not proof of the current hosted setting.

## Gaps and priorities

| Priority | Evidence | Required improvement |
| --- | --- | --- |
| P1 | All **18** sampled Docker base/service image references use mutable tags; production Compose also defaults app images to `latest` | Promote tested image digests, including PostgreSQL, Redis, object storage, Caddy and Python/Go builders. Automate reviewed digest updates and rebuilds; a permanent digest pin without patching is not a security policy. |
| P1 | Go scanning was nightly/release-only; no npm/Python/image advisory gate was present in the baseline CI | This change adds package scans to `ci-gate` and a daily schedule. Scanning actual built container images and desktop runtimes remains required. |
| P1 | Release scanning and server image compilation select their Go toolchains independently | Scan the finished server/CLI/MCP binaries and images, and record their exact Go patch version. Set a supported minimum patched toolchain for local release builds. |
| P1 | Desktop setup performs `pnpm install --frozen-lockfile` with package lifecycle scripts enabled | Use an explicit reviewed install-script allowlist or script-free installation plus isolated, narrowly authorized build steps. Installing before signing secrets are injected is useful but does not stop a malicious dependency modifying a later signing script. |
| P2 | Web and desktop share one package manifest and dependency installation tree | Separate the edition/build entry points and lockfile scopes, so a web-only release need not resolve Electron packaging dependencies. Keep integrity/provenance checks on both. |
| P2 | Lockfile graph includes rich documents, PDF/XLSX/PPTX parsing, diagrams, two chart stacks, CRDTs, voice and desktop telemetry | Establish a core feature allowlist and compile optional modules out. Dynamic imports reduce initial download cost but do not remove a dependency from the shipped edition. |
| P2 | Known-vulnerability scans do not identify every malicious package or future defect | Add release SBOM retention, new-dependency review, provenance review, patch response ownership and an emergency rebuild path. Track reachability and privilege alongside count. |

[Pin inventory with source lines](evidence/supply-chain-inventory.json). Specific sources include [server image](../../server/Dockerfile), [frontend image](../../frontend/Dockerfile), [voice image](../../voice-agent/Dockerfile), [desktop setup](../../.github/actions/desktop-setup/action.yml), [image release workflow](../../.github/workflows/build-image.yml), [release workflow](../../.github/workflows/release.yml) and [nightly workflow](../../.github/workflows/nightly.yml).

## Gates added with this assessment

[Dependency security](../../.github/workflows/security.yml) is a reusable workflow called by the existing CI workflow; its result is included in the existing `ci-gate` dependency list. It also runs daily and on manual request. It scans both full and minimal server symbols, checks the minimal import boundary, audits both complete and production npm closures, and queries OSV for all locked Python packages. Reports are retained as CI artifacts for 30 days. Existing GitHub rules must still require `ci-gate` for this to prevent merges.

The npm policy **blocks high and critical** advisories and retains lower-severity findings in the report. It is not a zero-advisory policy, and it does not declare every moderate finding safe. The current moderate uuid finding has the specific applicability review above. The npm wrapper validates the report and fails on a scanner/registry/parse error; it does not treat an unavailable registry as success. Python blocks every returned advisory and fails on incomplete, paginated, unscannable-source or failed queries.

Validation completed: both live npm policy scans passed with one moderate finding; the live Python query passed; the Go text-mode scans passed for both editions; actionlint 1.7.12 accepted both changed workflows. Simulated scanner results verified blocking for high/critical findings, malformed reports, scanner failure, failed package queries, incomplete results, pagination and network failure. [npm policy checks](evidence/npm-scanner-policy-checks.json), [Python failure checks](evidence/voice-scanner-failure-checks.json), [verification record](evidence/dependency-gate-verification.json). These are local results; the review pull requests also trigger hosted checks, whose current status must be read on GitHub. Branch-protection settings were not changed.

Fresh-checkout viability was also checked in an isolated copy containing only tracked and nonignored server files, with `internal/daemon/tool_releases/generated` confirmed absent. Both full and minimal `go list` and `govulncheck ./cmd/server` succeeded; neither server graph imports `internal/daemon`. The subsequent broader minimal-test step still runs `make minimal-check`, whose `tools-prepare` prerequisite supplies its generated fixtures. [Clean-copy evidence](evidence/clean-checkout-server-scans.json).

## Minimal-edition recommendations

Keep the business core: company/member/project permissions, tasks, conversations, execution state, approval boundaries, audit history, PostgreSQL integrity and the daemon's run-scoped access channel. Avoid rewriting these established boundaries merely to reduce a package count.

Start with a browser-only edition and an isolated daemon. Exclude the Electron runtime, LiveKit/Python voice service, provider-specific cloud publishing, inline office/PDF preview, rich collaborative canvas, diagram rendering and ancillary analytics where those features are not required. Use download-only attachments with the existing authorization checks, and simple structured forms/text for core business actions. A later edition can add individually isolated preview/voice services with distinct deployment and patch lifecycles.

Retain maintained cryptography, OAuth and database libraries where required; replacing them with custom security-sensitive code can increase risk. PostgreSQL remains the source of truth. Removing Redis/Asynq requires an actual PostgreSQL-backed scheduling/retry design, not deleting the queue while preserving claims of equivalent behavior. This audit does not count that migration as complete.

## Reproduction and limitations

No frontend tests or application dependency lifecycle scripts were run. The pinned pnpm executable was invoked using `npm exec --yes --ignore-scripts --package=pnpm@9.14.2`, because the globally installed pnpm 11 rejected this repository's pinned package-manager version. Go downloaded its official toolchain/scanner dependencies through its normal module mechanism. No package versions or lockfiles were changed by the audit.

```sh
cd server
go list -m -json all
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go list -mod=readonly -deps -json ./cmd/server
# Install/build govulncheck natively first when the audit host is not Linux.
GOTOOLCHAIN=go1.26.10 go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
GOTOOLCHAIN=go1.26.10 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 govulncheck ./cmd/server
cd ..
npm exec --yes --ignore-scripts --package=pnpm@9.14.2 -- pnpm --dir frontend audit --json
npm exec --yes --ignore-scripts --package=pnpm@9.14.2 -- pnpm --dir frontend audit --prod --json
```

The Python evidence queries `https://api.osv.dev/v1/querybatch` for every registry package/version parsed from `voice-agent/uv.lock` using the Python standard library. Source scans do not establish deployed-image patch levels or prove that every reported call is remotely exploitable. The follow-up [container assessment](container-images.md) covers the five initial local Linux/arm64 images. Final rebuilt images, other architectures, Electron/Chromium binaries, third-party agent CLIs/plugins, licensing, package provenance verification and production environment configuration require their own artifact-specific checks.
