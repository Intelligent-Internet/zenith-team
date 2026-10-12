-- name: GetCompanyShareLink :one
SELECT * FROM company_share_links WHERE company_id=$1;

-- name: FindCompanyShareLink :one
SELECT sqlc.embed(company_share_links), companies.name AS company_name
FROM company_share_links JOIN companies ON companies.id=company_share_links.company_id
WHERE company_share_links.token_hash=$1 AND companies.deleting_at IS NULL;

-- name: UpsertCompanyShareLink :one
INSERT INTO company_share_links (company_id,token_hash,encrypted_token,created_by_user_id,expires_at,role)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (company_id) DO UPDATE
SET token_hash=EXCLUDED.token_hash, encrypted_token=EXCLUDED.encrypted_token,
created_by_user_id=EXCLUDED.created_by_user_id,expires_at=EXCLUDED.expires_at,role=EXCLUDED.role,
revoked_at=NULL,updated_at=now(),version=company_share_links.version+1
RETURNING *;

-- name: RevokeCompanyShareLink :one
UPDATE company_share_links
SET revoked_at=now(),encrypted_token=NULL,updated_at=now(),version=version+1
WHERE company_id=$1 AND version=$2
RETURNING *;
