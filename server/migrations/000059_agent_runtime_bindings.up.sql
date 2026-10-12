-- Private runtimes, Agent templates and per-member bindings (ADR 0014,
-- superseding ADR 0007 decisions 2-4 and ADR 0011).
--
-- Every runtime is private to the member who registered it: only the
-- registrant's own authorized Runs execute there, and only the registrant
-- sees, renames, removes or probes it. Sharing (runtimes.visibility) and
-- hosting consent (agents.runtime_bound_by_user_id) are removed.
--
-- An Agent is a Company-owned template with its own provider. It has no
-- machine of its own: a member who wants to run it binds it to one of their
-- own runtimes of that provider (agent_runtime_bindings, one row per Agent
-- and member). A Run's runtime is the binding of (Agent, authorizing member),
-- and the claim admits a Run only while its authorizer owns that runtime.
--
-- A Task always names a member assignee — the person responsible for running
-- it on their own machine — and optionally the Agent that does the work
-- (executed_by_agent_id, renamed from assignee_agent_id; NULL means the
-- assignee does it themself).
--
-- The up file is also the cutover; the order below matters because bindings
-- and assignees are derived from agents.runtime_id before it is dropped.

-- The safe Task snapshot names the assignee columns; it is recreated at the
-- end with the renamed column.
DROP VIEW activity_subject_states;

-- 1. The Agent's provider is its own. Every Agent has a NOT NULL runtime today,
--    so the backfill leaves no NULL behind.
ALTER TABLE agents
    ADD COLUMN provider text;

UPDATE agents
SET provider = runtimes.provider
FROM runtimes
WHERE runtimes.id = agents.runtime_id;

ALTER TABLE agents
    ALTER COLUMN provider SET NOT NULL,
    ADD CONSTRAINT agents_provider_check CHECK (provider IN ('claude', 'codex')),
    ADD CONSTRAINT agents_company_id_id_provider_key UNIQUE (company_id, id, provider);

ALTER TABLE runtimes
    ADD CONSTRAINT runtimes_company_id_id_provider_key UNIQUE (company_id, id, provider);

-- 2. One row per (Agent, member): "I run this Agent on this runtime of mine".
--    The provider match is structural — both foreign keys carry it — so a
--    binding can never name a runtime of another provider. That the runtime
--    is the member's own, not removed, and that the member is active are
--    cross-table invariants the service restates under its locks.
CREATE TABLE agent_runtime_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    runtime_id uuid NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_runtime_bindings_pkey PRIMARY KEY (id),
    CONSTRAINT agent_runtime_bindings_agent_user_key UNIQUE (agent_id, user_id),
    CONSTRAINT agent_runtime_bindings_agent_fkey
        FOREIGN KEY (company_id, agent_id, provider)
        REFERENCES agents(company_id, id, provider) ON DELETE CASCADE,
    CONSTRAINT agent_runtime_bindings_runtime_fkey
        FOREIGN KEY (company_id, runtime_id, provider)
        REFERENCES runtimes(company_id, id, provider) ON DELETE CASCADE
);

CREATE INDEX agent_runtime_bindings_company_user_idx
    ON agent_runtime_bindings (company_id, user_id);
CREATE INDEX agent_runtime_bindings_runtime_idx
    ON agent_runtime_bindings (runtime_id);

-- 3. Cutover bindings: every Agent's current runtime, credited to its
--    registrant when that registrant is an enabled, active member. An Agent
--    whose registrant is gone stays a template with no binding; a removed
--    runtime binds nobody.
INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
SELECT agents.company_id, agents.id, runtimes.owner_user_id, runtimes.id, runtimes.provider
FROM agents
JOIN runtimes
  ON runtimes.id = agents.runtime_id
 AND runtimes.company_id = agents.company_id
JOIN users AS registrants
  ON registrants.id = runtimes.owner_user_id
 AND registrants.disabled_at IS NULL
JOIN company_members AS memberships
  ON memberships.company_id = runtimes.company_id
 AND memberships.user_id = runtimes.owner_user_id
 AND memberships.status = 'active'
WHERE runtimes.removed_at IS NULL;

-- 4. Capability selections are template state: the runtime key goes.
UPDATE agents
SET settings = jsonb_set(settings, '{capabilities}', (settings -> 'capabilities') - 'runtimeId')
WHERE settings -> 'capabilities' ? 'runtimeId';

-- 5. Tasks: a member is always responsible; an Agent may do the work.
ALTER TABLE tasks
    DROP CONSTRAINT tasks_assignee_exactly_one_check;
ALTER TABLE tasks
    RENAME COLUMN assignee_agent_id TO executed_by_agent_id;
ALTER TABLE tasks
    RENAME CONSTRAINT tasks_assignee_agent_fkey TO tasks_executed_by_agent_fkey;
