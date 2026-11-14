package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A card about a turn whose session is no longer on its computer does not stay among what needs the user (#6709): a
// Paseo agent's id moved to a fork (before the bridge kept it), or the session was deleted there. Each "continue" in
// Paseo's app after an API error made one more such card, none of which could be answered.

// modelErrorCard triages sid's turn, cut off by a model error the user must see to, and returns its card.
func modelErrorCard(t *testing.T, s *Service, f *fakeHub, sid string, others ...proto.Session) proto.Interaction {
	t.Helper()
	f.setSessions("box", append([]proto.Session{apiErrorTurn(sid, "API Error: 499 status code (no body)")}, others...)...)
	triage(t, s, sid)
	for _, ix := range s.workCards() {
		if ix.SessionID == sid {
			return ix
		}
	}
	t.Fatalf("no card for %s: %+v", sid, entryOf(t, s, sid))
	return proto.Interaction{}
}

func workEventsOf(s *Service, kind string) []map[string]any {
	var out []map[string]any
	for _, e := range testEvents(s) {
		if e.Kind == kind {
			out = append(out, e.Detail)
		}
	}
	return out
}

func TestCardOfASessionGoneFromItsComputerIsWithdrawn(t *testing.T) {
	s, f := workService(t)
	modelErrorCard(t, s, f, "s1")
	ctx := context.Background()
	k := [2]string{"box", "s1"}
	tick := func(at time.Time) { s.cardTick(ctx, s.newWorkView(ctx), k, at) }
	now := turnEnd.Add(time.Minute) // within the card's day

	// Gone from the list (Paseo moved it to a fork, or it was deleted there): kept for a while, as a node restarting
	// or an engine coming back lists its sessions again.
	f.setSessions("box")
	tick(now)
	tick(now.Add(workCardGoneAfter / 2))
	if len(s.workCards()) != 1 {
		t.Fatal("withdrawn before its session could come back")
	}
	// Back in the list: the wait starts over.
	f.setSessions("box", apiErrorTurn("s1", "API Error: 499 status code (no body)"))
	tick(now.Add(workCardGoneAfter * 3 / 4))
	f.setSessions("box")
	tick(now.Add(workCardGoneAfter))
	if len(s.workCards()) != 1 {
		t.Fatal("the wait did not start over once the session was listed again")
	}
	// Its machine away says nothing of its sessions: the wait starts over again.
	f.devices = []Device{{ID: "box", Name: "box"}}
	tick(now.Add(workCardGoneAfter * 2))
	f.devices = nil
	tick(now.Add(workCardGoneAfter * 2))
	if len(s.workCards()) != 1 {
		t.Fatal("a machine away counted as the session gone")
	}
	tick(now.Add(workCardGoneAfter*3 + time.Second))
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("still up: %+v", cards)
	}
	if e := entryOf(t, s, "s1"); e.InteractionID != "" || e.Work.InteractionID != "" {
		t.Fatalf("the entry still names the card: %+v", e)
	}
	if got := workEventsOf(s, "withdrawn"); len(got) != 1 || got[0]["by"] != "gone" {
		t.Fatalf("withdrawn events %v", got)
	}
	if len(f.sentToAgents()) != 0 {
		t.Fatal("something was sent to the agent")
	}
}

func TestCardReadBackForAGoneSessionGoesToo(t *testing.T) {
	s, f := workService(t)
	card := modelErrorCard(t, s, f, "s1")
	// The manager restarted (the hub kept no card up): read back, not out until its machine lists it again.
	s.work.mu.Lock()
	delete(s.work.cards, card.ID)
	s.work.mu.Unlock()
	f.setSessions("box")
	ctx := context.Background()
	k := [2]string{"box", "s1"}
	now := turnEnd.Add(time.Minute)
	s.cardTick(ctx, s.newWorkView(ctx), k, now)
	if e := entryOf(t, s, "s1"); e.InteractionID != card.ID {
		t.Fatalf("dropped at once: %+v", e)
	}
	s.cardTick(ctx, s.newWorkView(ctx), k, now.Add(workCardGoneAfter))
	if e := entryOf(t, s, "s1"); e.InteractionID != "" || len(s.workCards()) != 0 {
		t.Fatalf("kept for a session that is gone: %+v", e)
	}
	if got := workEventsOf(s, "withdrawn"); len(got) != 1 || got[0]["by"] != "gone" {
		t.Fatalf("withdrawn events %v", got)
	}
}

func TestAnswerToASessionItsComputerNoLongerHasWithdrawsTheCard(t *testing.T) {
	s, f := workService(t)
	card := modelErrorCard(t, s, f, "s1", stopped("s2", "x"))
	q := card.Questions[0]
	// "Fixed, go on": the node (its runtime) has no such session: no card put up again under a new id.
	f.failSend = &rpc.Error{Code: "not_found", Message: "Paseo has no such session (s1): it may have been archived or deleted"}
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{q.Question: q.Options[0].Label}})
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("put up again: %+v", cards)
	}
	if e := entryOf(t, s, "s1"); e.InteractionID != "" {
		t.Fatalf("the entry still names a card: %+v", e)
	}
	if got := workEventsOf(s, "answer_failed"); len(got) != 1 || got[0]["gone"] != true {
		t.Fatalf("answer_failed events %v", got)
	}
	// A machine that is away is no session gone: that card goes up again, to be answered once it is back.
	other := modelErrorCard(t, s, f, "s2", apiErrorTurn("s1", "x"))
	f.failSend = errors.New("box: offline")
	q = other.Questions[0]
	answerCard(t, s, other.ID, proto.Resolution{Answers: map[string]string{q.Question: q.Options[0].Label}})
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID == other.ID || cards[0].SessionID != "s2" {
		t.Fatalf("cards %+v", cards)
	}
	// "Leave it" never needs the session: the card goes, nothing is sent.
	f.failSend = &rpc.Error{Code: "not_found", Message: "no such session"}
	now := s.workCards()[0]
	answerCard(t, s, now.ID, proto.Resolution{Answers: map[string]string{q.Question: q.Options[1].Label}})
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("left: %+v", cards)
	}
}

func TestALaterTurnOfTheSameSessionReplacesItsCard(t *testing.T) {
	s, f := workService(t)
	first := modelErrorCard(t, s, f, "s1")
	// The agent went on and failed again, and the manager did not hear the turn start (it was away, or the turn was
	// over between two lists): the later turn's card is the session's only one.
	later := apiErrorTurn("s1", "API Error: 499 status code (no body)")
	later.UpdatedAt = turnEnd.Add(5 * time.Minute)
	f.setSessions("box", later)
	triage(t, s, "s1")
	cards := s.workCards()
	if len(cards) != 1 || cards[0].ID == first.ID || entryOf(t, s, "s1").InteractionID != cards[0].ID {
		t.Fatalf("cards %+v, entry %+v", cards, entryOf(t, s, "s1"))
	}
	if got := workEventsOf(s, "withdrawn"); len(got) != 1 || got[0]["by"] != "turn" {
		t.Fatalf("withdrawn events %v", got)
	}
}
