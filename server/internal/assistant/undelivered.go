package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/wireids"
)

// The owner's messages that could not go in (#10194, docs/assistant/assistant.md "Messages that could not go in").
//
// The owner writes to one of the assistant's conversations while its process cannot start (a cold start killed on a
// loaded computer, the computer away). Nothing was typed, so the hub used to answer the app with the failure and
// forget the message; a replay of the app's operation then read as accepted and the bubble went. Now the hub hands
// such a message here (assistant.keep_undelivered) and answers the app that it is kept. The manager:
//   - keeps it in its Postgres (the setting undeliveredKey) with the app's own words and pictures, oldest first;
//   - shows it on the conversation's row (proto.Session.Undelivered), so every client draws it as the owner's
//     message marked not delivered, not only the one that sent it;
//   - sends it again by itself (undeliveredPass): with backoff, at once when the conversation is seen running again,
//     in order, under the message's own client_msg_id. Each try's start (TriedAt) is saved before the try, and a try
//     whose mark could not be saved is not made, so a manager stopped during a try finds the mark when it starts again;
//   - never sends by itself again a try whose outcome is in doubt (no answer in time, the link dropped, the manager
//     stopped during it, the hub's own first try unknown): the node's memory of ids (sendsafe.go sentLog) is gone after
//     it restarts, and the transcript can be late or unreadable, so neither can say for sure it did not go in. Such a
//     message is only looked for in the conversation's latest turns (undeliveredLook) for undeliveredLookFor: found,
//     it went in; not found by then (the transcript unreadable, only pictures and no id to know them by, or not there),
//     the row says failed, that it may have gone in, and the owner looks and picks Retry or Delete (#10669);
//   - gives up after undeliveredGiveUp: the row then says failed and the owner picks Retry or Delete. The owner's Retry
//     sends it even after a try in doubt, looking for that try first on every attempt (one that cannot reach the
//     computer keeps the Retry and the look for later); a try of its own in doubt is again only looked for, still from
//     the first try's time.
// A newer owner message to a conversation with kept ones is kept behind them (CodeUndeliveredAhead), never sent first.

// undeliveredKey is the setting the kept messages live in.
const undeliveredKey = "assistant.undelivered"

// undeliveredGiveUp is how long a kept message is tried before it waits for the owner (Retry / Delete).
var undeliveredGiveUp = 24 * time.Hour

// undeliveredBackoff is the wait before each try after a failed one; the last repeats.
var undeliveredBackoff = []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

// undeliveredLookFor is how long a try whose outcome is in doubt is looked for before the owner is asked. The looks
// follow the first backoff steps (15 s, 30 s, 1 min, then the end: at 15 s, 45 s, 1 min 45 s and 3 min), which covers
// a transcript line a busy computer writes late or a read that fails once; the owner hears within minutes instead of
// the message waiting for a day.
var undeliveredLookFor = 3 * time.Minute

// undeliveredWait is the wait after the n-th failed try (or look), n ≥ 1; the last step repeats.
func undeliveredWait(n int) time.Duration {
	return undeliveredBackoff[min(max(n-1, 0), len(undeliveredBackoff)-1)]
}

// undeliveredTick is how often the kept messages are looked at without anything else waking the loop.
var undeliveredTick = 15 * time.Second

// undeliveredMaxBytes bounds one kept message (its words and pictures as the app sent them): a bigger one is not kept
// and the app gets the failure as before.
const undeliveredMaxBytes = 16 << 20

// keptMessage is one owner message kept for its conversation.
type keptMessage struct {
	ID             string           `json:"id"` // its client_msg_id
	Machine        string           `json:"machine"`
	Session        string           `json:"session"`
	Params         map[string]any   `json:"params"` // the app's message as it came (text, images, launcher, model)
	Human          bool             `json:"human,omitempty"`
	CredentialID   string           `json:"credential_id,omitempty"`
	OriginDeviceID wireids.DeviceID `json:"origin_device_id,omitempty"`
	Lang           string           `json:"lang,omitempty"`
	At             time.Time        `json:"at"`
	// State is proto.UndeliveredWaiting or proto.UndeliveredFailed.
	State    string    `json:"state"`
	Attempts int       `json:"attempts,omitempty"`
	NextAt   time.Time `json:"next_at"`
	// TriedAt: when the first try that may have gone in began (its outcome in doubt, or the manager stopped during it);
	// zero when every try so far surely typed nothing. Saved before each try, and kept, never moved later, until the
	// message goes in or the owner deletes it: every later look, the owner's Retry's included, looks from then. While
	// it is set the message is only looked for, never sent again by itself (inDoubt), unless the owner's Retry says so
	// (OwnerRetry).
	TriedAt time.Time `json:"tried_at,omitempty"`
	// LookUntil: when the look for a try in doubt ends (zero: it begins at the next look, as for a try the manager
	// stopped during); Looks: the looks made so far, for the wait before the next.
	LookUntil time.Time `json:"look_until,omitempty"`
	Looks     int       `json:"looks,omitempty"`
	// OwnerRetry: the owner pressed Retry; tries are made even after one in doubt, each looking for it first, until one
	// goes in or ends in doubt itself (tries that typed nothing keep it).
	OwnerRetry bool `json:"owner_retry,omitempty"`
	// WaitIdle: the latest try was refused because the conversation was at work while its task-follow-up role changed
	// (assistantwire.CodeConversationBusy). A session list that shows it still working does not wake it; one that shows
	// it settled does, so it goes in right after the work ends, on the new role.
	WaitIdle  bool   `json:"wait_idle,omitempty"`
	Error     string `json:"error,omitempty"`
	SignedOut bool   `json:"signed_out,omitempty"`
}

