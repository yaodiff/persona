-- todo_supervision.owner_work is always a JSON array (#15641): the assistant's Supervision.row writes the marshalled
-- []ownerSessionWork or '[]' when there is none, db.PutTodoSupervision writes '[]' for an empty value, and the column's
-- default is '[]'. A shape check keeps storage from holding anything else (docs/plans/plan-typed-core.md step P9).
-- NOT VALID: new and updated rows are checked, existing rows are not scanned, so the deploy cannot fail on old data.
ALTER TABLE todo_supervision ADD CONSTRAINT todo_supervision_owner_work_shape
	CHECK (jsonb_typeof(owner_work) = 'array') NOT VALID;
