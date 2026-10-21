# ADR 0018: Scheduler beat on asynq, Postgres ledger

Status: Accepted. Date: 2026-10-06.

Builds on [ADR 0014](0014-private-runtimes-and-agent-bindings.md): a
Schedule Run executes on the creator's own binding. Changes the 2-hour
queued limit for Runs that belong to a Schedule firing.

## Context

A Schedule fires an Agent once or on a cron cadence. Firing is one
PostgreSQL transaction: it writes a firing record, a message in the
Schedule's Conversation and a queued Run, and moves the Schedule's next time
forward. After the commit the server wakes the daemon on the creator's
computer, and the daemon claims the Run over HTTPS or WebSocket.

Before this decision, the beat, the repeating check that finds due
Schedules, was one goroutine in every API replica, with no leader and no
lock. It ran at boot and then every 15 seconds.
Due time came from the database clock, and four guards stopped a double
firing: a row lock that a second worker skips (`SKIP LOCKED`), a version
check when the next time moves forward, the unique index on
`(schedule_id, occurrence_at)` and the unique link from a Run to its firing
(both in `server/migrations/000005_execution.up.sql`).

A review of that beat found that it could stall or fall behind without
anyone being told:

- **One bad Schedule stopped all firing.** Any error that was not a refusal
  ended the pass. The bad row had the oldest time, so it came first again
  on every pass and every replica. An archived Agent, the cadence check that
  firing ran again and a time-zone rule change all triggered it.
- **A lock wait had no limit.** The pool set no lock or statement timeout
  and a pass had no deadline, so one slow writer on a Company row froze the
  beat everywhere, with no log line.
- **Bursts fell behind.** A replica fired at most 64 times per pass, so
  1,000 Schedules due at 09:00 on two replicas fired up to about two minutes
  late, and a late Schedule silently skipped times.
- **A panic in a firing stopped the API process.**
- **Nothing measured it.** `/healthz` was static, `/readyz` checked only the
  database, the migration version and Redis, no schedule metric existed,
  and the issue log dropped the error and the Schedule id.

Separately, a queued Run expired after 2 hours whatever started it (the
runtime sweeper in `server/cmd/server/runtime_sweeper.go`). A one-time
Schedule is already completed when it fires, so if the creator's laptop
slept through those 2 hours its only Run was lost, and a daily Schedule
lost each day the laptop stayed shut.

The question was whether asynq, a Go library that keeps background jobs in
Redis and has its own cron-style Scheduler, should run the beat.

## Decision

1. **asynq runs the beat in every API replica; there is no leader.** Each
   replica runs an asynq Scheduler with exactly one entry, `@every 10s`,
   which enqueues a `schedule:scan` task on the `schedules` queue with
   Unique 30 s, so at most one scan waits at a time. Each replica also runs
   an asynq Server on that queue. Duplicate scans from several replicas are
   expected and harmless.
2. **The scan asks PostgreSQL what is due.** It reads active Schedules whose
   next time has passed by the database clock, oldest first and in pages,
   and enqueues one `schedule:fire` task per Schedule time with the task id
   `schedule:fire:<schedule id>:<next_occurrence unix>`. Redis refuses a
   second task with the same id while the first is waiting, running,
   retrying or archived. The payload carries flat ids only (the Schedule
   and its Company); the time in the task id only filters duplicates, and
   the handler reads everything else from PostgreSQL.
3. **The fire task runs `FireSchedule(id)`.** That is the existing firing for one
   Schedule in one transaction (firing record, message, queued Run, next
   time), then the wake-up after the commit. The transaction sets a 5 s lock
   timeout and a 10 s statement timeout, and the task has a 30 s deadline.
   It checks again inside the transaction that the time is due, so a stale
   or duplicate task does nothing.
4. **PostgreSQL is the ledger (the record of what is due and what fired);
   Redis holds ids and timing only.** The unique
   index on `(schedule_id, occurrence_at)` stays the real guard. Losing every
   Redis key delays times but never loses or doubles one.
