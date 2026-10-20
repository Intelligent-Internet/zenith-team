-- Independent notes never borrow a voice invitation's authority.
ALTER TABLE huddles ADD COLUMN notes_mode boolean NOT NULL DEFAULT false;

CREATE TABLE huddle_notes_policies (
    company_id uuid PRIMARY KEY REFERENCES companies(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    retention_days integer NOT NULL DEFAULT 30,
    monthly_seconds bigint NOT NULL DEFAULT 0 CHECK (monthly_seconds >= 0),
    reserved_seconds bigint NOT NULL DEFAULT 0 CHECK (reserved_seconds >= 0),
    period_start date NOT NULL DEFAULT date_trunc('month', now())::date,
    created_at timestamptz NOT NULL DEFAULT now(),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);
CREATE TABLE huddle_notes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    state text NOT NULL DEFAULT 'off' CHECK (state IN ('off','starting','capturing','stopped','interrupted','ended','expired')),
    started_by_user_id uuid,
    stop_reason text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    recap_state text NOT NULL DEFAULT 'pending' CHECK (recap_state IN ('pending','processing','ready','failed')),
    recap jsonb NOT NULL DEFAULT '{}',
    recap_revision bigint NOT NULL DEFAULT 0 CHECK (recap_revision >= 0),
    recap_edited_by_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (company_id, id),
    UNIQUE (company_id, huddle_id),
    FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE
);
CREATE TABLE huddle_notes_intervals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    notes_id uuid NOT NULL,
    request_key uuid NOT NULL,
    capability_hash bytea,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','claimed','finished')),
    lease_expires_at timestamptz NOT NULL DEFAULT now() + interval '30 seconds',
    deadline_at timestamptz NOT NULL,
    started_at timestamptz,
    ended_at timestamptz,
    sequence bigint NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    batch_hash bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (company_id, id),
    UNIQUE (notes_id, request_key),
    FOREIGN KEY (company_id, notes_id) REFERENCES huddle_notes(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX huddle_notes_one_active ON huddle_notes_intervals(notes_id) WHERE state <> 'finished';
CREATE TABLE huddle_notes_segments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    interval_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    speaker text NOT NULL,
    content text NOT NULL,
    start_ms bigint NOT NULL,
    end_ms bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (interval_id, sequence),
    FOREIGN KEY (company_id, interval_id) REFERENCES huddle_notes_intervals(company_id, id) ON DELETE CASCADE
);
CREATE INDEX huddle_notes_expiry ON huddle_notes(expires_at) WHERE state <> 'expired';
ALTER TABLE huddle_notes ADD COLUMN recap_attempts integer NOT NULL DEFAULT 0 CHECK (recap_attempts >= 0),
 ADD COLUMN recap_lease_until timestamptz,
 ADD COLUMN recap_token uuid;
CREATE TABLE huddle_notes_recap_revisions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL,
 notes_id uuid NOT NULL,
 revision bigint NOT NULL CHECK (revision > 0),
 content jsonb NOT NULL,
 edited_by_user_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (notes_id,revision),
 FOREIGN KEY (company_id,notes_id) REFERENCES huddle_notes(company_id,id) ON DELETE CASCADE
);
CREATE TABLE huddle_notes_actions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL,
 notes_id uuid NOT NULL,
 action_index integer NOT NULL CHECK (action_index >= 0),
 recap_revision bigint NOT NULL CHECK (recap_revision > 0),
 task_id uuid,
 FOREIGN KEY(company_id,task_id) REFERENCES tasks(company_id,id) ON DELETE SET NULL(task_id),
 submitted_by_user_id uuid NOT NULL,
 submission_hash bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (notes_id,action_index),
 FOREIGN KEY (company_id,notes_id) REFERENCES huddle_notes(company_id,id) ON DELETE CASCADE
);