ALTER INDEX tasks_assignee_agent_idx RENAME TO tasks_executed_by_agent_idx;
ALTER INDEX tasks_company_agent_status_idx RENAME TO tasks_company_executed_by_status_idx;

-- The assignee of an Agent-assigned Task becomes, in order: the member the
-- executing Agent's cutover binding was credited to (its registrant), the
-- Task's creator while an active member, the Company's earliest active
-- owner, the creator regardless of membership, and finally any member row.
-- A Task in a Company with no member and no creating user cannot be given
-- an assignee, and SET NOT NULL fails loudly: by design.
UPDATE tasks
SET assignee_user_id = COALESCE(
    (
        SELECT binding.user_id
        FROM agent_runtime_bindings AS binding
        WHERE binding.company_id = tasks.company_id
          AND binding.agent_id = tasks.executed_by_agent_id
        ORDER BY binding.created_at, binding.id
        LIMIT 1
    ),
    (
        SELECT membership.user_id
        FROM company_members AS membership
        WHERE membership.company_id = tasks.company_id
          AND membership.user_id = tasks.creator_user_id
          AND membership.status = 'active'
    ),
    (
        SELECT membership.user_id
        FROM company_members AS membership
        WHERE membership.company_id = tasks.company_id
          AND membership.role = 'owner'
          AND membership.status = 'active'
        ORDER BY membership.created_at, membership.user_id
        LIMIT 1
    ),
    tasks.creator_user_id,
    (
        SELECT membership.user_id
        FROM company_members AS membership
        WHERE membership.company_id = tasks.company_id
        ORDER BY membership.created_at, membership.user_id
        LIMIT 1
    )
)
WHERE tasks.assignee_user_id IS NULL;

ALTER TABLE tasks
    ALTER COLUMN assignee_user_id SET NOT NULL;

DROP INDEX tasks_assignee_user_idx;
CREATE INDEX tasks_assignee_user_idx
    ON tasks USING btree (company_id, assignee_user_id, number DESC);

-- workspace_generation is the identity of the Task's workspace on its
-- assignee's machine. Only a confirmed handoff advances it (execution_generation
-- also advances on every fresh Run, so it cannot serve). A daemon whose
-- worktree for the Task's Conversation was prepared under an older value
-- retires that root and prepares a fresh one from the Project's main: work
-- another member did in between is never continued from stale files.
ALTER TABLE tasks
    ADD COLUMN workspace_generation integer NOT NULL DEFAULT 0;

-- 6. Agents lose their machine and their binder; runtimes lose sharing.
DROP INDEX agents_runtime_idx;
ALTER TABLE agents
    DROP CONSTRAINT agents_company_runtime_fkey,
    DROP COLUMN runtime_id,
    DROP COLUMN runtime_bound_by_user_id;

ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_visibility_check,
    DROP COLUMN visibility,
    DROP COLUMN visibility_revision;

-- 7. Runs cutover (the 000026 pattern). A pending Run the new claim can never
--    admit is cancelled with a readable reason rather than left to expire:
--    - its authorizer is not the registrant of its runtime, or it has no
--      authorizer at all;
--    - or, not yet claimed (queued or deferred), no binding names its Agent,
--      its authorizer and its runtime. An Agent that moved to another
--      runtime did not take its queued Runs along, and a retry kept its
--      parent's runtime, so such a Run can sit on a runtime its Agent's one
--      cutover binding does not name.
--    In-flight Runs keep going, waiting_local_directory included: the claim
--    already happened, and their terminal effect stays fenced by the Task's
--    execution generation, executing Agent and assignee.
UPDATE runs
SET status = 'cancelled',
    error = 'cancelled at the private-runtime cutover: its authorizer has no binding of the Agent on its runtime',
    wait_reason = NULL,
    prepare_lease_expires_at = NULL,
    fire_at = NULL,
    updated_at = now()
FROM runtimes
WHERE runtimes.id = runs.runtime_id
  AND runs.status IN ('queued', 'deferred', 'waiting_local_directory')
  AND (
      runs.execution_authorized_by_user_id IS DISTINCT FROM runtimes.owner_user_id
      OR (
          runs.status IN ('queued', 'deferred')
          AND NOT EXISTS (
              SELECT 1
              FROM agent_runtime_bindings AS binding
              WHERE binding.company_id = runs.company_id
                AND binding.agent_id = runs.agent_id
                AND binding.user_id = runs.execution_authorized_by_user_id
                AND binding.runtime_id = runs.runtime_id
          )
      )
  );

