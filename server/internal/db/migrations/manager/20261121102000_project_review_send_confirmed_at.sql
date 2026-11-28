-- send_confirmed_at: when the hub confirmed the latest send. A confirmed send whose echo never shows up in the
-- target's transcript (dropped after the node took it, or renumbered at or below history_after) is sent again once
-- this is old enough, within the delivery's send bound. Rows confirmed before this column get a fresh wait.
ALTER TABLE project_review_deliveries ADD COLUMN IF NOT EXISTS send_confirmed_at timestamptz;
UPDATE project_review_deliveries SET send_confirmed_at=now() WHERE send_confirmed AND send_confirmed_at IS NULL;
