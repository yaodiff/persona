-- An ambiguous input belongs to the dispatch attempt, not to whichever segment
-- happens to be active when a manager restarts. Empty identity means unknown.
ALTER TABLE conversation_inputs
 ADD COLUMN dispatch_segment text NOT NULL DEFAULT '',
 ADD COLUMN dispatch_machine text NOT NULL DEFAULT '',
 ADD COLUMN dispatch_session text NOT NULL DEFAULT '',
 ADD COLUMN dispatch_epoch bigint NOT NULL DEFAULT 0,
 ADD COLUMN delivery_version bigint NOT NULL DEFAULT 0,
 ADD COLUMN blocked_reason text NOT NULL DEFAULT '',
 ADD COLUMN last_reconciled_at timestamptz;

-- Only committed switch history proves an old dispatch target. Rejected switches
-- can have restored a source at another epoch: do not guess those identities.
UPDATE conversation_inputs i SET dispatch_segment=s.id,dispatch_machine=s.machine_id,
 dispatch_session=s.session_id,dispatch_epoch=i.epoch
FROM conversation_switches w JOIN conversation_segments s ON s.id=w.target_segment
WHERE i.logical_machine=w.logical_machine AND i.logical_session=w.logical_session
 AND i.epoch=w.expected_epoch+1 AND w.phase IN ('active','accepted')
 AND i.state IN ('sending','in_doubt');
UPDATE conversation_inputs i SET dispatch_segment=s.id,dispatch_machine=s.machine_id,
 dispatch_session=s.session_id,dispatch_epoch=1
FROM conversation_segments s WHERE i.logical_machine=s.logical_machine
 AND i.logical_session=s.logical_session AND s.ordinal=1 AND i.epoch=1
 AND i.state IN ('sending','in_doubt') AND i.dispatch_epoch=0;

-- The exact original payload and operator intent survive settlement and replay.
-- Absent receipts never authorize replay. Definitive refusals are audited before
-- requeue; operators explicitly choose ambiguous replay. Request IDs are idempotent.
CREATE TABLE conversation_input_resolutions (
 logical_machine text NOT NULL,
 logical_session text NOT NULL,
 request_id text NOT NULL,
 client_msg_id text NOT NULL,
 actor text NOT NULL,
 reason text NOT NULL,
 action text NOT NULL CHECK (action IN ('accepted','rejected','replay')),
 request bytea NOT NULL,
 original_payload bytea NOT NULL,
 payload_hash bytea NOT NULL,
 dispatch_segment text NOT NULL,
 dispatch_machine text NOT NULL,
 dispatch_session text NOT NULL,
 dispatch_epoch bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(logical_machine,logical_session,request_id),
 FOREIGN KEY(logical_machine,logical_session) REFERENCES logical_conversations ON DELETE CASCADE
);
