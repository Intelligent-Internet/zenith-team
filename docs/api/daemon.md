---
title: Daemon plane
summary: Runtime registration, model discovery, work claiming, and run callbacks
---

Everything under `/api/v1/daemon/*` is consumed by the machine-side daemon.
Browser and desktop clients use the user-plane resources documented in
[runs.md](runs.md) and [realtime.md](realtime.md).

Local working directories and provider environments are documented in
[workspaces.md](workspaces.md). That page distinguishes the served directory
override from the Planned managed-only release and documents local loopback
file/review capabilities separately from this server-side daemon plane.

## Authentication and transport

Send the daemon's own bearer session on every HTTP request and on the
WebSocket upgrade:

```http
Authorization: Bearer <accessToken>
```

The daemon's session belongs to the same person as the app's but is a
session of its own: `POST /api/v1/auth/daemon-session` starts it from the
person's session ([Authentication](authentication.md)), `zen daemon start`
or the desktop app stores it in `daemon-auth.json` in the daemon's home, and
the daemon renews it with `POST /auth/refresh` like any bearer session. The
server accepts it on every route of this plane. Off this plane it acts only
for a Run: on a route that accepts a Run, beside exactly one live
`X-Zenith-Team-Run-Token` whose Run works for the same person — the Run channel's
and the output presenter's calls. Anything else is
`403 run_proof_required`; it is never its person. Older daemons that still
send the person's own session keep working on this plane.

Cookies are not accepted, so CSRF never applies. A missing credential returns
`401 authentication_required`; an invalid or expired credential returns
`401 session_invalid`.

Runtime- and run-scoped routes verify that the authenticated user is an active
member of the owning company. Missing resources return the owning resource's
code — `404 runtime_not_found`, `404 run_not_found` — and membership denials
return `404 company_not_found`; this plane does not reveal cross-company
existence with a `403`.

The daemon also names its machine on every request:

```http
X-Zenith-Team-Daemon-ID: <daemonId>
```

The value is the `daemonId` it sent on register. Runtime-scoped routes —
heartbeat, deregister, prepare-lease, skill-bundle resolve, recover-orphans,
model-discovery results, the task-merge routes, the connector and plugin
discovery `PUT`s and the connector discovery reaffirm `POST` — require it to
match the runtime's `daemonId`, in addition
to membership. A mismatch is the same non-disclosing `404 runtime_not_found`
an unknown runtime gets; the deregister batch skips the id instead. Claim
already proves `daemonId` from its body.

A runtime is also private to the member who registered it
([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)), so every
runtime-scoped route, and the claim, also requires the signed-in user to be
that registrant. Run-scoped routes
(`/daemon/runs/{runId}/…`: status, start, wait-local-directory,
wait-studio-auth, usage, session, complete, fail, cancel-ack) apply the same
two checks to the run's runtime: a run whose runtime another daemon or
another member registered is the same `404 run_not_found` an unknown run
gets, reads included. The deregister batch skips a runtime another member
registered, as it skips one another daemon registered. Rollout: a missing
header is admitted with a server-side warning this release. Such a request
skips only the `daemonId` match; the registrant check still applies, so no
member can take another member's runtime offline. Once the server runs with
`APP_DAEMON_ID_REQUIRED=true` it is refused as `400 invalid_request` before
any lookup — a `400`, not a `404`, so an old daemon does not mistake it for
the runtime-gone signal and loop on re-registration.

Routes that bind JSON limit the body to 4 MiB and reject unknown fields.
Malformed JSON is `400 invalid_request`, an oversized body is
`413 request_too_large`, a non-JSON content type is
`415 unsupported_media_type`, and struct validation failures are
`422 validation_failed`. A malformed path id is `404`, per the API-wide rule
in [conventions.md](conventions.md) — an id that cannot name a row is
indistinguishable from one that names nothing. Error bodies use the standard
`{detail, code, requestId}` envelope.

The daemon plane follows every user-plane convention — it is the same API
with a different caller. Successful responses are endpoint-specific:

- lists — batch claim, registration, companies, skill bundles — use the same
  `{ "items": [...] }` wrapper as every user-plane list, with `[]` as the
  authoritative empty result;
- start, wait, complete, and fail return a bare `DaemonRun`;
- acknowledgement-only commands return `{ "status": "ok" }` — and only after
  the write actually happened, never over a swallowed failure;
- timestamps are RFC 3339 UTC, exactly as on the user plane.

## Shared response objects

### Daemon runtime

Registration returns this machine-oriented runtime projection — exactly the
fields the daemon decodes, nothing more. The daemon is the machine, so its own
id, device string, and heartbeat clock are facts it already holds; those live
on the user-plane `Runtime` projection, where a browser needs them.

| Field | Type | Notes |
|---|---|---|
| `id` | string | Runtime UUID. |
| `companyId` | string | Owning company UUID. |
| `name` | string | Display name. |
| `provider` | string | `claude` or `codex`. |
| `status` | string | `online` or `offline`. |

### Daemon run

