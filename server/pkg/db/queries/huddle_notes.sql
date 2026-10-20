-- name: EnsureHuddleNotesPolicy :exec
INSERT INTO huddle_notes_policies(company_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: LockHuddleNotesPolicy :one
SELECT * FROM huddle_notes_policies WHERE company_id=$1 FOR UPDATE;

-- name: HuddleNotesPolicyAllows :one
-- A Company without a policy row has the column defaults, which allow notes.
SELECT COALESCE((SELECT enabled AND monthly_seconds > 0 FROM huddle_notes_policies WHERE company_id=$1), true)::boolean AS allows;

-- name: SetHuddleNotesPolicy :one
UPDATE huddle_notes_policies SET enabled=$2, retention_days=$3, monthly_seconds=$4, revision=revision+1 WHERE company_id=$1 RETURNING *;

-- name: ReserveHuddleNotes :execrows
UPDATE huddle_notes_policies SET
 reserved_seconds=CASE WHEN period_start=date_trunc('month',now())::date THEN reserved_seconds ELSE 0 END + sqlc.arg(seconds)::bigint,
 period_start=date_trunc('month',now())::date
WHERE company_id=$1 AND enabled AND
 CASE WHEN period_start=date_trunc('month',now())::date THEN reserved_seconds ELSE 0 END + sqlc.arg(seconds)::bigint <= monthly_seconds;

-- name: SetHuddleNotesMode :exec
UPDATE huddles SET notes_mode=true WHERE company_id=$1 AND id=$2;

-- name: EnsureHuddleNotes :exec
INSERT INTO huddle_notes(company_id,huddle_id,expires_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING;

-- name: LockHuddleNotes :one
SELECT * FROM huddle_notes WHERE company_id=$1 AND huddle_id=$2 FOR UPDATE;

-- name: GetHuddleNotes :one
SELECT * FROM huddle_notes WHERE company_id=$1 AND huddle_id=$2;

-- name: StartHuddleNotes :one
UPDATE huddle_notes SET state='starting', revision=revision+1, started_by_user_id=$3, stop_reason='' WHERE company_id=$1 AND id=$2 RETURNING *;

-- name: SetHuddleNotesState :exec
UPDATE huddle_notes SET state=$3,stop_reason=$4,revision=revision+1 WHERE company_id=$1 AND id=$2;

-- name: CreateHuddleNotesInterval :one
INSERT INTO huddle_notes_intervals(company_id,notes_id,request_key,deadline_at) VALUES($1,$2,$3,$4) RETURNING *;

-- name: GetHuddleNotesIntervalReplay :one
SELECT * FROM huddle_notes_intervals WHERE company_id=$1 AND notes_id=$2 AND request_key=$3;

-- name: GetHuddleNotesInterval :one
SELECT i.*, n.huddle_id FROM huddle_notes_intervals i JOIN huddle_notes n ON n.id=i.notes_id WHERE i.id=$1;

-- name: ClaimHuddleNotesInterval :one
UPDATE huddle_notes_intervals SET state='claimed', capability_hash=$2, lease_expires_at=now()+interval '15 seconds',started_at=now()
WHERE id=$1 AND state='pending' AND lease_expires_at>now() RETURNING *;

-- name: RenewHuddleNotesInterval :one
UPDATE huddle_notes_intervals SET lease_expires_at=LEAST(now()+interval '15 seconds',drain_until) WHERE id=$1 AND state='claimed' RETURNING *;

-- name: FinishHuddleNotesIntervals :exec
UPDATE huddle_notes_intervals SET state='finished',ended_at=COALESCE(stop_requested_at,now()) WHERE company_id=$1 AND notes_id=$2 AND state<>'finished';

-- name: AdvanceHuddleNotesInterval :exec
UPDATE huddle_notes_intervals SET sequence=$2,batch_hash=$3 WHERE id=$1;

-- name: InsertHuddleNotesSegment :exec
INSERT INTO huddle_notes_segments(company_id,interval_id,sequence,speaker,content,start_ms,end_ms) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ListHuddleNotesSegments :many
SELECT s.* FROM huddle_notes_segments s JOIN huddle_notes_intervals i ON i.id=s.interval_id
WHERE s.company_id=$1 AND i.notes_id=$2 AND (s.created_at,s.id)>(sqlc.arg(after_time)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY s.created_at,s.id LIMIT 100;

-- name: ListHuddleNotesIntervals :many
SELECT * FROM huddle_notes_intervals WHERE company_id=$1 AND notes_id=$2 ORDER BY created_at,id;

-- name: ListActiveHuddleNotes :many
SELECT company_id,huddle_id FROM huddle_notes WHERE state IN ('starting','capturing','stopping','stopped','interrupted') ORDER BY created_at LIMIT 100;

-- name: ListExpiredHuddleNotes :many
SELECT company_id,huddle_id FROM huddle_notes WHERE state<>'expired' AND expires_at<=now() LIMIT 100;

-- name: PurgeHuddleNotesIntervals :exec
DELETE FROM huddle_notes_intervals WHERE company_id=$1 AND notes_id=$2;

-- name: ExpireHuddleNotes :exec
UPDATE huddle_notes SET state='expired',stop_reason='retention',recap='{}',revision=revision+1 WHERE company_id=$1 AND id=$2;

-- name: ShortenHuddleNotesRetention :exec
UPDATE huddle_notes SET expires_at=LEAST(expires_at,created_at+sqlc.arg(days)::integer*interval '1 day') WHERE company_id=$1 AND state<>'expired';

-- name: GetHuddleNotesSegmentCursor :one
SELECT s.created_at,s.id FROM huddle_notes_segments s JOIN huddle_notes_intervals i ON i.id=s.interval_id WHERE s.company_id=$1 AND i.notes_id=$2 AND s.id=$3;

-- name: ListHuddleNotesRecapCandidates :many
SELECT n.company_id,n.huddle_id FROM huddle_notes n JOIN huddles h ON h.id=n.huddle_id
WHERE h.state='ended' AND n.state<>'expired' AND n.expires_at>now() AND n.recap_attempts<3
AND (n.recap_state='pending' OR n.recap_state='failed' OR (n.recap_state='processing' AND n.recap_lease_until<now()))
ORDER BY n.created_at LIMIT 10;

-- name: ClaimHuddleNotesRecap :one
UPDATE huddle_notes SET recap_state='processing',recap_attempts=recap_attempts+1,recap_token=gen_random_uuid(),recap_lease_until=now()+interval '120 seconds'
WHERE company_id=$1 AND id=$2 AND recap_attempts<3 AND (recap_state IN ('pending','failed') OR (recap_state='processing' AND recap_lease_until<now())) RETURNING *;

-- name: AllHuddleNotesSegments :many
SELECT s.* FROM huddle_notes_segments s JOIN huddle_notes_intervals i ON i.id=s.interval_id
WHERE s.company_id=$1 AND i.notes_id=$2 ORDER BY i.created_at,s.start_ms,s.id LIMIT 10001;

-- name: SetHuddleNotesRecap :one
UPDATE huddle_notes SET recap=$3,recap_state=$4,recap_revision=recap_revision+1,recap_edited_by_user_id=$5,revision=revision+1 WHERE company_id=$1 AND id=$2 RETURNING *;

-- name: InsertHuddleNotesRecapRevision :exec
INSERT INTO huddle_notes_recap_revisions(company_id,notes_id,revision,content,edited_by_user_id) VALUES($1,$2,$3,$4,$5);

-- name: PurgeHuddleNotesRecapRevisions :exec
DELETE FROM huddle_notes_recap_revisions WHERE company_id=$1 AND notes_id=$2;

-- name: GetHuddleNotesAction :one
SELECT * FROM huddle_notes_actions WHERE company_id=$1 AND notes_id=$2 AND action_index=$3;

-- name: InsertHuddleNotesAction :one
INSERT INTO huddle_notes_actions(company_id,notes_id,action_index,recap_revision,task_id,submitted_by_user_id,submission_hash) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;

-- name: SnapshotHuddleNotesChat :exec
INSERT INTO huddle_notes_segments(company_id,interval_id,sequence,speaker,content,start_ms,end_ms)
SELECT n.company_id,interval.id,10000+item.seq*64+part.n,'chat',substring(item.content FROM part.n::integer*2000+1 FOR 2000),0,0
FROM huddle_notes n JOIN huddles h ON h.id=n.huddle_id
JOIN conversation_items item ON item.company_id=n.company_id AND item.conversation_id=h.chat_conversation_id AND item.type='user_message' AND item.author_kind='user' AND item.deleted_at IS NULL
JOIN LATERAL (
 SELECT i.id FROM huddle_notes_intervals i WHERE i.notes_id=n.id AND i.started_at IS NOT NULL
 AND item.created_at>=i.started_at AND item.created_at<=i.ended_at ORDER BY i.created_at LIMIT 1
) interval ON true
CROSS JOIN LATERAL generate_series(0,(length(item.content)-1)/2000) part(n)
WHERE n.company_id=$1 AND n.id=$2 AND length(item.content)>0
ON CONFLICT(interval_id,sequence) DO NOTHING;

-- name: ListHuddleNotesActions :many
SELECT action_index,task_id FROM huddle_notes_actions WHERE company_id=$1 AND notes_id=$2 ORDER BY action_index;

-- name: FailExpiredHuddleNotesRecaps :exec
UPDATE huddle_notes SET recap_state='failed',revision=revision+1
WHERE recap_state='processing' AND recap_attempts=3 AND recap_lease_until<now();

-- name: BeginHuddleNotesDrain :exec
UPDATE huddle_notes_intervals SET stop_requested_at=now(),drain_until=LEAST(now()+interval '20 seconds',deadline_at),
 lease_expires_at=LEAST(now()+interval '20 seconds',deadline_at)
WHERE company_id=$1 AND notes_id=$2 AND state='claimed' AND stop_requested_at IS NULL AND lease_expires_at>now();
