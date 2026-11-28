package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The card ledger (card_layers.go): each push to the owner counted by day with which of the owner's six kinds of
// decisions its source is (0: none of the six, a bug), and told once a day to the main conversation, not the owner.

// cardSourceConsts are the source constants of card_layers.go (the const block that holds cardSecretForm): name → value.
func cardSourceConsts(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "card_layers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		out := map[string]string{}
		holds := false
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == "cardSecretForm" {
					holds = true
				}
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						out[n.Name] = v
					}
				}
			}
		}
		if holds {
			return out
		}
	}
	t.Fatal("card_layers.go has no const block with cardSecretForm")
	return nil
}

// Every source constant has a kind in cardCategories (none is left to the missing-key 0), every kind is 0-7, and the
// table names no source that is not a constant.
func TestCardCategoriesCoverEverySource(t *testing.T) {
	consts := cardSourceConsts(t)
	if len(consts) < 30 {
		t.Fatalf("read only %d source constants: %v", len(consts), consts)
	}
	values := map[string]string{}
	for name, v := range consts {
		if other, dup := values[v]; dup {
			t.Errorf("%s and %s are both %q", name, other, v)
		}
		values[v] = name
		cat, ok := cardCategories[v]
		if !ok {
			t.Errorf("source %s (%q) has no kind in cardCategories", name, v)
		}
		if cat < cardCatNone || cat > cardCatSubscribed {
			t.Errorf("source %s has kind %d, want 0-%d", name, cat, cardCatSubscribed)
		}
	}
	for src := range cardCategories {
		if _, ok := values[src]; !ok {
			t.Errorf("cardCategories names %q, which is no source constant", src)
		}
	}
}

// The kinds the decision-rights design gives the sources.
func TestCardCategoriesOfTheSixKinds(t *testing.T) {
	for src, want := range map[string]int{
		cardSecretForm: cardCatInPerson, cardAgentSecretForm: cardCatInPerson,
		cardOutboundWrite: cardCatOutward,
		cardPlanMoney:     cardCatMoney, cardPlanOutward: cardCatOutward, cardPlanIrreversible: cardCatIrreversible,
		cardLeaderNeedsUser: cardCatDirection, cardDecision: cardCatDirection, cardAskEscalated: cardCatDirection,
		cardDecisionDirection: cardCatDirection, cardDecisionOwnerKnows: cardCatOwnerKnows,
		cardDecisionOutward: cardCatOutward, cardDecisionIrreversible: cardCatIrreversible,
		cardAskEscalatedDirection: cardCatDirection, cardAskEscalatedOwnerKnows: cardCatOwnerKnows,
		cardAskEscalatedOutward: cardCatOutward, cardAskEscalatedIrreversible: cardCatIrreversible,
		cardTodoRemind: cardCatSubscribed, cardHealthWater: cardCatSubscribed, cardPageAlert: cardCatSubscribed, cardMinutesReady: cardCatSubscribed,
		cardDayRecap: cardCatSubscribed, cardBriefFallback: cardCatSubscribed, cardAgentDone: cardCatSubscribed,
		cardConfirmOutbound: cardCatNone, cardSettingsChange: cardCatNone, cardAgentPermission: cardCatNone,
		cardAgentPlan: cardCatNone, cardAgentQuestion: cardCatNone, cardPlanWaiting: cardCatNone, cardAskUser: cardCatNone,
		cardToolPermission: cardCatNone, cardManagerQuestion: cardCatNone, cardTaskFailed: cardCatNone,
		cardUntagged: cardCatNone, "a source nobody named": cardCatNone,
	} {
		if got := cardCategoryOf(src); got != want {
			t.Errorf("%s: kind %d, want %d", src, got, want)
		}
	}
}

// cardsToday is the counts of the day it is now in s's zone, by source.
func cardsToday(t *testing.T, s *Service) map[string]cardCount {
	t.Helper()
	ctx := context.Background()
	got := map[string]cardCount{}
	for _, r := range s.cardsOn(ctx, time.Now().In(s.location(ctx)).Format(time.DateOnly)) {
		c := got[r.Source]
		c.Source, c.Category, c.N, c.Held = r.Source, r.Category, c.N+r.N, c.Held+r.Held
		got[r.Source] = c
	}
	return got
}

