-- 000005_execution: the execution machine — runtimes, runs, run_tokens, and
-- the human-in-the-loop requests a run raises.
--
-- A run produces no transcript table of its own: everything it says lands in
-- conversation_items (000004) carrying its run_id, in the same stream as the
-- user's words. The run keeps only the head of that stream —
-- transcript_through_seq — as the proof a terminal callback must name.
--
-- A daemon is a background process on a user's machine; it has no table of
-- its own: each runtime row carries the registering daemon's
-- persistent id as plain text. A runtime is one provider (claude | codex)
-- registered by one daemon in one company. A run is a single agent execution
-- claimed by a daemon via
--   UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)
-- with dispatched_at as the delivery-receipt CAS fence and a 45s prepare
-- lease renewed by the daemon until the run starts.
--
-- Structural edges carry FKs: ownership cascades, optional
-- pointers SET NULL. Usage facts (000007) stay FK-free so history survives
-- dimension deletion.

CREATE TABLE runtimes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    -- The registering daemon's persistent UUID (~/.zenith-team/daemon.id). Every
    -- runtime is registered by a machine, so this is the other half of the
    -- natural identity below and is never absent.
    daemon_id text NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    status text DEFAULT 'offline' NOT NULL,
    device_info text DEFAULT '' NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    -- Liveness column: written on registration and on every accepted
    -- heartbeat (the daemon beats each runtime every 15s, over its
    -- WebSocket when connected and over HTTP otherwise). There is no write
    -- throttle and no Redis liveness lane; this column is the only liveness
    -- authority. The sweeper marks a runtime offline when last_seen_at falls
    -- behind the 150s stale threshold.
    last_seen_at timestamp with time zone,
    -- Required to mint the brt_ run token at claim.
    owner_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT runtimes_pkey PRIMARY KEY (id),
    CONSTRAINT runtimes_company_id_id_key UNIQUE (company_id, id),
    -- Natural identity for daemon registration: RegisterRuntime upserts ON
    -- CONFLICT (company_id, daemon_id, provider). daemon_id is NOT NULL, so
    -- every registration is arbitrated here — none escapes the key by being
    -- unset.
    CONSTRAINT runtimes_company_daemon_provider_key UNIQUE (company_id, daemon_id, provider),
    CONSTRAINT runtimes_provider_check CHECK (provider IN ('claude', 'codex')),
    CONSTRAINT runtimes_status_check CHECK (status IN ('online', 'offline'))
);

CREATE INDEX runtimes_company_status_idx ON runtimes (company_id, status);
CREATE INDEX runtimes_last_seen_idx ON runtimes (last_seen_at);
CREATE INDEX runtimes_owner_company_idx
    ON runtimes (owner_user_id, company_id) WHERE owner_user_id IS NOT NULL;

-- Every agent is bound to the runtime that executes it: an agent can only
-- exist once a provider runtime (claude/codex) is registered in the company,
-- and a runtime with bound agents cannot be deleted (RESTRICT — rebind or
-- archive the agents first; the offline-runtime GC skips bound runtimes).
-- agents.provider is derived from the bound runtime at create/rebind time.
-- Declared here rather than in 000003_agents because runtimes must exist
-- before the column can reference them.
ALTER TABLE agents
    ADD COLUMN runtime_id uuid NOT NULL,
    ADD CONSTRAINT agents_company_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE RESTRICT;

CREATE INDEX agents_runtime_idx ON agents (runtime_id);

