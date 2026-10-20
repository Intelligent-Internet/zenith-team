# Helia for Zenith Team

Separate, room-scoped GPT-Live process for Huddles Phase 4. It uses
`livekit-agents[openai]==1.8.3`, `gpt-live-1` with the invited Agent's voice, and Responses delegation
to `gpt-5.6-luna`. `uv.lock` pins the dependency graph. No daemon, Run, Connector,
web-reading tool, or source Conversation history is attached to a voice session.

## LiveKit Cloud (managed media and voice worker)

`livekit.toml` identifies the Cloud deployment; the dispatch name remains
`zenith-helia` for every invited persona. Do not run `make voice-start` or a local
`livekit-server` alongside this setup. Keep the Go API and frontend running.

For development with a local API, run this in one visible terminal/tmux window:

```sh
ngrok http http://127.0.0.1:8788 --inspect=false --traffic-policy-file docker/ngrok.voice.yaml
```

The policy exposes only the authenticated voice-worker routes and signed LiveKit
webhook; other API routes are denied. Inspection is disabled so ngrok's local
request inspector does not retain credentials or transcript bodies. The tunnel
and local API must stay running; this is not a production API deployment.

Configure `server/.env` with the Cloud project's `LIVEKIT_URL`,
`LIVEKIT_API_KEY`, and `LIVEKIT_API_SECRET`, then restart the API.
In LiveKit Cloud → Settings → Webhooks, configure
`https://<tunnel-host>/api/v1/internal/livekit/webhook` using the same signing API
key as the Go API. A different signing key causes admission to fail.

The ignored, mode-0600 `voice-agent/.env.cloud` contains only:

```dotenv
OPENAI_API_KEY=...
HUDDLE_AI_WORKLOAD_SECRET=... # must match the API
ZENITH_API_URL=https://<tunnel-host>
```

LiveKit Cloud injects its own `LIVEKIT_URL`, `LIVEKIT_API_KEY`, and
`LIVEKIT_API_SECRET`; do not copy local development credentials into Cloud.
Only explicitly allowlisted production files enter the Docker build context.

Run from `voice-agent/`:

```sh
lk agent status --project your-livekit-project
lk agent logs --project your-livekit-project
lk agent deploy --project your-livekit-project --secrets-file .env.cloud
```

Stopping the logs command does **not** stop the Cloud worker. Local worker
`make voice-status` / `make voice-stop` commands do not manage Cloud deployments.
If the tunnel URL changes, update both the webhook URL and `ZENITH_API_URL` in
`.env.cloud`, then run `lk agent update-secrets --project your-livekit-project
--secrets-file .env.cloud`. This restarts the Cloud worker. For production, replace
the tunnel with a stable public HTTPS API URL before retiring the local API.

Verify with a new Huddle: join, invite a Voice-enabled Agent, test a Company
lookup, ask it to leave, and expand its transcript. A `Running` deployment alone
does not verify model access, signed participant admission, or tool calls.

## Start locally (alternative)

The whole stack runs on one machine with LiveKit in Docker; follow
[`docs/huddles-local.md`](../docs/huddles-local.md). In short:

```sh
echo "HUDDLES_ENABLED='true'" >> docker/dev.local.env   # once
make infra-up                      # includes LiveKit; then `make server` and `make frontend`
make voice-setup                   # creates voice-agent/.env; set OPENAI_API_KEY in it
make voice-doctor                  # checks LiveKit, its keys, the API and the shared secret
make voice-start
```

`voice-agent/.env.example` matches `docker/dev.env` and the local LiveKit
configs, so only the OpenAI key needs filling in. To point the worker at another
deployment, set `LIVEKIT_URL`/`LIVEKIT_API_KEY`/`LIVEKIT_API_SECRET`,
`ZENITH_API_URL` and the API's `HUDDLE_AI_WORKLOAD_SECRET` instead
(through your secret manager or shell; do not commit them). Keep development
keys and loopback media addresses out of staging and production.

