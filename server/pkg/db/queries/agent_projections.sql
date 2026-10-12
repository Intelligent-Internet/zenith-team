-- Canonical Agent read projection. One statement serves the roster, the
-- exact Agent read and the bootstrap root Agent; optional predicates only
-- narrow the same projection.
--
-- The projection is per viewer (ADR 0014): the runtime columns come from the
-- VIEWER's own binding of the Agent, and availability says whether the viewer
-- can run it right now. A binding on a runtime the viewer no longer owns (a
-- machine re-registered under another account) reads as no binding at all.
--
-- unavailable_reason precedence: agent_archived > not_bound >
-- runtime_unusable > runtime_offline > runtime_signed_out > '' (available).
-- An unusable runtime is structurally offline, so its branch has to come
-- before the offline one to be reachable; it is the refinement the UI recovers
-- from differently (fix the CLI, not wake the machine).

-- name: ListAgentProjectionsForActor :many
SELECT
    sqlc.embed(agents),
    viewer_binding.id AS binding_id,
    viewer_binding.created_at AS binding_created_at,
    runtimes.id AS runtime_id,
    runtimes.name AS runtime_name,
    runtimes.custom_name AS runtime_custom_name,
    runtimes.status AS runtime_status,
    runtimes.unusable_reason_code AS runtime_unusable_reason_code,
    runtimes.unusable_reason_message AS runtime_unusable_reason_message,
    runtimes.provider_sign_in_state AS runtime_sign_in_state,
    runtimes.provider_sign_in_detail AS runtime_sign_in_detail,
    runtimes.provider_sign_in_checked_at AS runtime_sign_in_checked_at,
    creators.display_name AS creator_display_name,
    creators.avatar_url AS creator_avatar_url,
    COALESCE(parent_agents.id, '00000000-0000-0000-0000-000000000000'::uuid)
        AS parent_agent_id,
    COALESCE(parent_agents.name, '')::text AS parent_agent_name,
    (
        agents.archived_at IS NULL
        AND runtimes.id IS NOT NULL
        AND runtimes.status = 'online'
        AND runtimes.unusable_reason_code IS NULL
        AND runtimes.provider_sign_in_state IS DISTINCT FROM 'signed_out'
    )::boolean AS runtime_available,
    CASE
        WHEN agents.archived_at IS NOT NULL THEN 'agent_archived'
        WHEN runtimes.id IS NULL THEN 'not_bound'
        WHEN runtimes.unusable_reason_code IS NOT NULL THEN 'runtime_unusable'
        WHEN runtimes.status <> 'online' THEN 'runtime_offline'
        WHEN runtimes.provider_sign_in_state = 'signed_out' THEN 'runtime_signed_out'
        ELSE ''
    END::text AS unavailable_reason,
    (
        SELECT count(*)::bigint
        FROM approvals
        WHERE approvals.company_id = agents.company_id
          AND approvals.requested_by_agent_id = agents.id
          AND approvals.status = 'pending'
    ) AS pending_approval_count
FROM agents
JOIN companies
  ON companies.id = agents.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = agents.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN agent_runtime_bindings AS viewer_binding
  ON viewer_binding.company_id = agents.company_id
 AND viewer_binding.agent_id = agents.id
 AND viewer_binding.user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN runtimes
  ON runtimes.company_id = viewer_binding.company_id
 AND runtimes.id = viewer_binding.runtime_id
 AND runtimes.removed_at IS NULL
 AND runtimes.owner_user_id = viewer_binding.user_id
LEFT JOIN users AS creators
  ON creators.id = agents.created_by_user_id
LEFT JOIN agents AS parent_agents
  ON parent_agents.company_id = agents.company_id
 AND parent_agents.id = agents.reports_to_agent_id
WHERE agents.company_id = sqlc.arg(company_id)::uuid
  AND (sqlc.arg(include_archived)::boolean OR agents.archived_at IS NULL)
  AND (
      sqlc.narg(reports_to_agent_id)::uuid IS NULL
      OR agents.reports_to_agent_id = sqlc.narg(reports_to_agent_id)::uuid
  )
  AND (
      NOT sqlc.arg(has_agent_id)::boolean
      OR agents.id = sqlc.arg(agent_id)::uuid
  )
  AND (
      NOT sqlc.arg(root_only)::boolean
      OR agents.reports_to_agent_id IS NULL
  )
ORDER BY agents.created_at, agents.id;
