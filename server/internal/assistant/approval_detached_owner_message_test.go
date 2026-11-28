package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// When the owner sends a new chat message to a conversation with an unanswered
// Detachable card still inside approvalInitialWait, the card must detach into
// its 24-hour waiting state right away instead of holding the turn for 75s.
func TestOwnerMessageDetachesPendingCommandApprovalImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		ix := detachedTestCard("ix_owner_instant_detach")
		done := startDetachedTest(t, ft, ix)

		// Advance well below the 75s approvalInitialWait so the timer alone cannot fire.
		elapse(10 * time.Millisecond)

		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "先忙别的，这条稍后我再看", "client_msg_id": "owner-msg-1"},
		}); err != nil {
			t.Fatalf("SendParams: %v", err)
		}

		// Must release promptly with no further clock advance past 75s.
		synctest.Wait()
		select {
		case first := <-done:
			if first.err != nil || !first.res.Pending || !first.res.Dismiss || first.res.Allow != nil ||
				first.res.ApprovalID != ix.ID || !strings.Contains(first.res.Message, approvalops.WaitingText) ||
				!strings.Contains(first.res.Message, "The command was not executed.") {
				t.Fatalf("detached hook result = %+v, %v; want WaitingText non-final pending release", first.res, first.err)
			}
		default:
			t.Fatal("owner message did not detach the pending command card before approvalInitialWait")
		}

		// Card stays pending with the extended approvalWaitingWindow deadline (both in memory and stored).
		pending := ft.s.pendingInteractions()
		if len(pending) != 1 || pending[0].ID != ix.ID {
			t.Fatalf("pending cards = %+v, want [%s]", pending, ix.ID)
		}
		if remaining := time.Until(pending[0].ExpiresAt); remaining < approvalWaitingWindow-time.Minute {
			t.Fatalf("waiting deadline remaining = %v, want ~%v", remaining, approvalWaitingWindow)
		}
		records, err := ft.st.Interactions(context.Background(), "mac", "s1", 10)
		if err != nil || len(records) != 1 || !records[0].Interaction.ExpiresAt.Equal(pending[0].ExpiresAt) {
			t.Fatalf("stored interactions = %+v (%v), want stored deadline %v", records, err, pending[0].ExpiresAt)
		}

		// Later owner Allow still mints and consumes the exact-once detached grant.
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("late Allow failed: %v", err)
		}
		if result := waitApprovalResult(t, ft, ix.ID); !strings.Contains(result, "approved") {
			t.Fatalf("continuation result = %q", result)
		}
		retry := detachedTestCard("ix_owner_instant_detach_retry")
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
			t.Fatalf("exact retry = %+v, %v; want one-time Allow", got, err)
		}
		second := startDetachedTest(t, ft, detachedTestCard("ix_owner_instant_detach_second"))
		deny := false
		if err := ft.s.resolve("ix_owner_instant_detach_second", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		if got := awaitDetachedTest(t, second); got.res.Allow != nil && *got.res.Allow {
			t.Fatalf("second retry reused consumed grant: %+v", got.res)
		}
	})
}

func TestOwnerMessageLeavesNonDetachableAndOtherSessionsUntouched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		otherSess := proto.Session{MachineID: "mac", SessionID: "s2", Assistant: true,
			Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
		ft.f.setSessions("mac",
			proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle},
			otherSess,
		)
		ft.d.setSessions("mac", []proto.Session{
			{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle},
			otherSess,
		})

		// 1. Non-detachable prepared card in (mac, s1).
		prepDone := startPreparedTest(t, ft, "ix_non_detachable_prep", "", nil)

		// 2. Ordinary non-detachable hook permission card in (mac, s1).
		ordinaryIx := proto.Interaction{
			ID: "ix_non_detachable_ordinary", MachineID: "mac", SessionID: "s1",
			Cwd: "/home/u/work", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
			ToolInput: json.RawMessage(`{"command":"ls"}`),
			CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
		}
		ordinaryDone := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.waitInteraction(context.Background(), ordinaryIx)
			ordinaryDone <- detachedTestResult{res, err}
		}()

		// 3. Detachable card in a DIFFERENT session (mac, s2).
		otherIx := detachedTestCard("ix_other_session_detachable")
		otherIx.SessionID = "s2"
		otherDone := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), otherIx)
			otherDone <- detachedTestResult{res, err}
		}()
		ft.waitPending(3)

		elapse(10 * time.Millisecond)
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "hello", "client_msg_id": "owner-msg-untouched"},
		}); err != nil {
			t.Fatalf("SendParams: %v", err)
		}
		synctest.Wait()

		select {
		case got := <-prepDone:
			t.Fatalf("non-detachable prepared card was released early: %+v", got)
		default:
		}
		select {
		case got := <-ordinaryDone:
			t.Fatalf("non-detachable ordinary card was released early: %+v", got)
		default:
		}
		select {
		case got := <-otherDone:
			t.Fatalf("other session's detachable card was released early: %+v", got)
		default:
		}

		// Cleanup all three cards cleanly.
		deny := false
		for _, id := range []string{"ix_non_detachable_prep", "ix_non_detachable_ordinary", "ix_other_session_detachable"} {
			if err := ft.s.resolve(id, proto.Resolution{Allow: &deny}); err != nil {
				t.Fatalf("cleanup resolve(%s): %v", id, err)
			}
		}
		awaitPreparedTest(t, prepDone)
		awaitDetachedTest(t, ordinaryDone)
		awaitDetachedTest(t, otherDone)
	})
}

