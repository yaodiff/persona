-- HoldOperation in server/internal/db/held_operations.go inserts HeldKindMessage or HeldKindStart.
-- server/internal/devices/held.go marshals a devicewire.NodeCall for a held message. holdStart in
-- server/internal/devices/held_starts.go marshals a devicewire.HeldStartCall. Both are JSON objects.
-- NOT VALID enforces inserts and updates without scanning existing rows.
ALTER TABLE held_operations
    DROP CONSTRAINT IF EXISTS held_operations_kind_check,
    DROP CONSTRAINT IF EXISTS held_operations_call_shape,
    ADD CONSTRAINT held_operations_kind_check
        CHECK (kind IN ('message', 'start')) NOT VALID,
    ADD CONSTRAINT held_operations_call_shape
        CHECK (jsonb_typeof(call) = 'object') NOT VALID;
