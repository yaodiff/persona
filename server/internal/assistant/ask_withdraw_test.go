package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func runAskWithdraw(s *Service, machine, self string, args map[string]any) (string, error) {
	raw, _ := json.Marshal(args)
	return s.runTool(context.Background(), machine, self, "ask_user_withdraw", raw)
}

func TestAskUserWithdrawWaitingOwnCardDeliversReasonAndIsIdempotent(t *testing.T) {
	ft := newFleetTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type askResult struct {
		text string
		err  error
	}
	done := make(chan askResult, 1)
	go func() {
		text, err := ft.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(`{"why_owner":"priority","questions":[{"header":"Deploy","question":"Deploy release v2 now?","options":[{"label":"Yes"},{"label":"No"}]}]}`))
		done <- askResult{text: text, err: err}
	}()

	asked := ft.waitPending(1)[0]
	if asked.ToolName != "ask_user" || asked.SessionID != "s1" || asked.MachineID != "mac" {
		t.Fatalf("unexpected pending card: %+v", asked)
	}

	out, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{
		"interaction_id": asked.ID,
		"reason":         "deployment was already cancelled",
	})
	if err != nil {
		t.Fatalf("withdraw own waiting card: %v", err)
	}
	for _, want := range []string{asked.ID, "Deploy release v2 now?", "deployment was already cancelled"} {
		if !strings.Contains(out, want) {
			t.Errorf("withdraw result = %q, want %q", out, want)
		}
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ask_user waiter error: %v", got.err)
		}
		for _, want := range []string{asked.ID, "withdrawn", "deployment was already cancelled", "No answer was supplied"} {
			if !strings.Contains(got.text, want) {
				t.Errorf("waiter result = %q, want %q", got.text, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user waiter did not receive cancellation")
	}

	ft.waitPending(0)
	if status, res, err := ft.st.InteractionOutcome(ctx, asked.ID); err != nil || status != "cancelled" || res == nil || !res.Cancel {
		t.Fatalf("history outcome = %q %+v %v, want cancelled", status, res, err)
	}

	// Repeated withdrawal is idempotent and does not resurrect or duplicate the card.
	again, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{
		"interaction_id": asked.ID,
		"reason":         "second try",
	})
	if err != nil {
		t.Fatalf("repeated withdraw error: %v", err)
	}
	if !strings.Contains(again, asked.ID) || !strings.Contains(again, "already cancelled") {
		t.Fatalf("repeated withdraw = %q, want idempotent already-cancelled notice", again)
	}
	if got := ft.s.pendingInteractions(); len(got) != 0 {
		t.Fatalf("repeated withdraw resurrected card: %+v", got)
	}
	if got := ft.s.pendingQuestions(); len(got) != 0 {
		t.Fatalf("repeated withdraw resurrected question: %+v", got)
	}
	if got := ft.toldOf(assistantwire.EventFleetResolved); len(got) != 1 {
		t.Fatalf("fleet resolved events = %d, want 1", len(got))
	}
}

func TestAskUserWithdrawNoWaitOwnCardCancelsWithoutDuplicateRelay(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)

	text, err := ft.s.toolAskUser(context.Background(), "mac", "s1", json.RawMessage(`{"why_owner":"priority","wait":false,"questions":[{"header":"Merge","question":"Merge the PR now?","options":[{"label":"Merge"},{"label":"Wait"}]}]}`))
	if err != nil {
		t.Fatalf("ask_user wait:false: %v", err)
	}
	asked := ft.waitPending(1)[0]
	if !strings.Contains(text, asked.ID) {
		t.Fatalf("ask_user wait:false result %q missing %s", text, asked.ID)
	}

	out, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{
		"interaction_id": asked.ID,
		"reason":         "PR was merged manually",
	})
	if err != nil {
		t.Fatalf("withdraw own wait:false card: %v", err)
	}
	for _, want := range []string{asked.ID, "Merge the PR now?", "PR was merged manually"} {
		if !strings.Contains(out, want) {
			t.Errorf("withdraw result = %q, want %q", out, want)
		}
	}

	ft.waitPending(0)
	if len(ft.s.pendingQuestions()) != 0 {
		t.Fatal("wait:false question remained pending")
	}
	if ft.s.askLaterSpokeOrKept(asked.ID) {
		t.Fatal("withdrawn wait:false question stayed in assistant.ask_later")
	}
	if status, res, _ := ft.st.InteractionOutcome(context.Background(), asked.ID); status != "cancelled" || res == nil || !res.Cancel {
		t.Fatalf("history outcome = %q %+v, want cancelled", status, res)
	}

	// Self-withdrawal does not trigger an extra relay message back to the same conversation.
	ft.f.mu.Lock()
	sends := len(ft.f.sends)
	ft.f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("self-withdrawal of wait:false card emitted %d unexpected relay messages", sends)
	}

	// Repeating withdrawal stays idempotent and keeps the card absent.
	again, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{"interaction_id": asked.ID})
	if err != nil || !strings.Contains(again, "already cancelled") {
		t.Fatalf("repeated withdraw = %q, %v", again, err)
	}
	if len(ft.s.pendingInteractions()) != 0 || len(ft.s.pendingQuestions()) != 0 {
		t.Fatal("repeated withdraw resurrected wait:false card")
	}
}

