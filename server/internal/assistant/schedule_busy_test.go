package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A scheduled task's conversation that waits for the user (a terminal dialog, an approval card) is busy: the next run
// is skipped, not sent into it. The node's own list has no unified state; the manager's has.
func TestSessionBusyWhileItWaitsForTheUser(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	now := time.Now().UTC()
	ft.sessions("mac",
		proto.Session{SessionID: "dialog", Status: proto.StatusBlocked, UpdatedAt: now},
		proto.Session{SessionID: "card", Status: proto.StatusDone, UpdatedAt: now},
		proto.Session{SessionID: "done", Status: proto.StatusDone, UpdatedAt: now})
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = s.waitInteraction(wctx, proto.Interaction{ID: "ix_card", MachineID: "mac", SessionID: "card",
			Kind: proto.IxPermission, OutwardWrite: true, Summary: "Bash: rm -rf build", ExpiresAt: now.Add(time.Minute)})
	}()
	waitFor(t, "the approval card", func() bool { _, ok := s.pendingIx("ix_card"); return ok })
	ctx := context.Background()
	if !s.sessionBusy(ctx, "mac", "dialog") {
		t.Error("held by a terminal dialog: busy")
	}
	if !s.sessionBusy(ctx, "mac", "card") {
		t.Error("waiting on an approval card: busy")
	}
	if s.sessionBusy(ctx, "mac", "done") {
		t.Error("a finished turn is not busy")
	}
}
