DROP TABLE conversation_studio_projects;
UPDATE output_items SET role = 'supporting' WHERE role = 'final';
ALTER TABLE output_items DROP CONSTRAINT output_items_role_check;
ALTER TABLE output_items ADD CONSTRAINT output_items_role_check
    CHECK (role IN ('primary', 'supporting'));
DROP TABLE output_studio_canvases;
