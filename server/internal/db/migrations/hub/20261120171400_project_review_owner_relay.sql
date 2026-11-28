CREATE TABLE project_review_owner_relay (
    message_id text NOT NULL,
    manager text NOT NULL,
    machine text NOT NULL,
    session text NOT NULL,
    unscoped boolean NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    -- Session ids are only unique on their machine, and a start's first input is named by its session.
    PRIMARY KEY (machine, session, message_id)
);

CREATE INDEX project_review_owner_relay_order ON project_review_owner_relay (manager, queued_at, message_id);

CREATE TABLE project_review_owner_managers (
    manager text PRIMARY KEY
);