func TestAskUserWithdrawRejectsCrossOriginAndUserOwnedCards(t *testing.T) {
	ft := newFleetTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Another conversation's ask_user card (different session and different machine).
	go func() {
		_, _ = ft.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(`{"why_owner":"priority","questions":[{"question":"Keep going?","options":[{"label":"Yes"},{"label":"No"}]}]}`))
	}()
	ownAsk := ft.waitPending(1)[0]

	if _, err := runAskWithdraw(ft.s, "mac", "s2", map[string]any{"interaction_id": ownAsk.ID}); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("different session withdrawal = %v, want cross-origin error", err)
	}
	if _, err := runAskWithdraw(ft.s, "other-mac", "s1", map[string]any{"interaction_id": ownAsk.ID}); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("different machine withdrawal = %v, want cross-origin error", err)
	}
	if _, ok := ft.s.pendingIx(ownAsk.ID); !ok {
		t.Fatal("rejected cross-origin withdrawal ended the pending card")
	}

	// 2. User-owned approval, plan, screen, elicitation, and another agent's AskUserQuestion.
	otherCards := []proto.Interaction{
		{ID: "ix_perm_own", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, ToolName: "persona_settings", Summary: "Change setting", ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ix_plan_own", MachineID: "mac", SessionID: "s1", Kind: proto.IxPlan, ToolName: "ExitPlanMode", Plan: "Step 1", ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ix_screen_own", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ix_elicit_own", MachineID: "mac", SessionID: "s1", Kind: proto.IxElicitation, Elicitation: &proto.Elicitation{Mode: "form", Message: "Confirm"}, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ix_agent_q", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion, ToolName: "AskUserQuestion", CanCancel: true, Questions: []proto.Question{{Question: "Agent question?"}}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	for _, ix := range otherCards {
		ft.s.fleet.mu.Lock()
		ft.s.ixMapLocked()[ix.ID] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
		ft.s.fleet.mu.Unlock()
		if _, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{"interaction_id": ix.ID, "reason": "obsolete"}); err == nil {
			t.Errorf("withdrawal of %s (%s/%s) succeeded, want rejection", ix.ID, ix.Kind, ix.ToolName)
		}
		if _, ok := ft.s.pendingIx(ix.ID); !ok {
			t.Errorf("rejected withdrawal removed %s", ix.ID)
		}
	}

	// 3. Even after s1 withdraws ownAsk, another conversation still gets cross-origin rejection.
	if _, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{"interaction_id": ownAsk.ID}); err != nil {
		t.Fatalf("origin withdraw: %v", err)
	}
	if _, err := runAskWithdraw(ft.s, "mac", "s2", map[string]any{"interaction_id": ownAsk.ID}); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("cross-origin withdraw after terminal = %v, want rejection", err)
	}
}