// inDoubt: the latest try may have gone in and nobody said to send it again, so it is only looked for (undeliveredLook).
func (k keptMessage) inDoubt() bool {
	return !k.TriedAt.IsZero() && !k.OwnerRetry
}

func (k keptMessage) text() string {
	t, _ := k.Params["text"].(string)
	return t
}

func (k keptMessage) images() int {
	n, _ := k.Params["images"].([]any)
	return len(n)
}

func (k *keptMessage) failSignedOut() {
	k.State, k.SignedOut = proto.UndeliveredFailed, true
	k.Error = i18n.T(i18n.Lang(k.Lang), "undelivered.signed_out")
}

type undeliveredBook struct {
	mu              sync.Mutex
	loaded          bool
	st              map[string][]keptMessage // by conversation, oldest first
	sending         map[string]bool          // conversations a pass is sending into
	revoked         map[string]bool          // credentials this manager was told were signed out
	revocationDirty bool                     // the failed rows still need to be saved
	wake            chan struct{}
	start           sync.Once
}

func (s *Service) undeliveredLocked(ctx context.Context) bool {
	b := &s.undelivered
	if b.loaded {
		return true
	}
	st := map[string][]keptMessage{}
	if store := s.store(); store != nil {
		if _, err := store.Setting(ctx, undeliveredKey, &st); err != nil {
			s.logf("load undelivered messages: %v", err)
			return false
		}
	}
	if st == nil {
		st = map[string][]keptMessage{}
	}
	b.st, b.loaded, b.sending = st, true, map[string]bool{}
	return true
}