`DaemonRun` is the run projection returned to the daemon: exactly what it
reads to execute — the routing ids, the enqueue-time context document, and the
resume pointer. Lifecycle fields the daemon never reads — status, attempts,
timestamps, failure text — are not served; the user plane's `Run` is where
those live.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID string | Database-generated Run identity. |
| `companyId` | string | Owning company UUID. |
| `agentId` | string | Executing agent UUID. |
| `taskId` | string, optional | Owning Task UUID for Task runs; omitted for direct and Schedule runs. |
| `conversationId` | string | Conversation UUID. |
| `runtimeId` | string | Assigned runtime UUID. |
| `context` | object, optional | Frozen prompt, Agent configuration, initial Company description, execution profile and baseline Resources, plus claim-reviewed Connector snapshot. Custom Skills/history snapshots are not currently produced by normal enqueue. |
| `forceFreshSession` | boolean | Prevents resuming the earlier provider session. |

There is **no trigger-message id**: the words the run answers arrive frozen in
`context.prompt`, so the daemon never resolves a prompt by id and had
nothing to do with the field.

Claim responses may additionally carry:

| Field | Type | Notes |
|---|---|---|
| `authToken` | string | Short-lived `brt_` credential for the claimed run. |
| `priorSessionId` | string, optional | Resumable session from the latest terminal run for the same conversation and agent, offered only when that run ran on this run's runtime. |
| `priorSessionThroughSeq` | integer, optional | The conversation seq through which that prior session received the chat. Served whenever the session exists, even when `priorSessionId` is withheld because the session cannot be resumed. |

Optional fields are omitted. Machine-local execution details are not stored in
or returned by this API.

The `context` document carries the run's frozen inputs, stamped in the enqueue
transaction: the prompt and the agent execution brief — name,
instructions, model, thinking level — read from the agents row at enqueue.
The daemon executes what the context says; it never fetches the
agent back through the user plane. It also carries `conversationSnapshot`,
the bounded prior chat of the run's conversation with each message's author,
and, when a member's message started the run, `trigger`: that member and the
enqueue time.

### Run transcript item

The durable transcript projection is provider-neutral. It contains `id`,
PostgreSQL-assigned `seq`, stable `itemKey`, `role`, complete structured
`parts`, `itemDigest`, and `createdAt`. Roles are `agent`, `tool`, and `system`;
part types are `text`, `reasoning`, `toolCall`, `toolResult`, and `error`.
Provider-native envelopes never cross this interface.
One completed item's canonical JSON is limited to 2 MiB before it enters the
daemon WebSocket. Tool results may include the normalized status and the
provider-reported non-negative exit code and duration; Zenith Team never silently
truncates them to fit the transport.

**`seq` belongs to the Conversation, not to the Run.** A transcript item is a
row of the one conversation stream that the user's messages are also rows of, so
the server allocates its `seq` from that single counter under the conversation
row lock. A Run's own items therefore ascend but are not dense — a message the
user sends mid-turn takes a number in between. The daemon cannot predict the
value and must take it from the receipt.

## Runtime registry and control

### Register runtimes

```http
POST /api/v1/daemon/register
```

Request:

```json
{
  "companyId": "497f6eca-6276-4993-bfeb-53cbbbba6f08",
  "daemonId": "daemon-01",
  "deviceName": "build-host",
  "cliVersion": "2.1.4",
  "launchedBy": "desktop",
  "runtimes": [
    {
      "name": "claude (build-host)",
      "provider": "claude",
      "version": "2.1.4",
      "status": "online"
    }
  ]
}
```

`companyId`, `daemonId`, and at least one runtime are required. Each runtime's
`provider` is required and must be exactly `claude` or `codex`. Provider names
are case-sensitive and are not trimmed, lowercased, or replaced with a fallback;
entries also accept `name`, `version`, `status`, `unusableReason`, `signIn`
and `credentialIsolation`.
Blank names are generated from the provider and device name. `status:
"offline"` marks the runtime offline; an omitted or other value becomes
`online`. The server upserts each entry on `(companyId, daemonId, provider)`.

`unusableReason` is `{code, message}` for a provider the daemon found but
cannot run: `code` is `below_minimum_version` or `not_executable`, `message`
is the daemon's sentence (at most 256 characters, never a path), for example:

```json
{
  "name": "Codex (build-host)",
  "provider": "codex",
  "version": "0.90.0",
  "status": "offline",
  "unusableReason": {
    "code": "below_minimum_version",
    "message": "Codex CLI 0.90.0 is below the minimum 0.100.0"
  }
}
```

An entry carrying a reason is stored `offline` whatever its `status` says,
and heartbeats keep it offline; the user plane serves the reason on the
`Runtime`. An entry without a reason clears any reason stored earlier — the
daemon sends exactly that once the CLI is fixed. An unknown `code`, a missing
`message`, or a message over 256 characters is `422 validation_failed` before
any database access, like an unknown provider.

An entry may also carry `signIn`, the provider CLI's own sign-in verdict from
the same probe round:

```json
{
  "name": "Codex (build-host)",
  "provider": "codex",
  "version": "0.154.0",
  "status": "online",
  "signIn": {
    "state": "signed_out",
    "detail": "Not signed in. Run `codex login` on this computer, then refresh.",
    "checkedAt": "2026-09-16T09:00:00Z"
  }
}
```

