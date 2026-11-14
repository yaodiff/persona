-- Persona's replies are indexed as the chat shows them now, without Markdown's markup (#7072, searchPlain in
-- server/internal/assistant/search_text.go). The rows read before keep the markup, so an index already built is
-- emptied once and every conversation read again from the start, as `assistant search-reindex` does (db.ResetSearch,
-- #6340): the count of resets moves too, so a manager that runs meanwhile lets go of the cursors it holds. A database
-- with no index yet is left as it is: a new manager's own database must stay without data until it is moved into
-- (db/owndb.go, copy.go).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM assistant_search_cursors) OR EXISTS (SELECT 1 FROM assistant_search_items) THEN
        DELETE FROM assistant_search_items;
        DELETE FROM assistant_search_cursors;
        INSERT INTO settings (key, value, updated_at) VALUES ('assistant_search.reset', '1', now())
        ON CONFLICT (key) DO UPDATE SET value = to_jsonb(COALESCE((settings.value #>> '{}')::bigint, 0) + 1), updated_at = now();
    END IF;
END
$$;