--    A channel tag's Run (000042) holds its invocation's one active slot in
--    the channel-Agent Conversation, so a tag whose Run was cancelled just
--    above is closed the way the application closes a tag whose caller
--    cannot run the Agent; left active, it would stall every tag queued
--    behind it. The Run's authorizer is the tag's caller, who has no binding
--    on that runtime, so the reason is agent_unbound. now() is fixed for the
--    transaction, so this matches exactly the Runs cancelled above.
UPDATE channel_invocations AS invocation
SET status = 'cancelled',
    publication_status = 'blocked',
    failure_reason = 'agent_unbound',
    updated_at = clock_timestamp()
FROM runs
WHERE runs.invocation_id = invocation.id
  AND runs.status = 'cancelled'
  AND runs.updated_at = now()
  AND runs.error = 'cancelled at the private-runtime cutover: its authorizer has no binding of the Agent on its runtime'
  AND invocation.status IN ('active', 'waiting_input');

-- 8. Two Inbox kinds: the assignee's nudge when a colleague's Task message
--    waits for them (task_run_requested) and the previous assignee's notice
--    of a handoff (task_handoff). The check is rewritten whole, so the
--    Calendar kinds 000030 added and the mention kind 000037 added are
--    named here too.
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned', 'blocker', 'run_completed', 'run_failed',
        'approval_request', 'question_request', 'runtime_offline',
        'invitation_renewal', 'calendar_reminder', 'calendar_update',
        'discussion_mention', 'task_run_requested', 'task_handoff'
    ));

-- 9. The safe Task snapshot again, naming the executing Agent. Everything else
-- is 000031's definition unchanged: a discussion still exposes no metadata.
CREATE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'assigneeUserId', task.assignee_user_id, 'executedByAgentId', task.executed_by_agent_id,
        'creatorUserId', task.creator_user_id, 'creatorAgentId', task.creator_agent_id,
        'projectId', task.project_id, 'milestoneType', task.milestone_type,
        'conversationId', task.conversation_id, 'completedAt', task.completed_at,
        'prerequisiteIds', COALESCE((SELECT jsonb_agg(dependency.prerequisite_task_id ORDER BY dependency.prerequisite_task_id)
            FROM task_dependencies AS dependency WHERE dependency.dependent_task_id = task.id), '[]'::jsonb),
        'revision', task.version, 'createdAt', task.created_at, 'updatedAt', task.updated_at
    ) AS state,
    jsonb_build_object('entityKey', task.key, 'entityTitle', task.title) AS context
FROM tasks AS task
UNION ALL
SELECT 'conversation', conversation.id, conversation.company_id, conversation.version,
    CASE conversation.kind WHEN 'task' THEN 'company' WHEN 'direct' THEN 'user' WHEN 'schedule' THEN 'user' END,
    CASE WHEN conversation.kind IN ('direct','schedule') THEN conversation.created_by_user_id END,
    CASE WHEN conversation.kind IN ('human_direct','channel') THEN jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at
    ) ELSE jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'title', conversation.title, 'taskId', conversation.task_id, 'scheduleId', conversation.schedule_id,
        'agentId', conversation.agent_id, 'createdByUserId', conversation.created_by_user_id,
        'pinnedAt', conversation.pinned_at, 'archivedAt', conversation.archived_at,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at) END,
    CASE WHEN conversation.kind IN ('human_direct','channel') THEN '{}'::jsonb
        ELSE jsonb_build_object('entityKey', NULL, 'entityTitle', conversation.title) END
FROM conversations AS conversation
UNION ALL
SELECT 'run', run.id, run.company_id, NULL::bigint,
    CASE conversation.kind WHEN 'task' THEN 'company' WHEN 'direct' THEN 'user' WHEN 'schedule' THEN 'user' END,
    CASE WHEN conversation.kind IN ('direct','schedule') THEN conversation.created_by_user_id END,
    jsonb_build_object(
        'id', run.id, 'companyId', run.company_id, 'runType', run.run_type, 'revision', run.revision,
        'conversationId', run.conversation_id, 'taskId', run.task_id, 'agentId', run.agent_id, 'runtimeId', run.runtime_id,
        'status', run.status, 'failureReason', run.failure_reason, 'waitReason', run.wait_reason,
        'attempt', run.attempt, 'parentRunId', run.parent_run_id, 'taskExecutionGeneration', run.task_execution_generation,
        'createdAt', run.created_at, 'fireAt', run.fire_at,
        'source', CASE WHEN run.revision IS NOT NULL THEN jsonb_build_object(
            'id', run.context->'source'->'entityId', 'type', run.context->'source'->'entityType',
            'key', run.context->'source'->'key', 'title', run.context->'source'->'title') END
    ), jsonb_build_object('entityKey', run.context->'source'->'key', 'entityTitle', run.context->'source'->'title')
FROM runs AS run JOIN conversations AS conversation ON conversation.id = run.conversation_id;