`state` is `signed_in`, `signed_out`, or `unknown`; `detail` is at most 256
characters and never a token; `checkedAt` is RFC 3339. The verdict is stored
beside the version and served on the user-plane `Runtime`; it never changes
`status`. An entry without `signIn` keeps the verdict stored earlier, so an
older daemon never blanks it. An unknown `state` or a malformed `checkedAt`
is `422 validation_failed`. The daemon sends a fresh round on its own when
the verdict changes, and on the `daemon.pending_work` hint of kind
`runtime_refresh` that the user plane's `POST …/runtimes/{runtimeId}/refresh`
raises.

Every entry also carries `credentialIsolation`, the daemon's verdict for the
whole computer from the same round:

```json
{
  "name": "Claude (build-host)",
  "provider": "claude",
  "version": "2.1.4",
  "status": "online",
  "credentialIsolation": "enforced"
}
```

`enforced` means the daemon runs on macOS and starts every provider process
of the runtime, and every app preview's commands, inside a sandbox that can
neither read nor write the Zenith Team sign-in files on that computer: the
daemon's home (`daemon-auth.json`, an operator's `auth.json`,
`local-preview.json`), `~/.zenith-team` and the desktop app's session store. The
same sandbox keeps each Run out of every other Run's isolated home, so no
Run reads another's `run.json` and calls through its channel, and refuses
to start `launchctl`, `open` and `osascript` or to write the person's
launchd agents folder and shell start-up files. Each round tests it: a
process in the sandbox must fail to read `daemon-auth.json` and a stand-in
Run's `run.json`, and must start. `not_enforced` means
anything else: Windows, Linux, a failed test, a workspaces root inside the
daemon's home, or the test switch `ZENITH_TEAM_DAEMON_CREDENTIAL_ISOLATION=not_enforced`
on the daemon, which lets a Mac play a Windows computer. The switch only
lowers the report; the daemon refuses to start with any other value. The
daemon wraps a provider or a preview exactly when the last round's test
passed, and a Run or a preview that started unwrapped keeps the report at
`not_enforced` until it has stopped, so `enforced` never covers such a
process running outside the sandbox.
Any other value of `credentialIsolation` is `422 validation_failed`; an entry without it (an older daemon) is
accepted. The server checks the value and does not store it, and nothing
on the server uses it.

Success returns:

```text
{ "items": [<DaemonRuntime>] }
```

Registration is one transaction: either every entry lands or none does, and
the roster broadcast follows the commit. Retrying is safe for stored runtime
state because the same key is upserted, although broadcasts can be repeated.

### Deregister runtimes

```http
POST /api/v1/daemon/deregister
```

Request and response:

```json
{ "runtimeIds": ["497f6eca-6276-4993-bfeb-53cbbbba6f08"] }
```

```json
{ "status": "ok" }
```

An id is skipped when it names no runtime, a runtime of another daemon (when
the `X-Zenith-Team-Daemon-ID` header is sent), a runtime another member
registered, or a runtime in a company where the caller is not an active
member — deregistering something already gone is the converged state, not an
error. A **failed write is `500`**, with nothing confirmed: the `200` receipt
means the batch was applied, never that a failure was swallowed. Repeating
the request is safe.

### Heartbeat

```http
POST /api/v1/daemon/heartbeat
```

```json
{
  "runtimeId": "497f6eca-6276-4993-bfeb-53cbbbba6f08",
  "credentialIsolation": "enforced"
}
```

The server refreshes liveness and marks the runtime online.
`credentialIsolation` repeats the daemon's current verdict (see Register
runtimes), so a Mac whose sandbox stops working says `not_enforced` on its
next beat; the WebSocket `daemon.heartbeat` frame carries the same field. It
is optional, any other value is `422 validation_failed` (over the WebSocket
the frame gets no ack), and it is not stored yet. Success returns:

```json
{
  "status": "ok",
  "runtimeId": "497f6eca-6276-4993-bfeb-53cbbbba6f08",
  "serverCapabilities": ["rpc-v1", "model-discovery-v1"],
  "pendingModelList": {
    "requestId": "0197f61a-b881-7a22-b1ca-a3e972e2f21d"
  }
}
```

The reply names the runtime it acknowledges, so the HTTP body and the
WebSocket `daemon.heartbeat_ack` carry the same payload.

An unknown runtime, one outside the caller's companies, or one registered by
another daemon is `404 runtime_not_found`. Heartbeats are safe to repeat. The WebSocket heartbeat acknowledgement can additionally report
`runtime_gone` without closing the socket. Both HTTP and
WebSocket heartbeats use the same atomic claim: `pendingModelList` is present
only when the daemon advertised `model-discovery-v1`, and a competing
heartbeat cannot claim the same request. `daemon.pending_work` is only a hint
to heartbeat sooner; it does not carry or reserve work — with one kind as the
exception: `runtime_refresh` (raised by the user plane's
`POST …/runtimes/{runtimeId}/refresh`) asks the daemon to probe every
provider CLI again and register the result, and the daemon coalesces such
hints and keeps a few seconds' floor between rounds.

The daemon executes discovery using the executable path already selected for
that exact runtime. Codex is invoked as `codex debug models --bundled`; Claude
uses the maintained safe alias catalog. Provider execution adapters are not
involved.

### Report a model-discovery result

```http
POST /api/v1/daemon/runtimes/{runtimeId}/model-discoveries/{requestId}/result
```

A completed result carries the bounded provider-native catalog:

