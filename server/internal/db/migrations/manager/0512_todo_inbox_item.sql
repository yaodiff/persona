-- The Inbox row a to-do was made from (#6480, docs/assistant/inbox.md "Add to to-dos"): its id, '' for a to-do made any other way.
-- One value: the to-do's page opens the row from it; the row points back through its handling chain, not a second link.
ALTER TABLE todos ADD COLUMN IF NOT EXISTS inbox_item text NOT NULL DEFAULT '';
