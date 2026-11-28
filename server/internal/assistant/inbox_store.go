package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// inboxStore is the database's inbox (table inbox_items); a store without it keeps the inbox in memory. It keeps the
// done items for inboxKeepHistory (GET /api/v1/inbox/done pages them); the manager holds the open ones and those done
// within inboxForget.
type inboxStore interface {
	PutInboxItem(ctx context.Context, id string, data []byte, updatedAt time.Time, doneAt *time.Time) error
	// InboxItems hands each open item, and each one done since doneSince, to each (raw is valid only during the call).
	InboxItems(ctx context.Context, doneSince time.Time, each func(raw []byte) error) error
	// StoredInboxSources names every source retained, including completed items older than InboxItems holds.
	StoredInboxSources(ctx context.Context) ([]string, error)
	DoneInboxItems(ctx context.Context, q db.InboxDoneQuery) ([]db.InboxRow, error)
	DoneInboxDays(ctx context.Context, zone string, connectors bool) ([]db.InboxDay, error)
	DoneInboxItemsByID(ctx context.Context, ids []string) ([][]byte, error)
	PurgeInboxItems(ctx context.Context, doneBefore time.Time) (int64, error)
	DeleteInboxSource(ctx context.Context, source string, ids []string) ([]string, error)
}

// inboxBatchStore is an inboxStore that writes many items in one round trip (the database's PutInboxItems).
type inboxBatchStore interface {
	PutInboxItems(ctx context.Context, rows []db.InboxRow) error
}

type conversationInboxStore interface {
	PutConversationInboxItems(ctx context.Context, machine, session string, rows []db.InboxRow) error
}

func (s *Service) inboxDB() inboxStore {
	st, _ := s.store().(inboxStore)
	return st
}

// errInboxUnavailable: what the inbox keeps could not be read (CodeInboxUnavailable).
var errInboxUnavailable = &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "收件箱暂时读不到存着的内容，稍后再试"}

// inboxReady loads what the inbox keeps (its items and mutes) the first time anything needs it. A load that failed
// (the database did not answer) is errInboxUnavailable and is tried again the next time: until one goes through
// nothing is taken or written, so the items and mutes stored are not overwritten with what memory has.
func (s *Service) inboxReady(ctx context.Context) error {
	b := &s.inbox
	b.init()
	if b.loaded.Load() {
		return nil
	}
	b.loadMu.Lock()
	defer b.loadMu.Unlock()
	if b.loaded.Load() {
		return nil
	}
	if !s.loadInbox(ctx) {
		return errInboxUnavailable
	}
	b.loaded.Store(true)
	s.tellInboxNeedsYou() // the hub was told none while it was unread
	return nil
}

// inboxItemIsGroup reports whether the referenced item is from a group chat, including older Lark items.
func (s *Service) inboxItemIsGroup(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	if err := s.inboxReady(ctx); err != nil {
		return false, err
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[id]
	return it != nil && (it.Kind == KindGroup || it.Kind == KindMention || it.Lark != nil && it.Lark.IsGroup), nil
}

// inboxUp is inboxReady for a route: false when it answered 503 inbox_unavailable.
func (s *Service) inboxUp(w http.ResponseWriter, r *http.Request) bool {
	if err := s.inboxReady(r.Context()); err != nil {
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeInboxUnavailable, err)
		return false
	}
	return true
}

// loadInbox reads the items still remembered and the mutes; false when the database could not be read.
func (s *Service) loadInbox(ctx context.Context) bool {
	b := &s.inbox
	if !s.loadInboxMutes(ctx) || !s.loadInboxPolicies(ctx) || !s.loadInboxFolders(ctx) || !s.loadMailBaselines(ctx) {
		return false
	}
	st := s.inboxDB()
	if st == nil {
		return true
	}
	now := time.Now()
	s.purgeInboxHistory(ctx, st, now)
	// Each row is decoded straight from the driver's buffer (json.Unmarshal copies every string and json.RawMessage it
	// keeps), so the rows' bytes are never all held besides the items; nothing is taken in until the read has ended.
	var loaded []*InboxItem
	err := st.InboxItems(ctx, now.Add(-inboxForget), func(raw []byte) error {
		var it InboxItem
		if json.Unmarshal(raw, &it) == nil && it.ID != "" {
			loaded = append(loaded, &it)
		}
		return nil
	})
	if err != nil {
		s.logf("inbox: load: %v", err)
		return false
	}
	cfg := s.inboxConfig(ctx)
	b.knowExtensions(cfg.Extensions)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.larkAccount = cfg.LarkAccount
	for _, it := range loaded {
		if _, have := b.items[it.ID]; have {
			continue // newer in memory than in the database
		}
		b.stored(it)
		b.items[it.ID] = it
	}
	return true
}

