package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func hasRelayFrom(s *Service, to, from, needle string) bool {
	for _, m := range append(laterFor(s, to), queuedRelays(s, to)...) {
		if m.From == from && (needle == "" || strings.Contains(m.Text, needle)) {
			return true
		}
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, p := range s.fleet.interactions {
		if p.route != nil && !p.route.standaloneUntil.IsZero() && p.route.at() == to && (needle == "" || strings.Contains("interaction_id="+p.ix.ID, needle)) && s.inputCardSource(p.ix, false) == from {
			return true
		}
	}
	return false
}

// Source 1 (agent_permission): non-six-class tool permissions from an unsupervised agent go to the main
// conversation instead of pushing to the owner, while six-class permissions (secret/form, outward/delete
// approval) still push to the owner. The main conversation's own ordinary permission goes to an independent
// background reviewer when configured (TestSupervisingConversationsRouteTheirOwnInternalCards).
func TestAgentPermissionNonOwnerRoutedAndSixClassesStillPush(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor = "bg1" })
	now := time.Now().UTC()

	// Unsupervised agent permission -> main conversation (conv1), no owner push.
	unsupervised := proto.Interaction{
		ID: "ix_perm_unsupervised", MachineID: "mac", SessionID: "standalone-agent",
		Kind: proto.IxPermission, ToolName: "Bash", Summary: "git status",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	uDone := e.raise(unsupervised)
	e.noticeWith("conv1", "interaction_id="+unsupervised.ID)
	if !e.lastShown(unsupervised.ID).Delegating {
		t.Fatal("unsupervised permission was not marked delegating")
	}

	// Main conversation's own internal tool permission actively wakes the independent reviewer.
	mainOwn := proto.Interaction{
		ID: "ix_perm_main_own", MachineID: "server", SessionID: "conv1",
		Kind: proto.IxPermission, ToolName: "Read", Summary: "/tmp/report.md",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	mDone := e.raise(mainOwn)
	waitFor(t, "main permission routed to independent reviewer", func() bool { return hasRelayFrom(e.s, "bg1", cardAgentPermission, "interaction_id="+mainOwn.ID) })

	// Six-class permission (delete/outward action) -> still pushed to the owner.
	outward := proto.Interaction{
		ID: "ix_perm_outward", MachineID: "mac", SessionID: "standalone-agent",
		Kind: proto.IxPermission, ToolName: "Bash", Summary: "delete user records permanently",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	oDone := e.raise(outward)
	waitFor(t, "six-class permission pushed to owner", func() bool { return e.pushesOf(outward.ID) == 1 })

	// Six-class secret/form permission -> still pushed to the owner.
	secretIx := proto.Interaction{
		ID: "ix_perm_secret", MachineID: "mac", SessionID: "standalone-agent",
		Kind: proto.IxPermission, ToolName: "Bash", Summary: "enter password", Secret: true,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	sDone := e.raise(secretIx)
	waitFor(t, "secret permission pushed to owner", func() bool { return e.pushesOf(secretIx.ID) == 1 })

	e.settle()
	if n := e.pushesOf(unsupervised.ID); n != 0 {
		t.Fatalf("unsupervised non-owner permission pushed %d times", n)
	}
	if !hasRelayFrom(e.s, "bg1", cardAgentPermission, "interaction_id="+mainOwn.ID) {
		t.Fatal("main own permission was not handed to the background supervisor")
	}
	if !e.lastShown(mainOwn.ID).Delegating {
		t.Fatal("main own permission is not shown as held by a supervisor")
	}
	// The six-class outward permission and the main conversation's own one count under agent_permission; the
	// unsupervised agent's does not (handed to main), and the secret is agent_secret_form.
	waitFor(t, "agent_permission ledger recorded", func() bool {
		return cardsToday(t, e.s)[cardAgentPermission].N == 1
	})

	allow := true
	for _, pair := range []struct {
		id string
		ch <-chan proto.Resolution
	}{
		{unsupervised.ID, uDone}, {mainOwn.ID, mDone}, {outward.ID, oDone}, {secretIx.ID, sDone},
	} {
		if err := e.s.resolve(pair.id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		answerOf(t, pair.ch)
	}
}

func TestStandalonePermissionWaitsForBusyMain(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.supervisorState(proto.StatusWorking, proto.StatusIdle)
	ix := askPermission("ix_standalone_busy", "standalone-agent")
	ix.ToolInput = json.RawMessage(`{"command":"git status"}`)
	worker := proto.Session{SessionID: ix.SessionID, MachineID: ix.MachineID, Cwd: "/code/work", Status: proto.StatusBlocked}
	e.sessions(ix.MachineID, worker)
	e.f.setSessions(ix.MachineID, worker)

	done := e.raise(ix)
	waitFor(t, "permission queued for the busy main conversation", func() bool {
		return len(queuedRelays(e.s, "conv1")) == 1
	})
	if q := queuedRelays(e.s, "conv1"); !q[0].Internal {
		t.Fatal("the routed permission relay is not marked internal")
	}
	if n := len(e.f.noticesTo("conv1")); n != 0 {
		t.Fatalf("busy conversation received %d messages", n)
	}
	e.supervisorState(proto.StatusIdle, proto.StatusIdle)
	e.noticeWith("conv1", "interaction_id="+ix.ID)
	args := map[string]any{"machine": ix.MachineID, "session_id": ix.SessionID, "interaction_id": ix.ID,
		"allow": false, "reason": "The agent can proceed without this tool"}
	if _, err := runJSON(t, e.s, "lead1", "interaction_answer", args); err == nil {
		t.Fatal("an unrelated leader could answer the permission")
	}
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", args); err != nil {
		t.Fatalf("main conversation could not answer its permission: %v", err)
	}
	if got := answerOf(t, done); got.Allow == nil || *got.Allow || got.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("agent received %+v, want denial by the assistant", got)
	}
	waitFor(t, "permission relay removed after delivery", func() bool { return len(queuedRelays(e.s, "conv1")) == 0 })
	e.s.askTick(ix.ExpiresAt)
	e.s.drainHeldRelays(context.Background(), ix.ExpiresAt)
	if n := len(e.f.noticesTo("conv1")); n != 1 {
		t.Fatalf("main conversation received %d messages, want one", n)
	}
	if n := e.pushesOf(ix.ID); n != 0 {
		t.Fatalf("answered permission pushed %d times", n)
	}
}

func TestStandalonePermissionReturnsToAppWhenMainIsUnavailable(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.sessions("server")
	e.f.setSessions("server")
	ix := askPermission("ix_standalone_unavailable", "standalone-agent")
	ix.ToolInput = json.RawMessage(`{"command":"git status"}`)
	done := e.raise(ix)
	waitFor(t, "permission queued for the unavailable main conversation", func() bool {
		return len(queuedRelays(e.s, "conv1")) == 1
	})
	at := ix.CreatedAt.Add(45 * time.Minute)
	e.s.askTick(at)
	if e.lastShown(ix.ID).Delegating {
		t.Fatal("unhandled permission is still held by the unavailable conversation")
	}
	if _, ok := e.s.routedTo(ix.ID, "conv1", true); ok {
		t.Fatal("main conversation can still answer after the app took over")
	}
	e.s.drainHeldRelays(context.Background(), at)
	if q := queuedRelays(e.s, "conv1"); len(q) != 0 {
		t.Fatalf("unavailable conversation still has %d queued messages", len(q))
	}
	e.supervisorState(proto.StatusIdle, proto.StatusIdle)
	allow := false
	if err := e.s.resolveApproved(context.Background(), ix.ID, proto.Resolution{Allow: &allow}, nil); err != nil {
		t.Fatalf("the app could not answer the card: %v", err)
	}
	if got := answerOf(t, done); got.Allow == nil || *got.Allow {
		t.Fatalf("agent received %+v, want denial from the app", got)
	}
	if n := len(e.f.noticesTo("conv1")); n != 0 {
		t.Fatalf("recovered conversation received %d stale messages", n)
	}
	if n := e.pushesOf(ix.ID); n != 0 {
		t.Fatalf("permission fallback pushed %d times", n)
	}
}

func TestStandalonePermissionResolvedWhileQueued(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.supervisorState(proto.StatusWorking, proto.StatusIdle)
	ix := askPermission("ix_standalone_resolved", "standalone-agent")
	ix.ToolInput = json.RawMessage(`{"command":"git status"}`)
	done := e.raise(ix)
	waitFor(t, "permission queued before resolution", func() bool { return len(queuedRelays(e.s, "conv1")) == 1 })
	if err := e.s.resolve(ix.ID, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, done)
	e.supervisorState(proto.StatusIdle, proto.StatusIdle)
	waitFor(t, "resolved permission removed from the queue", func() bool { return len(queuedRelays(e.s, "conv1")) == 0 })
	if n := len(e.f.noticesTo("conv1")); n != 0 {
		t.Fatalf("main conversation received %d messages about a resolved permission", n)
	}
}

func TestStandalonePermissionGoesStraightToApp(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := "short-lived"
		if private {
			name = "private"
		}
		t.Run(name, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			ix := askPermission("ix_standalone_app", "standalone-agent")
			ix.ToolInput = json.RawMessage(`{"command":"git status"}`)
			ix.Private = private
			if !private {
				ix.ExpiresAt = ix.CreatedAt.Add(superviseTick)
			}
			done := e.raise(ix)
			waitFor(t, "permission shown in its agent's chat", func() bool {
				_, shown := e.shown(ix.ID)
				return shown
			})
			if got := e.lastShown(ix.ID); got.Delegating || got.Private != private {
				t.Fatalf("app received Delegating=%t Private=%t", got.Delegating, got.Private)
			}
			if _, ok := e.s.routedTo(ix.ID, "conv1", true); ok {
				t.Fatal("main conversation can answer a card sent directly to the app")
			}
			if n := e.pushesOf(ix.ID); n != 0 {
				t.Fatalf("card pushed %d times", n)
			}
			if err := e.s.resolve(ix.ID, proto.Resolution{Dismiss: true}); err != nil {
				t.Fatal(err)
			}
			answerOf(t, done)
		})
	}
}

