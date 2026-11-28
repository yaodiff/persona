-- An uncertain delivery (sending/in_doubt) is a bounded transition, never a
-- permanent queue fence (docs/operations/logical-input-reconciliation.md
-- "Delivery deadline"). Past its deadline without a positive receipt the
-- manager settles it as expired: the full payload stays, nothing is replayed,
-- and later input drains in order. The owner then retries (requeued at the
-- tail, once) or dismisses it; both are audited CAS transitions.
ALTER TABLE conversation_inputs
 DROP CONSTRAINT conversation_inputs_state_check,
 ADD CONSTRAINT conversation_inputs_state_check CHECK (state IN ('queued','sending','accepted','in_doubt','rejected','expired','dismissed')),
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN claimed_at timestamptz,
 ADD COLUMN expired_at timestamptz;

-- Rows already ambiguous before this upgrade start their deadline now: the
-- original claim time was never recorded, and guessing it could expire a
-- dispatch that is still being reconciled.
UPDATE conversation_inputs SET claimed_at=now() WHERE state IN ('sending','in_doubt') AND claimed_at IS NULL;

-- The row projection lists every conversation's undelivered input on each
-- publish; terminal rows (accepted/rejected/dismissed) are never read there.
CREATE INDEX conversation_inputs_undelivered ON conversation_inputs (logical_machine, logical_session, position)
 WHERE state IN ('queued','sending','in_doubt','expired');

ALTER TABLE conversation_input_resolutions
 DROP CONSTRAINT conversation_input_resolutions_action_check,
 ADD CONSTRAINT conversation_input_resolutions_action_check CHECK (action IN ('accepted','rejected','replay','expired','retry','dismiss'));
