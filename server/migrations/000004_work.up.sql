-- 000004_work: the product work surface — attachments, conversations, the
-- immutable conversation stream, tasks, and task dependencies.
--
-- Shape notes:
--   conversations absorbs the old conversation_reads creator state
--   (pinned_at / archived_at — creator-scoped, direct conversations only; task
--   conversations carry no read tracking, unread is the inbox's job).
--   conversation_items is the one stream: the user's words and everything the
--   agent produced answering them live in a single ordered table, so the chat
--   page and a run's transcript are two reads of the same rows rather than two
--   copies that have to be kept in step.

CREATE TABLE attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    uploaded_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    object_key text GENERATED ALWAYS AS (
        'companies/' || company_id::text || '/attachments/' || id::text
    ) STORED NOT NULL,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    upload_lease_expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT attachments_pkey PRIMARY KEY (id),
    CONSTRAINT attachments_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT attachments_create_request_key
        UNIQUE (company_id, uploaded_by_user_id, request_key),
    CONSTRAINT attachments_object_key_key UNIQUE (object_key),
    CONSTRAINT attachments_status_check CHECK (status IN ('pending', 'ready', 'deleting', 'deleted'))
);

CREATE INDEX attachments_deleting_retry_cleanup_idx ON attachments USING btree (updated_at, id)
    WHERE (status = 'deleting');

CREATE INDEX attachments_pending_expiry_cleanup_idx ON attachments USING btree (upload_lease_expires_at, id)
    WHERE (status = 'pending');

CREATE INDEX attachments_ready_abandonment_cleanup_idx ON attachments USING btree (updated_at, id)
    WHERE (status = 'ready');

-- Founder-supplied reference documents: the files a founder hands the company
-- at onboarding so the CEO Agent can read them before interviewing. They are
-- Company-owned rather than attached to one message, because they are source
-- material every later Run may need, not the payload of a single turn.
-- Holding the Attachment here also keeps it out of the abandoned-upload sweep.
--
-- Shaped like message_attachments: the Attachment is the key, and company_id
-- exists only to carry the composite edge, so an Attachment cannot be claimed
-- by a Company that does not own it. Who uploaded it and when already live on
-- the Attachment.
CREATE TABLE company_reference_documents (
    company_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT company_reference_documents_pkey PRIMARY KEY (attachment_id),
    CONSTRAINT company_reference_documents_attachment_fkey
        FOREIGN KEY (company_id, attachment_id)
        REFERENCES attachments (company_id, id) ON DELETE CASCADE
);

CREATE INDEX company_reference_documents_company_idx
    ON company_reference_documents USING btree (company_id, created_at, attachment_id);

