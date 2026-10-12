-- The Task a Run was created for. runs.task_id is the live pointer and goes
-- NULL when the Task is deleted; this copy never changes, so a deleted Task's
-- Runs stay listable by its id. Attribution only, so no foreign key.
ALTER TABLE runs ADD COLUMN source_task_id uuid;

-- The one read of the frozen source identity. Task Runs older than the frozen
-- source fall back to their live Task pointer.
UPDATE runs
SET source_task_id = COALESCE(
    CASE WHEN context #>> '{source,entityId}' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        THEN (context #>> '{source,entityId}')::uuid END,
    task_id)
WHERE run_type = 'task';

-- Serves the Run list filtered by Task, in its (created_at, id) page order.
CREATE INDEX runs_source_task_created_idx
    ON runs (source_task_id, created_at DESC, id DESC) WHERE source_task_id IS NOT NULL;
