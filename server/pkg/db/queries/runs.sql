-- Runs: one agent execution.
-- The claim is a single statement — UPDATE ... WHERE id = (SELECT ... FOR
-- UPDATE SKIP LOCKED) — so concurrent claimers skip instead of blocking and
-- a row can never be claimed twice. updated_at is the delivery-receipt CAS
-- fence -- the row's last write, which for a dispatched run is the claim
-- itself; prepare_lease_expires_at is the claim→start lease.

-- The Run row is written before the user's item: runs.id is a database-generated UUID, so it
-- needs nothing from the conversation stream, and the item then carries the
-- run_id it was consumed by.
--
-- This is the ONLY way a Run is created. Nothing pre-allocates an id from
-- a separate identity allocator: a human-input continuation inserts here like every
-- other writer and stamps the id it gets back onto its Approval or Question in
-- the same transaction, so an id can never outlive the row it was to name.
--
-- The capacity lane is not a column here: task_id answers "which task, right
-- now" and goes NULL when the task is deleted, while the run's conversation
-- answers "is this task work" and survives that delete --
-- conversations.task_id is ON DELETE SET NULL, so the conversation keeps
-- kind = 'task'. The capacity gate reads the conversation.
-- A fresh Task Run advances its Task execution generation in this same
-- statement. Automatic retry children use CreateRetryRun below and copy the
-- parent's generation instead. Keeping the fence here means every caller of
-- the enqueue kernel gets the same semantics without a second Task write seam.
-- name: CreateRun :one
WITH advanced_task AS (
    UPDATE tasks
    SET execution_generation = tasks.execution_generation + 1
    WHERE tasks.id = sqlc.narg(task_id)
      AND tasks.company_id = sqlc.arg(company_id)
      AND tasks.conversation_id = sqlc.arg(conversation_id)
    RETURNING tasks.execution_generation, tasks.version
)
INSERT INTO runs (
    company_id, agent_id, task_id, conversation_id, runtime_id,
    execution_authorized_by_user_id,
    status, finished_at, priority, context,
    attempt, parent_run_id, force_fresh_session, fire_at, queued_at,
    task_execution_generation, run_type, revision, invocation_id, source_task_id, studio_enabled
) SELECT
    sqlc.arg(company_id), sqlc.arg(agent_id), sqlc.narg(task_id), sqlc.arg(conversation_id), sqlc.arg(runtime_id),
    sqlc.arg(execution_authorized_by_user_id)::uuid,
    sqlc.arg(status), CASE WHEN sqlc.arg(status)::text IN ('completed', 'failed', 'cancelled') THEN now() ELSE NULL END,
    sqlc.arg(priority), COALESCE(sqlc.arg(context)::jsonb, '{}'::jsonb) || jsonb_build_object(
        'source', jsonb_build_object(
            'entityType', CASE WHEN source.kind = 'task' THEN 'task' ELSE 'conversation' END,
            'entityId', CASE WHEN source.kind = 'task' THEN source_task.id ELSE source.id END,
            'revision', CASE WHEN source.kind = 'task' THEN source_task.version ELSE source.version END,
            'key', source_task.key,
            'title', CASE WHEN source.kind = 'task' THEN source_task.title ELSE source.title END
        )
    ),
    sqlc.arg(attempt), sqlc.narg(parent_run_id), sqlc.arg(force_fresh_session), sqlc.narg(fire_at),
    CASE WHEN sqlc.arg(status)::text = 'queued' THEN now() ELSE NULL END,
    (SELECT execution_generation FROM advanced_task),
    CASE WHEN source.kind = 'direct' THEN 'direct_chat' ELSE source.kind END,
    CASE WHEN source.kind = 'task' THEN source_task.version ELSE source.version END, sqlc.narg(invocation_id)::uuid,
    CASE WHEN source.kind = 'task' THEN source_task.id END,
    sqlc.arg(studio_enabled)::boolean
FROM conversations AS source
LEFT JOIN tasks AS source_task ON source_task.id = sqlc.narg(task_id) AND source_task.company_id = sqlc.arg(company_id)
WHERE source.id = sqlc.arg(conversation_id) AND source.company_id = sqlc.arg(company_id)
  AND (sqlc.narg(task_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM advanced_task))
  AND source.kind IN ('task','direct','schedule','channel_agent')
  AND (source.kind <> 'task' OR source_task.id IS NOT NULL)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- name: GetRunInCompany :one
SELECT * FROM runs WHERE id = $1 AND company_id = $2;

-- The HTTP boundary resolved this Run once from a live token before entering
-- the Approval/Question transaction. Match and lock that exact origin again
-- after its Company and Conversation locks so a terminal transition or account
-- deletion that won the race makes the stale proof unusable.
-- name: LockLiveHumanInputOriginRun :one
SELECT runs.*
FROM runs
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.agent_id = sqlc.arg(agent_id)::uuid
  AND runs.task_id IS NOT DISTINCT FROM sqlc.narg(task_id)::uuid
  AND runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND runs.status = 'running'
FOR UPDATE OF runs;

-- The task-lane capacity gate, first half: take the agent's claim lock.
-- The claim transaction takes it BEFORE the count below and BEFORE ClaimRun's
-- candidate SELECT, so two claims for one agent cannot each count the same
-- free slot (SKIP LOCKED hides nothing from a plain count) and both dispatch
-- past the cap.
--
-- An ADVISORY lock, not a row lock on agents, and the difference is the whole
-- point. Anything that locks the agents row couples the daemon's claim to
-- every writer of that table, and there are two that matter:
--
--   * every INSERT INTO runs takes FOR KEY SHARE on the agents row its
--     runs_company_agent_fkey points at, and holds it to commit — so a user
--     sending a chat message holds it across every item and attachment write
--     that follows. SELECT ... FOR UPDATE conflicts with FOR KEY SHARE.
--   * LockCompanyAgentGraph takes FOR UPDATE on EVERY agent row in the
--     company, and an agent config edit holds it across validation, the CAS
--     write, and the projection read.
--
-- Either one would block the claim at its FIRST statement, and because
-- ClaimRunsForRuntimes claims sequentially, one such wait stalls every later
-- (runtime, agent) pair in the batch, agents on other runtimes included.
-- FOR NO KEY UPDATE dodges the first but not the second. An advisory lock
-- dodges both: it lives in its own namespace, so the claim path waits on
-- nothing but another claim for the same agent.
--
-- The key is hashed from the (agent, runtime) pair under a fixed prefix:
-- capacity is per binding (ADR 0014), so two members' machines running the
-- same Agent never wait on each other. Two pairs whose ids hash alike would
-- serialize needlessly — harmless, and vanishingly rare at 64 bits.
-- pg_advisory_xact_lock releases at commit or rollback, so no claim can leak
-- a lock back into the pool.
--
-- What this gives up against a row lock: the cap is read below WITHOUT the
-- agent row held, so an edit committing between that read and the claim means
-- this claim acted on the previous value. That is fine by design — the cap is
-- dispatcher policy read live at claim time, never frozen into a run, so the
-- next claim uses the new number. What a run does freeze (its prompt and the
-- agent's execution identity) goes into runs.context at enqueue.
-- name: LockAgentClaimSlot :exec
SELECT pg_advisory_xact_lock(
    hashtextextended(
        'zenith-team.agent_claim:' || sqlc.arg(agent_id)::uuid::text
            || ':' || sqlc.arg(runtime_id)::uuid::text,
        0
    )
);

-- The cap itself. A plain read: the advisory lock above already serializes
-- this claim against every other claim for the same agent, which is the only
-- thing the count that follows needs to be stable against.
-- name: GetAgentClaimCapacity :one
SELECT max_concurrent_runs FROM agents WHERE id = sqlc.arg(agent_id)::uuid;

-- The task-lane capacity gate, second half: how many of the agent's runs
-- hold a slot right now. A separate statement AFTER the lock is granted on
-- purpose: under READ COMMITTED each statement takes a fresh snapshot, so
-- once a competing claim commits and releases the agent row this count sees
-- the run it dispatched. Folding the count into the lock statement (or into
-- ClaimRun itself) would evaluate it on the snapshot taken before the lock
-- wait and reopen the two-claims-one-slot race. The caller names READ
-- COMMITTED explicitly for that reason; see ClaimRun in run.go.
--
-- Deliberate divergence from the reference dispatcher pattern this gate is
-- copied from: only TASK-lane runs count toward — or wait for — the cap.
-- Direct-conversation runs bypass it in both directions. The cap is per
-- binding (ADR 0014): the count is the agent's runs on THIS runtime, so the
-- same Agent template set up on two members' machines has a full lane on
-- each.
--
-- The lane comes from the run's conversation, never from runs.task_id:
-- task_id is ON DELETE SET NULL and a company plan apply deletes exactly the
-- non-terminal tasks, so an executing run can lose its task_id. The
-- conversation outlives that delete and keeps kind = 'task', so the run stays
-- counted for as long as it holds its slot.
--
-- Plan shape: runs_agent_status_idx (agent_id, status) is a plain btree, so
-- it serves this as three equality probes, one per status value; the
-- conversation join runs over the probed rows. That residue is fine: the set
-- is the agent's ACTIVE runs, which the cap itself bounds to a handful (at
-- most max_concurrent_runs task runs plus the non-task runs the per-conversation
-- arm allows), so no wider or partial index is warranted.
-- name: CountActiveTaskLaneRuns :one
SELECT count(*) FROM runs
JOIN conversations ON conversations.id = runs.conversation_id
WHERE runs.agent_id = sqlc.arg(agent_id)::uuid
  AND runs.runtime_id = sqlc.arg(runtime_id)::uuid
  AND runs.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND conversations.kind = 'task';

