-- The up file changed no schema, so there is no shape to restore. The view
-- grants it added stay: removing them would take access away from the members
-- whose own Runs made those Outputs, and an owner can still revoke any of them.
SELECT 1;
