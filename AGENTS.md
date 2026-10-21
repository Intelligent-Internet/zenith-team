# Zenith Team Agent Guide

Use **Zenith Team** for the product name and **Zenith** as conversational shorthand.

Zenith Team is a cloud-first web and desktop product. Zenith is the shared
product-family prefix; see `docs/branding.md` for naming and compatibility rules. This file and
`server/CODEMAP.md` are the authoritative written guidance; where either
conflicts with the code, **the code wins** — fix the doc in the same change.
[`ARCHITECTURE.md`](ARCHITECTURE.md) is the one-page system overview.

## UI icons — mandatory Iconsax

All UI work for Zenith Team must use **Iconsax**, with Outline as the default.
This applies to design proposals, prototypes, new components and changes to
existing screens on both web and desktop. Import icons through
`@/shared/ui/icons` or `@/shared/ui/icon`. Do not install, import or copy icons
from another library, including Lucide, Heroicons, Phosphor, React Icons,
Tabler, Font Awesome, Material Icons or Radix Icons. When Iconsax lacks a
suitable symbol, an authored SVG matching its visual style is allowed. Reuse
or register it through the shared icon components; do not use ad hoc inline
drawings or emoji as UI icons.

When the user wants additional icons or alternatives, search the official
[Iconsax Outline catalog](https://app.iconsax.io/?style=outline). Do not limit
the search to assets already bundled in the repository. Follow the
[icon discovery workflow](frontend/AGENTS.md#finding-more-icons).

Read [frontend/AGENTS.md](frontend/AGENTS.md) before UI work for the required
asset workflow, authored-symbol guidelines and verification. Older specs,
Figma examples and copied components do not override this requirement.
Genuine brand logos, Agent avatars and illustrations retain
their authored assets; they are not substitutes for action or navigation icons.

## Repository map

```text
server            Go module: HTTP API, WebSocket planes, PostgreSQL, daemon
frontend          The client tree — Vite web app + Electron desktop packaging
docker            Development infrastructure
deploy            Production deployment manifests
docs              Design vision, vocabulary, and engineering conventions
scripts           Developer debug wrappers
```

## Server domain map (`server/internal/`)

Vocabulary — use these words and no others: **company**, **task**, **run**
(one agent execution), **agent** (a template the Company owns), **runtime** (a
provider instance a daemon registered, always private to the member who
registered it), **binding** (one member's placement of an Agent on one of
their own runtimes, `agent_runtime_bindings`, ADR 0014), **daemon** (the
machine-side background process), **provider** = `claude | codex`.
"Executor", "mission" and "hosting consent" are retired; do not reintroduce
them.

The data path is `handler → <Domain>Service → sqlc queries` (generated code
lives in `server/pkg/db/generated`, queries in `server/pkg/db/queries`).
Events use a plain envelope published post-commit on the in-process Bus. Each
domain declares its topic and event names in `internal/<domain>/events.go`;
`server/pkg/protocol` keeps only the daemon control-frame names. See
[ADR 0008](./docs/adr/0008-run-and-activity-history.md) for how Run and
Activity history is written; the typed-topic rules live in the
`server/internal/events` package doc.

```text
server/pkg/
├── protocol/         Daemon control-frame names + wire payloads
│                     (Message envelope, daemon RPC frames, structured
│                     RunMessagePayload) + the shared ActorRef/ActorKind wire
│                     type and the activity entity/actor vocabulary.
│                     No helpers: parsing lives in internal/util.
├── db/               sqlc: generated/ (package db; google/uuid overrides,
│                     pgtype nullable/timestamp values) and
│                     queries/ (per-domain .sql files). Untouchable paths:
│                     sqlc.yaml names both directories.
├── agent/            The provider-neutral ordered execution-event contract
│                     (delta/tool activity/completed item/session/finished) +
│                     agent.New(provider, cfg), the one factory
│                     switch, + the per-provider adapters as sibling files
│                     (claude_provider*.go, codex_provider*.go). These are
│                     provider adapters, not Runtimes — a Runtime is the
│                     registered instance in the `runtimes` table. The name
│                     vocabulary is protocol.SupportedProviders.
├── runfailure/       Canonical runs.failure_reason taxonomy.
└── redact/           Log/wire redaction helpers.

server/internal/
├── auth/ company/ conversation/ inbox/ approval/ attachment/
│   account/ usage/ telemetry/         Product domains. Each owns
│                     its <Domain>Service interface + Default implementation.
├── agent/            Agent templates, each member's bindings of them, and
│                     the private Runtimes those bindings name, plus the
│                     provider-neutral Connector and Plugin catalogues.
│                     A runtime is its registrant's alone and an Agent runs
│                     for a member only where they bound it (ADR 0014).
│                     PostgreSQL owns sanitized discovery projections and
│                     Connector policy; provider-native configuration and
│                     credentials remain on the daemon. Connector policy
│                     grants execution authority. Plugin state is a
│                     presentation/install surface and grants none. It
│                     also pins Workflow app hints to exact provider
│                     identities and reports one Agent's app readiness on
│                     the viewer's own binding, which restates the Run
│                     claim's rule.
├── task/             Tasks, dependencies, execution and workspace
│                     generations, the milestone catalogue, member-assignee
│                     execution and confirmed handoffs.
│                     It also owns upward done/
│                     failed outcome reports and the Run-proven Agent
│                     business-failure transition. It also owns Calendar Task
│                     schedules: assignee acceptance, recurring templates,
│                     occurrence edits and the shared Asynq Task worker.
│                     These are ordinary Project Tasks using Task capacity;
│                     Connector Schedules retain their separate non-task lane. `failed` and `error`
│                     are server-owned Task states, never ordinary PATCH
│                     values.
├── schedule/         Creator-private one-shot and cron definitions,
│                     exact reviewed Connector sources, firing receipts,
│                     Asynq scan/fire handlers, and the independent repair
│                     sweep. PostgreSQL
│                     owns firing truth; cmd/server composes the shared Redis
│                     queue lifecycle. A Schedule owns one `schedule`
│                     Conversation and its Runs use a capacity-exempt
│                     non-task lane. Transaction-participant seams in
│                     conversation/ and execution/ materialize or cancel
│                     them without turning them into Tasks.
├── workflow/         The release-owned Workflow catalog: ready-to-run
│                     templates parsed from the zenith-workflows skill
│                     (one workflow.yaml card/form/brief plus one
│                     playbook.md per Workflow) and served read-only to
│                     members. The content is authored in the plugins repository
│                     and pinned in internal/platformbundle (ADR 0016);
│                     CheckCatalog is the same parser reporting every
│                     problem, for bundlectl. Launching is an ordinary
│                     Task create; it owns no table and writes nothing.
│                     Its own package so internal/mcp/tools hosts one
│                     domain and the zen CLI, which links that package,
│                     never links the parser.
├── mailbox/          Company Email configuration, inbound metadata,
│                     provider-backed detail/attachment reads, and
│                     retention; plus Run-bound Agent Mail draft groups,
│                     optimistic review, and the durable send worker.
│                     Email is user content, so this stays separate from
│                     the notification Inbox.
├── activity/         Company action history. Task, Conversation and Run
│                     owners capture safe before/after snapshots in their
│                     mutation transaction through ObserveInTx/RecordInTx.
│                     The Bus sink retains uncovered events as version-0
│                     evidence. Feed, calendar and activity.created respect
│                     frozen Company/creator visibility, and feed and
│                     calendar also Project access (ADR 0017): a row naming
│                     a Project reads only for its readers, a project_grant
│                     row also for projects.manage holders. Subject revisions
│                     belong only to Task/Conversation. An Agent's row in a
│                     Run also names the person it acted for and the Run
│                     (on_behalf_of_user_id, run_id): producers pass
│                     events.RunActor, never a bare AgentActor, for a write
│                     a Run proves. Handlers never write Activity inline.
├── execution/        The run lifecycle kernel: enqueue/trigger, claim,
│                     Connector snapshot freeze, transitions, auto-retry,
│                     Schedule-run cancellation, sweeper support,
│                     EmptyClaimCache, and the Transactor seam. Reactions
│                     reach the daemon over the bus, not through an injected
│                     notifier. Named for the domain, not the layer — and NOT
│                     "run", which every db.Run local variable would shadow.
├── events/           The in-process Bus. Producers build an events.Event
│                     literal inside the transaction and publish after commit
│                     with bus.Publish(ctx, topic, ...); consumers register
│                     with bus.Subscribe(topic, ...) — or SubscribeAll for a
│                     sink that takes the whole stream.
├── realtime/         Receive-only client WS plane: identity-indexed hub, the
│                     Broadcaster's delivery methods, sharded Redis
│                     durable relay, the ephemeral relay for replaceable
│                     presence frames, metrics. It knows rooms and no domains;
│                     who sees what lives in cmd/server/realtime_routing.go.
├── daemonws/         Daemon WS control plane: connection maps, heartbeat
│                     frames, RPC correlation and dispatch to the handler
│                     package's transport-free adapters, wakeup fanout.
├── handler/          The Gin HTTP surface: one flat package, one file per
│                     URL resource; strict bindRequest + binding tags +
│                     errorMapping; pgtype mappers in pgx.go; daemon plane
│                     in daemon.go (/api/v1/daemon/*).
├── middleware/       Every Gin middleware, one file per functionality:
│                     common.go (the shared constants and helpers),
│                     header.go
│                     (request id, no-store, OAuth security headers),
│                     client.go, logging.go (request log + panic recovery),
│                     metrics.go, cors.go, csrf.go, auth.go, company.go,
│                     project.go (the Project access guards, ADR 0017).
│                     Each middleware is a plain function taking what it
│                     needs — a logger, a config, or a domain service the
│                     caller holds (auth.AuthService, company.CompanyService,
│                     company.ProjectService, auth.CSRFProtector). It
│                     declares no service interface of its own and nothing
│                     is injected into it.
├── db/               pgxpool, migration runner, session advisory lock,
│                     readiness, and the Store: Queries() for reads,
│                     WithinTx for mutations, WithinRawTx when a flow also
│                     needs the raw statement handle. The daemon plane uses
│                     execution.Transactor + Queries.WithTx instead, because it
│                     holds no Store. Plus the schema tests.
├── testutil/dbtest/  Shared Postgres test scaffolding.
├── util/             Boundary helpers: pgtype (ParseUUID/MustParseUUID/
│                     UUIDToString), canonical id parsing (ParseGoUUID/
│                     ValidUUID), strict JSON body decoding, and the
│                     transitional google/uuid bridge.
├── authfile/         The versioned credential store (atomic 0600 writes,
│                     one cross-process lock per file, Ensure refresh): the
│                     CLI's auth.json and the daemon's own daemon-auth.json
│                     (its session and its output presenter). The
│                     daemon never reads auth.json, and no Agent process
│                     reads either: zenith-team-mcp goes through the Run channel.
├── runchannel/       The Run channel wire (ADR 0009): run.json, the request
│                     and answer documents and the client that posts them to
│                     the daemon. A leaf (standard library and uuid) so zen,
│                     the daemon and the mcp-sealed zenith-team-mcp share it.
├── daemon/           The machine-side daemon runtime (config, health, wakeup
│                     WS, heartbeat, claim, prepare, transcript stream),
│                     provider-native Connector/Plugin discovery and
│                     operations, plus coalesced capability refresh and a
│                     Connector discovery keepalive that re-observes
│                     before the claim's freshness window closes. It
│                     publishes sanitized Runtime-scoped replacement
│                     snapshots (reaffirming an unchanged one) and
│                     resolves a claimed Run's frozen Connector contract
│                     back to local provider settings.
│                     run_cli_channel.go is the Run channel: run.json in a
│                     Run's isolated ZENITH_TEAM_HOME plus the loopback proxy
│                     that lets `zen` in Agent mode and zenith-team-mcp call the
│                     API with the daemon's session and the Run proof (ADR
│                     0009). Its policy has one row per zen command and per
│                     route a zenith-team-mcp tool the Run is offered calls; no
│                     channel means no zenith-team-mcp for the Run.
│   ├── workspace/    Managed workspace/worktree preparation (zenith-team design)
│   ├── repocache/    Shared clone cache
│   └── execenv/      Provider execution env: codex home + managed TOML
│                     blocks, claude settings, CLI mapping, skill bundles,
│                     and the macOS Seatbelt profile that keeps every
│                     provider process and app preview out of the
│                     sign-in files and other Runs' homes, and lets it
│                     write only its own folders under the workspaces
│                     root; a Run tagged in a chat also cannot open the
│                     rest of that root, the repository cache or a
│                     bound folder
├── infra/            Technology adapters: cloudrun/ (application publication),
│                     email/, storage/ (S3 objects and Cloudflare KV publication
│                     route projections), and validate/.
├── config/           Env config. There is no internal/app: the composition
│                     root is cmd/server (app.go, listeners.go, workers.go,
│                     runtime_sweeper.go, redis.go, auth.go, lifecycle.go).
├── platformbundle/   Pinned platform content from the plugins repository
│                     (ADR 0016): one canonical tar and one lock per
│                     plugin component, plus the harness SKILL.md zenith-team
│                     keeps for zenith-workflows. Every tar is verified
│                     against its lock at load; the server refuses to
│                     start on a failure and zenith-team-mcp drops only that
│                     bundle. bundlectl is the only writer; never edit the
│                     tar or the lock by hand. Its own package for the
│                     cmd/zen link set: the CLI links internal/mcp/tools
│                     and must never link the bundle bytes. It imports
│                     only internal/mcp/tools and the asset-free internal/contentbundle verifier.
├── contentbundle/    Asset-free archive/lock verification and managed Tool
│                     release contract shared by bundlectl, platformbundle
│                     and the daemon. Its own package for the cmd/zen link
│                     set: Studio must not pull Workflow bundle bytes into
│                     the CLI. Studio's source is plugins/zenith-studio in
│                     the plugins repository; its single JSON lock in daemon/tool_releases
│                     carries metadata and provenance without an archive.
│                     bundlectl prepare resolves verified npm skills into the
│                     ignored generated/ folder before compiling the daemon/CLI.
└── mcp/              The zenith-team-mcp stdio server (mcp-sealed: beyond its
                      own tree it imports only internal/runchannel,
                      internal/authfile, internal/platformbundle,
                      internal/contentbundle, internal/task/milestone and
                      pkg/studiocanvas): Run-scoped Artifact presentation,
                      Company Plan, Agent Mail, Task failure, Question,
                      Agent roster, and execution-skill tools, plus the
                      compiled zenith-platform skill. It holds no
                      credential; every call goes over the Run channel.
```

Deleted with the rebuild (do not resurrect): internal/executor,
internal/executorws, the old internal/run + run/event + run/trigger
(the current run kernel is internal/execution),
internal/websocket/protocol, internal/inbox/question (HITL),
internal/agentconsole, cmd/executor, cmd/agenttap, missions, the `bex_`
election/fencing model, app-side UUIDv7 id minting, the old validated
event envelope, and internal/onboarding — the ten-step Starter Journey, its
`GET`/`PATCH /companies/{companyId}/starter-journey` endpoint and the
onboarding intent. Agent setup and first-plan milestones remain
`companies.agent_setup_completed_at` and `companies.first_plan_applied_at`.
The Company domain's `company_initializations` receipt separately tracks the
founder's one bounded background research Run, which writes linked Company Wiki pages; it is not a Starter Journey.
Also deleted with ADR 0014: runtime visibility and sharing
(`runtimes.visibility`, its revision, the visibility route, dialog and
indicator), hosting consent and `HostedRunPermitted`, `agents.runtime_id`
with `agents.runtime_bound_by_user_id`, the `runtimes.manage` permission,
and the `expectedAgentIds` removal plan.

Test scaffolding: `server/tests/testpostgres` owns one PostgreSQL container
across package processes; tests are co-located with their package. `internal/testutil/dbtest` allocates the
isolated database + fixture per caller.

### Adding code to a domain package

1. **Find the domain that owns the noun and add a file — not a package.** A
   domain noun gets one consolidated file (`inbox/service.go` is the whole
   inbox domain); an aspect gets its own `<subject>_<aspect>.go` only when a
   cohesive subsystem earns it (`company/resource_service.go`,
   `daemon/execenv/`); tests sit beside their subject. A newcomer must be
   able to guess the location from the concept.
2. **A new package needs a forcing function.** The only accepted reasons are
   an import cycle, the `cmd/zen` link set (the CLI must never grow a
   server-service dependency — check `go list -deps ./cmd/zen`), or a
   domain subpackage when one package would otherwise host two domains.
   Record the reason in the package comment and mirror it in `CODEMAP.md`.
3. **Services are named types, not a god-service.** Every service is a trio:
   the interface `XService` carrying the implementation's full exported
   method set, the implementation `DefaultXService`, and the constructor
   `NewXService` returning `*DefaultXService` — with
   `var _ XService = (*DefaultXService)(nil)` directly under the interface.
   Bare `Service` and `NewService` are forbidden. The concept names the
   type, not the package path, and absorbed concepts keep their own trio
   (`ResourceService`, `ApprovalService`, `RuntimeService`).
4. **Prefix by subject, never by package.** `company.SearchPage`, not
   `company.CompanySearchPage`; method names never re-carry the type's prefix
   (`ResourceService.ListResources`, not `ListCompanyResources`).
5. **Closed enums use `Type`, not `Kind`** — `inbox.Type`, `ApprovalType`,
   `SearchType`, `ReportType` — with constants prefixed by the type name and
   distinct families kept distinct.
6. **Error sentinels are per-surface.** Reuse the package's sentinel only when
   `handler` maps both to the identical `{status, code, message}`; otherwise add
   an `ErrX*` family (`ErrResource*`, `ErrPlan*`, `ErrApproval*`).
   `server/internal/handler/error_mapping_test.go` adjudicates every collapse.
7. **Cross-domain writes are exported functions taking the caller's
   `*generated.Queries` handle** (`inbox.CreateInTx`,
   `conversation.MaterializeScheduleRunInTx`,
   `task.ReconcileOutcomeReportInTx`) — the caller owns the transaction.
8. **Never create `doc.go`** (`make go-shape` fails the build); the package
   comment lives atop the package's primary file. No `.(*generated.Queries)`
   type assertions; no identifiers starting with `Postgres`.
9. **Validate at two places only** — the API request boundary (handler DTO,
   `binding:` tags, value parsers) and immediately before a database write
   (`normalize*Input`, domain invariants). **Never re-validate a row read
   back from the database**: the schema already enforces NOT NULL, CHECK and
   FK, sqlc enforces the Go types, and a re-check is an unreachable branch no
   test can honestly exercise. Untrusted external documents (agent-supplied
   YAML) are input, not a read-back, and are still validated.
10. **Constants earn their names.** A value used once in production code is
    written inline; a named constant is for values used in several places or
    cross-referenced by another package. Do not invent a constant to avoid a
    literal.
11. **Verify**: `make go-shape`, `make go-sqlc-check`, then the affected
    package's tests, then `make gate` before merge.

## Non-negotiable rules

- CLI-backed managed Tools that require sign-in must isolate credentials by
  machine → Company → Agent → Tool → API origin. Login, status, cancellation,
  Run launchers and editor processes must all use the daemon's generic
  `localToolService.authHome` scope resolver. Runtime/provider and Run IDs do
  not replace the Agent scope. Installations may be shared; authentication
  state and browser jobs may not. Never inherit an ambient or legacy
  Company-wide credential home or copy that account into other Agents.
  Sanitized cloud Tool observations identify the Agent; credentials stay local.

- PostgreSQL is the authority for synchronized structured product state.
  Sanitized Runtime-scoped Connector and Plugin discovery projections are
  product state and are published there; provider-native configuration and
  credentials are not. Other daemon-local state is deliberately *not* product
  state and is never copied into PostgreSQL: execution scratch, the shared
  repo cache, the per-run codex home and its managed config blocks, the
  skill-bundle cache, and `~/.zenith-team` (daemon id, config, credentials).
  Execution machinery can be prepared again, with reauthentication where
  necessary. Authored `main`, Company Memory, and unmerged Task working files
  currently have no automatic cloud copy: preserve them until disposition is
  proven. Saved Outputs pin uploaded Asset Versions; they do not back up the
  whole Workspace.
- Do not add SQLite, a local ledger, entity synchronization, jrpc2, or legacy
  protocol compatibility.
  Browser document snapshots and pending CRDT edits may use IndexedDB for
  caching and draft recovery. PostgreSQL acknowledgements alone establish saved
  product state; cached permissions never grant editing or publication authority.
- Gin and gorilla/websocket are transport boundaries; domain packages remain
  transport-independent. `internal/realtime` and `internal/daemonws` own the
  WS planes; handlers do the HTTP upgrade.
- Schema style: DB-generated UUID entity ids (`gen_random_uuid()`),
  including Runs; real FOREIGN KEY edges — CASCADE for owned children, SET
  NULL for optional pointers, none on attribution columns or usage fact
  tables; the circular
  task<->conversation and schedule<->conversation edges are DEFERRABLE. sqlc
  maps UUID columns directly to google/uuid while nullable scalar/timestamp
  columns use pgtype; id validation is version-agnostic (`util.ParseGoUUID`).
- Task ownership is Company → Project → Task. Every Task has a required, immutable
  Project in its Company; dependencies stay within that Project. Project keys are
  Company-unique and immutable. Company Plan v5 carries Project identity; Task Runs use independent Project
  repositories selected by their frozen Project UUID. Every Task names its
  assignee — a member, responsible for running it on their own computer —
  optionally the Agent that does the work (`executedBy`), and a closed
  `MilestoneType`. Only the assignee starts a Run, and it executes on the
  assignee's binding for `execution.agent` (ADR 0014). For human work, that
  Agent assists in the Task chat: the creating Agent, or the CEO when a member
  created the Task. `executedBy` remains the human. Moving a Task that has run
  to another member or another Agent is a confirmed handoff that cancels the
  live work and advances both generations. `failed` is an Agent-declared business outcome proven by the exact
  live Run; `error` is a technical Run outcome. Both are server-owned and
  ordinary create/update requests cannot select them. A Task Run freezes the
  Task execution generation, and every retry inherits it.
- Agent concurrency limits only Task-Conversation Runs. Direct- and
  Schedule-Conversation Runs neither wait for nor occupy Task capacity;
  Schedule policy independently controls occurrence supersession and
  invalidation.
- Every JSON field Zenith Team itself serializes is camelCase. That covers
  `json:"..."` struct tags, the keys of `map[string]any` event and WS-frame
  payloads, and any JSON object written by hand. Exactly two exceptions:
  `pkg/db/generated/**`, the sqlc DB models, which mirror snake_case columns;
  and `pkg/agent/**`, which decodes other vendors'
  formats (the Claude CLI's stream-json, the Codex app-server's JSON-RPC and
  rollout files) where the field names belong to those vendors, not to us —
  renaming them zeroes out token usage at runtime with no compile error. Not
  JSON and therefore untouched: SQL and migrations, database column names, Go
  field names, `ZENITH_TEAM_`-prefixed env vars, CLI flags, the
  `zen config set <key>`
  vocabulary, URL query parameters, YAML authoring formats such as the Company
  Plan transaction, slog field keys, and string *values* drawn from database
  vocabularies (`status`, `action`, and failure-reason enums).
- `frontend/` is the only client tree and a standalone workspace; it carries
  both the web app and the Electron desktop packaging. For tasks scoped to
  `frontend/**`, treat its checked-in `package.json`, `README.md`, and
  neighboring code as authoritative for implementation stack and conventions.
  The server's route table and `pkg/protocol` are authoritative for API and
  transport contracts.
- Do not write, add, or restore tests under `frontend/**`, including web UI,
  Electron, unit, integration, snapshot, and end-to-end tests. Do not run
  `pnpm test`, `pnpm run test`, `pnpm --dir frontend test`, or equivalent
  frontend test commands. Validate frontend
  changes with lint, typecheck, build, and relevant manual UI checks. This rule
  takes precedence over the general test-first rule below. Server-owned tests
  remain in scope for backend work.
- RTK Query owns server state; plain Redux slices own UI-only state. The RTK
  layer uses `fakeBaseQuery` + per-endpoint `queryFn`, so the real transport is
  the axios client in `frontend/src/lib/api-client.ts` — RTK sees no URL and
  cannot dedupe by request. Client types are generated from the OpenAPI
  contract in `docs/api/spec/`, whose `openapi.yaml` is an index that $refs
  into one file per domain beside it — edit those, not the index. Do not
  hand-write a type the server owns.
- Provider adapters never import HTTP, PostgreSQL, or Zenith Team business domains.
- **There is exactly one principal type**, `auth.UserPrincipal`, behind one
  context accessor. Its `CompanyID` and `Role` are zero until the company
  middleware resolves access for a company-scoped route. There is no
  company-scoped principal type and no second accessor under any name. The
  middleware establishes the request identity and initial access decision; a
  mutating service does not derive another principal, but it does take the
  canonical Company then acting-membership locks and recheck the same
  membership's active status/role inside its write transaction. That is a
  lifecycle fence against concurrent revocation or account deletion, not a
  second authorization model.
- **Roles assign, permissions enforce** (ADR 0012). The Company permission
  catalog in `auth/permission.go` is the only place a role name meets a
  permission name. A route declares its permission through
  `middleware.RequireCompanyPermission`; a service asks `principal.Can` or
  `role.Can` beside the lock it takes; a query that must stay honest under
  concurrent revocation takes `sqlc.arg(actor_roles)::text[]` and the caller
  passes `auth.RolesWith(permission)`. Never compare a role to `owner`
  outside that file except for the owner-count rule itself. Runtime ownership
  is not a permission (ADR 0014): a computer belongs to the member who
  registered it, nobody else can see it, and no role overrides that —
  `tasks.assign` is the one permission that moves work between members.
  Actor type is likewise not a permission: every route says in
  `handler/route_actor.go` whether it accepts a Run (`person`, `run` or
  `any`), and `middleware.RunProofPolicy` refuses a Run proof on every
  `person` route, and on any route the table leaves out, before
  authentication (`RequireHumanActor` stays as a second guard on Team and
  Company lifecycle routes). A daemon's own session
  (`auth_sessions.client = 'daemon'`, `POST /auth/daemon-session`) is never
  its person: the credential guards admit it on the daemon plane and, beside
  exactly one live Run proof of its person, on `run` and `any` routes, and
  answer `403 run_proof_required` everywhere else.
  Access inside a Project (ADR 0017) is a second closed catalog,
  `auth/project_permission.go`: permissions granted to one person on one
  Project, never a role. `auth/project_access.go` holds the checks
  (`ResolveProjectAccess`, `RequireProjectPermission`,
  `RequireTaskPermission`, `RequireTaskChatPermission` and
  `RequireProjectContentPermission` for decisions in a Task chat,
  `HoldsProjectPermission` / `RequireProjectPermissionHeld` for someone
  other than the caller or under a Conversation lock, and
  `TaskChatDecidersInTx` for who may decide there).
  `auth/project_audience.go` decides a realtime frame's
  Project audience in the producing transaction (`ProjectFrame`,
  `StampProjectAudiencesInTx`). The rule itself lives in the
  `project_access` family of views and `project_permission_held`. A
  person's access is the union of their own grant, each of their Groups
  (`project_group_grants` through `company_group_members`), the
  Company-visible floor and the management bridge; every
  Group arm keeps the rejoin fence, and `ProjectAccess.Granted` stays the
  person's own rows (`GroupGranted` is their Groups'). What a
  delegate (a Run, or the Huddle voice assistant) may do on a Project is one
  rule on top of them: `delegate_permission_held` (its Authorizer
  holds the permission and read, never through the management bridge, and
  a Task chat's answer stays in its Project; no chat audience narrows it)
  and `run_permission_held`, the same for one Run from its own row. Enqueue,
  the claim, every Run-proof read (`task.RunScope`,
  `ListRunReadableProjects`) and the voice reads ask them, and nothing
  restates it. The voice assistant's Authorizer is the person who shared
  Company context, and only while they are in the call
  (`HuddleContextGrantActive`). SQL names no permission or role, and
  Go binds them through `auth.ProjectPermissionHeldSets()`. Only the
  `project_grants` and `project_group_grants` table definitions and their
  CHECKs name a permission in SQL. The resolved set travels to the
  client on every Company read; the client gates on it and never re-derives
  policy from the role name.
- **Transactions go through `db.Store.WithinTx`, its raw-handle variant
  `db.Store.WithinRawTx` (for flows that interleave hand-written SQL with
  generated queries in one commit), or `execution.Transactor` +
  `Queries.WithTx`** — no fourth mechanism. Collect events inside the
  transaction and publish them only after it commits.
- **Error translation is table-driven**: domain sentinels map to
  `{status, detail, code}` through `errorMapping` + `writeMappedError`, over
  the single `{detail, code, requestId}` envelope. Every code lives in
  `handler.errorCodeRegistry` and is diffed against `docs/api/errors.md` in both
  directions, so a code cannot reach the API undocumented and cannot be
  documented without existing. Handlers never invent an
  error body. `server/internal/handler/error_mapping_test.go` pins every row.
- **State design choices as Zenith Team's own.** Explain the reason in the
  code, comment, ADR, or commit message; never cite another product as the
  source.
- **Every identity is Zenith Team.** New code, docs, env vars, paths, and
  identifiers use `ZENITH_TEAM_` / `zenith-team` / `zenith_team` (table in
  `docs/branding.md`); the legacy product name appears only in the external
  contracts that page lists, and nothing reads it for compatibility.
- Tests precede behavioral implementation.
- Tests are co-located with their package, flat: unit tests and
  `*_integration_test.go` infrastructure suites sit beside the code. There is no
  separate tests/integration tree. Integration files carry a
  `//go:build integration` tag, so they compile only under
  `go test -tags=integration`, which is what `go run ./tests/testpostgres` runs.
- A helper used by both lanes must live in an untagged `*_test.go` file. Putting
  it in a tagged file breaks the unit build of every package that uses it.
- CI gates pull requests on the unit lane (`make go-unit`), not the integration
  suites. The integration job runs nightly and on manual dispatch, so
  **nothing on the merge path executes the DB, object-storage, or Redis
  suites** -- run `make go-integration` yourself before merging changes to
  migrations, sqlc queries, or fixtures. Local `make gate` includes the full
  PostgreSQL integration suite with the race detector as part of `go-gate`.
- CI (`.github/workflows/ci.yml`) runs on pull requests and pushes to `main`.
  Its lanes are path-gated: each lists the files its checks read, shared
  inputs included (`docs/api/**` and the deploy env template for the server
  lane, `docs/api/spec/**` for the frontend lane), and the `Makefile` runs
  every lane except `workflows`. The server lane runs `go-fmt-check`,
  `go-lint` (on pull requests, only findings the branch adds) and `go-unit`,
  which includes the pinned sqlc drift check. The frontend lane runs lint,
  typecheck, the API contract checks, a production build and the Electron
  security scan; `voice` runs ruff and the Python tests; `selfhost` builds
  both images and boots the Compose stack; `workflows` runs actionlint and
  zizmor when `.github/**` changes. `ci-gate` is the one required check: it
  fails when any lane failed or was cancelled. `.github/workflows/nightly.yml`
  runs the PostgreSQL integration suites, the Windows platform-content check
  (the pinned content in `internal/platformbundle`, `tools/builtin/` and
  `platform_skills.go` survives a CRLF checkout) and the macOS daemon sandbox
  tests. Secrets and vulnerable dependencies are GitHub's job: secret
  scanning with push protection and Dependabot alerts are on. Commit-message,
  branch-name and staged-secret checks are local git hooks in `.githooks/`
  (`make hooks`). Merges to `main` build no image. Pushing a
  `v<major>.<minor>.<patch>` tag (or an `-alpha.N`, `-beta.N` or `-rc.N`
  pre-release) at a commit on `main` verifies again, scans Go dependencies
  fail-closed, and publishes the server and web images to GHCR and the `zen`
  CLI to a GitHub Release (`.github/workflows/release.yml`). Nothing here
  deploys.
- Preserve unrelated user changes and never commit credentials or generated
  build output.

## Local development cleanup

After finishing work, stop the backend and frontend development processes so
the user can start their own test session. This includes the Electron development
launcher when used. Verify that the backend and frontend ports (8788 and 5173 by
default) have no remaining listeners. Leave infrastructure containers running.

## Verification

```bash
make gate
```

The complete gate is defined once in the root Makefile.
