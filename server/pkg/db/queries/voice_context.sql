-- Company-shared projections only. Huddle authority is checked by the caller
-- in the same transaction. Never join private conversation items or Run output.
-- The voice assistant is a delegate: Projects and their Tasks are
-- bounded by the delegate rule (delegate_permission_held), once per candidate
-- Project. The person who switched Company context on is its Authorizer: it
-- reads with that person's access alone, and everyone in the call hears the
-- answer. It answers into no Task chat. Go binds the permissions and the
-- default set.

-- name: GetVoiceCompanyContext :one
SELECT id,left(name,200)::text AS name,left(business_context,2000)::text AS description,
 (length(name)>200 OR length(business_context)>2000) AS truncated,updated_at
FROM companies WHERE id=$1 AND deleting_at IS NULL;

-- name: ListVoiceProjects :many
SELECT p.id,p.key,left(p.title,200)::text AS title,left(p.description,400)::text AS description,
 (length(p.title)>200 OR length(p.description)>400) AS truncated,p.updated_at
FROM projects p JOIN companies c ON c.id=p.company_id AND c.deleting_at IS NULL
WHERE p.company_id=sqlc.arg(company_id)::uuid AND p.archived_at IS NULL
 AND (sqlc.arg(after_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid OR p.id>sqlc.arg(after_id)::uuid)
 AND delegate_permission_held(p.company_id,sqlc.arg(context_granted_by_user_id)::uuid,NULL::uuid,
   p.id,sqlc.arg(read_permission)::text,sqlc.arg(read_permission)::text,
   sqlc.arg(company_default_permissions)::text[])
ORDER BY p.id LIMIT 21;

-- name: ReadVoiceTasks :many
SELECT t.id,t.project_id,t.number,t.key,left(t.title,200)::text AS title,t.status,
 left(t.description,CASE WHEN sqlc.arg(task_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid THEN 300 ELSE 2000 END)::text AS description,
 (length(t.title)>200 OR length(t.description)>CASE WHEN sqlc.arg(task_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid THEN 300 ELSE 2000 END) AS truncated,
 t.due_date,t.updated_at,left(COALESCE(u.display_name,''),200)::text AS assignee,
 (SELECT count(*) FROM task_dependencies d JOIN tasks prerequisite
 ON prerequisite.id=d.prerequisite_task_id AND prerequisite.company_id=d.company_id
 WHERE d.company_id=t.company_id AND d.dependent_task_id=t.id AND prerequisite.status<>'done')::bigint AS blocked_by_count
FROM tasks t
JOIN companies company ON company.id=t.company_id AND company.deleting_at IS NULL
JOIN conversations c ON c.id=t.conversation_id AND c.company_id=t.company_id AND c.kind='task'
LEFT JOIN users u ON u.id=t.assignee_user_id
WHERE t.company_id=sqlc.arg(company_id)::uuid
 -- One delegate check per Project, never one per Task.
 AND t.project_id IN (SELECT readable_project.id FROM projects readable_project
   WHERE readable_project.company_id=sqlc.arg(company_id)::uuid
     AND delegate_permission_held(readable_project.company_id,sqlc.arg(context_granted_by_user_id)::uuid,NULL::uuid,
       readable_project.id,sqlc.arg(read_permission)::text,
       sqlc.arg(read_permission)::text,sqlc.arg(company_default_permissions)::text[]))
 AND (sqlc.arg(task_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid OR t.id=sqlc.arg(task_id)::uuid)
 AND (sqlc.arg(project_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid OR t.project_id=sqlc.arg(project_id)::uuid)
 AND (sqlc.arg(status)::text='' OR t.status=sqlc.arg(status)::text)
 AND (sqlc.arg(query)::text='' OR strpos(lower(t.title),lower(sqlc.arg(query)::text))>0 OR strpos(lower(t.key),lower(sqlc.arg(query)::text))>0)
 AND (sqlc.arg(before_number)::bigint=0 OR t.number<sqlc.arg(before_number)::bigint)
ORDER BY t.number DESC LIMIT 21;