-- task_id gains its FK after tasks exists (circular edge, ALTER below).
CREATE TABLE conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    kind text NOT NULL,
    task_id uuid,
    schedule_id uuid,
    agent_id uuid,
    created_by_user_id uuid,
    title text,
    request_key uuid,
    CONSTRAINT conversations_request_key_kind_check CHECK (request_key IS NULL OR kind = 'direct'),

    -- Creator-scoped filing state for a direct conversation. There is no read
    -- watermark: being told about a conversation is the Inbox's job, and it
    -- carries its own per-recipient read state.
    pinned_at timestamp with time zone,
    archived_at timestamp with time zone,

    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT conversations_pkey PRIMARY KEY (id),
    CONSTRAINT conversations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversations_agent_fkey
        FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id)
        ON DELETE SET NULL (agent_id),
    CONSTRAINT conversations_kind_check CHECK (kind IN ('task', 'direct', 'schedule')),
    CONSTRAINT conversations_shape_check CHECK (
        (
            kind = 'task'
            AND schedule_id IS NULL
            AND agent_id IS NULL
            AND created_by_user_id IS NULL
        )
        OR
        (
            kind = 'direct'
            AND task_id IS NULL
            AND schedule_id IS NULL
            AND created_by_user_id IS NOT NULL
        )
        OR
        (
            kind = 'schedule'
            AND task_id IS NULL
            AND schedule_id IS NOT NULL
            AND agent_id IS NOT NULL
            AND created_by_user_id IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX conversations_task_unique_idx ON conversations USING btree (task_id) WHERE task_id IS NOT NULL;

CREATE UNIQUE INDEX conversations_schedule_unique_idx ON conversations USING btree (schedule_id) WHERE schedule_id IS NOT NULL;

CREATE INDEX conversations_direct_creator_list_idx ON conversations USING btree (company_id, created_by_user_id, updated_at DESC, id DESC)
    WHERE (kind = 'direct');

CREATE INDEX conversations_task_list_idx ON conversations USING btree (company_id, updated_at DESC, id DESC)
    WHERE (kind = 'task');

CREATE INDEX conversations_schedule_creator_list_idx
    ON conversations (company_id, created_by_user_id, updated_at DESC, id DESC)
    WHERE kind = 'schedule';

-- No index on agent_id alone: every read that filters by agent also pins the
-- company, which conversations_active_direct_unique_idx and the direct-list
-- index already lead with.

CREATE UNIQUE INDEX conversations_active_direct_unique_idx
    ON conversations (company_id, created_by_user_id, agent_id)
    WHERE kind = 'direct' AND archived_at IS NULL AND agent_id IS NOT NULL AND request_key IS NULL;

CREATE UNIQUE INDEX conversations_request_key_unique_idx
    ON conversations (company_id, created_by_user_id, request_key)
    WHERE request_key IS NOT NULL;

-- Viewer-private task-chat filing; direct chat filing remains on its creator's row.
CREATE TABLE conversation_user_states (
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pinned_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    PRIMARY KEY (conversation_id, user_id),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE
);
CREATE INDEX conversation_user_states_pins_idx
    ON conversation_user_states(company_id,user_id,pinned_at DESC) WHERE pinned_at IS NOT NULL;

-- Proactive Mode remembers the owner's exact root-Agent conversation. The
-- Conversation must belong to this Company; deleting it clears only the
-- optional pointer so every automation authority check fails closed.
ALTER TABLE companies
    ADD CONSTRAINT companies_proactive_mode_conversation_fkey
        FOREIGN KEY (id, proactive_mode_conversation_id)
        REFERENCES conversations(company_id, id)
        ON DELETE SET NULL (proactive_mode_conversation_id);

-- The one conversation stream. A turn's user words, the agent's answer, and
-- the reasoning, tool calls, tool results and errors that produced it are all
-- rows here, ordered by one counter.
--
-- What a reader is shown is derived from `type`, never stored as a flag:
--     chat timeline  = one Conversation, type IN ('user_message', 'agent_text')
--     transcript     = one Conversation, every type except 'user_message',
--                      with run_id preserving each provider item's producer
-- The two overlap on 'agent_text' and are meant to: the answer is the chat's
-- message and its Run's output, read twice rather than stored twice — which is
-- the whole point of one stream. What each surface drops is the other's alone.
-- There is deliberately no visibility column, because a column is something a
-- writer can set wrong, and the way to set it wrong is to leak an agent's raw
-- tool output into the user's chat.
--
-- run_id is NOT NULL: a Conversation only accepts a turn when it has an Agent,
-- so every item was either consumed or produced by exactly one Run. It carries
-- no foreign key on purpose. It labels the Run that handled the item; it does
-- not say the Run owns it. An ownership edge here would mean removing a Run row
-- removes the user's own words with it.
CREATE TABLE conversation_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- No company edge: conversation_items_conversation_fkey below is composite
    -- over (company_id, conversation_id), so it already proves the company and
    -- already cascades. A second single-column key would add a lock on the
    -- companies row to every insert and no guarantee. See data.md.
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    run_id bigint NOT NULL,
    -- The ordering authority, allocated under the Conversation row lock. The
    -- chat page and a Run transcript are two reads of this one counter, so
    -- they can never disagree about what came first.
    seq bigint NOT NULL,
    type text NOT NULL,
    author_kind text NOT NULL,
    author_id uuid NOT NULL,
    -- The provider's own identity for the item, so a re-sent append is
    -- recognised as the row already stored rather than appended twice. NULL
    -- for user items: the user's turn has no provider identity.
    item_key text,
    -- Public sends use a request key for exact replay without exposing the
    -- database-owned item id. Internal prompts and agent items leave it NULL
    -- so they never occupy a caller's replay slot.
    request_key uuid,
    -- The plain-text rendering every reader shares: search, list previews, and
    -- the user's own words. A structured item keeps its shape in `parts` and
    -- still renders here.
    content text DEFAULT '' NOT NULL,
    -- The provider item's structure, as a non-empty array. NULL exactly on the
    -- user's own turn, which has no provider shape — so a Run's items are the
    -- ones with parts, and its triggering 'user_message' is the one without.
    -- The run-detail reads say that in the vocabulary the rest of the schema
    -- uses (type <> 'user_message') rather than by testing for NULL here.
    parts jsonb,
    -- Provider items only: the PER-ITEM digest, which lets an append compare
    -- content against the row already stored under the same item_key and so
    -- tell a harmless replay from an attempt to rewrite committed history. It
    -- feeds no chain — runs.transcript_sha256 and the digest the daemon echoed
    -- back are gone on purpose and should not come back.
    content_sha256 bytea,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    -- No updated_at: an item is immutable. Nothing in the query set updates
    -- one, and the wire says so.
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT conversation_items_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversation_items_conversation_id_seq_key UNIQUE (conversation_id, seq),
    CONSTRAINT conversation_items_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT conversation_items_type_check CHECK (type IN (
        'user_message', 'agent_text', 'reasoning', 'tool_call', 'tool_result', 'error'
    )),
    CONSTRAINT conversation_items_author_kind_check CHECK (author_kind IN ('user', 'agent'))
);

-- The provider replay fence. Re-appending an item after a reconnect is the
-- normal case, and this is what makes the second copy fail instead of showing
-- the user the agent's answer twice.
CREATE UNIQUE INDEX conversation_items_run_item_key_idx
    ON conversation_items (run_id, item_key) WHERE item_key IS NOT NULL;

-- Exact send replay: the same request key from the same author in the same
-- Conversation resolves to the row it already wrote.
CREATE UNIQUE INDEX conversation_items_request_key_idx
    ON conversation_items (company_id, conversation_id, author_id, request_key)
    WHERE request_key IS NOT NULL;

-- Timeline paging. conversation_items_conversation_id_seq_key spans the same
-- columns but every item in the Conversation, and one busy Run puts more tool
-- and reasoning items between two visible ones than a page of chat holds — so
-- paging the chat off the full index reads mostly rows it must then discard.
CREATE INDEX conversation_items_timeline_idx
    ON conversation_items (conversation_id, seq)
    WHERE type IN ('user_message', 'agent_text');

-- One Run's slice of the stream in order. It serves the run detail view, the
-- Task workspace's per-Run timeline, and every "what did this Run produce"
-- probe — including the ones that read the single 'user_message' the Run was
-- started on (its trigger prompt, its initiator, the authored-run lineage the
-- user's cancel walks), which is why the type is not in the key: those probes
-- want the first row, the detail reads want everything after it, and both are
-- one ordered seek from here.
CREATE INDEX conversation_items_run_seq_idx ON conversation_items (run_id, seq);

-- Search reads what a person can see, so its predicate is the timeline's.
CREATE INDEX conversation_items_content_search_idx
    ON conversation_items USING gin (to_tsvector('simple', content))
    WHERE content <> '' AND type IN ('user_message', 'agent_text');

-- Files ride a user's turn, so the parent here is always the conversation_item
-- of type 'user_message' that the person sent — hence the column name.
CREATE TABLE message_attachments (
    -- Both composite keys below carry company_id into a parent that has its
    -- own company edge, so this column needs no key of its own.
    company_id uuid NOT NULL,
    message_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT message_attachments_pkey PRIMARY KEY (attachment_id),
    CONSTRAINT message_attachments_message_id_position_key UNIQUE (message_id, "position"),
    CONSTRAINT message_attachments_message_fkey
        FOREIGN KEY (company_id, message_id)
        REFERENCES conversation_items(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT message_attachments_attachment_fkey
        FOREIGN KEY (company_id, attachment_id)
        REFERENCES attachments(company_id, id)
        ON DELETE CASCADE
);

-- Deferred user prompts are not conversation items or Runs until they reach
-- the head of the Conversation queue. message_id is reserved as the future
-- immutable item id, which makes admission and exact replay durable without
-- assigning the item's seq before reorder settles. The Run id is no longer
-- reserved: runs.id is a bigserial the promotion transaction takes by
-- inserting the Run first, and the item then carries that run_id.
CREATE TABLE conversation_run_queue_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL,
    -- Reserved future immutable conversation_items id (the 'user_message' this
    -- prompt becomes) — no row exists yet, so deliberately no FK.
    message_id uuid DEFAULT gen_random_uuid() NOT NULL,
    initiated_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    content text NOT NULL,
    "position" integer NOT NULL,
    state text DEFAULT 'queued' NOT NULL,
    materialized_at timestamp with time zone,
    removed_at timestamp with time zone,
    removal_reason text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT conversation_run_queue_items_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_run_queue_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversation_run_queue_items_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT conversation_run_queue_items_message_id_key UNIQUE (message_id),
    CONSTRAINT conversation_run_queue_items_request_key_key
        UNIQUE (company_id, conversation_id, initiated_by_user_id, request_key),
    CONSTRAINT conversation_run_queue_items_state_check
        CHECK (state IN ('queued', 'materialized', 'removed'))
);

CREATE INDEX conversation_run_queue_items_queued_idx
    ON conversation_run_queue_items (conversation_id, "position", created_at, id)
    WHERE state = 'queued';
CREATE INDEX conversation_run_queue_items_actor_idx
    ON conversation_run_queue_items (company_id, initiated_by_user_id, state, created_at);

-- Writers lock the Attachment row before reserving or linking it, so the two
-- ownership tables serialize.
CREATE TABLE conversation_run_queue_attachments (
    -- Both composite keys below already carry company_id into a parent with
    -- its own company edge.
    company_id uuid NOT NULL,
    queue_item_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT conversation_run_queue_attachments_pkey PRIMARY KEY (queue_item_id, "position"),
    CONSTRAINT conversation_run_queue_attachments_item_attachment_key
        UNIQUE (queue_item_id, attachment_id),
    CONSTRAINT conversation_run_queue_attachments_queue_item_fkey
        FOREIGN KEY (company_id, queue_item_id)
        REFERENCES conversation_run_queue_items(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT conversation_run_queue_attachments_attachment_fkey
        FOREIGN KEY (company_id, attachment_id)
        REFERENCES attachments(company_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX conversation_run_queue_attachments_active_attachment_idx
    ON conversation_run_queue_attachments (attachment_id)
    WHERE released_at IS NULL;

CREATE TABLE tasks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL,
    number bigint NOT NULL,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    status text DEFAULT 'backlog' NOT NULL,
    priority text DEFAULT 'medium' NOT NULL,
    labels text[] DEFAULT '{}' NOT NULL,
    start_date date,
    due_date date,
    assignee_user_id uuid,
    assignee_agent_id uuid,
    creator_user_id uuid,
    creator_agent_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    goal_id uuid NOT NULL,
    department_agent_id uuid NOT NULL,
    milestone_type text NOT NULL,
    execution_generation bigint DEFAULT 0 NOT NULL,
    key text NOT NULL,
    CONSTRAINT tasks_pkey PRIMARY KEY (id),
    CONSTRAINT tasks_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT tasks_company_id_number_key UNIQUE (company_id, number),
    CONSTRAINT tasks_company_key_key UNIQUE (company_id, key),
    CONSTRAINT tasks_conversation_id_key UNIQUE (conversation_id),
    CONSTRAINT tasks_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT tasks_goal_fkey
        FOREIGN KEY (company_id, goal_id)
        REFERENCES goals(company_id, id),
    CONSTRAINT tasks_department_agent_fkey
        FOREIGN KEY (company_id, department_agent_id)
        REFERENCES agents(company_id, id),
    CONSTRAINT tasks_assignee_agent_fkey
        FOREIGN KEY (company_id, assignee_agent_id)
        REFERENCES agents(company_id, id),
    CONSTRAINT tasks_assignee_exactly_one_check
        CHECK (num_nonnulls(assignee_user_id, assignee_agent_id) = 1),
    CONSTRAINT tasks_creator_exactly_one_check
        CHECK (num_nonnulls(creator_user_id, creator_agent_id) = 1),
    CONSTRAINT tasks_milestone_type_check CHECK (
        milestone_type IN (
            'positioning', 'brand', 'presence', 'finance', 'product', 'launch',
            'growth', 'sales_outbound', 'operations', 'support'
        )
    ),
    CONSTRAINT tasks_priority_check CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
    CONSTRAINT tasks_status_check CHECK (
        status IN (
            'backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked',
            'failed', 'error', 'cancelled'
        )
    )
);

CREATE INDEX tasks_assignee_user_idx ON tasks USING btree (company_id, assignee_user_id, number DESC)
    WHERE (assignee_user_id IS NOT NULL);

CREATE INDEX tasks_assignee_agent_idx ON tasks USING btree (company_id, assignee_agent_id, number DESC)
    WHERE (assignee_agent_id IS NOT NULL);

CREATE INDEX tasks_company_status_idx ON tasks USING btree (company_id, status, number DESC);
-- Per-agent task counts for the departments list. tasks_assignee_agent_idx
-- carries (company_id, assignee_agent_id, number DESC) and no status, so a
-- grouped count had to read every task ever assigned and filter in the heap.
CREATE INDEX tasks_company_agent_status_idx
    ON tasks USING btree (company_id, assignee_agent_id, status)
    WHERE assignee_agent_id IS NOT NULL;

CREATE INDEX tasks_goal_idx ON tasks USING btree (company_id, goal_id, number DESC)
    ;

CREATE INDEX tasks_department_agent_idx
    ON tasks USING btree (company_id, department_agent_id, number DESC);

CREATE INDEX tasks_milestone_idx
    ON tasks USING btree (company_id, milestone_type, number DESC);

-- A milestone the founder declares was completed outside Zenith Team, so a
-- company that arrives mid-journey is not asked to redo work it already did.
-- It lives beside tasks because both constrain the same milestone vocabulary;
-- the Go catalogue remains the authority and a schema test diffs the two.
--
-- The row is a founder's statement of fact, so it carries no updated_at and no
-- version: correcting one is a delete followed by a fresh attestation. Whether
-- a milestone is satisfied is always derived at read time from Tasks and these
-- rows together, never stored.
CREATE TABLE company_milestone_attestations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    milestone_type text NOT NULL,
    catalog_version integer NOT NULL,
    note text DEFAULT '' NOT NULL,
    attested_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT company_milestone_attestations_pkey PRIMARY KEY (id),
    CONSTRAINT company_milestone_attestations_company_milestone_key
        UNIQUE (company_id, milestone_type),
    CONSTRAINT company_milestone_attestations_milestone_type_check CHECK (
        milestone_type IN (
            'positioning', 'brand', 'presence', 'finance', 'product', 'launch',
            'growth', 'sales_outbound', 'operations', 'support'
        )
    ),
    CONSTRAINT company_milestone_attestations_catalog_version_check
        CHECK (catalog_version >= 1)
);

-- The other half of the task <-> conversation edge. Deleting a live Task
-- detaches its historical Conversation so conversation items, Runs, and
-- immutable usage evidence remain reportable. Deleting the Conversation still removes its
-- live Task through tasks_conversation_fkey. DEFERRABLE because a new pair is
-- created inside one transaction (Conversation first, Task second).
ALTER TABLE conversations
    ADD CONSTRAINT conversations_task_id_fkey
        FOREIGN KEY (company_id, task_id)
        REFERENCES tasks(company_id, id)
        ON DELETE SET NULL (task_id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE task_dependencies (
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    dependent_task_id uuid NOT NULL,
    prerequisite_task_id uuid NOT NULL,
    created_by_user_id uuid,
    created_by_agent_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT task_dependencies_pkey PRIMARY KEY (company_id, dependent_task_id, prerequisite_task_id),
    CONSTRAINT task_dependencies_dependent_task_fkey
        FOREIGN KEY (company_id, dependent_task_id)
        REFERENCES tasks(company_id, id)
        ON DELETE CASCADE,
    CONSTRAINT task_dependencies_prerequisite_task_fkey
        FOREIGN KEY (company_id, prerequisite_task_id)
        REFERENCES tasks(company_id, id)
        ON DELETE CASCADE,
    -- Graph integrity, not policy: a self-edge makes a task block itself
    -- forever, and no write path rejects one. It stays for the same reason the
    -- two foreign keys above do.
    CONSTRAINT task_dependencies_distinct_tasks_check
        CHECK (dependent_task_id <> prerequisite_task_id)
);

CREATE INDEX task_dependencies_prerequisite_idx
    ON task_dependencies USING btree (company_id, prerequisite_task_id, dependent_task_id);
