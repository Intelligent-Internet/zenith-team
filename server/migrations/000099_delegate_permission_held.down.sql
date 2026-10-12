-- run_permission_held calls delegate_permission_held, so it goes first. The
-- queries of the previous release restate the rule themselves.
DROP FUNCTION run_permission_held(uuid, uuid, uuid, text, text, text[]);
DROP FUNCTION delegate_permission_held(uuid, uuid, uuid, uuid, uuid, text, text, text[], uuid);
