-- name: EnsureHuddleAIIntent :exec
INSERT INTO huddle_ai_states(company_id,huddle_id,chat_start_seq,chat_cursor,chat_completed_seq)
SELECT h.company_id,h.id,c.head_seq,c.head_seq,c.head_seq FROM huddles h
JOIN conversations c ON c.company_id=h.company_id AND c.id=h.chat_conversation_id
WHERE h.company_id=sqlc.arg(company_id)::uuid AND h.id=sqlc.arg(huddle_id)::uuid
ON CONFLICT(huddle_id) DO NOTHING;

-- name: GetHuddleAIState :one
SELECT * FROM huddle_ai_states WHERE company_id=$1 AND huddle_id=$2;

-- name: LockHuddleAIState :one
SELECT * FROM huddle_ai_states WHERE company_id=$1 AND huddle_id=$2 FOR UPDATE;

-- name: ListHuddleAIWork :many
SELECT h.company_id,h.id FROM huddles h JOIN huddle_ai_states s ON s.huddle_id=h.id
WHERE EXISTS(SELECT 1 FROM huddle_ai_budget_reservations b WHERE b.huddle_id=h.id AND b.settled_at IS NULL) OR h.state IN ('open','ending') OR EXISTS(SELECT 1 FROM huddle_ai_sessions a WHERE a.huddle_id=h.id AND a.state<>'finished')
OR EXISTS(SELECT 1 FROM huddle_transcripts t WHERE t.company_id=h.company_id AND t.huddle_id=h.id AND t.ended_at IS NULL)
ORDER BY s.updated_at LIMIT 500;

-- name: GetActiveHuddleAISession :one
SELECT * FROM huddle_ai_sessions WHERE company_id=$1 AND huddle_id=$2 AND state<>'finished';

-- name: GetHuddleAISession :one
SELECT * FROM huddle_ai_sessions WHERE id=$1;

-- name: GetLatestHuddleAISession :one
SELECT * FROM huddle_ai_sessions WHERE company_id=$1 AND huddle_id=$2
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: RecordHuddleAIStopReason :exec
UPDATE huddle_ai_sessions SET stop_reason=$2 WHERE id=$1 AND stop_reason='';

-- name: CreateHuddleAISession :one
INSERT INTO huddle_ai_sessions(company_id,huddle_id,generation,deadline_at,invitation_revision)
SELECT $1,$2,$3,$4,s.revision FROM huddle_ai_states s WHERE s.company_id=$1 AND s.huddle_id=$2 RETURNING *;

-- name: SetHuddleAIState :exec
UPDATE huddle_ai_states SET state=$3,generation=$4,failures=$5,retry_at=$6,updated_at=now()
WHERE company_id=$1 AND huddle_id=$2;

-- name: StopHuddleAISession :exec
UPDATE huddle_ai_sessions SET state='stopping',updated_at=now() WHERE id=$1 AND state IN ('pending','claimed');

-- name: FinishHuddleAISession :exec
UPDATE huddle_ai_sessions SET state='finished',finished_at=now(),updated_at=now() WHERE id=$1;

-- name: ClaimHuddleAISession :one
UPDATE huddle_ai_sessions SET state='claimed',capability_hash=$2,lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=now()
WHERE id=$1 AND state='pending' AND lease_expires_at>clock_timestamp() RETURNING *;

-- name: RenewHuddleAISession :one
UPDATE huddle_ai_sessions SET lease_expires_at=LEAST(deadline_at,clock_timestamp()+interval '30 seconds'),updated_at=now()
WHERE id=$1 AND state='claimed' AND lease_expires_at>clock_timestamp() RETURNING *;

-- name: AdvanceHuddleAIChat :exec
UPDATE huddle_ai_states SET chat_cursor=GREATEST(chat_cursor,$3),updated_at=now()
WHERE company_id=$1 AND huddle_id=$2;

-- name: ClaimHuddleAIDispatch :one
UPDATE huddle_ai_sessions SET dispatch_after=clock_timestamp()+interval '10 seconds',updated_at=now()
WHERE id=$1 AND state='pending' AND dispatch_after<=clock_timestamp() RETURNING *;