```json
{
  "status": "completed",
  "models": [
    {
      "id": "gpt-5.6-sol",
      "label": "GPT-5.6-Sol",
      "default": true,
      "reasoning": {
        "defaultLevel": "medium",
        "levels": [{ "value": "medium", "label": "Medium" }]
      }
    }
  ]
}
```

A failed result has no models and uses the single typed safe error:

```json
{
  "status": "failed",
  "error": {
    "code": "discovery_failed",
    "message": "Runtime model discovery failed"
  }
}
```

The request must already be `running` and belong to the exact runtime path.
Unknown, expired, cross-runtime, and cross-company requests are non-disclosing
`404`s; a pending request is `409 model_discovery_not_running`. The first
terminal result wins. Repeating a terminal report returns `200` without
replacing it. The daemon retries an ambiguous report with a bounded backoff.
No path, stderr, environment value, credential, or token enters either result
shape.

### List companies

```http
GET /api/v1/daemon/companies
```

Returns all non-deleting companies for the authenticated user:

```json
{
  "items": [
    {
      "id": "497f6eca-6276-4993-bfeb-53cbbbba6f08",
      "name": "Acme",
      "slug": "acme"
    }
  ]
}
```

This route has no pagination parameters.

### Open the control socket

```http
GET /api/v1/daemon/ws
```

This route upgrades to the daemon control WebSocket. `X-Zenith-Team-Daemon-ID`
identifies the machine; `daemon_id` is its query fallback. `runtime_ids` is an
optional comma-separated runtime scope. The server keeps only the ids of
runtimes the signed-in user registered, from this machine, in one of their
companies, and silently drops the rest (a malformed id is still
`400 invalid_request`). Wakeup and pending-work frames reach the socket only
for the runtimes it kept, and `runs.transcript.append` accepts only runs on
them. `X-Client-Version` and `X-Client-Capabilities` advertise client
metadata. Authentication happens before the upgrade.

The socket carries heartbeat/control frames and correlated daemon RPCs.
Completed provider items use `runs.transcript.append`; the daemon sends one
item, waits for the post-commit receipt, and only then consumes the next
durable provider event. The RPC `requestId` is transport correlation, not
item identity.

## Claim and preparation

### Claim across runtimes

```http
POST /api/v1/daemon/runs/claim
```

```json
{
  "daemonId": "daemon-01",
  "runtimeIds": ["497f6eca-6276-4993-bfeb-53cbbbba6f08"],
  "maxRuns": 4
}
```

`daemonId` is required. `maxRuns: 0` returns an empty array; positive values are
capped at 32; negative values return `400 invalid_request`. Malformed, missing,
unauthorized, or other-machine runtime ids are skipped.

```text
{ "items": [<DaemonRun>] }
```

`items` is `[]` when there is nothing to claim — the stable success shape, so
the daemon decodes without branching on status. Each returned run is already
`dispatched` and carries its run credential. The database claim is atomic per
run; a mid-batch database failure is `500` with the standard error envelope —
a `200` means the claim attempt ran to completion. Runs claimed before such a
failure stay claimed and are re-served by the stale-claim reclaim, so the
daemon retries the claim rather than guessing what it holds. If a claim
response is lost, an unstarted dispatch becomes claimable again after its
server-side preparation fence lapses.

### Extend preparation

```http
POST /api/v1/daemon/runtimes/{runtimeId}/runs/{runId}/prepare-lease
```

This operation has no request body. It extends the server-side preparation
fence only when the run belongs to the runtime, has not started, and remains
`dispatched` or `waiting_local_directory`.

```json
{ "status": "ok" }
```

An unknown runtime, or one registered by another daemon, is
`404 runtime_not_found`; an unknown run, or a run that does not belong to
this runtime, is `404 run_not_found`. A successful extension is safe to
repeat while those predicates remain true.

### Resolve skill bundles

```http
POST /api/v1/daemon/runtimes/{runtimeId}/runs/{runId}/skill-bundles/resolve
```

```json
{
  "skills": [
    { "id": "review", "source": "company" }
  ]
}
```

`skills` must contain 1 to 32 `{id, source}` references. Each reference must
match a bundle in stored `context.agent.skills`, keyed by `(source,id)`.
Bundles are echoed from that frozen document, not fetched from a latest
library. `agent.skillRefs` alone is not sufficient. The Run must still be
`dispatched` or `waiting_local_directory`; that gate precedes body decoding.

```text
{ "items": [<opaque skill bundle>] }
```

An unknown runtime, or one registered by another daemon, is
`404 runtime_not_found`; an unknown reference is `404 skill_not_found`; a run
outside its preparation window is `409 run_not_preparing`.

Normal enqueue currently produces neither `agent.skills` nor `agent.skillRefs`.
The daemon's cache/materializer and MCP snapshot reader are consumer support,
not a shipped Agent Skill assignment flow. Normal MCP preparation passes no
custom snapshot directory; Task platform Skills are compiled into MCP and
work independently. The request does not accept a `hash` field.

An end-to-end producer, exact-reference hash checks, native/MCP parity and
local workspace provenance are Planned. Their future context and index fields
are not current public request fields.

### Recover orphaned runs

```http
POST /api/v1/daemon/runtimes/{runtimeId}/recover-orphans
```

This operation has no request body. It fails the runtime's in-flight runs after
a daemon restart and applies the normal retry policy.

