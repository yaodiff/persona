-- UpsertPRFollowupRecords in server/internal/assistant/repositorystore/pr_followups.go leaves state at its '' default.
-- SavePRFollowupObservation receives forge states from applyFollowupObservation in server/internal/assistant/pr_followup.go.
-- The sweep saves open observations and deletes subscriptions on terminal observations. The check accepts the canonical
-- ReviewOpen, ReviewClosed and ReviewMerged states from server/internal/assistant/repository/review.go.
-- UpsertPRFollowupRecords, SavePRFollowupObservation and SavePRFollowupWatchStatus write PRFollowupWatchActive or
-- PRFollowupWatchHostNotConnected (server/internal/assistant/repository/pr_followup.go). Keep '' from the watch-status
-- migration for legacy rows that read as active.
-- NOT VALID enforces inserts and updates without scanning existing rows.
ALTER TABLE todo_pr_followups
    DROP CONSTRAINT IF EXISTS todo_pr_followups_state_check,
    DROP CONSTRAINT IF EXISTS todo_pr_followups_watch_status_check,
    ADD CONSTRAINT todo_pr_followups_state_check
        CHECK (state IN ('', 'open', 'closed', 'merged')) NOT VALID,
    ADD CONSTRAINT todo_pr_followups_watch_status_check
        CHECK (watch_status IN ('', 'active', 'host_not_connected')) NOT VALID;
