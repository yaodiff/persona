package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func sensitiveDelegationEnv(t *testing.T) *routeEnv {
	t.Helper()
	e := newRouteEnv(t, nil, true)
	e.f.infoTagModel = true
	e.f.fakeModel(t)
	e.s.noInfoTag = false
	e.f.mu.Lock()
	e.f.infoTag = `{"categories":["legal_id"]}`
	e.f.mu.Unlock()
	rows := []proto.Session{
		{SessionID: "conv1", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "lead1", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "side-chat", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
	}
	e.sessions("server", rows...)
	e.f.setSessions("server", rows...)
	return e
}

func TestSensitiveOwnedAgentDelegationWaitsForSupervisor(t *testing.T) {
	e := sensitiveDelegationEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		ok, _, err := e.s.confirmOutbound(ctx, "server", "side-chat", "agent_send", "synthetic sensitive identity text",
			"Delegate verification", json.RawMessage(`{"session_id":"owned-agent","text":"synthetic sensitive identity text"}`))
		done <- ok && err == nil
	}()
	var ix proto.Interaction
	waitFor(t, "sensitive internal gate", func() bool {
		for _, q := range e.s.pendingQuestions() {
			if q.ToolName == "agent_send" {
				ix = q
				_, up := e.s.pendingIx(ix.ID)
				return up
			}
		}
		return false
	})
	e.noticeWith("conv1", "interaction_id="+ix.ID)
	select {
	case <-done:
		t.Fatal("sensitive delegation passed without approval")
	default:
	}
	if len(e.f.agentSent) != 0 || e.pushesOf(ix.ID) != 0 {
		t.Fatal("unapproved content was sent or owner was pushed")
	}
	for _, notice := range e.f.noticesTo("conv1") {
		if strings.Contains(notice, "synthetic sensitive identity text") {
			t.Fatal("reviewer wake-up leaked the content instead of the purpose")
		}
	}
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "server", "session_id": "side-chat",
		"interaction_id": ix.ID, "allow": true, "reason": "Existing task authorizes this owned Agent delegation"}); err != nil {
		t.Fatal(err)
	}
	if !<-done {
		t.Fatal("explicit supervisor approval did not release the gate")
	}
}

func TestInternalOutboundResolutionCannotReplaceOwnerConsent(t *testing.T) {
	allow := true
	ix := proto.Interaction{ToolName: "agent_send", ExpiresAt: time.Now().Add(time.Minute)}
	for _, tc := range []struct {
		name string
		r    proto.Resolution
		want bool
	}{
		{"owner", proto.Resolution{Allow: &allow}, true},
		{"supervisor", proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant}, true},
		{"default", proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByDefault}, false},
		{"late", proto.Resolution{Allow: &allow, Late: true}, false},
		{"cancelled", proto.Resolution{Allow: &allow, Cancel: true}, false},
		{"remembered", proto.Resolution{Allow: &allow, Remembered: true}, false},
		{"expired", proto.Resolution{Allow: &allow, Dismiss: true, Reason: proto.DismissedExpired}, false},
	} {
		if got := validInternalAnswer(ix, tc.r); got != tc.want {
			t.Fatalf("%s = %t, want %t", tc.name, got, tc.want)
		}
	}
	ix.OwnerProposal = true
	if validInternalAnswer(ix, proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant}) {
		t.Fatal("supervisor replaced consent for an isolated data boundary")
	}
}

