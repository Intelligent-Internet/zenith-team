-- Huddle locks serialize proposal writes. Submission shares the Task transaction.

-- name: GetHuddleProposalReplay :one
SELECT * FROM huddle_work_proposals
WHERE company_id=$1 AND huddle_id=$2 AND invitation_revision=$3 AND request_key=$4;

-- name: ListHuddleProposals :many
SELECT * FROM huddle_work_proposals WHERE company_id=$1 AND huddle_id=$2
ORDER BY created_at DESC,id DESC LIMIT 20;

-- name: CountHuddleProposals :one
SELECT count(*) FROM huddle_work_proposals WHERE company_id=$1 AND huddle_id=$2;

-- name: GetHuddleProposal :one
SELECT * FROM huddle_work_proposals WHERE company_id=$1 AND huddle_id=$2 AND id=$3;

-- name: GetHuddleProposalTarget :one
SELECT p.title AS project_title,COALESCE(t.key,'')::text AS task_key
FROM projects p LEFT JOIN tasks t ON t.company_id=p.company_id AND t.project_id=p.id AND t.id=sqlc.narg(task_id)::uuid
WHERE p.company_id=sqlc.arg(company_id)::uuid AND p.id=sqlc.arg(project_id)::uuid AND p.archived_at IS NULL
AND (sqlc.narg(task_id)::uuid IS NULL OR t.id IS NOT NULL);

