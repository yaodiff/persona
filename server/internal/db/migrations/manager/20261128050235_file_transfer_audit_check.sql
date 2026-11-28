-- auditAgentTransfer in server/internal/devices/agent_transfer.go writes caller_kind as 'agent' or 'assistant'.
-- Keep its '' SQL default. The same writer records FilePackFile or FilePackDir for a known pack kind and '' if
-- the call fails before a known kind is available. It writes TransferDone on success and TransferFailed on errors,
-- including refusals. The constants are in server/internal/proto/transfer.go.
-- AddFileTransferAudit in server/internal/db/file_transfer_audit.go requires a nonempty state and inserts the row.
-- NOT VALID enforces inserts and updates without scanning existing rows.
ALTER TABLE file_transfer_audit
    DROP CONSTRAINT IF EXISTS file_transfer_audit_caller_kind_check,
    DROP CONSTRAINT IF EXISTS file_transfer_audit_kind_check,
    DROP CONSTRAINT IF EXISTS file_transfer_audit_state_check,
    ADD CONSTRAINT file_transfer_audit_caller_kind_check
        CHECK (caller_kind IN ('', 'agent', 'assistant')) NOT VALID,
    ADD CONSTRAINT file_transfer_audit_kind_check
        CHECK (kind IN ('', 'file', 'dir')) NOT VALID,
    ADD CONSTRAINT file_transfer_audit_state_check
        CHECK (state IN ('done', 'failed')) NOT VALID;