-- id is a bigserial, not a uuid: a run is an internal work record whose id is
-- only ever handed back to the caller that owns it, so it never needed to be
-- globally unguessable. EVERY writer lets the sequence allocate on INSERT —
-- queue promotion inserts the run first and lets the promoted item carry the
-- run_id back, and a human-input continuation does the same, stamping
-- continuation_run_id onto the Approval or Question it continues in the very
-- transaction that inserts the run. Nothing names a run before its row exists,
-- so no value is ever taken from runs_id_seq and then left naming nothing.
CREATE TABLE runs (
    id bigserial NOT NULL,
    -- No company edge: runs_company_conversation_fkey is composite and
    -- conversation_id is NOT NULL, so the company is already proven.
    company_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    -- The work item this run serves; NULL for direct-conversation runs.
    task_id uuid,
    conversation_id uuid NOT NULL,
    -- The agent's bound runtime (agents.runtime_id), copied onto the run at
    -- enqueue; the claim matches on it. There is no per-conversation runtime
    -- preference and no provider-wide fallback — the binding is the agent's,
    -- and it is what agents_company_runtime_fkey above makes mandatory, so an
    -- enqueue can never fail to find one. NOT NULL for the same reason. An
    -- offline runtime is fine: the run waits queued until its daemon returns.
    runtime_id uuid NOT NULL,
    status text DEFAULT 'queued' NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    -- No trigger_message_id: a run's input is its own first conversation_item
    -- of type 'user_message', reached by (run_id, seq). Enqueue idempotency is
    -- that item's request_key, which fences the send itself rather than a
    -- second copy of the edge.
    --
    -- Not every run has one. A retry child writes no words of its own — the
    -- user said them once, on the parent, and the child reaches them through
    -- parent_run_id — and an assignment run was never started on words at all.
    -- So the reads that follow this edge are all outer: a missing first
    -- 'user_message' means "this run has no initiator of its own", not
    -- "corrupt".
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    error text,
    -- Coarse failure classifier (pkg/runfailure vocabulary).
    failure_reason text,
    context jsonb,
    -- Provider-session resume pointer, pinned mid-run and finalized by the
    -- terminal callback.
    session_id text,
    session_rollout_missing boolean DEFAULT false NOT NULL,
    retired_session_id text,
    attempt integer DEFAULT 1 NOT NULL,
    -- No max_attempts column: the ceiling is a constant in
    -- internal/execution, read from the failure reason at the moment the
    -- retry decision is made. Storing it per row meant every run carried a
    -- copy of a number the code already knew.
    parent_run_id bigint,
    force_fresh_session boolean DEFAULT false NOT NULL,
    -- Hint while status = 'waiting_local_directory'; cleared on start/recover.
    wait_reason text,
    -- Claim -> start lease (45s), renewed by the daemon every ~15s during
    -- preparation. A live lease excludes the row from the dispatch-timeout
    -- sweep and the stale-dispatch reclaim.
    prepare_lease_expires_at timestamp with time zone,
    -- Deferred promotion time: status = 'deferred' rows become claimable when
    -- fire_at <= now().
    fire_at timestamp with time zone,
    -- A Task Run carries the generation it was created for. Direct and
    -- Schedule Runs leave it NULL.
    task_execution_generation bigint,
    -- The requested Connector set and the reviewed executable contract frozen
    -- at claim. Provider credentials and native configuration never enter
    -- PostgreSQL.
    connector_selection jsonb
        DEFAULT '{"schemaVersion":1,"mode":"all","connectors":[]}'::jsonb NOT NULL,
    connector_snapshot jsonb
        DEFAULT '{"schemaVersion":1,"connectors":[]}'::jsonb NOT NULL,
    -- NULL except for a Run materialized from one Schedule occurrence. The
    -- circular structural edge is added after schedule_firings exists below.
    schedule_firing_id uuid,
    -- Durable head of this run's slice of the conversation stream: the highest
    -- conversation seq this run has committed an item at, and the whole of the
    -- terminal proof. seq is the conversation-wide counter now, so this is
    -- bigint like the column it tracks. There is no companion chain digest —
    -- AppendRunItem holds the conversation row lock, StartRun is fenced by
    -- runtime_id, conversation_items rows are never UPDATEd, and (run_id,
    -- item_key) is unique, so two different histories cannot share a
    -- through_seq.
    transcript_through_seq bigint DEFAULT 0 NOT NULL,
    CONSTRAINT runs_pkey PRIMARY KEY (id),
    CONSTRAINT runs_company_id_id_key UNIQUE (company_id, id),
    -- runs_company_conversation_id_key is gone with the composite foreign key
    -- that was its only reader: conversation_items.run_id carries no FK, so a
    -- second unique index over (company_id, conversation_id, id) was a write
    -- cost on every insert proving something runs_pkey already proves.
    CONSTRAINT runs_company_agent_fkey FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_company_task_fkey FOREIGN KEY (company_id, task_id)
        REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id),
    CONSTRAINT runs_company_conversation_fkey FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_company_runtime_fkey FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    -- parent_run_id carries no foreign key: it could never fail, because
    -- CreateRetryRun copies the parent id out of the row it just read.
    CONSTRAINT runs_status_check CHECK (status IN (
        'queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled',
        'waiting_local_directory', 'deferred')),
    CONSTRAINT runs_connector_selection_check CHECK (
        jsonb_typeof(connector_selection) = 'object'
    ),
    CONSTRAINT runs_connector_snapshot_check CHECK (
        jsonb_typeof(connector_snapshot) = 'object'
    )
    -- The transcript head carries no CHECK. A non-negative counter is the
    -- shape of the value the execution module writes, not a vocabulary anyone
    -- can choose from, and a constraint name is not an error message a caller
    -- can act on.
);

-- Partial indexes here earn their place one way only: a small live subset of
-- a table that grows forever. Runs, approvals, and questions are never
-- deleted, so a sweep with no partial index rescans all history every 30
-- seconds. Where the live subset is already indexed for another reason, a
-- second partial index over the same rows is not written -- the queued TTL
-- sweep reads the claim index, and the running-run backstop reads the live
-- index -- because both sets are bounded by the capacity caps.
--
-- The claim hot path: queued candidates per runtime.
-- ClaimRun's inner SELECT leads on (agent_id, runtime_id) and orders by
-- (priority DESC, created_at, id). Neither of the two indexes that used to
-- serve it matched that: one leads on runtime_id with no agent_id, the other
-- on agent_id with a broader status predicate and no id in the sort key, so
-- the claim read and filtered rows it could have skipped. This one is the
-- query's own shape.
CREATE INDEX runs_claim_candidates_idx ON runs (agent_id, runtime_id, priority DESC, created_at, id)
    WHERE status = 'queued';
-- runs_pending_idx (agent_id, priority DESC, created_at) WHERE status IN
-- ('queued','dispatched') is gone. It was described as the claim inner select
-- plus capacity checks; the claim has had its own shape since
-- runs_claim_candidates_idx, and no query left in the set asks for an agent's
-- queued-and-dispatched work in priority order. The remaining agent-scoped
-- reads — LockActiveRunsForAgent (runs.sql) and the outcome rollup in
-- GetDepartmentInsightAggregate (department_insights.sql) — pin agent_id and a
-- status set with no ordering, which runs_agent_status_idx serves.
CREATE INDEX runs_agent_status_idx ON runs (agent_id, status);
-- The stale-dispatch reclaim path. updated_at is the fence: a dispatched row
-- is touched by the claim, by every prepare-lease renewal, and by the reclaim
-- itself, so "not touched in a recovery window" is exactly "the daemon holding
-- this claim went away". dispatched_at was a hand-rolled copy of that clock --
-- the reclaim had to SET it to now() to keep it honest.
CREATE INDEX runs_dispatched_prepare_idx ON runs (runtime_id, priority DESC, updated_at)
    WHERE status = 'dispatched';
