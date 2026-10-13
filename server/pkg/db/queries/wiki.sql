-- name: ListWikiCollectionsForMember :many
SELECT c.* FROM wiki_collections c
JOIN company_members actor ON actor.company_id = c.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE c.company_id = sqlc.arg(company_id)::uuid
ORDER BY lower(c.name), c.id;

-- name: CreateWikiCollection :one
INSERT INTO wiki_collections (company_id, name) VALUES ($1, $2) RETURNING *;

-- name: GetWikiCollectionForUpdate :one
SELECT * FROM wiki_collections
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(collection_id)::uuid
FOR UPDATE;

-- name: RenameWikiCollection :one
UPDATE wiki_collections SET name = sqlc.arg(name)::text,
  version = version + 1, updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(collection_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: UnfileWikiCollectionPages :exec
WITH cleared_drafts AS (
 UPDATE wiki_canvas_drafts SET collection_id=NULL,head_seq=head_seq+1,updated_at=clock_timestamp()
 WHERE company_id=sqlc.arg(company_id)::uuid AND collection_id=sqlc.arg(collection_id)::uuid
)
UPDATE wiki_pages SET collection_id = NULL, version = version + 1,
  updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND collection_id = sqlc.arg(collection_id)::uuid;

-- name: DeleteWikiCollection :exec
DELETE FROM wiki_collections
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(collection_id)::uuid;

-- name: GetWikiPageForMember :one
SELECT p.id, p.company_id,
 display_collection.id AS collection_id,
 p.slug, (CASE WHEN p.current_revision_id IS NULL THEN COALESCE(d.title,p.title) ELSE p.title END)::text AS title,
 (CASE WHEN p.current_revision_id IS NULL THEN COALESCE(d.summary,p.summary) ELSE p.summary END)::text AS summary,
  p.current_revision_id, p.status, p.version, p.created_by_user_id,
  p.created_at, p.updated_at, p.archived_at,
  COALESCE(r.body,'')::text AS body, COALESCE(r.number,0)::bigint AS content_revision,
  COALESCE(r.block_content,'{}'::jsonb)::jsonb AS block_content,
  COALESCE(r.author_user_id,p.created_by_user_id)::uuid AS last_author_user_id, COALESCE(r.created_at,p.created_at)::timestamptz AS revision_created_at,
  r.author_agent_id AS last_author_agent_id, author_agent.name AS last_author_agent_name,
  EXISTS(SELECT 1 FROM wiki_favorites f WHERE f.company_id = p.company_id
    AND f.page_id = p.id AND f.user_id = sqlc.arg(actor_user_id)::uuid)::boolean AS favorite
FROM company_wiki_pages p
LEFT JOIN wiki_canvas_drafts d ON d.company_id=p.company_id AND d.page_id=p.id
LEFT JOIN wiki_collections display_collection ON display_collection.company_id=p.company_id
 AND display_collection.id=CASE WHEN p.current_revision_id IS NULL THEN d.collection_id ELSE p.collection_id END
LEFT JOIN wiki_revisions r ON r.id = p.current_revision_id
LEFT JOIN agents author_agent ON author_agent.company_id = p.company_id AND author_agent.id = r.author_agent_id
JOIN company_members actor ON actor.company_id = p.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE (p.current_revision_id IS NOT NULL OR actor.role=ANY(sqlc.arg(publisher_roles)::text[]))
  AND p.company_id = sqlc.arg(company_id)::uuid AND p.id = sqlc.arg(page_id)::uuid;

-- name: ListWikiPagesForMember :many
-- Use the index's parser for word prefixes and retain the whole-input prefix
-- for partly typed emails/hosts. No lexemes means no search filter.
-- Check emptiness from the input so custom plans can use the ordered index
-- for browsing and the GIN index for a nonempty search.
WITH search_input AS MATERIALIZED (
  SELECT ('(' || string_agg(quote, ' & ') || ') | '
    || '''' || replace(replace(lower(btrim(sqlc.arg(search)::text)), '\', '\\'), '''', '''''') || ''':*')::tsquery AS query
  FROM unnest(to_tsvector('simple', sqlc.arg(search)::text)) AS token(lexeme),
    LATERAL (SELECT '''' || replace(replace(lexeme, '\', '\\'), '''', '''''') || ''':*') AS quoted(quote)
)
SELECT p.id, p.company_id,
 display_collection.id AS collection_id,
 p.slug, (CASE WHEN p.current_revision_id IS NULL THEN COALESCE(d.title,p.title) ELSE p.title END)::text AS title,
 (CASE WHEN p.current_revision_id IS NULL THEN COALESCE(d.summary,p.summary) ELSE p.summary END)::text AS summary,
  p.status, p.version, p.created_at, p.updated_at, (p.current_revision_id IS NOT NULL)::boolean AS published,
  EXISTS(SELECT 1 FROM wiki_favorites f WHERE f.company_id = p.company_id
    AND f.page_id = p.id AND f.user_id = sqlc.arg(actor_user_id)::uuid)::boolean AS favorite
FROM company_wiki_pages p
LEFT JOIN wiki_canvas_drafts d ON d.company_id=p.company_id AND d.page_id=p.id
LEFT JOIN wiki_collections display_collection ON display_collection.company_id=p.company_id
 AND display_collection.id=CASE WHEN p.current_revision_id IS NULL THEN d.collection_id ELSE p.collection_id END
JOIN company_members actor ON actor.company_id = p.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE (p.current_revision_id IS NOT NULL OR actor.role=ANY(sqlc.arg(publisher_roles)::text[]))
  AND p.company_id = sqlc.arg(company_id)::uuid
  AND p.status = sqlc.arg(status)::text
  AND (sqlc.narg(collection_id)::uuid IS NULL OR CASE WHEN p.current_revision_id IS NULL THEN d.collection_id ELSE p.collection_id END = sqlc.narg(collection_id)::uuid)
  AND (NOT sqlc.arg(favorites_only)::boolean OR EXISTS(
    SELECT 1 FROM wiki_favorites f WHERE f.company_id = p.company_id
      AND f.page_id = p.id AND f.user_id = sqlc.arg(actor_user_id)::uuid))
  AND (length(to_tsvector('simple', sqlc.arg(search)::text)) = 0
    OR p.search_vector @@ (SELECT query FROM search_input)
    OR (p.current_revision_id IS NULL AND to_tsvector('simple',d.title || ' ' || d.markdown) @@ (SELECT query FROM search_input)))
ORDER BY p.updated_at DESC, p.id DESC
LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int;

-- name: CountWikiPagesForMember :one
WITH search_input AS MATERIALIZED (
  SELECT ('(' || string_agg(quote, ' & ') || ') | '
    || '''' || replace(replace(lower(btrim(sqlc.arg(search)::text)), '\', '\\'), '''', '''''') || ''':*')::tsquery AS query
  FROM unnest(to_tsvector('simple', sqlc.arg(search)::text)) AS token(lexeme),
    LATERAL (SELECT '''' || replace(replace(lexeme, '\', '\\'), '''', '''''') || ''':*') AS quoted(quote)
)
SELECT count(*) FROM company_wiki_pages p
LEFT JOIN wiki_canvas_drafts d ON d.company_id=p.company_id AND d.page_id=p.id
JOIN company_members actor ON actor.company_id = p.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE (p.current_revision_id IS NOT NULL OR actor.role=ANY(sqlc.arg(publisher_roles)::text[]))
  AND p.company_id = sqlc.arg(company_id)::uuid
  AND p.status = sqlc.arg(status)::text
  AND (sqlc.narg(collection_id)::uuid IS NULL OR CASE WHEN p.current_revision_id IS NULL THEN d.collection_id ELSE p.collection_id END = sqlc.narg(collection_id)::uuid)
  AND (NOT sqlc.arg(favorites_only)::boolean OR EXISTS(
    SELECT 1 FROM wiki_favorites f WHERE f.company_id = p.company_id
      AND f.page_id = p.id AND f.user_id = sqlc.arg(actor_user_id)::uuid))
  AND (length(to_tsvector('simple', sqlc.arg(search)::text)) = 0
    OR p.search_vector @@ (SELECT query FROM search_input)
    OR (p.current_revision_id IS NULL AND to_tsvector('simple',d.title || ' ' || d.markdown) @@ (SELECT query FROM search_input)));

-- name: GetWikiPageForUpdate :one
SELECT id, status, version FROM wiki_pages WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(page_id)::uuid AND source_output_id IS NULL AND project_id IS NULL FOR UPDATE;

-- name: CreateWikiPage :one
INSERT INTO wiki_pages (id, company_id, collection_id, slug, title, summary, created_by_user_id)
SELECT seed.id, $1, $2, 'page-' || seed.id::text, $3, $4, $5
FROM (SELECT gen_random_uuid() AS id) seed RETURNING id;

-- name: CreateWikiRevision :one
INSERT INTO wiki_revisions (company_id, page_id, number, title, body, summary,
  collection_id, author_user_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *;

-- name: SetWikiPageCurrentRevision :one
UPDATE wiki_pages SET title = sqlc.arg(title)::text, summary = sqlc.arg(summary)::text,
  collection_id = sqlc.narg(collection_id)::uuid,
  current_revision_id = sqlc.arg(revision_id)::uuid,
  search_vector = (SELECT to_tsvector('simple', r.title || ' ' || r.body) FROM wiki_revisions r
    WHERE r.company_id = wiki_pages.company_id AND r.page_id = wiki_pages.id
      AND r.id = sqlc.arg(revision_id)::uuid),
  version = version + 1, next_revision_number = GREATEST(next_revision_number,(SELECT number+1 FROM wiki_revisions WHERE id=sqlc.arg(revision_id)::uuid)), updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(page_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint AND status = 'active'
RETURNING id;

-- name: SetWikiPageArchived :one
UPDATE wiki_pages SET status = 'archived', archived_at = clock_timestamp(),
  version = version + 1, updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(page_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint AND status = 'active'
RETURNING id;

-- name: SetWikiPageRestored :one
UPDATE wiki_pages SET status = 'active', archived_at = NULL,
  version = version + 1, updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(page_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint AND status = 'archived'
RETURNING id;

-- name: ListWikiRevisionsForMember :many
SELECT r.* FROM wiki_revisions r
JOIN company_wiki_pages p ON p.company_id = r.company_id AND p.id = r.page_id
JOIN company_members actor ON actor.company_id = r.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE r.published_at IS NOT NULL AND r.company_id = sqlc.arg(company_id)::uuid AND r.page_id = sqlc.arg(page_id)::uuid
ORDER BY r.number DESC
LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int;

-- name: CountWikiRevisionsForMember :one
SELECT count(*) FROM wiki_revisions r
JOIN company_wiki_pages p ON p.company_id=r.company_id AND p.id=r.page_id
JOIN company_members actor ON actor.company_id = r.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE r.published_at IS NOT NULL AND r.company_id = sqlc.arg(company_id)::uuid AND r.page_id = sqlc.arg(page_id)::uuid;

-- name: GetWikiCurrentRevision :one
SELECT r.* FROM wiki_revisions r
JOIN company_wiki_pages p ON p.company_id = r.company_id AND p.id = r.page_id
  AND p.current_revision_id = r.id
WHERE p.company_id = sqlc.arg(company_id)::uuid AND p.id = sqlc.arg(page_id)::uuid;

-- name: GetWikiRevisionForMember :one
SELECT r.* FROM wiki_revisions r
JOIN company_wiki_pages p ON p.company_id = r.company_id AND p.id = r.page_id
JOIN company_members actor ON actor.company_id = r.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE r.published_at IS NOT NULL AND r.company_id = sqlc.arg(company_id)::uuid AND r.page_id = sqlc.arg(page_id)::uuid
  AND r.id = sqlc.arg(revision_id)::uuid;

-- name: DeleteWikiLinksFromPage :exec
DELETE FROM wiki_links WHERE company_id = sqlc.arg(company_id)::uuid
  AND from_page_id = sqlc.arg(page_id)::uuid;

-- name: InsertWikiLinksFromPage :exec
INSERT INTO wiki_links (company_id, from_page_id, to_page_id)
SELECT sqlc.arg(company_id)::uuid, sqlc.arg(page_id)::uuid, target.id
FROM unnest(sqlc.arg(target_ids)::uuid[]) AS linked(id)
JOIN company_wiki_pages target ON target.company_id = sqlc.arg(company_id)::uuid
  AND target.id = linked.id AND target.current_revision_id IS NOT NULL
WHERE target.id <> sqlc.arg(page_id)::uuid
ON CONFLICT DO NOTHING;

-- name: ListWikiLinksForMember :many
SELECT link.from_page_id, link.to_page_id FROM wiki_links link
JOIN company_wiki_pages source ON source.company_id = link.company_id
  AND source.id = link.from_page_id AND source.status = 'active'
JOIN company_wiki_pages target ON target.company_id = link.company_id
  AND target.id = link.to_page_id AND target.status = 'active'
JOIN company_members actor ON actor.company_id = link.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE link.company_id = sqlc.arg(company_id)::uuid
  AND link.from_page_id = ANY(sqlc.arg(visible_page_ids)::uuid[])
  AND link.to_page_id = ANY(sqlc.arg(visible_page_ids)::uuid[])
ORDER BY link.from_page_id, link.to_page_id
LIMIT 5001;

-- name: ListWikiPageLinksForMember :many
-- Each direction is ordered and limited on its own (201 = the 200-row cap
-- plus one to detect truncation), so many backlinks never hide outgoing ones.
(SELECT 'outgoing'::text AS direction, target.id AS linked_page_id, target.title AS linked_title
FROM wiki_links link
JOIN company_wiki_pages target ON target.company_id = link.company_id
  AND target.id = link.to_page_id AND target.status = 'active'
JOIN company_members actor ON actor.company_id = link.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE link.company_id = sqlc.arg(company_id)::uuid AND link.from_page_id = sqlc.arg(page_id)::uuid
ORDER BY target.title, target.id
LIMIT 201)
UNION ALL
(SELECT 'incoming'::text AS direction, source.id AS linked_page_id, source.title AS linked_title
FROM wiki_links link
JOIN company_wiki_pages source ON source.company_id = link.company_id
  AND source.id = link.from_page_id AND source.status = 'active'
JOIN company_members actor ON actor.company_id = link.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE link.company_id = sqlc.arg(company_id)::uuid AND link.to_page_id = sqlc.arg(page_id)::uuid
ORDER BY source.title, source.id
LIMIT 201)
ORDER BY direction, linked_title, linked_page_id;

-- name: SetWikiFavorite :one
-- Returns the page status read in the same snapshot as the insert, so an
-- archived page is reported rather than silently not favorited. No row means
-- the page or the actor's membership is not visible.
WITH page AS (
  SELECT p.company_id, p.id, p.status FROM company_wiki_pages p
  WHERE p.company_id = sqlc.arg(company_id)::uuid AND p.id = sqlc.arg(page_id)::uuid
    AND EXISTS (SELECT 1 FROM company_members actor WHERE actor.company_id = p.company_id
      AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active')
), favorited AS (
  INSERT INTO wiki_favorites (company_id, page_id, user_id)
  SELECT page.company_id, page.id, sqlc.arg(actor_user_id)::uuid FROM page
  WHERE page.status = 'active'
  ON CONFLICT DO NOTHING
)
SELECT page.status FROM page;

-- name: ClearWikiFavorite :exec
DELETE FROM wiki_favorites f WHERE f.company_id = sqlc.arg(company_id)::uuid
  AND f.page_id = sqlc.arg(page_id)::uuid AND f.user_id = sqlc.arg(actor_user_id)::uuid
  AND EXISTS (SELECT 1 FROM company_members actor WHERE actor.company_id = f.company_id
    AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active');

-- name: DeleteWikiArtifactRefsFromPage :exec
DELETE FROM wiki_artifact_refs WHERE company_id = sqlc.arg(company_id)::uuid
  AND page_id = sqlc.arg(page_id)::uuid;

-- Ordinal -1 stands for the whole Artifact (NULL ordinal). A citation whose
-- Artifact or item no longer exists is skipped: the body keeps the token and
-- readers see it as unavailable.
-- name: InsertWikiArtifactRefs :exec
INSERT INTO wiki_artifact_refs (company_id, page_id, output_id, ordinal)
SELECT sqlc.arg(company_id)::uuid, sqlc.arg(page_id)::uuid, ref.output_id, NULLIF(ref.ordinal, -1)
FROM (SELECT cited.output_id, position.ordinal
      FROM unnest(sqlc.arg(output_ids)::uuid[]) WITH ORDINALITY AS cited(output_id, n)
      JOIN unnest(sqlc.arg(ordinals)::integer[]) WITH ORDINALITY AS position(ordinal, n)
        ON position.n = cited.n) AS ref
JOIN outputs ON outputs.company_id = sqlc.arg(company_id)::uuid AND outputs.id = ref.output_id
WHERE ref.ordinal = -1 OR EXISTS (
    SELECT 1 FROM output_items AS item
    WHERE item.company_id = outputs.company_id AND item.output_id = ref.output_id
      AND item.ordinal = ref.ordinal)
ON CONFLICT DO NOTHING;

-- The cited Artifacts the actor can open now, with their item ordinals. A
-- missing row means missing or not visible; callers never tell the two apart.
-- name: ListWikiArtifactRefTargetsForMember :many
SELECT access.output_id,
       COALESCE(array_agg(item.ordinal ORDER BY item.ordinal)
           FILTER (WHERE item.ordinal IS NOT NULL), '{}')::integer[] AS ordinals
FROM output_access AS access
LEFT JOIN output_items AS item
  ON item.company_id = access.company_id AND item.output_id = access.output_id
WHERE access.company_id = sqlc.arg(company_id)::uuid
  AND access.user_id = sqlc.arg(actor_user_id)::uuid
  AND access.output_id = ANY(sqlc.arg(output_ids)::uuid[])
GROUP BY access.output_id;

-- name: UpdateWikiDraftMetadata :one
UPDATE wiki_canvas_drafts SET title=$3,summary=$4,collection_id=$5,
 head_seq=head_seq+1,updated_at=clock_timestamp()
WHERE company_id=$1 AND page_id=$2 AND head_seq=$6 RETURNING *;

-- name: SetWikiRevisionPublished :exec
UPDATE wiki_revisions SET published_at=COALESCE(published_at,clock_timestamp())
WHERE company_id=$1 AND page_id=$2 AND id=$3;

-- name: RestoreWikiBlockDraft :one
UPDATE wiki_canvas_drafts SET state=$3,content=$4,markdown=$5,title=$6,summary=$7,collection_id=$8,
 head_seq=head_seq+1,structure_seq=head_seq+1,updated_at=clock_timestamp(),restored_revision_id=sqlc.narg(restored_revision_id)::uuid
WHERE company_id=$1 AND page_id=$2 AND head_seq=$9 RETURNING *;

-- name: ClearWikiDraftRestore :exec
UPDATE wiki_canvas_drafts SET restored_revision_id=NULL WHERE company_id=$1 AND page_id=$2;
