-- Bold that holds code, spans two lines or has no partner now loses its `**` in the chat index too, and emphasis or a
-- link around code its markup (#7195, searchStrong and searchInline in server/internal/assistant/search_text.go). The
-- index is not emptied again as 0513 did: a conversation whose indexed replies may still hold such markup (a star, an
-- underscore, a tilde or a bracket) is rewound to just before the first of them. A manager reads every conversation it
-- lists once after its cursor when it starts (searchSweep), so it reads these again from there and the rows it reads
-- replace the old ones (PutSearchItems). Everything else stays found meanwhile, and nothing is written over a database
-- with no index. Manager migrations run as the manager starts (db.Open in assistant main.go), before its index loop.
UPDATE assistant_search_cursors c
SET after_seq = f.rewind
FROM (
    SELECT machine_id, session_id, GREATEST(min(seq) - 1, 0) AS rewind
    FROM assistant_search_items
    WHERE role = 'assistant'
      AND (strpos(body, '*') > 0 OR strpos(body, '_') > 0 OR strpos(body, '~') > 0 OR strpos(body, '[') > 0)
    GROUP BY machine_id, session_id
) f
WHERE c.machine_id = f.machine_id AND c.session_id = f.session_id AND c.after_seq > f.rewind;