CREATE INDEX runs_deferred_fire_idx ON runs (runtime_id, fire_at) WHERE status = 'deferred';
-- One pending or deferred run per (task, agent); NULL task_id rows
-- (direct-conversation runs) never collide because NULLs are distinct.
CREATE UNIQUE INDEX runs_one_pending_per_task_agent_idx ON runs (task_id, agent_id)
    WHERE status IN ('queued', 'deferred', 'dispatched');
-- runs_conversation_pending_idx (conversation_id, created_at DESC) over the
-- same live status set is gone. Its one query is GetConversationCurrentRun
-- (conversation_run_queue.sql), which takes the oldest live run of one
-- conversation; runs_conversation_live_idx has the identical partial predicate,
-- so it returns exactly the same rows and the sort is over the handful of runs
-- the one-run-at-a-time queue invariant allows to be live at once. The counters
-- that read the same set also need the status column to tell queued from
-- active, and only the live index carries it.
-- The conversation-list run counters. Keyed on status rather than created_at
-- so the count is index-only: the list asks "how many queued, how many active"
-- per conversation, and ordering is not part of that question. The predicate
-- matches the JOIN's status set verbatim — queued + deferred count as queued
-- work, the three executing states as active — which is what lets the
-- planner prove it applies.
CREATE INDEX runs_conversation_live_idx ON runs (conversation_id, status)
    WHERE status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running');
CREATE INDEX runs_conversation_created_idx ON runs (conversation_id, created_at DESC, id DESC);
CREATE INDEX runs_task_created_idx ON runs (task_id, created_at DESC) WHERE task_id IS NOT NULL;
-- User-plane retry receipts probe children by parent id. Only retry children
-- populate parent_run_id, so keep terminal/root history out of the index.
CREATE INDEX runs_parent_retry_idx ON runs (parent_run_id) WHERE parent_run_id IS NOT NULL;
-- runs_message_trigger_idx is gone with trigger_message_id: enqueue
-- idempotency is conversation_items_request_key_idx, which fences the send the
-- caller actually retried instead of a second copy of the edge.
-- There is no completed_at to index. A terminal run's finish time is
-- updated_at (the terminal transition is the row's last write), and the
-- authored history -- who ended it, why -- is the activity stream's.
CREATE INDEX runs_company_created_idx ON runs (company_id, created_at DESC, id DESC);
-- The company-wide live-run counter that bootstrap draws. runs_company_created_idx
-- has no status, so counting running runs meant scanning the company's whole
-- run history on the first screen of the app.
CREATE INDEX runs_company_live_idx ON runs (company_id, status)
    WHERE status IN ('queued', 'dispatched', 'waiting_local_directory', 'running');
CREATE INDEX runs_runtime_keyset_idx ON runs (runtime_id, id);
CREATE UNIQUE INDEX runs_schedule_firing_unique_idx
    ON runs (schedule_firing_id) WHERE schedule_firing_id IS NOT NULL;

-- A completed Task Run can create one proactive or explicit-review request
-- for its owning daemon to merge the exact execution generation. The
-- authorizing person and request provenance are captured for audit,
-- then rechecked when the job is claimed. Result acknowledgement remains
-- valid for the daemon that claimed the source Run even if Proactive Mode is
-- disabled while the local Git operation is already in flight.
CREATE TABLE task_merge_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id bigint NOT NULL,
    task_execution_generation bigint NOT NULL,
    authority_user_id uuid NOT NULL,
    request_type text DEFAULT 'proactive' NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    lease_expires_at timestamp with time zone,
    result_code text,
    merged_commit text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT task_merge_jobs_pkey PRIMARY KEY (id),
    CONSTRAINT task_merge_jobs_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT task_merge_jobs_run_key UNIQUE (run_id),
    CONSTRAINT task_merge_jobs_task_generation_key
        UNIQUE (task_id, task_execution_generation),
    CONSTRAINT task_merge_jobs_task_fkey
        FOREIGN KEY (company_id, task_id)
        REFERENCES tasks(company_id, id) ON DELETE CASCADE,
    CONSTRAINT task_merge_jobs_run_fkey
        FOREIGN KEY (company_id, run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT task_merge_jobs_status_check CHECK (
        status IN ('pending', 'executing', 'succeeded', 'blocked')
    ),
    CONSTRAINT task_merge_jobs_request_type_check CHECK (
        request_type IN ('proactive', 'manual')
    )
);

CREATE INDEX task_merge_jobs_due_idx
    ON task_merge_jobs (next_attempt_at, created_at, id)
    WHERE status IN ('pending', 'executing');

CREATE INDEX task_merge_jobs_company_task_created_idx
    ON task_merge_jobs (company_id, task_id, created_at DESC, id DESC);

-- There is no run_messages table. The transcript is the slice of
-- conversation_items (000004) carrying this run's run_id — minus the one
-- 'user_message' the run was started on, which carries the run_id too but is
-- the chat page's row, and which the run-detail queries exclude. It is read in
-- the same order, off the same counter, as the user's own words, and
-- conversation_items_run_item_key_idx is the replay fence that
-- run_messages_run_id_item_key_key used to be.

-- brt_ run tokens, minted at claim (24h), used by the agent process
-- (zenith-team-mcp) against the server; revoked when the run fails.
CREATE TABLE run_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- No key into runs: nothing deletes a run, and this table has no
    -- company_id, so the constraint enforced no tenant scope either.
    run_id bigint NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT run_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT run_tokens_token_hash_key UNIQUE (token_hash)
);

CREATE INDEX run_tokens_run_idx ON run_tokens (run_id);
CREATE INDEX run_tokens_expiry_idx ON run_tokens (expires_at);

