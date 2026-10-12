-- name: ListCalendarEvents :many
SELECT sqlc.embed(e),c.type AS calendar_type,COALESCE(co.name,'My calendar')::text AS calendar_name,
 COALESCE(c.company_id,e.sharing_company_id) AS company_id,
 EXISTS (SELECT 1 FROM company_members organizer WHERE organizer.company_id=e.sharing_company_id
    AND organizer.user_id=e.user_id AND organizer.status='active') AS sharing_active,
 COALESCE(organizer_user.display_name,'Former member')::text AS organizer_name,
 COALESCE(c.user_id=sqlc.arg(actor_user_id) OR (c.type='company' AND m.user_id IS NOT NULL AND co.deleting_at IS NULL AND (e.user_id=sqlc.arg(actor_user_id) OR m.role=ANY(sqlc.arg(actor_roles)::text[]))),false)::boolean AS can_edit
FROM calendar_events e
JOIN calendars c ON c.id=e.calendar_id
LEFT JOIN users organizer_user ON organizer_user.id=e.user_id
LEFT JOIN companies co ON co.id=COALESCE(c.company_id,e.sharing_company_id)
LEFT JOIN company_members m ON m.company_id=co.id AND m.user_id=sqlc.arg(actor_user_id) AND m.status='active'
WHERE (
 (c.user_id=sqlc.arg(actor_user_id))
 OR (co.deleting_at IS NULL AND m.user_id IS NOT NULL AND (
   (c.company_id=sqlc.arg(actor_company_id)::uuid)
   OR EXISTS(SELECT 1 FROM calendar_attendees a WHERE a.event_id=e.id AND a.user_id=m.user_id
     AND (c.type='company' OR EXISTS(SELECT 1 FROM company_members organizer WHERE organizer.company_id=co.id AND organizer.user_id=e.user_id AND organizer.status='active')))
 ))
) AND e.start_local < sqlc.arg(before_local)::timestamp
 AND (e.end_local >= sqlc.arg(after_local)::timestamp OR e.recurrence <> 'none')
 ORDER BY e.start_local,e.id LIMIT 2001;

-- name: GetVisibleCalendarEvent :one
SELECT sqlc.embed(e),c.type AS calendar_type,COALESCE(co.name,'My calendar')::text AS calendar_name,
 COALESCE(c.company_id,e.sharing_company_id) AS company_id,
 EXISTS (SELECT 1 FROM company_members organizer WHERE organizer.company_id=e.sharing_company_id
    AND organizer.user_id=e.user_id AND organizer.status='active') AS sharing_active,
 COALESCE(organizer_user.display_name,'Former member')::text AS organizer_name,
 COALESCE(c.user_id=sqlc.arg(actor_user_id) OR (c.type='company' AND m.user_id IS NOT NULL AND co.deleting_at IS NULL AND (e.user_id=sqlc.arg(actor_user_id) OR m.role=ANY(sqlc.arg(actor_roles)::text[]))),false)::boolean AS can_edit
FROM calendar_events e
JOIN calendars c ON c.id=e.calendar_id
LEFT JOIN users organizer_user ON organizer_user.id=e.user_id
LEFT JOIN companies co ON co.id=COALESCE(c.company_id,e.sharing_company_id)
LEFT JOIN company_members m ON m.company_id=co.id AND m.user_id=sqlc.arg(actor_user_id) AND m.status='active'
WHERE (
 (c.user_id=sqlc.arg(actor_user_id))
 OR (co.deleting_at IS NULL AND m.user_id IS NOT NULL AND (
   (c.company_id=sqlc.arg(actor_company_id)::uuid)
   OR EXISTS(SELECT 1 FROM calendar_attendees a WHERE a.event_id=e.id AND a.user_id=m.user_id
     AND (c.type='company' OR EXISTS(SELECT 1 FROM company_members organizer WHERE organizer.company_id=co.id AND organizer.user_id=e.user_id AND organizer.status='active')))
 ))
) AND e.id=sqlc.arg(id);