### Local worker ownership

Run these commands from the repository root (macOS/Linux):

```sh
make voice-start   # foreground; keep this terminal/tmux pane for worker logs
make voice-status  # run in another terminal; no model call or secrets needed
make voice-stop    # request graceful shutdown of this checkout's local worker
```

Start never backgrounds a process. Status reports the owner PID, startup Git
revision (including tracked dirty changes), terminal and tmux pane ID, and the
latest SDK worker/job snapshot with room and attempt IDs. `tmux select-pane -t
%ID` selects that pane in your current tmux session. A missing/stale snapshot is
explicit; HTTP health and a previously registered worker ID do not prove that
a model session or the LiveKit connection is currently healthy.

A per-user/per-checkout OS lock prevents duplicate local workers, including
direct `uv run --env-file .env python agent.py dev` launches. Port 8081 is checked
before starting; an existing listener is never killed. On the first upgrade,
stop your old worker with Ctrl+C in its original terminal, wait for it to exit,
then use `make voice-start`. Older workers have no control socket and cannot be
stopped by the new command; status does not pretend they are absent. Use
`lsof -nP -iTCP:8081 -sTCP:LISTEN` to identify an unmanaged listener.

Stop uses a private Unix socket to ask the owning process to signal itself once;
it never signals a PID read from a stale file or kills by name/port. Repeated
stop requests do not force-kill. The SDK's local dev shutdown closes sessions
and allows the existing final-usage cleanup to run; the command waits up to
45 seconds and reports a timeout without escalating. Avoid a second Ctrl+C
during cleanup (the SDK interprets that as force exit). Local stop interrupts
AI sessions, not human calls; the backend may recover an outstanding invitation
after restart. Remove the Agent in the call to revoke that invitation.

Only ephemeral control files live under the private `/tmp/zenith-voice-*`
directory. No tokens, speech, prompts or product state are saved there.
Production `agent.py start` retains its existing orchestration/drain behavior;
the local commands intentionally do not manage production workers.

Choose an existing Agent, or create one from **Agents**.
Choose its runtime and manager as for Susan, Eli or Lyra. When the server runs
voice Agents (`HUDDLE_AI_ENABLED=true`), Voice is on for every Agent, including
existing ones, unless it was turned off in **Agent → Voice**; with the flag off,
the Voice tab and Invite Agent are hidden. Shared Instructions supply the persona; the Voice tab sets the speaking
voice and participation style. Agents greet once after joining; this is not a
preference, and reconnects do not replay it. Enabling Voice allows these instructions
to be used in calls visible to other participants. Read-only Company context is
included in invitations from the Huddle UI. Runtime tools, skills,
Company Memory and the Agent's execution model are **not** attached to voice.

Configure the Company's UTC monthly USD allowance in **Invite Agent → AI budget**
(as a Company administrator), or use the documented `huddle-budget` API. Zero
is the default and prevents model startup. Human calls and chat continue.
Connect the LiveKit webhook to the existing signed
`/api/v1/internal/livekit/webhook` endpoint. A token reservation alone does not
start Helia. Calls start without Helia, even when the Company has a budget.
After a verified human joins, the starter or a Company administrator can choose
**Invite Agent**, select a Voice-enabled Agent and confirm the invitation. That explicit invitation records the chat boundary and AI
intent; the controller dispatches the named `zenith-helia` process. Messages
before the invitation are not included. **Remove [Agent]** revokes participation;
inviting again establishes a new chat boundary without replenishing the budget.

## Authority and recovery

- Dispatch metadata is only a UUID. A workload-secret claim atomically acquires
  one attempt; duplicate dispatches cannot open a second model.
- Heartbeats renew a 30-second lease. The process renews every five seconds,
  checks again at the actual WebSocket handshake, and stops early enough to
  close before lease expiry. A separate watchdog terminates a stalled job
  process. SDK reconnection is blocked; the database controller owns recovery.