-- Human-in-the-loop requests belong to the execution lifecycle: a running
-- Run raises one, reaches its ordinary terminal state, and a later terminal
-- human decision opens a durable continuation receipt that the reconciler
-- later turns into a fresh Run. The two request types intentionally keep
-- distinct tables and status vocabularies. Their Idempotency-Key slots are
-- operation-scoped, so the same UUID may key one Approval and one Question
-- raised by the same Run.
CREATE TABLE approvals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    type text DEFAULT 'company_plan_apply' NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    rule_key text NOT NULL,
    title text NOT NULL,
    summary text,
    preview_markdown text NOT NULL,
    presentation jsonb,
    requested_by_agent_id uuid NOT NULL,
    origin_run_id bigint NOT NULL,
    request_key uuid NOT NULL,
    task_id uuid,
    conversation_id uuid NOT NULL,
    -- Direct-conversation visibility is creator-only. Task origins carry no
    -- user shortcut and remain visible to the Company.
    origin_user_id uuid,
    canonical_action jsonb NOT NULL,
    request_sha256 bytea NOT NULL,
    action_sha256 bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    -- The decision's author is kept: it is the Approval's final state, not a
    -- history entry. When it was decided is updated_at read against status,
    -- and the sequence of transitions is the activity stream's.
    decision_note text,
    decided_by_user_id uuid,
    execution_status text,
    execution_result jsonb,
    skipped_reason text,
    -- Stamped by the continuation reconciler in the same transaction that
    -- INSERTs the Run and marks this receipt delivered, so a non-NULL value
    -- always names a Run that exists. NULL until then, and NULL forever if the
    -- continuation is abandoned — the shape check below is what enforces that.
    -- No FK: neither delete policy fits. SET NULL would contradict the
    -- delivered shape and CASCADE would erase a human decision along with a
    -- Run; the column is only ever written beside the INSERT that creates its
    -- target, and Runs are not deleted.
    continuation_run_id bigint,
    continuation_prompt text,
    continuation_status text,
    continuation_next_attempt_at timestamp with time zone,
    continuation_abandon_reason text,
    continuation_attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT approvals_pkey PRIMARY KEY (id),
    CONSTRAINT approvals_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT approvals_origin_run_id_request_key_key UNIQUE (origin_run_id, request_key),
    CONSTRAINT approvals_origin_run_fkey
        FOREIGN KEY (company_id, origin_run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT approvals_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT approvals_type_check CHECK (type IN (
        'company_plan_apply', 'commerce_manifest_publish'
    )),
    CONSTRAINT approvals_status_check CHECK (status IN (
        'pending', 'approved', 'rejected', 'cancelled', 'expired'
    )),
    CONSTRAINT approvals_execution_status_check CHECK (
        execution_status IS NULL OR execution_status IN ('executed', 'skipped')
    ),
    CONSTRAINT approvals_continuation_status_check CHECK (
        continuation_status IS NULL
        OR continuation_status IN ('pending', 'delivered', 'abandoned', 'not_required')
    ),
    CONSTRAINT approvals_origin_scope_check CHECK (
        (task_id IS NULL AND origin_user_id IS NOT NULL)
        OR (task_id IS NOT NULL AND origin_user_id IS NULL)
    ),
    CONSTRAINT approvals_presentation_shape_check CHECK (
        presentation IS NULL OR jsonb_typeof(presentation) = 'object'
    ),
    -- One invariant, not an enumeration: a pending Approval has no
    -- continuation, and a resolved one does. The correlations inside a
    -- continuation -- a delivered one names its Run, an abandoned one names a
    -- reason -- are written together by one transaction and belong to that
    -- writer. Spelling every combination out here restates the state machine
    -- in a language nothing can test, and every new continuation state means
    -- editing a CHECK instead of the code that owns the transition.
    CONSTRAINT approvals_continuation_presence_check CHECK (
        (status = 'pending') = (continuation_status IS NULL)
    )
);

CREATE INDEX approvals_company_created_idx
    ON approvals (company_id, created_at DESC, id DESC);
CREATE INDEX approvals_company_task_created_idx
    ON approvals (company_id, task_id, created_at DESC, id DESC) WHERE task_id IS NOT NULL;
CREATE INDEX approvals_pending_agent_idx
    ON approvals (requested_by_agent_id) WHERE status = 'pending';
CREATE INDEX approvals_expiry_idx
    ON approvals (expires_at, id) WHERE status = 'pending';
CREATE UNIQUE INDEX approvals_continuation_run_key
    ON approvals (continuation_run_id) WHERE continuation_run_id IS NOT NULL;
CREATE INDEX approvals_continuation_pending_idx
    ON approvals (continuation_next_attempt_at, id) WHERE continuation_status = 'pending';

CREATE TABLE questions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    status text DEFAULT 'pending' NOT NULL,
    title text NOT NULL,
    asked_by_agent_id uuid NOT NULL,
    origin_run_id bigint NOT NULL,
    request_key uuid NOT NULL,
    task_id uuid,
    conversation_id uuid NOT NULL,
    origin_user_id uuid,
    prompts jsonb NOT NULL,
    request_sha256 bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    -- Same rule as approvals: the answering user is final state, the time is
    -- updated_at read against status.
    answers jsonb,
    resolved_by_user_id uuid,
    -- Stamped exactly as the Approval continuation is: written by the
    -- reconciler in the transaction that INSERTs the Run, NULL before that and
    -- NULL forever if the continuation is abandoned.
    continuation_run_id bigint,
    continuation_prompt text,
    continuation_status text,
    continuation_next_attempt_at timestamp with time zone,
    continuation_abandon_reason text,
    continuation_attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT questions_pkey PRIMARY KEY (id),
    CONSTRAINT questions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT questions_origin_run_id_request_key_key UNIQUE (origin_run_id, request_key),
    CONSTRAINT questions_origin_run_fkey
        FOREIGN KEY (company_id, origin_run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT questions_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT questions_status_check CHECK (status IN (
        'pending', 'answered', 'withdrawn', 'expired'
    )),
    CONSTRAINT questions_continuation_status_check CHECK (
        continuation_status IS NULL OR continuation_status IN ('pending', 'delivered', 'abandoned')
    ),
    -- A Question belongs either to a Task (any member may answer) or to one
    -- user's direct conversation (only they may). Who may answer is that
    -- choice read back, not a third column to keep in step.
    CONSTRAINT questions_origin_scope_check CHECK (
        (task_id IS NULL) = (origin_user_id IS NOT NULL)
    ),
    -- The same one invariant approvals carries, for the same reason.
    CONSTRAINT questions_continuation_presence_check CHECK (
        (status = 'pending') = (continuation_status IS NULL)
    )
);

