-- ClientOpTarget and ClientOpTargetWithOccurrence marshal a target object for BeginClientOp.
-- executeClientOp finishes with a clientOpResult object or leaves result unset on panic.
ALTER TABLE client_ops
    ADD CONSTRAINT client_ops_target_shape
        CHECK (jsonb_typeof(target) = 'object') NOT VALID,
    ADD CONSTRAINT client_ops_result_shape
        CHECK (result IS NULL OR jsonb_typeof(result) = 'object') NOT VALID;
