package assistant

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// An ask_user that waits for its answer holds its conversation's turn for as long as its card is up (askUserWait), and
// whatever else is sent to the conversation meanwhile waits behind that turn in the relay queue: a message from another
// conversation (conversation_send), a supervision message, a task's notice, the answer to an earlier question (#7392,
// docs/assistant/assistant.md "Asking without holding the turn"). The owner writing in the conversation takes the
// question back (withdrawAskUser); a message from anywhere else does not, since the owner did not answer: the question
// lets go of the turn instead. It becomes a question asked without waiting, in place: the same card, still on the
// phone, no second push; its record is kept and its card made resumable as wait false does it (ask_later.go), so it
// outlives a restart; the call returns saying the answer comes later as a message, and the turn ends, letting the
// queued message in. A question lets go only once it has waited askReleaseAfter: a confirmation the owner answers
// within a minute or two keeps its turn.

// askReleaseAfter is how long a waiting ask_user keeps its turn while a message waits for its conversation: about as
// long as a message from the system waits while the owner is talking there (ownerPresentFor). A variable for the
// tests.
var askReleaseAfter = 2 * time.Minute

// errReleased is what ask returns for an ask_user question that let go of its turn (releaseAsk).
var errReleased = errors.New("the question no longer holds the turn: a message waits for the conversation")

// resumableMarker is a store that marks a pending card as outliving a restart (db.DB); one without it keeps no cards.
type resumableMarker interface {
	MarkInteractionResumable(ctx context.Context, id string) (bool, error)
}

func (g guardedStore) MarkInteractionResumable(ctx context.Context, id string) (ok bool, err error) {
	defer guard(&err)
	m, has := g.st.(resumableMarker)
	if !has {
		return false, errNoTable
	}
	return m.MarkInteractionResumable(ctx, id)
}

// releaseHeldAsks: a message waits for conversation session (queueRelay), or a question was just asked in it (ask).
// Each ask_user it waits on that has waited askReleaseAfter lets go of the turn; a younger one is looked at again
// once it is that old, if a message still waits then.
func (s *Service) releaseHeldAsks(session string) {
	if session == "" || s.queuedFor(session) == 0 {
		return
	}
	now := time.Now()
	var due []*question
	s.questions.mu.Lock()
	for _, q := range s.questions.m {
		if q.ix.SessionID != session || !q.holdsTurn() {
			continue
		}
		if wait := q.ix.CreatedAt.Add(askReleaseAfter).Sub(now); wait > 0 {
			if q.releaseCheck == nil {
				q.releaseCheck = time.AfterFunc(wait, func() { s.releaseHeldAsks(session) })
			}
			continue
		}
		due = append(due, q)
	}
	s.questions.mu.Unlock()
	for _, q := range due {
		s.releaseAsk(context.Background(), q)
	}
}

// holdsTurn: q is an ask_user a call waits on, not taken back, not answered and not letting go already. Under
// questions.mu.
func (q *question) holdsTurn() bool {
	return q.release != nil && !q.later && !q.userSpoke && !q.releasing && len(q.answer) == 0
}

// releaseAsk has q, an ask_user a call waits on, let go of its turn: its record is kept first and its card made
// resumable (a card whose end could reach nobody after a restart does not stop being waited on), then it is a
// question asked without waiting and the call returns (errReleased). false when it stays as it was: it was answered
// or taken back meanwhile, at most askLaterMax questions are out without waiting already, or its card could not be
// kept; the message then waits behind the turn as before.
func (s *Service) releaseAsk(ctx context.Context, q *question) bool {
	s.questions.mu.Lock()
	if s.questions.m[q.ix.ID] != q || !q.holdsTurn() {
		s.questions.mu.Unlock()
		return false
	}
	q.releasing = true
	ix := q.ix
	s.questions.mu.Unlock()
	ix.Resumable = true // what AskedWithoutWaiting reads: the channels, the hub's update (#7392)
	kept := false
	defer func() {
		s.questions.mu.Lock()
		q.releasing = false
		s.questions.mu.Unlock()
		if !kept {
			s.forgetAskLater(ctx, ix.ID)
		}
	}()
	if err := s.keepAskLater(ctx, ix); err != nil {
		s.logf("ask_user %s: keeps the turn: %v", ix.ID, err)
		return false
	}
	if st, has := s.fleetStore().(resumableMarker); has {
		ok, err := st.MarkInteractionResumable(ctx, ix.ID)
		switch {
		case errors.Is(err, errNoTable): // a store that keeps no cards: a restart keeps none either way
		case err != nil:
			s.logf("ask_user %s: keeps the turn: its card can't be kept across a restart: %v", ix.ID, err)
			return false
		case !ok:
			s.logf("ask_user %s: keeps the turn: its card is not pending in the history", ix.ID)
			return false
		}
	}
	s.fleet.mu.Lock()
	var card *pending
	if p := s.fleet.interactions[ix.ID]; p != nil {
		p.ix.Resumable = true // its node going away no longer ends it (cancelMachineInteractions)
		card = p
	}
	shown := ix
	if card != nil {
		shown = card.ix
	}
	s.fleet.mu.Unlock()
	s.questions.mu.Lock()
	// Answered or taken back while its record was written: the call has that, and the record goes (the defer).
	if s.questions.m[ix.ID] != q || q.userSpoke || len(q.answer) > 0 {
		s.questions.mu.Unlock()
		return false
	}
	q.ix.Resumable, q.later = true, true
	close(q.release)
	kept = true
	s.questions.mu.Unlock()
	if card != nil {
		s.show(shown) // the apps and the hub learn it is resumable now; no second push
	}
	s.publish(context.WithoutCancel(ctx))
	s.logf("ask_user %s: no longer holds the turn of %s: a message waits for it; its answer comes as a message", ix.ID, short(ix.SessionID))
	return true
}

// keepAskLater adds ix's record to those of the questions asked without waiting (ask_later.go), as askLaterStart does.
func (s *Service) keepAskLater(ctx context.Context, ix proto.Interaction) error {
	b := &s.askLaters
	s.loadAskLater(ctx) // the ones a manager before this one left go back first: this read must not stand in for theirs
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAskLaterLocked(ctx) {
		return errors.New("the questions asked without waiting can't be read")
	}
	if len(b.list) >= askLaterMax {
		return errors.New("too many questions are out without waiting")
	}
	b.list = append(b.list, askLater{Interaction: ix})
	if err := s.saveAskLaterLocked(ctx); err != nil {
		b.list = slices.DeleteFunc(b.list, func(a askLater) bool { return a.Interaction.ID == ix.ID })
		return err
	}
	return nil
}

// forgetAskLater drops the record of question id, one that did not let go of its turn after all.
func (s *Service) forgetAskLater(ctx context.Context, id string) {
	b := &s.askLaters
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.list)
	b.list = slices.DeleteFunc(b.list, func(a askLater) bool { return a.Interaction.ID == id })
	if len(b.list) == n {
		return
	}
	if err := s.saveAskLaterLocked(context.WithoutCancel(ctx)); err != nil {
		s.logf("ask_user %s: forget its record: %v", id, err)
	}
}
