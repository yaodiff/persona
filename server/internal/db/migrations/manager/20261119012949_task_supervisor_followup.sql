-- Do not use ledger updated_at: automatic judgments are not supervisor follow-up.
ALTER TABLE todo_supervision ADD COLUMN IF NOT EXISTS followed_up_at timestamptz;