```json
{ "orphaned": 2, "retried": 1 }
```

An unknown runtime, or one registered by another daemon, is
`404 runtime_not_found`. Repeating the operation is safe; runs already made
terminal are not recovered again.

## Provider execution stream

Provider adapters expose one ordered interface to the daemon, independent of
whether the underlying runtime speaks JSON-RPC, stream JSON, or another vendor
protocol:

| Event | Daemon behavior |
|---|---|
| `ItemDelta` | Forward only to the optional Desktop-local preview. It may be dropped or coalesced. |
| `ToolActivity` | Select the tool watchdog window and reconcile in-flight tool counts by stable item key. Never persist or broadcast it. |
| `CompletedItem` | Normalize/redact, append through `runs.transcript.append`, and wait for the post-commit receipt. |
| `SessionReady` | Persist the provider session only after its resume artifact is usable. |
| `Finished` | Observe exactly once after every earlier event; report terminal state using the acknowledged transcript proof. |

Provider-specific behavior is expressed by the events an adapter actually
emits. A provider with no partial-output feed emits no `ItemDelta`; a provider
without native tool lifecycle emits no `ToolActivity`; a resumable
provider emits `SessionReady` only after its pointer is usable. This keeps Run
execution extendable without importing vendor frame vocabulary or advertising
unused recovery modes. The current Claude and Codex adapters cannot
re-enumerate completed items after their process is lost, so daemon-crash
recovery fails the Run rather than inventing a transcript suffix. A future
history seam must reproduce the same stable item identities and normalized
content before it is used for recovery.

## Run state and reporting

### Read status

```http
GET /api/v1/daemon/runs/{runId}/status
```

```json
{ "status": "cancelled", "reason": "task_reassigned" }
```

`status` is the run's current status. `reason` is sent only for a `cancelled`
run: it is the cancel reason the server recorded, for example
`task_reassigned` or `user_cancelled`. It is absent for every other status and
for a cancel that recorded no reason. The response is not a run object.

`task_reassigned` means a confirmed handoff cancelled the run
([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)). The Task's
workspace generation has moved on, so the daemon marks the run's Task root
superseded — kept, never continued — instead of completed.

The daemon reads this route at three points in
`server/internal/daemon/run.go`. `client.go` decodes the body as `RunStatus`;
its `handedOff()` is true for `cancelled` with `task_reassigned`.

1. **Cancellation poll**, while the run executes: every 5 s, and at once after
   the control socket reconnects. A `completed`, `failed` or `cancelled`
   status, or `404 run_not_found`, stops the agent. Any other error does not.
2. **Handoff check**, when a Task Run in a managed Task worktree ends any way
   other than a clean completion. A Task Run that executes in place in a bound
   local directory has no Task root and skips this read. Once the cancel
   commits, the server refuses the run's next transcript append, and that
   refusal usually ends the run before the poll does. Unless the poll already
   saw the handoff, this read decides whether the root is superseded; if the
   read fails, the root is completed.
3. **Final check**, when the agent finished without an error, before the
   daemon reports its result. If the run already ended on the server, or is
   gone, the daemon drops the result and sends `cancel-ack`. With
   `task_reassigned` it first supersedes the Task root the finished run had
   just completed; only a Run in a managed Task worktree has one.

