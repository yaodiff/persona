package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/chatsearch"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// memSearch is the index's store in memory, answering as the SQL does (db.SearchAssistantItems).
type memSearch struct {
	mu      sync.Mutex
	rows    map[searchKey][]db.SearchItemRow
	cursors map[searchKey]db.SearchCursorRow
	resets  int
	fail    error
	emptied int // db.ResetSearch's count (search-reindex)
	purges  int // PurgeSearch calls
}

// resetIndex is db.ResetSearch: `assistant search-reindex` emptying the index under a manager that runs.
func (m *memSearch) resetIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows, m.cursors = map[searchKey][]db.SearchItemRow{}, map[searchKey]db.SearchCursorRow{}
	m.emptied++
}

func (m *memSearch) SearchReset(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(m.emptied), nil
}

func (m *memSearch) PurgeSearch(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows, m.cursors = map[searchKey][]db.SearchItemRow{}, map[searchKey]db.SearchCursorRow{}
	m.purges++
	return nil
}

func newMemSearch() *memSearch {
	return &memSearch{rows: map[searchKey][]db.SearchItemRow{}, cursors: map[searchKey]db.SearchCursorRow{}}
}

func (m *memSearch) SearchCursors(context.Context) ([]db.SearchCursorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.SearchCursorRow
	for _, c := range m.cursors {
		out = append(out, c)
	}
	return out, nil
}

func (m *memSearch) PutSearchItems(_ context.Context, machine, session, history string, after int, items []db.SearchItemRow, reset bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := searchKey{machine, session}
	if reset {
		m.resets++
		delete(m.rows, k)
	}
	for _, it := range items {
		m.rows[k] = slices.DeleteFunc(m.rows[k], func(r db.SearchItemRow) bool { return r.Seq == it.Seq && r.ItemID == it.ItemID })
		m.rows[k] = append(m.rows[k], it)
	}
	m.cursors[k] = db.SearchCursorRow{MachineID: machine, SessionID: session, History: history, AfterSeq: after}
	return nil
}

func (m *memSearch) DropSearchConversation(_ context.Context, machine, session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, searchKey{machine, session})
	delete(m.cursors, searchKey{machine, session})
	return nil
}

func (m *memSearch) SearchAssistantItems(_ context.Context, machines, sessions, words []string, per int) ([]db.SearchHitRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	var out []db.SearchHitRow
	for i := range machines {
		k := searchKey{machines[i], sessions[i]}
		var hits []db.SearchItemRow
		for _, r := range m.rows[k] {
			if chatsearch.Holds(r.Folded, words) {
				hits = append(hits, r)
			}
		}
		sort.Slice(hits, func(a, b int) bool { return hits[a].Seq > hits[b].Seq })
		for n, r := range hits {
			if n == per {
				break
			}
			out = append(out, db.SearchHitRow{MachineID: k.machine, SessionID: k.session, Seq: r.Seq, ItemID: r.ItemID, Role: r.Role, TS: r.TS, Body: r.Body, Hits: len(hits)})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].TS.After(out[b].TS) })
	return out, nil
}

// chatFeed is the transcripts the fake node pages after a seq, by conversation, with their history tokens.
type chatFeed struct {
	mu      sync.Mutex
	items   map[string][]proto.Item
	history map[string]string
	page    int
	reads   []string // "session after"
	offline bool
}

func (c *chatFeed) fetch(_ context.Context, _, session string, after int) (searchPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offline {
		return searchPage{}, errors.New("computer offline")
	}
	c.reads = append(c.reads, fmt.Sprintf("%s %d", session, after))
	var page []proto.Item
	for _, it := range c.items[session] {
		if it.Seq > after {
			page = append(page, it)
		}
	}
	more := c.page > 0 && len(page) > c.page
	if more {
		page = page[:c.page]
	}
	return searchPage{Items: page, MoreAfter: more, History: c.history[session]}, nil
}

var searchT0 = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func msg(seq int, role, text string, extra ...func(*proto.Item)) proto.Item {
	it := proto.Item{ID: fmt.Sprintf("i%d#0", seq), Seq: seq, Role: role, Kind: proto.KindText, Text: text, TS: searchT0.Add(time.Duration(seq) * time.Minute)}
	for _, f := range extra {
		f(&it)
	}
	return it
}

// searchService has the main conversation (main), a trip chat (trip), an archived one (old), a chat not named yet
// (fresh) and a health conversation (body), each with a transcript; the index reads them through feed.
func searchService(t *testing.T) (*Service, *chatFeed, *memSearch) {
	s, feed, store, _ := searchFixture(t)
	return s, feed, store
}

// chatList is the assistant computer's session list, changed in place and handed to the fake hub again.
type chatList struct {
	f        *fakeHub
	sessions []proto.Session
}

