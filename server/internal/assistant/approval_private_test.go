package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestPrivateApprovalRenewsJustMeForAnswerAndResultTurns(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ft.s.justMeSent("mac", "s1")
	ft.s.justMeStarted("mac", "s1", true, false)
	done := startPreparedInteraction(t, ft, proto.Interaction{ID: "ix_private_turns", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, ToolName: "persona_settings"}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
		return "private setting applied", nil
	}})
	ix, ok := ft.s.pendingIx("ix_private_turns")
	if !ok || !ix.Private || !ix.CanClarify {
		t.Fatalf("card raised in a Just me turn = %+v, present=%v", ix, ok)
	}
	if err := ft.s.clarify(context.Background(), ix.ID, "Why?", "owner-private-turns", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ft.s.justMeEnded("mac", "s1", false)
	ft.s.justMeStarted("mac", "s1", true, false)
	if !ft.s.justMeNow("mac", "s1") {
		t.Fatal("the turn answering a Private card's question lost Just me protection")
	}

	ft.s.justMeEnded("mac", "s1", false)
	allow := true
	if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if text := waitApprovalResult(t, ft, ix.ID); !strings.Contains(text, "private setting applied") {
		t.Fatalf("Private approval result = %q", text)
	}
	ft.s.justMeStarted("mac", "s1", true, false)
	if !ft.s.justMeNow("mac", "s1") {
		t.Fatal("the terminal-result turn of a Private approval lost Just me protection")
	}
}

func TestPrivateApprovalQueuedResultRenewsJustMeAfterRestart(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ft.s.justMeSent("mac", "s1")
	ft.s.justMeStarted("mac", "s1", true, false)
	done := startPreparedInteraction(t, ft, proto.Interaction{ID: "ix_private_delayed", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, ToolName: "persona_settings", Private: true}, approvalops.Operation{})
	if err := ft.s.clarify(context.Background(), "ix_private_delayed", "Why?", "owner-private-delayed", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true,
		Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	deny := false
	if err := ft.s.resolve("ix_private_delayed", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}

	var queued relayMsg
	waitFor(t, "Private approval result in the durable queue", func() bool {
		ft.s.relays.mu.Lock()
		defer ft.s.relays.mu.Unlock()
		if !ft.s.relayLocked(context.Background()) || len(ft.s.relays.st.Queue["s1"]) != 1 {
			return false
		}
		queued = ft.s.relays.st.Queue["s1"][0]
		return true
	})
	if !queued.Private {
		t.Fatal("the durable relay message dropped the Private mark")
	}

	// Rebuild the manager from the same durable state and deliver only after the
	// original three-hour Just me mark can no longer protect the result turn.
	restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
	ft.f.mu.Lock()
	ft.f.known["s1"] = true
	ft.f.mu.Unlock()
	now := time.Now().Add(justMeMax + time.Minute)
	recClock(restarted, func() time.Time { return now })
	if err := restarted.deliverRelay(context.Background(), "mac", "s1", queued); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_private_delayed")
	restarted.justMeStarted("mac", "s1", true, false)
	if !restarted.justMeNow("mac", "s1") {
		t.Fatal("a Private result delivered from the queue after restart was not Just me")
	}
}

func TestPrivateApprovalRecoveredJournalKeepsJustMe(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ft.f.mu.Lock()
	ft.f.known["s1"] = true
	ft.f.mu.Unlock()
	ft.s.justMeSent("mac", "s1")
	ft.s.justMeStarted("mac", "s1", true, false)
	ft.s.justMeEnded("mac", "s1", false)

	// No earlier journal row: the terminal fallback must not silently drop the
	// Private marker while recovering the delivery record.
	ft.s.approvalJournalFinished(context.Background(), proto.Interaction{
		ID: "ix_private_recovered", MachineID: "mac", SessionID: "s1", Private: true,
	}, "cancelled", "nothing was executed", true)
	if result := waitApprovalResult(t, ft, "ix_private_recovered"); !strings.Contains(result, "nothing was executed") {
		t.Fatalf("recovered Private approval result = %q", result)
	}
	ft.s.justMeStarted("mac", "s1", true, false)
	if !ft.s.justMeNow("mac", "s1") {
		t.Fatal("a recovered Private journal entry lost Just me on terminal delivery")
	}
}
