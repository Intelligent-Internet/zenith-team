-- When a Project's access last changed (ADR 0017). Every statement that
-- advances projects.access_version (a grant change, a visibility change, a
-- Company revocation removing grants) also sets this, so the Inbox counts
-- version can move forward when access hides or shows a person's items:
-- countsVersion is the newest of the person's item writes and of the access
-- changes of the Projects those items name, so it never goes backwards and
-- the read after a revoke outranks any counts frame built before it.
ALTER TABLE projects ADD COLUMN access_changed_at timestamptz NOT NULL DEFAULT now();