-- name: CreateCalendarEvent :one
INSERT INTO calendar_events (user_id, calendar_id, sharing_company_id, type, title, description, location, color, all_day, start_local, end_local, time_zone, recurrence, recurrence_until, recurrence_interval, recurrence_weekdays, recurrence_monthly_mode, recurrence_count, reminder_minutes, reminder_next_at)
VALUES (sqlc.arg(user_id), sqlc.arg(calendar_id), sqlc.narg(sharing_company_id), sqlc.arg(type), sqlc.arg(title), sqlc.arg(description), sqlc.arg(location), sqlc.arg(color), sqlc.arg(all_day), sqlc.arg(start_local), sqlc.arg(end_local), sqlc.arg(time_zone), sqlc.arg(recurrence), sqlc.narg(recurrence_until), sqlc.arg(recurrence_interval), sqlc.arg(recurrence_weekdays), sqlc.arg(recurrence_monthly_mode), sqlc.narg(recurrence_count), sqlc.narg(reminder_minutes), (sqlc.arg(start_local)::timestamp AT TIME ZONE sqlc.arg(time_zone)::text) - make_interval(mins => sqlc.narg(reminder_minutes)::integer))
RETURNING *;

-- name: LockCalendarEvent :one
SELECT * FROM calendar_events WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: UpdateCalendarEvent :one
UPDATE calendar_events SET sharing_company_id = sqlc.narg(sharing_company_id), type = sqlc.arg(type), title = sqlc.arg(title), description = sqlc.arg(description), location = sqlc.arg(location), color = sqlc.arg(color), all_day = sqlc.arg(all_day), start_local = sqlc.arg(start_local), end_local = sqlc.arg(end_local), time_zone = sqlc.arg(time_zone), recurrence = sqlc.arg(recurrence), recurrence_until = sqlc.narg(recurrence_until), recurrence_interval = sqlc.arg(recurrence_interval), recurrence_weekdays = sqlc.arg(recurrence_weekdays), recurrence_monthly_mode = sqlc.arg(recurrence_monthly_mode), recurrence_count = sqlc.narg(recurrence_count), reminder_minutes = sqlc.narg(reminder_minutes), reminder_next_at = (sqlc.arg(start_local)::timestamp AT TIME ZONE sqlc.arg(time_zone)::text) - make_interval(mins => sqlc.narg(reminder_minutes)::integer), version = version + 1, updated_at = statement_timestamp()
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_revision)
RETURNING *;

-- name: DeleteCalendarEvent :exec
DELETE FROM calendar_events WHERE id = sqlc.arg(id);


-- name: EnsurePersonalCalendar :exec
INSERT INTO calendars(type,user_id) VALUES ('personal',$1)
ON CONFLICT(user_id) DO NOTHING;

-- name: EnsureCompanyCalendar :exec
INSERT INTO calendars(type,company_id) VALUES ('company',$1)
ON CONFLICT(company_id) DO NOTHING;

-- name: ListCalendarCatalog :many
SELECT c.id,c.type,COALESCE(co.name,'My calendar')::text AS name,c.company_id
FROM calendars c LEFT JOIN companies co ON co.id=c.company_id
WHERE c.user_id=sqlc.arg(actor_user_id) OR (c.company_id=sqlc.arg(actor_company_id)::uuid AND co.deleting_at IS NULL
 AND EXISTS(SELECT 1 FROM company_members m WHERE m.company_id=c.company_id AND m.user_id=sqlc.arg(actor_user_id) AND m.status='active'))
ORDER BY c.type DESC,c.id;

-- name: ListCalendarAttendees :many
SELECT a.*,u.display_name,u.email FROM calendar_attendees a
JOIN calendar_events e ON e.id=a.event_id
JOIN calendars c ON c.id=e.calendar_id
JOIN users u ON u.id=a.user_id AND u.disabled_at IS NULL
JOIN company_members m ON m.company_id=a.company_id AND m.user_id=a.user_id AND m.status='active'
WHERE a.event_id=ANY(sqlc.arg(event_ids)::uuid[])
 AND (c.type='company' OR EXISTS(SELECT 1 FROM company_members organizer WHERE organizer.company_id=a.company_id AND organizer.user_id=e.user_id AND organizer.status='active'))
 ORDER BY a.event_id,u.display_name,a.user_id;

-- name: ReplaceCalendarAttendees :exec
DELETE FROM calendar_attendees WHERE event_id=sqlc.arg(event_id) AND NOT(user_id=ANY(sqlc.arg(user_ids)::uuid[]));

