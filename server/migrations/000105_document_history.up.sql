-- Saved document history is independent of review snapshots and Project placement.
CREATE TABLE document_changes (
 company_id uuid NOT NULL,
 page_id uuid NOT NULL,
 sequence bigint NOT NULL CHECK(sequence > 0),
 actor_type text NOT NULL CHECK(actor_type IN ('user','agent','system')),
 actor_id uuid,
 change_type text NOT NULL CHECK(change_type IN ('initial','edit','restore')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 delta bytea NOT NULL,
 checkpoint bytea,
 label text NOT NULL DEFAULT '' CHECK(length(label) <= 200),
 PRIMARY KEY(company_id,page_id,sequence),
 FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE,
 CHECK((actor_type='system' AND actor_id IS NULL) OR (actor_type<>'system' AND actor_id IS NOT NULL)),
 CHECK(change_type<>'initial' OR checkpoint IS NOT NULL),
 CONSTRAINT document_changes_storage_check CHECK((checkpoint IS NOT NULL AND octet_length(delta)=0) OR (checkpoint IS NULL AND octet_length(delta)>0))
);
CREATE INDEX document_changes_time ON document_changes(company_id,page_id,created_at DESC,sequence DESC);
CREATE INDEX document_changes_actor ON document_changes(company_id,page_id,actor_id,sequence DESC);
CREATE INDEX document_changes_checkpoint ON document_changes(company_id,page_id,sequence DESC) WHERE checkpoint IS NOT NULL;
