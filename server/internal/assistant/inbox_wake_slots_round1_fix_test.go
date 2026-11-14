package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

func wakeSlotsRound1FixTestDeferredNotes(it *InboxItem) []inboxNote {
	var out []inboxNote
	if it.Agent != nil {
		for _, n := range it.Agent.Pending {
			if n.Deferred {
				out = append(out, n)
			}
		}
	}
	return out
}

// A current Agent whose settled stop is waiting for capacity is not still running. The sweep must leave both its
// handling state and its complete notification alone until the ordinary admission path delivers them.
func TestInboxWakeSlotsRound1FixDeferredSettledStopSurvivesSweep(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1, TimeoutMinutes: 1}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent = cfg })
	busy := wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-round1-sweep-busy")
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not recorded")
	}
	started := time.Now().Add(-5 * time.Minute)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()

	h.sweepInboxAgents(ctx, cfg, time.Now())

	h.inbox.mu.Lock()
	state := it.Handling.State
	deferredAt := it.Agent.DeferredAt
	deferred := wakeSlotsRound1FixTestDeferredNotes(it)
	kinds := chainKinds(it)
	h.inbox.mu.Unlock()
	if state != HandlingAgent || deferredAt == nil || len(deferred) != 1 || deferred[0].Name != prompts.NoteAgentStopped {
		t.Fatalf("the sweep settled or damaged a held stop: handling %q, deferred_at %v, notes %+v", state, deferredAt, deferred)
	}
	for _, kind := range kinds {
		if kind == ChainFailed {
			t.Fatalf("a known-settled deferred stop timed out: %v", kinds)
		}
	}

	wakeSlotsAgentTestRelease(t, h, busy)
	h.checkInboxFollowUps(ctx, cfg, time.Now())
	h.deliverPending(ctx)
	toldConv(t, h, sess.LastText)
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != 1 {
		t.Fatalf("the held stop was delivered %d times after capacity freed, want once", got)
	}
}

// Selective delivery can discover a missing processing conversation while sending only an informational note. The
// deferred wake still needs a replacement conversation, but its first message waits for the same capacity admission.
func TestInboxWakeSlotsRound1FixMissingChatRecoveryCarriesDeferredWake(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent = cfg })
	busy := wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-round1-recovery-busy")
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not recorded")
	}
	const informational = "Synthetic informational reply"
	h.inbox.mu.Lock()
	it.Recap = "Synthetic recovery recap"
	h.inbox.tell(it, inboxNote{Name: prompts.NoteSent, Text: informational}, false, time.Now())
	h.inbox.mu.Unlock()
	h.f.setSessions("server")
	h.f.mu.Lock()
	h.f.failSend = fmt.Errorf("no such session")
	h.f.mu.Unlock()

	h.deliverNotes(ctx, it)

	h.inbox.mu.Lock()
	a := it.Agent
	deferred := wakeSlotsRound1FixTestDeferredNotes(it)
	if a.SessionID != "" || a.State != AgentQueued || !a.Wake || a.DeferredAt == nil || !a.lost("s1") ||
		len(deferred) != 1 || deferred[0].Name != prompts.NoteAgentStopped {
		h.inbox.mu.Unlock()
		t.Fatalf("gone-chat recovery lost the held wake: agent %+v, deferred notes %+v", a, deferred)
	}
	h.inbox.mu.Unlock()

	starts := h.f.made
	h.f.mu.Lock()
	h.f.failSend = nil
	h.f.mu.Unlock()
	h.startInboxAgents(ctx, cfg)
	if got := h.f.made; got != starts {
		t.Fatalf("recovery bypassed the occupied slot: starts %d -> %d", starts, got)
	}

	wakeSlotsAgentTestRelease(t, h, busy)
	h.startInboxAgents(ctx, cfg)
	if got := h.f.made; got != starts+1 {
		t.Fatalf("recovery starts after capacity freed = %d, want %d", got, starts+1)
	}
	h.inbox.mu.Lock()
	a = it.Agent
	left := append([]inboxNote(nil), a.Pending...)
	deferredAt := it.Agent.DeferredAt
	session := a.SessionID
	h.inbox.mu.Unlock()
	if session == "" || deferredAt != nil || len(left) != 0 {
		t.Fatalf("the replacement conversation did not carry the held work: session %q, deferred_at %v, pending %+v", session, deferredAt, left)
	}
	first := h.f.lastSend(t)["text"].(string)
	for _, want := range []string{"Synthetic recovery recap", informational, sess.LastText} {
		if !strings.Contains(first, want) {
			t.Errorf("the replacement conversation's first message lacks %q:\n%s", want, first)
		}
	}
}

