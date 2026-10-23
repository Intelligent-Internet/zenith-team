# Zenith Team daemon, desktop, and browser security review

Review date: 2026-10-23. Baseline: `cdce9db946b739c0068bdd2faff7192bca48931a`.
This is a source audit of the daemon execution boundary, desktop bridge, local
HTTP capabilities, application previews, rendering, and runtime dependency
installation. It is not a penetration-test certification. No production service,
real account credentials, or generated executable was exercised. The separate
dependency inventory covers advisory matches; the risks below do not depend on
there being an assigned CVE.

## Decision

A browser-only company control plane is a credible hardening direction. The
current daemon is a trusted personal-computer agent, with deliberate access to
the member's machine. Removing Electron from the delivered control plane removes
its native bridge, updater, bundled Chromium/Node, and local provider discovery.
It does **not** make arbitrary agent execution safe. Keep execution on a separate,
short-lived, isolated worker and give it run-scoped capabilities rather than the
control plane's credentials or the member's home directory.

The existing company, project, membership, task, approval, activity, run proof,
and PostgreSQL transaction semantics are valuable foundations. Do not rewrite
those simply to lower the count of dependencies. The largest reduction comes from
removing optional product surfaces and execution privileges.

## Confirmed defect corrected in this audit

### DF-01 — Resource cache traversal and insufficient IPC sender validation

**Priority: high when the desktop renderer is compromised; fixed locally.**

Before this change, `normalizeIdentifier` preserved the resource ID `..`, and
`getManagedResourceFilePath` joined it below `userData/resource-files`. The result
was a renderer-controlled write directly into `userData`, rather than the intended
per-resource directory. The payload includes the file's bytes and filename. A
file write also followed an existing destination symlink. Native-action handlers
did not check the originating frame.

The exploit precondition is code execution in a renderer carrying the privileged
preload, or another way to invoke its native resource IPC. This is **not** evidence
that ordinary chat content currently yields XSS, and the navigation/preview
restrictions described below substantially reduce reachability. A renderer
compromise must nevertheless not be allowed to overwrite desktop state outside
the cache.

`frontend/electron/main/native-actions.cjs` now:

- Guards all native actions with the existing `isTrustedMainFrame` policy
  (lines 35–60).
- Rejects malformed resource IDs, dot segments, path separators, trailing dots,
  and Windows device names; checks the resolved path remains a two-component
  child of the resource cache (lines 389–495).
- Rejects redirected cache/resource directories before writing the file, and
  replaces the final entry atomically with a new `0600` file rather than following
  a symlink or overwriting a hard-linked target (lines 310–386).

The change adds no package. It does not claim protection against an already
compromised local OS account racing directory replacement; that account can
already manipulate the desktop's files. No changes were made to user-selected
Save As destinations.

Validation: dependency-free manual Node checks exercised async open, synchronous
drag, traversal and device-name rejection, file permissions, existing symlink and
hard-link destinations, redirected directories, foreign-frame rejection, and a
normal resource open with a stub OS opener. `node --check` and `git diff --check`
passed. No frontend tests were added or run. After installing the locked frontend
dependencies, lint, typecheck, the production build, and the Electron security
scan all passed. The scanner checks seven static configuration rules; it does not
validate every IPC or establish that a packaged executable is secure.

## Material remaining risks

### DF-02 — General agent execution fails open when machine isolation is unavailable

**Priority: high for an enterprise execution service; explicit current trust model.**

`server/internal/daemon/credential_isolation.go:76` starts an unwrapped launch when
the isolation verdict is not enforced. The probe explicitly reports no sandbox
off macOS (`:284–290`), and a failed macOS probe also lowers enforcement. App
preview commands still launch when isolation cannot be constructed (`:130–145`).
Run setup follows that decision (`server/internal/daemon/run.go:1467–1485` and
`:1548–1565`). If a run was already marked sandboxed and its individual profile
cannot be built, that run fails; the issue is the intentional fallback when the
machine-wide verdict is not enforced.