// A source is counted with its kind, a push no source was named for as untagged (none of the six); the pushes
// themselves go out as before.
func TestCardLedgerCountsPushesBySourceAndKind(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	s.noteCard(ctx, cardLeaderNeedsUser, Notification{SessionID: "conv1"})
	s.push(withCard(ctx, cardPlanWaiting), Notification{Title: "a plan waits", SessionID: "conv1"})
	s.push(withCard(ctx, cardPlanWaiting), Notification{Title: "another plan waits", SessionID: "conv1"})
	s.push(ctx, Notification{Title: "from nowhere"})

	sent := 0
	for _, n := range f.pushed() {
		if strings.Contains(n.Title, "plan waits") || n.Title == "from nowhere" {
			sent++
		}
	}
	if sent != 3 {
		t.Fatalf("%d of the pushes went out, want all 3: the ledger only records", sent)
	}
	got := cardsToday(t, s)
	if c := got[cardLeaderNeedsUser]; c.Category != cardCatDirection || c.N != 1 {
		t.Errorf("leader_needs_user = %+v, want kind 1 once", c)
	}
	if c := got[cardPlanWaiting]; c.Category != cardCatNone || c.N != 2 {
		t.Errorf("plan_waiting = %+v, want kind 0 twice", c)
	}
	if c := got[cardUntagged]; c.Category != cardCatNone || c.N != 1 {
		t.Errorf("untagged = %+v, want kind 0 once", c)
	}
}

// The counts are kept in the database: a manager started again reads them.
func TestCardLedgerSurvivesARestart(t *testing.T) {
	st := newFleetMem()
	s, _ := newTaskServiceOn(t, st)
	s.noteCard(context.Background(), cardSecretForm, Notification{SessionID: "lead1"})
	again, _ := newTaskServiceOn(t, st)
	if c := cardsToday(t, again)[cardSecretForm]; c.Category != cardCatInPerson || c.N != 1 {
		t.Fatalf("after a restart secret_form = %+v, want kind 6 once", c)
	}
}