-- name: GetHuddleAIOperation :one
SELECT * FROM huddle_ai_operations WHERE company_id=$1 AND huddle_id=$2 AND user_id=$3 AND request_key=$4;

-- name: InsertHuddleAIOperation :exec
INSERT INTO huddle_ai_operations(company_id,huddle_id,user_id,request_key,enabled,expected_revision,agent_id,allow_company_context)
VALUES($1,$2,$3,$4,$5,$6,sqlc.narg(agent_id),sqlc.arg(allow_company_context));

-- name: ToggleHuddleAI :one
UPDATE huddle_ai_states s SET disabled=$3,revision=s.revision+1,
allow_company_context=NOT $3::boolean AND sqlc.arg(allow_company_context)::boolean,
context_granted_by_user_id=sqlc.narg(context_granted_by_user_id), tool_activity='{}',
agent_id=CASE WHEN NOT $3::boolean THEN sqlc.narg(agent_id)::uuid ELSE s.agent_id END,
agent_profile=CASE WHEN NOT $3::boolean THEN sqlc.arg(agent_profile)::jsonb ELSE s.agent_profile END,
greeted=CASE WHEN NOT $3::boolean THEN false ELSE s.greeted END,
state=CASE WHEN $3::boolean THEN 'disabled' ELSE 'pending' END,
chat_start_seq=CASE WHEN NOT $3::boolean THEN c.head_seq ELSE s.chat_start_seq END,
chat_cursor=CASE WHEN NOT $3::boolean THEN c.head_seq ELSE s.chat_cursor END,
chat_completed_seq=CASE WHEN NOT $3::boolean THEN c.head_seq ELSE s.chat_completed_seq END,
failures=0,retry_at=now(),updated_at=now()
FROM huddles h JOIN conversations c ON c.id=h.chat_conversation_id
WHERE s.company_id=$1 AND s.huddle_id=$2 AND h.id=s.huddle_id RETURNING s.*;

-- Voice is on unless an Agent was explicitly turned off (voice.FromSettings).
-- name: GetHuddleVoiceAgent :one
SELECT * FROM agents WHERE company_id=$1 AND id=$2 AND archived_at IS NULL
AND COALESCE(settings->'voice'->>'enabled','true')='true';

-- name: HuddleVoiceAgentEnabled :one
SELECT EXISTS(SELECT 1 FROM agents WHERE company_id=$1 AND id=$2
AND archived_at IS NULL AND COALESCE(settings->'voice'->>'enabled','true')='true') AS enabled;

-- The person who switched Company context on is the voice assistant's
-- Authorizer. The grant holds only while they are an active member and
-- in the call: connected to the Huddle and an active member of its chat. The
-- chat keeps a former participant's row for history, so the connection is
-- what ends the grant when they leave.
-- name: HuddleContextGrantActive :one
SELECT EXISTS (
 SELECT 1 FROM agents a JOIN company_members m ON m.company_id=a.company_id
 JOIN users u ON u.id=m.user_id
 JOIN huddles h ON h.company_id=a.company_id AND h.id=sqlc.arg(huddle_id)::uuid
 JOIN conversation_members chat ON chat.company_id=h.company_id AND chat.conversation_id=h.chat_conversation_id
  AND chat.user_id=m.user_id AND chat.status='active'
 WHERE a.company_id=sqlc.arg(company_id)::uuid AND a.id=sqlc.arg(agent_id)::uuid
 AND m.user_id=sqlc.arg(granted_by)::uuid AND m.status='active'
 AND u.deleted_at IS NULL AND u.disabled_at IS NULL
 AND EXISTS(SELECT 1 FROM huddle_connections hc WHERE hc.company_id=h.company_id AND hc.huddle_id=h.id
  AND hc.user_id=m.user_id AND hc.status='connected')
)::boolean;

