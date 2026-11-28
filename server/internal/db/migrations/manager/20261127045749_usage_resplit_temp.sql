-- Token usage by project (docs/server/machines.md "Token usage by project", #16827): a node that names a temporary
-- folder by its path (its repository's project when a repository was made in it) re-reads its last week once more,
-- and the manager splits the usage it stored under ':temp' among those folders, keeping each ten minutes' totals
-- (db.ResplitUsage, pass "temp"). This is how far each machine's second pass got; the first's columns are unchanged.
ALTER TABLE usage_resplit ADD COLUMN IF NOT EXISTS temp_through timestamptz;
ALTER TABLE usage_resplit ADD COLUMN IF NOT EXISTS temp_done_at timestamptz;
ALTER TABLE usage_resplit ADD COLUMN IF NOT EXISTS temp_moved bigint NOT NULL DEFAULT 0;
