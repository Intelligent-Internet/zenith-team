-- Agent runtime bindings (ADR 0014): one row per (Agent, member) naming
-- which of the member's own runtimes runs that Agent template. A binding is
-- the member's private setup; the Runs it admits are the ones the member
-- authorizes, and the claim fence (runs.sql) rechecks the row at dispatch.

-- The bind write restates the invariants the service checked under its
-- locks: the runtime is the member's own, not removed and of the Agent's
-- provider; the Agent is not archived; the member is active in the Company.
-- No row means one of them stopped holding between the check and the write.
-- name: UpsertAgentRuntimeBinding :one
INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
SELECT agents.company_id, agents.id, sqlc.arg(user_id)::uuid, runtimes.id, runtimes.provider
FROM agents
JOIN runtimes
  ON runtimes.company_id = agents.company_id
 AND runtimes.id = sqlc.arg(runtime_id)::uuid
 AND runtimes.provider = agents.provider
 AND runtimes.owner_user_id = sqlc.arg(user_id)::uuid
 AND runtimes.removed_at IS NULL
JOIN company_members AS memberships
  ON memberships.company_id = agents.company_id
 AND memberships.user_id = sqlc.arg(user_id)::uuid
 AND memberships.status = 'active'
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL
ON CONFLICT (agent_id, user_id) DO UPDATE
SET runtime_id = EXCLUDED.runtime_id,
    updated_at = now()
RETURNING agent_runtime_bindings.*;

-- name: GetAgentRuntimeBindingForUserForUpdate :one
SELECT agent_runtime_bindings.*
FROM agent_runtime_bindings
WHERE agent_runtime_bindings.company_id = sqlc.arg(company_id)::uuid
  AND agent_runtime_bindings.agent_id = sqlc.arg(agent_id)::uuid
  AND agent_runtime_bindings.user_id = sqlc.arg(user_id)::uuid
FOR UPDATE;

-- The enqueue resolver: the runtime a Run of this Agent, authorized by this
-- member, executes on. The binding must still name the member's own live
-- runtime and the member must still be an enabled, active member — the same
-- facts the claim fence rechecks at dispatch. FOR SHARE on the binding row so
-- an unbind or a runtime removal in flight waits for this enqueue to commit
-- and then retargets or cancels the Run it wrote.
-- name: GetAgentRuntimeBindingForRun :one
SELECT binding.id, binding.runtime_id, runtimes.status AS runtime_status, runtimes.provider
FROM agent_runtime_bindings AS binding
JOIN runtimes
  ON runtimes.company_id = binding.company_id
 AND runtimes.id = binding.runtime_id
 AND runtimes.removed_at IS NULL
 AND runtimes.owner_user_id = binding.user_id
JOIN users AS owners
  ON owners.id = binding.user_id
 AND owners.disabled_at IS NULL
JOIN company_members AS memberships
  ON memberships.company_id = binding.company_id
 AND memberships.user_id = binding.user_id
 AND memberships.status = 'active'
WHERE binding.company_id = sqlc.arg(company_id)::uuid
  AND binding.agent_id = sqlc.arg(agent_id)::uuid
  AND binding.user_id = sqlc.arg(user_id)::uuid
FOR SHARE OF binding;

-- The caller's bindings, oldest first, with the runtime facts a RuntimeRef
-- renders. Optionally one runtime's — the runtime detail screen's "agents
-- on this computer" and the removal review.
-- name: ListAgentRuntimeBindingsForUser :many
SELECT
    sqlc.embed(binding),
    runtimes.name AS runtime_name,
    runtimes.custom_name AS runtime_custom_name,
    runtimes.status AS runtime_status,
    runtimes.unusable_reason_code AS runtime_unusable_reason_code,
    runtimes.unusable_reason_message AS runtime_unusable_reason_message,
    runtimes.provider_sign_in_state AS runtime_sign_in_state,
    runtimes.provider_sign_in_detail AS runtime_sign_in_detail,
    runtimes.provider_sign_in_checked_at AS runtime_sign_in_checked_at
FROM agent_runtime_bindings AS binding
JOIN runtimes
  ON runtimes.company_id = binding.company_id
 AND runtimes.id = binding.runtime_id
 AND runtimes.removed_at IS NULL
WHERE binding.company_id = sqlc.arg(company_id)::uuid
  AND binding.user_id = sqlc.arg(user_id)::uuid
  AND (
      sqlc.narg(runtime_id)::uuid IS NULL
      OR binding.runtime_id = sqlc.narg(runtime_id)::uuid
  )
ORDER BY binding.created_at, binding.id;

-- name: ListAgentRuntimeBindingsForRuntimeForUpdate :many
SELECT agent_runtime_bindings.*
FROM agent_runtime_bindings
WHERE agent_runtime_bindings.company_id = sqlc.arg(company_id)::uuid
  AND agent_runtime_bindings.runtime_id = sqlc.arg(runtime_id)::uuid
ORDER BY agent_runtime_bindings.id
FOR UPDATE;

-- Runtime removal with a target: the registrant's bindings on the removed
-- machine move to another of their runtimes. The provider match is the
-- composite foreign key's to enforce; the service checks it first so the
-- refusal is a readable 422, not a constraint error.
-- name: RetargetAgentRuntimeBindings :many
UPDATE agent_runtime_bindings
SET runtime_id = sqlc.arg(target_runtime_id)::uuid,
    updated_at = now()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND runtime_id = sqlc.arg(source_runtime_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
RETURNING agent_runtime_bindings.*;

-- name: DeleteAgentRuntimeBinding :one
DELETE FROM agent_runtime_bindings
WHERE company_id = sqlc.arg(company_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
RETURNING agent_runtime_bindings.*;

-- name: DeleteAgentRuntimeBindingsForRuntime :many
DELETE FROM agent_runtime_bindings
WHERE company_id = sqlc.arg(company_id)::uuid
  AND runtime_id = sqlc.arg(runtime_id)::uuid
RETURNING agent_runtime_bindings.*;

-- Membership revocation and account deletion: a member who leaves the
-- Company takes their setup with them.
-- name: DeleteAgentRuntimeBindingsForUser :many
DELETE FROM agent_runtime_bindings
WHERE company_id = sqlc.arg(company_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
RETURNING agent_runtime_bindings.*;