func (s *Service) saveUndeliveredLocked() error {
	store := s.store()
	if store == nil {
		return nil
	}
	for id, q := range s.undelivered.st {
		if len(q) == 0 {
			delete(s.undelivered.st, id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return store.PutSetting(ctx, undeliveredKey, s.undelivered.st)
}

// KeepUndelivered is assistant.keep_undelivered: the hub could not deliver the owner's message p (nothing was typed),
// or SendParams kept it behind older ones. It is kept and sent again later; the same message kept twice (the app
// retried the same id) is kept once.
func (s *Service) KeepUndelivered(ctx context.Context, p assistantwire.KeepUndeliveredParams) (proto.UndeliveredMessage, error) {
	id, _ := p.Params["client_msg_id"].(string)
	if id == "" || p.SessionID == "" || p.MachineID == "" {
		return proto.UndeliveredMessage{}, &rpc.Error{Code: CodeBadRequest, Message: "client_msg_id, machine_id and session_id are required"}
	}
	if err := s.conversationAvailable(ctx, p.MachineID, p.SessionID); err != nil {
		return proto.UndeliveredMessage{}, err
	}
	params := map[string]any{}
	for _, k := range []string{"text", "images", "launcher", "model", "effort", "client_msg_id", "public"} {
		if v, ok := p.Params[k]; ok {
			params[k] = v
		}
	}
	if raw, err := json.Marshal(params); err != nil || len(raw) > undeliveredMaxBytes {
		return proto.UndeliveredMessage{}, &rpc.Error{Code: CodeBadRequest, Message: "the message is too big to keep"}
	}
	lang := s.appLang(ctx, p.Lang)
	now := time.Now().UTC()
	b := &s.undelivered
	b.mu.Lock()
	if !s.undeliveredLocked(ctx) {
		b.mu.Unlock()
		return proto.UndeliveredMessage{}, errors.New("the undelivered messages could not be read")
	}
	for _, k := range b.st[p.SessionID] {
		if k.ID == id {
			b.mu.Unlock()
			return k.wire(), nil
		}
	}
	k := keptMessage{ID: id, Machine: p.MachineID, Session: p.SessionID, Params: params, Human: p.Human, Lang: string(lang), CredentialID: p.CredentialID, OriginDeviceID: p.OriginDeviceID,
		At: now, State: proto.UndeliveredWaiting, Attempts: 1, NextAt: now.Add(undeliveredBackoff[0]),
		Error: undeliveredReason(lang, p.Code), WaitIdle: p.Code == assistantwire.CodeConversationBusy}
	if p.Code == assistantwire.CodeUndeliveredAhead {
		// never tried: it goes right after the ones ahead of it, and says so
		k.Attempts, k.NextAt, k.Error = 0, now, i18n.T(lang, "assistant-undelivered.behind")
	}
	if p.Code == assistantwire.CodeDeliveryUnknown {
		// The first attempt may have typed it: only looked for, never sent again by itself (undeliveredLook), even if
		// an older hub omitted the time.
		k.TriedAt, k.LookUntil = p.TriedAt, now.Add(undeliveredLookFor)
		if k.TriedAt.IsZero() {
			k.TriedAt = now
		}
	}
	if b.revoked[k.CredentialID] {
		k.failSignedOut()
	}
	b.st[p.SessionID] = append(b.st[p.SessionID], k)
	err := s.saveUndeliveredLocked()
	b.mu.Unlock()
	if err != nil {
		s.undeliveredDrop(p.SessionID, id) // not kept for sure: the app is told it failed, as before
		return proto.UndeliveredMessage{}, err
	}
	s.logf("undelivered: kept a message for %s (%d waiting)", short(p.SessionID), s.undeliveredCount(p.SessionID))
	// An exact decision choice waiting on this message waits on the kept one now (decision_supervisor_reply.go).
	s.markSupervisorSettleKept(SessionRef{MachineID: p.MachineID, SessionID: p.SessionID}, id, time.Time{})
	s.publish(context.WithoutCancel(ctx))
	s.wakeUndelivered()
	return k.wire(), nil
}

// undeliveredReason is the line a kept message shows: why it did not go in.
func undeliveredReason(lang i18n.Lang, code string) string {
	if code == "machine_offline" || code == "node_closed" {
		return i18n.T(lang, "assistant-undelivered.computer_away")
	}
	if code == assistantwire.CodeConversationBusy {
		return i18n.T(lang, "assistant-undelivered.busy")
	}
	return i18n.T(lang, "assistant-undelivered.not_started")
}

func (k keptMessage) wire() proto.UndeliveredMessage {
	m := proto.UndeliveredMessage{ID: k.ID, Text: k.text(), Images: k.images(), At: k.At, State: k.State, Attempts: k.Attempts, Error: k.Error, DeleteOnly: k.SignedOut}
	if k.State == proto.UndeliveredWaiting && !k.NextAt.IsZero() {
		next := k.NextAt
		m.NextAt = &next
	}
	return m
}

// undeliveredCredentialRevoked records the sender even if a concurrent request has yet to keep its message.
func (s *Service) undeliveredCredentialRevoked(ctx context.Context, id string) {
	b := &s.undelivered
	b.mu.Lock()
	b.recordRevocationLocked(id)
	changed := s.undeliveredLocked(ctx) && s.failRevokedKeptLocked()
	b.mu.Unlock()
	if changed {
		s.publish(context.WithoutCancel(ctx))
	}
	s.wakeUndelivered()
}

func (b *undeliveredBook) recordRevocationLocked(id string) {
	if b.revoked == nil {
		b.revoked = make(map[string]bool)
	}
	b.revoked[id] = true
}

// failRevokedKeptLocked fails the known revoked rows and retries any failed save. The book's lock is held.
func (s *Service) failRevokedKeptLocked() bool {
	b := &s.undelivered
	if len(b.revoked) == 0 && !b.revocationDirty {
		return false
	}
	changed := false
	for _, q := range b.st {
		for i := range q {
			if b.revoked[q[i].CredentialID] && !q[i].SignedOut {
				q[i].failSignedOut()
				changed = true
			}
		}
	}
	if changed || b.revocationDirty {
		err := s.saveUndeliveredLocked()
		b.revocationDirty = err != nil
		if err != nil {
			s.logf("save kept messages from signed-out devices: %v", err)
		}
	}
	return changed
}

// undeliveredViews are the kept messages by conversation, for the rows (snapshot); false while they could not be read.
func (s *Service) undeliveredViews(ctx context.Context) (map[string][]keptMessage, bool) {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(ctx) {
		return nil, false
	}
	out := make(map[string][]keptMessage, len(b.st))
	for id, q := range b.st {
		out[id] = slices.Clone(q)
	}
	return out, true
}

func (s *Service) undeliveredCount(id string) int {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.st[id])
}

// hasKeptUndelivered reports whether message id is currently kept in undelivered for conversation sid (conservatively
// true if the undelivered book cannot be read, so a kept message's quiet-relay reservation is not discarded).
func (s *Service) hasKeptUndelivered(ctx context.Context, sid, id string) bool {
	if sid == "" || id == "" {
		return false
	}
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(ctx) {
		return true
	}
	return s.keptLocked(sid, id) != nil
}

// undeliveredAhead: the owner's message id to conversation sid would overtake kept ones (any not given up on).
func (s *Service) undeliveredAhead(ctx context.Context, sid, id string) bool {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(ctx) {
		return false
	}
	for _, k := range b.st[sid] {
		if k.ID != id && k.State == proto.UndeliveredWaiting {
			return true
		}
	}
	return false
}

// keepsBehind: SendParams' message for p's conversation must wait behind kept ones (an owner's message with words or
// pictures and its own id; the node asking what its own message runs on carries neither).
func (s *Service) keepsBehind(ctx context.Context, p SendParams) bool {
	id, _ := p.Params["client_msg_id"].(string)
	_, text := p.Params["text"]
	_, images := p.Params["images"]
	return id != "" && (text || images) && s.undeliveredAhead(ctx, p.SessionID, id)
}

func (s *Service) undeliveredDrop(sid, id string) bool {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(context.Background()) {
		return false
	}
	q := b.st[sid]
	i := slices.IndexFunc(q, func(k keptMessage) bool { return k.ID == id })
	if i < 0 {
		return false
	}
	b.st[sid] = slices.Delete(slices.Clone(q), i, i+1)
	if err := s.saveUndeliveredLocked(); err != nil {
		s.logf("save undelivered messages: %v", err)
	}
	return true
}

func (s *Service) wakeUndelivered() {
	b := &s.undelivered
	b.mu.Lock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	ch := b.wake
	b.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// undeliveredLoop sends the kept messages again until ctx ends.
func (s *Service) undeliveredLoop(ctx context.Context) {
	b := &s.undelivered
	b.mu.Lock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	wake := b.wake
	b.mu.Unlock()
	t := time.NewTicker(undeliveredTick)
	defer t.Stop()
	for {
		s.undeliveredPass(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}

// undeliveredSeen: a session list came in. A conversation with kept messages that runs again (its process is up, or
// it works) gets them now rather than at the next backoff step.
func (s *Service) undeliveredSeen(d DeviceSessions) {
	b := &s.undelivered
	b.mu.Lock()
	if !b.loaded || len(b.st) == 0 {
		b.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	woke := false
	for _, row := range d.Sessions {
		q := b.st[row.SessionID]
		if len(q) == 0 || row.Status == proto.StatusOffline || row.Status == "" {
			continue
		}
		for i := range q {
			// A try in doubt is only looked for, on its own steps (undeliveredLook).
			// One refused while the conversation was at work waits until the list shows it settled.
			if q[i].WaitIdle && row.Status == proto.StatusWorking {
				continue
			}
			if q[i].State == proto.UndeliveredWaiting && !q[i].inDoubt() && q[i].Machine == d.MachineID && q[i].NextAt.After(now) {
				q[i].NextAt, woke = now, true
			}
		}
	}
	b.mu.Unlock()
	if woke {
		s.wakeUndelivered()
	}
}

// wakeUndeliveredFor moves waiting kept messages for (machine, sid) forward to now
// so they drain immediately after a cross-machine failover rebind.
func (s *Service) wakeUndeliveredFor(machine, sid string) {
	b := &s.undelivered
	b.mu.Lock()
	if !s.undeliveredLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	q := b.st[sid]
	if len(q) == 0 {
		b.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	woke := false
	for i := range q {
		if q[i].State == proto.UndeliveredWaiting && !q[i].inDoubt() && (machine == "" || q[i].Machine == machine) {
			q[i].NextAt = now
			q[i].WaitIdle = false
			woke = true
		}
	}
	b.mu.Unlock()
	if woke {
		s.wakeUndelivered()
	}
}

// undeliveredPass tries, for each conversation, its oldest kept message that is due, and the next ones after it while
// they go in. One pass per conversation at a time.
func (s *Service) undeliveredPass(ctx context.Context, now time.Time) {
	b := &s.undelivered
	b.mu.Lock()
	if !s.undeliveredLocked(ctx) {
		b.mu.Unlock()
		return
	}
	changed := s.failRevokedKeptLocked()
	var due []string
	for id, q := range b.st {
		if b.sending[id] {
			continue
		}
		for _, k := range q {
			if k.State != proto.UndeliveredWaiting {
				continue
			}
			if !k.NextAt.After(now) {
				due = append(due, id)
			}
			break // in order: the oldest waiting one decides
		}
	}
	for _, id := range due {
		b.sending[id] = true
	}
	b.mu.Unlock()
	if changed {
		s.publish(context.WithoutCancel(ctx))
	}
	defer s.reapSupervisorSettles(time.Now()) // choices waiting on kept messages that failed meanwhile
	var wg sync.WaitGroup
	for _, id := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.undeliveredSend(ctx, id)
			b.mu.Lock()
			delete(b.sending, id)
			b.mu.Unlock()
		}()
	}
	wg.Wait()
}

// undeliveredSend sends conversation id's waiting messages in order until one does not go in. One whose latest try
// is in doubt is only looked for (undeliveredLook).
func (s *Service) undeliveredSend(ctx context.Context, id string) {
	changed := false
	defer func() {
		if changed {
			s.publish(context.WithoutCancel(ctx))
		}
	}()
	b := &s.undelivered
	for ctx.Err() == nil {
		b.mu.Lock()
		var k keptMessage
		found := false
		for _, m := range b.st[id] {
			if m.State == proto.UndeliveredWaiting {
				k, found = m, true
				break
			}
		}
		if !found { // the pass found it due; after one goes in, the next follows at once
			b.mu.Unlock()
			return
		}
		if k.inDoubt() {
			b.mu.Unlock()
			changed = true
			if !s.undeliveredLook(ctx, id, k) {
				return
			}
			continue // it went in, or the owner decides now: the next one may go
		}
		if time.Since(k.At) > undeliveredGiveUp {
			err := s.updateKeptLocked(id, k.ID, func(m *keptMessage) { m.State = proto.UndeliveredFailed })
			b.mu.Unlock()
			if err != nil {
				s.logf("undelivered: a kept message to %s is not marked given up: %v", short(id), err)
				return // the next pass marks it
			}
			changed = true
			continue // the next one has its own day
		}
		tried := time.Now().UTC()
		// The mark that this try may go in is saved before it: a manager stopped during the try finds it and only looks.
		// After a try in doubt (the owner's Retry) the mark keeps that try's time, so it is still the one looked for.
		if err := s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
			if m.TriedAt.IsZero() {
				m.TriedAt = tried
			}
			m.LookUntil, m.Looks, m.OwnerRetry = time.Time{}, 0, false
		}); err != nil {
			// Not saved: a try now could go in twice after a restart. None now; the next after a wait.
			if m := s.keptLocked(id, k.ID); m != nil {
				m.NextAt = tried.Add(undeliveredWait(m.Attempts))
			}
			b.mu.Unlock()
			changed = true
			s.logf("undelivered: not sending a kept message to %s, its try could not be marked: %v", short(id), err)
			return
		}
		b.mu.Unlock()
		doubt, err := s.deliverKept(ctx, k)
		b.mu.Lock()
		m := s.keptLocked(id, k.ID)
		var saveErr error
		var settle supervisorSettle
		settles := false
		switch {
		case err == nil:
			s.dropKeptLocked(id, k.ID)
			settle, settles = s.claimKeptSupervisorSettleLocked(k)
			s.logf("undelivered: sent a kept message to %s after %d tries", short(id), k.Attempts)
		case errors.Is(err, errKeptSignedOut):
			b.recordRevocationLocked(k.CredentialID)
			s.failRevokedKeptLocked()
		case m != nil && m.SignedOut:
			// The revocation event already settled this row while the try ran.
		case errors.Is(err, errKeptCredentialCheck):
			saveErr = s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.TriedAt, m.OwnerRetry = k.TriedAt, k.OwnerRetry
				m.LookUntil, m.Looks = k.LookUntil, k.Looks
				m.NextAt = time.Now().UTC().Add(undeliveredWait(1))
			})
			s.logf("check kept message's device credential: %v", err)
		case doubt:
			// It may have gone in: from now on only looked for, never sent again by itself (undeliveredLook).
			now := time.Now().UTC()
			saveErr = s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.Attempts++
				m.LookUntil, m.Looks, m.NextAt = now.Add(undeliveredLookFor), 0, now.Add(undeliveredWait(1))
				m.Error = undeliveredReason(i18n.Lang(m.Lang), keptErrCode(err))
			})
			s.logf("undelivered: a kept message to %s may have gone in (try %d): %v; looking for it", short(id), k.Attempts+1, err)
		case undeliveredGone(err):
			saveErr = s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.State, m.TriedAt, m.Error = proto.UndeliveredFailed, time.Time{}, i18n.T(i18n.Lang(m.Lang), "assistant-undelivered.gone")
			})
		default: // refused before anything was typed: tried again with backoff
			saveErr = s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.Attempts++
				m.NextAt = time.Now().UTC().Add(undeliveredWait(m.Attempts))
				m.Error = undeliveredReason(i18n.Lang(m.Lang), keptErrCode(err))
				m.WaitIdle = keptErrCode(err) == assistantwire.CodeConversationBusy
				// Nothing typed: the mark is as before the try. A first try's is cleared (the next need not look
				// first); an owner's Retry after a try in doubt still has that try to look for, and stays a Retry.
				m.TriedAt, m.OwnerRetry = k.TriedAt, k.OwnerRetry
			})
			s.logf("undelivered: %s still does not take a kept message (try %d): %v", short(id), k.Attempts+1, err)
		}
		b.mu.Unlock()
		s.settleKeptSupervisorChoice(ctx, k, settle, settles)
		if saveErr != nil {
			// The saved mark of the try stays, in memory too: the next pass only looks for it.
			s.logf("undelivered: the outcome of a try to %s is not saved: %v", short(id), saveErr)
		}
		changed = true
		if err != nil && !errors.Is(err, errKeptSignedOut) {
			return
		}
	}
}