-- name: AddCalendarAttendee :exec
INSERT INTO calendar_attendees(event_id,company_id,user_id) VALUES(sqlc.arg(event_id),sqlc.arg(company_id),sqlc.arg(user_id))
ON CONFLICT(event_id,user_id) DO NOTHING;

-- name: RespondCalendarInvitation :one
UPDATE calendar_attendees SET response_status=sqlc.arg(response_status),updated_at=statement_timestamp()
WHERE event_id=sqlc.arg(event_id) AND user_id=sqlc.arg(user_id)
RETURNING updated_at;

-- name: ResetCalendarResponses :exec
UPDATE calendar_attendees SET response_status='needs_action',updated_at=statement_timestamp() WHERE event_id=$1;

-- name: ListCalendarOccurrenceStates :many
SELECT s.* FROM calendar_occurrence_states s WHERE event_id=ANY(sqlc.arg(event_ids)::uuid[])
 AND occurrence_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date;

-- name: ListCalendarReminderStates :many
SELECT s.* FROM calendar_reminder_states s WHERE event_id=ANY(sqlc.arg(event_ids)::uuid[]) AND user_id=sqlc.arg(user_id)
 AND occurrence_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date;

-- name: CancelCalendarOccurrence :exec
INSERT INTO calendar_occurrence_states(event_id,occurrence_date,cancelled) VALUES($1,$2,true)
ON CONFLICT(event_id,occurrence_date) DO UPDATE SET cancelled=true,updated_at=statement_timestamp();

-- name: SetCalendarReminderState :exec
INSERT INTO calendar_reminder_states(event_id,user_id,occurrence_date,completed)
VALUES(sqlc.arg(event_id),sqlc.arg(user_id),sqlc.arg(occurrence_date),sqlc.arg(completed))
ON CONFLICT(event_id,user_id,occurrence_date) DO UPDATE SET
 completed=EXCLUDED.completed,updated_at=statement_timestamp();

-- name: LockCalendarConversation :one
SELECT id FROM conversations
WHERE id=sqlc.arg(conversation_id) AND company_id=sqlc.arg(company_id)
 AND kind='direct' AND created_by_user_id=sqlc.arg(user_id)
FOR UPDATE;

-- name: LockCalendarRun :one
SELECT id FROM runs
WHERE id=sqlc.arg(run_id) AND company_id=sqlc.arg(company_id) AND conversation_id=sqlc.arg(conversation_id)
 AND agent_id=sqlc.arg(agent_id) AND status='running'
FOR UPDATE;

-- name: GetCalendarToolReceipt :one
SELECT * FROM calendar_tool_receipts WHERE run_id=$1 AND request_key=$2;

-- name: CreateCalendarToolReceipt :exec
INSERT INTO calendar_tool_receipts(run_id,request_key,payload_hash,response) VALUES($1,$2,$3,$4);

-- name: SetCalendarSharingCompany :exec
UPDATE calendar_events SET sharing_company_id=sqlc.narg(sharing_company_id) WHERE id=sqlc.arg(id);

-- name: ListDueCalendarReminders :many
SELECT e.id,e.version,e.user_id,c.type AS calendar_type,
 COALESCE(c.company_id,e.sharing_company_id,'00000000-0000-0000-0000-000000000000'::uuid)::uuid AS company_id
FROM calendar_events e JOIN calendars c ON c.id=e.calendar_id
WHERE e.reminder_next_at <= sqlc.arg(now_at)::timestamptz
ORDER BY e.reminder_next_at,e.id LIMIT 100;

-- name: AdvanceCalendarReminder :exec
UPDATE calendar_events SET reminder_next_at=sqlc.narg(next_at)::timestamptz WHERE id=sqlc.arg(id);

-- name: CalendarReminderSuppressed :one
SELECT EXISTS(SELECT 1 FROM calendar_occurrence_states s WHERE s.event_id=sqlc.arg(event_id) AND s.occurrence_date=sqlc.arg(occurrence_date)::date AND s.cancelled)
 OR EXISTS(SELECT 1 FROM calendar_reminder_states r WHERE r.event_id=sqlc.arg(event_id) AND r.user_id=sqlc.arg(user_id) AND r.occurrence_date=sqlc.arg(occurrence_date)::date AND r.completed) AS suppressed;