// An Agent's card takes its source from its kind, the manager's own from what ask was told or from its tool.
func TestInputCardSource(t *testing.T) {
	s, _ := newQuietService(t)
	s.cardAsks.Store("ix_own", cardConfirmOutbound)
	for _, tc := range []struct {
		ix        proto.Interaction
		escalated bool
		want      string
	}{
		{proto.Interaction{ID: "ix_own", Kind: proto.IxPermission, ToolName: "agent_send"}, false, cardConfirmOutbound},
		{proto.Interaction{ID: "ix_a", Kind: proto.IxElicitation}, true, cardAgentSecretForm},
		{proto.Interaction{ID: "ix_b", Kind: proto.IxQuestion, Secret: true}, false, cardAgentSecretForm},
		{proto.Interaction{ID: "ix_c", Kind: proto.IxPermission, ToolName: "Bash"}, true, cardAskEscalated},
		{proto.Interaction{ID: "ix_d", Kind: proto.IxPermission, ToolName: "Bash"}, false, cardAgentPermission},
		// Trusted connector gate outward writes count as category 3 regardless of session title or escalation.
		{proto.Interaction{ID: "ix_gate_out", Kind: proto.IxPermission, ToolName: "Feishu", SessionTitle: "Project Chat", OutwardWrite: true}, false, cardOutboundWrite},
		{proto.Interaction{ID: "ix_gate_esc", Kind: proto.IxPermission, ToolName: "Feishu", SessionTitle: "Project Chat", OutwardWrite: true}, true, cardOutboundWrite},
		{proto.Interaction{ID: "ix_gate_persona", Kind: proto.IxPermission, ToolName: "Feishu", SessionTitle: "Persona", OutwardWrite: true}, false, cardOutboundWrite},
		// Text wording alone never elevates an untagged agent permission to outbound_write.
		{proto.Interaction{ID: "ix_untagged_text", Kind: proto.IxPermission, ToolName: "Feishu", SessionTitle: "Project Chat", Summary: "Send a message as you", OutwardWrite: false}, false, cardAgentPermission},
		// Non-permission kinds ignore OutwardWrite.
		{proto.Interaction{ID: "ix_q_out", Kind: proto.IxQuestion, OutwardWrite: true}, false, cardAgentQuestion},
		{proto.Interaction{ID: "ix_plan_out", Kind: proto.IxPlan, OutwardWrite: true}, false, cardAgentPlan},
		{proto.Interaction{ID: "ix_e", Kind: proto.IxPlan}, false, cardAgentPlan},
		{proto.Interaction{ID: "ix_f", Kind: proto.IxQuestion, ToolName: "ask_user"}, false, cardAskUser},
		{proto.Interaction{ID: "ix_g", Kind: proto.IxScreen}, false, cardAgentQuestion},
		// persona_settings: the settings change card, even with fields (extmgr self_settings.go)
		{proto.Interaction{ID: "ix_h", Kind: proto.IxPermission, ToolName: "persona_settings", SessionTitle: "Persona",
			Fields: []proto.Field{{Key: "value"}}}, false, cardSettingsChange},
		{proto.Interaction{ID: "ix_i", Kind: proto.IxPermission, ToolName: "repository_add", SessionTitle: "Persona"}, false, cardRepositoryAdd},
		{proto.Interaction{ID: "ix_j", Kind: proto.IxPermission, ToolName: "mail_send", SessionTitle: "Persona"}, false, cardOutboundWrite},
		{proto.Interaction{ID: "ix_k", Kind: proto.IxPermission, ToolName: "github_comment", SessionTitle: "Persona"}, false, cardOutboundWrite},
		{proto.Interaction{ID: "ix_l", Kind: proto.IxPermission, ToolName: "iphone_do", SessionTitle: "Persona"}, false, cardDeviceControl},
		{proto.Interaction{ID: "ix_m", Kind: proto.IxPermission, ToolName: "extension.ui", SessionTitle: "Persona"}, false, cardExtensionApproval},
	} {
		got := s.inputCardSource(tc.ix, tc.escalated)
		if got != tc.want {
			t.Errorf("%s: source %q, want %q", tc.ix.ID, got, tc.want)
		}
		if _, ok := cardCategories[got]; !ok {
			t.Errorf("source %s has no kind in cardCategories", got)
		}
	}
}

// The day's report: in all, each of the six kinds, and none of the six by source, the most first; nothing pushed says
// nothing. A row is counted by the kind the table gives its source now.
func TestCardLedgerReport(t *testing.T) {
	rows := []cardCount{
		{Source: cardDecision, Category: cardCatDirection, Owner: "lead1", N: 3},
		{Source: cardSecretForm, Category: cardCatInPerson, Owner: "conv1", N: 1},
		{Source: cardTaskFailed, Category: cardCatNone, Owner: "lead1", N: 1},
		{Source: cardAgentPermission, Category: cardCatNone, Owner: "lead1", N: 2},
		{Source: cardTaskFailed, Category: cardCatNone, Owner: "conv1", N: 1},
		{Source: cardSettingsChange, Category: cardCatDirection, Owner: "conv1", N: 1}, // saved under an older table
	}
	tally := tallyCards(rows)
	if tally.Total != 9 || tally.ByKind[cardCatDirection] != 3 || tally.ByKind[cardCatInPerson] != 1 || tally.ByKind[cardCatNone] != 5 {
		t.Fatalf("tally = %+v", tally)
	}
	got := cardLedgerReport("2026-11-22", rows)
	for _, want := range []string{
		"Card ledger for 2026-11-22", "Pushed to the owner's phone: 9.",
		"1 goals and direction: 3 — decision×3\n", "2 only the owner knows: 0\n", "3 outward as the owner: 0\n", "4 money: 0\n",
		"5 irreversible, never said: 0\n", "6 only the owner in person: 1 — secret_form×1\n",
		"Subscribed alerts and reminders (not decisions, not bugs): 0\n",
		"None of the six (each one a bug): 5 — agent_permission×2, task_failed×2, settings_change×1.",
		"not the owner",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lacks %q:\n%s", want, got)
		}
	}
	if got := cardLedgerReport("2026-11-22", nil); got != "" {
		t.Errorf("nothing pushed = %q, want no report", got)
	}
}