CREATE INDEX questions_company_created_idx
    ON questions (company_id, created_at DESC, id DESC);
CREATE INDEX questions_company_task_created_idx
    ON questions (company_id, task_id, created_at DESC, id DESC) WHERE task_id IS NOT NULL;
CREATE INDEX questions_company_origin_user_created_idx
    ON questions (company_id, origin_user_id, created_at DESC, id DESC)
    WHERE origin_user_id IS NOT NULL;
CREATE INDEX questions_expiry_idx
    ON questions (expires_at, id) WHERE status = 'pending';
CREATE UNIQUE INDEX questions_continuation_run_key
    ON questions (continuation_run_id) WHERE continuation_run_id IS NOT NULL;
CREATE INDEX questions_continuation_pending_idx
    ON questions (continuation_next_attempt_at, id) WHERE continuation_status = 'pending';

-- Provider-native Connector and Plugin discovery is retained per Runtime.
-- These rows contain presentation metadata and reviewed tool namespaces only;
-- provider credentials, commands, environment values, and install paths stay
-- daemon-local.
CREATE TABLE connector_catalog_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    provider text NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    name text NOT NULL,
    description text,
    category text,
    icon_url text,
    install_count bigint,
    status text NOT NULL,
    action text NOT NULL,
    can_execute boolean DEFAULT false NOT NULL,
    contract_digest text,
    tool_namespace jsonb,
    observed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT connector_catalog_items_pkey PRIMARY KEY (id),
    CONSTRAINT connector_catalog_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT connector_catalog_items_company_provider_source_native_key
        UNIQUE (company_id, provider, source, native_id),
    CONSTRAINT connector_catalog_items_provider_check
        CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT connector_catalog_items_source_check CHECK (
        source IN (
            'codex_app', 'codex_mcp', 'claude_plugin', 'claude_mcp',
            'claude_cloud'
        )
    ),
    CONSTRAINT connector_catalog_items_status_check CHECK (
        status IN (
            'available', 'installed_not_ready', 'needs_auth', 'connected',
            'managed_in_provider', 'unavailable', 'error'
        )
    ),
    CONSTRAINT connector_catalog_items_action_check CHECK (
        action IN ('install_and_auth', 'authenticate', 'manage_in_provider', 'none')
    ),
    CONSTRAINT connector_catalog_items_category_check CHECK (
        category IS NULL OR category IN (
            'Productivity', 'Creativity', 'Developer Tools',
            'Business & Operations', 'Data & Analytics', 'Communication',
            'Education & Research', 'Security', 'Finance', 'Healthcare',
            'Travel', 'Entertainment', 'Other'
        )
    ),
    CONSTRAINT connector_catalog_items_contract_check CHECK (
        (
            can_execute
            AND contract_digest IS NOT NULL
            AND tool_namespace IS NOT NULL
            AND jsonb_typeof(tool_namespace) = 'array'
        )
        OR (
            NOT can_execute
            AND contract_digest IS NULL
            AND tool_namespace IS NULL
        )
    )
);

CREATE INDEX connector_catalog_items_company_provider_name_idx
    ON connector_catalog_items (company_id, provider, lower(name), id);
CREATE INDEX connector_catalog_items_company_provider_status_idx
    ON connector_catalog_items (company_id, provider, status);
CREATE INDEX connector_catalog_items_company_provider_category_idx
    ON connector_catalog_items (company_id, provider, category)
    WHERE category IS NOT NULL;

CREATE TABLE connector_policies (
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    desired_enabled boolean DEFAULT false NOT NULL,
    reviewed_contract_digest text,
    review_required boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT connector_policies_pkey PRIMARY KEY (connector_id),
    CONSTRAINT connector_policies_connector_fkey
        FOREIGN KEY (company_id, connector_id)
        REFERENCES connector_catalog_items(company_id, id) ON DELETE CASCADE
);

CREATE INDEX connector_policies_company_idx
    ON connector_policies (company_id, connector_id);

CREATE TABLE connector_discovery_generations (
    runtime_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    manifest_digest text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT connector_discovery_generations_pkey PRIMARY KEY (runtime_id),
    CONSTRAINT connector_discovery_generations_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT connector_discovery_generations_provider_check
        CHECK (provider IN ('codex', 'claude'))
);

CREATE INDEX connector_discovery_generations_company_provider_idx
    ON connector_discovery_generations (company_id, provider, observed_at DESC);

