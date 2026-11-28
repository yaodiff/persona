package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Every try carries the same once_id. The node keeps it for ten minutes or until restart (sentLog).
// The queue also saves the start of a try before sending and checks the transcript when the outcome is in doubt.
// A line can appear late even after a successful read, so both a miss and a read error get another look.
const (
	relayCheckWait = 15 * time.Second
	relayLookGap   = 10 * time.Second
	relayLookLimit = 3
)

// supervisionConfirmRetryLimit bounds how many transcript-confirmation failure cycles an unconfirmed supervision
// relay undergoes before retiring the stuck queue head and alerting the owner so newer supervision can deliver.
var supervisionConfirmRetryLimit = 3

// supervisionConfirmDelays bounds brief post-send settle re-checks for a successful supervision RPC whose
// runtime flushes transcript items asynchronously or coalesced a concurrent pre-send snapshot.
var supervisionConfirmDelays = []time.Duration{
	150 * time.Millisecond,
	350 * time.Millisecond,
	500 * time.Millisecond,
}

// relayMsgID is the once_id every try of message msg to conversation to goes under: made from the message, so
// the same after the manager restarts and for messages queued before ids existed, and different for any other one.
// A detached continuation's text may change when its grant is consumed; its occurrence metadata, not that text,
// identifies the delivery. relayLocked pins the old text hash on pre-upgrade queued continuations.
func relayMsgID(to string, msg relayMsg) string {
	if msg.OnceID != "" {
		return msg.OnceID
	}
	text := msg.Text
	if msg.Continuation {
		text = "continuation\x00" + msg.ContinuationDigest
	}
	return relayMsgIDText(to, msg, text)
}

// legacyRelayMsgID is the identity used before continuation text became
// non-identifying. It is retained only when loading an existing queue.
func legacyRelayMsgID(to string, msg relayMsg) string {
	return relayMsgIDText(to, msg, msg.Text)
}

// sameLegacyContinuationRelay recognizes the journal occurrence of a queued
// pre-upgrade continuation even after its retry text becomes a no-retry notice.
// Its pinned OnceID deliberately remains the old text hash, so comparing only
// the newly computed ID would append the same journal result again.
func sameLegacyContinuationRelay(a, b relayMsg) bool {
	return a.Continuation && b.Continuation && a.ContinuationDigest == "" && b.ContinuationDigest == "" &&
		a.From == b.From && a.Tool == b.Tool && a.State == b.State && a.At.Equal(b.At)
}

func relayMsgIDText(to string, msg relayMsg, text string) string {
	h := sha256.New()
	for _, part := range []string{to, msg.From, msg.Tool, msg.State, msg.At.UTC().Format(time.RFC3339Nano), text} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "relay-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// relayInDoubt: the send may have gone in though no answer said so — the time ran out, or the link dropped while it
// went. A refusal (the conversation is gone, its launcher cannot start) and an explicit not-sent / not-written
// transport error are no doubt: nothing went in.
func relayInDoubt(err error) bool {
	if errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten) {
		return false
	}
	return replyInDoubt(err) || errors.Is(err, rpc.ErrClosed)
}

// relayClockSlack is how much earlier than a try a line may say it was written and still be the try's: the node's
// clock against the manager's.
const relayClockSlack = 30 * time.Second

// relayContextRe is any context block a line can carry before or around a relay's words: the live snapshot, the
// message source the manager puts in front (prompts.MessageSourceText), the note on a message that came mid-turn.
var relayContextRe = lazyre.New(`(?s)<` + compat.ContextPattern() + `(?:\s[^>]*)?>.*?</` + compat.ContextPattern() + `>\s*`)

// relayWords is a line's or a relay's own words: its context blocks left out.
func relayWords(s string) string {
	return strings.TrimSpace(relayContextRe.ReplaceAllString(s, ""))
}