Like every run-scoped route, a run on a runtime that another daemon or another
member registered is `404 run_not_found`, the same as an unknown run (see
[Authentication and transport](#authentication-and-transport)).

### Append one completed item over the control socket

This is daemon WebSocket RPC method `runs.transcript.append`, not an HTTP
endpoint. Request:

```json
{
  "runId": "00000000-0000-0000-0000-0000000012d5",
  "itemKey": "assistant-12",
  "role": "agent",
  "parts": [
    { "type": "text", "text": "Done." }
  ]
}
```

The daemon does not submit `seq`, a row id, or a generic idempotency key. The
server normalizes and redacts the item, locks the **Conversation** row and then
the running Run assigned to that runtime, and allocates the next sequence from
the conversation's counter. Success is the post-commit receipt:

`itemKey` is 1..512 UTF-8 bytes; `parts` contains 1..128 exact variant objects;
call/tool names and ids are bounded to 512 bytes; and the canonical normalized
item is at most 2 MiB. A shape or bound violation returns RPC status `422`.

```json
{
  "id": "0197f5b0-7781-7a03-9c14-6d2e8f4b1a29",
  "runId": "00000000-0000-0000-0000-0000000012d5",
  "itemKey": "assistant-12",
  "seq": 44,
  "throughSeq": 44,
  "itemDigest": "67aa798d323d3147d69a964a2b6317d79d78a7147a4eaf4f551bc9a4ecf5251a",
  "committedAt": "2026-08-15T16:02:55.100000Z"
}
```

`(runId, seq)` is the authoritative order and catch-up cursor;
`(runId, itemKey)` is the item's natural replay identity. An exact replay
returns the existing item receipt, including its original id and sequence, and
reports the Run's latest head. Reusing an item key with different normalized
content returns RPC status `409`. The daemon waits for this receipt before
sending the next completed item, so PostgreSQL always owns a contiguous
acknowledged prefix and `Finished` cannot overtake it.

`itemDigest` is SHA-256 over canonical `{itemKey, role, parts}`. It is a
per-item value only: it is what distinguishes a harmless exact replay of one
item from an attempt to reuse its key for different content. There is no chain
digest over the transcript, and a daemon computes no digest of its own —
`throughSeq` alone names the committed prefix, because the server allocates
`seq` under the Conversation row lock and never rewrites a committed item.

After commit the server broadcasts the full structured item as `run.message_created` to
every client eligible for the Run's Conversation. That event is Zenith Team's
provider item-completed boundary, it is published before the receipt permits the
daemon to consume `Finished`, and for an agent text item it is the **only**
delivery of that text — nothing further is emitted when the Run completes.

### Start a run

```http
POST /api/v1/daemon/runs/{runId}/start
```

```json
{
  "runtimeId": "497f6eca-6276-4993-bfeb-53cbbbba6f08"
}
```

`runtimeId` is required: it names the runtime the daemon claimed this run for,
and the transition is fenced on it — the `UPDATE` matches `runs.runtime_id`, so
a daemon that lost the claim cannot start the run under its old owner. The
server does not read the runtime off the row it is about to update; that would
compare the value to itself and fence nothing. A missing or malformed
`runtimeId` returns `422 validation_failed`.

The operation atomically moves a `dispatched` or `waiting_local_directory` run
to `running` and returns the bare `DaemonRun`. Any other state — including a
run reassigned to a different runtime since the claim — returns
`409 run_not_startable`, usually a lost race with a cancel or a sweeper. A
repeated start therefore requires reading the run status to distinguish a lost
success response from a rejected transition.

### Wait for a local directory

```http
POST /api/v1/daemon/runs/{runId}/wait-local-directory
```

This operation has no request body. It moves a `dispatched` run to
`waiting_local_directory`, stores the fixed public reason
`local_directory_busy`, and returns the bare `DaemonRun`. The request carries
no contested path; the returned frozen context may still contain a baseline
local-directory Resource's `localPath`. Any other state returns
`409 run_wait_unavailable` — re-read the run rather than retrying unchanged.

The same operation also parks direct Runs waiting for the managed Company
`main` directory. Its name does not imply that a user selected an
external folder, and the planned removal of direct-folder execution does not
by itself remove this status or endpoint.

### Local Desktop preview

There is no daemon-to-server delta endpoint. Item-correlated partial output
stays on the executing machine and, only when launched by Desktop, is exposed
as authenticated NDJSON from the daemon's loopback `/local-preview` handler.
Its capability is rotated at daemon startup and stored in a mode-`0600`
`local-preview.json` file read by Electron main. Shutdown leaves this
reconstructible file in place so an exiting daemon cannot remove a replacement
daemon's configuration. Startup atomically replaces it; a previous daemon's
capability does not authorize requests to its replacement.

`run.preview.delta` frames carry `runId`, `conversationId`, `itemKey`,
`channel`, `operation`, and `content`. `runId` is the Run UUID string, the
same id the server's planes carry; the frame is disposable scratch the desktop
correlates by string key and validates as bounded text. The channel is `text`
or `reasoning`;
Electron maps reasoning to the UI's thinking presentation. These frames are
deliberately droppable: bounded queues may coalesce adjacent appends, replace
pending values, or discard old frames under load. A payload-free
`run.preview.reset` clears all local scratch when a stream is established or
the Desktop bridge disconnects. The browser never receives either frame. The
matching `run.message_created` from the server is the post-commit authority and reaches every
eligible connected client. See [realtime.md](realtime.md#local-desktop-preview).

### Report usage

```http
POST /api/v1/daemon/runs/{runId}/usage
```

```json
{
  "usage": [
    {
      "provider": "claude",
      "model": "claude-opus-5",
      "usageBasis": "per_run",
      "inputTokens": 18422,
      "outputTokens": 3110,
      "cacheReadTokens": 210400,
      "cacheWriteTokens": 12008,
      "costUsdTicks": 41200000,
      "rawJson": {
        "input_tokens": 18422,
        "output_tokens": 3110
      }
    }
  ]
}
```

Each item is the final immutable fact for one `(runId, provider, model)`.
`provider` is validated **exactly** against the supported provider set,
case-sensitively — no lowercasing, trimming, or substitution, the same rule
registration applies; an unknown provider is `422`. The model must be nonblank
and at most 255 bytes; token and cost counters must be non-negative JSON
integers.

`usageBasis` says what the counters measure. `per_run` — this run's own
consumption, the default when omitted. `session_cumulative` — the provider
reports totals for the whole resumed session, so the fact supersedes rather
than adds to the session's earlier runs; the codex path reports on this basis.

`costUsdTicks` is optional and represents 1e-10 USD units. Omission or `null`
means the provider did not report a price; an explicit `0` is an exact
reported zero-cost fact. `rawJson` must be an object. The server retains only
approved numeric evidence and limits its canonical encoded form to 16 KiB.

The first fact inserts. An exact replay succeeds without mutation. Different
content for an existing identity returns `409 run_usage_conflict` and never
overwrites the stored fact. The whole submitted batch is transactional.
Success, including an exact replay or an empty batch, returns
`{ "status": "ok" }`.

### Pin a session

```http
POST /api/v1/daemon/runs/{runId}/session
```

```json
{ "sessionId": "sess_01H8XQ4M2Z9V6T0KJ7B3N5RGWD" }
```

`sessionId` is the only accepted request field. A nonempty value replaces the
stored provider session; an empty value leaves it unchanged. The endpoint has
no run-status precondition and returns `{ "status": "ok" }`.

### Complete a run

```http
POST /api/v1/daemon/runs/{runId}/complete
```

```json
{
  "throughSeq": 44,
  "finalItemKey": "assistant-12",
  "sessionId": "sess_01H8XQ4M2Z9V6T0KJ7B3N5RGWD",
  "sessionRolloutMissing": false,
  "retiredSessionId": ""
}
```

`throughSeq` is required, and it is the conversation `seq` of the newest item
the daemon saw acknowledged for this Run — not a count of its items. Inside one
transaction the server locks the Conversation and the Run, verifies that the
sequence exactly matches PostgreSQL, and — when `finalItemKey` is present —
requires it to name this
Run's newest committed `agent_text` item. It then advances the queue and returns
the bare `DaemonRun`.

**Completion copies nothing.** That item is already in the Conversation: it was
written there when this daemon appended it and broadcast then as `run.message_created`.
`finalItemKey` only says which item is the answer. Omit it when the successful
turn produced no final agent text, and the Run completes with no `agent_text`
item at all — the absence is the record. The callback still does not accept or
trust provider output text.

**Nominate by the same test the server stored the row with.** An appended item
became `agent_text` exactly when its role was `agent` and it carried a
non-empty `text` part — agent text taking precedence over any reasoning or tool
part in the same item. A daemon that picks its final item by a different rule
names something the server did not store as `agent_text`, and the completion is
refused as `run_terminal_item_invalid` with the answer already durable in the
Conversation.

An exact replay against an already-completed Run returns the completed Run.
If cancellation already won, a valid completion callback returns `200` with
the bare `DaemonRun` and preserves cancellation without promoting output or
recording another Activity. A
stale head returns `409 run_transcript_head_mismatch`; a `finalItemKey` naming
no committed `agent_text` item of this Run, or one a later `agent_text` item
superseded, returns `409 run_terminal_item_invalid`. Neither conflict performs
the terminal transition. The daemon may retry an ambiguous `5xx` with the same
proof — but never a `409 run_transcript_head_mismatch`, which it must
[reconcile](#reconciling-a-refused-terminal-transition).

### Fail a run

```http
POST /api/v1/daemon/runs/{runId}/fail
```

```json
{
  "throughSeq": 44,
  "error": "provider disconnected",
  "sessionId": "sess_01H8XQ4M2Z9V6T0KJ7B3N5RGWD",
  "failureReason": "provider_network",
  "sessionRolloutMissing": false,
  "retiredSessionId": ""
}
```

`throughSeq` is required; failure does not require a final agent-text item. The server verifies the exact committed head before it
classifies a missing failure reason, may create a retry child according to the
failure policy, revokes the run credential, and returns the bare `DaemonRun`.
An exact replay returns the failed Run. If cancellation already won, a valid
failure callback returns `200` with the bare `DaemonRun`; cancellation stays
final, with no retry child or additional Activity. A stale proof is
`409 run_transcript_head_mismatch` and leaves the current Run state unchanged.

Whatever the agent already said survives the failure. Each `agent_text` item was
committed when it was appended, so failing a Run never costs the user an answer
they already received — it only stops the Run adding more.

### Reconciling a refused terminal transition

`409 run_transcript_head_mismatch` means the proof does not name the Run's
committed prefix — typically because the daemon sent an item whose receipt it
never saw, so the server is one item ahead of what the daemon believes. **The
refusal carries the server's own head**, flat on the standard error envelope:

```json
{
  "detail": "run 4821 proved through seq 42, server is at seq 44",
  "code": "run_transcript_head_mismatch",
  "requestId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "runId": "00000000-0000-0000-0000-0000000012d5",
  "throughSeq": 44
}
```

Reconciling is **required**, not optional. Re-sending the same proof is refused
again, and the Run stays non-terminal until a sweeper kills it — with the
agent's answer already durable in the Conversation and nothing marking the turn
as over. On receiving this body a daemon must:

1. **Validate it.** Ignore a body that does not name this Run, or whose
   `throughSeq` is not a value a head could hold. Never invent a head it was not
   told.
2. **Replay the unacknowledged item first,** if it is holding one. An exact
   replay through the `(runId, itemKey)` fence returns the existing receipt
   whether or not the first attempt committed, and that receipt is preferable to
   the refusal's head because it also identifies the item — which a completion
   needs in order to nominate its `finalItemKey`. Adopting the head alone would
   let the transition pass while still nominating a stale answer, which the
   server refuses separately as `run_terminal_item_invalid`. One attempt only:
   either the item is already committed and the replay says so immediately, or
   the delivery lane is still down and waiting again will not change that — fall
   through to step 3.
3. **Otherwise adopt `throughSeq` verbatim** as the head.
4. **Re-send the terminal transition** with the rebuilt proof. Bound the loop:
   after a small number of reconciliations, fail loudly rather than spinning.

Every other terminal `409` carries no head, because there is nothing to
reconcile against; re-read the transcript instead.

### Acknowledge cancellation

```http
POST /api/v1/daemon/runs/{runId}/cancel-ack
```

This operation has no request body. It tells the server that the daemon has
stopped the process and flushed its transcript, then wakes the runtime for more
work. It does not require a particular run state and returns
`{ "status": "ok" }`. Repeating it is safe.

## Retry summary

| Operation | Retry behavior |
|---|---|
| Register | Stored state is upserted by machine/provider key in one transaction — a request never partially lands — but broadcasts can repeat. |
| Deregister and heartbeat | Safe to repeat; a deregister `200` means the batch was applied, and a failed write is `500`. |
| Model discovery result | The first terminal result wins; exact and conflicting terminal duplicates are both acknowledged without replacement. |
| Claim | Claiming is atomic, but a response is not replayed verbatim. An unstarted lost-response claim is recovered after its preparation fence lapses. |
| Extend preparation | Safe while the run remains in the accepted preparation state. |
| Start and wait | State transitions are atomic but a repeated call is rejected; read status after an ambiguous response. |
| Transcript append RPC | Exact `(runId, itemKey, normalized content)` replay returns the existing item; conflicting content returns `409`. The server, not the daemon, assigns `seq`. |
| Usage | Exact fact replay succeeds; different content returns `409 run_usage_conflict`. |
| Complete and fail | Exact proof replay is safe. A stale sequence returns `409 run_transcript_head_mismatch` with the server's head, cannot terminalize the Run, and must be [reconciled](#reconciling-a-refused-terminal-transition) — retrying the same proof strands the Run. |
| Recover orphans and cancel acknowledgement | Safe to repeat. |


### Opt-in Zenith Studio

Interactive Runs carry the Agent's frozen `capabilities.enabledTools` selection.
Only `zenith-studio` is currently supported; absent/empty means disabled for
both existing and new Agents, including the CEO. Rebinding an Agent preserves
this selection. Schedules ignore it. The daemon prepares reviewed npm
releases in the background, supplies a pinned Run-local launcher and the verified Studio Skill
snapshot, and uses the Company account connected from Desktop on the Runtime computer.
Node.js 22+ and npm are required. Studio's HyperFrames renderer remains an
executable dependency, not another Agent capability. Credentials and local
installation caches are not product state and never enter PostgreSQL.

The normal Run PATH shadows an ambient Studio CLI when disabled, and provider
skill settings hide its personal skill and installer. These controls govern
Team-provided capabilities; they are not an OS sandbox against arbitrary
shell access. Operator login stays outside Runs. Studio Outputs use the
workspace-file contract behind `present_output` without new canvas fields.

Desktop controls call the token-protected loopback `POST /v1/tools/operate`
with `{schemaVersion: 1, companyId, runtimeId, provider, toolKey, action}`.
Actions are `status`, `repair`, `connect`, and `cancel`; the renderer only
supports status and repair. Operations recheck the exact local Runtime and
the caller's live membership and Runtime ownership on the server. Browser sign-in runs separately from setup and status jobs.
Company credentials live in the CLI-owned `.tools/accounts/<companyId>/zenith-studio`
home; Runs pin that home and API origin and cannot initiate account changes.

Before a browser sign-in and before it publishes states, the daemon checks
the Runtime and its person's bindings on this plane:

```http
GET /api/v1/daemon/runtimes/{runtimeId}/agent-bindings
```

```json
{
  "runtime": {
    "id": "0198bb00-0000-7000-8000-000000000001",
    "companyId": "0198bb00-0000-7000-8000-000000000002",
    "provider": "codex",
    "daemonId": "0198bb00-0000-7000-8000-000000000003"
  },
  "items": [
    {
      "id": "0198bb00-0000-7000-8000-000000000004",
      "agentId": "0198bb00-0000-7000-8000-000000000005",
      "runtime": { "id": "0198bb00-0000-7000-8000-000000000001", "name": "Codex (MacBook)", "status": "online", "provider": "codex" },
      "createdAt": "2026-10-05T09:00:00Z"
    }
  ]
}
```

The daemon header, Runtime ownership and active Company membership are
required, as on every runtime-scoped route; anything else is
`404 runtime_not_found` or `404 company_not_found`. `items` are the person's
own bindings on the Runtime, as `GET /companies/{companyId}/agent-bindings?runtime_id=`
lists them; the daemon's own session cannot read that member route.

The daemon reports sanitized installation and authentication states using
`POST /api/v1/daemon/runtimes/{runtimeId}/tools` with `{items: [...]}`.
The daemon header, active Company membership, and Runtime ownership are
required. Observations replace `runtimes.metadata.tools` under the canonical
Company/membership and Runtime locks, then publish a roster event. Only
key, version, state, authState, reasonCode, and observedAt are synchronized;
credentials, CLI output, and local paths never enter this projection.

### Conversation-owned Studio Projects

`POST /api/v1/daemon/runtimes/{runtimeId}/runs/{runId}/studio-project` accepts
`get`, `reserve`, or `bind` with a live `runToken`. The user session, daemon,
Runtime, Company membership, running Run and frozen `enabledTools` selection
must all match. Only the first reservation permits remote creation. Bindings
are immutable and unique across Conversations; continuations reuse the Project.
The daemon loopback bridge forwards the Run proof while keeping the user's
session private. The managed CLI recovers uncertain creation by the reservation
marker in the remote Project description; it never adopts a Project by title.