func TestAskUserWithdrawAlreadyAnsweredIsIdempotent(t *testing.T) {
	ft := newFleetTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		text, _ := ft.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(`{"why_owner":"priority","questions":[{"question":"Ship today?","options":[{"label":"Yes"},{"label":"Later"}]}]}`))
		done <- text
	}()
	asked := ft.waitPending(1)[0]
	if err := ft.s.resolve(asked.ID, proto.Resolution{Answers: map[string]string{"Ship today?": "Yes"}}); err != nil {
		t.Fatalf("answer card: %v", err)
	}
	if got := <-done; !strings.Contains(got, "Ship today? → Yes") {
		t.Fatalf("waiter got %q, want user answer", got)
	}

	out, err := runAskWithdraw(ft.s, "mac", "s1", map[string]any{"interaction_id": asked.ID})
	if err != nil {
		t.Fatalf("withdraw already-answered card: %v", err)
	}
	if !strings.Contains(out, asked.ID) || !strings.Contains(out, "already answered") {
		t.Fatalf("already-answered withdraw = %q", out)
	}
	if status, _, _ := ft.st.InteractionOutcome(ctx, asked.ID); status != "resolved" {
		t.Fatalf("history status mutated to %q, want resolved", status)
	}
}

func TestRestartInvalidationDeliversSafeContextForAskUserAndApproval(t *testing.T) {
	t.Run("non-resumable ask_user delivers question summary on restart", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		go func() {
			_, _ = ft.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(`{"why_owner":"priority","questions":[{"header":"Rollout","question":"Roll out the new billing flag?","options":[{"label":"Roll out"},{"label":"Hold"}]}]}`))
		}()
		asked := ft.waitPending(1)[0]

		st := &restoreMem{fleetMem: ft.st}
		f2 := newFakeHub()
		d2 := newFleetDevs()
		d2.add("mac", "Mac")
		sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
		f2.setSessions("mac", sess)
		d2.setSessions("mac", []proto.Session{sess})
		f2.mu.Lock()
		f2.known["s1"] = true
		f2.mu.Unlock()

		s2 := newRestartedService(t, Options{Hub: f2, Store: func() Store { return st }, Logf: testLogf(t), Devices: d2})
		s2.FleetUp(context.Background())

		var notice string
		waitFor(t, "restart invalidation notice for ask_user", func() bool {
			f2.mu.Lock()
			defer f2.mu.Unlock()
			var found []string
			for _, p := range f2.sends {
				text, _ := p["text"].(string)
				if strings.Contains(text, asked.ID) {
					found = append(found, text)
				}
			}
			if len(found) > 1 {
				t.Fatalf("delivered %d notices for %s: %q", len(found), asked.ID, found)
			}
			if len(found) == 1 {
				notice = found[0]
				return true
			}
			return false
		})
		for _, want := range []string{asked.ID, "Roll out the new billing flag?", "Nothing was applied", "fresh ask_user card"} {
			if !strings.Contains(notice, want) {
				t.Errorf("invalidation notice = %q, want %q", notice, want)
			}
		}
		// Must not echo raw JSON tool input arguments.
		if strings.Contains(notice, `"options"`) || strings.Contains(notice, `"header"`) {
			t.Fatalf("invalidation notice leaked raw tool input: %q", notice)
		}
		// Running recoverApprovalJournals afterward must not emit a duplicate notice.
		s2.recoverApprovalJournals(context.Background())
		f2.mu.Lock()
		count := 0
		for _, p := range f2.sends {
			if text, _ := p["text"].(string); strings.Contains(text, asked.ID) {
				count++
			}
		}
		f2.mu.Unlock()
		if count != 1 {
			t.Fatalf("notices after recoverApprovalJournals = %d, want 1", count)
		}
	})

	t.Run("resumable ask_user wait:false keeps restart behavior without invalidation notice", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		_, err := ft.s.toolAskUser(context.Background(), "mac", "s1", json.RawMessage(`{"why_owner":"priority","wait":false,"questions":[{"question":"Keep pending across restart?","options":[{"label":"Yes"},{"label":"No"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		asked := ft.waitPending(1)[0]

		st := &restoreMem{fleetMem: ft.st}
		f2 := newFakeHub()
		d2 := newFleetDevs()
		d2.add("mac", "Mac")
		sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
		f2.setSessions("mac", sess)
		d2.setSessions("mac", []proto.Session{sess})
		f2.mu.Lock()
		f2.known["s1"] = true
		f2.mu.Unlock()

		s2 := newRestartedService(t, Options{Hub: f2, Store: func() Store { return st }, Logf: testLogf(t), Devices: d2})
		s2.FleetUp(context.Background())
		s2.recoverApprovalJournals(context.Background())

		if status, _, _ := st.InteractionOutcome(context.Background(), asked.ID); status != "pending" {
			t.Fatalf("resumable ask_user status = %q, want pending", status)
		}
		f2.mu.Lock()
		sends := len(f2.sends)
		f2.mu.Unlock()
		if sends != 0 {
			t.Fatalf("resumable ask_user received %d invalidation notices, want 0", sends)
		}
	})

	t.Run("clarified approval restart retains summary without secrets or raw tool input", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)

		ix := proto.Interaction{
			ID:        "ix_restart_summary",
			MachineID: "mac",
			SessionID: "s1",
			Kind:      proto.IxPermission,
			ToolName:  "persona_settings",
			Summary:   "Change assistant.load_all_tools from off to on",
			ToolInput: json.RawMessage(`{"key":"assistant.load_all_tools","secret_token":"RAW_ARG_SHOULD_NOT_ECHO"}`),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		done := startPreparedInteraction(t, ft, ix, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) { return "applied", nil }})
		if err := ft.s.clarify(context.Background(), ix.ID, "Why?", "owner-restart-summary", nil); err != nil {
			t.Fatal(err)
		}
		awaitPreparedTest(t, done)

		// Restart into a fresh manager sharing the same store and verify exactly one contextual notice.
		f2 := newFakeHub()
		d2 := newFleetDevs()
		d2.add("mac", "Mac")
		sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
		f2.setSessions("mac", sess)
		d2.setSessions("mac", []proto.Session{sess})
		f2.mu.Lock()
		f2.known["s1"] = true
		f2.mu.Unlock()

		restarted := newRestartedService(t, Options{Hub: f2, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: d2})
		restarted.recoverApprovalJournals(context.Background())

		result := waitFakeApprovalResult(t, f2, ix.ID)
		for _, want := range []string{ix.ID, "Change assistant.load_all_tools from off to on", "nothing was applied", "fresh approval"} {
			if !strings.Contains(result, want) {
				t.Errorf("restart result = %q, want %q", result, want)
			}
		}
		if strings.Contains(result, "RAW_ARG_SHOULD_NOT_ECHO") || strings.Contains(result, "secret_token") {
			t.Fatalf("restart result leaked raw tool input: %q", result)
		}
	})

	t.Run("upgrade from legacy journal without Summary backfills from interaction record", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := proto.Interaction{
			ID:        "ix_upgrade_legacy",
			MachineID: "mac",
			SessionID: "s1",
			Kind:      proto.IxPermission,
			ToolName:  "persona_settings",
			Summary:   "Enable quiet evening briefing",
			ToolInput: json.RawMessage(`{"raw_arg":"MUST_NOT_LEAK"}`),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		ft.s.record(ix)
		// Simulate an older binary's journal entry that had no Summary field.
		putApprovalJournal(t, ft, approvalJournalEntry{
			ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, UpdatedAt: time.Now().UTC(),
		})

		ft.s.recoverApprovalJournals(context.Background())
		result := waitApprovalResult(t, ft, ix.ID)
		for _, want := range []string{ix.ID, "Enable quiet evening briefing", "nothing was applied", "fresh approval"} {
			if !strings.Contains(result, want) {
				t.Errorf("upgraded restart result = %q, want %q", result, want)
			}
		}
		if strings.Contains(result, "MUST_NOT_LEAK") {
			t.Fatalf("upgraded restart result leaked raw tool input: %q", result)
		}
	})

	t.Run("secret cards never echo secret prompt or password-field summaries", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ix   proto.Interaction
			deny string
		}{
			{
				name: "secret flag",
				ix: proto.Interaction{
					ID: "ix_sec_1", Kind: proto.IxScreen, Secret: true, ToolName: "secret_prompt",
					Summary: "Enter root password SUPER_SECRET_PROMPT",
				},
				deny: "SUPER_SECRET_PROMPT",
			},
			{
				name: "secret field",
				ix: proto.Interaction{
					ID: "ix_sec_2", Kind: proto.IxPermission, ToolName: proto.SecretRunTool, ToolTitle: "Password command",
					Summary: "Run sudo -S sh -c 'echo SECRET_CMD_ARG'",
					Fields:  []proto.Field{{Key: "password", Label: "Password", Secret: true}},
				},
				deny: "SECRET_CMD_ARG",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := safeInteractionSummary(tc.ix)
				if strings.Contains(got, tc.deny) {
					t.Fatalf("safeInteractionSummary(%s) = %q leaked %q", tc.name, got, tc.deny)
				}
				if got == "" {
					t.Fatalf("safeInteractionSummary(%s) returned empty label", tc.name)
				}
			})
		}
	})

	t.Run("coding agent non-resumable cards are not notified as assistant conversations", func(t *testing.T) {
		for _, ix := range []proto.Interaction{
			{ID: "ix_agent_perm", MachineID: "mac", SessionID: "agent1", Kind: proto.IxPermission, ToolName: "Bash", Summary: "rm -rf /tmp/x"},
			{ID: "ix_agent_ask", MachineID: "mac", SessionID: "agent1", Kind: proto.IxQuestion, ToolName: "AskUserQuestion", Questions: []proto.Question{{Question: "Continue?"}}},
			{ID: "ix_oneshot", MachineID: "mac", SessionID: "", Kind: proto.IxPermission, ToolName: "persona_settings", CanClarify: true},
		} {
			if shouldNotifyRestartInvalidated(ix) {
				t.Errorf("shouldNotifyRestartInvalidated(%s) = true, want false", ix.ID)
			}
		}
		if !slices.Contains(toolNames, "ask_user_withdraw") {
			t.Fatal("ask_user_withdraw missing from toolNames")
		}

		// Verify the public MCP Tool() entry point routes ask_user_withdraw and preserves errors.
		ft := newFleetTest(t)
		res := ft.s.Tool(context.Background(), ToolCall{
			Name:      "ask_user_withdraw",
			Arguments: json.RawMessage(`{"interaction_id":"ix_missing"}`),
			MachineID: "mac",
			SessionID: "s1",
		})
		if !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "no pending ask_user question ix_missing") {
			t.Fatalf("Tool(ask_user_withdraw missing) = %+v", res)
		}
	})

	t.Run("restoreInteractions before recoverApprovalJournals does not overwrite applying or duplicate clarified", func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		applyingIx := proto.Interaction{
			ID: "ix_order_applying", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Update morning routine time", CanClarify: true,
			ExpiresAt: time.Now().Add(time.Hour),
		}
		preClarifyIx := proto.Interaction{
			ID: "ix_order_preclarify", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Switch default launcher", CanClarify: true,
			ExpiresAt: time.Now().Add(time.Hour),
		}
		ft.s.record(applyingIx)
		ft.s.record(preClarifyIx)
		if err := ft.s.approvalJournalStarted(context.Background(), applyingIx); err != nil {
			t.Fatal(err)
		}
		if err := ft.s.approvalJournalApplying(context.Background(), applyingIx); err != nil {
			t.Fatal(err)
		}

		st := &restoreMem{fleetMem: ft.st}
		f2 := newFakeHub()
		d2 := newFleetDevs()
		d2.add("mac", "Mac")
		sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
		f2.setSessions("mac", sess)
		d2.setSessions("mac", []proto.Session{sess})
		f2.mu.Lock()
		f2.known["s1"] = true
		f2.mu.Unlock()

		s2 := newRestartedService(t, Options{Hub: f2, Store: func() Store { return st }, Logf: testLogf(t), Devices: d2})
		s2.FleetUp(context.Background())
		s2.recoverApprovalJournals(context.Background())

		applyingResult := waitFakeApprovalResult(t, f2, applyingIx.ID)
		for _, want := range []string{applyingIx.ID, "Update morning routine time", "uncertain"} {
			if !strings.Contains(applyingResult, want) {
				t.Errorf("applying recovery result = %q, want %q", applyingResult, want)
			}
		}
		if strings.Contains(applyingResult, "cancelled without executing") {
			t.Fatalf("restoreInteractions overwrote applying entry as cancelled: %q", applyingResult)
		}

		preResult := waitFakeApprovalResult(t, f2, preClarifyIx.ID)
		for _, want := range []string{preClarifyIx.ID, "Switch default launcher", "nothing was applied", "fresh approval"} {
			if !strings.Contains(preResult, want) {
				t.Errorf("pre-clarify restart result = %q, want %q", preResult, want)
			}
		}
	})
}
