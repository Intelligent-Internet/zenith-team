-- name: LockPresentationForPublication :one
SELECT * FROM project_presentations
WHERE company_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE;

-- name: GetPresentationPublication :one
SELECT id,public_token,created_at,revoked_at FROM project_presentation_publications
WHERE company_id=$1 AND project_id=$2 AND presentation_id=$3 AND revoked_at IS NULL;

-- name: GetPresentationPublicationReceipt :one
SELECT id,public_token,created_at,revoked_at FROM project_presentation_publications
WHERE company_id=$1 AND project_id=$2 AND presentation_id=$3 AND created_by_user_id=$4 AND request_key=$5;

-- name: InsertPresentationPublication :one
INSERT INTO project_presentation_publications(company_id,project_id,presentation_id,html,created_by_user_id,request_key)
VALUES($1,$2,$3,$4,$5,$6) RETURNING id,public_token,created_at,revoked_at;

-- name: RevokePresentationPublication :execrows
UPDATE project_presentation_publications SET revoked_at=now(),revoked_by_user_id=$5
WHERE company_id=$1 AND project_id=$2 AND presentation_id=$3 AND id=$4 AND revoked_at IS NULL;

-- name: GetPublicPresentationHTML :one
SELECT publication.html FROM project_presentation_publications publication
JOIN companies company ON company.id=publication.company_id AND company.deleting_at IS NULL
JOIN projects project ON project.company_id=publication.company_id AND project.id=publication.project_id AND project.archived_at IS NULL
WHERE publication.public_token=$1 AND publication.revoked_at IS NULL;

-- name: PresentationPublicationProjectActive :one
SELECT (archived_at IS NULL)::boolean AS active FROM projects WHERE company_id=$1 AND id=$2;
