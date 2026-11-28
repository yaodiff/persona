-- Token usage by project (docs/server/machines.md「Token usage by project」, #16530): once a node meters by directory,
-- it re-reads its last week's transcripts one time and the manager splits the usage stored before by directory,
-- keeping each ten minutes' totals as they were (db.ResplitUsage). This is how far each machine's re-split got.
CREATE TABLE IF NOT EXISTS usage_resplit (
    machine_id text        PRIMARY KEY,
    through    timestamptz,          -- the start of the last ten minutes sent
    done_at    timestamptz,          -- the re-split finished
    moved      bigint      NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
