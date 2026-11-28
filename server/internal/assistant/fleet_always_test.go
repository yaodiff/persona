package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// These fixtures exercise the owner's standing authorization and revocation.
func alwaysIx(id, session string, keys ...string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: session, Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
		ExpiresAt: time.Now().Add(time.Hour), Resumable: true, AlwaysKeys: keys}
}

// ask is a node's interaction.create, answered with r once its card shows; got is what the node hears.
func ask(t *testing.T, s *Service, ix proto.Interaction, r *proto.Resolution) (got proto.Resolution) {
	t.Helper()
	out := make(chan proto.Resolution, 1)
	go func() { res, _ := s.nodeInteraction(context.Background(), ix); out <- res }()
	if r != nil {
		deadline := time.Now().Add(5 * time.Second)
		for s.resolve(ix.ID, *r) != nil {
			if time.Now().After(deadline) {
				t.Fatalf("%s never showed", ix.ID)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	select {
	case got = <-out:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no answer", ix.ID)
	}
	return got
}

// "Always Allow in Session" on an assistant conversation's card outlasts the agent's process: the manager answers the same card of
// the resumed process itself (#2202), and only that conversation's, only for what it granted.
func TestAssistantAlwaysAllowKept(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	yes, no := true, false
	ask(t, s, alwaysIx("ix_1", "conv", "tool:Bash"), &proto.Resolution{Allow: &yes})
	ft.waitPending(0)
	if _, ok := s.alwaysAllowed(context.Background(), alwaysIx("ix_x", "conv", "tool:Bash")); ok {
		t.Fatal("allowing once was kept as always")
	}
	ask(t, s, alwaysIx("ix_2", "agent"), &proto.Resolution{Allow: &yes, Always: true})
	if _, ok := s.alwaysAllowed(context.Background(), alwaysIx("ix_x", "agent", "tool:Bash")); ok {
		t.Fatal("a card without keys kept a grant")
	}
	ask(t, s, alwaysIx("ix_3", "conv", "tool:Edit"), &proto.Resolution{Allow: &no, Always: true})
	if first := ask(t, s, alwaysIx("ix_4", "conv", "tool:Bash"), &proto.Resolution{Allow: &yes, Always: true}); first.Remembered {
		t.Fatalf("the card's own answer was taken for a kept grant: %+v", first)
	}
	ft.waitPending(0)
	got := ask(t, s, alwaysIx("ix_5", "conv", "tool:Bash"), nil)
	if got.Allow == nil || !*got.Allow || !got.Always || !got.Remembered { // remembered: no card was answered for it
		t.Fatalf("kept grant answered %+v", got)
	}
	if n := len(s.pendingInteractions()); n != 0 {
		t.Fatalf("%d cards shown for a granted tool", n)
	}
	for _, ix := range []proto.Interaction{
		alwaysIx("ix_6", "conv", "tool:Edit"),
		alwaysIx("ix_7", "other", "tool:Bash"),
		alwaysIx("ix_8", "conv", "tool:Bash", "lark:POST /im/v1/messages"),
	} {
		if _, ok := s.alwaysAllowed(context.Background(), ix); ok {
			t.Fatalf("%s allowed without a grant", ix.ID)
		}
	}
}

// A card of the same conversation waiting when another one is answered with "Always Allow in Session" is answered with it.
func TestAssistantAlwaysAnswersWaitingCards(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	waiting := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(context.Background(), alwaysIx("ix_b", "conv", "tool:Bash"))
		waiting <- r
	}()
	go func() { _, _ = s.nodeInteraction(context.Background(), alwaysIx("ix_c", "conv", "tool:Edit")) }()
	ft.waitPending(2)
	yes := true
	ask(t, s, alwaysIx("ix_a", "conv", "tool:Bash"), &proto.Resolution{Allow: &yes, Always: true})
	select {
	case r := <-waiting:
		if r.Allow == nil || !*r.Allow || !r.Remembered {
			t.Fatalf("waiting card got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting card for the same tool still waits")
	}
	if p := ft.waitPending(1); p[0].ID != "ix_c" {
		t.Fatalf("pending %+v", p)
	}
}

func TestInboxWholeBashAlwaysNeverHonoredOrKept(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx := context.Background()
	s.originSet(ctx, "mac", "inbox-conv", OriginInbox, "item-1", "")

	// Case 1: an older node still sending whole-tool ["tool:Bash"] on an Inbox conversation must have
	// AlwaysKeys stripped and AlwaysHonored set to false; even if an older client answers Always:true,
	// honest() clears it and no session-wide Bash grant is kept.
	yes := true
	firstDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_1", "inbox-conv", "tool:Bash"))
		firstDone <- r
	}()
	pending := ft.waitPending(1)
	if pending[0].AlwaysHonored == nil || *pending[0].AlwaysHonored {
		t.Fatalf("inbox whole-tool Bash card honored=%v, want false", pending[0].AlwaysHonored)
	}
	if len(pending[0].AlwaysKeys) != 0 {
		t.Fatalf("inbox whole-tool Bash card retained AlwaysKeys=%q, want none", pending[0].AlwaysKeys)
	}
	if err := s.resolve("ix_inbox_1", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	res := <-firstDone
	if res.Always {
		t.Fatalf("honest() did not clear Always on inbox whole-tool Bash: %+v", res)
	}
	ft.waitPending(0)

	// A follow-up Bash call (e.g. destructive command) must still ask instead of being auto-allowed.
	secondDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_2", "inbox-conv", "tool:Bash"))
		secondDone <- r
	}()
	ft.waitPending(1)
	if err := s.resolve("ix_inbox_2", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := <-secondDone; r.Remembered {
		t.Fatalf("second inbox Bash call was auto-allowed: %+v", r)
	}

	// Case 2: even a stale pre-existing tool:Bash grant in the store must never match for OriginInbox.
	if err := s.fleetStore().AllowAlways(ctx, "inbox-conv", "Bash", []string{"tool:Bash"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.alwaysAllowed(ctx, alwaysIx("ix_inbox_stale", "inbox-conv", "tool:Bash")); ok {
		t.Fatal("stale tool:Bash grant matched on OriginInbox conversation")
	}
	staleDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_stale_card", "inbox-conv", "tool:Bash"))
		staleDone <- r
	}()
	ft.waitPending(1)
	if err := s.resolve("ix_inbox_stale_card", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := <-staleDone; r.Remembered {
		t.Fatalf("stale tool:Bash grant bypassed card on OriginInbox: %+v", r)
	}

	// Case 3: a new node omitting AlwaysKeys on an Inbox Bash card must still receive AlwaysHonored=false.
	newDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_new", "inbox-conv"))
		newDone <- r
	}()
	pendingNew := ft.waitPending(1)
	if pendingNew[0].AlwaysHonored == nil || *pendingNew[0].AlwaysHonored {
		t.Fatalf("new-node inbox Bash card honored=%v, want false", pendingNew[0].AlwaysHonored)
	}
	if err := s.resolve("ix_inbox_new", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	<-newDone

	// Case 4: narrow exact-command (Codex) and directory-scoped file keys on OriginInbox remain honored.
	narrowIx := alwaysIx("ix_inbox_codex", "inbox-conv", "codex-command:ls /tmp")
	narrowDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, narrowIx)
		narrowDone <- r
	}()
	pendingNarrow := ft.waitPending(1)
	if pendingNarrow[0].AlwaysHonored == nil || !*pendingNarrow[0].AlwaysHonored {
		t.Fatalf("narrow codex-command key on inbox honored=%v, want true", pendingNarrow[0].AlwaysHonored)
	}
	if err := s.resolve("ix_inbox_codex", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	<-narrowDone
	if got := ask(t, s, alwaysIx("ix_inbox_codex_2", "inbox-conv", "codex-command:ls /tmp"), nil); !got.Remembered {
		t.Fatalf("narrow codex-command grant was not reused: %+v", got)
	}
	if _, ok := s.alwaysAllowed(ctx, alwaysIx("ix_inbox_codex_other", "inbox-conv", "codex-command:rm -rf /tmp/x")); ok {
		t.Fatal("different codex command unexpectedly matched narrow grant")
	}

	// Case 5: ordinary (non-Inbox) assistant sessions retain whole-tool Bash behavior unchanged.
	ask(t, s, alwaysIx("ix_ord_1", "ordinary-conv", "tool:Bash"), &proto.Resolution{Allow: &yes, Always: true})
	if got := ask(t, s, alwaysIx("ix_ord_2", "ordinary-conv", "tool:Bash"), nil); !got.Remembered {
		t.Fatalf("ordinary assistant tool:Bash grant was not reused: %+v", got)
	}

	// Case 6: mixed AlwaysKeys containing tool:Bash on OriginInbox fail closed (stripped, never honored/kept).
	mixedDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_mixed", "inbox-conv", "tool:Bash", "lark:GET /foo"))
		mixedDone <- r
	}()
	pendingMixed := ft.waitPending(1)
	if pendingMixed[0].AlwaysHonored == nil || *pendingMixed[0].AlwaysHonored || len(pendingMixed[0].AlwaysKeys) != 0 {
		t.Fatalf("mixed tool:Bash inbox card honored=%v keys=%q, want false/none", pendingMixed[0].AlwaysHonored, pendingMixed[0].AlwaysKeys)
	}
	if err := s.resolve("ix_inbox_mixed", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-mixedDone; r.Always {
		t.Fatalf("mixed tool:Bash inbox card kept Always=true: %+v", r)
	}
	if _, ok := s.alwaysAllowed(ctx, alwaysIx("ix_inbox_mixed_check", "inbox-conv", "lark:GET /foo")); ok {
		t.Fatal("mixed tool:Bash inbox card unexpectedly stored sibling key")
	}

	// Case 7: approving a non-Bash key with Always on Inbox must NOT wake a waiting card carrying tool:Bash
	// even if a stale tool:Bash row exists in the store.
	if err := s.fleetStore().AllowAlways(ctx, "inbox-conv", "Bash", []string{"tool:Bash"}); err != nil {
		t.Fatal(err)
	}
	waitBash := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, alwaysIx("ix_inbox_wait_bash", "inbox-conv", "tool:Bash"))
		waitBash <- r
	}()
	ft.waitPending(1)
	editIx := alwaysIx("ix_inbox_edit_wake", "inbox-conv", "tool:Edit(//tmp/**)")
	editIx.ToolName = "Edit"
	ask(t, s, editIx, &proto.Resolution{Allow: &yes, Always: true})
	select {
	case r := <-waitBash:
		t.Fatalf("approving Edit woke waiting inbox Bash card: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.resolve("ix_inbox_wait_bash", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	<-waitBash
	ft.waitPending(0)
}

func TestInboxWholeBashHardeningEdgeCases(t *testing.T) {
	ctx := context.Background()
	yes := true

	t.Run("first-turn interaction.create origin hint before originSet and before inAssistant", func(t *testing.T) {
		ft := newFleetTest(t)
		s := ft.s
		// Do NOT call originSet and do NOT populate machine Home / sessions: simulates first-turn race
		// where ixInAssistant is false and s.origins has not recorded the new Inbox session yet.
		for _, tc := range []struct {
			name string
			ix   proto.Interaction
		}{
			{"old node with tool:Bash", alwaysIx("ix_race_old", "race-inbox-1", "tool:Bash")},
			{"new node omitting AlwaysKeys", alwaysIx("ix_race_new", "race-inbox-2")},
		} {
			params, _ := json.Marshal(map[string]any{"interaction": tc.ix, "origin": OriginInbox})
			done := make(chan proto.Resolution, 1)
			go func() {
				out, handled, err := s.NodeRequest(ctx, "mac", "interaction.create", params)
				if err != nil || !handled {
					t.Errorf("%s: NodeRequest handled=%v err=%v", tc.name, handled, err)
					done <- proto.Resolution{}
					return
				}
				done <- out.(proto.Resolution)
			}()
			pending := ft.waitPending(1)
			if pending[0].AlwaysHonored == nil || *pending[0].AlwaysHonored {
				t.Fatalf("%s: AlwaysHonored=%v, want false", tc.name, pending[0].AlwaysHonored)
			}
			if len(pending[0].AlwaysKeys) != 0 {
				t.Fatalf("%s: AlwaysKeys=%q, want none", tc.name, pending[0].AlwaysKeys)
			}
			if err := s.resolve(tc.ix.ID, proto.Resolution{Allow: &yes, Always: true}); err != nil {
				t.Fatal(err)
			}
			if r := <-done; r.Always {
				t.Fatalf("%s: honest() left Always=true: %+v", tc.name, r)
			}
			ft.waitPending(0)
		}
	})

	t.Run("origins store error fails closed on tool:Bash without breaking non-assistant Bash", func(t *testing.T) {
		ft := newFleetTest(t)
		s := ft.s
		if err := s.fleetStore().AllowAlways(ctx, "inbox-err", "Bash", []string{"tool:Bash"}); err != nil {
			t.Fatal(err)
		}
		// Force unloaded origins + read error for originsKey (simulates transient DB failure on manager restart).
		ft.st.failKey(originsKey, errors.New("origins db down"))
		s.origins.mu.Lock()
		s.origins.loaded = false
		s.origins.mu.Unlock()

		if _, ok := s.alwaysAllowed(ctx, alwaysIx("ix_err_stale", "inbox-err", "tool:Bash")); ok {
			t.Fatal("stale tool:Bash grant matched when origins store failed")
		}
		done := make(chan proto.Resolution, 1)
		go func() {
			r, _ := s.nodeInteraction(ctx, alwaysIx("ix_err_card", "inbox-err", "tool:Bash"))
			done <- r
		}()
		pending := ft.waitPending(1)
		if pending[0].AlwaysHonored == nil || *pending[0].AlwaysHonored || len(pending[0].AlwaysKeys) != 0 {
			t.Fatalf("origins error card honored=%v keys=%q, want false/none", pending[0].AlwaysHonored, pending[0].AlwaysKeys)
		}
		if err := s.resolve("ix_err_card", proto.Resolution{Allow: &yes, Always: true}); err != nil {
			t.Fatal(err)
		}
		<-done
		ft.waitPending(0)

		// Terminal Agent (non-assistant, empty AlwaysKeys) must still get AlwaysHonored=nil on origins error.
		agentDone := make(chan proto.Resolution, 1)
		go func() {
			r, _ := s.nodeInteraction(ctx, alwaysIx("ix_err_agent", "terminal-agent"))
			agentDone <- r
		}()
		pendingAgent := ft.waitPending(1)
		if pendingAgent[0].AlwaysHonored != nil {
			t.Fatalf("terminal agent Bash card got AlwaysHonored=%v on origins error, want nil", *pendingAgent[0].AlwaysHonored)
		}
		if err := s.resolve("ix_err_agent", proto.Resolution{Allow: &yes}); err != nil {
			t.Fatal(err)
		}
		<-agentDone
		ft.waitPending(0)
	})

	t.Run("restoreInteractions and resumable reconnect sanitize pre-upgrade inbox tool:Bash cards", func(t *testing.T) {
		f := newFakeHub()
		d := newFleetDevs()
		d.add("mac", "Mac")
		base := newFleetMem()
		st := &restoreMem{base}
		s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
		ft := &fleetTest{t: t, s: s, d: d, f: f, st: base}
		s.originSet(ctx, "mac", "inbox-upgrade", OriginInbox, "item-up", "")

		// Seed a pre-upgrade pending resumable Inbox Bash card with AlwaysKeys=["tool:Bash"] and AlwaysHonored=true.
		oldHonored := true
		preUpgrade := alwaysIx("ix_pre_upgrade", "inbox-upgrade", "tool:Bash")
		preUpgrade.AlwaysHonored = &oldHonored
		_ = st.InsertInteraction(ctx, preUpgrade)

		s.restoreInteractions(ctx)
		pending := ft.waitPending(1)
		if pending[0].AlwaysHonored == nil || *pending[0].AlwaysHonored {
			t.Fatalf("restored pre-upgrade inbox Bash card honored=%v, want false", pending[0].AlwaysHonored)
		}
		if len(pending[0].AlwaysKeys) != 0 {
			t.Fatalf("restored pre-upgrade inbox Bash card retained AlwaysKeys=%q, want none", pending[0].AlwaysKeys)
		}
		if err := s.resolve("ix_pre_upgrade", proto.Resolution{Allow: &yes, Always: true}); err != nil {
			t.Fatal(err)
		}
		ft.waitPending(0)
		if ok, _ := st.AlwaysAllowed(ctx, "inbox-upgrade", []string{"tool:Bash"}); ok {
			t.Fatal("resolving restored pre-upgrade inbox Bash card with Always=true persisted tool:Bash")
		}

		// Also verify resumable reconnect in waitInteractionReady sanitizes a live card created before originSet.
		firstDone := make(chan proto.Resolution, 1)
		go func() {
			r, _ := s.nodeInteraction(ctx, alwaysIx("ix_reconnect_upgrade", "inbox-reconnect", "tool:Bash"))
			firstDone <- r
		}()
		before := ft.waitPending(1)
		if before[0].AlwaysHonored == nil || !*before[0].AlwaysHonored {
			t.Fatalf("setup expected pre-originSet card AlwaysHonored=true, got %v", before[0].AlwaysHonored)
		}
		s.originSet(ctx, "mac", "inbox-reconnect", OriginInbox, "item-rc", "")
		reconnectDone := make(chan proto.Resolution, 1)
		go func() {
			r, _ := s.nodeInteraction(ctx, alwaysIx("ix_reconnect_upgrade", "inbox-reconnect", "tool:Bash"))
			reconnectDone <- r
		}()
		deadline := time.Now().Add(2 * time.Second)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			cur := s.pendingInteractions()
			if len(cur) == 1 && cur[0].AlwaysHonored != nil && !*cur[0].AlwaysHonored && len(cur[0].AlwaysKeys) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("resumable reconnect did not sanitize pending card: %+v", cur)
			}
			<-tick.C
		}
		if err := s.resolve("ix_reconnect_upgrade", proto.Resolution{Allow: &yes, Always: true}); err != nil {
			t.Fatal(err)
		}
		if r := <-reconnectDone; r.Always {
			t.Fatalf("reconnected inbox Bash card left Always=true: %+v", r)
		}
		<-firstDone
	})
}

func TestAGYRuntimeAlwaysAllowLifecycleAndRevocation(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx := context.Background()
	yes := true

	const agySid = "antigravity-0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f"
	m, _ := s.fleetMachine("mac")
	ft.sessions("mac", proto.Session{
		SessionID: agySid, Agent: "antigravity", AgentLabel: "Antigravity",
		Cwd: m.assistantCwd(), Status: proto.StatusWorking,
	})

	agyCard := func(id, tool string, keys ...string) proto.Interaction {
		return proto.Interaction{
			ID: id, MachineID: "mac", SessionID: agySid, Kind: proto.IxPermission, OutwardWrite: true,
			Agent: "antigravity", AgentLabel: "Antigravity", Cwd: m.assistantCwd(),
			ToolName: tool, ExpiresAt: time.Now().Add(time.Hour), Resumable: true, AlwaysKeys: keys,
		}
	}

	// 1. AGY Assistant Write card gets AlwaysHonored=true and stores directory-scoped key on Always=true.
	firstDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, agyCard("ix_agy_w1", "Write", "tool:Edit(//tmp/work/**)"))
		firstDone <- r
	}()
	firstPending := ft.waitPending(1)
	if firstPending[0].AlwaysHonored == nil || !*firstPending[0].AlwaysHonored {
		t.Fatalf("AGY Write card AlwaysHonored = %v, want true", firstPending[0].AlwaysHonored)
	}
	if err := s.resolve("ix_agy_w1", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-firstDone; r.Allow == nil || !*r.Allow || !r.Always || r.Remembered {
		t.Fatalf("first AGY Write resolution = %+v, want allow+always (not remembered)", r)
	}
	ft.waitPending(0)

	// 2. Second Write in the same directory is immediately auto-allowed with Remembered=true and no card.
	second := ask(t, s, agyCard("ix_agy_w2", "Write", "tool:Edit(//tmp/work/**)"), nil)
	if second.Allow == nil || !*second.Allow || !second.Always || !second.Remembered {
		t.Fatalf("second AGY Write resolution = %+v, want remembered allow", second)
	}
	if n := len(s.pendingInteractions()); n != 0 {
		t.Fatalf("remembered AGY Write left %d pending cards", n)
	}

	// 3. Write in a different directory is NOT covered and raises a fresh card.
	otherDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, agyCard("ix_agy_w_other", "Write", "tool:Edit(//tmp/other/**)"))
		otherDone <- r
	}()
	ft.waitPending(1)
	if err := s.resolve("ix_agy_w_other", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := <-otherDone; r.Remembered {
		t.Fatalf("different directory Write was unexpectedly remembered: %+v", r)
	}
	ft.waitPending(0)

	// 4. Revoking the grant immediately forces a fresh card on the next matching Write.
	if err := (&grantTestStore{fleetMem: ft.st}).RevokeAlways(ctx, agySid, grantHash("tool:Edit(//tmp/work/**)")); err != nil {
		t.Fatal(err)
	}
	afterRevokeDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, agyCard("ix_agy_w3", "Write", "tool:Edit(//tmp/work/**)"))
		afterRevokeDone <- r
	}()
	ft.waitPending(1)
	if err := s.resolve("ix_agy_w3", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := <-afterRevokeDone; r.Remembered {
		t.Fatalf("revoked AGY Write grant was still auto-allowed: %+v", r)
	}
	ft.waitPending(0)

	// 5. Defense-in-depth: Inbox runtime whole-tool Bash keys (runtime:antigravity:tool:Bash) are stripped and never match.
	const inboxSid = "antigravity-inbox-99"
	s.originSet(ctx, "mac", inboxSid, OriginInbox, "item-agy", "")
	if err := s.fleetStore().AllowAlways(ctx, inboxSid, "Bash", []string{"runtime:antigravity:tool:Bash"}); err != nil {
		t.Fatal(err)
	}
	inboxCard := proto.Interaction{
		ID: "ix_agy_inbox_rt", MachineID: "mac", SessionID: inboxSid, Kind: proto.IxPermission, OutwardWrite: true,
		Agent: "antigravity", AgentLabel: "Antigravity", Cwd: m.assistantCwd(),
		ToolName: "Bash", ExpiresAt: time.Now().Add(time.Hour), Resumable: true,
		AlwaysKeys: []string{"runtime:antigravity:tool:Bash"},
	}
	inboxDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, inboxCard)
		inboxDone <- r
	}()
	inboxPending := ft.waitPending(1)
	if inboxPending[0].AlwaysHonored == nil || *inboxPending[0].AlwaysHonored || len(inboxPending[0].AlwaysKeys) != 0 {
		t.Fatalf("inbox runtime:antigravity:tool:Bash honored=%v keys=%q, want false/none", inboxPending[0].AlwaysHonored, inboxPending[0].AlwaysKeys)
	}
	if err := s.resolve("ix_agy_inbox_rt", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-inboxDone; r.Always || r.Remembered {
		t.Fatalf("inbox runtime whole-Bash kept always/remembered: %+v", r)
	}
	ft.waitPending(0)

	// 6. Space confirm-outbound policy (health) must never auto-allow from a stale grant.
	const healthSid = "antigravity-health-99"
	if err := s.fleetStore().AllowAlways(ctx, healthSid, "Write", []string{"tool:Edit(//tmp/work/**)"}); err != nil {
		t.Fatal(err)
	}
	healthCard := proto.Interaction{
		ID: "ix_agy_health", MachineID: "mac", SessionID: healthSid, Kind: proto.IxPermission, OutwardWrite: true,
		Agent: "antigravity", AgentLabel: "Antigravity", Cwd: "/home/me/.persona/health",
		ToolName: "Write", ExpiresAt: time.Now().Add(time.Hour), Resumable: true,
		AlwaysKeys: []string{"tool:Edit(//tmp/work/**)"},
	}
	healthDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.nodeInteraction(ctx, healthCard)
		healthDone <- r
	}()
	healthPending := ft.waitPending(1)
	if healthPending[0].AlwaysHonored == nil || *healthPending[0].AlwaysHonored {
		t.Fatalf("health space AGY card honored=%v, want false", healthPending[0].AlwaysHonored)
	}
	if err := s.resolve("ix_agy_health", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-healthDone; r.Always || r.Remembered {
		t.Fatalf("health space AGY card kept always/remembered: %+v", r)
	}
	ft.waitPending(0)

	// 7. keepAlways must re-check ConfirmOutbound on both the source card and any waiting sibling card
	// (even legacy cards with AlwaysHonored == nil).
	const siblingSid = "antigravity-sibling-confirm"
	legacyWaiting := proto.Interaction{
		ID: "ix_agy_wait_health", MachineID: "mac", SessionID: siblingSid, Kind: proto.IxPermission,
		Agent: "antigravity", AgentLabel: "Antigravity", Cwd: "/home/me/.persona/health",
		ToolName: "Write", ExpiresAt: time.Now().Add(time.Hour), Resumable: true,
		AlwaysKeys: []string{"tool:Edit(//tmp/work/**)"},
	}
	nonSpaceSibling := proto.Interaction{
		ID: "ix_agy_nonspace", MachineID: "mac", SessionID: siblingSid, Kind: proto.IxPermission,
		Agent: "antigravity", AgentLabel: "Antigravity", Cwd: m.assistantCwd(),
		ToolName: "Write", ExpiresAt: time.Now().Add(time.Hour), Resumable: true,
		AlwaysKeys: []string{"tool:Edit(//tmp/work/**)"},
	}
	// Source card under ConfirmOutbound must not record any grant in the store.
	s.keepAlways(legacyWaiting, proto.Resolution{Allow: &yes, Always: true})
	if _, ok := s.alwaysAllowed(ctx, nonSpaceSibling); ok {
		t.Fatal("keepAlways persisted a grant from a ConfirmOutbound source card")
	}

	s.fleet.mu.Lock()
	mWait := s.ixMapLocked()
	waitPending := &pending{ix: legacyWaiting, done: make(chan proto.Resolution, 1), resolved: make(chan struct{})}
	mWait[legacyWaiting.ID] = waitPending
	s.fleet.mu.Unlock()
	t.Cleanup(func() { s.finishInteraction(legacyWaiting.ID) })

	// Trigger keepAlways from a non-space sibling card with the same session & key.
	s.keepAlways(nonSpaceSibling, proto.Resolution{Allow: &yes, Always: true})
	select {
	case <-waitPending.resolved:
		t.Fatal("keepAlways woke a waiting ConfirmOutbound legacy card")
	default:
	}
}

