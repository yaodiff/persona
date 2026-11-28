-- deliverPhoneWrites marshals a proto.ToolResult object for FinishPhoneWrite.
-- Other queue transitions may leave the result unset.
ALTER TABLE phone_write_queue ADD CONSTRAINT phone_write_queue_result_shape
    CHECK (result IS NULL OR jsonb_typeof(result) = 'object') NOT VALID;