Codex explicitly uses `danger-full-access` outside macOS
(`server/internal/daemon/execenv/codex_sandbox.go:60–67`). Normal Claude runs use
`--permission-mode bypassPermissions`
(`server/pkg/agent/claude_provider.go:890–895`). Connector-only runs have a narrower
separate policy, which should not be conflated with normal arbitrary-code runs.

A malicious repository, tool, model-produced program, or prompt injection that
induces code execution therefore has the daemon user's filesystem/network powers
on an unisolated machine. This is documented product behavior, not a claimed
sandbox escape. Enterprise mode should reject a claim unless an attested worker
isolation policy is available; a warning or a `not_enforced` field is not an
enforcement boundary.

### DF-03 — The macOS profile protects selected paths, not all company secrets

**Priority: high for mixed-trust/company execution; explicit current trust model.**

`server/internal/daemon/execenv/seatbelt.go:192` starts with `(allow default)`.
Subsequent rules protect Zenith Team sign-ins, run homes, selected startup files,
and shared workspace writes. Ordinary runs intentionally hide no additional
folders (`server/internal/daemon/credential_isolation.go:165–178`). Tagged channel
runs add stronger workspace read restrictions.

This is useful credential protection, but it is not a general deny-by-default
filesystem or network boundary. Other files readable by the machine user, such
as unrelated SSH/cloud credentials or business documents, are not automatically
protected by the Zenith Team sign-in deny list. General outbound network is not
allowlisted by this profile. Provider-native configuration and marketplace code
are additional trust inputs.

For the hardened execution tier: one ephemeral worker per run, no real user home,
no host Docker socket, no cloud metadata access, explicit read-only inputs,
resource limits, default-deny network policy, and short-lived scoped connector
credentials. A container around a long-lived daemon and all its runs is weaker
than isolation between the runs themselves.

### DF-04 — The native resource opener still trusts renderer-selected content

**Priority: high after renderer compromise; remains open.**

`frontend/electron/preload/index.cjs:585–593` exposes `openResourceFile`.
`frontend/electron/main/native-actions.cjs:99–106` writes renderer-provided bytes
and opens the resulting filename with the operating system. The validated name
still has no safe-document content/type restriction or native confirmation.

After a main-renderer compromise, the sender check correctly identifies the main
renderer and consequently allows the request. On a platform where a supplied
extension is associated with execution, this is a potential escalation from
renderer script to OS execution. Windows command-file associations are a relevant
example; an actual Windows exploit was not run. An extension blacklist alone is
not a sufficient long-term fix.

Preferred enterprise treatment: remove this bridge in the browser-only edition.
If keeping desktop, resolve authenticated resource references in the main
process, use an explicit document-viewer policy, and make launching active
content a separate user-mediated action. The traversal fix should not be read as
remediation of this independent opener risk.

### DF-05 — Runtime tool installation does not visibly freeze the full npm closure

**Priority: medium supply-chain assurance gap; conditional on package contents.**

`server/internal/daemon/tool_install.go:162–188` pins and packs an exact top-level
release, verifies its SHA-512 integrity against the reviewed lock, then performs
`npm install --global ... --ignore-scripts` from that tarball. This is stronger
than installing a floating package and correctly suppresses lifecycle scripts.

The installer itself does not require an audited shrinkwrap or validate every
transitive package against a checked-in dependency closure. A package that ships
fully bundled dependencies or a suitable shrinkwrap may already close this gap;
the actual published tarballs were not unpacked during this review. Without that
property, transitive ranges can resolve differently on different installation
dates even though the top-level tarball matches its reviewed hash. Installation
checks subsequently execute the tool's version/export commands (`:255–280`), so
`--ignore-scripts` is not a promise that downloaded code never executes.

