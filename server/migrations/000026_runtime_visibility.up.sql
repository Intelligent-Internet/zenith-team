-- Runtime visibility and execution consent (ADR 0007 as amended by ADR 0011).
--
-- visibility: whether the registrant lends this provider connection to the
-- Company. 'private' — the default for every new registration and for every
-- existing row at this cutover — keeps the row visible to the registrant
-- alone (no Company role sees it) and admits only the registrant's own
-- requests plus the Agents the registrant hosts (see agents below);
-- 'public' shows it to every active member, lets Company owners bind Agents
-- to it and admits any active member's request. It is the registrant's
-- decision alone, never a Company role's, and is written only by the
-- user-plane visibility route. Registration keeps it unless the row's owner
-- changes, in which case the upsert resets it to private: a new registrant
-- inherits no consent the previous one gave.
--
-- visibility_revision: a database-owned counter every visibility change
-- advances, so a sharing review that went stale on another device returns a
-- conflict instead of silently overwriting a newer decision.
ALTER TABLE runtimes
    ADD COLUMN visibility text DEFAULT 'private' NOT NULL,
    ADD COLUMN visibility_revision bigint DEFAULT 1 NOT NULL,
    ADD CONSTRAINT runtimes_visibility_check CHECK (visibility IN ('private', 'public'));

-- runtime_bound_by_user_id: the Company owner who bound this Agent to its
-- runtime (ADR 0011). When that person is the runtime's registrant, hosting
-- the Agent on their own machine is their standing consent for the Company
-- to work with it there: any active member's request runs, private or not.
-- A binding made by someone else runs for others only while the runtime is
-- public. Rewritten only when the binding itself changes, so editing an
-- Agent's instructions never moves its consent. Attribution, no foreign key.
-- Cutover: a binding is credited to the registrant when the registrant is a
-- Company owner — the only role that binds — and left unattributed
-- otherwise, which admits nobody but the registrant until an owner rebinds.
ALTER TABLE agents
    ADD COLUMN runtime_bound_by_user_id uuid;

UPDATE agents
SET runtime_bound_by_user_id = runtimes.owner_user_id
FROM runtimes
JOIN company_members AS registrants
  ON registrants.company_id = runtimes.company_id
 AND registrants.user_id = runtimes.owner_user_id
 AND registrants.status = 'active'
 AND registrants.role = 'owner'
WHERE agents.runtime_id = runtimes.id
  AND agents.runtime_bound_by_user_id IS NULL;

-- execution_authorized_by_user_id: the member whose permitted action asked
-- for this Run — the message sender, the assigning owner, the Schedule
-- creator, the Approval or Question decider, or the originating Run's
-- authorizer for a promotion or retry. The claim admits a Run only while this
-- user may still execute on the Run's runtime: its registrant always, an
-- active member while the runtime is public, and an active member while the
-- Run's Agent is one the registrant hosts. Attribution, not ownership, so no
-- foreign key: a deleted account must not cascade through Run history.
-- Nullable because rows written before this migration carry none.
ALTER TABLE runs
    ADD COLUMN execution_authorized_by_user_id uuid;

-- Cutover. A Run written before consent existed has no authorizer. Rather
-- than guess one, recover it only where the row itself proves it — the words
-- it was started on (its own user_message), the private direct chat it
-- belongs to, or the human decision that continued it — for every Run that
-- has not finished. Pending Runs that remain unprovable are cancelled with a
-- readable reason: the claim would never admit them, and an unexplained
-- two-hour queue expiry would be the dishonest alternative. In-flight Runs
-- keep going whatever their provenance — the claim already happened, and a
-- policy change does not undo a running process. Finished rows are history
-- and stay as they are.
UPDATE runs
SET execution_authorized_by_user_id = COALESCE(
    (
        SELECT conversation_items.author_id
        FROM conversation_items
        WHERE conversation_items.run_id = runs.id
          AND conversation_items.company_id = runs.company_id
          AND conversation_items.type = 'user_message'
          AND conversation_items.author_kind = 'user'
        ORDER BY conversation_items.seq
        LIMIT 1
    ),
    (
        SELECT conversations.created_by_user_id
        FROM conversations
        WHERE conversations.id = runs.conversation_id
          AND conversations.company_id = runs.company_id
          AND conversations.kind = 'direct'
    ),
    (
        SELECT approvals.decided_by_user_id
        FROM approvals
        WHERE approvals.continuation_run_id = runs.id
          AND approvals.company_id = runs.company_id
    ),
    (
        SELECT questions.resolved_by_user_id
        FROM questions
        WHERE questions.continuation_run_id = runs.id
          AND questions.company_id = runs.company_id
    )
)
WHERE runs.status IN ('queued', 'deferred', 'waiting_local_directory', 'dispatched', 'running')
  AND runs.execution_authorized_by_user_id IS NULL;

UPDATE runs
SET status = 'cancelled',
    error = 'cancelled at the private-runtime cutover: no provable authorizing member',
    wait_reason = NULL,
    prepare_lease_expires_at = NULL,
    fire_at = NULL,
    updated_at = now()
WHERE runs.status IN ('queued', 'deferred', 'waiting_local_directory')
  AND runs.execution_authorized_by_user_id IS NULL;
