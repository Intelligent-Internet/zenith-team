-- name: UpsertCompanyMilestoneAttestation :one
-- Attesting the same milestone twice is the founder restating a fact, not a
-- conflict, so the write is idempotent on (company_id, milestone_type).
INSERT INTO company_milestone_attestations (
    company_id, milestone_type, catalog_version, note, attested_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(milestone_type)::text,
    sqlc.arg(catalog_version)::integer,
    sqlc.arg(note)::text,
    sqlc.arg(attested_by_user_id)::uuid
)
ON CONFLICT (company_id, milestone_type) DO UPDATE SET
    catalog_version = EXCLUDED.catalog_version,
    note = EXCLUDED.note,
    attested_by_user_id = EXCLUDED.attested_by_user_id,
    created_at = now()
RETURNING *;

-- name: DeleteCompanyMilestoneAttestation :execrows
DELETE FROM company_milestone_attestations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND milestone_type = sqlc.arg(milestone_type)::text;

-- name: ListCompanyMilestoneAttestations :many
SELECT *
FROM company_milestone_attestations
WHERE company_id = sqlc.arg(company_id)::uuid
ORDER BY milestone_type;