Enterprise mode should use a build-time assembled, signed worker image with
complete lockfiles, SBOMs, hashes, and provenance; no runtime npm installs. The
locked platform Workflow tar mechanism is a useful model to retain.

### DF-06 — Desktop IPC checks are inconsistent outside the corrected module

**Priority: medium defense-in-depth; no untrusted-web exploit demonstrated.**

The shared helper says it is the gate for privileged handlers, but auth-session
refresh/logout handlers ignore their event argument
(`frontend/electron/main/ipc.cjs:740–760`), as do some startup, updater, and workbench
handlers. Token reads have a stronger explicit session-frame check (`:632–648`).
Previews do not normally carry the privileged preload, and top-level navigation
is guarded, so missing checks alone do not prove that arbitrary websites can call
these handlers.

Apply an explicit trusted-frame policy to every privileged handler and document
the few calls intentionally allowed from auxiliary windows. Do not rely on a
preload's current exposure list as the sole long-term sender authorization.

## Controls worth preserving

| Boundary | Evidence and result |
| --- | --- |
| Electron renderer isolation | `frontend/electron/main/windows.cjs:32–35` uses context isolation, no Node integration, renderer sandboxing, and web security. `:125–137` blocks foreign main-window navigation and popups. |
| Electron package integrity | `frontend/forge.config.cjs:152–160` disables Node entry points, enables cookie encryption and ASAR integrity, and restricts loading to the ASAR. These source settings do not establish that a released binary was built with them. |
| Packaged CSP | `frontend/electron/scripts/build-renderer.cjs:59–76` injects self-only script policy, no objects, and constrained connection sources. This packaging step is specific to desktop; browser response headers need separate validation. |
| Run-scoped loopback authority | `server/internal/daemon/run_cli_channel.go:719–758` requires loopback peer/host, rejects browser Origin, checks a live capability and its frozen route policy. `:938–943` refuses API redirects. Run configuration is written privately and atomically (`:692–703`). |
| Local desktop capability | `server/internal/daemon/health.go:136–143` binds to `127.0.0.1`. `local_preview.go:277–282` uses constant-time capability comparison; `:311–350` uses 32 random bytes and an atomic `0600` file. This does not protect against same-user code on an unsandboxed worker. |
| Link-preview SSRF defense | `frontend/electron/main/link-preview-request.cjs:47–61` passes validated DNS answers to the socket itself, avoiding a separate-check rebinding race; `:81–118` checks every redirect and omits browser cookies/credentials. |
| Untrusted HTML | Mail HTML uses `sandbox=""` and no referrer (`mailbox-inbox.tsx:546–551`). Artifact/node HTML previews also use sandboxed iframes. App previews allow scripts but omit `allow-same-origin` (`artifact-app-view.tsx:507–513`). |
| Markdown, equations, diagrams | Wiki/chat rendering uses `rehype-sanitize`; the diagram view sets Mermaid `securityLevel: "strict"` and a text-size bound (`block-editor-schema.tsx:338–344`); KaTeX uses `trust: false`, strict errors and an expansion bound (`:512–520`). Library bugs remain relevant, but these raw-HTML sites are not evidence of unsanitized XSS by themselves. |
| Platform bundle integrity | `server/internal/platformbundle/platformbundle.go:29–55` embeds and verifies the committed Workflow archives and locks before use. The decoder validates canonical regular files, hash/file counts and canonical encoding (`server/internal/contentbundle/contentbundle.go:249–299`, `:378–383`). |

