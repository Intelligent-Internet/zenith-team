-- A connection reservation is the server-side admission record. Media
-- confirmation remains an asynchronous LiveKit/webhook transition.
CREATE TABLE huddle_connections (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    status text NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved','connected','ended','revoked')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    connected_at timestamptz,
    ended_at timestamptz,
    PRIMARY KEY (id),
    UNIQUE (company_id, id),
    UNIQUE (company_id, huddle_id, user_id, request_key),
    FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE RESTRICT,
    CHECK ((status='connected' AND connected_at IS NOT NULL) OR status<>'connected'),
    CHECK ((status='ended' AND ended_at IS NOT NULL) OR status<>'ended')
);
CREATE UNIQUE INDEX huddle_connections_open_user_idx
    ON huddle_connections(company_id, huddle_id, user_id)
    WHERE status IN ('reserved','connected');
CREATE INDEX huddle_connections_huddle_idx
    ON huddle_connections(company_id, huddle_id, created_at, id);