CREATE TABLE connector_runtime_observations (
    runtime_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    name text NOT NULL,
    description text,
    category text,
    icon_url text,
    install_count bigint,
    status text NOT NULL,
    action text NOT NULL,
    can_execute boolean DEFAULT false NOT NULL,
    contract_digest text,
    tool_namespace jsonb,
    observed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT connector_runtime_observations_pkey PRIMARY KEY (runtime_id, connector_id),
    CONSTRAINT connector_runtime_observations_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT connector_runtime_observations_connector_fkey
        FOREIGN KEY (company_id, connector_id)
        REFERENCES connector_catalog_items(company_id, id) ON DELETE CASCADE,
    CONSTRAINT connector_runtime_observations_provider_check
        CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT connector_runtime_observations_status_check CHECK (
        status IN (
            'available', 'installed_not_ready', 'needs_auth', 'connected',
            'managed_in_provider', 'unavailable', 'error'
        )
    ),
    CONSTRAINT connector_runtime_observations_action_check CHECK (
        action IN ('install_and_auth', 'authenticate', 'manage_in_provider', 'none')
    ),
    CONSTRAINT connector_runtime_observations_category_check CHECK (
        category IS NULL OR category IN (
            'Productivity', 'Creativity', 'Developer Tools',
            'Business & Operations', 'Data & Analytics', 'Communication',
            'Education & Research', 'Security', 'Finance', 'Healthcare',
            'Travel', 'Entertainment', 'Other'
        )
    ),
    CONSTRAINT connector_runtime_observations_contract_check CHECK (
        (
            can_execute
            AND contract_digest IS NOT NULL
            AND tool_namespace IS NOT NULL
            AND jsonb_typeof(tool_namespace) = 'array'
        )
        OR (
            NOT can_execute
            AND contract_digest IS NULL
            AND tool_namespace IS NULL
        )
    )
);

CREATE INDEX connector_runtime_observations_connector_idx
    ON connector_runtime_observations (connector_id, observed_at DESC, runtime_id);
CREATE INDEX connector_runtime_observations_company_provider_idx
    ON connector_runtime_observations (company_id, provider, runtime_id);

CREATE TABLE plugin_catalog_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    provider text NOT NULL,
    native_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT plugin_catalog_items_pkey PRIMARY KEY (id),
    CONSTRAINT plugin_catalog_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT plugin_catalog_items_company_provider_native_key
        UNIQUE (company_id, provider, native_id),
    CONSTRAINT plugin_catalog_items_provider_check
        CHECK (provider IN ('codex', 'claude'))
);

CREATE INDEX plugin_catalog_items_company_provider_idx
    ON plugin_catalog_items (company_id, provider, id);

CREATE TABLE plugin_discovery_generations (
    runtime_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    manifest_digest text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT plugin_discovery_generations_pkey PRIMARY KEY (runtime_id),
    CONSTRAINT plugin_discovery_generations_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT plugin_discovery_generations_provider_check
        CHECK (provider IN ('codex', 'claude'))
);

CREATE INDEX plugin_discovery_generations_company_provider_idx
    ON plugin_discovery_generations (company_id, provider, observed_at DESC);

CREATE TABLE plugin_runtime_observations (
    runtime_id uuid NOT NULL,
    plugin_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    name text NOT NULL,
    description text,
    category text NOT NULL,
    icon_url text,
    share_url text,
    status text NOT NULL,
    installed boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    featured boolean DEFAULT false NOT NULL,
    action text NOT NULL,
    catalog_rank integer NOT NULL,
    app_count integer DEFAULT 0 NOT NULL,
    mcp_server_count integer DEFAULT 0 NOT NULL,
    skill_count integer DEFAULT 0 NOT NULL,
    install_count bigint,
    connector_identities jsonb DEFAULT '[]'::jsonb NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT plugin_runtime_observations_pkey PRIMARY KEY (runtime_id, plugin_id),
    CONSTRAINT plugin_runtime_observations_runtime_rank_key
        UNIQUE (runtime_id, catalog_rank) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT plugin_runtime_observations_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT plugin_runtime_observations_plugin_fkey
        FOREIGN KEY (company_id, plugin_id)
        REFERENCES plugin_catalog_items(company_id, id) ON DELETE CASCADE,
    CONSTRAINT plugin_runtime_observations_provider_check
        CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT plugin_runtime_observations_status_check
        CHECK (status IN ('available', 'installed', 'disabled', 'error')),
    CONSTRAINT plugin_runtime_observations_action_check
        CHECK (action IN ('install', 'uninstall', 'none')),
    CONSTRAINT plugin_runtime_observations_category_check CHECK (
        category IN (
            'Productivity', 'Creativity', 'Developer Tools',
            'Business & Operations', 'Data & Analytics', 'Communication',
            'Education & Research', 'Security', 'Finance', 'Healthcare',
            'Travel', 'Entertainment', 'Other'
        )
    ),
    CONSTRAINT plugin_runtime_observations_connector_identities_check CHECK (
        jsonb_typeof(connector_identities) = 'array'
    )
);

CREATE INDEX plugin_runtime_observations_company_provider_runtime_idx
    ON plugin_runtime_observations (
        company_id, provider, runtime_id, catalog_rank, plugin_id
    );
CREATE INDEX plugin_runtime_observations_plugin_idx
    ON plugin_runtime_observations (plugin_id, observed_at DESC, runtime_id);