// A connector-gate outward-write permission card pushes to the owner with approval behavior unchanged
// and is recorded in the card ledger as category 3 (outbound_write) rather than category 0 (agent_permission).
// Non-permission cards ignore any spoofed OutwardWrite flag.
func TestConnectorGateOutwardWritePushesToOwnerAndCountsAsCategory3(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	now := time.Now().UTC()

	raiseNode := func(ix proto.Interaction) <-chan proto.Resolution {
		out := make(chan proto.Resolution, 1)
		go func() {
			res, _ := e.s.nodeInteraction(context.Background(), ix)
			out <- res
		}()
		e.waitPendingID(ix.ID)
		return out
	}

	mainOutward := proto.Interaction{
		ID: "ix_gate_main", MachineID: "server", SessionID: "conv1", SessionTitle: "Main",
		Kind: proto.IxPermission, ToolName: "Feishu", Summary: "Send chat message",
		OutwardWrite: true, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	mainDone := raiseNode(mainOutward)
	waitFor(t, "main connector gate outward write pushed", func() bool { return e.pushesOf(mainOutward.ID) == 1 })

	agentOutward := proto.Interaction{
		ID: "ix_gate_agent", MachineID: "mac", SessionID: "standalone-agent", SessionTitle: "Task Chat",
		Kind: proto.IxPermission, ToolName: "Feishu", Summary: "Update group settings",
		OutwardWrite: true, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	agentDone := raiseNode(agentOutward)
	waitFor(t, "agent connector gate outward write pushed", func() bool { return e.pushesOf(agentOutward.ID) == 1 })

	spoofedQuestion := askQuestion("ix_spoofed_q", "standalone-q")
	spoofedQuestion.OutwardWrite = true
	qDone := raiseNode(spoofedQuestion)
	waitFor(t, "non-permission card still routed to main", func() bool {
		return hasRelayFrom(e.s, "conv1", cardAgentQuestion, "interaction_id="+spoofedQuestion.ID)
	})

	e.settle()
	if n := e.pushesOf(spoofedQuestion.ID); n != 0 {
		t.Fatalf("spoofed question pushed %d times", n)
	}
	if got := e.lastShown(spoofedQuestion.ID); got.OutwardWrite {
		t.Fatalf("nodeInteraction did not strip OutwardWrite on non-permission card: %+v", got)
	}
	if hasRelayFrom(e.s, "conv1", cardOutboundWrite, "interaction_id="+agentOutward.ID) {
		t.Fatal("connector gate outward write was diverted from owner")
	}

	waitFor(t, "outbound_write ledger recorded", func() bool {
		return cardsToday(t, e.s)[cardOutboundWrite].N == 2
	})
	today := cardsToday(t, e.s)
	if c := today[cardOutboundWrite]; c.Category != cardCatOutward || c.N != 2 {
		t.Fatalf("outbound_write ledger = %+v, want category %d count 2", c, cardCatOutward)
	}
	if c := today[cardAgentPermission]; c.N != 0 {
		t.Fatalf("agent_permission ledger = %+v, want 0", c)
	}
	report := cardLedgerReport(time.Now().In(e.s.location(context.Background())).Format(time.DateOnly),
		e.s.cardsOn(context.Background(), time.Now().In(e.s.location(context.Background())).Format(time.DateOnly)))
	if !strings.Contains(report, "3 outward as the owner: 2 — outbound_write×2\n") || !strings.Contains(report, "None of the six (each one a bug): 0.") {
		t.Fatalf("unexpected ledger report:\n%s", report)
	}

	allow := true
	if err := e.s.resolve(mainOutward.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, mainDone)
	if err := e.s.resolve(agentOutward.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, agentDone)
	if err := e.s.resolve(spoofedQuestion.ID, proto.Resolution{Answers: map[string]string{spoofedQuestion.Questions[0].Question: "main"}}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, qDone)
}

// Antigravity's ACP permission title and Skill instruction arguments are local data, not evidence
// of an outward action. Their sanitized production shapes take the non-owner route without a push
// or an agent_permission ledger entry; form/secret/owner-proposal precedence is pinned in TestUserApproval.
func TestLocalNonActionPermissionsUseNonOwnerRoute(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
	}{
		{"antigravity draft mentioning deletion", proto.Interaction{
			ID: "ix_local_agy_delete", MachineID: "mac", SessionID: "agy-local-draft", Kind: proto.IxPermission,
			ToolName: "File /work/draft.md", ToolInput: []byte(`{"TargetFile":"/work/draft.md","CodeContent":"删除该字段"}`), // i18n-ignore: synthetic Antigravity file payload
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
		{"antigravity draft mentioning a comment", proto.Interaction{
			ID: "ix_local_agy_comment", MachineID: "mac", SessionID: "agy-local-review", Kind: proto.IxPermission,
			ToolName: "File /work/review.md", ToolInput: []byte(`{"Path":"/work/review.md","Content":"Replay the comment"}`),
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
		{"Skill instructions with a negated send", proto.Interaction{
			ID: "ix_local_skill", MachineID: "mac", SessionID: "standalone-skill", Kind: proto.IxPermission,
			ToolName: "Skill", ToolInput: []byte(`{"skill":"agent-browser","args":"只读检查、不发送邮件、不改设置"}`), // i18n-ignore: synthetic skill arguments
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			if got := userApproval(tc.ix); got != "" {
				t.Fatalf("local non-action is owner-only: %s", got)
			}
			answered := e.raise(tc.ix)
			e.noticeWith("conv1", "interaction_id="+tc.ix.ID)
			e.settle()
			if n := e.pushesOf(tc.ix.ID); n != 0 {
				t.Fatalf("local non-action pushed to the owner %d times", n)
			}
			if c := cardsToday(t, e.s)[cardAgentPermission]; c.N != 0 {
				t.Fatalf("agent_permission ledger = %+v, want 0", c)
			}
			allow := true
			if err := e.s.resolve(tc.ix.ID, proto.Resolution{Allow: &allow}); err != nil {
				t.Fatal(err)
			}
			answerOf(t, answered)
		})
	}
}

// Ordinary supervisor permissions wake an independent reviewer, including main's
// own permission; the reviewer answers as the assistant and never as the user.
func TestSupervisingConversationsRouteTheirOwnInternalCards(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor = "bg1" })
	for _, source := range []string{"lead1", "conv1", "bg1"} {
		ix := askPermission("ix_own_"+source, source)
		ix.MachineID = "server"
		ix.ToolInput = json.RawMessage(`{"command":"ls /tmp | head"}`)
		done := e.raise(ix)
		to := "conv1"
		if source == "conv1" {
			to = "bg1"
		}
		waitFor(t, "independent reviewer", func() bool {
			return hasRelayFrom(e.s, to, cardAgentPermission, "interaction_id="+ix.ID)
		})
		no := false
		if _, err := runJSON(t, e.s, to, "interaction_answer", map[string]any{"machine": "server", "session_id": source,
			"interaction_id": ix.ID, "allow": no, "reason": "Reviewed the read-only command"}); err != nil {
			t.Fatal(err)
		}
		got := answerOf(t, done)
		if got.Allow == nil || *got.Allow || got.AnsweredBy != proto.AnsweredByAssistant {
			t.Fatalf("answer = %+v", got)
		}
		if e.pushesOf(ix.ID) != 0 {
			t.Fatal("internal supervisor permission pushed to owner")
		}
	}
}

// Source 2 (agent_question): non-six-class questions and parsed non-user terminal dialogs from an
// unsupervised agent go to the main conversation without pushing to the owner, while six-class cards
// (elicitation/form, folder trust, delete risk) still push to the owner.
func TestAgentQuestionNonOwnerRoutedAndSixClassesStillPush(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	now := time.Now().UTC()

	// Unsupervised ordinary IxQuestion -> main conversation, no push.
	q := askQuestion("ix_q_unsupervised", "standalone-q")
	qDone := e.raise(q)
	waitFor(t, "unsupervised question routed to main", func() bool {
		return hasRelayFrom(e.s, "conv1", cardAgentQuestion, "interaction_id="+q.ID)
	})
	for _, msg := range laterFor(e.s, "conv1") {
		if msg.From == cardAgentQuestion && !msg.Internal {
			t.Fatal("the routed question relay is not marked internal")
		}
	}

	// Unsupervised ordinary parsed IxScreen (e.g. default permission mode prompt) -> main conversation, no push.
	scr := proto.Interaction{
		ID: "scr_auto_mode", MachineID: "mac", SessionID: "standalone-scr",
		Kind: proto.IxScreen, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Dialog: &screen.Dialog{
			Question: "Make auto mode your default permission mode?",
			Rev:      "r1",
			Options: []screen.Option{
				{Key: "1", Label: "Yes, set auto mode", Keys: []string{"enter"}},
				{Key: "2", Label: "No, keep manual mode", Keys: []string{"down", "enter"}},
			},
		},
	}
	scrDone := e.raise(scr)
	waitFor(t, "unsupervised screen routed to main", func() bool {
		return hasRelayFrom(e.s, "conv1", cardAgentQuestion, "interaction_id="+scr.ID)
	})

	// Six-class IxScreen (folder trust) -> still pushed to the owner.
	trust := proto.Interaction{
		ID: "scr_trust_folder", MachineID: "mac", SessionID: "standalone-trust",
		Kind: proto.IxScreen, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Dialog: &screen.Dialog{
			Question: "Quick safety check: Is this a project you trust?",
			Rev:      "r2",
			Options: []screen.Option{
				{Key: "1", Label: "Yes, I trust this folder", Keys: []string{"enter"}},
				{Key: "2", Label: "No, exit", Keys: []string{"down", "enter"}},
			},
		},
	}
	trustDone := e.raise(trust)
	waitFor(t, "folder trust screen pushed to owner", func() bool { return e.pushesOf(trust.ID) == 1 })

	// Six-class IxElicitation -> still pushed to the owner when elicitation clients are reachable.
	e.s.fleet.mu.Lock()
	e.s.fleet.clients.Elicitation = true
	e.s.fleet.mu.Unlock()
	elicit := proto.Interaction{
		ID: "ix_elicit", MachineID: "mac", SessionID: "standalone-elicit",
		Kind: proto.IxElicitation, Summary: "Sign in form",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	elicitDone := e.raise(elicit)
	waitFor(t, "elicitation pushed to owner", func() bool { return e.pushesOf(elicit.ID) == 1 })

	e.settle()
	if n := e.pushesOf(q.ID); n != 0 {
		t.Fatalf("unsupervised non-owner question pushed %d times", n)
	}
	if n := e.pushesOf(scr.ID); n != 0 {
		t.Fatalf("unsupervised non-owner screen pushed %d times", n)
	}

	for _, pair := range []struct {
		id  string
		res proto.Resolution
		ch  <-chan proto.Resolution
	}{
		{q.ID, proto.Resolution{Answers: map[string]string{"用哪个分支？": "main"}}, qDone},
		{scr.ID, proto.Resolution{Option: "2"}, scrDone},
		{trust.ID, proto.Resolution{Option: "2"}, trustDone},
		{elicit.ID, proto.Resolution{Action: "decline"}, elicitDone},
	} {
		if err := e.s.resolve(pair.id, pair.res); err != nil {
			t.Fatal(err)
		}
		answerOf(t, pair.ch)
	}
}

// Source 3 (agent_reply): reply reminders go to the task's supervisor (when supervised) or to the
// main conversation (when unsupervised), and never push to the owner.
func TestAgentReplyGoesToSupervisorOrMainNotOwner(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	ended := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)

	supervised := proto.Session{
		SessionID: "worker-1", Title: "受督办任务", Cwd: "/code/app",
		Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: ended,
		Asks: "要继续跑全量吗？", AwaitsReply: true,
	}
	unsupervised := proto.Session{
		SessionID: "standalone-reply", Title: "独立对话", Cwd: "/code/other",
		Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: ended,
		Asks: "选方案 A 还是 B？", AwaitsReply: true,
	}
	if err := e.s.remindReply("mac", supervised); err != nil {
		t.Fatal(err)
	}
	if err := e.s.remindReply("mac", unsupervised); err != nil {
		t.Fatal(err)
	}

	if !hasRelayFrom(e.s, "lead1", cardAgentReply, "session_id=worker-1") {
		t.Fatalf("supervised reply reminder did not go to supervisor lead1: %+v", laterFor(e.s, "lead1"))
	}
	if !hasRelayFrom(e.s, "conv1", cardAgentReply, "session_id=standalone-reply") {
		t.Fatalf("unsupervised reply reminder did not go to main conv1: %+v", laterFor(e.s, "conv1"))
	}
	for _, n := range e.f.pushed() {
		if n.Kind == assistantwire.NotifyReply {
			t.Fatalf("agent_reply was still pushed to the owner: %+v", n)
		}
	}
	if c := cardsToday(t, e.s)[cardAgentReply]; c.N != 0 {
		t.Fatalf("agent_reply ledger = %+v, want 0", c)
	}
}

// Source 4 (extension_approval): non-six-class extension approvals (such as internal maintenance tools)
// go to the main conversation without pushing to the owner, while six-class extension approvals
// (outbound writes such as mail_send, or secret/form cards) still push to the owner.
func TestExtensionApprovalNonOwnerRoutedAndSixClassesStillPush(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	now := time.Now().UTC()

	// Non-six-class extension approval (like enkeep_rerun) -> main conversation, no owner push.
	extIx := proto.Interaction{
		ID: "ix_ext_rerun", MachineID: "mac", SessionID: "",
		Kind: proto.IxPermission, ToolName: "enkeep_rerun", SessionTitle: "Persona",
		Summary: "Rerun extraction for 2026-10-27", ToolInput: json.RawMessage(`{"from":"2026-10-27"}`),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	extDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := e.s.askHub(context.Background(), extIx)
		extDone <- r
	}()
	waitFor(t, "non-owner extension approval routed to main", func() bool {
		return hasRelayFrom(e.s, "conv1", cardExtensionApproval, "interaction_id="+extIx.ID)
	})

	// Six-class outbound extension approval (mail_send, kind 3) -> still pushed to the owner.
	mailIx := proto.Interaction{
		ID: "ix_ext_mail", MachineID: "mac", SessionID: "",
		Kind: proto.IxPermission, ToolName: "mail_send", SessionTitle: "Persona",
		Summary: "Send email to external recipient", ToolInput: json.RawMessage(`{"to":["a@example.com"]}`),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	mailDone := make(chan proto.Resolution, 1)
	go func() {
		r, _ := e.s.askHub(context.Background(), mailIx)
		mailDone <- r
	}()
	waitFor(t, "outbound mail_send pushed to owner", func() bool { return e.pushesOf(mailIx.ID) == 1 })

	e.settle()
	if n := e.pushesOf(extIx.ID); n != 0 {
		t.Fatalf("non-owner extension approval pushed %d times", n)
	}
	waitFor(t, "outbound_write ledger recorded", func() bool {
		return cardsToday(t, e.s)[cardOutboundWrite].N == 1
	})
	if c := cardsToday(t, e.s)[cardExtensionApproval]; c.N != 0 {
		t.Fatalf("extension_approval ledger = %+v, want 0", c)
	}

	allow := true
	if err := e.s.resolve(extIx.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, extDone)
	if err := e.s.resolve(mailIx.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, mailDone)
}

// Source 5 (schedule_failed): failed non-briefing scheduled runs go to their originating supervisor
// or the main conversation instead of pushing to the owner, while a briefing fallback (a six-class
// owner-subscribed briefing) still pushes to the owner.
func TestScheduleFailedGoesToSupervisorOrMainAndBriefFallbackStillPushes(t *testing.T) {
	s, f := newQuietService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	ctx := context.Background()

	// Unowned one-off follow-up fails -> main conversation (conv1), no owner push.
	s.oneOffFailed(ctx,
		Task{ID: "fu_main", Title: "Check batch", Repeat: repeatOnce},
		ScheduleRun{ID: "run_1", Error: "machine offline"},
	)
	if !hasRelayFrom(s, "conv1", cardScheduleFailed, "fu_main") {
		t.Fatalf("unowned schedule_failed did not go to main conv1: %+v", laterFor(s, "conv1"))
	}

	// Leader-owned one-off follow-up fails -> leader (lead1), no owner push.
	s.oneOffFailed(ctx,
		Task{ID: "fu_lead", Title: "Leader patrol", Repeat: repeatOnce, SessionID: "lead1"},
		ScheduleRun{ID: "run_2", Error: "upstream 503"},
	)
	if !hasRelayFrom(s, "lead1", cardScheduleFailed, "fu_lead") {
		t.Fatalf("leader schedule_failed did not go to lead1: %+v", laterFor(s, "lead1"))
	}

	// Verify no owner push and card_ledger has 0 schedule_failed.
	if p := f.pushed(); len(p) != 0 {
		t.Fatalf("schedule_failed still pushed to the owner: %+v", p)
	}
	if c := cardsToday(t, s)[cardScheduleFailed]; c.N != 0 {
		t.Fatalf("schedule_failed ledger = %+v, want 0", c)
	}

	// Six-class briefing fallback (owner-subscribed briefing failed to start) still pushes.
	briefTask := Task{ID: "brief_1", Title: "Morning briefing", Repeat: repeatDaily, Facts: true}
	now := time.Now()
	s.briefReportStart(ctx, briefTask, now)
	s.briefNotStarted(ctx, briefTask,
		ScheduleRun{ID: "run_3", RefID: briefTask.ID, Due: now, Error: "machine offline"},
		false,
	)
	if !s.modules.Loaded("briefing") {
		if c := cardsToday(t, s)[cardBriefFallback]; c.N != 0 || len(f.pushed()) != 0 || s.briefTaskRunCount() != 0 {
			t.Fatalf("briefing hooks without their module produced a fallback: ledger=%+v pushes=%d runs=%d", c, len(f.pushed()), s.briefTaskRunCount())
		}
		return
	}
	waitFor(t, "brief_fallback pushed to owner", func() bool {
		return cardsToday(t, s)[cardBriefFallback].N == 1
	})
}
