-- Hub trust and link-switch writers marshal a detail object. AddMachineAudit uses {} for missing detail.
ALTER TABLE machine_audit ADD CONSTRAINT machine_audit_detail_shape
    CHECK (jsonb_typeof(detail) = 'object') NOT VALID;