func TestOwnerMessageDoesNotDetachScheduledConversation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ft.s.originSet(context.Background(), "mac", "s1", OriginTask, "task_sched_owner", "")

		done := startDetachedTest(t, ft, detachedTestCard("ix_sched_owner_msg"))
		elapse(10 * time.Millisecond)

		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "hi", "client_msg_id": "sched-msg-1"},
		}); err != nil {
			t.Fatalf("SendParams: %v", err)
		}
		synctest.Wait()

		select {
		case got := <-done:
			t.Fatalf("scheduled conversation card detached early on owner message: %+v", got)
		default:
		}

		// Wait out approvalInitialWait: scheduled card still expires instead of becoming 24h waiting.
		elapse(approvalInitialWait)
		first := awaitDetachedTest(t, done)
		if first.err != nil || first.res.Pending || !first.res.Dismiss {
			t.Fatalf("scheduled card outcome = %+v, %v; want expiry without pending retention", first.res, first.err)
		}
		ft.waitPending(0)
	})
}

func TestNonOwnerAndNoContentSendsDoNotDetach(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_non_owner_guard"))
		elapse(10 * time.Millisecond)

		// 1. Automated / channel "answer again" (owner=false).
		if _, err := ft.s.messageParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Params:    map[string]any{"text": "answer again", "client_msg_id": "auto-1"},
		}, false); err != nil {
			t.Fatalf("messageParams(owner=false): %v", err)
		}
		// 2. NodeAnswer parameter probe (no text or images).
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Params:    map[string]any{},
		}); err != nil {
			t.Fatalf("SendParams(empty): %v", err)
		}
		// 3. Whitespace-only message without images.
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "   \t\n ", "client_msg_id": "blank-1"},
		}); err != nil {
			t.Fatalf("SendParams(blank): %v", err)
		}
		// 4. Lowercase sendParams (supervision / relay / system notices).
		if _, err := ft.s.sendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Params:    map[string]any{"text": "automated notice"},
		}); err != nil {
			t.Fatalf("sendParams(system): %v", err)
		}
		synctest.Wait()

		select {
		case got := <-done:
			t.Fatalf("non-owner or empty send detached card: %+v", got)
		default:
		}

		// Cleanup.
		deny := false
		if err := ft.s.resolve("ix_non_owner_guard", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		awaitDetachedTest(t, done)
	})
}

