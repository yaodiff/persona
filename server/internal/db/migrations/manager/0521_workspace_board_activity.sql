-- One bounded metadata journal entry for each committed board revision. The
-- document and its entry are saved in one transaction; old documents are not kept.
CREATE TABLE IF NOT EXISTS workspace_board_activity (
    revision bigint PRIMARY KEY CHECK (revision > 0),
    board_id boolean NOT NULL DEFAULT true REFERENCES workspace_board(id) ON DELETE CASCADE CHECK (board_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    change_count integer NOT NULL CHECK (change_count >= 0),
    card_count integer NOT NULL CHECK (card_count >= 0),
    kinds text[] NOT NULL,
    card_ids text[] NOT NULL,
    changes jsonb NOT NULL CHECK (jsonb_typeof(changes) = 'array'),
    encoded_bytes integer NOT NULL CHECK (encoded_bytes >= 0)
);