// stored brings an item read from the database up to what the inbox holds now, under b.mu: the Lark account the apps
// pass for it, and what rows from before arrivals and chains lack.
func (b *inboxBook) stored(it *InboxItem) {
	b.larkRequestAccount(it)
	for _, a := range it.SuggestionAttempts {
		if a != nil && a.Status == suggestionSending {
			// No process-local sender survived loading this row. The original identity stays reserved, but its
			// delivery must be checked in the source rather than retried under a new caller UUID.
			a.Status, a.Retryable, a.active = suggestionUnknown, false, false
		}
	}
	if it.ArrivedOf == "" { // stored before arrivals were stamped
		it.ArrivedOf, it.ArrivedAt = it.Last, it.UpdatedAt
	}
	if len(it.Chain) == 0 { // stored before chains: received and triaged, from its fields
		it.Chain = synthesizedChain(b.userLang(), it)
		it.ChainSeq = len(it.Chain)
	}
	// Loading a previously linked item is not a new message. Resume the process-local signal cursor at the
	// stored snapshot; new batches collected after loading still change Last and reach the supervisor.
	if a := it.Agent; a != nil && a.TodoID != "" {
		_, a.signaled = receivedSince(it, 0, false)
		a.signaledOf = it.Last
	}
}

// purgeInboxHistory deletes the items done more than inboxKeepHistory ago: at load, then from the rounds at most every
// inboxPurgeEvery. One that fails is only logged; the next one deletes them.
func (s *Service) purgeInboxHistory(ctx context.Context, st inboxStore, now time.Time) {
	b := &s.inbox
	b.mu.Lock()
	due := now.Sub(b.purgedAt) >= inboxPurgeEvery
	if due {
		b.purgedAt = now
	}
	b.mu.Unlock()
	if !due {
		return
	}
	if _, err := st.PurgeInboxItems(ctx, now.Add(-inboxKeepHistory)); err != nil {
		s.logf("inbox: purge the done items past %s: %v", inboxKeepHistory, err)
	}
}

// saveInbox persists changed items and tells the hub (it tells the extensions and the apps). It returns the ids
// whose rows could not be written (the in-memory state of those is the caller's to roll back: with saveInboxUndo).
func (s *Service) saveInbox(ctx context.Context, changed []*InboxItem) map[string]bool {
	return s.saveInboxUndo(ctx, changed, nil)
}

// saveInboxUndo is saveInbox with the rollback of what could not be written: undo runs with the failed ids, b.mu
// held, before the next save may start. Undone after b.saving, a save queued behind the failed one (the store back
// by then) would write the change first and the undo would leave memory behind the row until a restart (#4924).
func (s *Service) saveInboxUndo(ctx context.Context, changed []*InboxItem, undo func(failed map[string]bool)) map[string]bool {
	return s.saveInboxPrepared(ctx, changed, nil, undo)
}