// An exact owner-action approval reply (approval_reply_to) resolves the target
// proposal without sending a conversation turn, so it must not detach an
// unrelated pending Detachable card in the same conversation.
func TestExactOwnerApprovalReplyDoesNotDetachUnrelatedCommandCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		st := &ownerActionTestStore{}
		ft.s = New(Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
		preparedTestConversation(ft)
		on := true
		if err := st.PutSetting(context.Background(), "assistant", Settings{
			MachineID: "mac", Main: "s1", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on,
		}); err != nil {
			t.Fatal(err)
		}
		connector := &ownerActionConnector{svc: ft.s}
		ft.s.SetExtensions(connector)

		// Start an unrelated pending Detachable command card on (mac, s1).
		cmdDone := startDetachedTest(t, ft, detachedTestCard("ix_unrelated_cmd"))

		// Start an owner-action proposal batch on the same (mac, s1).
		args := json.RawMessage(`{"items":[{"tool":"mail_send","input":{"to":[{"email":"a@example.test"}],"subject":"s","body":"b"}}]}`)
		batchDone := make(chan error, 1)
		go func() {
			_, err := ft.s.toolOwnerActionPrepare(context.Background(), "mac", "s1", args)
			batchDone <- err
		}()
		ft.waitPending(3) // 1 detachable command card + 1 batch card + 1 item card
		var proposalID string
		for _, ix := range ft.s.pendingInteractions() {
			if ix.ToolName == ownerActionBatchTool {
				proposalID = ix.ID
				break
			}
		}
		if proposalID == "" {
			t.Fatal("owner-action batch proposal card did not appear")
		}

		elapse(10 * time.Millisecond)
		out, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "发", "client_msg_id": "reply-approve-1", "approval_reply_to": proposalID},
		})
		if err != nil || out[ownerApprovalHandledParam] != proposalID {
			t.Fatalf("exact approval reply = %+v, %v; want owner_approval_handled=%s", out, err, proposalID)
		}
		if err := <-batchDone; err != nil {
			t.Fatalf("batch prepare: %v", err)
		}
		synctest.Wait()

		select {
		case got := <-cmdDone:
			t.Fatalf("exact owner approval reply spuriously detached unrelated command card: %+v", got)
		default:
		}

		deny := false
		if err := ft.s.resolve("ix_unrelated_cmd", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		awaitDetachedTest(t, cmdDone)
	})
}

func TestOwnerMessageDetachesMultipleParallelCardsAndImageOnlyMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		doneA := startDetachedTest(t, ft, detachedTestCard("ix_parallel_a"))
		doneB := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_parallel_b"))
			doneB <- detachedTestResult{res: res, err: err}
		}()
		ft.waitPending(2)
		elapse(10 * time.Millisecond)

		// Image-only owner message (no text, JSON-decoded []any images slice as over hub RPC).
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params: map[string]any{
				"images":        []any{map[string]any{"mime": "image/png", "data": "iVBORw0KGgo="}},
				"client_msg_id": "img-only-1",
			},
		}); err != nil {
			t.Fatalf("SendParams(image-only): %v", err)
		}
		synctest.Wait()

		for _, tc := range []struct {
			id   string
			done <-chan detachedTestResult
		}{{"ix_parallel_a", doneA}, {"ix_parallel_b", doneB}} {
			select {
			case got := <-tc.done:
				if got.err != nil || !got.res.Pending || !got.res.Dismiss || got.res.ApprovalID != tc.id {
					t.Fatalf("%s detach = %+v, %v", tc.id, got.res, got.err)
				}
			default:
				t.Fatalf("%s did not detach immediately on image-only owner message", tc.id)
			}
		}

		deny := false
		for _, id := range []string{"ix_parallel_a", "ix_parallel_b"} {
			if err := ft.s.resolve(id, proto.Resolution{Allow: &deny}); err != nil {
				t.Fatal(err)
			}
			waitApprovalResult(t, ft, id)
		}
	})
}

// Clarifying one Detachable card preserves its own ClarifyingText and paused
// timer (no deadlock on a.attempt, no premature detach if sendOnce fails), and
// once accepted it also detaches any sibling un-clarified Detachable card so
// the clarification question is not held behind the sibling's 75s wait.
func TestClarifyFailureDoesNotDetachAndClarifySuccessDetachesSiblingCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		primaryIx := detachedTestCard("ix_clarify_primary")
		primaryDone := startDetachedTest(t, ft, primaryIx)
		siblingIx := detachedTestCard("ix_clarify_sibling")
		siblingDone := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), siblingIx)
			siblingDone <- detachedTestResult{res: res, err: err}
		}()
		ft.waitPending(2)
		elapse(10 * time.Millisecond)

		// 1. Failed clarify must leave both cards un-clarified and still waiting.
		ft.f.mu.Lock()
		ft.f.failSendOnce = map[string]error{"mac/s1": errors.New("synthetic send refusal")}
		ft.f.mu.Unlock()
		if err := ft.s.clarify(context.Background(), primaryIx.ID, "Why?", "clarify-fail-1", nil); err == nil {
			t.Fatal("expected clarify to fail on failSendOnce")
		}
		synctest.Wait()
		select {
		case got := <-primaryDone:
			t.Fatalf("failed clarify released primary card: %+v", got)
		default:
		}
		select {
		case got := <-siblingDone:
			t.Fatalf("failed clarify detached sibling card: %+v", got)
		default:
		}

		// 2. Successful clarify keeps ClarifyingText on primary and detaches sibling immediately.
		ft.f.mu.Lock()
		ft.f.failSendOnce = nil
		ft.f.mu.Unlock()
		if err := ft.s.clarify(context.Background(), primaryIx.ID, "Why run this?", "clarify-ok-1", nil); err != nil {
			t.Fatalf("clarify: %v", err)
		}
		synctest.Wait()

		primaryRes := awaitDetachedTest(t, primaryDone)
		if primaryRes.err != nil || !primaryRes.res.Pending || !primaryRes.res.Dismiss ||
			primaryRes.res.Message != approvalops.ClarifyingText {
			t.Fatalf("primary clarify result = %+v, %v; want ClarifyingText", primaryRes.res, primaryRes.err)
		}
		select {
		case siblingRes := <-siblingDone:
			if siblingRes.err != nil || !siblingRes.res.Pending || !siblingRes.res.Dismiss ||
				!strings.Contains(siblingRes.res.Message, approvalops.WaitingText) {
				t.Fatalf("sibling detach result = %+v, %v; want WaitingText", siblingRes.res, siblingRes.err)
			}
		default:
			t.Fatal("sibling detachable card did not detach immediately after clarify succeeded")
		}

		deny := false
		for _, id := range []string{primaryIx.ID, siblingIx.ID} {
			if err := ft.s.resolve(id, proto.Resolution{Allow: &deny}); err != nil {
				t.Fatal(err)
			}
			waitApprovalResult(t, ft, id)
		}
	})
}

