-- Owner instructions and Persona assignments per linked Agent session (#15406).
-- Kept apart from the review ledger's updated_at so recording an instruction does not
-- make a pending review look newer. The assistant owns the JSON shape.
ALTER TABLE todo_supervision
    ADD COLUMN IF NOT EXISTS owner_work jsonb NOT NULL DEFAULT '[]'::jsonb;