// saveInboxPrepared can stage a detached row and publish its committed effects under mu before saving is released.
// prepare runs under mu; finish runs there after the store answers, including when every row succeeded. Every change
// of an item reaches the database here, so this is also where GET /api/v1/inbox forgets the changed items' kept bytes
// (inbox_list_memo.go): first, before waiting for another save, so a GET meanwhile lists what memory holds; as the rows
// are taken; and after finish, which may undo a change.
func (s *Service) saveInboxPrepared(ctx context.Context, changed []*InboxItem, prepare func(*InboxItem) *InboxItem, finish func(map[string]bool)) map[string]bool {
	b := &s.inbox
	_, post := ctx.Value(inboxPostConversation{}).([2]string)
	if len(changed) == 0 {
		return nil
	}
	b.mu.Lock()
	b.forgetListed(changed)
	b.mu.Unlock()
	if !b.loaded.Load() { // not read yet: what is stored stays as it is (inboxReady)
		s.logf("inbox: %d changed items not saved: what is stored was not read", len(changed))
		return nil
	}
	var told []assistantwire.InboxChangedItem
	b.saving.Lock()
	b.mu.Lock()
	b.forgetListed(changed)
	rows := make([]db.InboxRow, 0, len(changed))
	events := map[string][]assistantwire.ExternalInboxEvent{}
	now := time.Now()
	for i, it := range changed {
		if current := b.items[it.ID]; current != it {
			if !post || prepare == nil || it.Source != inboxPostSource || it.Kind != inboxPostKind {
				continue // generic saves still refuse forgotten/replaced rows; only a typed post stages a detached candidate
			}
		}
		it.arrived(now)
		row := it
		if prepare != nil {
			row = prepare(it)
		} else {
			row, _ = b.filingSnapshot(it, nil)
		}
		if post {
			row.arrived(now) // the staged post, never the previously committed row
		}
		if !it.externalChecking && len(it.externalMoves) != 0 {
			events[it.ID] = slices.Clone(it.externalMoves)
		}
		raw, _ := json.Marshal(row)
		rows = append(rows, db.InboxRow{ID: row.ID, Data: raw, UpdatedAt: row.UpdatedAt, DoneAt: row.DoneAt})
		if i < 50 { // a first round's hundreds: the plugin reads the inbox if it wants them all
			told = append(told, assistantwire.InboxChangedItem{ID: it.ID, Source: it.Source, Kind: it.Kind, Title: it.Title})
		}
	}
	b.mu.Unlock()
	if len(rows) == 0 {
		b.saving.Unlock()
		return nil
	}
	var failed map[string]bool
	if st := s.inboxDB(); st != nil {
		failed = s.putInboxRows(ctx, st, rows, events, now)
	}
	if finish != nil || len(events) != 0 {
		b.mu.Lock()
		if finish != nil {
			finish(failed)
			b.forgetListed(changed)
		}
		for _, it := range changed {
			if sent := events[it.ID]; len(sent) > 0 && !failed[it.ID] {
				it.externalMoves = slices.DeleteFunc(it.externalMoves, func(e assistantwire.ExternalInboxEvent) bool {
					return slices.ContainsFunc(sent, func(saved assistantwire.ExternalInboxEvent) bool { return e.Key == saved.Key })
				})
			}
		}
		b.mu.Unlock()
	}
	b.saving.Unlock()
	if len(failed) > 0 {
		// What was not stored is not announced: the apps would otherwise drop items the database still holds open.
		told = slices.DeleteFunc(told, func(i assistantwire.InboxChangedItem) bool { return failed[i.ID] })
	}
	s.inboxChanged(ctx, told)
	if len(events) > 0 {
		select {
		case b.externalWake <- struct{}{}:
		default:
		}
	}
	return failed
}