// relayArrived checks the latest turns for the relay's text and source since the first try that may have gone in.
// It bypasses the manager's database history cache on the active source so freshly written turns are visible before
// debounced session revision updates arrive. An error means the transcript could not be read. A successful read
// without a match can still miss a late write.
func (s *Service) relayArrived(machine, to string, msg relayMsg) (bool, error) {
	text := strings.TrimSpace(relayDeliveryText(msg))
	want := relayWords(text)
	ctx, cancel := context.WithTimeout(withLiveHistoryRead(context.Background()), relayCheckWait)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": to, "turns": 3, "lazy_images": true}, &res); err != nil {
		return false, err
	}
	fromConversation := msg.Tool == "conversation_send" || msg.Tool == "conversation_new"
	var onceID string
	if want == "" {
		onceID = relayMsgID(to, msg)
	}
	for _, it := range res.Items {
		if it.Role != "user" || it.Kind != proto.KindText {
			continue
		}
		if !it.TS.IsZero() && it.TS.Before(msg.TriedAt.Add(-relayClockSlack)) {
			continue
		}
		if fromConversation {
			if src := prompts.ItemSource(it); src == nil || src.Kind != proto.SourceConversation || src.ConversationID != msg.From {
				continue
			}
		}
		if want == "" {
			if id := prompts.RelayMessageID(it.Text); id != "" {
				if id == onceID && it.ClientMsgID == "" {
					return true, nil
				}
				continue
			}
			// A relay sent before ids were kept in the transcript must match its rendered context.
			if text == "" || !strings.HasSuffix(strings.TrimSpace(it.Text), text) {
				continue
			}
		}
		if relayWords(it.Text) != want {
			continue
		}
		return true, nil
	}
	return false, nil
}

// errSupervisionRelayUnconfirmed marks a supervision send whose RPC returned without a matching user item in the
// destination transcript. It is not a refusal; the caller persists the relay with its attempt state for reconciliation.
var errSupervisionRelayUnconfirmed = errors.New("supervision send not confirmed in transcript")

// errSupervisionConfirmExhausted marks a queued supervision relay that exhausted supervisionConfirmRetryLimit
// transcript-confirmation cycles without matching in the destination transcript.
var errSupervisionConfirmExhausted = errors.New("supervision send unconfirmed after retry limit")

// lookForRelay notes a match as delivered. Otherwise it advances the relay's look schedule.
func (s *Service) lookForRelay(machine, to string, msg *relayMsg) bool {
	landed, err := s.relayArrived(machine, to, *msg)
	if landed {
		s.logf("relay to %s: a try in doubt is in the conversation, taken as delivered", short(to))
		s.relayDelivered(context.Background(), to, *msg)
		return true
	}
	if err != nil {
		s.logf("relay to %s: transcript check failed: %v", short(to), err)
	}
	msg.Looks = min(msg.Looks+1, relayLookLimit)
	msg.LookAfter = time.Now().UTC().Add(relayLookGap)
	return false
}

// confirmSupervisionRelay checks the destination transcript after a supervision send, including a successful RPC
// return (#12567). Delivery is completed only once the rendered supervision user item is confirmed present.
func (s *Service) confirmSupervisionRelay(ctx context.Context, machine, to string, msg *relayMsg, res superviseRoundReply, round, inDoubt bool) bool {
	check := func() (bool, error) {
		landed, err := s.relayArrived(machine, to, *msg)
		if !landed && err == nil && res.SessionID != "" && res.SessionID != to {
			landed, err = s.relayArrived(machine, res.SessionID, *msg)
		}
		return landed, err
	}
	landed, err := check()
	if !landed && err == nil && !inDoubt {
		for _, wait := range supervisionConfirmDelays {
			if wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					err = ctx.Err()
				case <-t.C:
				}
				if err != nil {
					break
				}
			}
			landed, err = check()
			if landed || err != nil {
				break
			}
		}
	}
	if landed {
		if inDoubt {
			s.logf("relay to %s: a try in doubt is in the conversation, taken as delivered", short(to))
		}
		if !inDoubt && round {
			s.superviseRoundAnswered(to, res)
		}
		s.relayDelivered(ctx, to, *msg)
		return true
	}
	if err != nil {
		s.logf("relay to %s: transcript check failed: %v", short(to), err)
	} else if !inDoubt {
		s.logf("relay to %s: supervision send returned without a transcript item; waiting to confirm", short(to))
	}
	msg.Looks = min(msg.Looks+1, relayLookLimit)
	msg.LookAfter = time.Now().UTC().Add(relayLookGap)
	return false
}