// If an owner message arrives during the brief registration window before
// startPreparedTimer runs (decisionGen == 0), startPreparedTimer detaches the
// card immediately once record/announce finish.
func TestOwnerMessageBeforeTimerStartsDetachesOnceRegistered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		ix := detachedTestCard("ix_pre_timer_owner_msg")
		state := &preparedApproval{
			caller:     context.Background(),
			release:    make(chan struct{}),
			deliveries: map[string]clarifyDelivery{},
			op:         approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) { return "ok", nil }},
		}
		p := &pending{ix: ix, done: make(chan proto.Resolution, 1), prepared: state, deadlineChanged: make(chan struct{}, 1)}
		ft.s.record(ix)
		ft.s.fleet.mu.Lock()
		ft.s.ixMapLocked()[ix.ID] = p
		ft.s.fleet.mu.Unlock()

		// Owner speaks before startPreparedTimer has bumped decisionGen.
		ft.s.detachPendingApprovalsOnOwnerMessage("mac", "s1")

		done := make(chan error, 1)
		go func() {
			_, err := ft.s.awaitPrepared(context.Background(), p, false)
			done <- err
		}()
		ft.s.startPreparedTimer(p)
		synctest.Wait()

		select {
		case err := <-done:
			if !errors.Is(err, errApprovalClarifying) {
				t.Fatalf("awaitPrepared err = %v, want errApprovalClarifying", err)
			}
		default:
			t.Fatal("pre-timer owner message did not detach once startPreparedTimer ran")
		}
		deny := false
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)
	})
}

func TestViewActionAndPageTapDetachPendingCommandApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		// 1. View action.
		viewDone := startDetachedTest(t, ft, detachedTestCard("ix_view_action_detach"))
		elapse(10 * time.Millisecond)
		if _, err := ft.s.viewActionParams(context.Background(), db.AssistantViewActionRow{
			MachineID: "mac", Conversation: "s1", MessageID: "view-msg-1", Text: "Run action",
		}); err != nil {
			t.Fatalf("viewActionParams: %v", err)
		}
		synctest.Wait()
		if got := awaitDetachedTest(t, viewDone); got.err != nil || !got.res.Pending || got.res.ApprovalID != "ix_view_action_detach" {
			t.Fatalf("view action detach = %+v, %v", got.res, got.err)
		}
		deny := false
		if err := ft.s.resolve("ix_view_action_detach", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_view_action_detach")

		// 2. Page tap.
		pageDone := startDetachedTest(t, ft, detachedTestCard("ix_page_tap_detach"))
		elapse(10 * time.Millisecond)
		if _, err := ft.s.pageTapParams(context.Background(), "mac", "s1", &db.AssistantPageActionRow{
			MessageID: "page-msg-1", Text: "Tap action",
		}); err != nil {
			t.Fatalf("pageTapParams: %v", err)
		}
		synctest.Wait()
		if got := awaitDetachedTest(t, pageDone); got.err != nil || !got.res.Pending || got.res.ApprovalID != "ix_page_tap_detach" {
			t.Fatalf("page tap detach = %+v, %v", got.res, got.err)
		}
		if err := ft.s.resolve("ix_page_tap_detach", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_page_tap_detach")
	})
}

