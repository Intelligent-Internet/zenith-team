-- name: RecordDocumentChange :exec
INSERT INTO document_changes(company_id,page_id,sequence,actor_type,actor_id,change_type,delta,checkpoint)
VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: DocumentHistoryExists :one
SELECT EXISTS(SELECT 1 FROM document_changes WHERE company_id=$1 AND page_id=$2);

-- name: ListDocumentChanges :many
SELECT c.sequence,c.created_at AS saved_at,c.actor_type,c.actor_id,c.change_type,c.label,
 COALESCE(u.display_name,a.name,CASE WHEN c.actor_type='system' THEN 'Initial document' ELSE 'Former contributor' END)::text AS actor_name
FROM document_changes c
LEFT JOIN users u ON c.actor_type='user' AND u.id=c.actor_id
LEFT JOIN agents a ON c.actor_type='agent' AND a.id=c.actor_id
WHERE c.company_id=sqlc.arg(company_id) AND c.page_id=sqlc.arg(page_id)
 AND (sqlc.narg(actor_id)::uuid IS NULL OR c.actor_id=sqlc.narg(actor_id))
 AND (sqlc.narg(after_time)::timestamptz IS NULL OR c.created_at>=sqlc.narg(after_time))
 AND (sqlc.narg(before_time)::timestamptz IS NULL OR c.created_at<sqlc.narg(before_time))
 AND (sqlc.arg(before_sequence)::bigint=0 OR c.sequence<sqlc.arg(before_sequence))
ORDER BY c.sequence DESC LIMIT 101;

-- name: DocumentHistoryContributors :many
SELECT DISTINCT c.actor_type,c.actor_id,COALESCE(u.display_name,a.name,'Former contributor')::text AS actor_name
FROM document_changes c LEFT JOIN users u ON c.actor_type='user' AND u.id=c.actor_id
LEFT JOIN agents a ON c.actor_type='agent' AND a.id=c.actor_id
WHERE c.company_id=$1 AND c.page_id=$2 AND c.actor_id IS NOT NULL ORDER BY actor_name,c.actor_type,c.actor_id;

-- name: DocumentSuggestionReviews :many
SELECT review.*,change.project_id,change.page_id,change.actor_agent_id,
 COALESCE(agent.name,'Former Agent')::text AS agent_name
FROM page_suggestion_reviews review
JOIN project_page_changes change ON change.company_id=review.company_id AND change.id=review.change_id
LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
WHERE change.company_id=$1 AND change.page_id=$2
 AND review.after_sequence=ANY(sqlc.arg(sequences)::bigint[])
 AND review.action IN ('apply','undo');

-- name: DocumentHistoryCheckpoint :one
SELECT sequence,checkpoint FROM document_changes WHERE company_id=$1 AND page_id=$2 AND sequence<=$3 AND checkpoint IS NOT NULL ORDER BY sequence DESC LIMIT 1;

-- name: DocumentSuggestionCapture :one
SELECT change.project_id,before.block_content AS before_content,result.block_content AS result_content
FROM page_suggestion_reviews review
JOIN project_page_changes change ON change.company_id=review.company_id AND change.id=review.change_id
JOIN wiki_revisions before ON before.company_id=review.company_id AND before.id=review.before_revision_id
JOIN wiki_revisions result ON result.company_id=review.company_id AND result.id=review.result_revision_id
WHERE change.company_id=$1 AND change.page_id=$2 AND review.after_sequence=$3
 AND review.action IN ('apply','undo')
ORDER BY review.created_at DESC,review.id DESC LIMIT 1;

-- name: DocumentHistoryDeltas :many
SELECT delta FROM document_changes WHERE company_id=$1 AND page_id=$2 AND sequence>$3 AND sequence<=$4 ORDER BY sequence;

-- name: GetDocumentChange :one
SELECT * FROM document_changes WHERE company_id=$1 AND page_id=$2 AND sequence=$3;

-- name: NameDocumentChange :execrows
UPDATE document_changes SET label=$4 WHERE company_id=$1 AND page_id=$2 AND sequence=$3;

-- name: PreviousDocumentChange :one
SELECT COALESCE(max(sequence),0)::bigint FROM document_changes WHERE company_id=$1 AND page_id=$2 AND sequence<$3;
