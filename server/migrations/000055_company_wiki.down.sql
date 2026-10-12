BEGIN;
DROP TABLE wiki_favorites;
DROP TABLE wiki_links;
ALTER TABLE wiki_pages DROP CONSTRAINT wiki_pages_current_revision_fkey;
DROP TABLE wiki_revisions;
DROP TABLE wiki_pages;
DROP TABLE wiki_collections;
COMMIT;