var (
	errKeptSignedOut       = errors.New("kept message's sending device was signed out")
	errKeptCredentialCheck = errors.New("kept message's sending credential could not be checked")
)

// undeliveredLook looks for kept message k of conversation id, whose latest try may have gone in, in the
// conversation's latest turns; it is never sent again by itself (#10669). Found, it went in and leaves the list. Not
// found, it is looked for again on the next backoff step until undeliveredLookFor has passed, then the row says
// failed, that it may already have gone in: the transcript could not be read, the node forgot the id and there are no
// words to know it by (pictures only), or it is not there, and the owner looks at the conversation and picks Retry or
// Delete. True when the conversation's next message may go.
func (s *Service) undeliveredLook(ctx context.Context, id string, k keptMessage) bool {
	landed := s.undeliveredLanded(k)
	if landed {
		s.relayLaterOwnerOutcome(ctx, id, k.ID, true)
	}
	// A transcript read can still settle a prior try when the hub is unreachable.
	var revoked bool
	if !landed {
		revoked, _ = s.keptCredentialRevoked(ctx, k.CredentialID)
	}
	b := &s.undelivered
	b.mu.Lock()
	if landed {
		s.dropKeptLocked(id, k.ID)
		settle, settles := s.claimKeptSupervisorSettleLocked(k)
		b.mu.Unlock()
		s.logf("undelivered: a kept message to %s whose try was in doubt is in the conversation, taken as delivered", short(id))
		s.settleKeptSupervisorChoice(ctx, k, settle, settles)
		return true
	}
	defer b.mu.Unlock()
	if revoked {
		b.recordRevocationLocked(k.CredentialID)
		s.failRevokedKeptLocked()
		return true
	}
	if m := s.keptLocked(id, k.ID); m == nil || m.SignedOut {
		return true
	}
	now := time.Now().UTC()
	failed, retried := false, false
	err := s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
		if !m.inDoubt() {
			retried = true // the owner's Retry came meanwhile: it goes now
			return
		}
		if m.LookUntil.IsZero() {
			m.LookUntil = now.Add(undeliveredLookFor) // the manager stopped during the try: the look begins now
		}
		if !now.Before(m.LookUntil) {
			m.State, m.Error, failed = proto.UndeliveredFailed, i18n.T(i18n.Lang(m.Lang), "assistant-undelivered.may_have_gone_in"), true
			return
		}
		m.Looks++
		m.NextAt = now.Add(undeliveredWait(m.Looks + 1))
		if m.NextAt.After(m.LookUntil) {
			m.NextAt = m.LookUntil
		}
	})
	switch {
	case err != nil:
		s.logf("undelivered: the look for a kept message to %s is not saved: %v", short(id), err)
		return false
	case failed:
		s.logf("undelivered: a kept message to %s whose try was in doubt is not in the conversation; the owner decides", short(id))
	}
	return failed || retried
}

func keptErrCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// undeliveredGone: the conversation will never take it (deleted, gone): nothing to try again.
func undeliveredGone(err error) bool {
	code := keptErrCode(err)
	return code == "conversation_deleted" || code == CodeBadRequest || strings.Contains(err.Error(), "is gone")
}

// keptLocked is kept message mid of conversation sid, to change in memory only (nil: there is none); b.mu is held.
func (s *Service) keptLocked(sid, mid string) *keptMessage {
	q := s.undelivered.st[sid]
	for i := range q {
		if q[i].ID == mid {
			return &q[i]
		}
	}
	return nil
}

// updateKeptLocked changes kept message mid of conversation sid and saves; b.mu is held. A change that could not be
// saved is undone, in memory too, and the error returned: the manager goes on from what a restart would find.
func (s *Service) updateKeptLocked(sid, mid string, f func(*keptMessage)) error {
	m := s.keptLocked(sid, mid)
	if m == nil {
		return nil
	}
	was := *m
	f(m)
	if err := s.saveUndeliveredLocked(); err != nil {
		*m = was
		return err
	}
	return nil
}

// dropKeptLocked takes kept message mid of conversation sid off the list once it went in. A failed save is only
// logged: the saved copy still carries the try's mark, so a manager that starts again only looks for it.
func (s *Service) dropKeptLocked(sid, mid string) {
	q := s.undelivered.st[sid]
	if i := slices.IndexFunc(q, func(k keptMessage) bool { return k.ID == mid }); i >= 0 {
		s.undelivered.st[sid] = slices.Delete(slices.Clone(q), i, i+1)
		if err := s.saveUndeliveredLocked(); err != nil {
			s.logf("save undelivered messages: %v", err)
		}
	}
}