// The same completion must not push twice: a Stop whose reply was already pushed for, with no prompt since, is the same
// turn end seen again (the status flapping around it). A new prompt, a different reply, or a Stop with no reply
// (nothing to tell it by) is a new one.
func TestStopDuplicate(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	event := func(sid, name, sig string) {
		ft.event("mac", "hook.event", map[string]any{"session_id": sid, "event": name, "stop_sig": sig})
	}
	for _, step := range []struct {
		name string
		do   func()
		sid  string
		dup  bool
	}{
		{"a session without hooks", func() {}, "plain", false},
		{"the first Stop", func() { event("s1", "Stop", "aaaa") }, "s1", false},
		{"the same Stop again", func() { event("s1", "Stop", "aaaa") }, "s1", true},
		{"another session is its own", func() { event("s2", "Stop", "aaaa") }, "s2", false},
		{"the same reply after a new prompt", func() { event("s1", "UserPromptSubmit", ""); event("s1", "Stop", "aaaa") }, "s1", false},
		{"and again", func() {}, "s1", true},
		{"a different reply", func() { event("s1", "Stop", "bbbb") }, "s1", false},
		{"a Stop with no reply is never held back", func() { event("s1", "Stop", "") }, "s1", false},
		{"nor again", func() {}, "s1", false},
	} {
		step.do()
		if got := s.stopDuplicate("mac", step.sid); got != step.dup {
			t.Errorf("%s: duplicate %v, want %v", step.name, got, step.dup)
		}
	}
}

// A report-only hook (a session the user started, node/globalhooks.go) is not Persona's hooks running in the session:
// it must not hold screen cards back (hasHooks), but its Stop still feeds the duplicate check; a hook of Persona's own
// still counts. The hub still gets the event (it shows it to the apps).
func TestReportOnlyStopDedupesWithoutMarkingHooks(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	b, _ := json.Marshal(map[string]any{"session_id": "s1", "event": "Stop", "stop_sig": "aaaa", "report_only": true})
	if !s.NodeEvent("mac", "hook.event", b) {
		t.Error("the hub no longer gets the hook event")
	}
	if s.hasHooks("mac", proto.Session{SessionID: "s1"}) {
		t.Error("a report-only event marked the session as having Persona's hooks")
	}
	if s.stopDuplicate("mac", "s1") {
		t.Error("the first push was held back")
	}
	if !s.stopDuplicate("mac", "s1") {
		t.Error("the same reply pushed twice")
	}
	ft.event("mac", "hook.event", map[string]any{"session_id": "s-own", "event": "Stop"})
	if !s.hasHooks("mac", proto.Session{SessionID: "s-own"}) {
		t.Error("a hook event of Persona's own no longer counts")
	}
}
