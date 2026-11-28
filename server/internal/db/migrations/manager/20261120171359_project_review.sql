CREATE TABLE project_review_state (
    binding text PRIMARY KEY,
    observed_at timestamptz NOT NULL,
    block_reason text NOT NULL
);

CREATE TABLE project_review_deliveries (
    seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id text UNIQUE NOT NULL,
    target text NOT NULL,
    event jsonb NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    history_after bigint,
    review_only boolean,
    -- Reserved scoped, then sent as triage after authorization was revoked: either receipt settles it.
    scope_downgraded boolean NOT NULL DEFAULT false,
    retry_at timestamptz NOT NULL DEFAULT now(),
    outcome text NOT NULL DEFAULT '',
    settled_at timestamptz
);

CREATE INDEX project_review_pending ON project_review_deliveries (target, seq)
    WHERE settled_at IS NULL;