-- name: CreateHuddleProposal :one
INSERT INTO huddle_work_proposals (
 company_id,huddle_id,attempt_id,invitation_revision,created_by_agent_id,agent_name,
 project_id,project_title,task_id,task_key,request_key,request_hash,title,brief,expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING *;

-- name: UpdateHuddleProposal :one
UPDATE huddle_work_proposals SET title=$4,brief=$5,state=$6,revision=revision+1,
updated_by_user_id=$7,updated_at=now()
WHERE company_id=$1 AND huddle_id=$2 AND id=$3 RETURNING *;

-- Receipt-bound work only: never substitute a later unrelated Task Run. Follow
-- automatic retries and explicit human-input continuations, capped defensively.
-- No runtime identity, private transcript, reasoning or raw error is exposed.
-- A submitted Task's work is kept only where reader_user_id may read its
-- Project by the delegate rule (D31): the person who shared Company context
-- for the voice tools and spoken notices, the viewer for the status a person
-- reads. Go binds the permissions and the default set.
-- name: ReadHuddleWork :many
WITH RECURSIVE proposals AS (
 SELECT * FROM huddle_work_proposals
 WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
   AND state='submitted'
   AND (sqlc.arg(proposal_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid OR id=sqlc.arg(proposal_id)::uuid)
 ORDER BY created_at DESC,id DESC LIMIT 20
), lineage AS (
 SELECT p.id AS proposal_id,r.id AS run_id,r.company_id,r.task_id,r.conversation_id,0 AS depth
 FROM proposals p
 JOIN tasks t ON t.id=p.submitted_task_id AND t.company_id=p.company_id
   AND delegate_permission_held(t.company_id,sqlc.arg(reader_user_id)::uuid,NULL::uuid,t.project_id,
     sqlc.arg(read_permission)::text,sqlc.arg(read_permission)::text,sqlc.arg(company_default_permissions)::text[])
 JOIN conversations c ON c.id=t.conversation_id AND c.company_id=t.company_id AND c.kind='task'
 JOIN runs r ON r.id=p.submitted_run_id AND r.company_id=p.company_id
   AND r.task_id=t.id AND r.conversation_id=c.id
 UNION
 SELECT l.proposal_id,r.id,r.company_id,r.task_id,r.conversation_id,l.depth+1
 FROM lineage l
 JOIN LATERAL (
   SELECT child.id FROM runs child WHERE child.parent_run_id=l.run_id AND child.company_id=l.company_id
   UNION
   SELECT q.continuation_run_id FROM questions q WHERE q.origin_run_id=l.run_id AND q.company_id=l.company_id
   UNION
   SELECT a.continuation_run_id FROM approvals a WHERE a.origin_run_id=l.run_id AND a.company_id=l.company_id
 ) edge ON true
 JOIN runs r ON r.id=edge.id AND r.company_id=l.company_id AND r.task_id=l.task_id AND r.conversation_id=l.conversation_id
 WHERE l.depth<32
)
SELECT p.id AS proposal_id,p.title,p.submitted_task_key,p.voice_notice_keys,
 t.id AS task_id,COALESCE(t.status,'')::text AS task_status,
 COALESCE(r.id,'00000000-0000-0000-0000-000000000000'::uuid)::uuid AS run_id,COALESCE(r.status,'')::text AS run_status,
 COALESCE(rt.status='online',false)::boolean AS runtime_online,
 EXISTS(SELECT 1 FROM questions q JOIN lineage l ON l.run_id=q.origin_run_id AND l.company_id=q.company_id
   WHERE l.proposal_id=p.id AND q.task_id=t.id AND q.conversation_id=t.conversation_id AND q.status='pending' AND q.expires_at>now()
   UNION ALL SELECT 1 FROM approvals a JOIN lineage l ON l.run_id=a.origin_run_id AND l.company_id=a.company_id
   WHERE l.proposal_id=p.id AND a.task_id=t.id AND a.conversation_id=t.conversation_id AND a.status='pending' AND a.expires_at>now())::boolean AS waiting_input,
 EXISTS(SELECT 1 FROM questions q JOIN lineage l ON l.run_id=q.origin_run_id AND l.company_id=q.company_id
   WHERE l.proposal_id=p.id AND q.task_id=t.id AND q.conversation_id=t.conversation_id AND q.continuation_status='pending'
   UNION ALL SELECT 1 FROM approvals a JOIN lineage l ON l.run_id=a.origin_run_id AND l.company_id=a.company_id
   WHERE l.proposal_id=p.id AND a.task_id=t.id AND a.conversation_id=t.conversation_id AND a.continuation_status='pending')::boolean AS resuming,
 COALESCE(r.task_execution_generation<>t.execution_generation,false)::boolean AS newer_task_execution,
 (answer.id IS NOT NULL)::boolean AS has_result,
 answer.id AS result_message_id,COALESCE(answer.seq,0)::bigint AS result_message_seq,answer.conversation_id AS result_conversation_id,
 CASE WHEN sqlc.arg(include_result)::boolean THEN left(COALESCE(answer.content,''),2000) ELSE '' END::text AS result_text,
 (sqlc.arg(include_result)::boolean AND COALESCE(length(answer.content)>2000,false))::boolean AS result_truncated,
 GREATEST(p.updated_at,t.updated_at,r.updated_at)::timestamptz AS updated_at
FROM proposals p
LEFT JOIN tasks t ON t.id=p.submitted_task_id AND t.company_id=p.company_id
 AND delegate_permission_held(t.company_id,sqlc.arg(reader_user_id)::uuid,NULL::uuid,t.project_id,
   sqlc.arg(read_permission)::text,sqlc.arg(read_permission)::text,sqlc.arg(company_default_permissions)::text[])
LEFT JOIN LATERAL (
 SELECT run.* FROM lineage l JOIN runs run ON run.id=l.run_id AND run.company_id=l.company_id
 WHERE l.proposal_id=p.id
 ORDER BY (run.status IN ('queued','deferred','dispatched','running','waiting_local_directory')) DESC,
   run.created_at DESC,run.id DESC LIMIT 1
) r ON true
LEFT JOIN runtimes rt ON rt.id=r.runtime_id AND rt.company_id=p.company_id
LEFT JOIN conversation_items answer ON answer.company_id=p.company_id AND answer.conversation_id=r.conversation_id
 AND answer.run_id=r.id AND answer.item_key=r.final_answer_item_key AND answer.type='agent_text'
 AND answer.deleted_at IS NULL AND r.status='completed'
ORDER BY p.created_at DESC,p.id DESC;

-- name: SubmitHuddleProposal :one
UPDATE huddle_work_proposals SET state='submitted',revision=revision+1,
submitted_by_user_id=$4,submitted_revision=revision,submission_hash=$5,
submitted_task_id=$6,submitted_task_key=$7,submitted_run_id=$8,submitted_at=now(),updated_at=now()
WHERE company_id=$1 AND huddle_id=$2 AND id=$3 RETURNING *;

-- name: ClaimHuddleWorkNotice :execrows
UPDATE huddle_work_proposals
SET voice_notice_keys=array_append(voice_notice_keys,sqlc.arg(notice_key)::text)
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
 AND id=sqlc.arg(proposal_id)::uuid AND state='submitted'
 AND NOT(sqlc.arg(notice_key)::text=ANY(voice_notice_keys))
 AND cardinality(voice_notice_keys)<128;