5. **The old loop stays as the repair sweep.** It runs every 30 s on
   PostgreSQL alone, fires active Schedules overdue by more than a 30 s
   grace, pages through all of them under a per-pass deadline, skips a
   failing Schedule for the rest of the pass, recovers from a panic, and runs
   the drift pass. It is the only firing path while Redis is down, and it
   covers deploy gaps, lost or stuck tasks and bursts.
6. **One Schedule's failure never delays another.** Transient database
   errors (deadlock, serialization failure, a timeout, a lost connection)
   are retried and do not pause the Schedule on their own; only the
   `firing_failed` escalation below can pause. If the retries run out, the
   sweep fires that time after its grace. A refusal (an archived Agent, lost
   access, a missing binding) records a firing that needs attention and
   pauses the Schedule with its reason. A stored trigger that no longer
   parses pauses it with `trigger_invalid`. After a set number of failed
   attempts on the same time that belong to that one Schedule, the firing
   records `needs_attention` with the reason `firing_failed` and pauses only
   that Schedule; a database-wide outage or slowdown never escalates.
   Firing no longer re-runs the creation-time cadence check.
7. **Health is measured by outcome.** A probe worker reads the firing lag
   (how late the oldest due Schedule is) and a count of overdue Schedules
   straight from PostgreSQL, independent of asynq's own queue read, and
   exports them with closed-label metrics; logs carry the Schedule and
   Company ids and the error. Alerts page on overdue Schedules. Firing
   health never feeds `/readyz`, so one stuck Schedule cannot take every pod
   out of service.
8. **Redis becomes required** on every deploy: AOF persistence,
   `maxmemory-policy noeviction`, and its own database index or instance per
   environment, because asynq's key prefix is fixed. The shared Redis
   client talks to a single node only; failover is an open question.
   `APP_REDIS_URL`, once required only above one API replica, is now always
   required. New settings are `.env` variables.
9. **The daemon does not change.** It never talks to Redis and keeps
   claiming over HTTPS and WebSocket.
10. **No new package.** The scan and fire handlers live in
    `server/internal/schedule`. The asynq constructors, the logger adapter
    and the worker wiring live in `server/cmd/server`, reusing the existing
    Redis client. The workers use asynq's `Start` and `Shutdown`, never
    `Run`, because the server already owns the signals
    (`server/cmd/server/main.go`).
11. **Pin `github.com/hibiken/asynq` v0.26.0, gated on a compatibility
    check.** The constructors that take a shared Redis client exist only
    from v0.25.0, so there is no older version to fall back to. At the time
    of this decision, automated bumps of asynq's go-redis dependency to 9.18
    or later had failed asynq's own CI for an unknown reason, while Zenith
    uses go-redis 9.21 on Go 1.26 (`server/go.mod`). So adoption required
    asynq's own `go test -race ./...` to pass with go-redis 9.21 on Go 1.26
    against Redis 7, and Zenith to build with asynq added. A failure that
    only affects asynq's tests is acceptable; a change in how Redis scripts
    or pub/sub behave is not.
12. **Offline policy: wait until replaced.** A queued Schedule Run no longer
    expires because of its age. The 2-hour queued limit keeps applying to
    Runs with no firing link (`schedule_firing_id IS NULL`); the rule keys
    on the link, not on the Run type. A linked Run waits for the computer
    until a newer time replaces it, or the creator pauses or cancels the
    Schedule; whether an edit also cancels it is still open. A one-time
    Schedule's Run waits at most 7 days from its firing, then expires as
    `queued_expired`. A retry keeps the firing link, so a newer time can
    still replace it. Pause and Cancel mark the live firing cancelled
    whatever the Run's status, cancel the Run only while it is queued or
    deferred, and stop a later retry or requeue from bringing it back.

## Consequences

