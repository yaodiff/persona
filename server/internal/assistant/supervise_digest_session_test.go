package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sessionGetsFor counts the session.get calls the node was asked for conversation to.
func sessionGetsFor(f *fakeHub, to string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method != DeviceSessionGet {
			continue
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(c.Params, &a) == nil && a.SessionID == to {
			n++
		}
	}
	return n
}

// digestSessionGets delivers a one-signal digest to an idle conv1 and counts the session.get calls it made, failing
// unless the digest went straight in.
func digestSessionGets(t *testing.T, s *Service, f *fakeHub, sig superviseSignal) int {
	t.Helper()
	ctx := context.Background()
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[sig.id()] = sig })
	before, sent := sessionGetsFor(f, "conv1"), len(f.noticesTo("conv1"))
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{sig}, "", time.Now(), time.Time{}) {
		t.Fatalf("the %s digest was not delivered", sig.Kind)
	}
	if n := f.noticesTo("conv1"); len(n) != sent+1 {
		t.Fatalf("want the %s digest sent directly, got %q", sig.Kind, n)
	}
	return sessionGetsFor(f, "conv1") - before
}

// A digest with no signal that needs delivery-time revalidation goes straight to the direct send, which looks the
// conversation up itself. The up-front lookup for #16637's partition only matters when a PR, draft, decision-reply
// or unhandled-inbox signal rides along, so an ordinary digest makes one session.get fewer than a PR-conflict digest
// that goes in the same way.
func TestOrdinaryDigestSkipsThePartitionSessionLookup(t *testing.T) {
	s, f, prTask := newPRConflictTask(t, &memStore{})
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	ctx := context.Background()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if err := s.applyPRStatus(ctx, prTask, TodoPRStatus{
		URL: prTask.PRURL, State: "open", HeadOID: "head-lookups", Conflicts: true, CheckedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	prSig, ok := pendingPRConflict(s, prTask.ID)
	if !ok {
		t.Fatal("missing pending PR conflict signal")
	}
	done, err := s.createTodo(ctx, "Completed task", "ship it", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, done.ID, "conv1", []string{"Merged"}, 0)
	doneSig := superviseSignal{Todo: done.ID, Kind: sigReport, Report: upReportDone, Key: "done", Detail: "Task done", At: time.Now().UTC()}

	withPartition := digestSessionGets(t, s, f, prSig)
	ordinary := digestSessionGets(t, s, f, doneSig)
	if ordinary != withPartition-1 {
		t.Errorf("an ordinary digest asked the node for conv1 %d times, a PR-conflict digest %d: want exactly one fewer",
			ordinary, withPartition)
	}
}

// The partition and the send decide on the same relay: right after the owner's round ends the owner is still present
// (ownerPresentFor), so the send would queue a digest rather than interrupt. The partition must see that hold too, or
// a PR-conflict signal rides into the send, queueDigest refuses the whole digest, and the completed-task report next
// to it goes back to pending with it until the window passes.
func TestMixedDigestPartitionsWhileTheOwnerIsStillPresent(t *testing.T) {
	presenceTimes(t, time.Hour, time.Hour)
	s, f, prTask := newPRConflictTask(t, &memStore{})
	stopHolds(t, s)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	ctx := context.Background()
	session := func(status string) proto.Session {
		return proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: status}
	}
	f.setSessions("server", session(proto.StatusIdle))
	if err := s.applyPRStatus(ctx, prTask, TodoPRStatus{
		URL: prTask.PRURL, State: "open", HeadOID: "head-present", Conflicts: true, CheckedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	prSig, ok := pendingPRConflict(s, prTask.ID)
	if !ok {
		t.Fatal("missing pending PR conflict signal")
	}
	done, err := s.createTodo(ctx, "Completed task", "ship it", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, done.ID, "conv1", []string{"Merged"}, 0)
	doneSig := superviseSignal{Todo: done.ID, Kind: sigReport, Report: upReportDone, Key: "done", Detail: "Task done", At: time.Now().UTC()}
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[doneSig.id()] = doneSig })

	// The owner speaks and the round runs and ends: the turn gate is gone, the presence window is not.
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	s.setSessions("server", []proto.Session{session(proto.StatusWorking)})
	waitFor(t, "owner turn seen working", func() bool {
		s.relays.mu.Lock()
		defer s.relays.mu.Unlock()
		return s.relays.ownerTurns["conv1"].working
	})
	s.setSessions("server", []proto.Session{session(proto.StatusIdle)})
	waitFor(t, "owner turn seen over", func() bool {
		s.relays.mu.Lock()
		defer s.relays.mu.Unlock()
		_, gated := s.relays.ownerTurns["conv1"]
		return !gated
	})

	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{prSig, doneSig}, "", time.Now(), time.Time{}) {
		t.Fatal("the mixed digest was turned away instead of queueing its completed-task report")
	}
	q := queuedRelays(s, "conv1")
	if len(q) != 1 || !strings.Contains(q[0].Text, "Completed task") || strings.Contains(q[0].Text, prTask.Title) {
		t.Fatalf("want the completed-task report queued alone, got %+v", q)
	}
	if sent := f.sentTo("conv1"); sent != 0 {
		t.Fatalf("the digest interrupted the owner: sent=%d", sent)
	}
	if _, still := pendingPRConflict(s, prTask.ID); !still {
		t.Fatal("the PR conflict signal left pending instead of waiting for revalidation")
	}
	if _, pending := storedSupervisePending(t, s)[doneSig.id()]; pending {
		t.Fatal("the queued completed-task report stayed in SupervisePending")
	}
}
