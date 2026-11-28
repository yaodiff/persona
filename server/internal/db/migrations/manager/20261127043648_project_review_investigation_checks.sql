-- projectReviewStep persists only these outcomes and retry reasons. The four
-- review_receipt_* results from projectReviewOutcome are prefixed on settlement;
-- investigationFindings is one report object (or NULL when there is no report).
-- NOT VALID: enforce new and updated rows without scanning or rejecting old rows.
ALTER TABLE project_review_deliveries ADD CONSTRAINT project_review_deliveries_event_shape
    CHECK (jsonb_typeof(event) = 'object') NOT VALID;
ALTER TABLE project_review_deliveries ADD CONSTRAINT project_review_deliveries_outcome_check
    CHECK (outcome IN ('', 'self_source',
        'review_receipt_triage_only', 'review_receipt_facts_recorded',
        'review_receipt_no_change', 'review_receipt_needs_evidence',
        'review_receipt_missing', 'send_echo_missing', 'send_unconfirmed',
        'target_not_ready', 'target_history_unavailable',
        'review_receipt_unconfirmed', 'waiting_for_review_receipt')) NOT VALID;
ALTER TABLE todo_supervision ADD CONSTRAINT todo_supervision_investigation_findings_shape
    CHECK (investigation_findings IS NULL OR jsonb_typeof(investigation_findings) = 'object') NOT VALID;
