package assistant

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/chatsearch"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The chat search index (#6282, docs/design/proposals/assistant-search.md "Keeping it current"): the messages of the
// conversations "All chats" lists, read from their transcripts after each one's cursor and kept in the manager's
// database (db.SearchItemRow). A sweep finds the conversations whose rev moved since they were read (woken by the
// nodes' session lists, else every searchIdle) and reads them one at a time: a working one at most every
// searchWorkingEvery, a new one (the backfill) newest first with a pause between pages. A transcript that was
// rewritten (its history token changed) is read again from the start; a conversation gone from the list for
// searchGoneAfter leaves the index. An index emptied under it (`assistant search-reindex`, db.ResetSearch) is read
// again from the start too: each sweep first looks whether the resets' count moved since its cursors were read.

// searchStore is what the index keeps in the database; a store without it has no index (search answers titles only).
type searchStore interface {
	SearchCursors(ctx context.Context) ([]db.SearchCursorRow, error)
	PutSearchItems(ctx context.Context, machine, session, history string, afterSeq int, items []db.SearchItemRow, reset bool) error
	DropSearchConversation(ctx context.Context, machine, session string) error
	SearchAssistantItems(ctx context.Context, machines, sessions, words []string, perConversation int) ([]db.SearchHitRow, error)
	SearchReset(ctx context.Context) (int64, error)
	PurgeSearch(ctx context.Context) error
}

var _ searchStore = (*db.DB)(nil)

const (
	searchIdle         = 30 * time.Second // the sweep without a wake-up
	searchMinGap       = time.Second      // between two sweeps, however often woken
	searchWorkingEvery = 5 * time.Second  // a working conversation is read at most this often
	searchBackfillGap  = 200 * time.Millisecond
	searchRetryAfter   = time.Minute    // a conversation whose read failed (its computer offline) waits this long
	searchGoneAfter    = 24 * time.Hour // a conversation absent from the list this long leaves the index
	searchPagesPerRun  = 400            // pages of about 500 items one read takes at most before the next sweep
	searchCallTimeout  = 30 * time.Second
)

type searchKey struct{ machine, session string }

type searchCursor struct {
	history  string
	afterSeq int
}

// searchPage is a session.items answer read after a seq.
type searchPage struct {
	Items     []proto.Item `json:"items"`
	MoreAfter bool         `json:"more_after"`
	History   string       `json:"history"`
}

type searchIndex struct {
	mu      sync.Mutex
	loaded  bool
	reset   int64 // the resets' count its cursors are from (db.SearchReset)
	cursors map[searchKey]searchCursor
	readRev map[searchKey]int64     // each conversation's searchVersion when it was last read through
	readAt  map[searchKey]time.Time // when it was last read
	retry   map[searchKey]time.Time // a failed read waits until then
	gone    map[searchKey]time.Time // since when it has been missing from the list
	wake    chan struct{}
	// For tests: the page reader and the store, instead of the node and the database.
	fetch func(ctx context.Context, machine, session string, afterSeq int) (searchPage, error)
	store searchStore
}

func (x *searchIndex) init() {
	if x.cursors == nil {
		x.cursors, x.readRev, x.readAt = map[searchKey]searchCursor{}, map[searchKey]int64{}, map[searchKey]time.Time{}
		x.retry, x.gone = map[searchKey]time.Time{}, map[searchKey]time.Time{}
	}
	if x.wake == nil {
		x.wake = make(chan struct{}, 1)
	}
}

// searchDB is the index's store, nil when there is none.
func (s *Service) searchDB() searchStore {
	if s.search.store != nil {
		return s.search.store
	}
	if s.todoDB() == nil {
		return nil
	}
	st, _ := s.store().(searchStore)
	return st
}

