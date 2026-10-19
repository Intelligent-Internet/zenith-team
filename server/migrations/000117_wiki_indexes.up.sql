-- Indexes for the Wiki's Collection and checkpoint lookups. The tables are
-- small, so these are plain CREATE INDEX statements: the migration runner
-- applies a file as one implicit transaction, where CONCURRENTLY cannot run.
--
-- Deleting a Collection sets wiki_revisions.collection_id and
-- wiki_canvas_drafts.collection_id to NULL through composite foreign keys
-- (000055, 000089); without these partial indexes every delete scans both
-- tables. UnfileWikiCollectionPages also finds a Collection's drafts by the
-- same pair.
CREATE INDEX wiki_revisions_collection_idx ON wiki_revisions (company_id, collection_id)
    WHERE collection_id IS NOT NULL;
CREATE INDEX wiki_canvas_drafts_collection_idx ON wiki_canvas_drafts (company_id, collection_id)
    WHERE collection_id IS NOT NULL;
-- OutputDocumentCheckpoint finds the checkpoint of a page at a draft sequence.
CREATE INDEX wiki_canvas_checkpoints_page_seq_idx ON wiki_canvas_checkpoints (company_id, page_id, through_seq);
-- ListRunWikiWrites reads a Company's research writes oldest first, by key.
CREATE INDEX run_wiki_writes_company_created_idx ON run_wiki_writes (company_id, created_at, key);