-- One person is in the call: connected to the Huddle and an active member of
-- its chat. Only they may switch Company context on.
-- name: HuddleChatParticipantActive :one
SELECT EXISTS (
 SELECT 1 FROM huddles h
 JOIN conversation_members chat ON chat.company_id=h.company_id AND chat.conversation_id=h.chat_conversation_id
  AND chat.user_id=sqlc.arg(user_id)::uuid AND chat.status='active'
 JOIN huddle_connections hc ON hc.company_id=h.company_id AND hc.huddle_id=h.id
  AND hc.user_id=chat.user_id AND hc.status='connected'
 WHERE h.company_id=sqlc.arg(company_id)::uuid AND h.id=sqlc.arg(huddle_id)::uuid
)::boolean;

-- name: ClaimHuddleContextRead :execrows
UPDATE huddle_ai_sessions SET tool_calls=tool_calls+1,
tool_window_calls=CASE WHEN last_tool_at IS NULL OR last_tool_at<clock_timestamp()-interval '10 seconds' THEN 1 ELSE tool_window_calls+1 END,
last_tool_at=CASE WHEN last_tool_at IS NULL OR last_tool_at<clock_timestamp()-interval '10 seconds' THEN clock_timestamp() ELSE last_tool_at END
WHERE id=$1 AND tool_calls<120
AND (tool_window_calls<5 OR last_tool_at IS NULL OR last_tool_at<clock_timestamp()-interval '10 seconds');

-- name: SetHuddleToolActivity :exec
UPDATE huddle_ai_states SET tool_activity=sqlc.arg(activity)::jsonb
WHERE company_id=$1 AND huddle_id=$2;

-- name: ListHuddleAIHumans :many
SELECT hc.user_id FROM huddle_connections hc
JOIN company_members m ON m.company_id=hc.company_id AND m.user_id=hc.user_id AND m.status='active'
WHERE hc.company_id=$1 AND hc.huddle_id=$2 AND hc.status='connected';

-- name: ListHuddleAIChat :many
SELECT i.id,i.seq,i.author_id,COALESCE(u.display_name,'Former member')::text AS display_name,i.content,i.created_at
FROM huddles h JOIN conversation_items i ON i.company_id=h.company_id AND i.conversation_id=h.chat_conversation_id
LEFT JOIN users u ON u.id=i.author_id
WHERE h.company_id=$1 AND h.id=$2 AND i.seq>$3 AND i.type='user_message' AND i.author_kind='user'
ORDER BY i.seq LIMIT 50;

-- name: GetHuddleAIChatHead :one
SELECT c.head_seq FROM huddles h JOIN conversations c ON c.id=h.chat_conversation_id WHERE h.company_id=$1 AND h.id=$2;

-- name: UpsertHuddleAIUsage :one
INSERT INTO huddle_ai_usage_receipts(company_id,huddle_id,attempt_id,model_session_id,model_seconds,input_tokens,cached_input_tokens,output_tokens,final)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(attempt_id,model_session_id) DO UPDATE SET
model_seconds=EXCLUDED.model_seconds,input_tokens=EXCLUDED.input_tokens,cached_input_tokens=EXCLUDED.cached_input_tokens,output_tokens=EXCLUDED.output_tokens,final=EXCLUDED.final,updated_at=now()
WHERE NOT huddle_ai_usage_receipts.final
AND EXCLUDED.model_seconds>=huddle_ai_usage_receipts.model_seconds
AND EXCLUDED.input_tokens>=huddle_ai_usage_receipts.input_tokens
AND EXCLUDED.cached_input_tokens>=huddle_ai_usage_receipts.cached_input_tokens
AND EXCLUDED.output_tokens>=huddle_ai_usage_receipts.output_tokens
RETURNING *;

-- name: ListHuddleAIUsage :many
SELECT * FROM huddle_ai_usage_receipts WHERE company_id=$1 AND huddle_id=$2;

-- name: RecordHuddleAIAttemptUsage :exec
UPDATE huddle_ai_sessions SET usage_final=$2,model_seconds=$3,spent_usd_ticks=$4,updated_at=now() WHERE id=$1;

-- name: GetHuddleAIUsageReceipt :one
SELECT * FROM huddle_ai_usage_receipts WHERE attempt_id=$1 AND model_session_id=$2;

