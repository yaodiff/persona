CREATE TABLE supervision_triage (
    id          text PRIMARY KEY,
    todo_id     text NOT NULL,
    recipient   text NOT NULL,
    signal      text NOT NULL,
    signal_key  text NOT NULL,
    title       text NOT NULL,
    detail      text NOT NULL,
    decision    text NOT NULL CHECK (decision IN ('wake', 'defer')),
    reason      text NOT NULL,
    at          timestamptz NOT NULL,
    briefed_at  timestamptz
);

CREATE INDEX supervision_triage_deferred_at ON supervision_triage (at, id) WHERE decision = 'defer';
