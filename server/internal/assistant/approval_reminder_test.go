package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestPendingApprovalReminderPush(t *testing.T) {
	t.Run("unanswered permission card reminds once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan proto.Resolution, 1)
			go func() {
				r, _ := ft.s.waitInteraction(ctx, proto.Interaction{
					ID:           "ix_remind_once",
					MachineID:    "mac",
					SessionID:    "s1",
					Kind:         proto.IxPermission,
					OutwardWrite: true,
					ToolName:     "Bash",
					ExpiresAt:    time.Now().Add(2 * time.Hour).UTC(),
				})
				done <- r
			}()
			synctest.Wait()

			if got := ft.pushesOf(assistantwire.NotifyInput, 1, 0); len(got) != 1 {
				t.Fatalf("initial pushes = %d, want 1", len(got))
			}
			elapse(pendingReminderDelay + time.Second)
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, 0); len(got) != 2 {
				t.Fatalf("pushes after reminder delay = %d, want 2 (initial + 1 reminder): %+v", len(got), got)
			}
			// Advance past another delay window to verify at most one reminder fires.
			elapse(pendingReminderDelay + time.Minute)
			if again := ft.pushesOf(assistantwire.NotifyInput, 3, 0); len(again) != 2 {
				t.Fatalf("pushes after extra wait = %d, want 2", len(again))
			}
			cancel()
			<-done
		})
	})

	t.Run("resolved card before delay sends no reminder", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan proto.Resolution, 1)
			go func() {
				r, _ := ft.s.waitInteraction(ctx, proto.Interaction{
					ID:           "ix_remind_resolved",
					MachineID:    "mac",
					SessionID:    "s1",
					Kind:         proto.IxPermission,
					OutwardWrite: true,
					ToolName:     "Bash",
					ExpiresAt:    time.Now().Add(2 * time.Hour).UTC(),
				})
				done <- r
			}()
			synctest.Wait()

			if got := ft.pushesOf(assistantwire.NotifyInput, 1, 0); len(got) != 1 {
				t.Fatalf("initial pushes = %d, want 1", len(got))
			}
			allow := true
			if err := ft.s.resolve("ix_remind_resolved", proto.Resolution{Allow: &allow}); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			<-done
			elapse(pendingReminderDelay + time.Minute)
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, 0); len(got) != 1 {
				t.Fatalf("pushes after resolve = %d, want 1", len(got))
			}
		})
	})

	t.Run("short-lived card expiring before delay does not remind", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan proto.Resolution, 1)
			go func() {
				r, _ := ft.s.waitInteraction(ctx, proto.Interaction{
					ID:           "ix_remind_short",
					MachineID:    "mac",
					SessionID:    "s1",
					Kind:         proto.IxPermission,
					OutwardWrite: true,
					ToolName:     "Bash",
					ExpiresAt:    time.Now().Add(10 * time.Minute).UTC(),
				})
				done <- r
			}()
			synctest.Wait()

			if got := ft.pushesOf(assistantwire.NotifyInput, 1, 0); len(got) != 1 {
				t.Fatalf("initial pushes = %d, want 1", len(got))
			}
			elapse(10*time.Minute + time.Second)
			<-done
			elapse(pendingReminderDelay + time.Minute)
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, 0); len(got) != 1 {
				t.Fatalf("pushes on expired card = %d, want 1", len(got))
			}
		})
	})

	t.Run("muted session suppresses reminder", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan proto.Resolution, 1)
			go func() {
				r, _ := ft.s.waitInteraction(ctx, proto.Interaction{
					ID:        "ix_remind_muted",
					MachineID: "mac",
					SessionID: "s1",
					Kind:      proto.IxQuestion,
					ToolName:  "ask_user",
					ToolInput: json.RawMessage(`{"why_owner":"priority"}`),
					ExpiresAt: time.Now().Add(2 * time.Hour).UTC(),
				})
				done <- r
			}()
			synctest.Wait()

			if got := ft.pushesOf(assistantwire.NotifyInput, 1, 0); len(got) != 1 {
				t.Fatalf("initial pushes = %d, want 1", len(got))
			}
			ft.s.setMutes(assistantwire.Mutes{Sessions: []string{"mac/s1"}})

			elapse(pendingReminderDelay + time.Minute)
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, 0); len(got) != 1 {
				t.Fatalf("pushes after mute = %d, want 1", len(got))
			}
			cancel()
			<-done
		})
	})

	t.Run("prepared card entering keepWaiting arms reminder on extended deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			preparedTestConversation(ft)

			// Initial expiry (10m) is shorter than pendingReminderDelay (30m), so only keepWaiting's 24h deadline arms the reminder.
			done := startShortPrepared(t, ft, "ix_remind_waiting", 10*time.Minute, func(context.Context, proto.Resolution) (string, error) {
				return "applied", nil
			})
			if got := ft.pushesOf(assistantwire.NotifyInput, 1, 0); len(got) != 1 {
				t.Fatalf("initial pushes = %d, want 1", len(got))
			}
			elapse(10*time.Minute + time.Second)
			res := awaitPreparedTest(t, done)
			if res.err != nil || !res.out.Clarifying || res.out.Text != approvalops.WaitingText {
				t.Fatalf("waitPreparedCard = (%+v, %v), want clarifying waiting result", res.out, res.err)
			}
			elapse(pendingReminderDelay + time.Second)
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, 0); len(got) != 2 {
				t.Fatalf("pushes after keepWaiting = %d, want 2 (initial + 1 reminder): %+v", len(got), got)
			}
			allow := false
			if err := ft.s.resolve("ix_remind_waiting", proto.Resolution{Allow: &allow}); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			synctest.Wait()
		})
	})
}
