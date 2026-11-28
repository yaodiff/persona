package assistant

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func storedApprovalJournal(t *testing.T, ft *fleetTest) []approvalJournalEntry {
	t.Helper()
	var list []approvalJournalEntry
	if _, err := ft.st.Setting(context.Background(), approvalJournalKey, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func putApprovalJournal(t *testing.T, ft *fleetTest, entry approvalJournalEntry) {
	t.Helper()
	if err := ft.st.PutSetting(context.Background(), approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
		t.Fatal(err)
	}
}

// reloadApprovalJournalForRestart discards process-local live markers in a test
// that keeps its pending card but simulates startup recovery. The next recovery
// reads the persisted journal exactly as a new Service would.
func reloadApprovalJournalForRestart(s *Service) {
	s.approvals.mu.Lock()
	s.approvals.loaded = false
	s.approvals.list = nil
	s.approvals.mu.Unlock()
}

func TestApprovalResultJournalRemovesHandedOffEntries(t *testing.T) {
	t.Run("delivered", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startPreparedTest(t, ft, "ix_journal_delivered", "", nil)
		if err := ft.s.clarify(context.Background(), "ix_journal_delivered", "Question", "owner-journal-delivered", nil); err != nil {
			t.Fatal(err)
		}
		awaitPreparedTest(t, done)
		deny := false
		if err := ft.s.resolve("ix_journal_delivered", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_journal_delivered")
		waitFor(t, "delivered approval journal cleanup", func() bool {
			return len(storedApprovalJournal(t, ft)) == 0
		})
		ft.s.deliverApprovalResults()
		waitApprovalResult(t, ft, "ix_journal_delivered") // still exactly one, never redelivered
	})

	t.Run("accepted by an older version", func(t *testing.T) {
		ft := newFleetTest(t)
		putApprovalJournal(t, ft, approvalJournalEntry{ID: "ix_journal_legacy", MachineID: "mac", SessionID: "gone",
			Result: "Approval ix_journal_legacy is denied and is no longer pending.", Accepted: true, UpdatedAt: time.Now().UTC()})
		ft.s.deliverApprovalResults()
		if got := storedApprovalJournal(t, ft); len(got) != 0 {
			t.Fatalf("legacy handed-off entries survived cleanup: %+v", got)
		}
		ft.f.mu.Lock()
		sends := len(ft.f.sends)
		ft.f.mu.Unlock()
		if sends != 0 {
			t.Fatalf("cleanup redelivered a legacy result %d times", sends)
		}
	})
}

func TestApprovalResultJournalStopsRetryingDeletedConversation(t *testing.T) {
	ft := newFleetTest(t)
	other := proto.Session{MachineID: "mac", SessionID: "other", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	ft.f.setSessions("mac", other)
	ft.d.setSessions("mac", []proto.Session{other})
	entry := approvalJournalEntry{ID: "ix_journal_deleted", MachineID: "mac", SessionID: "deleted",
		Result: "Approval ix_journal_deleted is denied and is no longer pending.", UpdatedAt: time.Now().UTC()}
	row := deletedConversation{Machine: "mac", ID: "deleted", At: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), deletedConversationsKey, map[string]deletedConversation{runtimeListID("mac", "deleted"): row}); err != nil {
		t.Fatal(err)
	}
	putApprovalJournal(t, ft, entry)
	if err := ft.s.deliverApprovalResult(entry); !errors.Is(err, errApprovalConversationGone) {
		t.Fatalf("missing originating conversation error = %v", err)
	}
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 0 {
		t.Fatalf("permanently undeliverable result was retained: %+v", got)
	}
	ft.s.approvals.mu.Lock()
	retry := ft.s.approvals.retry
	ft.s.approvals.mu.Unlock()
	if retry {
		t.Fatal("a deleted originating conversation scheduled another retry")
	}
	ft.s.deliverApprovalResults()
	ft.f.mu.Lock()
	sends := len(ft.f.sends)
	ft.f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("a result for a deleted conversation was delivered %d times", sends)
	}
}

func TestApprovalResultJournalStopsRetryingNotFoundConversation(t *testing.T) {
	ft := newFleetTest(t)
	conv := proto.Session{MachineID: "mac", SessionID: "unreadable", Assistant: true, Agent: "antigravity", Status: proto.StatusIdle}
	ft.f.setSessions("mac", conv)
	ft.d.setSessions("mac", []proto.Session{conv})
	ft.f.mu.Lock()
	ft.f.failAssistant = &rpc.Error{Code: "not_found", Message: "session unreadable"}
	ft.f.mu.Unlock()

	entry := approvalJournalEntry{ID: "ix_journal_not_found", MachineID: "mac", SessionID: "unreadable",
		Result: "Approval ix_journal_not_found is approved.", UpdatedAt: time.Now().UTC()}
	putApprovalJournal(t, ft, entry)

	if err := ft.s.deliverApprovalResult(entry); !errors.Is(err, errApprovalConversationGone) {
		t.Fatalf("not_found originating conversation error = %v, want errApprovalConversationGone", err)
	}
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 0 {
		t.Fatalf("permanently undeliverable not_found result was retained: %+v", got)
	}
	if got := ft.s.queuedFor("unreadable"); got != 0 {
		t.Fatalf("not_found approval result was queued (%d), want 0", got)
	}
}

func TestApprovalResultJournalRetainsUncertainOutcomeForDeletedConversation(t *testing.T) {
	ft := newFleetTest(t)
	entry := approvalJournalEntry{ID: "ix_journal_deleted_uncertain", MachineID: "mac", SessionID: "deleted", Status: "failed",
		Result: "Approval ix_journal_deleted_uncertain left with an uncertain execution outcome and is no longer pending.", UpdatedAt: time.Now().UTC()}
	row := deletedConversation{Machine: "mac", ID: "deleted", At: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), deletedConversationsKey, map[string]deletedConversation{runtimeListID("mac", "deleted"): row}); err != nil {
		t.Fatal(err)
	}
	putApprovalJournal(t, ft, entry)
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Status != "failed" || got[0].Result == "" {
		t.Fatalf("a deleted destination silently discarded the uncertain execution outcome: %+v", got)
	}
	ft.s.approvals.mu.Lock()
	retry := ft.s.approvals.retry
	ft.s.approvals.mu.Unlock()
	if retry {
		t.Fatal("an explicitly deleted destination scheduled an endless uncertain-result retry")
	}
}

