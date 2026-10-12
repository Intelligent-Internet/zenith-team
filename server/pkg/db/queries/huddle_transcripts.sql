-- name: EnsureHuddleTranscript :one
INSERT INTO huddle_transcripts(company_id,huddle_id,invitation_revision,agent_name)
VALUES($1,$2,$3,$4)
ON CONFLICT(company_id,huddle_id,invitation_revision) DO UPDATE SET agent_name=huddle_transcripts.agent_name
RETURNING *;

-- name: UpdateHuddleTranscript :exec
UPDATE huddle_transcripts SET content=$2,last_speaker=$3,truncated=$4,updated_at=now() WHERE id=$1 AND ended_at IS NULL;

-- name: AdvanceHuddleTranscript :exec
UPDATE huddle_ai_sessions SET transcript_sequence=$2,transcript_hash=$3 WHERE id=$1;

-- name: SealHuddleTranscript :execrows
UPDATE huddle_transcripts SET ended_at=now(),updated_at=now()
WHERE company_id=$1 AND huddle_id=$2 AND invitation_revision=$3 AND ended_at IS NULL;

-- name: ListUnpostedHuddleTranscripts :many
SELECT * FROM huddle_transcripts WHERE ended_at IS NOT NULL AND posted_at IS NULL ORDER BY created_at LIMIT 100;

-- name: LockHuddleTranscript :one
SELECT * FROM huddle_transcripts WHERE company_id=$1 AND id=$2 FOR UPDATE;

-- name: MarkHuddleTranscriptPosted :exec
UPDATE huddle_transcripts SET posted_at=now(),updated_at=now() WHERE company_id=$1 AND id=$2;

-- name: InsertHuddleTranscriptMessage :exec
INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata,body)
VALUES($1,$2,$3,'discussion_system','system',sqlc.arg(metadata)::jsonb,sqlc.arg(body)::jsonb);