func (s *Service) noteDoubt(to string, msg relayMsg, tried time.Time) {
	b := &s.relays
	b.mu.Lock()
	if b.doubted == nil {
		b.doubted = map[string]time.Time{}
	}
	id := relayMsgID(to, msg)
	if _, ok := b.doubted[id]; !ok {
		b.doubted[id] = tried
	}
	b.mu.Unlock()
}

func (s *Service) recordQueuedDoubt(to string, msg *relayMsg) {
	b := &s.relays
	b.mu.Lock()
	if b.doubted == nil {
		b.doubted = map[string]time.Time{}
	}
	id := relayMsgID(to, *msg)
	triedAt, ok := b.doubted[id]
	if !ok {
		triedAt = msg.TriedAt
		if !msg.DoubtedAt.IsZero() {
			triedAt = msg.DoubtedAt
		}
		b.doubted[id] = triedAt
	}
	if msg.Tool == superviseTool {
		msg.DoubtedAt = triedAt
		// Keep the doubt with the durable queue head too. A restart loses b.doubted, and a confirmation may rewrite
		// the head's signals while this send is in flight; neither may turn a late-visible original into a replay (#5732).
		q := b.st.Queue[to]
		for i := range q {
			if relayMsgID(to, q[i]) != id {
				continue
			}
			if q[i].DoubtedAt.IsZero() {
				q[i].DoubtedAt = triedAt
			}
			q[i].Looks, q[i].LookAfter, q[i].ConfirmFails = msg.Looks, msg.LookAfter, msg.ConfirmFails
			b.st.Queue[to] = q
			_ = s.saveRelayLocked()
			break
		}
	}
	b.mu.Unlock()
}

// tryDirectRelay carries an immediate try's start into the queue if the send ends in doubt. Supervision sends always
// confirm their user item in the destination transcript before completing delivery (#12567).
func (s *Service) tryDirectRelay(ctx context.Context, machine, to string, msg *relayMsg) error {
	tried := time.Now().UTC()
	if msg.Tool != superviseTool {
		err := s.deliverRelay(ctx, machine, to, *msg)
		if relayInDoubt(err) {
			msg.TriedAt = tried
		}
		return err
	}
	if msg.OnceID == "" {
		msg.OnceID = relayMsgID(to, *msg)
	}
	res, round, err := s.sendRelay(ctx, machine, to, *msg)
	if err != nil && !relayInDoubt(err) {
		return err
	}
	msg.TriedAt = tried
	if s.confirmSupervisionRelay(ctx, machine, to, msg, res, round, err != nil) {
		return nil
	}
	msg.DoubtedAt = tried
	s.noteDoubt(to, *msg, tried)
	s.releasePushes(context.Background(), to)
	if err != nil {
		return err
	}
	return errSupervisionRelayUnconfirmed
}

// tryQueuedRelay sends the queue head after reconciliation and updates head to its saved pre-send state.
// A positive wait keeps the returned message queued for another drain.
func (s *Service) tryQueuedRelay(ctx context.Context, machine, to string, head *relayMsg) (relayMsg, time.Duration, error) {
	return s.tryQueuedRelayText(ctx, machine, to, head, *head)
}