- Recovery waits past the prior lease plus five seconds, uses bounded backoff,
  and stops after five failures. It never starts in an empty room. An existing
  attempt may remain during the human call's 10-second empty grace.
- The starter or an authorized Company administrator can invite/remove Helia.
  Disable revokes feed/renewal immediately; dispatch removal and the local
  lease watchdog stop publication. Re-enable retains the original room budget
  deadline, establishes a new chat boundary, and permits a new greeting.
- Each invitation freezes the selected Agent's name, appearance, instructions,
  revision and Voice settings. Edits apply at the next invitation. Claim and each
  heartbeat check that the Agent is still active in the same Company with Voice
  enabled; revocation stops the process within its lease. Legacy unbound Helia
  invitations must be replaced with an explicit Agent invitation after upgrading.
- The mixer subscribes only to admitted humans' microphone tracks. It removes
  muted, departed, and no-longer-admitted inputs. Camera, screen, and AI tracks
  never enter the voice model. Input stays live while Helia speaks; GPT-Live
  and the SDK own native turn taking and interruption.
- Typed delivery checkpoints before `response.item.create`, waits for running
  Responses/tool work, and follows with `response.create` in the same session.
  Pending input survives as exact text/author/id/seq context after recovery;
  uncertain requests are not automatically repeated. Delivery is not evidence
  that a question was answered. URLs remain strings; there is no web tool.

## Accounting

Money uses integer 1e-10 USD ticks. The room reserves remaining voice duration
plus $1 for reasoning. Partial funding shortens the visible AI deadline; human
media still follows its original deadline. Rates checked 2026-09-25 are
$0.05/minute for GPT-Live and $0.20/$0.02/$1.20 per million input/cached/output
Luna tokens. Voice rounds upward to whole ticks, not whole minutes. Update the
Go pricing configuration and the process's reasoning guard together if rates
or model identifiers change.

Each provider session reports cumulative seconds and cumulative backend tokens;
replays do not add them twice. Session time includes silence. The process caps
backend output at 2,048 tokens and stops with $0.25 reasoning headroom for
in-flight work. Provider-reported overruns are retained, never clamped away.
Reservations are conservative spending estimates, not provider billing limits;
configure a separate OpenAI project spend limit for the pilot.

`GET H/ai` reports wall-clock room seconds, summed human connection seconds,
and provider-reported model seconds. Usage receipts remain isolated by company,
huddle, attempt, and provider session. A missing final `session.closed` report
retains the uncertain reservation. Cleanly reconciled sessions release unused
funds when the room ends. Reconnect/re-enable never creates a fresh allowance.

The budget UI separates recorded usage, additional held funds, and available
allowance. Recorded usage is not a provider invoice. A new room needs the $1
reasoning reserve plus at least one second of voice allowance; a positive
remaining balance below that minimum cannot start Helia. Unavailable status
distinguishes insufficient/expired budget, a disabled server service, and other
session failures. Never clear a missing final receipt by marking usage zero.

On shutdown the worker closes the provider, submits accounting, then tears down
media. Media cleanup failures do not skip the receipt. The process shutdown
timeout is 35 seconds: the pinned SDK may wait 15 seconds before cancelling the
entrypoint, whose finally block still needs time to report usage. Forced kills
or API failures can still leave an uncertain hold requiring reconciliation.

`transcript.py` checkpoints only the existing live model's input/output audio
transcript events every two seconds. It opens no STT or additional model session.
Each invitation leaves one expandable transcript card in Huddle chat when it ends;
recoveries share the invitation and mark an interruption. The Agent flushes before
self-leave. Removal, call end, or forced shutdown can omit the last words; the card
discloses this. Human speech is labelled Participants (mixed audio), and generated
Agent speech is not proof that everyone heard it. Text is capped at 48 KiB JSON per
invitation and the local queue at 64 KiB; either limit produces a partial-transcript
notice. No raw audio is retained. Captions, full-meeting STT, summaries and exported
meeting-note files remain future work. Apply migration 77 and restart API and voice
agent together; existing running jobs must be reinvited to enable capture.

