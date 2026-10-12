DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM document_changes) THEN
  RAISE EXCEPTION 'document history exists; export a backup before downgrading';
 END IF;
END $$;
DROP TABLE document_changes;