-- The claim. Serialization: at most one active run per (task, agent) and at
-- most one active run per conversation. The per-agent capacity verdict
-- arrives as @task_lane_open, computed by the caller inside the same
-- transaction while holding the agent's claim lock (LockAgentClaimSlot,
-- GetAgentClaimCapacity, and the count above); it must NOT live here as a
-- bare count arm — see the gate queries for why.
--
-- Runtime ownership (ADR 0014) is the first arm: a runtime executes only its
-- registrant's own work, so the Run's authorizer must be the runtime's owner
-- at the moment of claim, that owner must still be an enabled, active
-- member, and the binding the enqueue resolved must still exist. A Run with
-- no authorizer, an ownerless or removed runtime, an owner who left or was
-- disabled, a binding since removed, or a runtime not currently online is
-- never claimable — the daemon sees no candidate, and the unbind, removal
-- and handoff flows cancel what they must. Keep this arm identical in
-- ListQueuedClaimCandidatesByRuntimes, which feeds the empty-claim cache
-- from the same predicate.
--
-- Project access (ADR 0017) is the second arm, beside the first: a Run in a
-- Task chat is claimable only while the delegate rule lets it execute on the
-- chat's Project (run_permission_held): its authorizer (by the first
-- arm also the machine's owner) holds project.read and project.tasks.execute
-- there. It is keyed on the chat, not on runs.task_id, which a Task delete
-- sets NULL; a Task chat with no Project (its Task was deleted before Task
-- chats recorded their Project) runs nothing. The function reads the
-- candidate's own row, one call per candidate. Go binds the permissions and
-- the default set; SQL names none of them. Keep it identical in
-- ListQueuedClaimCandidatesByRuntimes too.
-- Scalar schedule lookups stay correlated to each candidate. EXISTS under
-- these OR arms can become hashed subplans that scan all schedule history.
-- name: SelectClaimableRun :one
    SELECT candidate.* FROM runs AS candidate
    WHERE candidate.agent_id = sqlc.arg(agent_id)::uuid
      AND candidate.runtime_id = sqlc.arg(runtime_id)::uuid
      AND candidate.status = 'queued'
      AND (NOT candidate.task_scheduled OR (
          SELECT 1 FROM tasks calendar_task
          JOIN task_schedules calendar_schedule ON calendar_schedule.id = calendar_task.task_schedule_id
          JOIN tasks calendar_template ON calendar_template.id = calendar_schedule.template_task_id
          WHERE calendar_task.id = candidate.task_id AND calendar_task.company_id = candidate.company_id
            AND calendar_task.scheduled_at <= now()
            AND calendar_task.task_schedule_generation = calendar_schedule.generation
            AND calendar_task.assignment_status = 'accepted' AND calendar_template.assignment_status = 'accepted'
            AND NOT calendar_schedule.paused
          LIMIT 1
      ) IS NOT NULL)
      AND (candidate.schedule_firing_id IS NULL OR (
          SELECT 1
          FROM schedule_firings AS firing
          JOIN schedules AS schedule
            ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
          WHERE firing.id = candidate.schedule_firing_id AND firing.company_id = candidate.company_id
            AND firing.disposition = 'materialized'
            AND (schedule.trigger_type <> 'once' OR firing.created_at > now() - interval '7 days')
          LIMIT 1
      ) IS NOT NULL)
      AND EXISTS (
          SELECT 1
          FROM runtimes AS fence
          JOIN users AS fence_owners ON fence_owners.id = fence.owner_user_id
          JOIN company_members AS fence_owner_memberships
            ON fence_owner_memberships.company_id = fence.company_id
           AND fence_owner_memberships.user_id = fence.owner_user_id
           AND fence_owner_memberships.status = 'active'
          JOIN agent_runtime_bindings AS binding
            ON binding.company_id = fence.company_id
           AND binding.agent_id = candidate.agent_id
           AND binding.user_id = fence.owner_user_id
           AND binding.runtime_id = fence.id
          WHERE fence.id = candidate.runtime_id
            AND fence.removed_at IS NULL
            AND fence.status = 'online'
            AND fence_owners.disabled_at IS NULL
            AND candidate.execution_authorized_by_user_id = fence.owner_user_id
      )
      AND EXISTS (
          SELECT 1
          FROM conversations AS claim_chat
          WHERE claim_chat.company_id = candidate.company_id
            AND claim_chat.id = candidate.conversation_id
            AND (
                (claim_chat.kind <> 'task' AND claim_chat.document_thread_id IS NULL)
                OR (
                    claim_chat.project_id IS NOT NULL
                    AND run_permission_held(
                        candidate.company_id,
                        candidate.id,
                        claim_chat.project_id,
                        sqlc.arg(execute_permission)::text,
                        sqlc.arg(read_permission)::text,
                        sqlc.arg(company_default_permissions)::text[]
                    )
                )
            )
      )
      -- Task-lane capacity: at capacity the claim still runs, it just only
      -- sees non-task candidates (direct or schedule). Task-lane candidates stay 'queued'
      -- untouched; NotifyRunFinished on the next terminal transition is the
      -- wakeup that revisits them. Keyed on the conversation, not on task_id:
      -- a queued run whose task was deleted keeps the conversation it was
      -- enqueued in, so it cannot jump the cap by losing its task.
      AND (@task_lane_open::boolean OR EXISTS (
          SELECT 1 FROM conversations
          WHERE conversations.id = candidate.conversation_id
            AND conversations.kind IN ('direct', 'schedule', 'channel_agent')
      ))
      AND NOT EXISTS (
          SELECT 1 FROM runs AS active
          WHERE active.status IN ('dispatched', 'running', 'waiting_local_directory')
            AND (
                (candidate.task_id IS NOT NULL
                 AND active.task_id = candidate.task_id
                 AND active.agent_id = candidate.agent_id)
                OR
                (candidate.task_id IS NULL
                 AND active.conversation_id = candidate.conversation_id)
            )
      )
    ORDER BY candidate.priority DESC, candidate.created_at ASC, candidate.id ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED;

-- name: ClaimRun :one
UPDATE runs SET status = 'dispatched',
    claimed_at = now(),
    prepare_lease_expires_at = now() + make_interval(secs => sqlc.arg(prepare_lease_secs)::double precision),
    updated_at = now()
WHERE id = sqlc.arg(run_id)::uuid AND agent_id = sqlc.arg(agent_id)::uuid
  AND runtime_id = sqlc.arg(runtime_id)::uuid AND status = 'queued'
RETURNING *;