Local transcript check (use the existing terminals; do not start duplicate workers):

1. Stop API and voice worker with Ctrl+C, then restart `make server-local` and
   `make voice-start` from the repository root. API startup applies migration 77.
2. Start a Huddle, invite a Voice-enabled Agent, speak briefly, then ask it to leave.
   Open Chat and expand **Transcript · Agent name**. There should be one card.
3. Invite again, then use Remove Agent. Expect another card, not an update to the
   first. A silent visit says no speech was captured. A recoverable connection error
   within one invitation keeps a single card with a gap marker.
4. Reopen the Huddle chat after leaving to check history. A Company member who
   never joined must not be able to read it. Terminal budget/permission failure
   finalizes capture; invite again after fixing the cause to start a fresh visit.

The automated voice suite uses local protocol peers/fakes and does not call paid
models. A real Huddle test still incurs the existing voice session's normal usage.

## Container and deployment

```sh
docker build -t zenith-helia:0.1.0 voice-agent
```

Run the build from the repository root. CI tests and builds the image; it does
not deploy. `deploy/voice-agent/deployment.yaml` supplies a Kubernetes deployment
with 2 CPU/4 GiB, a two-room process load limit, health checks, non-root execution,
and a 3,935-second termination grace. Its replicas default to zero. Provision
its `zenith-helia` Secret, replace the image with a registry digest, and configure
the API flag and Company budget before scaling up.

SIGTERM drains new job admission and allows 65 minutes for existing calls.
LiveKit's health endpoint is `/` on port 8081. It reports process/provider
health; LiveKit registration owns admission during drain. Verify that your
orchestrator/node eviction policy actually honors this grace. If your platform
forces a shorter shutdown, expect a visible interruption and lease recovery.

## Pilot acceptance (Phase 5)

Before enabling the pilot, use a GPT-Live-enabled OpenAI project and two API
replicas to verify:

1. Two independent rooms; 2/4/8 real people; mute/unmute/leave/rejoin; no AI echo
   or camera/screen audio entering the mixer.
2. Continuous multilingual listening/speaking, yielding, follow-ups, useful
   initiative, and rejected advice; one greeting across recovery.
3. A URL pasted during backend work, exact-value retention, duplicate delivery,
   and an interrupted request restored without a repeated acknowledgment.
4. Kill the job process, interrupt API connectivity, repeat dispatch delivery,
   disable AI during output, revoke membership, and end an occupied room.
   Confirm media/chat continue through AI failure and no stale model reopens.
5. Reconcile vendor seconds/tokens against room receipts, including silence,
   partial budgets, zero budget, month rollover, and crashes without final usage.
6. SIGTERM the deployment and verify drain/lease recovery on the actual host.

Synthetic mixer tests exercise summed frames for 2/4/8 inputs. They do not
establish real-call quality, latency, or deployment drain acceptance.

## Recovery hardening — 2026-09-28

The chat bridge waits as soon as Responses delegation is announced, including
the interval before `response.created`. Failed, cancelled, and incomplete
responses release their abandoned tool calls so queued chat can continue.
A completed tool-call response alone does not mark a typed request complete.
Partial token usage is accounted once per response receipt; missing usage or
receipt identity retains an uncertain reservation.

Recovery keeps the reasoning allowance returned by Claim after prior spending
has been deducted. Heartbeat renewal does not replenish it. Microphone track
replacement closes the old input even when the publication SID is unchanged;
revocation removes the replacement as well. Mixer failure stops the voice
session and releases its resources so the controller can recover.

Twenty local tests cover these paths, including rejected claims and authority
lost after joining but before model startup. These tests use local fakes or a
local WebSocket peer, not a live OpenAI project. Provider credentials and real
multi-person calls are still required for Phase 5 acceptance.