// putInboxRows writes rows in one go when the store can (a round's hundreds are one round trip, not hundreds); when
// that fails, or the store can't, each row is written on its own so one that fails does not keep the others out. It
// returns the ids that were not written anywhere.
func (s *Service) putInboxRows(ctx context.Context, st inboxStore, rows []db.InboxRow, events map[string][]assistantwire.ExternalInboxEvent, now time.Time) map[string]bool {
	if source, post := ctx.Value(inboxPostConversation{}).([2]string); post {
		if guarded, ok := st.(conversationInboxStore); ok {
			if err := guarded.PutConversationInboxItems(ctx, source[0], source[1], rows); err != nil {
				s.logf("inbox: save assistant post: %v", err)
				failed := make(map[string]bool, len(rows))
				for _, row := range rows {
					failed[row.ID] = true
				}
				return failed // never retry a rejected post through the unguarded writer
			}
			return nil
		}
	}
	if len(events) != 0 {
		if routing, ok := st.(externalInboxStore); ok {
			var all []assistantwire.ExternalInboxEvent
			for _, row := range rows {
				all = append(all, events[row.ID]...)
			}
			if err := routing.SaveExternalInboxItems(ctx, rows, all, now); err == nil {
				return nil
			} else {
				s.logf("inbox: persist external routing receipts: %v (one by one)", err)
			}
			failed := map[string]bool{}
			for _, row := range rows {
				if err := routing.SaveExternalInboxItems(ctx, []db.InboxRow{row}, events[row.ID], now); err != nil {
					s.logf("inbox: save %s with source receipts: %v", row.ID, err)
					failed[row.ID] = true
				}
			}
			return failed
		}
	}
	if bs, ok := st.(inboxBatchStore); ok && len(rows) > 1 {
		err := bs.PutInboxItems(ctx, rows)
		if err == nil {
			return nil
		}
		s.logf("inbox: save %d items at once: %v (one by one)", len(rows), err)
	}
	var failed map[string]bool
	for _, r := range rows {
		if err := st.PutInboxItem(ctx, r.ID, r.Data, r.UpdatedAt, r.DoneAt); err != nil {
			s.logf("inbox: save %s: %v", r.ID, err)
			if failed == nil {
				failed = map[string]bool{}
			}
			failed[r.ID] = true
		}
	}
	return failed
}

// inboxChanged tells the hub what changed (EventInboxChanged): it tells the extensions and the apps.
func (s *Service) inboxChanged(ctx context.Context, items []assistantwire.InboxChangedItem) {
	if items == nil {
		items = []assistantwire.InboxChangedItem{}
	}
	s.hub.InboxChanged(ctx, assistantwire.InboxChanged{Items: items})
	s.tellInboxNeedsYou()
}

// inboxTeller is tellInboxNeedsYou's state, under inboxBook.needsYouMu: a teller runs (telling), and the inbox changed
// since it last counted (again). A teller reads s.hub only under needsYouMu too, so a test can swap the hub while one
// runs (setHub); nothing else changes it after New.
type inboxTeller struct{ telling, again bool }

// tellInboxNeedsYou tells the hub how many open items are in "Needs you" (EventInboxNeedsYou), for the roster's
// inbox_needs_you: after every change (inboxChanged), once what is stored was first read (inboxReady), and after each
// handshake (BusHub.onHello). Nothing is told while the stored items are unread: the hub then shows no number.
//
// It never waits for the hub: one teller at a time counts and tells on its own goroutine, and a change that comes
// while it does is counted and told by it afterwards. So a save's tail is not held up by a connection that stalls,
// the counts reach the hub in the order they were counted, and the last one told is of the inbox as it is.
func (s *Service) tellInboxNeedsYou() {
	b := &s.inbox
	if !b.loaded.Load() {
		return
	}
	b.needsYouMu.Lock()
	if b.needsYou.telling {
		b.needsYou.again = true
		b.needsYouMu.Unlock()
		return
	}
	b.needsYou.telling = true
	hub := s.hub
	b.needsYouMu.Unlock()
	go func() {
		for {
			n := 0
			b.mu.Lock()
			cutoff := time.Now().Add(-inboxKeepDone)
			duplicates := b.larkDuplicates(cutoff, b.listedDoneCut(cutoff, true), true)
			for _, it := range b.items {
				if !duplicates.hidden[it.ID] && it.status() == StatusAction {
					n++
				}
			}
			b.mu.Unlock()
			_ = hub.Tell(assistantwire.EventInboxNeedsYou, assistantwire.InboxNeedsYou{Count: n}) // an error: not connected, told at the handshake
			b.needsYouMu.Lock()
			if !b.needsYou.again {
				b.needsYou.telling = false
				b.needsYouMu.Unlock()
				return
			}
			b.needsYou.again = false
			hub = s.hub
			b.needsYouMu.Unlock()
		}
	}()
}