// Confirming a reply, confirming an Agent and replying directly all queue informational notes without asking for a
// turn. None of them may turn an older deferred source wake into ready delivery or discard its queue position.
func TestInboxWakeSlotsRound1FixNonWakingRepliesPreserveDeferredWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *inboxHub, *InboxItem)
		act     func(*testing.T, *inboxHub, *InboxItem) (int, map[string]any)
	}{
		{
			name: "confirm reply",
			prepare: func(t *testing.T, h *inboxHub, it *InboxItem) {
				if _, err := suggestTool(t, h, `{"kind":"reply","text":"Synthetic confirmed reply"}`); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, h *inboxHub, it *InboxItem) (int, map[string]any) {
				return suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
			},
		},
		{
			name: "confirm Agent",
			prepare: func(t *testing.T, h *inboxHub, it *InboxItem) {
				if _, err := suggestTool(t, h, `{"kind":"agent","text":"Synthetic confirmed Agent instruction"}`); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, h *inboxHub, it *InboxItem) (int, map[string]any) {
				return suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
			},
		},
		{
			name: "direct reply",
			prepare: func(t *testing.T, h *inboxHub, it *InboxItem) {
				h.inbox.mu.Lock()
				h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
				h.inbox.mu.Unlock()
			},
			act: func(t *testing.T, h *inboxHub, it *InboxItem) (int, map[string]any) {
				return inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"Synthetic direct reply"}`)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, _, it := handledHub(t)
			h.inbox.handing.Lock()
			t.Cleanup(h.inbox.handing.Unlock)
			tc.prepare(t, h, it)
			wakeSlotsBypassTestFillCapacity(t, h)
			held := inboxNote{Name: prompts.NoteSelfRead}
			h.inbox.mu.Lock()
			h.inbox.queueInboxWake(it, held, false, time.Now())
			h.inbox.mu.Unlock()

			code, out := tc.act(t, h, it)
			if code != 200 {
				t.Fatalf("non-waking action: %d %v", code, out)
			}
			h.deliverPending(ctx)

			h.inbox.mu.Lock()
			deferredAt := it.Agent.DeferredAt
			wake := it.Agent.Wake
			deferred := wakeSlotsRound1FixTestDeferredNotes(it)
			pending := append([]inboxNote(nil), it.Agent.Pending...)
			h.inbox.mu.Unlock()
			if deferredAt == nil || wake || len(deferred) != 1 || deferred[0].Name != prompts.NoteSelfRead || len(pending) != 1 {
				t.Fatalf("the informational action consumed held work: deferred_at %v, wake %v, pending %+v", deferredAt, wake, pending)
			}
			readyOnly := renderNote(held, h.owner(ctx))
			if sent := strings.Join(h.f.sentToAgents(), "\n"); strings.Contains(sent, readyOnly) {
				t.Fatalf("the deferred source note was delivered without admission:\n%s", sent)
			}
		})
	}
}

// A source wake admitted in a pass owns that pass's slot even when the matter already made three empty checks. The
// terminal no-capacity path must not subsequently mark the same admitted turn done and release its reservation.
func TestInboxWakeSlotsRound1FixSourceAdmissionSkipsTerminalDueCheck(t *testing.T) {
	ctx := context.Background()
	h, it, now := followCheckHub(t, inboxFollowUpAskChecks)
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent = cfg })
	queued := actionItem("wake-slots-round1-terminal-queued", now)
	queued.Agent = &InboxAgent{State: AgentQueued, Task: "Inspect the synthetic queued matter", Of: queued.Last}
	h.inbox.mu.Lock()
	h.inbox.items[queued.ID] = queued
	h.inbox.queueInboxWake(it, inboxNote{Name: prompts.NoteSelfRead}, false, now)
	beforeKinds := append([]string(nil), chainKinds(it)...)
	h.inbox.mu.Unlock()

	h.checkInboxFollowUps(ctx, cfg, now)
	h.startInboxAgents(ctx, cfg)

	state, wake, _ := handlingNow(h, it)
	h.inbox.mu.Lock()
	running := inboxConversationRunning(it)
	f := it.Follow
	kinds := append([]string(nil), chainKinds(it)...)
	h.inbox.mu.Unlock()
	if state != HandlingLooking || !wake || !running || h.f.made != 0 {
		t.Fatalf("the admitted source wake lost its slot: handling %q, wake %v, running %v, starts %d", state, wake, running, h.f.made)
	}
	if f == nil || f.Checks != inboxFollowUpAskChecks || f.CheckAt == nil || f.Due {
		t.Fatalf("source admission ran the terminal check transition: %+v", f)
	}
	if len(kinds) != len(beforeKinds) {
		t.Fatalf("source admission added a terminal check step: before %v, after %v", beforeKinds, kinds)
	}
}