// deliverKept sends kept message k as the owner sent it: SendParams again (what it runs on now, the live snapshot of
// now), then the node's session.send under its own client_msg_id. A try that may have gone in before (the owner's
// Retry after one in doubt) is looked for first. doubt: the send failed but may have gone in (no answer in time, the
// link dropped, the manager stopping during it) and its line is not in the conversation yet; a failure before the
// send, or a refusal, is no doubt.
func (s *Service) deliverKept(ctx context.Context, k keptMessage) (doubt bool, err error) {
	if !k.TriedAt.IsZero() && s.undeliveredLanded(k) {
		s.relayLaterOwnerOutcome(ctx, k.Session, k.ID, true)
		return false, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // a cold start on a busy computer can take minutes
	defer cancel()
	revoked, err := s.keptCredentialRevoked(sctx, k.CredentialID)
	if revoked {
		return false, errKeptSignedOut
	}
	if err != nil {
		return false, errors.Join(errKeptCredentialCheck, err)
	}
	params, err := s.messageParams(withLauncherNoteDiscarded(sctx), SendParams{MachineID: k.Machine, SessionID: k.Session,
		Params: maps.Clone(k.Params), Lang: k.Lang, Human: k.Human, OriginDeviceID: k.OriginDeviceID}, true)
	if err != nil {
		return false, err // nothing was sent
	}
	params["session_id"] = k.Session
	params["client_msg_id"] = k.ID
	laterDone, err := s.withOwnerRelayLater(sctx, k.Machine, k.Session, k.ID, params, k.TriedAt.IsZero())
	if err != nil {
		return false, err // nothing was sent
	}
	laterSettled := false
	defer func() {
		if !laterSettled {
			laterDone(false)
		}
	}()
	s.undelivered.mu.Lock()
	revoked = s.undelivered.revoked[k.CredentialID]
	s.undelivered.mu.Unlock()
	if revoked {
		s.humanSent.voidSend(k.Session, k.ID)
		return false, errKeptSignedOut
	}
	tried := time.Now()
	err = s.call(sctx, k.Machine, DeviceSessionSend, params, nil)
	if err == nil {
		laterSettled = true
		laterDone(true)
		return false, nil
	}
	if !relayInDoubt(err) && sctx.Err() == nil {
		return false, err
	}
	if s.undeliveredLandedSince(k, tried) {
		laterSettled = true
		laterDone(true)
		return false, nil
	}
	return true, err
}

// keptCredentialRevoked combines the hub's answer with any revocation event received during the check.
func (s *Service) keptCredentialRevoked(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	revoked, err := s.hub.CredentialRevoked(ctx, id)
	s.undelivered.mu.Lock()
	defer s.undelivered.mu.Unlock()
	return s.undelivered.revoked[id] || (err == nil && revoked), err
}

// withLauncherNoteDiscarded: a resend has no app to tell that it ran on another launcher.
func withLauncherNoteDiscarded(ctx context.Context) context.Context {
	ctx, _ = withLauncherNote(ctx)
	return ctx
}

// undeliveredLanded says whether kept message k is in its conversation's latest turns already: an earlier try went in.
func (s *Service) undeliveredLanded(k keptMessage) bool {
	return s.undeliveredLandedSince(k, k.TriedAt)
}

// undeliveredLandedSince looks for k's line written no earlier than tried: tied to its client_msg_id, or, the node
// having forgotten the tie (it restarted), the owner's line with its words.
func (s *Service) undeliveredLandedSince(k keptMessage, tried time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), relayCheckWait)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, k.Machine, DeviceSessionItems, map[string]any{"session_id": k.Session, "turns": 3, "lazy_images": true}, &res); err != nil {
		return false
	}
	want := relayWords(k.text())
	for _, it := range res.Items {
		if it.Role != "user" {
			continue
		}
		if it.ClientMsgID == k.ID {
			return true
		}
		if want != "" && it.ClientMsgID == "" && !it.TS.IsZero() && !it.TS.Before(tried.Add(-relayClockSlack)) && strings.Contains(relayWords(it.Text), want) {
			return true
		}
	}
	return false
}

