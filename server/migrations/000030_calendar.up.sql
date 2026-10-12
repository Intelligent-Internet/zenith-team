-- Personal and Company calendars, invitations, Agent tools and durable Inbox notifications.
CREATE TABLE calendars (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type text NOT NULL CHECK (type IN ('personal', 'company')),
    user_id uuid UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    company_id uuid UNIQUE REFERENCES companies(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK ((type = 'personal' AND user_id IS NOT NULL AND company_id IS NULL)
        OR (type = 'company' AND company_id IS NOT NULL AND user_id IS NULL))
);

CREATE TABLE calendar_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    calendar_id uuid NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    sharing_company_id uuid REFERENCES companies(id) ON DELETE SET NULL,
    type text NOT NULL CHECK (type IN ('event', 'reminder')),
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    location text NOT NULL DEFAULT '',
    color text NOT NULL CHECK (color IN ('sage', 'sky', 'lavender', 'rose', 'sand')),
    all_day boolean NOT NULL DEFAULT false,
    start_local timestamp NOT NULL,
    end_local timestamp NOT NULL,
    time_zone text NOT NULL,
    recurrence text NOT NULL DEFAULT 'none' CHECK (recurrence IN ('none', 'daily', 'weekly', 'monthly', 'yearly')),
    recurrence_until date,
    reminder_minutes integer,
    reminder_next_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    UNIQUE (user_id, id)
);
CREATE INDEX calendar_events_user_start_idx ON calendar_events (user_id, start_local);

CREATE TABLE calendar_occurrence_states (
    event_id uuid NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    occurrence_date date NOT NULL,
    cancelled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (event_id, occurrence_date)
);

CREATE INDEX calendar_events_calendar_start_idx ON calendar_events(calendar_id,start_local);
CREATE INDEX calendar_events_reminder_due_idx ON calendar_events(reminder_next_at,id) WHERE reminder_next_at IS NOT NULL;

CREATE TABLE calendar_attendees (
    event_id uuid NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    company_id uuid NOT NULL,
    user_id uuid NOT NULL,
    response_status text NOT NULL DEFAULT 'needs_action' CHECK (response_status IN ('needs_action','accepted','tentative','declined')),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (event_id,user_id),
    FOREIGN KEY (company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);
CREATE INDEX calendar_attendees_user_idx ON calendar_attendees(user_id,event_id);
CREATE TABLE calendar_reminder_states (
    event_id uuid NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    occurrence_date date NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (event_id,user_id,occurrence_date)
);
CREATE TABLE calendar_tool_receipts (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    request_key uuid NOT NULL,
    payload_hash bytea NOT NULL,
    response jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY(run_id,request_key)
);

-- Account-owned reminders have no Company. Existing Company producers retain
-- their scope and membership checks; only calendar reminders may be personal.
ALTER TABLE inbox_items ALTER COLUMN company_id DROP NOT NULL;
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key,
    ADD CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key
        UNIQUE NULLS NOT DISTINCT (company_id, recipient_user_id, dedupe_key),
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal','calendar_reminder','calendar_update')),
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company','user','agent','goal','task','run','conversation','message',
        'inbox','resource','runtime','approval','question','deliverable','calendar')),
    ADD CONSTRAINT inbox_items_scope_check CHECK (
        company_id IS NOT NULL OR (kind='calendar_reminder' AND conversation_id IS NULL));
CREATE INDEX inbox_items_account_created_idx
    ON inbox_items(recipient_user_id,created_at DESC,id DESC);
CREATE INDEX inbox_items_account_unread_idx
    ON inbox_items(recipient_user_id) WHERE read_at IS NULL AND archived_at IS NULL;