func TestFeishuOwnerSendDetachesPendingCommandApproval(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)

	feishuDone := startDetachedTest(t, ft, detachedTestCard("ix_feishu_owner_detach"))
	rt := resolvedTarget{
		machine: "mac",
		session: proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true},
	}
	if err := ft.s.startBotRun(context.Background(), rt, botTarget{Kind: targetMain}, "om_owner_1", "飞书发来的新消息", nil, "", "", nil); err != nil {
		t.Fatalf("startBotRun: %v", err)
	}
	select {
	case got := <-feishuDone:
		if got.err != nil || !got.res.Pending || !got.res.Dismiss || got.res.ApprovalID != "ix_feishu_owner_detach" {
			t.Fatalf("feishu detach = %+v, %v", got.res, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Feishu owner message did not detach pending command approval before approvalInitialWait")
	}
	deny := false
	if err := ft.s.resolve("ix_feishu_owner_detach", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_feishu_owner_detach")
}

func TestOwnerMessageDetachFailOpenOnStoreError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detach_fail_open"))
		elapse(10 * time.Millisecond)

		// Synthesize a transient deadline persistence error; SendParams must succeed
		// and the card must NOT be prematurely expired.
		ft.st.deadlineErr = errors.New("transient db error")
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "owner message during db hiccup", "client_msg_id": "hiccup-1"},
		}); err != nil {
			t.Fatalf("SendParams failed on transient detach error: %v", err)
		}
		synctest.Wait()

		select {
		case got := <-done:
			t.Fatalf("card should stay pending (not expired) on transient detach error: %+v", got)
		default:
		}
		if len(ft.s.pendingInteractions()) != 1 {
			t.Fatalf("pending cards = %d, want 1", len(ft.s.pendingInteractions()))
		}

		// Once DB recovers, a new owner message detaches it immediately.
		ft.st.deadlineErr = nil
		if _, err := ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "second owner message", "client_msg_id": "hiccup-2"},
		}); err != nil {
			t.Fatalf("SendParams after recovery: %v", err)
		}
		synctest.Wait()
		first := awaitDetachedTest(t, done)
		if first.err != nil || !first.res.Pending || !first.res.Dismiss {
			t.Fatalf("post-recovery detach = %+v, %v", first.res, first.err)
		}

		// Cleanup.
		deny := false
		if err := ft.s.resolve("ix_detach_fail_open", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_detach_fail_open")
	})
}

func TestOwnerMessageConcurrentWithResolveHasNoRace(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_concurrent_owner_resolve")
	done := startDetachedTest(t, ft, ix)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, _ = ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "msg 1", "client_msg_id": "race-1"},
		})
	}()
	go func() {
		defer wg.Done()
		_, _ = ft.s.SendParams(context.Background(), SendParams{
			MachineID: "mac",
			SessionID: "s1",
			Human:     true,
			Params:    map[string]any{"text": "msg 2", "client_msg_id": "race-2"},
		})
	}()
	go func() {
		defer wg.Done()
		allow := true
		_ = ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow})
	}()
	wg.Wait()

	got := awaitDetachedTest(t, done)
	if got.err != nil {
		t.Fatalf("nodeInteraction err = %v", got.err)
	}
	// Either the immediate Allow won (Allow=true, Pending=false, no detached grant),
	// OR early detach won first and then Allow minted one continuation grant (Allow=nil, Dismiss=true).
	immediateAllow := got.res.Allow != nil && *got.res.Allow
	if immediateAllow {
		ft.s.detached.mu.Lock()
		_, hasGrant := ft.s.detached.grants[ix.ID]
		ft.s.detached.mu.Unlock()
		if hasGrant {
			t.Fatal("immediate Allow also minted a duplicate detached grant")
		}
	} else {
		if !got.res.Dismiss {
			t.Fatalf("non-immediate result = %+v, want Dismiss", got.res)
		}
		if result := waitApprovalResult(t, ft, ix.ID); !strings.Contains(result, "approved") {
			t.Fatalf("detached late Allow result = %q", result)
		}
		retry := detachedTestCard("ix_concurrent_owner_resolve_retry")
		retried, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || retried.Allow == nil || !*retried.Allow || retried.ApprovalID != ix.ID {
			t.Fatalf("exact retry after race = %+v, %v", retried, err)
		}
	}
}