-- Server-authoritative one-shot and recurring Schedules. PostgreSQL owns due
-- time, occurrence replay, and the linkage from an occurrence to its Run.
CREATE TABLE schedules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL,
    creator_user_id uuid NOT NULL,
    -- Caller-owned replay fence. Schedule and Conversation identities remain
    -- database-generated and are returned by the insert.
    request_key uuid NOT NULL,
    agent_id uuid NOT NULL,
    reviewed_provider text NOT NULL,
    name text NOT NULL,
    instructions text NOT NULL,
    preset_key text,
    trigger_type text NOT NULL,
    run_at timestamp with time zone,
    cron_expression text,
    timezone text,
    next_occurrence_at timestamp with time zone,
    last_occurrence_at timestamp with time zone,
    status text DEFAULT 'draft' NOT NULL,
    pause_reason text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT schedules_pkey PRIMARY KEY (id),
    CONSTRAINT schedules_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT schedules_conversation_key UNIQUE (conversation_id),
    CONSTRAINT schedules_company_creator_request_key
        UNIQUE (company_id, creator_user_id, request_key),
    CONSTRAINT schedules_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT schedules_agent_fkey
        FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id),
    CONSTRAINT schedules_provider_check
        CHECK (reviewed_provider IN ('codex', 'claude')),
    CONSTRAINT schedules_trigger_type_check CHECK (trigger_type IN ('once', 'cron')),
    CONSTRAINT schedules_trigger_shape_check CHECK (
        (
            trigger_type = 'once'
            AND run_at IS NOT NULL
            AND cron_expression IS NULL
            AND timezone IS NULL
        )
        OR (
            trigger_type = 'cron'
            AND run_at IS NULL
            AND cron_expression IS NOT NULL
            AND timezone IS NOT NULL
        )
    ),
    CONSTRAINT schedules_status_check CHECK (
        status IN ('draft', 'active', 'paused', 'completed', 'cancelled')
    ),
    CONSTRAINT schedules_occurrence_state_check CHECK (
        (status IN ('draft', 'completed', 'cancelled') AND next_occurrence_at IS NULL)
        OR (status IN ('active', 'paused') AND next_occurrence_at IS NOT NULL)
    ),
    CONSTRAINT schedules_pause_reason_check CHECK (
        (status = 'paused') = (pause_reason IS NOT NULL)
    )
);

CREATE INDEX schedules_creator_list_idx
    ON schedules (company_id, creator_user_id, created_at DESC, id DESC);
CREATE INDEX schedules_due_idx
    ON schedules (next_occurrence_at, id) WHERE status = 'active';
CREATE INDEX schedules_agent_idx
    ON schedules (company_id, agent_id)
    WHERE status IN ('draft', 'active', 'paused');
CREATE UNIQUE INDEX schedules_creator_preset_key
    ON schedules (company_id, creator_user_id, preset_key)
    WHERE preset_key IS NOT NULL AND status <> 'cancelled';

