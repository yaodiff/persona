-- Token usage by project (docs/server/machines.md「Token usage by project」, #16530): each row also says where its
-- session started (proto.UsageBucket.Dir: written from the home folder, or a token such as ':assistant'), and
-- usage_dirs keeps what each machine's node resolved each of those directories to: the project the manager sums by
-- across machines (a git remote's host and path, a repository's root commit, a folder's name), its name, and the
-- working tree or folder. Rows stored before stay '' ("not split"); a node's next whole window sends its last 26
-- hours by directory. usage_dirs keeps a resolution after its directory is removed, so that usage keeps its project.
ALTER TABLE machine_usage ADD COLUMN IF NOT EXISTS dir text NOT NULL DEFAULT '';
ALTER TABLE machine_usage DROP CONSTRAINT IF EXISTS machine_usage_pkey;
ALTER TABLE machine_usage ADD PRIMARY KEY (machine_id, ts, step, model, launcher, agent, dir);

CREATE TABLE IF NOT EXISTS usage_dirs (
    machine_id text        NOT NULL,
    dir        text        NOT NULL,
    project    text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    host       text        NOT NULL DEFAULT '',
    place      text        NOT NULL DEFAULT '',
    worktree   boolean     NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, dir)
);