func TestDeletedUncertainResultRetriesFailedGrantRevocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		st := &grantWriteFailStore{fleetMem: ft.st}
		ft.s.setStore(func() Store { return st })
		entry := approvalJournalEntry{ID: "ix_deleted_revocation_retry", MachineID: "mac", SessionID: "deleted", Status: "failed",
			Result: "Approval ix_deleted_revocation_retry left with an uncertain execution outcome and is no longer pending.", UpdatedAt: time.Now().UTC()}
		row := deletedConversation{Machine: "mac", ID: "deleted", At: time.Now().UTC()}
		if err := ft.st.PutSetting(context.Background(), deletedConversationsKey, map[string]deletedConversation{runtimeListID("mac", "deleted"): row}); err != nil {
			t.Fatal(err)
		}
		if err := ft.s.putDetachedGrant(context.Background(), detachedTestCard(entry.ID), proto.Resolution{}); err != nil {
			t.Fatal(err)
		}
		st.fail.Store(true)
		putApprovalJournal(t, ft, entry)
		ft.s.deliverApprovalResults()
		ft.s.approvals.mu.Lock()
		retry := ft.s.approvals.retry
		ft.s.approvals.mu.Unlock()
		if !retry {
			t.Fatal("failed durable revocation did not schedule a retry for a deleted destination")
		}

		st.fail.Store(false)
		elapse(time.Minute)
		var grants map[string]detachedGrant
		if _, err := st.Setting(context.Background(), detachedGrantSetting, &grants); err != nil || len(grants) != 0 {
			t.Fatalf("durable grant survived the revocation retry: %+v, %v", grants, err)
		}
		if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Status != "failed" {
			t.Fatalf("uncertain deleted-conversation evidence was discarded: %+v", got)
		}
		ft.s.approvals.mu.Lock()
		retry = ft.s.approvals.retry
		ft.s.approvals.mu.Unlock()
		if retry {
			t.Fatal("successful revocation kept scheduling retries for a deleted destination")
		}
	})
}