-- The ownership and Project access arms are SelectClaimableRun's, repeated
-- verbatim: a queued Run its authorizer may no longer execute is not a
-- candidate, so the runtime can be marked empty instead of being polled for a
-- claim that would never admit it.
-- name: ListQueuedClaimCandidatesByRuntimes :many
SELECT DISTINCT candidate.runtime_id, candidate.agent_id FROM runs AS candidate
WHERE candidate.runtime_id = ANY(@runtime_ids::uuid[])
  AND candidate.status = 'queued'
      AND (NOT candidate.task_scheduled OR (
          SELECT 1 FROM tasks calendar_task
          JOIN task_schedules calendar_schedule ON calendar_schedule.id = calendar_task.task_schedule_id
          JOIN tasks calendar_template ON calendar_template.id = calendar_schedule.template_task_id
          WHERE calendar_task.id = candidate.task_id AND calendar_task.company_id = candidate.company_id
            AND calendar_task.scheduled_at <= now()
            AND calendar_task.task_schedule_generation = calendar_schedule.generation
            AND calendar_task.assignment_status = 'accepted' AND calendar_template.assignment_status = 'accepted'
            AND NOT calendar_schedule.paused
          LIMIT 1
      ) IS NOT NULL)
  AND (candidate.schedule_firing_id IS NULL OR (
      SELECT 1
      FROM schedule_firings AS firing
      JOIN schedules AS schedule
        ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
      WHERE firing.id = candidate.schedule_firing_id AND firing.company_id = candidate.company_id
        AND firing.disposition = 'materialized'
        AND (schedule.trigger_type <> 'once' OR firing.created_at > now() - interval '7 days')
      LIMIT 1
  ) IS NOT NULL)
  AND EXISTS (
      SELECT 1
      FROM runtimes AS fence
      JOIN users AS fence_owners ON fence_owners.id = fence.owner_user_id
      JOIN company_members AS fence_owner_memberships
        ON fence_owner_memberships.company_id = fence.company_id
       AND fence_owner_memberships.user_id = fence.owner_user_id
       AND fence_owner_memberships.status = 'active'
      JOIN agent_runtime_bindings AS binding
        ON binding.company_id = fence.company_id
       AND binding.agent_id = candidate.agent_id
       AND binding.user_id = fence.owner_user_id
       AND binding.runtime_id = fence.id
      WHERE fence.id = candidate.runtime_id
        AND fence.removed_at IS NULL
        AND fence.status = 'online'
        AND fence_owners.disabled_at IS NULL
        AND candidate.execution_authorized_by_user_id = fence.owner_user_id
  )
  AND EXISTS (
      SELECT 1
      FROM conversations AS claim_chat
      WHERE claim_chat.company_id = candidate.company_id
        AND claim_chat.id = candidate.conversation_id
        AND (
            (claim_chat.kind <> 'task' AND claim_chat.document_thread_id IS NULL)
            OR (
                claim_chat.project_id IS NOT NULL
                AND run_permission_held(
                    candidate.company_id,
                    candidate.id,
                    claim_chat.project_id,
                    sqlc.arg(execute_permission)::text,
                    sqlc.arg(read_permission)::text,
                    sqlc.arg(company_default_permissions)::text[]
                )
            )
        )
  );

-- The enqueue kernel's Project access fence (ADR 0017): a Run in a Task chat
-- needs the delegate rule's project.tasks.execute on the chat's Project
-- (delegate_permission_held), the same rule the claim applies through
-- run_permission_held. The Run does not exist yet, so the caller names its
-- Authorizer; the chat it answers into is this one. A Task chat with no
-- Project admits no Run. Any other chat is not a Project's and passes. No
-- row: the chat is not in the Company.
-- name: RunConversationExecutePermitted :one
SELECT ((conversation.kind <> 'task' AND conversation.document_thread_id IS NULL) OR (
           conversation.project_id IS NOT NULL
           AND delegate_permission_held(
               conversation.company_id,
               sqlc.arg(authorizer_user_id)::uuid,
               conversation.id,
               conversation.project_id,
               sqlc.arg(execute_permission)::text,
               sqlc.arg(read_permission)::text,
               sqlc.arg(company_default_permissions)::text[]
           )
       ))::boolean AS permitted
FROM conversations AS conversation
WHERE conversation.company_id = sqlc.arg(company_id)::uuid
  AND conversation.id = sqlc.arg(conversation_id)::uuid;

-- A binding moving to another of the member's runtimes — a rebind, or a
-- runtime removal with a target — takes the member's pending work on that
-- Agent with it. Only rows nobody has claimed move: queued and deferred. A
-- dispatched or running row on the source is left where it is; the offline
-- sweepers already own that terminal transition.
-- name: RetargetPendingRunsForBindings :many
UPDATE runs
SET runtime_id = sqlc.arg(target_runtime_id)::uuid,
    updated_at = now()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND runtime_id = sqlc.arg(source_runtime_id)::uuid
  AND execution_authorized_by_user_id = sqlc.arg(user_id)::uuid
  AND agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND id = ANY(sqlc.arg(run_ids)::uuid[])
  AND status IN ('queued', 'deferred')
RETURNING *;

-- Lost-response recovery: a dispatch whose claim response never reached the
-- daemon moves back into claimable reach by refreshing the fence in place
--.
-- name: ReclaimStaleDispatchedRunsForRuntimes :many
-- Capped at the batch the daemon can accept: refreshing the fence on rows
-- beyond the cap would push undelivered work a full recovery window into the
-- future on every poll.
UPDATE runs
SET prepare_lease_expires_at = now() + make_interval(secs => @prepare_lease_secs::double precision),
    updated_at = now()
