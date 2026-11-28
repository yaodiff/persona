-- putTaskBrief in server/internal/assistant/task_brief.go marshals a briefRecord, including reconstructed briefs.
-- changeBriefLocked there marshals a briefState. CreateTaskBrief in server/internal/db/task_briefs.go starts state at
-- {}, matching the SQL default; PutTaskBriefState stores the marshaled object.
-- NOT VALID enforces inserts and updates without scanning existing rows.
ALTER TABLE task_briefs
    DROP CONSTRAINT IF EXISTS task_briefs_brief_shape,
    DROP CONSTRAINT IF EXISTS task_briefs_state_shape,
    ADD CONSTRAINT task_briefs_brief_shape
        CHECK (jsonb_typeof(brief) = 'object') NOT VALID,
    ADD CONSTRAINT task_briefs_state_shape
        CHECK (jsonb_typeof(state) = 'object') NOT VALID;