func TestApprovalResultJournalKeepsTransientFailureForRetry(t *testing.T) {
	ft := newFleetTest(t)
	entry := approvalJournalEntry{ID: "ix_journal_transient", MachineID: "offline", SessionID: "s1",
		Result: "Approval ix_journal_transient is denied and is no longer pending.", UpdatedAt: time.Now().UTC()}
	putApprovalJournal(t, ft, entry)
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Result == "" {
		t.Fatalf("transient failure discarded the result: %+v", got)
	}
	ft.s.approvals.mu.Lock()
	retry := ft.s.approvals.retry
	ft.s.approvals.mu.Unlock()
	if !retry {
		t.Fatal("a transient delivery failure did not schedule a retry")
	}

	ft.d.add("offline", "Offline")
	working := proto.Session{MachineID: "offline", SessionID: "s1", Assistant: true,
		Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("offline", working)
	ft.d.setSessions("offline", []proto.Session{working})
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 0 {
		t.Fatalf("successful durable-queue handoff was not cleaned up: %+v", got)
	}
	ft.s.relays.mu.Lock()
	defer ft.s.relays.mu.Unlock()
	if !ft.s.relayLocked(context.Background()) || len(ft.s.relays.st.Queue["s1"]) != 1 {
		t.Fatalf("transient result was not preserved for delivery: %+v", ft.s.relays.st.Queue["s1"])
	}
}

func TestApprovalResultJournalRetriesUntilRestartedSessionListArrives(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	id := "ix_journal_restart_unlisted"
	done := startPreparedTest(t, ft, id, "", nil)
	if err := ft.s.clarify(context.Background(), id, "Question", "owner-journal-restart-unlisted", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	// A restarted manager has the durable journal and configured machine, but the
	// node has not listed its sessions on the new connection yet.
	f2 := newFakeHub()
	d2 := newFleetDevs()
	d2.add("mac", "Mac")
	restarted := New(Options{Hub: f2, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: d2})
	restarted.recoverApprovalJournals(context.Background())
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Result == "" {
		t.Fatalf("recovery before the session list discarded the terminal result: %+v", got)
	}

	sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
	f2.setSessions("mac", sess)
	d2.setSessions("mac", []proto.Session{sess})
	f2.mu.Lock()
	f2.known["s1"] = true
	f2.mu.Unlock()
	restarted.deliverApprovalResults()
	waitFakeApprovalResult(t, f2, id)
}

func TestApprovalResultJournalRetriesBrieflyUnlistedConversation(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	id := "ix_journal_briefly_unlisted"
	done := startPreparedTest(t, ft, id, "", nil)
	if err := ft.s.clarify(context.Background(), id, "Question", "owner-journal-briefly-unlisted", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ft.f.setSessions("mac")
	ft.d.setSessions("mac", nil)
	deny := false
	if err := ft.s.resolve(id, proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Result == "" {
		t.Fatalf("a temporarily unlisted conversation discarded the terminal result: %+v", got)
	}

	preparedTestConversation(ft)
	ft.f.mu.Lock()
	ft.f.known["s1"] = true
	ft.f.mu.Unlock()
	ft.s.deliverApprovalResults()
	waitApprovalResult(t, ft, id)
}