// The report goes to the main conversation once a day from the morning briefing's time, kept without waking it: no
// push to the owner, no message sent into a session.
func TestCardLedgerReportGoesToTheMainConversationWithoutAPush(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	loc := s.location(ctx)
	today := time.Now().In(loc)
	yesterday := today.AddDate(0, 0, -1)
	s.countCard(ctx, yesterday, cardCount{Source: cardAgentPermission, Category: cardCatNone, Owner: "lead1", N: 2})
	s.countCard(ctx, yesterday, cardCount{Source: cardDecision, Category: cardCatDirection, Owner: "lead1", N: 1})
	pushes := len(f.pushed())
	sends := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if c.Method == DeviceSessionSend {
				n++
			}
		}
		return n
	}
	sent := sends()

	early := time.Date(today.Year(), today.Month(), today.Day(), 0, 1, 0, 0, loc)
	s.reportCardLedger(ctx, early)
	if n := len(laterFor(s, "conv1")); n != 0 {
		t.Fatalf("%d kept before the morning briefing's time", n)
	}
	late := time.Date(today.Year(), today.Month(), today.Day(), 23, 30, 0, 0, loc)
	s.reportCardLedger(ctx, late)
	s.reportCardLedger(ctx, late.Add(10*time.Minute)) // once a day
	kept := laterFor(s, "conv1")
	if len(kept) != 1 {
		t.Fatalf("%d kept for the main conversation, want the one report: %+v", len(kept), kept)
	}
	if !kept[0].Internal {
		t.Error("the card ledger report is not marked internal")
	}
	if shown, _ := s.queuedRelays(); len(shown["conv1"]) != 0 {
		t.Fatalf("the card ledger report is shown above the composer: %v", shown["conv1"])
	}
	if !strings.Contains(kept[0].Text, "Card ledger for "+yesterday.Format(time.DateOnly)) ||
		!strings.Contains(kept[0].Text, "None of the six (each one a bug): 2 — agent_permission×2.") ||
		!strings.Contains(kept[0].Text, "1 goals and direction: 1 — decision×1\n") {
		t.Errorf("report:\n%s", kept[0].Text)
	}
	if n := len(f.pushed()); n != pushes {
		t.Errorf("%d push(es) after the report, want none: it is the main conversation's, not the owner's", n-pushes)
	}
	if n := sends(); n != sent {
		t.Errorf("%d message(s) sent into a session, want none: the report waits without waking anyone", n-sent)
	}
}

func seedDecisionForLedgerTest(s *Service, id, whyOwner string) {
	now := time.Now().UTC()
	s.todos.mu.Lock()
	if s.todos.todos == nil {
		s.todos.todos = map[string]*Todo{}
	}
	s.todos.todos[id] = &Todo{ID: id, Title: "Decision " + id, Origin: "lead1", CreatedAt: now}
	s.todos.mu.Unlock()
	s.accept.mu.Lock()
	s.accept.loaded = true
	if s.accept.asks == nil {
		s.accept.asks = map[string]*acceptAsk{}
	}
	s.accept.asks[id] = &acceptAsk{
		Conclusion: true,
		Outcome:    outcomeDecide,
		Reason:     "Choose the next step",
		Recommend:  "Option A",
		WhyOwner:   whyOwner,
		AskedAt:    now,
	}
	s.accept.mu.Unlock()
}