// undeliveredRoutes serve the owner's Retry and Delete on a kept message.
func (s *Service) undeliveredRoutes() {
	find := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role"))
			return "", "", false
		}
		return r.PathValue("id"), r.PathValue("msg"), true
	}
	s.handle("POST /api/v1/assistant/conversations/{id}/undelivered/{msg}/retry", func(w http.ResponseWriter, r *http.Request) {
		sid, mid, ok := find(w, r)
		if !ok {
			return
		}
		b := &s.undelivered
		b.mu.Lock()
		found, signedOut := false, false
		var err error
		if s.undeliveredLocked(r.Context()) {
			if m := s.keptLocked(sid, mid); m != nil {
				found, signedOut = true, m.SignedOut
			}
			if found && !signedOut {
				now := time.Now().UTC()
				err = s.updateKeptLocked(sid, mid, func(m *keptMessage) {
					// A new day of tries from now, sent even after a try in doubt: the owner looked and said so.
					// The line it may have written is still looked for first.
					m.State, m.At, m.NextAt, m.Attempts, m.OwnerRetry = proto.UndeliveredWaiting, now, now, 0, true
					m.LookUntil, m.Looks = time.Time{}, 0
				})
			}
		}
		b.mu.Unlock()
		if signedOut {
			apierr.Write(w, http.StatusConflict, i18n.Err("undelivered.signed_out"))
			return
		}
		if !found {
			if s.logicalOwnerAction(w, r, sid, mid, true) || s.heldAction(w, r, sid, mid, true) {
				return
			}
			apierr.Write(w, http.StatusNotFound, errors.New("no such message"))
			return
		}
		if err != nil {
			s.logf("undelivered: Retry of a kept message to %s is not saved: %v", short(sid), err)
			apierr.Write(w, http.StatusInternalServerError, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.not_saved")))
			return
		}
		s.publish(context.WithoutCancel(r.Context()))
		s.wakeUndelivered()
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	s.handle("DELETE /api/v1/assistant/conversations/{id}/undelivered/{msg}", func(w http.ResponseWriter, r *http.Request) {
		sid, mid, ok := find(w, r)
		if !ok {
			return
		}
		b := &s.undelivered
		b.mu.Lock()
		busy := b.sending[sid]
		b.mu.Unlock()
		if busy {
			// It may be going in this moment: deleting it now could not take it back.
			apierr.Write(w, http.StatusConflict, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.sending_now")))
			return
		}
		if !s.undeliveredDrop(sid, mid) {
			if s.logicalOwnerAction(w, r, sid, mid, false) || s.heldAction(w, r, sid, mid, false) {
				return
			}
			apierr.Write(w, http.StatusNotFound, errors.New("no such message"))
			return
		}
		s.forgetOwnerRelayLater(context.WithoutCancel(r.Context()), sid, mid)
		s.publish(context.WithoutCancel(r.Context()))
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// heldAction is Retry (retry) or Delete on message mid of session sid when none of the conversations keeps it: one an
// Agent's computer's manager holds (internal/devices held.go, #10661). Older apps call these routes for every row's
// undelivered message, whatever kind it is. Only a person's own app may change one (assistantwire.PersonHeader, set by
// the hub): its delivery carries that person's provenance. False when no machine holds it, or another asks: the route
// answers 404 as before.
func (s *Service) heldAction(w http.ResponseWriter, r *http.Request, sid, mid string, retry bool) bool {
	if s.devs == nil || r.Header.Get(assistantwire.PersonHeader) != "1" {
		return false
	}
	act := s.devs.HeldDelete
	if retry {
		act = s.devs.HeldRetry
	}
	found, err := act(r.Context(), sid, mid)
	switch {
	case !found:
		return false
	case err != nil:
		apierr.Call(w, err) // coded for the app: 409 while it is being sent
	default:
		apierr.WriteJSON(w, map[string]any{"ok": true})
	}
	return true
}