// searchWake asks for a sweep (the nodes' session lists changed); it never blocks.
func (s *Service) searchWake() {
	s.search.mu.Lock()
	s.search.init()
	wake := s.search.wake
	s.search.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

// searchLoop keeps the index current until ctx ends.
func (s *Service) searchLoop(ctx context.Context) {
	if s.searchDB() == nil {
		return
	}
	s.search.mu.Lock()
	s.search.init()
	wake := s.search.wake
	s.search.mu.Unlock()
	for {
		next := s.searchSweep(ctx)
		t := time.NewTimer(min(next, searchIdle))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
		select { // at most one sweep a second, however busy the lists are
		case <-ctx.Done():
			return
		case <-time.After(searchMinGap):
		}
	}
}

// searchLoad reads the cursors once, and after that the resets' count at each sweep: when the index was reset under
// it, it lets go of what it holds and reads every conversation again from the start. False while the database cannot
// be read.
func (s *Service) searchLoad(ctx context.Context, st searchStore) bool {
	// The count before the cursors: a reset between the two is seen at the next sweep.
	reset, err := st.SearchReset(ctx)
	if err != nil {
		s.logf("assistant: search index: read the resets: %v", err)
		return false
	}
	s.search.mu.Lock()
	loaded, was := s.search.loaded, s.search.reset
	s.search.mu.Unlock()
	if loaded && reset == was {
		return true
	}
	if loaded {
		// What it wrote since the reset went on from cursors from before it: that goes too, or those conversations
		// would keep a cursor past messages the index no longer has.
		if err := st.PurgeSearch(ctx); err != nil {
			s.logf("assistant: search index: empty it after a reset: %v", err)
			return false
		}
		s.logf("assistant: search index: emptied (search-reindex): reading every conversation again")
	}
	rows, err := st.SearchCursors(ctx) // none after a reset
	if err != nil {
		s.logf("assistant: search index: read cursors: %v", err)
		return false
	}
	s.search.mu.Lock()
	defer s.search.mu.Unlock()
	s.search.cursors, s.search.readRev, s.search.readAt, s.search.retry, s.search.gone = nil, nil, nil, nil, nil
	s.search.init()
	for _, r := range rows {
		s.search.cursors[searchKey{r.MachineID, r.SessionID}] = searchCursor{history: r.History, afterSeq: r.AfterSeq}
	}
	s.search.loaded, s.search.reset = true, reset
	return true
}

// searchSweep reads the conversations that changed and says how soon to look again.
func (s *Service) searchSweep(ctx context.Context) time.Duration {
	st := s.searchDB()
	if st == nil || !s.searchLoad(ctx, st) {
		return searchRetryAfter
	}
	list, _ := s.searchConversations(ctx)
	now := time.Now()
	type due struct {
		c      proto.Session
		cur    searchCursor
		backup bool // not read yet: the backfill
	}
	var todo []due
	next := searchIdle
	s.search.mu.Lock()
	s.search.init()
	listed := map[searchKey]bool{}
	for _, c := range list {
		k := searchKey{c.MachineID, c.SessionID}
		listed[k] = true
		delete(s.search.gone, k)
		cur, read := s.search.cursors[k]
		if read && s.search.readRev[k] == searchVersion(c) {
			continue
		}
		if at, ok := s.search.retry[k]; ok && now.Before(at) {
			next = min(next, at.Sub(now))
			continue
		}
		if c.Status == proto.StatusWorking {
			if wait := searchWorkingEvery - now.Sub(s.search.readAt[k]); wait > 0 {
				next = min(next, wait)
				continue
			}
		}
		todo = append(todo, due{c: c, cur: cur, backup: !read})
	}
	// conversations the list no longer has: gone from the index after a while (never while the list is empty: the
	// assistant's computer may just be offline)
	var drop []searchKey
	if len(list) > 0 {
		for k := range s.search.cursors {
			if listed[k] {
				continue
			}
			since, ok := s.search.gone[k]
			if !ok {
				s.search.gone[k] = now
			} else if now.Sub(since) >= searchGoneAfter {
				drop = append(drop, k)
			}
		}
	}
	s.search.mu.Unlock()

	for _, k := range drop {
		if err := st.DropSearchConversation(ctx, k.machine, k.session); err != nil {
			s.logf("assistant: search index: drop a conversation: %v", err)
			continue
		}
		s.search.mu.Lock()
		delete(s.search.cursors, k)
		delete(s.search.readRev, k)
		delete(s.search.readAt, k)
		delete(s.search.gone, k)
		s.search.mu.Unlock()
	}
	// what changed first (new messages in a chat already read), then the backfill, the latest updated first
	sort.SliceStable(todo, func(i, j int) bool {
		if todo[i].backup != todo[j].backup {
			return !todo[i].backup
		}
		return todo[i].c.UpdatedAt.After(todo[j].c.UpdatedAt)
	})
	for _, d := range todo {
		if ctx.Err() != nil {
			return next
		}
		k := searchKey{d.c.MachineID, d.c.SessionID}
		cur, done, err := s.searchRead(ctx, st, d.c, d.cur, d.backup)
		s.search.mu.Lock()
		if cur != d.cur {
			s.search.cursors[k] = cur
		}
		if err != nil {
			s.search.retry[k] = time.Now().Add(searchRetryAfter)
			s.logf("assistant: search index: read a conversation on %s: %v", d.c.MachineID, err)
		} else {
			delete(s.search.retry, k)
			s.search.readAt[k] = time.Now()
			if done {
				s.search.readRev[k] = searchVersion(d.c)
			}
		}
		s.search.mu.Unlock()
		if err == nil && !done {
			next = 0 // more to read: come back at once
		}
	}
	return next
}

// searchRead reads a conversation's messages after its cursor into the index, page by page; done: it reached the end.
func (s *Service) searchRead(ctx context.Context, st searchStore, c proto.Session, cur searchCursor, backfill bool) (searchCursor, bool, error) {
	reset := false
	for range searchPagesPerRun {
		page, err := s.searchFetch(ctx, c.MachineID, c.SessionID, cur.afterSeq)
		if err != nil {
			return cur, false, err
		}
		if !reset && cur.history != "" && page.History != "" && page.History != cur.history {
			// the transcript was replaced or rewritten: its seqs start over, and so does the index of it
			cur, reset = searchCursor{history: page.History}, true
			continue
		}
		rows, last := searchRows(page.Items)
		after := max(cur.afterSeq, last)
		history := page.History
		if history == "" {
			history = cur.history
		}
		if err := st.PutSearchItems(ctx, c.MachineID, c.SessionID, history, after, rows, reset); err != nil {
			return cur, false, err
		}
		cur, reset = searchCursor{history: history, afterSeq: after}, false
		if !page.MoreAfter || len(page.Items) == 0 {
			return cur, true, nil
		}
		if backfill {
			select {
			case <-ctx.Done():
				return cur, false, ctx.Err()
			case <-time.After(searchBackfillGap):
			}
		}
	}
	return cur, false, nil
}

// searchFetch is one page of a conversation's items after a seq: unfolded, without image data.
func (s *Service) searchFetch(ctx context.Context, machine, session string, afterSeq int) (searchPage, error) {
	if f := s.search.fetch; f != nil {
		return f(ctx, machine, session, afterSeq)
	}
	ctx, cancel := context.WithTimeout(ctx, searchCallTimeout)
	defer cancel()
	var p searchPage
	err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": session, "after_seq": afterSeq, "lazy_images": true}, &p)
	return p, err
}

// searchVersion says whether a conversation changed since it was read: its rev (the transcript's size), else when it
// was last updated (a node that sends no rev).
func searchVersion(c proto.Session) int64 {
	if c.Rev != 0 {
		return c.Rev
	}
	return c.UpdatedAt.UnixNano()
}

// searchRows is what the index keeps of a page (chatsearch.IndexText), and the page's last seq.
func searchRows(items []proto.Item) ([]db.SearchItemRow, int) {
	var rows []db.SearchItemRow
	last := 0
	for _, it := range items {
		last = max(last, it.Seq)
		text, ok := chatsearch.IndexText(it)
		if !ok {
			continue
		}
		rows = append(rows, db.SearchItemRow{Seq: it.Seq, ItemID: it.ID, Role: it.Role, TS: it.TS, Body: text, Folded: chatsearch.Fold(text)})
	}
	return rows, last
}

// searchConversations is what the index reads and a search looks in: the conversations "All chats" lists (the archived
// ones too, never the health space's), each with its computer (a node's list may not name it: the assistant's).
func (s *Service) searchConversations(ctx context.Context) ([]proto.Session, string) {
	list, main := s.conversations(ctx, true, "")
	here := s.settings(ctx).MachineID
	for i := range list {
		if list[i].MachineID == "" {
			list[i].MachineID = here
		}
	}
	return list, main
}

// searchProgress is how many of list are indexed (read at least once).
func (s *Service) searchProgress(list []proto.Session) int {
	s.search.mu.Lock()
	defer s.search.mu.Unlock()
	n := 0
	for _, c := range list {
		if _, ok := s.search.cursors[searchKey{c.MachineID, c.SessionID}]; ok {
			n++
		}
	}
	return n
}
