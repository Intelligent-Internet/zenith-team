-- The daemon gets its own session. A session belongs to a person (the
-- web app, the desktop app, the CLI) or to a daemon. A daemon session acts
-- only on the daemon plane and, beside a live Run proof whose Authorizer is
-- its person, on the routes that accept a Run; the server refuses it
-- everywhere else. Every existing session is a person's. A refresh copies
-- the value to the new row, so a session keeps its client for life.
ALTER TABLE auth_sessions
    ADD COLUMN client text NOT NULL DEFAULT 'person'
        CONSTRAINT auth_sessions_client_check CHECK (client IN ('person', 'daemon'));