Desktop credentials use Electron safeStorage, and the store refuses a new
session when secure storage is unavailable. Platform semantics matter: Windows
DPAPI does not protect against all applications running as the same OS user.
The official [Electron safeStorage documentation](https://www.electronjs.org/docs/latest/api/safe-storage)
describes those limits. Do not claim that encryption at rest isolates the
daemon/provider processes from each other.

## Minimum browser product

Keep company/member administration, project/task ownership, agent templates,
assignments, approvals, run status/history, basic text conversation, audit history,
and controlled file upload/download. Preserve the existing permission and
transaction rules. These are the capabilities needed to coordinate a company;
they do not require desktop software.

Remove from the minimal shipped frontend: Electron/Forge/Squirrel, local daemon
management, raw native file handling, provider/plugin installers, application
previews, embedded arbitrary websites, LiveKit huddles/screen sharing, Studio
media/Socket.IO, rich collaborative document editing, Office/PDF renderers,
Mermaid/KaTeX, duplicated chart systems, and large design-canvas/animation surfaces.
Retain downloads as inert files on a separate content origin. Rich previews can
later return as isolated services with their own patch cadence.

`frontend/package.json` currently combines web and desktop dependencies. Merely
deploying the current web build removes desktop runtime exposure, but it does not
remove its build-time dependencies, rendering packages, or feature code. A true
minimal edition needs a distinct dependency/build entry point and a feature
contract, plus verification of its output module graph. Feature flags that hide
navigation alone are not dependency removal.

For browser-only execution, adapt runtime registration to dedicated remote
workers while retaining member ownership and run proofs. Do not register a
privileged shared worker as an ordinary member's personal machine and assume
that PostgreSQL company scoping protects the worker's filesystem. A later
API-native agent runner could remove local CLI/provider discovery entirely, but
it is a separate migration with capability and regression work.

### Scope of the initial minimal deployment scaffold

The accompanying `deploy/minimal/compose.yml` and `zenith_minimal,nomsgpack` server
build provide an initial restricted deployment. The Cloud Run publisher stub
fails closed and removes its Google SDK import closure. Private API/data networks,
loopback-only web exposure, read-only filesystems, non-root processes, dropped
capabilities, bounded resources, and disabled external integrations reduce the
delivered system's exposure. Browser attachment downloads stream through the
authorized API (`server/internal/attachment/service.go:427–446`), so keeping the
S3 endpoint on the private data network is compatible with ordinary downloads.

This scaffold still builds the existing full browser UI with its rich renderers
and full frontend lockfile. The daemon API remains available for separately
connected workers. The edge CSP currently sets frame ancestry, not a restrictive
script/connect policy. It must therefore be described as a restricted deployment
with a reduced Go link graph, not as the completed minimal browser product above.

Two remaining least-privilege tasks matter for production. The API connects as
the database initialization user, which the PostgreSQL container creates with
superuser authority; separate the migration/administrative principal from the
application role. The API's S3 credentials are also the storage root credentials;
provision a bucket-scoped application identity and keep storage administration
outside the application environment. These private data-service credentials are
not exposed as public ports, but an API compromise would still inherit their
excess authority. Documented private-preview isolation is not a substitute for
this credential separation.

## Review limits and release gates

The main/native bridge patch was verified through dependency-free manual checks.
Frontend lint, typecheck, production build, and the seven-rule Electron security
scan passed after dependency installation. No packaged Electron run, Windows
launch test, sandbox escape attempt, published installer-signature test, or full
npm tarball closure inspection was completed.

The static updater downloads through HTTPS and delegates installation to native
updater machinery (`update-download.cjs:58–108`). The policy requires signing
flags for macOS/Windows (`update-policy.cjs:60–75`), but flags alone are not
cryptographic evidence. Before calling desktop enterprise-hardened, verify a
tampered update is rejected, the signer identity is constrained, and a downgrade
cannot silently replace the current application. The metadata redirect loader
also lacks the bounded redirect count present in the package downloader
(`auto-updates.cjs:825–839` versus `update-download.cjs:235–263`).

Before production use of the minimal edition, require per-run isolation, restore
drills for PostgreSQL and uploaded assets, restrictive browser headers and content
origins, dependency/SBOM verification for the actual deliverables, and a scoped
independent authorization/execution assessment. A smaller dependency graph lowers
maintenance and exposure; it does not replace these controls.