// Pushes are accounted only after NotifyRequest succeeds: held pushes (res.Held == true) increment the separate Held
// tally and never inflate pushed totals or Bugs (no ask_held false positives); failed pushes count in neither; legacy
// zero-value NotifyResult (Held == false) degrades smoothly and counts as pushed.
func TestCardLedgerSkipsHeldPushes(t *testing.T) {
	st := newFleetMem()
	s, f := newTaskServiceOn(t, st)
	ctx := context.Background()

	// Disable non-owner routing so remindReply hits the fallback NotifyRequest path directly.
	setSettings(t, s, func(v *Settings) { v.Main = ""; v.Leaders = nil })
	seedDecisionForLedgerTest(s, "todo_held", whyPriority)

	// 1) Hub push grading holds every push (err == nil && res.Held == true).
	f.mu.Lock()
	f.notifyResult = NotifyResult{OK: true, Held: true, Reason: "ln_p1_held"}
	f.mu.Unlock()

	if _, err := s.pushResult(withCard(ctx, cardAskHeld), Notification{Title: "held reminder", SessionID: "conv1"}); err != nil {
		t.Fatalf("pushResult held: %v", err)
	}
	s.deliverCard(cardAgentDone, Notification{Kind: assistantwire.NotifyDone, Title: "done held", SessionID: "sess1"})
	waitFor(t, "deliverCard held recorded", func() bool {
		return cardsToday(t, s)[cardAgentDone].Held == 1
	})
	if sent, err := s.pushDecisionErr(ctx, "todo_held"); err != nil || !sent {
		t.Fatalf("pushDecisionAbout held: sent=%v err=%v", sent, err)
	}
	if _, err := (healthHost{s}).Notify(ctx, Notification{Kind: assistantwire.NotifyHealthWater, Title: "water", SessionID: "conv1"}); err != nil {
		t.Fatalf("healthHost.Notify held: %v", err)
	}
	if err := s.remindReply("mac", proto.Session{SessionID: "sess_reply", Title: "Agent", Asks: "ready?", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("remindReply held: %v", err)
	}
	recNote := Notification{ID: "ntf_minutes_rec1_1", Kind: NotifyPush, Title: "Minutes", SessionID: "conv1"}
	if err := (recordingHost{s}).Notify(ctx, recNote); err != nil {
		t.Fatalf("recordingHost.Notify held: %v", err)
	}
	(recordingHost{s}).NoteCard(ctx, cardMinutesReady, recNote)

	got := cardsToday(t, s)
	for _, src := range []string{cardAskHeld, cardAgentDone, cardDecisionDirection, cardHealthWater, cardAgentReply, cardMinutesReady} {
		c := got[src]
		if c.N != 0 || c.Held != 1 {
			t.Errorf("held %s = %+v, want N=0 Held=1", src, c)
		}
	}

	day := time.Now().In(s.location(ctx)).Format(time.DateOnly)
	rows := s.cardsOn(ctx, day)
	tally := tallyCards(rows)
	if tally.Total != 0 || len(tally.Bugs) != 0 || tally.Held != 6 {
		t.Fatalf("held-only tally = %+v, want Total=0 Bugs=0 Held=6", tally)
	}
	report := cardLedgerReport(day, rows)
	if !strings.Contains(report, "Pushed to the owner's phone: 0.") ||
		!strings.Contains(report, "None of the six (each one a bug): 0.") ||
		!strings.Contains(report, "Held by push grading (not pushed to phone): 6 — ") ||
		strings.Contains(report, "platform defect") {
		t.Fatalf("unexpected held-only report:\n%s", report)
	}

	// Held counts survive a restart through assistant.card_ledger.
	again, _ := newTaskServiceOn(t, st)
	if c := cardsToday(t, again)[cardAskHeld]; c.N != 0 || c.Held != 1 {
		t.Fatalf("after restart ask_held = %+v, want N=0 Held=1", c)
	}

	// 2) NotifyRequest errors record in neither N nor Held.
	errLogged := make(chan struct{}, 1)
	prevLog := s.logf
	s.logf = func(format string, args ...any) {
		if strings.HasPrefix(format, "assistant: push:") {
			select {
			case errLogged <- struct{}{}:
			default:
			}
		}
		if prevLog != nil {
			prevLog(format, args...)
		}
	}
	f.mu.Lock()
	f.notifyResult = NotifyResult{}
	f.refuseNotify = errors.New("hub refused")
	f.mu.Unlock()

	if _, err := s.pushResult(withCard(ctx, cardTodoRemind), Notification{Title: "err remind", SessionID: "conv1"}); err == nil {
		t.Fatal("expected pushResult error")
	}
	s.deliverCard(cardBriefFallback, Notification{Kind: assistantwire.NotifyDone, Title: "err brief", SessionID: "sess2"})
	select {
	case <-errLogged:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for deliverCard error log")
	}
	if _, err := s.pushDecisionErr(ctx, "todo_held"); err == nil {
		t.Fatal("expected pushDecisionAbout error")
	}
	if _, err := (healthHost{s}).Notify(ctx, Notification{Kind: assistantwire.NotifyHealthWater, Title: "water err", SessionID: "conv1"}); err == nil {
		t.Fatal("expected healthHost.Notify error")
	}
	if err := s.remindReply("mac", proto.Session{SessionID: "sess_reply_err", Title: "Agent", Asks: "ready?", UpdatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected remindReply error")
	}

	afterErr := cardsToday(t, s)
	if c := afterErr[cardTodoRemind]; c.N != 0 || c.Held != 0 {
		t.Errorf("failed todo_remind = %+v, want N=0 Held=0", c)
	}
	if c := afterErr[cardBriefFallback]; c.N != 0 || c.Held != 0 {
		t.Errorf("failed brief_fallback = %+v, want N=0 Held=0", c)
	}
	if c := afterErr[cardDecisionDirection]; c.N != 0 || c.Held != 1 {
		t.Errorf("failed decision_direction = %+v, want unchanged N=0 Held=1", c)
	}
	if c := afterErr[cardHealthWater]; c.N != 0 || c.Held != 1 {
		t.Errorf("failed health_water = %+v, want unchanged N=0 Held=1", c)
	}
	if c := afterErr[cardAgentReply]; c.N != 0 || c.Held != 1 {
		t.Errorf("failed agent_reply = %+v, want unchanged N=0 Held=1", c)
	}

	// 3) Legacy hub response (zero-value NotifyResult, Held == false) counts as pushed to phone.
	f.mu.Lock()
	f.refuseNotify = nil
	f.notifyResult = NotifyResult{}
	f.mu.Unlock()

	if _, err := s.pushResult(withCard(ctx, cardTodoRemind), Notification{Title: "ok remind", SessionID: "conv1"}); err != nil {
		t.Fatalf("pushResult ok: %v", err)
	}
	if c := cardsToday(t, s)[cardTodoRemind]; c.N != 1 || c.Held != 0 || c.Category != cardCatSubscribed {
		t.Fatalf("legacy ok todo_remind = %+v, want N=1 Held=0 cat=%d", c, cardCatSubscribed)
	}
}

// Decisions and ask_escalate map to fixed per-why_owner sources (1/2/3/5), while preserving legacy cardDecision,
// cardAskEscalated and cardLeaderNeedsUser for 8-day historical JSON rows.
func TestDecisionAndEscalatedCategorizedByWhyOwner(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()

	cases := []struct {
		why             string
		wantDecisionSrc string
		wantEscSrc      string
		wantCat         int
	}{
		{whyPriority, cardDecisionDirection, cardAskEscalatedDirection, cardCatDirection},
		{whyAcceptance, cardDecisionDirection, cardAskEscalatedDirection, cardCatDirection},
		{whyOnlyOwnerKnow, cardDecisionOwnerKnows, cardAskEscalatedOwnerKnows, cardCatOwnerKnows},
		{whyVisible, cardDecisionOutward, cardAskEscalatedOutward, cardCatOutward},
		{whyIrreversible, cardDecisionIrreversible, cardAskEscalatedIrreversible, cardCatIrreversible},
		{"", cardDecision, cardAskEscalated, cardCatDirection},
		{"unknown_legacy", cardDecision, cardAskEscalated, cardCatDirection},
	}

	for i, tc := range cases {
		todoID := "todo_why_" + strconv.Itoa(i)
		seedDecisionForLedgerTest(s, todoID, tc.why)
		if sent, err := s.pushDecisionErr(ctx, todoID); err != nil || !sent {
			t.Fatalf("why=%q pushDecisionAbout: sent=%v err=%v", tc.why, sent, err)
		}
		if got := decisionCardSource(tc.why); got != tc.wantDecisionSrc {
			t.Errorf("decisionCardSource(%q) = %q, want %q", tc.why, got, tc.wantDecisionSrc)
		}
		if got := cardCategoryOf(tc.wantDecisionSrc); got != tc.wantCat {
			t.Errorf("cardCategoryOf(%q) = %d, want %d", tc.wantDecisionSrc, got, tc.wantCat)
		}
		if got := escalatedCardSource(tc.why); got != tc.wantEscSrc {
			t.Errorf("escalatedCardSource(%q) = %q, want %q", tc.why, got, tc.wantEscSrc)
		}
		if got := cardCategoryOf(tc.wantEscSrc); got != tc.wantCat {
			t.Errorf("cardCategoryOf(%q) = %d, want %d", tc.wantEscSrc, got, tc.wantCat)
		}
		perm := proto.Interaction{ID: "ix_esc_" + strconv.Itoa(i), Kind: proto.IxPermission, ToolName: "Bash"}
		if got := s.inputCardSourceWithWhy(perm, true, tc.why); got != tc.wantEscSrc {
			t.Errorf("inputCardSourceWithWhy(%q) = %q, want %q", tc.why, got, tc.wantEscSrc)
		}
	}

	// Secret/form and connector OutwardWrite keep priority over escalated why_owner.
	if got := s.inputCardSourceWithWhy(proto.Interaction{ID: "ix_sec", Kind: proto.IxQuestion, Secret: true}, true, whyPriority); got != cardAgentSecretForm {
		t.Errorf("secret escalated = %q, want %q", got, cardAgentSecretForm)
	}
	if got := s.inputCardSourceWithWhy(proto.Interaction{ID: "ix_out", Kind: proto.IxPermission, OutwardWrite: true}, true, whyPriority); got != cardOutboundWrite {
		t.Errorf("outward escalated = %q, want %q", got, cardOutboundWrite)
	}

	got := cardsToday(t, s)
	for src, want := range map[string]struct{ cat, n int }{
		cardDecisionDirection:    {cardCatDirection, 2}, // priority + acceptance
		cardDecisionOwnerKnows:   {cardCatOwnerKnows, 1},
		cardDecisionOutward:      {cardCatOutward, 1},
		cardDecisionIrreversible: {cardCatIrreversible, 1},
		cardDecision:             {cardCatDirection, 2}, // "" + unknown_legacy
	} {
		if c := got[src]; c.Category != want.cat || c.N != want.n {
			t.Errorf("ledger[%s] = %+v, want cat=%d n=%d", src, c, want.cat, want.n)
		}
	}

	// Historical 8-day JSON rows without the new fields or split source names decode and tally cleanly.
	const legacyJSON = `[{"source":"decision","category":1,"owner":"lead1","n":4},{"source":"ask_escalated","category":1,"owner":"lead1","n":2},{"source":"leader_needs_user","category":1,"owner":"lead1","n":1}]`
	var legacyRows []cardCount
	if err := json.Unmarshal([]byte(legacyJSON), &legacyRows); err != nil {
		t.Fatalf("unmarshal legacy rows: %v", err)
	}
	tally := tallyCards(legacyRows)
	if tally.Total != 7 || tally.ByKind[cardCatDirection] != 7 || len(tally.Bugs) != 0 || tally.Held != 0 {
		t.Fatalf("legacy tally = %+v, want Total=7 Direction=7 Bugs=0 Held=0", tally)
	}
}

// Owner-subscribed alerts and reminders live in cardCatSubscribed (7), never inflating category 1 (goals and
// direction) and never counted as category 0 platform defects.
func TestSubscribedAlertsSeparatedFromDirection(t *testing.T) {
	subscribed := []string{
		cardTodoRemind,
		cardHealthWater,
		cardPageAlert,
		cardMinutesReady,
		cardDayRecap,
		cardBriefFallback,
		cardAgentDone,
	}
	var rows []cardCount
	for _, src := range subscribed {
		if got := cardCategoryOf(src); got != cardCatSubscribed {
			t.Errorf("cardCategoryOf(%q) = %d, want %d (cardCatSubscribed)", src, got, cardCatSubscribed)
		}
		rows = append(rows, cardCount{Source: src, Category: cardCatDirection, Owner: "conv1", N: 2}) // even if saved under old cat 1
	}
	rows = append(rows, cardCount{Source: cardDecisionDirection, Category: cardCatDirection, Owner: "lead1", N: 3})

	tally := tallyCards(rows)
	if tally.Total != 17 {
		t.Fatalf("Total = %d, want 17", tally.Total)
	}
	if tally.ByKind[cardCatDirection] != 3 {
		t.Errorf("Direction = %d, want 3 (only genuine direction decisions)", tally.ByKind[cardCatDirection])
	}
	if tally.ByKind[cardCatSubscribed] != 14 {
		t.Errorf("Subscribed = %d, want 14", tally.ByKind[cardCatSubscribed])
	}
	if tally.ByKind[cardCatNone] != 0 || len(tally.Bugs) != 0 {
		t.Errorf("Bugs = %v (kind 0 = %d), want none", tally.Bugs, tally.ByKind[cardCatNone])
	}

	report := cardLedgerReport("2026-11-25", rows)
	for _, want := range []string{
		"1 goals and direction: 3 — decision_direction×3\n",
		"Subscribed alerts and reminders (not decisions, not bugs): 14 — ",
		"None of the six (each one a bug): 0.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "platform defect") {
		t.Errorf("subscribed alerts triggered platform defect wording:\n%s", report)
	}
}

// cardLedgerReport prints descending per-source breakdowns for every non-zero category (1-6, subscribed, bugs, held),
// omits breakdown dashes on zero categories, and keeps Held outside the pushed-to-phone total.
func TestCardLedgerReportIncludesPerKindBreakdown(t *testing.T) {
	rows := []cardCount{
		{Source: cardDecisionDirection, Owner: "lead1", N: 9},
		{Source: cardAskEscalatedDirection, Owner: "lead1", N: 2},
		{Source: cardDecision, Owner: "lead2", N: 1},
		{Source: cardDecisionOwnerKnows, Owner: "lead1", N: 2},
		{Source: cardAskEscalatedOwnerKnows, Owner: "lead1", N: 2}, // tie: ask_escalated_owner_knows < decision_owner_knows
		{Source: cardOutboundWrite, Owner: "conv1", N: 3},
		{Source: cardDecisionOutward, Owner: "lead1", N: 1},
		{Source: cardDecisionIrreversible, Owner: "lead1", N: 2},
		{Source: cardPlanIrreversible, Owner: "lead1", N: 1},
		{Source: cardSecretForm, Owner: "conv1", N: 1},
		{Source: cardAgentDone, Owner: "sess1", N: 5, Held: 12},
		{Source: cardTodoRemind, Owner: "conv1", N: 3},
		{Source: cardHealthWater, Owner: "conv1", N: 3}, // tie: health_water < todo_remind
		{Source: cardTaskFailed, Owner: "lead1", N: 2},
		{Source: cardAskHeld, Owner: "conv1", N: 0, Held: 4},
	}

	report := cardLedgerReport("2026-11-25", rows)
	for _, want := range []string{
		"Card ledger for 2026-11-25 (decision rights: the owner decides only six kinds of things). Pushed to the owner's phone: 37.",
		"1 goals and direction: 12 — decision_direction×9, ask_escalated_direction×2, decision×1\n",
		"2 only the owner knows: 4 — ask_escalated_owner_knows×2, decision_owner_knows×2\n",
		"3 outward as the owner: 4 — outbound_write×3, decision_outward×1\n",
		"4 money: 0\n",
		"5 irreversible, never said: 3 — decision_irreversible×2, plan_irreversible×1\n",
		"6 only the owner in person: 1 — secret_form×1\n",
		"Subscribed alerts and reminders (not decisions, not bugs): 11 — agent_done×5, health_water×3, todo_remind×3\n",
		"None of the six (each one a bug): 2 — task_failed×2.",
		"Held by push grading (not pushed to phone): 16 — agent_done×12, ask_held×4\n",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "4 money: 0 —") {
		t.Errorf("zero category included breakdown dash:\n%s", report)
	}
	if strings.Contains(report, "ask_held×4.") {
		t.Errorf("held ask_held leaked into Bugs line:\n%s", report)
	}
}