- **A Redis outage takes the API out of rotation.** `/readyz` already pings
  Redis through the Studio relay when Redis is set, and Redis is now always
  set. During an outage every pod fails
  readiness and leaves the load balancer. The sweep still records firings
  within about 75 s, but no computer can claim them until Redis returns.
  Single-replica deploys that ran without Redis gain this dependency.
  Splitting readiness from Redis is an open question.
- **Two firing paths share one core.** The beat and the sweep both call
  `FireSchedule`, and the tests cover both, two replicas firing the same
  time, a Redis flush in the middle, and a rolling deploy that mixes the old
  loop with the new beat.
- **Small Redis leftovers on an unclean exit.** Each crash or forced kill
  leaves one asynq Scheduler history key of up to 1,000 entries, with no
  expiry, plus stale members in asynq's server and worker sets. They are
  bounded.
- **Stopping fits the 30 s grace.** The HTTP drain (15 s) comes first, then
  the asynq Scheduler and Server stop under a hard deadline even if Redis
  hangs, inside the 30 s stop grace
  (`deploy/docker-compose.production.yml`).
  A firing cut off mid-way rolls back.
- **Task names cannot shadow each other.** asynq's router falls back to the
  longest matching prefix, so no task type may be a prefix of another. A
  breaking payload change gets a new, non-prefix name, and the old handler
  stays for one release. Fields are only ever added.
- **asynq's hooks and logs need guards.** Its enqueue, health and error
  hooks run without panic recovery, so each is wrapped; its error logs
  during a Redis outage are rate limited.
- **Waiting Runs can wait a long time.** A Schedule has at most one waiting
  Run, but a monthly cron's Run can wait weeks. While it waits, archiving
  its Agent is refused, as for any queued Run
  (`server/internal/agent/service.go`). Making archive cancel those Runs and
  pause their Schedules instead is a possible later change. Rolling back
  the wait-until-replaced change expires every waiting Schedule Run older
  than 2 hours on the first sweeper tick.
- **Migrations are appended, never edited.** The schema changes are a new
  migration, `000109_scheduler`: it makes the Run's firing link a plain
  index so retries can share it, adds the `cancelled` firing state and adds
  the missed-times count.
- **Operators check their settings before deploying.** Redis needs AOF,
  a memory limit with the `noeviction` policy and a database index per
  environment, and the pod stop grace must cover the shutdown above.

## Alternatives considered

- **Keep the PostgreSQL-only beat and fix it in place.** The simplest
  choice: one store, no new dependency, and its repair work is needed
  either way. After those fixes, asynq's
  main gains are a quick retry for each Schedule time (1–8 s instead of the
  next 15 s pass), a task with its own deadline for each Schedule time,
  parallel workers in a burst spread over every replica, and a few seconds
  less delay in the normal case (a 10 s timer instead of a 15 s poll); a
  small PostgreSQL worker pool could give much of that too. What
  this decision costs instead: a pre-1.0 dependency not yet proven on Go 1.26
  with go-redis 9.21; Redis becoming durable and required, with the
  readiness cost above; and two firing paths to keep consistent.
- **asynq's PeriodicTaskManager with one cron entry per Schedule.**
  Rejected. Its timer lives in each process's memory; edits reach it only at
  the next sync, 3 minutes by default; a second clock would compute due
  times beside `next_occurrence_at`; every heartbeat rewrites every entry;
  and one entry per Schedule multiplies the history-key leak and the sync
  lag.
- **One delayed (`ProcessAt`) task per Schedule time.** Rejected. Every
  create, edit, pause and cancel would have to write both PostgreSQL and
  Redis, and long-lived tasks would make Redis the record of what is due.
- **asynq workers on the user's computer.** Rejected. Redis would have to be
  reachable from the internet with shared credentials and no isolation
  between Companies, and a sleeping laptop cannot fire anything anyway.

## Links

- [ADR 0014](0014-private-runtimes-and-agent-bindings.md): private runtimes
  and per-member Agent bindings.
