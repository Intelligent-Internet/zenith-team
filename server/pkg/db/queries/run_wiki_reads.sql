-- name: GetRunWikiAccess :one
-- Whether the Run's Agent has switched Wiki reading off. No row means the Run
-- is not in the Company or the actor is not an active member of it. The server
-- enforces the switch itself: the daemon's tool allowlist is not authority.
SELECT (COALESCE(agents.settings->'capabilities'->'disabledBuiltIns', '[]'::jsonb) @> '"wiki"'::jsonb)::boolean AS disabled
FROM runs
JOIN agents ON agents.company_id = runs.company_id AND agents.id = runs.agent_id
JOIN company_members actor ON actor.company_id = runs.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
WHERE runs.company_id = sqlc.arg(company_id)::uuid AND runs.id = sqlc.arg(run_id)::uuid;

-- name: SearchWikiPagesForRun :many
-- Ranked full-text search over active pages. The words of the input are OR-ed
-- as lexeme prefixes (exact below three characters), so a natural-language
-- question still finds pages and ts_rank puts the ones matching most terms
-- first. total counts every match; only result_limit rows are returned and
-- only those pay for a snippet. No lexemes means no matches.
WITH search_input AS MATERIALIZED (
  SELECT string_agg(
    '''' || replace(replace(lexeme, '\', '\\'), '''', '''''') || ''''
      || CASE WHEN char_length(lexeme) >= 3 THEN ':*' ELSE '' END, ' | ')::tsquery AS query
  FROM unnest(to_tsvector('simple', sqlc.arg(search)::text)) AS token(lexeme)
), matches AS (
  SELECT p.id, p.title, p.summary, p.updated_at, p.collection_id, p.current_revision_id,
    ts_rank(p.search_vector, search_input.query) AS rank,
    count(*) OVER ()::int AS total
  FROM company_wiki_pages p
  JOIN company_members actor ON actor.company_id = p.company_id
    AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
  CROSS JOIN search_input
  WHERE p.company_id = sqlc.arg(company_id)::uuid AND p.status = 'active'
    AND p.current_revision_id IS NOT NULL
    AND p.search_vector @@ search_input.query
  ORDER BY rank DESC, p.updated_at DESC, p.id DESC
  LIMIT sqlc.arg(result_limit)::int
)
SELECT matches.id, matches.title, matches.summary, matches.updated_at, matches.total,
  revision.number AS revision_number, collection.name AS collection_name,
  ts_headline('simple', revision.body, search_input.query,
    'StartSel="", StopSel="", MaxFragments=2, MaxWords=28, MinWords=12, FragmentDelimiter=" ... "')::text AS snippet
FROM matches
JOIN wiki_revisions revision ON revision.id = matches.current_revision_id
LEFT JOIN wiki_collections collection ON collection.company_id = sqlc.arg(company_id)::uuid
  AND collection.id = matches.collection_id
CROSS JOIN search_input
ORDER BY matches.rank DESC, matches.updated_at DESC, matches.id DESC;

-- name: GetWikiPageSliceForRun :one
-- An active page's current revision with one character slice of its body:
-- content is total_chars characters long at most char_limit from char_offset.
-- The revision id is what the evidence row pins.
SELECT p.id, p.title, p.summary, p.updated_at,
  revision.id AS revision_id, revision.number AS revision_number,
  collection.name AS collection_name, author.display_name AS updated_by_name,
  char_length(revision.body)::int AS total_chars,
  substr(revision.body, sqlc.arg(char_offset)::int + 1, sqlc.arg(char_limit)::int)::text AS content
FROM company_wiki_pages p
JOIN wiki_revisions revision ON revision.id = p.current_revision_id
JOIN company_members actor ON actor.company_id = p.company_id
  AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
LEFT JOIN wiki_collections collection ON collection.company_id = p.company_id
  AND collection.id = p.collection_id
LEFT JOIN users author ON author.id = revision.author_user_id
WHERE p.company_id = sqlc.arg(company_id)::uuid AND p.id = sqlc.arg(page_id)::uuid
  AND p.status = 'active';

-- name: InsertRunWikiSearch :execrows
-- Attribution comes from the Run row, and the company match makes a Run of
-- another Company insert nothing.
INSERT INTO run_wiki_reads (company_id, run_id, agent_id, kind, query, result_count)
SELECT runs.company_id, runs.id, runs.agent_id, 'search', sqlc.arg(query)::text, sqlc.arg(result_count)::int
FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid AND runs.id = sqlc.arg(run_id)::uuid;

-- name: InsertRunWikiRead :execrows
INSERT INTO run_wiki_reads (company_id, run_id, agent_id, kind, page_id, revision_id,
  revision_number, title, char_offset, chars_returned, truncated)
SELECT runs.company_id, runs.id, runs.agent_id, 'read', sqlc.arg(page_id)::uuid,
  sqlc.arg(revision_id)::uuid, sqlc.arg(revision_number)::int, sqlc.arg(title)::text,
  sqlc.arg(char_offset)::int, sqlc.arg(chars_returned)::int, sqlc.arg(truncated)::boolean
FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid AND runs.id = sqlc.arg(run_id)::uuid;

-- name: CanViewRunWikiReads :one
-- The Run history rule (ADR 0017): a Task Run's evidence reads for a reader
-- of its Task chat (conversation_readers: its Project's readers), a direct or
-- Schedule Run's only for the member who opened its Conversation. One chat,
-- so the EXISTS is one probe.
SELECT EXISTS (
  SELECT 1 FROM runs
  JOIN conversations ON conversations.company_id = runs.company_id
    AND conversations.id = runs.conversation_id
  JOIN company_members actor ON actor.company_id = runs.company_id
    AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
  WHERE runs.company_id = sqlc.arg(company_id)::uuid AND runs.id = sqlc.arg(run_id)::uuid
    AND ((conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid))
      OR (conversations.kind IN ('direct', 'schedule')
        AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid))
)::boolean AS visible;

-- name: ListRunWikiReads :many
-- Oldest first. page_status and page_current_revision are the page's live
-- state, NULL for a search row, so the reader can show "changed since" and
-- "archived".
SELECT w.id, w.kind, w.query, w.result_count, w.page_id, w.revision_id, w.revision_number,
  w.title, w.truncated, w.created_at,
  page.status AS page_status, current_revision.number AS page_current_revision
FROM run_wiki_reads w
LEFT JOIN company_wiki_pages page ON page.company_id = w.company_id AND page.id = w.page_id
LEFT JOIN wiki_revisions current_revision ON current_revision.id = page.current_revision_id
WHERE w.company_id = sqlc.arg(company_id)::uuid AND w.run_id = sqlc.arg(run_id)::uuid
ORDER BY w.created_at, w.id
LIMIT 200;

-- name: ListWikiPageAgentReads :many
-- Per Run, the latest read of the page, newest first, only for Runs the actor
-- may see under the Run history rule (CanViewRunWikiReads). total counts every
-- visible Run. Each Run's chat is checked with one scalar
-- conversation_readers probe (ADR 0017: inside an OR a list never joins the
-- view). task_readable says whether the actor reads the Run's Task
-- (task_access); the caller drops a Task id they cannot read, so a direct
-- chat's Run never points at a Task in a Project they cannot read.
WITH latest AS (
  SELECT DISTINCT ON (w.run_id) w.run_id, w.agent_id, w.revision_number, w.created_at,
    COALESCE(runs.task_id, runs.source_task_id) AS task_id,
    conversations.id AS conversation_id, conversations.kind AS conversation_kind,
    conversations.created_by_user_id AS conversation_creator_id
  FROM run_wiki_reads w
  JOIN runs ON runs.company_id = w.company_id AND runs.id = w.run_id
  JOIN conversations ON conversations.company_id = runs.company_id
    AND conversations.id = runs.conversation_id
  JOIN company_members actor ON actor.company_id = w.company_id
    AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
  WHERE w.company_id = sqlc.arg(company_id)::uuid AND w.page_id = sqlc.arg(page_id)::uuid
    AND w.kind = 'read'
    AND conversations.kind IN ('task', 'direct', 'schedule')
  ORDER BY w.run_id, w.created_at DESC, w.id DESC
), visible AS (
  SELECT latest.run_id, latest.agent_id, latest.revision_number, latest.created_at, latest.task_id
  FROM latest
  WHERE (latest.conversation_kind = 'task' AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = sqlc.arg(company_id)::uuid
            AND reader.conversation_id = latest.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1), false))
     OR (latest.conversation_kind IN ('direct', 'schedule')
       AND latest.conversation_creator_id = sqlc.arg(actor_user_id)::uuid)
)
SELECT visible.run_id,
  visible.task_id,
  COALESCE((
    SELECT true FROM task_access AS access
    WHERE access.company_id = sqlc.arg(company_id)::uuid
      AND access.task_id = visible.task_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1), false)::boolean AS task_readable,
  visible.agent_id,
  COALESCE(agent.name, '')::text AS agent_name,
  visible.revision_number, visible.created_at AS last_read_at,
  count(*) OVER ()::int AS total
FROM visible
LEFT JOIN agents agent ON agent.company_id = sqlc.arg(company_id)::uuid AND agent.id = visible.agent_id
ORDER BY visible.created_at DESC, visible.run_id DESC
LIMIT sqlc.arg(result_limit)::int;

-- name: WikiPageExistsForMember :one
SELECT EXISTS (
  SELECT 1 FROM company_wiki_pages p
  JOIN company_members actor ON actor.company_id = p.company_id
    AND actor.user_id = sqlc.arg(actor_user_id)::uuid AND actor.status = 'active'
  WHERE p.company_id = sqlc.arg(company_id)::uuid AND p.id = sqlc.arg(page_id)::uuid
    AND p.current_revision_id IS NOT NULL
)::boolean AS present;