ALTER TABLE conversations
    ADD CONSTRAINT conversations_schedule_id_fkey
        FOREIGN KEY (company_id, schedule_id)
        REFERENCES schedules(company_id, id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE schedule_sources (
    schedule_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    reviewed_contract_digest text NOT NULL,
    policy_revision bigint NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT schedule_sources_pkey PRIMARY KEY (schedule_id, connector_id),
    CONSTRAINT schedule_sources_schedule_position_key UNIQUE (schedule_id, "position"),
    CONSTRAINT schedule_sources_schedule_fkey
        FOREIGN KEY (company_id, schedule_id)
        REFERENCES schedules(company_id, id) ON DELETE CASCADE,
    CONSTRAINT schedule_sources_connector_fkey
        FOREIGN KEY (company_id, connector_id)
        REFERENCES connector_catalog_items(company_id, id),
    CONSTRAINT schedule_sources_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT schedule_sources_source_check CHECK (
        source IN ('codex_app', 'claude_plugin', 'claude_mcp')
    ),
    CONSTRAINT schedule_sources_provider_source_check CHECK (
        (provider = 'codex' AND source = 'codex_app')
        OR (provider = 'claude' AND source IN ('claude_plugin', 'claude_mcp'))
    )
);

CREATE INDEX schedule_sources_company_connector_idx
    ON schedule_sources (company_id, connector_id);

CREATE TABLE schedule_firings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    schedule_id uuid NOT NULL,
    schedule_version bigint NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid,
    run_id bigint,
    occurrence_at timestamp with time zone NOT NULL,
    trigger text NOT NULL,
    -- Manual Run-now calls carry a caller replay fence. Scheduled firings use
    -- their occurrence timestamp as the replay fence.
    request_key uuid,
    disposition text NOT NULL,
    reason text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT schedule_firings_pkey PRIMARY KEY (id),
    CONSTRAINT schedule_firings_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT schedule_firings_message_key UNIQUE (message_id),
    CONSTRAINT schedule_firings_run_key UNIQUE (run_id),
    CONSTRAINT schedule_firings_schedule_fkey
        FOREIGN KEY (company_id, schedule_id)
        REFERENCES schedules(company_id, id) ON DELETE CASCADE,
    CONSTRAINT schedule_firings_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT schedule_firings_message_fkey
        FOREIGN KEY (company_id, message_id)
        REFERENCES conversation_items(company_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT schedule_firings_run_fkey
        FOREIGN KEY (company_id, run_id)
        REFERENCES runs(company_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT schedule_firings_trigger_check
        CHECK (trigger IN ('scheduled', 'manual')),
    CONSTRAINT schedule_firings_request_key_check CHECK (
        (trigger = 'scheduled' AND request_key IS NULL)
        OR (trigger = 'manual' AND request_key IS NOT NULL)
    ),
    CONSTRAINT schedule_firings_disposition_check CHECK (
        disposition IN ('materialized', 'superseded', 'skipped', 'needs_attention')
    ),
    CONSTRAINT schedule_firings_materialization_check CHECK (
        (message_id IS NULL) = (run_id IS NULL)
        AND (disposition <> 'materialized' OR run_id IS NOT NULL)
        AND (disposition <> 'superseded' OR run_id IS NOT NULL)
    ),
    CONSTRAINT schedule_firings_reason_check CHECK (
        (disposition = 'materialized') = (reason IS NULL)
    )
);

CREATE UNIQUE INDEX schedule_firings_scheduled_occurrence_key
    ON schedule_firings (schedule_id, occurrence_at)
    WHERE trigger = 'scheduled';
CREATE UNIQUE INDEX schedule_firings_manual_request_key
    ON schedule_firings (schedule_id, request_key)
    WHERE request_key IS NOT NULL;
CREATE INDEX schedule_firings_schedule_history_idx
    ON schedule_firings (schedule_id, occurrence_at DESC, id DESC);

ALTER TABLE runs
    ADD CONSTRAINT runs_schedule_firing_fkey
        FOREIGN KEY (company_id, schedule_firing_id)
        REFERENCES schedule_firings(company_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT runs_schedule_connector_selection_check CHECK (
        schedule_firing_id IS NULL
        OR ((
            connector_selection ->> 'schemaVersion' = '1'
            AND connector_selection ->> 'mode' = 'exact'
            AND jsonb_typeof(connector_selection -> 'connectors') = 'array'
        ) IS TRUE)
    );

-- Durable upward Task outcome reports and the confirmed batches that deliver
-- them into a manager Agent conversation.
CREATE TABLE task_report_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- The owning Conversation composite FK proves Company scope and owns
    -- deletion. Avoid a late direct Company lock after Conversation/Run locks.
    company_id uuid NOT NULL,
    recipient_agent_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    confirmed_by_user_id uuid,
    delivery_type text DEFAULT 'manual' NOT NULL,
    automation_user_id uuid,
    request_key uuid NOT NULL,
    message_id uuid NOT NULL,
    run_id bigint NOT NULL,
    event_count integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_report_batches_pkey PRIMARY KEY (id),
    CONSTRAINT task_report_batches_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT task_report_batches_message_id_key UNIQUE (message_id),
    CONSTRAINT task_report_batches_run_id_key UNIQUE (run_id),
    CONSTRAINT task_report_batches_delivery_type_check CHECK (
        delivery_type IN ('manual', 'proactive')
    ),
    CONSTRAINT task_report_batches_delivery_authority_check CHECK (
        (delivery_type = 'manual' AND confirmed_by_user_id IS NOT NULL AND automation_user_id IS NULL)
        OR
        (delivery_type = 'proactive' AND confirmed_by_user_id IS NULL AND automation_user_id IS NOT NULL)
    ),
    CONSTRAINT task_report_batches_recipient_agent_fkey
        FOREIGN KEY (company_id, recipient_agent_id)
        REFERENCES agents(company_id, id),
    CONSTRAINT task_report_batches_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT task_report_batches_message_fkey
        FOREIGN KEY (company_id, message_id)
        REFERENCES conversation_items(company_id, id),
    CONSTRAINT task_report_batches_run_fkey
        FOREIGN KEY (company_id, run_id)
        REFERENCES runs(company_id, id)
);

CREATE INDEX task_report_batches_company_created_idx
    ON task_report_batches (company_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX task_report_batches_manual_request_key
    ON task_report_batches (company_id, confirmed_by_user_id, request_key)
    WHERE delivery_type = 'manual';

CREATE UNIQUE INDEX task_report_batches_proactive_request_key
    ON task_report_batches (company_id, automation_user_id, request_key)
    WHERE delivery_type = 'proactive';

CREATE TABLE task_report_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- The owning Task composite FK proves Company scope and owns deletion.
    -- A terminal Run reaches this insert after Conversation/Run/Task locks.
    company_id uuid NOT NULL,
    task_id uuid NOT NULL,
    task_version bigint NOT NULL,
    task_key text NOT NULL,
    task_title text NOT NULL,
    source_agent_id uuid NOT NULL,
    recipient_agent_id uuid NOT NULL,
    department_key text,
    completion_source text NOT NULL,
    completed_at timestamp with time zone NOT NULL,
	result_summary text DEFAULT '' NOT NULL,
	-- Agent-authored business failures are replay-fenced by the exact live Run
	-- and request key that produced them. Other outcome sources have no Run
	-- credential and therefore leave both attribution fields NULL.
	source_run_id bigint,
	request_key uuid,
	batch_id uuid,
    superseded_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_report_events_pkey PRIMARY KEY (id),
    CONSTRAINT task_report_events_task_version_key UNIQUE (task_id, task_version),
    CONSTRAINT task_report_events_task_fkey
        FOREIGN KEY (company_id, task_id)
        REFERENCES tasks(company_id, id) ON DELETE CASCADE,
    CONSTRAINT task_report_events_batch_fkey
        FOREIGN KEY (company_id, batch_id)
        REFERENCES task_report_batches(company_id, id)
        ON DELETE SET NULL (batch_id),
	CONSTRAINT task_report_events_completion_source_check CHECK (
		completion_source IN (
			'manual', 'review_accepted', 'agent_business_failure'
		)
	),
	CONSTRAINT task_report_events_failure_origin_check CHECK (
		(
			completion_source = 'agent_business_failure'
			AND source_run_id IS NOT NULL
			AND request_key IS NOT NULL
		)
		OR (
			completion_source <> 'agent_business_failure'
			AND source_run_id IS NULL
			AND request_key IS NULL
		)
	)
);

CREATE INDEX task_report_events_pending_recipient_idx
    ON task_report_events (company_id, recipient_agent_id, created_at, id)
    WHERE batch_id IS NULL AND superseded_at IS NULL;
CREATE INDEX task_report_events_batch_idx
	ON task_report_events (batch_id, created_at, id)
	WHERE batch_id IS NOT NULL;
CREATE UNIQUE INDEX task_report_events_failure_request_key
	ON task_report_events (source_run_id, request_key)
	WHERE source_run_id IS NOT NULL;
