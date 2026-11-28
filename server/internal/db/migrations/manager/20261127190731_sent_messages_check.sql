-- ClaimSend writes SendStateSending. RecordEcho can create a receipt with SendStateUnknown.
-- SendStateDelivered, SendStateUnknown and SendStateLater are the settlement outcomes.
-- RecordEcho stores the session.echo object. A new send has no echo.
-- Both migration sets can run on the same table. Replace identical checks in one statement.
-- NOT VALID enforces writes without scanning existing rows.
ALTER TABLE sent_messages
    DROP CONSTRAINT IF EXISTS sent_messages_state_check,
    DROP CONSTRAINT IF EXISTS sent_messages_echo_shape,
    ADD CONSTRAINT sent_messages_state_check
        CHECK (state IN ('sending', 'delivered', 'unknown', 'later')) NOT VALID,
    ADD CONSTRAINT sent_messages_echo_shape
        CHECK (echo IS NULL OR jsonb_typeof(echo) = 'object') NOT VALID;
