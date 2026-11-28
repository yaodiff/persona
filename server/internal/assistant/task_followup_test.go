package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTaskFollowupOnlyRecordsConfirmedSupervisorSends(t *testing.T) {
	for _, tc := range []struct {
		name, from, text string
		err              error
		want             bool
	}{
		{"supervisor", "side1", "Check the missing test.", nil, true},
		{"other conversation", "unrelated", "Check the missing test.", nil, false},
		{"failed send", "side1", "Check the missing test.", errors.New("synthetic send failure"), false},
		{"every confirmed instruction counts", "side1", "exit", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, todo := sideReviewFixture(t)
			ctx := context.Background()
			a := todo.Agents[0]
			n, err := s.noteAgentSend(ctx, tc.from, a.MachineID, a.SessionID, "", tc.text)
			if err != nil {
				t.Fatal(err)
			}
			n.sent(ctx, tc.err)
			got := todoNow(s, todo.ID).Supervision
			if (got.FollowedUpAt != nil) != tc.want {
				t.Fatalf("followed_up_at = %v, want present %v", got.FollowedUpAt, tc.want)
			}
			if tc.want {
				if !got.UpdatedAt.Equal(todo.Supervision.UpdatedAt) || !got.PendingSince.Equal(*todo.Supervision.PendingSince) {
					t.Fatal("a send changed the review round or its age")
				}
				restored := supervisionFromRow(got.row(todo.ID))
				if restored.FollowedUpAt == nil || !restored.FollowedUpAt.Equal(*got.FollowedUpAt) {
					t.Fatal("follow-up did not survive the persisted row")
				}
				if got.clone().FollowedUpAt == got.FollowedUpAt {
					t.Fatal("snapshot shares its mutable follow-up pointer")
				}
			}
		})
	}
}

func TestTaskReviewRecordsFollowupButAutomaticWritesDoNot(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "Ready for review.")
	ctx := context.Background()
	if todoNow(s, todo.ID).Supervision.FollowedUpAt != nil {
		t.Fatal("automatic ledger write became supervisor follow-up")
	}
	args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "Add the missing test."})
	if _, err := s.toolTaskReview(ctx, "conv1", args); err != nil {
		t.Fatal(err)
	}
	got := todoNow(s, todo.ID).Supervision
	if got.FollowedUpAt == nil || time.Since(*got.FollowedUpAt) > time.Minute {
		t.Fatalf("review follow-up = %v", got.FollowedUpAt)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(raw, &wire) != nil || len(wire["followed_up_at"]) == 0 {
		t.Fatal("clients cannot read the supervisor follow-up")
	}
}