func (l *chatList) edit(t *testing.T, id string, change func(*proto.Session)) {
	t.Helper()
	for i := range l.sessions {
		if l.sessions[i].SessionID == id {
			change(&l.sessions[i])
			l.f.setSessions("server", l.sessions...)
			return
		}
	}
	t.Fatalf("no session %s", id)
}

func searchFixture(t *testing.T) (*Service, *chatFeed, *memSearch, *chatList) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "main"}
	list := &chatList{f: f, sessions: []proto.Session{
		proto.Session{SessionID: "main", Title: "琥珀灯塔 not shown", Assistant: true, Rev: 1, UpdatedAt: searchT0.Add(time.Hour)},
		proto.Session{SessionID: "trip", Title: "周末出行 Trip plan", Assistant: true, Rev: 1, UpdatedAt: searchT0.Add(2 * time.Hour), Origin: "user"},
		proto.Session{SessionID: "old", Title: "Old trip", Assistant: true, Archived: true, Rev: 1, UpdatedAt: searchT0},
		proto.Session{SessionID: "fresh", Title: "Claude Code", Assistant: true, Rev: 1, UpdatedAt: searchT0},
		proto.Session{SessionID: "body", Title: "Sleep", Assistant: true, Space: "health", Rev: 1, UpdatedAt: searchT0},
		proto.Session{SessionID: "agent", Title: "An agent's session", Rev: 1, UpdatedAt: searchT0},
	}}
	f.setSessions("server", list.sessions...)
	feed := &chatFeed{items: map[string][]proto.Item{
		"main": {msg(1, "user", "提醒我买机票"), msg(2, "assistant", "好的，明早九点提醒你买机票。")},
		"trip": {
			msg(1, "user", "我想去青石巷 BlueLane 看看"),
			{ID: "t2#0", Seq: 2, Role: "assistant", Kind: proto.KindThinking, Text: "ThinkWord BlueLane", TS: searchT0},
			{ID: "t3#0", Seq: 3, Role: "user", Kind: proto.KindToolResult, Text: "CopperKey BlueLane", TS: searchT0},
			msg(4, "user", "选 B\n<persona-action view=\"v\" action=\"b\">{\"values\":{\"code\":\"ActionSecret\"}}</persona-action>"),
			msg(5, "user", "InjectedRaw BlueLane", func(it *proto.Item) {
				it.Injected = &proto.Injected{Kind: proto.InjectedSystem, State: "supervise", Line: "示例任务验收"}
			}),
			msg(6, "assistant", "WisteriaTea 紫藤茶馆 在巷口，BlueLane 那头。"),
		},
		"old":   {msg(1, "assistant", "旧的行程：BlueLane 不去了。")},
		"fresh": {msg(1, "user", "Claude Code 怎么装")},
		"body":  {msg(1, "user", "BlueLane 睡眠")},
		"agent": {msg(1, "user", "BlueLane in an agent")},
	}, history: map[string]string{}}
	store := newMemSearch()
	s := newTestService(t, f)
	s.search.fetch, s.search.store = feed.fetch, store
	return s, feed, store, list
}

func searchFor(t *testing.T, s *Service, q string) searchAnswer {
	t.Helper()
	return s.searchChats(context.Background(), q, searchLimit)
}

func found(a searchAnswer) []string {
	var out []string
	for _, r := range a.Results {
		out = append(out, fmt.Sprintf("%s title=%v hits=%d", r.SessionID, r.TitleMatch, len(r.Hits)))
	}
	return out
}

