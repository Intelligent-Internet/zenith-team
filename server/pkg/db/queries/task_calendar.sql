-- name: SetTaskAssignment :one
UPDATE tasks SET assignment_status = sqlc.arg(assignment_status),
    version = version + 1, updated_at = now()
WHERE id = sqlc.arg(task_id) AND company_id = sqlc.arg(company_id)
RETURNING *;

-- name: InsertTaskSchedule :one
INSERT INTO task_schedules (company_id, project_id, template_task_id, trigger, next_occurrence_at, template_title, template_description)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: UpdateTaskScheduleBrief :exec
UPDATE task_schedules SET template_title = sqlc.arg(title), template_description = sqlc.arg(description),
    version = version + 1, updated_at = now()
WHERE company_id = sqlc.arg(company_id) AND template_task_id = sqlc.arg(task_id);

-- name: GetTaskSchedule :one
SELECT * FROM task_schedules WHERE id = $1 AND company_id = $2;

-- name: AdvanceTaskScheduleGeneration :one
UPDATE task_schedules SET generation = generation + 1, version = version + 1, updated_at = now()
WHERE id = $1 AND company_id = $2 RETURNING *;

-- name: LockTaskSchedule :one
SELECT * FROM task_schedules WHERE id = $1 AND company_id = $2 FOR UPDATE;

-- name: UpdateTaskSchedule :one
UPDATE task_schedules SET trigger = sqlc.arg(trigger), paused = sqlc.arg(paused),
    next_occurrence_at = sqlc.narg(next_occurrence_at), version = version + 1, updated_at = now()
WHERE id = sqlc.arg(id) AND company_id = sqlc.arg(company_id)
RETURNING *;

-- name: SetTaskCalendar :one
UPDATE tasks SET scheduled_at = sqlc.narg(scheduled_at), task_schedule_id = sqlc.narg(task_schedule_id),
    schedule_occurrence_at = sqlc.narg(schedule_occurrence_at),
    task_schedule_generation = sqlc.arg(task_schedule_generation),
    schedule_disposition = sqlc.arg(schedule_disposition), schedule_fired_at = sqlc.narg(schedule_fired_at),
    assignment_status = sqlc.arg(assignment_status), status = sqlc.arg(status), version = version + 1, updated_at = now()
WHERE id = sqlc.arg(task_id) AND company_id = sqlc.arg(company_id)
RETURNING *;

-- name: ListDueTaskSchedules :many
SELECT s.* FROM task_schedules s JOIN tasks t ON t.id = s.template_task_id
JOIN projects p ON p.id = s.project_id AND p.archived_at IS NULL
JOIN company_members m ON m.company_id = t.company_id AND m.user_id = t.assignee_user_id AND m.status = 'active'
JOIN users u ON u.id = m.user_id AND u.disabled_at IS NULL
JOIN project_access a ON a.company_id = t.company_id AND a.project_id = t.project_id AND a.user_id = t.assignee_user_id
WHERE t.assignment_status = 'accepted' AND NOT s.paused AND s.next_occurrence_at <= sqlc.arg(now_at)
    AND (NOT sqlc.arg(has_after)::boolean OR (s.next_occurrence_at, s.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid))
ORDER BY s.next_occurrence_at, s.id LIMIT sqlc.arg(page_size);

-- name: ListDueCalendarTasks :many
SELECT t.* FROM tasks t JOIN task_schedules s ON s.id = t.task_schedule_id
JOIN tasks template ON template.id = s.template_task_id
JOIN projects p ON p.id = t.project_id AND p.archived_at IS NULL
JOIN company_members m ON m.company_id = t.company_id AND m.user_id = t.assignee_user_id AND m.status = 'active'
JOIN users u ON u.id = m.user_id AND u.disabled_at IS NULL
JOIN project_access a ON a.company_id = t.company_id AND a.project_id = t.project_id AND a.user_id = t.assignee_user_id
WHERE t.schedule_disposition = 'scheduled' AND t.assignment_status = 'accepted'
    AND t.task_schedule_generation = s.generation
    AND t.scheduled_at <= sqlc.arg(now_at) AND NOT s.paused AND template.assignment_status = 'accepted'
    AND (NOT sqlc.arg(has_after)::boolean OR (t.scheduled_at, t.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid))
ORDER BY t.scheduled_at, t.id LIMIT sqlc.arg(page_size);

-- name: GetTaskOccurrence :one
SELECT * FROM tasks WHERE task_schedule_id = $1 AND task_schedule_generation = $2 AND schedule_occurrence_at = $3;

