package assistant

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func approvalTerminalResults(ft *fleetTest, id string) []string {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	var results []string
	for _, p := range ft.f.sends {
		text, _ := p["text"].(string)
		if strings.Contains(text, "Approval "+id+" ") {
			results = append(results, text)
		}
	}
	return results
}

func settledApprovalTerminalResults(t *testing.T, ft *fleetTest, id string) []string {
	t.Helper()
	// A result's direct try can hand it to the durable relay, whose drain runs
	// asynchronously. Waiting for the first visible result misses that later lane.
	waitFor(t, "approval "+id+" direct and relay delivery to settle", func() bool {
		ft.s.approvals.mu.Lock()
		delivering := ft.s.approvals.delivering
		ft.s.approvals.mu.Unlock()
		return !delivering && !drainingNow(ft.s, "s1") && len(approvalTerminalResults(ft, id)) > 0
	})
	return approvalTerminalResults(ft, id)
}

func TestApprovalTerminalResultDeliverySettlesExactlyOnce(t *testing.T) {
	t.Run("interrupted restart apply is uncertain", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := proto.Interaction{ID: "ix_interrupted", MachineID: "mac", SessionID: "s1"}
		if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
			t.Fatal(err)
		}
		if err := ft.s.approvalJournalApplying(context.Background(), ix); err != nil {
			t.Fatal(err)
		}
		reloadApprovalJournalForRestart(ft.s)
		ft.s.recoverApprovalJournals(context.Background())

		results := settledApprovalTerminalResults(t, ft, ix.ID)
		if len(results) != 1 {
			t.Fatalf("approval %s delivered %d terminal results after delivery settled: %q", ix.ID, len(results), results)
		}
		if !strings.Contains(results[0], "uncertain") || strings.Contains(results[0], "cancelled without executing") {
			t.Fatalf("interrupted apply result = %q", results[0])
		}
	})

	t.Run("ordinary clarified allow succeeds", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		const id = "ix_settled_allow"
		var applies atomic.Int32
		done := startPreparedTest(t, ft, id, "", func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		})
		if err := ft.s.clarify(context.Background(), id, "Why is this change needed?", "owner-settled-allow", nil); err != nil {
			t.Fatal(err)
		}
		if first := awaitPreparedTest(t, done); first.err != nil || !first.out.Clarifying {
			t.Fatalf("original tool result = %+v, %v", first.out, first.err)
		}
		allow := true
		if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		if got := applies.Load(); got != 1 {
			t.Fatalf("apply count = %d, want exactly one", got)
		}
		ft.waitPending(0)

		results := settledApprovalTerminalResults(t, ft, id)
		if len(results) != 1 {
			t.Fatalf("approval %s delivered %d terminal results after delivery settled: %q", id, len(results), results)
		}
		if !strings.Contains(results[0], "applied") || !strings.Contains(results[0], "setting applied") {
			t.Fatalf("ordinary terminal result = %q", results[0])
		}
	})
}
