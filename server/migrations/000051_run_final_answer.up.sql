-- The item carrying a Run's proven final answer: an agent_text item with a
-- text part the provider marked final. The transcript append records its key,
-- so the channel publisher finds the item through the (run_id, item_key) index
-- instead of scanning parts documents.
ALTER TABLE runs ADD COLUMN final_answer_item_key text;

-- The one scan of the parts documents: the latest such item of each Run.
UPDATE runs SET final_answer_item_key = proven.item_key
FROM (
    SELECT DISTINCT ON (item.run_id) item.run_id, item.item_key
    FROM conversation_items AS item
    WHERE item.run_id IS NOT NULL AND item.type = 'agent_text' AND item.item_key IS NOT NULL
      AND EXISTS (
          SELECT 1
          FROM jsonb_array_elements(
              CASE WHEN jsonb_typeof(item.parts) = 'array' THEN item.parts ELSE '[]'::jsonb END
          ) AS part
          WHERE part->>'type' = 'text' AND part->>'final' = 'true')
    ORDER BY item.run_id, item.seq DESC
) AS proven
WHERE runs.id = proven.run_id;