-- name: CountUncertainHuddleAISessions :one
SELECT count(*) FROM huddle_ai_sessions WHERE company_id=$1 AND huddle_id=$2 AND (state<>'finished' OR NOT usage_final);

-- name: HuddleAIHasHumanGrace :one
SELECT EXISTS(SELECT 1 FROM huddle_connections hc
JOIN company_members m ON m.company_id=hc.company_id AND m.user_id=hc.user_id AND m.status='active'
WHERE hc.company_id=$1 AND hc.huddle_id=$2 AND hc.status='connected')
OR COALESCE((SELECT max(ended_at)>clock_timestamp()-interval '10 seconds' FROM huddle_connections
WHERE company_id=$1 AND huddle_id=$2 AND connected_at IS NOT NULL),false)::boolean AS allowed;

-- name: FinalizeUnclaimedHuddleAIUsage :exec
UPDATE huddle_ai_sessions SET usage_final=true WHERE id=$1 AND capability_hash IS NULL;

-- name: GetHuddleAITiming :one
SELECT EXTRACT(epoch FROM (LEAST(COALESCE(h.ended_at,clock_timestamp()),h.deadline_at)-h.created_at))::float8 AS room_seconds,
COALESCE((SELECT sum(EXTRACT(epoch FROM (LEAST(COALESCE(hc.ended_at,clock_timestamp()),COALESCE(h.ended_at,h.deadline_at))-hc.connected_at)))
FROM huddle_connections hc WHERE hc.huddle_id=h.id AND hc.connected_at IS NOT NULL),0)::float8 AS human_seconds,
COALESCE((SELECT sum(model_seconds) FROM huddle_ai_usage_receipts r WHERE r.huddle_id=h.id),0)::float8 AS model_seconds
FROM huddles h WHERE h.company_id=$1 AND h.id=$2;

-- name: ClaimHuddleAIGreeting :execrows
UPDATE huddle_ai_states SET greeted=true,updated_at=now() WHERE company_id=$1 AND huddle_id=$2 AND NOT greeted;

-- name: CompleteHuddleAIChat :exec
UPDATE huddle_ai_states SET chat_completed_seq=GREATEST(chat_completed_seq,$3),updated_at=now()
WHERE company_id=$1 AND huddle_id=$2 AND $3<=chat_cursor;

-- name: ListPendingHuddleAIChat :many
SELECT i.id,i.seq,i.author_id,COALESCE(u.display_name,'Former member')::text AS display_name,i.content,i.created_at
FROM huddle_ai_states s JOIN huddles h ON h.id=s.huddle_id
JOIN conversation_items i ON i.company_id=h.company_id AND i.conversation_id=h.chat_conversation_id
LEFT JOIN users u ON u.id=i.author_id
WHERE s.company_id=$1 AND s.huddle_id=$2 AND i.seq>s.chat_completed_seq AND i.seq<=s.chat_cursor
AND i.type='user_message' AND i.author_kind='user' ORDER BY i.seq LIMIT 50;

-- name: HuddleAIHasRevokedListener :one
SELECT EXISTS(SELECT 1 FROM companies AS company WHERE company.id=sqlc.arg(company_id)::uuid AND company.deleting_at IS NOT NULL) OR EXISTS (
 SELECT 1 FROM huddle_connections hc JOIN huddles h ON h.id=hc.huddle_id
 LEFT JOIN company_members m ON m.company_id=hc.company_id AND m.user_id=hc.user_id
 LEFT JOIN users u ON u.id=hc.user_id
 WHERE hc.company_id=sqlc.arg(company_id)::uuid AND hc.huddle_id=sqlc.arg(huddle_id)::uuid AND hc.status='connected'
 AND (m.user_id IS NULL OR u.id IS NULL OR m.status<>'active' OR u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL
 OR NOT (EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.company_id=h.company_id AND cm.conversation_id=h.source_conversation_id AND cm.user_id=hc.user_id AND cm.status='active')
 OR EXISTS(SELECT 1 FROM huddle_invitations hi WHERE hi.company_id=h.company_id AND hi.huddle_id=h.id AND hi.invitee_user_id=hc.user_id AND hi.status='accepted')))
)::boolean;