WHERE id IN (
    SELECT id FROM runs
    WHERE runtime_id = ANY(@runtime_ids::uuid[])
      AND status = 'dispatched'
      AND (runs.schedule_firing_id IS NULL OR EXISTS (
          SELECT 1
          FROM schedule_firings AS firing
          JOIN schedules AS schedule
            ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
          WHERE firing.id = runs.schedule_firing_id AND firing.company_id = runs.company_id
            AND firing.disposition = 'materialized'
            AND (schedule.trigger_type <> 'once' OR firing.created_at > now() - interval '7 days')
      ))
      AND updated_at < now() - make_interval(secs => @claim_recovery_secs::double precision)
      AND (prepare_lease_expires_at IS NULL OR prepare_lease_expires_at < now())
    ORDER BY priority DESC, updated_at ASC
    LIMIT @max_runs::int
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- Deferred promotion.
-- name: SelectDueDeferredRunsForUpdate :many
SELECT * FROM runs
WHERE status = 'deferred' AND fire_at <= now()
  AND (NOT sqlc.arg(filter_runtimes)::boolean OR runtime_id = ANY(sqlc.arg(runtime_ids)::uuid[]))
ORDER BY id FOR UPDATE SKIP LOCKED;

-- name: PromoteDueDeferredRunsByIDs :many
UPDATE runs SET status = 'queued', queued_at = now(), fire_at = NULL, wait_reason = NULL, updated_at = now()
WHERE id = ANY(sqlc.arg(run_ids)::uuid[]) AND status = 'deferred'
RETURNING *;

-- The Agent requests sign-in only when it invokes Studio. Keep its live
-- process, transcript, heartbeat and execution attempt while the CLI waits.
-- name: WaitForStudioAuth :one
UPDATE runs SET wait_reason = CASE WHEN sqlc.arg(waiting)::boolean
    THEN 'studio_auth_required' ELSE NULL END, updated_at = now()
WHERE id = sqlc.arg(run_id)::uuid AND status = 'running'
  AND (wait_reason IS NULL OR wait_reason = 'studio_auth_required')
RETURNING *;

-- Retry child. Clones the failed
-- parent into a fresh attempt. The child writes no 'user_message' item of its
-- own: the user said it once, on the parent, and the child reaches those words
-- through parent_run_id. Direct-conversation
-- retries are queued at GREATEST(priority, 3) so a transiently-failed earlier
-- turn is re-claimed ahead of any fresh conversation run (priority 2) queued
-- while it was failing. fire_at non-NULL arms a deferred backoff child that
-- PromoteDueDeferredRunsForRuntimes later flips to queued.
-- @force_fresh_session carries the session-poisoned
-- verdict computed from the parent's failure (see service.ResumeUnsafeFailure)
-- and drops the inherited resume pointer when set.
-- The child shares the parent's conversation, so it inherits the lane by
-- construction rather than re-deriving it from p.task_id: if the
-- parent's task was deleted while it ran, the parent is still holding a task
-- slot, its retry must keep holding one, not slip into the exempt lane.
-- A Task-backed retry is admitted only while the parent still owns the
-- current executable Task generation, executing Agent and assignee — a
-- handoff advances the generation, so a retry can never resurrect work on
-- the previous assignee's machine. Locking that Task row keeps the
-- eligibility decision and child insert in one transaction;
-- direct-conversation Runs have no Task policy and remain eligible.
-- The child's runtime is the authorizer's CURRENT binding of the Agent
-- (ADR 0014), not the parent's runtime copied blindly: a member who moved
-- the Agent to another of their machines retries there, and one who unbound
-- it gets no retry at all.
-- name: CreateRetryRun :one
WITH parent AS MATERIALIZED (
    SELECT runs.*
    FROM runs
    WHERE runs.id = @parent_run_id
      AND runs.status = 'failed'
), eligible_task AS MATERIALIZED (
    SELECT tasks.id
    FROM tasks
    JOIN parent AS p
      ON tasks.company_id = p.company_id
     AND tasks.id = p.task_id
     AND tasks.conversation_id = p.conversation_id
     AND tasks.execution_generation = p.task_execution_generation
     AND EXISTS (SELECT 1 FROM task_run_agents WHERE task_id = tasks.id AND agent_id = p.agent_id)
     AND (tasks.executed_by_agent_id IS NULL) = COALESCE(
         (p.context->'executionProfile'->'task'->>'humanWork')::boolean, false)
     AND tasks.assignee_user_id = p.execution_authorized_by_user_id
    WHERE tasks.status = 'in_progress'
      AND NOT EXISTS (
          SELECT 1
          FROM task_dependencies
          JOIN tasks AS prerequisite
            ON prerequisite.company_id = task_dependencies.company_id
           AND prerequisite.id = task_dependencies.prerequisite_task_id
          WHERE task_dependencies.company_id = tasks.company_id
            AND task_dependencies.dependent_task_id = tasks.id
            AND prerequisite.status <> 'done'
      )
    FOR UPDATE OF tasks
), current_binding AS MATERIALIZED (
    SELECT binding.runtime_id
    FROM agent_runtime_bindings AS binding
    JOIN parent AS p
      ON binding.company_id = p.company_id
     AND binding.agent_id = p.agent_id
     AND binding.user_id = p.execution_authorized_by_user_id
    JOIN runtimes
      ON runtimes.id = binding.runtime_id
     AND runtimes.removed_at IS NULL
     AND runtimes.owner_user_id = binding.user_id
), retry AS (
    INSERT INTO runs (
        company_id, agent_id, task_id, conversation_id, runtime_id,
        execution_authorized_by_user_id,
        status, priority, context,
        session_id,
        attempt, parent_run_id, force_fresh_session, fire_at, queued_at,
        task_execution_generation, connector_selection_mode, run_type, revision, invocation_id,
        source_task_id, studio_enabled, schedule_firing_id
    )
    SELECT
        p.company_id, p.agent_id, p.task_id, p.conversation_id,
        (SELECT runtime_id FROM current_binding),
        p.execution_authorized_by_user_id,
        CASE WHEN sqlc.narg(fire_at)::timestamptz IS NOT NULL THEN 'deferred' ELSE 'queued' END,
        CASE WHEN p.task_id IS NULL THEN GREATEST(p.priority, 3) ELSE p.priority END,
        p.context,
        CASE WHEN @force_fresh_session::boolean THEN NULL ELSE p.session_id END,
        p.attempt + 1, p.id,
        @force_fresh_session::boolean, sqlc.narg(fire_at),
        CASE WHEN sqlc.narg(fire_at)::timestamptz IS NULL THEN now() ELSE NULL END,
        p.task_execution_generation, p.connector_selection_mode, p.run_type, p.revision, p.invocation_id,
        p.source_task_id, p.studio_enabled, p.schedule_firing_id
    FROM parent AS p
    WHERE (p.schedule_firing_id IS NULL OR EXISTS (
        SELECT 1
        FROM schedule_firings AS firing
        JOIN schedules AS schedule
          ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
        WHERE firing.id = p.schedule_firing_id AND firing.company_id = p.company_id
          AND firing.disposition = 'materialized'
          AND (schedule.trigger_type <> 'once' OR firing.created_at > now() - interval '7 days')
    ))
      AND EXISTS (SELECT 1 FROM current_binding)
      AND (p.task_id IS NULL OR EXISTS (SELECT 1 FROM eligible_task))
    RETURNING *
), inherited_skills AS (
    INSERT INTO run_skills (company_id, run_id, version_id)
    SELECT retry.company_id, retry.id, run_skills.version_id
    FROM retry JOIN run_skills ON run_skills.run_id = retry.parent_run_id
), inherited_selection AS (
    -- The retry keeps its parent's exact Connector scope. It never inherits
    -- the frozen snapshot marker: its own claim freezes a snapshot, and until
    -- then a failed freeze may cancel it. A binding can only move the retry
    -- to another runtime of the same provider, so the scope still applies.
    INSERT INTO run_connector_selections (
        run_id, connector_id, policy_revision, source, native_id, contract_digest
    )
    SELECT retry.id, selected.connector_id, selected.policy_revision,
        selected.source, selected.native_id, selected.contract_digest
    FROM retry
    JOIN run_connector_selections AS selected ON selected.run_id = retry.parent_run_id
)
SELECT * FROM retry;

-- Delivery-receipt failure: the fence is (id, runtime, dispatched, exact
-- updated_at) — CAS on the claim we actually made. A dispatched row is by
-- definition not started, so there is no second predicate to write.
-- name: RequeueRunAfterClaimFailure :one
WITH recovery AS MATERIALIZED (
    SELECT candidate.id,
    (NOT candidate.task_scheduled OR EXISTS (
        SELECT 1 FROM tasks AS calendar_task
        JOIN task_schedules AS calendar_schedule ON calendar_schedule.id = calendar_task.task_schedule_id
        JOIN tasks AS calendar_template ON calendar_template.id = calendar_schedule.template_task_id
        WHERE calendar_task.id = candidate.task_id AND calendar_task.company_id = candidate.company_id
          AND calendar_task.task_schedule_generation = calendar_schedule.generation
          AND calendar_task.assignment_status = 'accepted'
          AND calendar_template.assignment_status = 'accepted'
          AND NOT calendar_schedule.paused
    )) AND (candidate.schedule_firing_id IS NULL OR EXISTS (
        SELECT 1
        FROM schedule_firings AS firing
        JOIN schedules AS schedule
          ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
        WHERE firing.id = candidate.schedule_firing_id AND firing.company_id = candidate.company_id
          AND firing.disposition = 'materialized'
          AND (schedule.trigger_type <> 'once' OR firing.created_at > now() - interval '7 days')
    )) AS can_requeue
    FROM runs AS candidate WHERE candidate.id = $1
)
UPDATE runs
SET status = CASE WHEN recovery.can_requeue THEN 'queued' ELSE 'cancelled' END,
    finished_at = CASE WHEN recovery.can_requeue THEN NULL ELSE now() END,
    error = CASE WHEN recovery.can_requeue THEN NULL ELSE 'schedule_invalidated' END,
    claimed_at = NULL, queued_at = COALESCE(runs.queued_at, now()), prepare_lease_expires_at = NULL,
    updated_at = now()
FROM recovery
WHERE runs.id = recovery.id AND runs.runtime_id = $2 AND runs.status = 'dispatched'
  AND runs.updated_at = $3
RETURNING runs.*;

-- name: ExtendRunPrepareLease :one
UPDATE runs
SET prepare_lease_expires_at = now() + make_interval(secs => @prepare_lease_secs::double precision),
    updated_at = now()
WHERE id = $1 AND runtime_id = $2
  AND status IN ('dispatched', 'waiting_local_directory')
RETURNING id;

-- name: StartRun :one
-- Fenced by runtime_id like every other daemon-driven transition: a run is
-- started by the machine that claimed it, and without the fence a daemon that
-- still holds a stale run id can start work another runtime now owns.
UPDATE runs
SET status = 'running', wait_reason = NULL,
    started_at = COALESCE(started_at, now()),
    prepare_lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND runtime_id = $2
  AND status IN ('dispatched', 'waiting_local_directory')
RETURNING *;

-- name: MarkRunWaitingLocalDirectory :one
UPDATE runs
SET status = 'waiting_local_directory', wait_reason = 'local_directory_busy',
    prepare_lease_expires_at = now() + make_interval(secs => @prepare_lease_secs::double precision),
    updated_at = now()
WHERE id = $1 AND status = 'dispatched'
RETURNING *;

-- name: PinRunSession :exec
UPDATE runs
SET session_id = COALESCE(NULLIF(@session_id::text, ''), session_id),
    updated_at = now()
WHERE id = $1;

-- Terminal writes. A completed
-- run with a missing session rollout stores NULL session_id so a later
-- follow-up never resumes a pointer that cannot replay.
-- name: CompleteRun :one
UPDATE runs
SET status = 'completed',
    finished_at = now(),
    session_id = CASE WHEN @session_rollout_missing::boolean THEN NULL
                      ELSE COALESCE(NULLIF(@session_id::text, ''), session_id) END,
    session_rollout_missing = @session_rollout_missing::boolean,
    retired_session_id = COALESCE(NULLIF(@retired_session_id::text, ''), retired_session_id),
    prepare_lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND status = 'running'
RETURNING *;

-- name: FailRun :one
UPDATE runs
SET status = 'failed', error = $2,
    finished_at = now(),
    failure_reason = NULLIF(@failure_reason::text, ''),
    session_id = CASE WHEN @session_rollout_missing::boolean THEN NULL
                      ELSE COALESCE(NULLIF(@session_id::text, ''), session_id) END,
    session_rollout_missing = @session_rollout_missing::boolean,
    retired_session_id = COALESCE(NULLIF(@retired_session_id::text, ''), retired_session_id),
    wait_reason = NULL,
    prepare_lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND status IN ('dispatched', 'running', 'waiting_local_directory')
RETURNING *;

-- name: CancelRun :one
UPDATE runs
SET status = 'cancelled',
    finished_at = now(),
    error = NULLIF(@reason::text, ''),
    wait_reason = NULL, prepare_lease_expires_at = NULL, fire_at = NULL,
    updated_at = now()
WHERE id = $1 AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
RETURNING *;

-- A Connector snapshot rejection rolls the claim transaction back, so its
-- terminal fallback starts from the original queued row. Another poll may
-- win before that fallback acquires the Conversation lock. Restrict the
-- cancellation to a queued row that has never committed a frozen snapshot:
-- a later dispatch, or a dispatched claim requeued after delivery failure,
-- owns the Run's lifecycle from that point onward.
-- name: CancelQueuedRunAfterConnectorSnapshotFailure :one
UPDATE runs
SET status = 'cancelled',
    finished_at = now(),
    error = NULLIF(sqlc.arg(reason)::text, ''),
    wait_reason = NULL, prepare_lease_expires_at = NULL, fire_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(run_id)::uuid
  AND status = 'queued'
  AND connector_snapshot_frozen_at IS NULL
RETURNING *;

-- name: CancelQueuedScheduleRun :one
UPDATE runs
SET status = 'cancelled',
    finished_at = now(),
    error = NULLIF(sqlc.arg(reason)::text, ''),
    wait_reason = NULL, prepare_lease_expires_at = NULL, fire_at = NULL,
    updated_at = now()
FROM schedule_firings
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND runs.schedule_firing_id = sqlc.arg(firing_id)::uuid
  AND schedule_firings.id = runs.schedule_firing_id
  AND schedule_firings.schedule_id = sqlc.arg(schedule_id)::uuid
  AND schedule_firings.company_id = runs.company_id
  AND schedule_firings.conversation_id = runs.conversation_id
  AND runs.status IN ('queued', 'deferred')
RETURNING runs.*;

-- name: ListActiveRunConversationIDsForCompanyUser :many
-- Account deletion locks every affected Conversation before it mutates a
-- Run. This is deliberately a plain candidate read: the caller sorts and
-- locks the Conversation rows first, then CancelRunsForCompanyUser rechecks
-- the active status under that lock order. A direct Conversation belongs to
-- its creator; Approval/Question receipts preserve the user whose decision
-- started a human-input continuation that has no user_message of its own.
WITH RECURSIVE authored_run_lineage AS (
    SELECT runs.id
    FROM runs
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND (
          EXISTS (
              SELECT 1
              FROM conversation_items
              WHERE conversation_items.run_id = runs.id
                AND conversation_items.company_id = runs.company_id
                AND conversation_items.type = 'user_message'
                AND conversation_items.author_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM conversations
              WHERE conversations.id = runs.conversation_id
                AND conversations.company_id = runs.company_id
                AND conversations.kind = 'direct'
                AND conversations.created_by_user_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM approvals
              WHERE approvals.company_id = runs.company_id
                AND approvals.continuation_run_id = runs.id
                AND approvals.decided_by_user_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM questions
              WHERE questions.company_id = runs.company_id
                AND questions.continuation_run_id = runs.id
                AND questions.resolved_by_user_id = sqlc.narg(author_id)::uuid
          )
      )
    UNION
    SELECT descendants.id
    FROM runs AS descendants
    JOIN authored_run_lineage AS ancestors
      ON ancestors.id = descendants.parent_run_id
    WHERE descendants.company_id = sqlc.arg(company_id)::uuid
)
SELECT DISTINCT runs.conversation_id
FROM runs
JOIN authored_run_lineage ON authored_run_lineage.id = runs.id
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY runs.conversation_id;

-- name: CancelRunsForCompanyUser :many
-- A Run is the user's when their own words are in it, it is private work in a
-- direct Conversation they created, or their Approval/Question decision
-- started the human-input continuation. A retry child inherits the claim
-- through parent_run_id.
WITH RECURSIVE authored_run_lineage AS (
    SELECT runs.id
    FROM runs
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND (
          EXISTS (
              SELECT 1
              FROM conversation_items
              WHERE conversation_items.run_id = runs.id
                AND conversation_items.company_id = runs.company_id
                AND conversation_items.type = 'user_message'
                AND conversation_items.author_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM conversations
              WHERE conversations.id = runs.conversation_id
                AND conversations.company_id = runs.company_id
                AND conversations.kind = 'direct'
                AND conversations.created_by_user_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM approvals
              WHERE approvals.company_id = runs.company_id
                AND approvals.continuation_run_id = runs.id
                AND approvals.decided_by_user_id = sqlc.narg(author_id)::uuid
          )
          OR EXISTS (
              SELECT 1
              FROM questions
              WHERE questions.company_id = runs.company_id
                AND questions.continuation_run_id = runs.id
                AND questions.resolved_by_user_id = sqlc.narg(author_id)::uuid
          )
      )
    UNION
    SELECT descendants.id
    FROM runs AS descendants
    JOIN authored_run_lineage AS ancestors
      ON ancestors.id = descendants.parent_run_id
    WHERE descendants.company_id = sqlc.arg(company_id)::uuid
), cancelled AS (
    UPDATE runs
    SET status = 'cancelled', error = NULLIF(@reason::text, ''),
        finished_at = now(),
        wait_reason = NULL, prepare_lease_expires_at = NULL, fire_at = NULL,
        updated_at = now()
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
      AND runs.id IN (SELECT authored_run_lineage.id FROM authored_run_lineage)
    RETURNING runs.*
)
SELECT *
FROM cancelled
ORDER BY created_at, id;

-- name: LockActiveRunsForAgent :many
SELECT id FROM runs
WHERE agent_id = $1
  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
FOR UPDATE;

-- Sweepers.

-- What the runtime sweep counts before it demotes a machine, over exactly the
-- statuses ListOfflineRuntimeFailureCandidates fails a moment later — so the
-- number the owner is told is the number of runs that die. Read inside the
-- demotion transaction, while the rows are still 'online', which is why it
-- keys on runtimes it is given rather than on runtimes.status. A runtime with
-- nothing in flight is absent from the result, not zero.
-- name: CountInFlightRunsByRuntime :many
SELECT runtime_id, count(*)::bigint AS in_flight
FROM runs
WHERE runtime_id = ANY(sqlc.arg(runtime_ids)::uuid[])
  AND status IN ('dispatched', 'running', 'waiting_local_directory')
GROUP BY runtime_id;

-- name: ListOfflineRuntimeFailureCandidates :many
SELECT runs.id, runs.company_id, runs.conversation_id
FROM runs
WHERE runs.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND runs.runtime_id IN (SELECT id FROM runtimes WHERE status = 'offline');

-- name: FailRunsForOfflineRuntimes :many
UPDATE runs
SET status = 'failed',
    finished_at = now(),
    error = 'runtime went offline', failure_reason = 'runtime_offline',
    wait_reason = NULL, prepare_lease_expires_at = NULL,
    updated_at = now()
WHERE id = ANY(sqlc.arg(run_ids)::uuid[])
  AND status IN ('dispatched', 'running', 'waiting_local_directory')
  AND runtime_id IN (SELECT id FROM runtimes WHERE status = 'offline')
RETURNING *;

-- name: ListStaleRunFailureCandidates :many
SELECT runs.id, runs.company_id, runs.conversation_id
FROM runs
WHERE (
    runs.status = 'dispatched'
    AND runs.updated_at < now() - make_interval(secs => @dispatch_timeout_secs::double precision)
    AND (runs.prepare_lease_expires_at IS NULL OR runs.prepare_lease_expires_at < now())
) OR (
    runs.status = 'running'
    AND runs.updated_at < now() - make_interval(secs => @running_timeout_secs::double precision)
    AND NOT EXISTS (
        SELECT 1 FROM runtimes r
        WHERE r.id = runs.runtime_id
          AND r.status = 'online'
          AND r.last_seen_at >= now() - make_interval(secs => @runtime_stale_secs::double precision)
    )
);

-- name: FailStaleRuns :many
UPDATE runs
SET status = 'failed',
    finished_at = now(),
    error = 'run timed out', failure_reason = 'timeout',
    prepare_lease_expires_at = NULL,
    updated_at = now()
WHERE id = ANY(sqlc.arg(run_ids)::uuid[])
  AND (
      (
          status = 'dispatched'
          AND updated_at < now() - make_interval(secs => @dispatch_timeout_secs::double precision)
          AND (prepare_lease_expires_at IS NULL OR prepare_lease_expires_at < now())
      ) OR (
          status = 'running'
          AND updated_at < now() - make_interval(secs => @running_timeout_secs::double precision)
          AND NOT EXISTS (
              SELECT 1 FROM runtimes r
              WHERE r.id = runs.runtime_id
                AND r.status = 'online'
                AND r.last_seen_at >= now() - make_interval(secs => @runtime_stale_secs::double precision)
          )
      )
  )
RETURNING *;

-- name: ListQueuedRunExpiryCandidates :many
SELECT runs.id, runs.company_id, runs.conversation_id
FROM runs
WHERE runs.status IN ('queued', 'deferred')
  AND (
      (runs.schedule_firing_id IS NULL AND NOT runs.task_scheduled AND runs.status = 'queued'
       AND runs.created_at < now() - make_interval(secs => @ttl_secs::double precision))
      OR EXISTS (
          SELECT 1 FROM schedule_firings AS firing
          JOIN schedules AS schedule
            ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
          WHERE firing.id = runs.schedule_firing_id AND firing.company_id = runs.company_id
            AND schedule.trigger_type = 'once'
            AND firing.created_at < now() - make_interval(secs => @once_wait_secs::double precision)
      )
  )
ORDER BY runs.created_at, runs.id
LIMIT @max_per_tick;

-- name: ExpireStaleQueuedRuns :many
UPDATE runs
SET status = 'failed',
    finished_at = now(),
    error = 'run expired in queue', failure_reason = 'queued_expired',
    prepare_lease_expires_at = NULL, fire_at = NULL, wait_reason = NULL,
    updated_at = now()
WHERE id = ANY(sqlc.arg(run_ids)::uuid[])
  AND status IN ('queued', 'deferred')
  AND (
      (runs.schedule_firing_id IS NULL AND NOT runs.task_scheduled AND runs.status = 'queued'
       AND runs.created_at < now() - make_interval(secs => @ttl_secs::double precision))
      OR EXISTS (
          SELECT 1 FROM schedule_firings AS firing
          JOIN schedules AS schedule
            ON schedule.id = firing.schedule_id AND schedule.company_id = firing.company_id
          WHERE firing.id = runs.schedule_firing_id AND firing.company_id = runs.company_id
            AND schedule.trigger_type = 'once'
            AND firing.created_at < now() - make_interval(secs => @once_wait_secs::double precision)
      )
  )
RETURNING *;

-- name: ListRuntimeRecoveryFailureCandidates :many
SELECT runs.id, runs.company_id, runs.conversation_id
FROM runs
WHERE runs.runtime_id = sqlc.arg(runtime_id)::uuid
  AND runs.status IN ('dispatched', 'running', 'waiting_local_directory');

-- name: RecoverOrphanedRunsForRuntime :many
UPDATE runs
SET status = 'failed',
    finished_at = now(),
    error = 'daemon restarted while run was in flight',
    failure_reason = 'runtime_recovery',
    wait_reason = NULL, prepare_lease_expires_at = NULL,
    updated_at = now()
WHERE id = ANY(sqlc.arg(run_ids)::uuid[])
  AND runtime_id = sqlc.arg(runtime_id)::uuid
  AND status IN ('dispatched', 'running', 'waiting_local_directory')
RETURNING *;

-- User-facing reads.

-- One projection serves list, get, cancel receipts, and typed lifecycle-event
-- snapshots. Public callers enable the visibility fence; lifecycle producers
-- already hold an authoritative run row and disable only that predicate.
-- retry_queued is one indexed EXISTS, never a child lookup per result row.
-- name: ListRunProjections :many
SELECT
  runs.id,
  runs.run_type,
  runs.revision,
  COALESCE(CASE WHEN runs.revision IS NOT NULL THEN jsonb_build_object(
    'id', runs.context #>> '{source,entityId}',
    'type', runs.context #>> '{source,entityType}',
    'key', runs.context #> '{source,key}',
    'title', runs.context #> '{source,title}'
  ) END, 'null'::jsonb)::jsonb AS source,
  runs.company_id,
  agents.id AS agent_id,
  agents.name AS agent_name,
  runs.task_id,
  runs.task_execution_generation,
  runs.conversation_id,
  runtimes.id AS runtime_id,
  runtimes.name AS runtime_name,
  runtimes.status AS runtime_status,
  runtimes.provider AS runtime_provider,
  -- The runtime's registrant: only they are shown its id (ADR 0014).
  runtimes.owner_user_id AS runtime_owner_user_id,
  runs.status,
  runs.priority,
  runs.attempt,
  runs.parent_run_id,
  (
    runs.status = 'failed'
    AND EXISTS (
      SELECT 1
      FROM runs AS retry_children
      WHERE retry_children.parent_run_id = runs.id
    )
  )::boolean AS retry_queued,
  -- COALESCE, not a bare column: sqlc infers nullability through a plain
  -- LEFT JOIN but not through LEFT JOIN LATERAL, so a bare reference here
  -- generates a NOT NULL uuid that panics the first time a retry child or an
  -- assignment run — neither of which owns a user_message — scans a real NULL.
  -- The nil uuid is the absent value the projection reads as "no trigger
  -- message".
  COALESCE(trigger_items.id, '00000000-0000-0000-0000-000000000000'::uuid)
      AS trigger_item_id,
  runs.transcript_through_seq,
  runs.error,
  runs.failure_reason,
  runs.wait_reason,
  runs.created_at,
  runs.updated_at,
  runs.fire_at,
  runs.queued_at,
  runs.claimed_at,
  runs.started_at,
  runs.finished_at,
  COALESCE((
    SELECT jsonb_build_object(
      'inputTokens', sum(run_usage.input_tokens)::text,
      'outputTokens', sum(run_usage.output_tokens)::text,
      'cacheReadTokens', sum(run_usage.cache_read_tokens)::text,
      'cacheWriteTokens', sum(run_usage.cache_write_tokens)::text,
      'costUsdTicks', round(sum(run_usage.provider_cost_usd) FILTER (WHERE run_usage.billing_mode <> 'subscription') * 10000000000)::text
    )
    FROM run_usage
    WHERE run_usage.run_id = runs.id AND usage_access.allowed
    HAVING count(*) > 0
  ), 'null'::jsonb)::jsonb AS usage,
  NOT usage_access.allowed AS usage_restricted,
  -- Browser routing reads these two, never the wire. A run on a task
  -- conversation is visible to its Project's readers; a direct one only to
  -- the founder who opened it. kind is authoritative rather than runs.task_id,
  -- which goes NULL when a plan apply deletes the task while the
  -- conversation — and its kind — survives.
  conversations.kind AS conversation_kind,
  conversations.created_by_user_id AS conversation_created_by_user_id
FROM runs
JOIN conversations
  ON conversations.id = runs.conversation_id
 AND conversations.company_id = runs.company_id
JOIN agents
  ON agents.id = runs.agent_id
 AND agents.company_id = runs.company_id
JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
CROSS JOIN LATERAL (
  SELECT EXISTS (
    SELECT 1 FROM company_members
    WHERE company_members.company_id = runs.company_id
      AND company_members.user_id = @user_id::uuid
      AND company_members.status = 'active'
      AND (runs.execution_authorized_by_user_id = @user_id::uuid
           OR company_members.role = ANY(@usage_roles::text[]))
  ) AS allowed
) AS usage_access
-- The run's input is its own first 'user_message' item, one seek per row
-- against conversation_items_run_seq_idx. A retry child has none of its own —
-- the words are on the parent — so trigger_item_id is NULL for it, exactly as
-- the uninherited trigger edge left it before.
LEFT JOIN LATERAL (
    SELECT conversation_items.id
    FROM conversation_items
    WHERE conversation_items.run_id = runs.id
      AND conversation_items.company_id = runs.company_id
      AND conversation_items.type = 'user_message'
    ORDER BY conversation_items.seq
    LIMIT 1
) AS trigger_items ON TRUE
WHERE runs.company_id = @company_id
  -- Run history follows the chat it ran in (ADR 0017): a Task chat's Runs are
  -- read by its Project's readers, and keep following that Project after the
  -- Task is deleted, because the chat keeps its project_id. Direct and
  -- Schedule Runs stay their creator's. The correlated lookup names the row
  -- and the person, so each candidate Run costs one indexed probe
  -- (TestProjectAccessListsProbeReadersPerRow).
  AND (
    NOT @enforce_visibility::boolean
    OR (
      conversations.kind IN ('task', 'direct', 'schedule')
      -- A scalar lookup with LIMIT 1: Postgres may turn an EXISTS here
      -- into a hashed subplan that builds the person's whole readable set
      -- (every chat they read, from a scan of all conversations) before it
      -- matches one Run. This form is always one probe per Run.
      AND COALESCE((
        SELECT true
        FROM conversation_readers AS reader
        WHERE reader.company_id = runs.company_id
          AND reader.conversation_id = runs.conversation_id
          AND reader.user_id = @user_id::uuid
        LIMIT 1
      ), false)
    )
  )
  AND (NOT @has_run_id::boolean OR runs.id = @run_id::uuid)
  AND (
    NOT @has_cursor::boolean
    OR (runs.created_at, runs.id) < (@before_created_at::timestamptz, @before_id::uuid)
  )
  AND (NOT @has_statuses::boolean OR runs.status = ANY(@statuses::text[]))
  AND (
    NOT @active_only::boolean
    OR runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
  )
  -- The live task_id, or source_task_id after the Task's deletion cleared it.
  -- Both are indexed.
  AND (NOT @has_task_id::boolean OR runs.task_id = @task_id::uuid OR runs.source_task_id = @task_id::uuid)
  AND (NOT @has_run_type::boolean OR runs.run_type = @filter_run_type::text)
  AND (NOT @has_revision::boolean OR runs.revision = @filter_revision::bigint)
  AND (NOT @has_from::boolean OR runs.created_at >= @from_created_at::timestamptz)
  AND (NOT @has_to::boolean OR runs.created_at < @to_created_at::timestamptz)
  AND (NOT @has_conversation_id::boolean OR runs.conversation_id = @conversation_id::uuid)
  AND (NOT @has_agent_id::boolean OR runs.agent_id = @filter_agent_id::uuid)
  -- A runtime is its registrant's alone (ADR 0014): another member's runtime
  -- matches nothing, so the filter never ties a Run to their machine.
  AND (
    NOT @has_runtime_id::boolean
    OR (runs.runtime_id = @filter_runtime_id::uuid AND runtimes.owner_user_id = @user_id::uuid)
  )
ORDER BY runs.created_at DESC, runs.id DESC
LIMIT @page_size;

-- name: GetLatestRunForConversation :one
SELECT * FROM runs
WHERE conversation_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetRunStatus :one
SELECT status FROM runs WHERE id = $1;

-- Session resume lookup for follow-up runs.
-- name: GetLastRunSessionForConversation :one
-- The resume pointer for a fresh claim, plus through_seq: the Conversation seq
-- that session provably received, meaning its own trigger user_message and
-- everything it appended. transcript_through_seq is this Run's newest provider
-- item rather than a Conversation watermark, so a Run that failed before
-- execution reports its trigger alone, and a retry child that neither consumed
-- a message nor appended reports 0, which makes the next resume inline more
-- history rather than less. runtime_id is where that session lives: a
-- provider session is local to one machine, so the claim offers it only to
-- a Run on the same runtime and otherwise passes on just the watermark.
SELECT runs.session_id,
	   COALESCE(runs.context #>> '{agent,skillSetHash}', '')::text AS skill_set_hash,
       runs.session_rollout_missing,
       runs.retired_session_id,
       GREATEST(runs.transcript_through_seq, COALESCE(trigger_items.seq, 0))::bigint
           AS through_seq,
       runs.runtime_id
FROM runs
LEFT JOIN LATERAL (
    SELECT conversation_items.seq
    FROM conversation_items
    WHERE conversation_items.run_id = runs.id
      AND conversation_items.type = 'user_message'
    ORDER BY conversation_items.seq
    LIMIT 1
) AS trigger_items ON TRUE
WHERE runs.conversation_id = $1 AND runs.agent_id = $2
  AND runs.status IN ('completed', 'failed')
ORDER BY runs.updated_at DESC, runs.created_at DESC, runs.id DESC
LIMIT 1;

-- name: GetRunAgentBrief :one
-- The enqueue kernel freezes the agent's execution identity into the run
-- context in the same transaction that creates the run, exactly as it does
-- the trigger prompt: frozen input must not depend on a later read. The
-- plugin observations are the run's runtime's — the binding the enqueue
-- resolved — since the template itself has no machine.
SELECT name, title, instructions,
 COALESCE(CASE WHEN chat.agent_id IS NULL THEN agents.model ELSE chat.model END, '')::text AS model,
 COALESCE(CASE WHEN chat.agent_id IS NULL THEN agents.thinking_level ELSE chat.thinking_level END, '')::text AS thinking_level, provider,
 (reports_to_agent_id IS NULL)::boolean AS is_root,
 COALESCE(settings->'capabilities','{}'::jsonb)::jsonb AS capabilities,
 COALESCE((
  SELECT jsonb_agg(jsonb_build_object('nativeId',plugins.native_id,'connectorIdentities',COALESCE(observations.connector_identities,'[]'::jsonb)) ORDER BY plugins.id)
  FROM plugin_catalog_items AS plugins
  LEFT JOIN plugin_runtime_observations AS observations ON observations.plugin_id=plugins.id AND observations.runtime_id=sqlc.arg(runtime_id)::uuid
  WHERE plugins.company_id=agents.company_id
   AND plugins.id = ANY(agents.disabled_plugin_ids)
 ),'[]'::jsonb)::jsonb AS disabled_plugins
FROM agents
LEFT JOIN conversation_model_settings chat ON chat.agent_id = agents.id AND chat.conversation_id = sqlc.arg(conversation_id)::uuid
WHERE agents.id = sqlc.arg(agent_id)::uuid;

-- name: GetRunTaskBrief :one
-- A Task execution profile is frozen by the enqueue kernel rather than
-- reconstructed by the daemon after claim. The executing-Agent fence keeps
-- a caller from attaching an unrelated Task to a Run, and the assignee
-- fence is the kernel-level "only the assignee starts a Task Run"
-- (ADR 0014): no row when the authorizer is not the Task's assignee.
SELECT
    tasks.key,
    tasks.title,
    tasks.description,
    tasks.status,
    tasks.priority,
    tasks.conversation_id,
    tasks.workspace_generation,
    (tasks.executed_by_agent_id IS NULL)::boolean AS human_work
FROM tasks
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.conversation_id = sqlc.arg(conversation_id)::uuid
  AND EXISTS (SELECT 1 FROM task_run_agents WHERE task_id = tasks.id AND agent_id = sqlc.arg(agent_id)::uuid)
  AND tasks.assignee_user_id = sqlc.arg(authorized_by_user_id)::uuid;

-- The transcript head. Terminal transitions take the Conversation lock first,
-- then this Run lock; appends to the Run's slice of the conversation stream
-- serialize on the same Run row, so the returned head is the authoritative
-- committed prefix a terminal proof must name.

-- name: LockRunForTerminalTranscript :one
SELECT *
FROM runs
WHERE id = @run_id
FOR UPDATE;

-- name: LockRunningRunForTranscript :one
SELECT *
FROM runs
WHERE id = @run_id
  AND runtime_id = @runtime_id
  AND status = 'running'
FOR UPDATE;

-- name: UpdateRunTranscriptHead :one
-- A NULL final answer key keeps the last one: only an item with a proven final
-- answer part replaces it.
UPDATE runs
SET transcript_through_seq = @through_seq,
    final_answer_item_key = COALESCE(sqlc.narg(final_answer_item_key)::text, final_answer_item_key),
    updated_at = clock_timestamp()
WHERE id = @run_id
RETURNING *;

-- name: LockRunHistoryRows :many
SELECT id FROM runs WHERE id = ANY(sqlc.arg(run_ids)::uuid[])
ORDER BY id FOR UPDATE;

-- name: ListPendingRunsForBindingsForUpdate :many
-- The member's unclaimed work on the named Agents of one runtime: what a
-- rebind or a removal with a target moves, and what an unbind or a removal
-- without one cancels.
SELECT id FROM runs WHERE company_id = sqlc.arg(company_id)::uuid
  AND runtime_id = sqlc.arg(source_runtime_id)::uuid
  AND execution_authorized_by_user_id = sqlc.arg(user_id)::uuid
  AND agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND status IN ('queued', 'deferred') ORDER BY id FOR UPDATE;

-- name: ListPendingRunsForBindings :many
-- The rows ListPendingRunsForBindingsForUpdate locked, in the shape the
-- cancellation pass takes (conversation-then-id order).
SELECT runs.* FROM runs WHERE company_id = sqlc.arg(company_id)::uuid
  AND runtime_id = sqlc.arg(source_runtime_id)::uuid
  AND execution_authorized_by_user_id = sqlc.arg(user_id)::uuid
  AND agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND status IN ('queued', 'deferred') ORDER BY conversation_id, id;

-- name: ListActiveRunsForTaskHandoff :many
-- A confirmed handoff cancels every live Run of the Task — queued, deferred,
-- claimed or executing — before the assignee or executing Agent changes
-- (ADR 0014). Same conversation-then-id order as the other cancellation
-- passes; for a Task that is one conversation, already held by the caller.
SELECT runs.* FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.task_id = sqlc.arg(task_id)::uuid
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY runs.conversation_id, runs.id;

-- The caller holds the Task Conversation before locking these Runs. Filtering
-- under the row lock preserves any Run a daemon already dispatched while the
-- Calendar mutation waited for its locks.
-- name: LockPendingTaskScheduleRuns :many
SELECT runs.* FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.task_id = sqlc.arg(task_id)::uuid
  AND runs.task_scheduled
  AND runs.status IN ('queued', 'deferred')
ORDER BY runs.conversation_id, runs.id
FOR UPDATE;

-- name: ListActiveRunIDsInConversationsForUpdate :many
SELECT id FROM runs WHERE company_id = sqlc.arg(company_id)::uuid
  AND conversation_id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory')
ORDER BY id FOR UPDATE;

-- name: ListRunIDsForDeletedTasksForUpdate :many
SELECT id FROM runs WHERE company_id = sqlc.arg(company_id)::uuid
  AND task_id = ANY(sqlc.arg(task_ids)::uuid[]) ORDER BY id FOR UPDATE;

-- name: ListActiveRunsForRuntimeOwner :many
-- Membership revocation cancels queued and active work on the owner's runtimes.
SELECT runs.* FROM runs
JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runtimes.owner_user_id = sqlc.arg(owner_user_id)::uuid
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY runs.conversation_id, runs.id;

-- name: ListActiveRunsForProjectAuthorizer :many
-- A member lost project.read or project.tasks.execute on a Project (ADR 0017):
-- their live Runs on that Project's chats end. Keyed on the chat's Project,
-- not runs.task_id, which a Task delete clears. Same conversation-then-id
-- order as the other cancellation passes.
SELECT runs.* FROM conversations
JOIN runs ON runs.company_id = conversations.company_id AND runs.conversation_id = conversations.id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.project_id = sqlc.arg(project_id)::uuid
  AND runs.execution_authorized_by_user_id = sqlc.arg(user_id)::uuid
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY runs.conversation_id, runs.id;

-- name: CountActiveRunsForProjectAuthorizers :many
-- ListActiveRunsForProjectAuthorizer counted for many people on many
-- Projects in one read, for a Group change preview: how many Runs the sweep
-- would end, per Project and person who has any.
SELECT conversations.project_id::uuid AS project_id,
       runs.execution_authorized_by_user_id::uuid AS user_id,
       count(*)::bigint AS runs
FROM conversations
JOIN runs ON runs.company_id = conversations.company_id AND runs.conversation_id = conversations.id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND runs.execution_authorized_by_user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
GROUP BY conversations.project_id, runs.execution_authorized_by_user_id
ORDER BY conversations.project_id, runs.execution_authorized_by_user_id;

-- name: ListProjectAccessSweepPairs :many
-- Which of these people have anything a Project access sweep would end on
-- which of these Projects, in one read, for a Group change: a queued item in
-- one of the Project's Task chats (ListQueuedRunQueueConversationIDsForUser
-- with a Project), or a live Run they authorized on its chats
-- (ListActiveRunsForProjectAuthorizer). The sweep visits only these pairs;
-- every other pair would find nothing to end.
SELECT conversations.project_id::uuid AS project_id,
       item.initiated_by_user_id::uuid AS user_id
FROM conversation_run_queue_items AS item
JOIN conversations
  ON conversations.company_id = item.company_id
 AND conversations.id = item.conversation_id
 AND conversations.kind = 'task'
WHERE item.company_id = sqlc.arg(company_id)::uuid
  AND item.initiated_by_user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND item.state = 'queued'
  AND conversations.project_id = ANY(sqlc.arg(project_ids)::uuid[])
UNION
SELECT conversations.project_id::uuid AS project_id,
       runs.execution_authorized_by_user_id::uuid AS user_id
FROM conversations
JOIN runs ON runs.company_id = conversations.company_id AND runs.conversation_id = conversations.id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND runs.execution_authorized_by_user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY project_id, user_id;

-- name: ListProjectAccessRecheckUserIDs :many
-- The people a visibility change must re-check (ADR 0017): who authorized a
-- live Run on the Project's chats, whose Runs may not continue without
-- access, and who has items queued in them, which are removed once the
-- person may no longer post there.
SELECT runs.execution_authorized_by_user_id::uuid AS user_id
FROM conversations
JOIN runs ON runs.company_id = conversations.company_id AND runs.conversation_id = conversations.id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.project_id = sqlc.arg(project_id)::uuid
  AND runs.execution_authorized_by_user_id IS NOT NULL
  AND runs.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
UNION
SELECT items.initiated_by_user_id
FROM conversations
JOIN conversation_run_queue_items AS items
  ON items.company_id = conversations.company_id AND items.conversation_id = conversations.id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.project_id = sqlc.arg(project_id)::uuid
  AND items.state = 'queued'
ORDER BY 1;


-- name: MarkTaskRunScheduled :exec
UPDATE runs SET task_scheduled = true
WHERE id = sqlc.arg(run_id) AND task_id = sqlc.arg(task_id) AND company_id = sqlc.arg(company_id)
    AND status = 'queued';
