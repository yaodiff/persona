-- Each native send of a delivery gets its own client message ID (sends counts them): the hub refuses an ID whose
-- send it never learned the outcome of, so one reused after an uncertain send could never be sent again.
-- send_confirmed: the hub confirmed the latest ID's send, so its echo is awaited instead of sending another.
ALTER TABLE project_review_deliveries
    ADD COLUMN IF NOT EXISTS sends integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS send_confirmed boolean NOT NULL DEFAULT false;