Sources: [LiveKit GPT-Live](https://docs.livekit.io/agents/models/realtime/plugins/gpt-live/),
[typed input](https://developers.openai.com/api/docs/guides/live-delegation),
[GPT-Live pricing](https://developers.openai.com/api/docs/pricing),
[Luna pricing](https://developers.openai.com/api/docs/models/gpt-5.6-luna).

## Read-only Company context

After migration 72, a moderator can select **Allow shared Company
context** when inviting an Agent. New invitations default on; existing
invitations keep their frozen scope. This read-only scope does not require Planning. Remove and invite
again to change scope; changing a checkbox does not elevate a live session.

Migration 73 records a bounded session stop reason for the unavailable status.
The worker logs `Voice session starting`, `Voice session ready`, and
`Voice session stopping` with the attempt ID and first stop reason. Recoverable
provider errors do not close the session; transient control API failures retry
only within the current authorization lease. Revocation and lease expiry still
stop processing. A finished session does not mean the invitation worker exited.

The four read tools are `get_company_context`, `list_projects`,
`search_tasks`, and `get_task`. They use the attempt capability, never the workload
secret or a fabricated Run token. The Go API derives Company scope and rechecks
the grant, Agent, audience, lease and budget. Only shared Company profile,
active projects and Task-conversation projections are exposed. Private
conversations, execution transcripts, workspace memory, credentials and Task mutations are excluded.
Phase C adds the two receipt-bound progress/result reads described below; only a
completed submitted Run's verified final answer can be shared, not its transcript.

Responses delegation and the existing reasoning model are unchanged. There are
at most three consecutive tool steps per turn, five API reads per ten seconds
and 120 per attempt. Results contain at most 20 items with explicit continuation
and truncation fields. A lookup times out after four seconds and is canceled on
session shutdown. Lookup errors are returned as facts-unavailable, not fatal
Agent errors. The API keeps only the latest bounded activity/source projection,
not a tool transcript; no prompt, query or full result is logged by these handlers.

For a local smoke test, invite with context enabled and ask for Company context,
projects and a task by key. Compare with the product UI. Verify context-disabled
invites refuse lookups, removal stops access, and revoking Company membership ends
the context-bearing AI session without ending the human call. Disabling Planning
must not prevent invitations or lookups. Real-call testing
uses the configured provider budget and must be run separately from offline tests.

## Work proposals — Phases A/B

Migration 74 adds `propose_task_work` when shared context is enabled. Ask the
invited Agent to prepare work with a Project and desired outcome, then open
**Work proposals** beside Chat. A moderator can edit the title/brief, reject,
or choose **Submit proposal**. Saving a draft creates no Task or Run. No
runtime, private memory or additional skills are enabled by proposing work.

### Submitted work — Phase C

With shared Company context enabled, `get_work_status` discovers submitted
proposals from the current call; an optional `proposalId` narrows the read.
`get_work_result` requires that proposal ID and returns up to 2,000 characters
of the selected completed Run's verified final answer. These reads follow the
submission receipt, automatic retries and explicit question/approval
continuations, never a later unrelated Task Run. No runtime identity, raw
execution error, reasoning or tool transcript is shared.

Ask “What is the status of the work we submitted?” and then “Read its result.”
Compare the answer with the Task. Offline queues must not be called running;
a completed Run must not be called an accepted/done Task. Missing or truncated
results are stated explicitly. Questions and approvals still require human
review in the Task. Tool reads happen on request, not through model-driven polling.
Leaving the call does not cancel submitted work. Restart the existing voice
process and invite again to load new tool definitions; do not start a second
worker alongside the first.

### Work handoff acceptance suite

The offline suite covers the real SDK tool bridge plus PostgreSQL-backed
proposal, submission and progress services. It does not simulate microphone
quality or execute a paid provider Run:

```sh
make voice-gate
cd server
go run ./tests/testpostgres go test -race -tags=integration ./internal/task ./internal/conversation ./internal/handler -run 'Test(SubmitHuddle|Huddle)' -count=1
```

Assertions include concurrent confirmation yielding one Task/Run, transaction
rollback without a personal binding, offline-runtime queue visibility, stale
review/foreign-Company rejection, failed execution, question/approval
continuations, verified result-only reads and leaving without canceling work.

For human acceptance, invite a Voice-enabled Agent with shared context, ask it
to propose work, review and confirm in **Work proposals**, then compare its
status/result with **Open Task**. Repeat with the daemon offline, reconnect it,
and leave the call while the Run is active. The Task must persist and its result
remain available outside the call. A completed Run is not automatically an
accepted/done Task. This real-call check consumes your configured provider
budget and is separate from the offline suite.

### Proactive work notices

Migration 76 adds durable at-most-once notification checkpoints. With shared
context enabled, the regular five-second authority heartbeat can offer short
notices for submitted work that needs human input or has a verified result.
No result text, question body, approval details or private transcript is sent in
these notices. Queued/running transitions are not spoken. A newer unrelated Task
execution suppresses old proactive notices; on-demand receipt reads still work.

The adapter waits for two seconds of observed conversational quiet, yields to
typed chat and in-flight tools, and limits announcements to one per 30 seconds.
An admitted-microphone amplitude check also defers notices before provider
transcription arrives; background noise may conservatively defer them too. It
does not store samples or replace the provider's turn detection.
It claims the current notice through the same live capability and rechecks
quiet/shutdown before speech. Speech remains interruptible and requests no tools.
The Agent announces availability, never claims the Task was accepted/done or
approves a request. Review stays in **Work proposals → Open Task**.

Each proposal/Run/status is attempted at most once across recovery and fresh
invitations in the same call. PostgreSQL records the claim before model output;
a lost response, interruption or crash may therefore drop a notification instead
of repeating it. This is not exactly-once audible delivery. There are at most
128 recorded keys per proposal; reaching the cap stops proactive notices only.
On-demand status/result access and the durable Task remain available.

Offline tests cover quiet-time and cooldown gates, speaking/shutdown during a
claim, duplicate/concurrent claims, stale work, recovery, missing shared-context
permission and removal. Human acceptance still needs a real call: finish work
while someone speaks, wait for a pause, interrupt the short notification, and
reinvite the Agent to confirm it does not repeat. Provider speech timing and
multilingual wording are not established by the offline tests.

Repeated identical input within an invitation returns the same draft, even after
voice recovery or human edits. At most 20 drafts are stored per call. Pending
drafts become non-editable after one hour or call end, but survive Agent departure;
they are not silently deleted. A timeout can mean the draft was saved but the
response was lost: check the card before retrying, using the same input.

Local acceptance: ask Susan (or any invited Agent) to propose a task, verify the
card names that Agent, edit and reload, then reject. Check that the Task/Run
lists have not changed. Try a non-moderator, a removed invitation and a stale
edit from another window. Do not use spoken agreement as execution authority.

Migration 75 adds the human submission receipt. Submit opens a review with an
explicit milestone for a new Task. **Confirm and create Task** assigns it to you
with the proposing Agent and saves it in Backlog without starting a Run. No
runtime binding is needed to confirm. Existing Task proposals require a reviewed
Task revision, yourself as assignee and the same Agent; confirmation does not
change the Task or start execution. Exact retries return the same receipt.
Use **Open Task** to review and start work separately when ready.

The `leave_meeting` tool is available with or without shared Company context.
Ask the invited Agent by name to leave; the backend disables its own invitation
and normal cleanup closes the model socket and submits final usage. Human audio
and chat continue. Restart the API and voice worker to load these changes; no
extra long-running process is needed.

## Local smoke check — 2026-09-29

The API readiness endpoint, web app, and Helia health endpoint passed local
checks. Helia registered with LiveKit 1.13.7, and the configured OpenAI project
successfully retrieved `gpt-live-1` model metadata. This confirms access, not a
completed voice generation or a real-call quality result.

The real LiveKit server exposed a missing `roomList` grant on Go administration
tokens. A regression test reproduced it before the fix; the LiveKit adapter's
race-enabled tests pass afterward. The grant is required by
[ListRooms](https://docs.livekit.io/reference/other/roomservice-api/), which
the room reconciliation loop calls. Participant tokens are unchanged.

Real microphone acceptance still requires a signed-in human joining a Huddle
and an administrator-configured Company allowance. No allowance was increased
by this smoke check.

## Local verification — 2026-09-25

- Nine Python tests pass: typed input during backend work, tool continuations,
  checkpoint failure, uncertain final usage, microphone admission, synthetic
  2/4/8-input mixing, and the pinned SDK's actual WebSocket event path against a
  local protocol peer.
- Huddle/controller/budget integration tests pass with the Go race detector.
  They cover duplicate claims/controllers, expired leases, shutdown, chat
  admission/history, exact typed values, greeting deduplication, cumulative
  usage, and the company-deletion fence. Handler/OpenAPI and dispatch tests pass.
- All 206 existing frontend tests, TypeScript checks, and production build pass.
  The Docker image builds and its entrypoint/configuration check passes.
- The full `make gate` run remains red on two independently reproduced baseline
  failures: `TestMigrationRepairsDashboardSchemaMissingFromExistingBaseline`
  (historical version-14 upgrade) and `TestSocialMigrationPreservesMainData`
  (historical Social upgrade rewrites conversation items). These are not waived.
- Live provider credentials were not configured for this verification. Actual
  GPT-Live access, real people/microphones, and deployed drain behavior remain
  Phase 5 acceptance checks. Keep rollout disabled until those checks pass.

## Independent AI notes

The worker also accepts `notes:<attemptId>` dispatches. These are silent capture
connections, separate from an invited conversational Agent. Deploy the updated
worker before enabling the API feature; do not enable it against an old worker.

API configuration (disabled by default):

```dotenv
HUDDLE_AI_ENABLED=true
HUDDLE_AI_WORKLOAD_SECRET=... # same 32+ byte secret as the worker
HUDDLE_NOTES_RECAP_API_KEY=... # optional, API-side Responses credential
HUDDLE_NOTES_RECAP_MODEL=... # explicit model with Responses JSON output support
```

Normal Huddles/LiveKit configuration is also required. The worker uses its
`OPENAI_API_KEY` for `gpt-live-transcribe`; this credential remains in the worker.
`HUDDLE_AI_ENABLED` enables both conversational invitations and independent notes.
Apply migrations through 119. Company notes policies default to enabled, with
30-day retention and 100 audio-stream hours per month; administrators can adjust
these limits in the advanced notes settings. Explicit existing policies are preserved. Start a
**new** Huddle: existing calls preserve their original capture mode. The local
ngrok policy includes the authenticated notes claim/append routes.

Smoke-test human-only capture, voice invitation/removal while notes stay active,
stop/restart, worker disconnect, call end, sourced recap editing and confirmed
Task creation. Verify provider access and overlapping speakers with real audio.
Automated tests use synthetic transcripts and do not establish audio quality.
Migration 118 adds a bounded final flush for Stop notes. Deploy the updated API
and worker together. The UI stays in “Saving final words” while the worker commits
its in-memory tail and waits for persisted final text, for at most 20 seconds.
Stop fixes the audio cutoff; retries cannot extend it or duplicate text. Silence
padding supports very short tails without reading later microphone audio.
Provider timeout or revoked access closes the attempt and reports incomplete
finalization. Ending the whole call or losing a microphone track still fails
closed and can lose its tail. No audio is written to disk or backfilled.
See [TECH.md](../docs/specs/huddles/TECH.md) for quota and recap-size limits.