func TestInternalOutboundApprovalIsScopedToCurrentBoundary(t *testing.T) {
	for _, mode := range []string{"target", "track_after", "track_waiting", "source_lost"} {
		t.Run(mode, func(t *testing.T) {
			e := sensitiveDelegationEnv(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			type gateResult struct {
				Allowed bool
				Outcome string
				Failure error
			}
			start := func(callCtx context.Context, target string) <-chan gateResult {
				done := make(chan gateResult, 1)
				input, err := json.Marshal(map[string]string{"session_id": target, "text": "synthetic sensitive identity text"})
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					ok, outcome, err := e.s.confirmOutbound(callCtx, "server", "side-chat", "agent_send",
						"synthetic sensitive identity text", "Same display label", input)
					done <- gateResult{ok, outcome, err}
				}()
				return done
			}
			card := func() proto.Interaction {
				var ix proto.Interaction
				waitFor(t, "fresh outbound approval", func() bool {
					for _, q := range e.s.pendingQuestions() {
						if _, up := e.s.pendingIx(q.ID); up && q.ToolName == "agent_send" {
							ix = q
							return true
						}
					}
					return false
				})
				return ix
			}
			changeSource := func(space string, present bool) {
				rows := []proto.Session{
					{SessionID: "conv1", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
					{SessionID: "lead1", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
				}
				if present {
					rows = append(rows, proto.Session{SessionID: "side-chat", Cwd: assistantHome, Space: space, Assistant: true, Status: proto.StatusIdle})
				}
				e.sessions("server", rows...)
				e.f.setSessions("server", rows...)
			}
			callCtx := ctx
			if mode == "track_waiting" || mode == "source_lost" {
				callCtx = context.WithValue(ctx, callerSpaceSnapshotKey{}, callerSpaceSnapshot{
					machine: "server", session: "side-chat", row: proto.Session{SessionID: "side-chat", Cwd: assistantHome}, known: true,
				})
			}
			done := start(callCtx, "owned-agent-one")
			ix := card()
			e.noticeWith("conv1", "interaction_id="+ix.ID)
			if mode == "track_waiting" || mode == "source_lost" {
				changeSource(SpaceHealth, mode != "source_lost")
			}
			if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{
				"machine": "server", "session_id": "side-chat", "interaction_id": ix.ID,
				"allow": true, "reason": "Existing task authorizes this delegation",
			}); err != nil {
				t.Fatal(err)
			}
			first := <-done
			changed := mode == "track_waiting" || mode == "source_lost"
			if first.Failure != nil || first.Allowed == changed || changed && first.Outcome != proto.OutcomeUnanswered {
				t.Fatalf("first approval after boundary change=%t: %+v", changed, first)
			}
			if mode == "track_after" {
				changeSource(SpaceHealth, true)
			}
			target := "owned-agent-one"
			if mode == "target" {
				target = "owned-agent-two" // identical label and words, a different actual target
			}
			done = start(ctx, target)
			next := card()
			if next.ID == ix.ID {
				t.Fatal("old approval occurrence was reused")
			}
			if mode == "target" {
				e.noticeWith("conv1", "interaction_id="+next.ID)
				if e.pushesOf(next.ID) != 0 {
					t.Fatal("new internal target pushed to owner")
				}
			} else {
				waitFor(t, "changed boundary asks the owner", func() bool { return e.pushesOf(next.ID) == 1 })
				if !next.OwnerProposal || e.s.internalCard(next) || e.s.inputCardSource(next, false) != cardSecretForm {
					t.Fatal("old internal approval replaced owner privacy consent")
				}
			}
			if mode == "target" {
				if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{
					"machine": "server", "session_id": "side-chat", "interaction_id": next.ID,
					"allow": false, "reason": "The second target is not authorized",
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				deny := false
				if err := e.s.resolve(next.ID, proto.Resolution{Allow: &deny}); err != nil {
					t.Fatal(err)
				}
			}
			if second := <-done; second.Allowed || second.Failure != nil {
				t.Fatalf("refused second proposal was authorized: %+v", second)
			}
		})
	}
}

func TestSensitiveAgentTargetBoundaryIsRecheckedAfterApproval(t *testing.T) {
	e := sensitiveDelegationEnv(t)
	rows := []proto.Session{{SessionID: "worker-1", Cwd: "/code/app", Status: proto.StatusBlocked}}
	e.sessions("mac", rows...)
	e.f.setSessions("mac", rows...)
	done := make(chan error, 1)
	go func() {
		_, err := runJSON(t, e.s, "side-chat", "agent_send", map[string]any{
			"machine": "Mac", "session_id": "worker-", "text": "synthetic sensitive identity text",
		})
		done <- err
	}()
	var ix proto.Interaction
	waitFor(t, "internal send waits on approval", func() bool {
		for _, q := range e.s.pendingQuestions() {
			if _, up := e.s.pendingIx(q.ID); up && q.ToolName == "agent_send" {
				ix = q
				return true
			}
		}
		return false
	})
	e.noticeWith("conv1", "interaction_id="+ix.ID)
	if !strings.Contains(string(ix.ToolInput), `"machine":"mac"`) || !strings.Contains(string(ix.ToolInput), `"session_id":"worker-1"`) {
		t.Fatal("approval did not bind the resolved target address")
	}
	rows[0].Space = SpaceHealth
	e.sessions("mac", rows...)
	e.f.setSessions("mac", rows...)
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{
		"machine": "server", "session_id": "side-chat", "interaction_id": ix.ID,
		"allow": true, "reason": "Original task authorized the original target boundary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("target moved across a privacy boundary while approval waited, but send succeeded")
	}
	if len(e.f.agentSent) != 0 || e.pushesOf(ix.ID) != 0 {
		t.Fatal("changed target received sensitive content or original internal gate pushed to owner")
	}
}