-- name: GetTaskScheduleRunState :one
SELECT EXISTS(SELECT 1 FROM tasks t JOIN runs r ON r.task_id = t.id
    WHERE t.task_schedule_id = sqlc.arg(schedule_id) AND r.company_id = sqlc.arg(company_id)
        AND r.status NOT IN ('completed', 'failed', 'cancelled'))::boolean AS active_run,
    EXISTS(SELECT 1 FROM tasks t
        WHERE t.task_schedule_id = sqlc.arg(schedule_id) AND t.company_id = sqlc.arg(company_id)
            AND t.task_schedule_generation = sqlc.arg(generation)
            AND t.schedule_disposition IN ('scheduled', 'started')
            AND t.scheduled_at > sqlc.arg(scheduled_at) AND t.scheduled_at <= sqlc.arg(now_at)
    )::boolean AS newer_due_occurrence;

-- name: ListCalendarTaskRows :many
SELECT sqlc.embed(tasks), task_schedules.trigger, task_schedules.paused,
    task_schedules.version AS schedule_version, task_schedules.template_task_id
FROM tasks JOIN task_schedules ON task_schedules.id = tasks.task_schedule_id
JOIN project_access a ON a.company_id = tasks.company_id AND a.project_id = tasks.project_id
    AND a.user_id = sqlc.arg(actor_user_id)
WHERE tasks.company_id = sqlc.arg(company_id)
    AND tasks.scheduled_at >= sqlc.arg(from_at) AND tasks.scheduled_at < sqlc.arg(to_at)
ORDER BY tasks.scheduled_at, tasks.id LIMIT 2001;

-- name: ListCalendarTaskSeries :many
SELECT sqlc.embed(task_schedules), sqlc.embed(tasks)
FROM task_schedules JOIN tasks ON tasks.id = task_schedules.template_task_id
JOIN project_access a ON a.company_id = task_schedules.company_id AND a.project_id = task_schedules.project_id
    AND a.user_id = sqlc.arg(actor_user_id)
WHERE task_schedules.company_id = sqlc.arg(company_id) AND NOT task_schedules.paused
    AND task_schedules.next_occurrence_at < sqlc.arg(to_at)
ORDER BY task_schedules.id LIMIT 201;

-- name: ListMaterializedTaskOccurrences :many
SELECT t.task_schedule_id, t.schedule_occurrence_at FROM tasks t
JOIN task_schedules s ON s.id = t.task_schedule_id
WHERE t.company_id = sqlc.arg(company_id) AND t.task_schedule_id = ANY(sqlc.arg(schedule_ids)::uuid[])
    AND t.task_schedule_generation = s.generation
    AND t.schedule_occurrence_at >= s.next_occurrence_at
    AND t.schedule_occurrence_at >= sqlc.arg(from_at) AND t.schedule_occurrence_at < sqlc.arg(to_at)
ORDER BY t.task_schedule_id, t.schedule_occurrence_at LIMIT 2001;

-- name: CopyTaskOccurrenceDependencies :exec
INSERT INTO task_dependencies (company_id, project_id, dependent_task_id, prerequisite_task_id)
SELECT d.company_id, d.project_id, sqlc.arg(task_id)::uuid, d.prerequisite_task_id FROM task_dependencies d
WHERE d.dependent_task_id = sqlc.arg(template_task_id) ON CONFLICT DO NOTHING;

-- name: CopyTaskOccurrenceResultPolicy :exec
UPDATE tasks AS occurrence
SET result_proposal_only = template.result_proposal_only,
    result_review_required = template.result_review_required
FROM tasks AS template
WHERE occurrence.id = sqlc.arg(task_id) AND template.id = sqlc.arg(template_task_id)
    AND occurrence.company_id = template.company_id;

-- name: GetCalendarTaskTarget :one
SELECT * FROM tasks WHERE company_id = $1 AND id = $2;

-- name: ListUnstartedTaskOccurrences :many
SELECT * FROM tasks
WHERE task_schedule_id = sqlc.arg(schedule_id) AND tasks.id <> sqlc.arg(excluded_task_id)
    AND task_schedule_generation = sqlc.arg(generation)
    AND ((schedule_disposition = 'scheduled' AND sqlc.arg(include_scheduled)::boolean) OR (
        schedule_disposition = 'started' AND EXISTS (
            SELECT 1 FROM runs
            WHERE runs.task_id = tasks.id AND runs.company_id = tasks.company_id
                AND runs.task_scheduled
                AND runs.status IN ('queued', 'deferred')
        )
    ))
ORDER BY tasks.id FOR UPDATE;

-- name: ListRetainedTaskOccurrences :many
-- The Company Plan caller already holds the scoped Task and Conversation locks.
-- Include old generations so removing a definition also detaches their history.
SELECT t.* FROM tasks t
JOIN task_schedules s ON s.id = t.task_schedule_id
WHERE t.company_id = sqlc.arg(company_id)
    AND s.template_task_id = ANY(sqlc.arg(deleted_task_ids)::uuid[])
    AND t.id <> ALL(sqlc.arg(deleted_task_ids)::uuid[])
ORDER BY t.id;