// tryQueuedRelayText separates the durable queue identity from the exact text
// sent. A continuation can change to a no-retry notice without making the head
// either unremovable after success or stale after a restart.
func (s *Service) tryQueuedRelayText(ctx context.Context, machine, to string, head *relayMsg, msg relayMsg) (relayMsg, time.Duration, error) {
	queued := *head
	previous := msg
	if msg.TriedAt.IsZero() {
		msg.TriedAt = time.Now().UTC()
	} else {
		// A duplicate relay is less harmful than a lost one when there is no owner to ask. Retry under the same id.
		s.logf("relay to %s: no match after %d transcript checks; trying again", short(to), relayLookLimit)
		s.handoverRetried(msg.Handover) // an answer that went in meanwhile did not answer it (relay_handover.go)
	}
	msg.Looks, msg.LookAfter = 0, time.Time{}

	// The draining head is reserved for this try. A manager stopped during the send must find the mark on restart.
	b := &s.relays
	b.mu.Lock()
	q := b.st.Queue[to]
	if !b.loaded || len(q) == 0 || q[0] != queued {
		// A reload can invalidate the selection before the attempt is saved. Let the next drain pick again.
		b.mu.Unlock()
		return queued, relayRetryGap, nil
	}
	q[0] = msg
	err := s.saveRelayLocked()
	if err != nil {
		q[0] = queued
	}
	b.mu.Unlock()
	if err != nil {
		return queued, relayRetryGap, err
	}
	*head = msg
	if msg.Tool != superviseTool {
		err = s.deliverRelay(ctx, machine, to, msg)
		if !relayInDoubt(err) {
			// A refusal does not add any doubt. Retain the earlier try's mark, if there was one.
			return previous, 0, err
		}
		if s.lookForRelay(machine, to, &msg) {
			return msg, 0, nil
		}
		s.recordQueuedDoubt(to, &msg)
		return msg, relayLookGap, nil
	}
	res, round, err := s.sendRelay(ctx, machine, to, msg)
	if err != nil && !relayInDoubt(err) {
		// A refusal does not add any doubt. Retain the earlier try's mark, if there was one.
		return previous, 0, err
	}
	if s.confirmSupervisionRelay(ctx, machine, to, &msg, res, round, err != nil) {
		return msg, 0, nil
	}
	if !previous.TriedAt.IsZero() || !previous.DoubtedAt.IsZero() {
		msg.ConfirmFails++
	}
	if msg.ConfirmFails >= supervisionConfirmRetryLimit {
		s.releasePushes(context.Background(), to)
		return msg, 0, errSupervisionConfirmExhausted
	}
	s.recordQueuedDoubt(to, &msg)
	s.releasePushes(context.Background(), to)
	return msg, relayLookGap, nil
}

// relayLanded says whether an earlier try of msg that ended in doubt has gone in since: its line may come after the
// look (relaySettled), and a node restarted in between no longer knows the once_id. Found, it is noted as delivered.
func (s *Service) relayLanded(machine, to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	tried, ok := b.doubted[relayMsgID(to, msg)]
	b.mu.Unlock()
	if !ok && !msg.DoubtedAt.IsZero() {
		tried, ok = msg.DoubtedAt, true // the durable head survived a manager restart
	}
	if !ok && msg.Withdrawn && !msg.At.IsZero() {
		// The original send may have landed just before a restart removed the process, after withdrawal but before the
		// queue head was removed. No doubt was recorded for that acknowledged send; its original timestamp still bounds
		// the transcript lookup, so an older identical line is not accepted as this delivery.
		tried, ok = msg.At, true
	}
	if !ok {
		return false
	}
	msg.TriedAt = tried
	landed, err := s.relayArrived(machine, to, msg)
	if err != nil || !landed {
		return false
	}
	s.logf("relay to %s: an earlier try in doubt is in the conversation, taken as delivered", short(to))
	s.relayDelivered(context.Background(), to, msg)
	s.forgetDoubt(to, msg)
	return true
}

func (s *Service) forgetDoubt(to string, msg relayMsg) {
	b := &s.relays
	b.mu.Lock()
	delete(b.doubted, relayMsgID(to, msg))
	b.mu.Unlock()
}
