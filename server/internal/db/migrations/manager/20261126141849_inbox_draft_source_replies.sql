-- Earlier feed reads copied a source reply's completion time onto its draft cards.
-- Remove the mark only when the Inbox records a source reply at that time and no Persona send.
UPDATE assistant_feed AS f
SET body = f.body - 'sent_at', sent_at = NULL, updated_at = clock_timestamp()
FROM inbox_items AS i
WHERE f.kind = 'message'
  AND f.ref = i.id
  AND f.sent_at IS NOT NULL
  AND COALESCE(f.body->>'sent', '') = ''
  AND CASE WHEN jsonb_typeof(i.data::jsonb->'chain') = 'array' THEN
    EXISTS (
      SELECT 1 FROM jsonb_array_elements(i.data::jsonb->'chain') AS e
      WHERE e->>'kind' = 'self_replied' AND (e->>'at')::timestamptz = f.sent_at
    )
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(i.data::jsonb->'chain') AS e
      WHERE e->>'kind' = 'sent' AND (e->>'at')::timestamptz = f.sent_at
    )
    ELSE false
  END;