// The index reads "All chats"' conversations (the archived one too, never the health space or an agent's session)
// and keeps the user's words and the replies; a search finds titles first, then messages by the newest hit.
func TestSearchFindsChatsByWhatWasSaid(t *testing.T) {
	s, feed, _ := searchService(t)
	before := searchFor(t, s, "bluelane")
	if !before.Partial || before.Indexed.Done != 0 || before.Indexed.Total != 4 {
		t.Fatalf("before indexing: partial=%v indexed=%+v", before.Partial, before.Indexed)
	}
	s.searchSweep(context.Background())
	for _, r := range feed.reads {
		if strings.HasPrefix(r, "body ") || strings.HasPrefix(r, "agent ") {
			t.Fatalf("read %q: not a conversation of All chats", r)
		}
	}

	a := searchFor(t, s, "BLUELANE")
	if a.Partial || a.Indexed.Done != 4 {
		t.Fatalf("after indexing: partial=%v indexed=%+v", a.Partial, a.Indexed)
	}
	if got, want := found(a), []string{"trip title=false hits=2", "old title=false hits=1"}; !slices.Equal(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	trip := a.Results[0]
	if trip.Hits[0].ItemID != "i6#0" || trip.Hits[1].ItemID != "i1#0" || trip.Hits[1].Role != "user" {
		t.Fatalf("hits %+v: want the reply then the user's words, newest first", trip.Hits)
	}
	if h := trip.Hits[1]; h.Snippet != "我想去青石巷 BlueLane 看看" || !slices.Equal(h.Ranges, [][2]int{{7, 15}}) {
		t.Fatalf("snippet %q ranges %v", h.Snippet, h.Ranges)
	}
	if !a.Results[1].Archived || trip.Origin != "user" {
		t.Fatalf("archived %v origin %q", a.Results[1].Archived, trip.Origin)
	}
	// thoughts, tools, hidden tags and Persona's words for the model are not in it
	for _, q := range []string{"ThinkWord", "CopperKey", "ActionSecret", "persona-action", "InjectedRaw", "示例任务验收"} {
		if r := searchFor(t, s, q); len(r.Results) != 0 {
			t.Errorf("%s found %v", q, found(r))
		}
	}
	// a title first (starting with the query above holding it), the main chat never by its title
	if got := found(searchFor(t, s, "trip")); !slices.Equal(got, []string{"trip title=true hits=0", "old title=true hits=0"}) {
		t.Fatalf("titles: %v", got)
	}
	if got := found(searchFor(t, s, "琥珀灯塔")); got != nil {
		t.Fatalf("the main chat was found by a title it does not show: %v", got)
	}
	if got := found(searchFor(t, s, "机票")); !slices.Equal(got, []string{"main title=false hits=2"}) {
		t.Fatalf("the main chat by its messages: %v", got)
	}
	if got := found(searchFor(t, s, "claude code")); !slices.Equal(got, []string{"fresh title=false hits=1"}) {
		t.Fatalf("a chat not named yet is found by its words, not its placeholder title: %v", got)
	}
	// every word in one message
	if got := found(searchFor(t, s, "紫藤 bluelane")); !slices.Equal(got, []string{"trip title=false hits=1"}) {
		t.Fatalf("two words: %v", got)
	}
	if got := found(searchFor(t, s, "紫藤 看看")); got != nil {
		t.Fatalf("words from two messages matched: %v", got)
	}
}

// What a conversation gains is read after its cursor; a rewritten transcript is read again from the start; a read
// that fails waits; a working conversation is read at most every few seconds.
func TestSearchIndexKeepsUp(t *testing.T) {
	s, feed, store, list := searchFixture(t)
	feed.page = 2
	ctx := context.Background()
	for s.searchSweep(ctx) == 0 { // the trip chat takes several pages
	}
	if c := store.cursors[searchKey{"server", "trip"}]; c.AfterSeq != 6 {
		t.Fatalf("trip read to %d, want 6", c.AfterSeq)
	}
	// nothing changed: nothing read
	feed.reads = nil
	s.searchSweep(ctx)
	if len(feed.reads) != 0 {
		t.Fatalf("read %v with nothing changed", feed.reads)
	}

	// a new message: read after the cursor only
	feed.items["trip"] = append(feed.items["trip"], msg(7, "user", "再加一站 GlacierPost"))
	list.edit(t, "trip", func(c *proto.Session) { c.Rev = 2 })
	s.searchSweep(ctx)
	if !slices.Equal(feed.reads, []string{"trip 6"}) {
		t.Fatalf("reads %v, want only the trip chat after 6", feed.reads)
	}
	if got := found(searchFor(t, s, "glacierpost")); !slices.Equal(got, []string{"trip title=false hits=1"}) {
		t.Fatalf("the new message: %v", got)
	}

	// rewritten: a new history token, read again from 0, and what is gone from the transcript goes from the index
	feed.history["trip"] = "h1"
	store.cursors[searchKey{"server", "trip"}] = db.SearchCursorRow{MachineID: "server", SessionID: "trip", History: "h0", AfterSeq: 7}
	s.search.mu.Lock()
	s.search.cursors[searchKey{"server", "trip"}] = searchCursor{history: "h0", afterSeq: 7}
	s.search.mu.Unlock()
	feed.items["trip"] = []proto.Item{msg(1, "user", "重新开始的行程")}
	list.edit(t, "trip", func(c *proto.Session) { c.Rev = 3 })
	feed.reads = nil
	s.searchSweep(ctx)
	if store.resets != 1 || !slices.Equal(feed.reads, []string{"trip 7", "trip 0"}) {
		t.Fatalf("resets %d reads %v", store.resets, feed.reads)
	}
	if got := found(searchFor(t, s, "glacierpost")); got != nil {
		t.Fatalf("a rewritten transcript's old words are still found: %v", got)
	}

	// offline: waits, then reads
	feed.offline = true
	list.edit(t, "trip", func(c *proto.Session) { c.Rev = 4 })
	s.searchSweep(ctx)
	feed.offline = false
	feed.reads = nil
	s.searchSweep(ctx)
	if len(feed.reads) != 0 {
		t.Fatalf("read %v right after a failure", feed.reads)
	}
	s.search.mu.Lock()
	s.search.retry[searchKey{"server", "trip"}] = time.Now().Add(-time.Second)
	s.search.mu.Unlock()
	s.searchSweep(ctx)
	if !slices.Equal(feed.reads, []string{"trip 1"}) {
		t.Fatalf("reads %v once the wait is over", feed.reads)
	}

	// working: at most every searchWorkingEvery
	list.edit(t, "trip", func(c *proto.Session) { c.Rev, c.Status = 5, proto.StatusWorking })
	feed.reads = nil
	s.searchSweep(ctx)
	if len(feed.reads) != 0 {
		t.Fatalf("a working chat read again at once: %v", feed.reads)
	}
}

// An index emptied under a running manager (search-reindex, #6340) is read again from the start at the next sweep:
// what the manager wrote meanwhile from its old cursors goes too, even when no conversation can be read again yet.
func TestSearchIndexReadsAgainAfterAReset(t *testing.T) {
	s, feed, store, _ := searchFixture(t)
	var logged []string
	s.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	ctx := context.Background()
	for s.searchSweep(ctx) == 0 {
	}
	if store.purges != 0 || len(store.cursors) != 4 {
		t.Fatalf("indexing purged %d times, %d cursors", store.purges, len(store.cursors))
	}
	feed.reads = nil
	s.searchSweep(ctx)
	if len(feed.reads) != 0 || store.purges != 0 {
		t.Fatalf("no reset: read %v, purged %d times", feed.reads, store.purges)
	}

	store.resetIndex()
	// the manager wrote one more page from the cursor it held before it saw the reset
	if err := store.PutSearchItems(ctx, "server", "trip", "", 9, []db.SearchItemRow{{Seq: 9, ItemID: "i9#0", Role: "user",
		Body: "StaleWord", Folded: "staleword"}}, false); err != nil {
		t.Fatal(err)
	}
	feed.offline = true
	s.searchSweep(ctx)
	if store.purges != 1 || len(store.cursors) != 0 || len(store.rows) != 0 {
		t.Fatalf("after the sweep that saw the reset: purged %d times, cursors %v, rows %v", store.purges, store.cursors, store.rows)
	}
	if a := searchFor(t, s, "bluelane"); !a.Partial || a.Indexed.Done != 0 {
		t.Fatalf("not read again yet: partial=%v indexed=%+v", a.Partial, a.Indexed)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "emptied (search-reindex)") }) {
		t.Fatalf("logged %q", logged)
	}

	feed.offline = false
	s.search.mu.Lock()
	clear(s.search.retry)
	s.search.mu.Unlock()
	feed.reads = nil
	for s.searchSweep(ctx) == 0 {
	}
	if !slices.Contains(feed.reads, "trip 0") || store.purges != 1 {
		t.Fatalf("reads %v, purged %d times: want every conversation from 0, purged once", feed.reads, store.purges)
	}
	if c := store.cursors[searchKey{"server", "trip"}]; c.AfterSeq != 6 {
		t.Fatalf("trip read to %d, want 6", c.AfterSeq)
	}
	if got := found(searchFor(t, s, "bluelane")); !slices.Equal(got, []string{"trip title=false hits=2", "old title=false hits=1"}) {
		t.Fatalf("found %v after reading again", got)
	}
	if got := found(searchFor(t, s, "staleword")); got != nil {
		t.Fatalf("what was written from the old cursor is still found: %v", got)
	}
}

// A search the store fails answers the titles, says it is partial, and logs no query.
func TestSearchFailingStoreLogsNoQuery(t *testing.T) {
	s, _, store := searchService(t)
	s.searchSweep(context.Background())
	var logged []string
	s.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	store.fail = errors.New("statement timeout")
	a := searchFor(t, s, "Trip 秘密词")
	if !a.Partial {
		t.Fatal("not partial")
	}
	if strings.Contains(strings.Join(logged, "\n"), "秘密词") || len(logged) == 0 {
		t.Fatalf("logged %q", logged)
	}
}

func TestSearchRouteNeedsAFullDevice(t *testing.T) {
	s, _, _ := searchService(t)
	if !slices.Contains(s.Routes(), "GET /api/v1/assistant/search") {
		t.Fatal("route not registered")
	}
	for role, want := range map[string]int{"": http.StatusOK, roleFull: http.StatusOK, "read_only": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/assistant/search?q=trip", nil)
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("role %q: %d, want %d (%s)", role, w.Code, want, w.Body.String())
		}
	}
}
