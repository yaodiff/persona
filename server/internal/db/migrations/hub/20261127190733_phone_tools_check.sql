-- ReplacePhoneTools stores the MCP input schema object and uses {} for a missing schema.
ALTER TABLE phone_tools ADD CONSTRAINT phone_tools_input_schema_shape
    CHECK (jsonb_typeof(input_schema) = 'object') NOT VALID;
