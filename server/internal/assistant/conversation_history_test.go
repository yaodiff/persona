package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// historyRuntime is a fake normalized runtime. No CLI or model process is started.
type historyRuntime struct {
	mu      sync.Mutex
	items   []proto.Item
	history string
	offline bool
	reads   int
	after   []int
	// sdkEnd answers a cursor past the end as the runtime SDK's page() does: from_seq is the cursor, to_seq the end.
	sdkEnd bool
}

func (rt *historyRuntime) page(_ string, raw json.RawMessage) (any, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.reads++
	if rt.offline {
		return nil, errors.New("fake runtime is offline")
	}
	var p historyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Strict {
		return map[string]any{"items": rt.items, "strict_read": true}, nil
	}
	from, end := 0, 0
	if p.After != nil {
		from = *p.After
		rt.after = append(rt.after, from)
	}
	for _, it := range rt.items {
		end = max(end, it.Seq)
	}
	if !rt.sdkEnd {
		end = max(end, from)
	}
	items := []proto.Item{}
	for _, it := range rt.items {
		if it.Seq <= from || (p.Before != nil && *p.Before > 0 && it.Seq >= *p.Before) {
			continue
		}
		items = append(items, it)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	if p.Turns > 0 {
		limit = p.Turns * 2
	}
	more, hasMore := false, false
	if len(items) > limit {
		if p.After != nil {
			items = items[:limit]
			end = items[len(items)-1].Seq
			more = true
		} else {
			items = items[len(items)-limit:]
			from = items[0].Seq - 1
			hasMore = true
		}
	}
	if p.Before != nil && *p.Before > 0 {
		end = *p.Before - 1
	}
	return map[string]any{"items": items, "history": rt.history, "from_seq": from, "to_seq": end,
		"has_more": hasMore, "more_after": more}, nil
}

func historyFixture(t *testing.T, logs ...func(string, ...any)) (*Service, *fakeHub, *db.DB, *historyRuntime) {
	t.Helper()
	logf := testLogf(t)
	if len(logs) > 0 {
		logf = logs[0]
	}
	return historyFixtureWithHub(t, logf, nil)
}

func historyFixtureWithHub(t *testing.T, logf func(string, ...any), wrapHub func(Hub) Hub) (*Service, *fakeHub, *db.DB, *historyRuntime) {
	t.Helper()
	database := todoTestDB(t)
	f := newFakeHub().withMachine()
	var hub Hub = f
	if wrapHub != nil {
		hub = wrapHub(f)
	}
	s := withSettings(t, f, New(Options{Hub: hub, Store: func() Store { return database }, Logf: logf}))
	setSettings(t, s, func(cfg *Settings) { cfg.Main = "main-chat"; cfg.Leaders = []string{"lead-chat"} })
	f.mu.Lock()
	for _, id := range []string{"main-chat", "lead-chat", "side-chat", "coding-agent"} {
		f.known[id] = true
	}
	f.mu.Unlock()
	f.setSessions("server",
		proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100},
		proto.Session{SessionID: "lead-chat", Assistant: true, Rev: 100},
		proto.Session{SessionID: "side-chat", Assistant: true, Archived: true, Rev: 100},
		proto.Session{SessionID: "coding-agent", PaneID: "pane-1", Rev: 100})
	rt := &historyRuntime{history: "h1", items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "question one"},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "answer one"},
		{ID: "u2", Seq: 3, Role: "user", Kind: proto.KindText, Text: "same words"},
		{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "answer two"},
		{ID: "u3", Seq: 5, Role: "user", Kind: proto.KindText, Text: "same words"},
		{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "answer three"},
	}}
	f.answerNode(DeviceSessionItems, rt.page)
	f.answerNode("session.item", func(_ string, raw json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.offline {
			return nil, errors.New("fake runtime is offline")
		}
		var p historyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		for _, it := range rt.items {
			if it.ID == p.ItemID {
				it.FullSize = 0
				return it, nil
			}
		}
		return nil, errors.New("fake source item not found")
	})
	f.answerNode(DeviceAssistantHandoff, func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.offline {
			return nil, errors.New("fake runtime is offline")
		}
		return map[string]any{"summary": "source engine compaction"}, nil
	})
	return s, f, database, rt
}

func readHistory(t *testing.T, s *Service, sid string, params map[string]any) historyPage {
	t.Helper()
	params["session_id"] = sid
	var page historyPage
	if err := s.call(t.Context(), "server", DeviceSessionItems, params, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestDurableHistoryDefaultOnAssistantScopeAndOfflineRestart(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	if s.historyDB(t.Context()) == nil {
		t.Fatal("missing setting must enable database-first history")
	}
	s.historySweep(t.Context(), database) // no UI watch or subscription
	stats, err := database.ConversationHistoryStats(t.Context())
	if err != nil || stats.Conversations != 3 || stats.Items != 18 {
		t.Fatalf("assistant scope %+v %v", stats, err)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", "coding-agent"); err != nil || h != nil {
		t.Fatalf("coding Agent enrolled: %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	reads := rt.reads
	rt.mu.Unlock()
	for _, sid := range []string{"main-chat", "lead-chat", "side-chat"} {
		p := readHistory(t, s, sid, map[string]any{"turns": 2, "lazy_images": true})
		if len(p.Items) != 4 || p.Items[0].ID != "u2" || p.Items[2].ID != "u3" {
			t.Fatalf("recent rounds/dedup: %+v", p)
		}
		var h mainHandoff
		if err := s.call(t.Context(), "server", DeviceAssistantHandoff, map[string]any{"session_id": sid}, &h); err != nil || h.Summary != "source engine compaction" || len(h.Recent) != 6 {
			t.Fatalf("offline handoff %+v %v", h, err)
		}
	}
	rt.mu.Lock()
	after := rt.reads
	rt.mu.Unlock()
	if after != reads {
		t.Fatalf("covered history queried offline runtime: %d -> %d", reads, after)
	}
	restarted := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return database }, Logf: testLogf(t)}))
	p := readHistory(t, restarted, "main-chat", map[string]any{"limit": 100, "lazy_images": true})
	if len(p.Items) != 6 || p.HasMore == nil || *p.HasMore || p.Partial {
		t.Fatalf("restart history %+v", p)
	}
}

func TestInlineViewSourceReadBypassesDurableHistory(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	ctx := t.Context()
	const sid = "side-chat"
	if err := s.historySync(ctx, database, "server", proto.Session{SessionID: sid, Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	// Native export can replace a provisional item ID without changing the session revision.
	rt.mu.Lock()
	rt.items[len(rt.items)-1].ID = "exported#0"
	rt.mu.Unlock()
	params := map[string]any{"session_id": sid, "turns": 1, "lazy_images": true}
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "normal read", ctx: ctx, want: "a3"},
		{name: "inline view source read", ctx: context.WithValue(ctx, inlineViewSourceKey{}, true), want: "exported#0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var page historyPage
			if err := s.call(tt.ctx, "server", DeviceSessionItems, params, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[1].ID != tt.want {
				t.Fatalf("read returned %+v, want last item %q", page.Items, tt.want)
			}
		})
	}
}

func TestDurableHistoryIncrementalGenerationAndRollback(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.items = append(rt.items, proto.Item{ID: "a4", Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "new words"})
	rt.mu.Unlock()
	c.Rev = 200
	f.setSessions("server", c)
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	cursor := append([]int(nil), rt.after...)
	rt.mu.Unlock()
	if fmt.Sprint(cursor) != "[0 6 6]" {
		t.Fatalf("not incremental: %v", cursor)
	}
	if p := readHistory(t, s, "main-chat", map[string]any{"after_seq": 6, "lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "a4" {
		t.Fatalf("catch-up %+v", p)
	}
	if r := request(s, "PUT", "/api/v1/assistant/history", `{"enabled":false}`); r.Code != 200 {
		t.Fatalf("rollback %d %s", r.Code, r.Body.String())
	}
	if s.historyDB(t.Context()) != nil {
		t.Fatal("rollback did not disable reads/sync")
	}
	rt.mu.Lock()
	rt.history = "h2"
	rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "rewound"}}
	rt.mu.Unlock()
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replacement" {
		t.Fatalf("rollback runtime path %+v", p)
	}
	if r := request(s, "PUT", "/api/v1/assistant/history", `{"enabled":true}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	c.Rev = 50
	f.setSessions("server", c)
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h.History != "h2" {
		t.Fatalf("re-enable generation %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replacement" {
		t.Fatalf("stale generation mixed %+v", p)
	}
}

func TestDurableHistoryLegacyTailCoverageAndIncompleteReferences(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items[5].Text = strings.Repeat("x", 100000)
	rt.items[5].FullSize = 100000
	rt.mu.Unlock()
	p := readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	if len(p.Items) != 2 {
		t.Fatalf("legacy source %+v", p)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "side-chat")
	if err != nil || h == nil || conversationlog.Covers(h.Coverage, 0, 6) {
		t.Fatalf("partial tail misrepresented %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	p = readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	if !p.Partial || p.HasMore == nil || !*p.HasMore || len(p.Items) != 2 || p.Items[1].Stored == nil || p.Items[1].Stored.Complete {
		t.Fatalf("missing incomplete/coverage metadata %+v", p)
	}
	for _, method := range []string{"session.item", DeviceSessionItems} {
		params := map[string]any{"session_id": "side-chat", "item_id": "a3", "strict_read": true}
		if err := s.call(t.Context(), "server", method, params, &map[string]any{}); err == nil {
			t.Fatalf("strict source read %s fabricated complete content", method)
		}
	}
	if err := s.call(t.Context(), "server", DeviceSessionItems, map[string]any{"session_id": "side-chat", "before_seq": 5, "lazy_images": true}, &historyPage{}); err == nil {
		t.Fatal("uncovered older page must not claim empty complete history")
	}
}

type failedHistoryStore struct{ *db.DB }

func (failedHistoryStore) ConversationHistory(context.Context, string, string) (*db.ConversationHistory, error) {
	return nil, errors.New("fake database failure")
}

func TestDurableHistoryDatabaseFailureFallsBackToRuntime(t *testing.T) {
	_, f, database, rt := historyFixture(t)
	st := failedHistoryStore{database}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true})
	if len(p.Items) != 6 {
		t.Fatalf("runtime fallback %+v", p)
	}
	rt.mu.Lock()
	reads := rt.reads
	rt.mu.Unlock()
	if reads == 0 {
		t.Fatal("database failure did not use runtime")
	}
}

func TestDurableHistoryHubNodeCallReadsDatabase(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "test-only", Devices: &fakeDevices{}})
	b.helloOnce.Do(func() { close(b.helloed) })
	b.svc = s
	p, _ := json.Marshal(devicewire.NodeCall{MachineID: "server", Method: DeviceSessionItems, Params: json.RawMessage(`{"session_id":"main-chat","lazy_images":true}`)})
	out, err := b.request(t.Context(), devicewire.MethodCall, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var page historyPage
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 6 {
		t.Fatalf("hub relay did not use durable history: %s %v", raw, err)
	}
}

type gatedHistoryStore struct {
	*db.DB
	entered, release chan struct{}
}

func (st gatedHistoryStore) ConversationItems(ctx context.Context, machine, sid, history string, after, through, limit int, newest bool) ([]conversationlog.Item, error) {
	rows, err := st.DB.ConversationItems(ctx, machine, sid, history, after, through, limit, newest)
	close(st.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-st.release:
	}
	return rows, err
}

func TestDurableHistoryDeletionDuringCachedReadDropsContent(t *testing.T) {
	s, f, database, _ := historyFixture(t)
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "side-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	st := gatedHistoryStore{database, make(chan struct{}), make(chan struct{})}
	reader := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, _, err := reader.historyCall(t.Context(), "server", DeviceSessionItems, json.RawMessage(`{"session_id":"side-chat","lazy_images":true}`))
		done <- result{raw, err}
	}()
	select {
	case <-st.entered:
	case early := <-done:
		t.Fatalf("read ended before gate: %v", early.err)
	case <-time.After(5 * time.Second):
		t.Fatal("cached read did not reach gate")
	}
	if err := database.PutConversationDeletion(t.Context(), "server", "side-chat", json.RawMessage(`{"session_id":"side-chat"}`)); err != nil {
		t.Fatal(err)
	}
	close(st.release)
	got := <-done
	if got.err == nil || len(got.raw) != 0 {
		t.Fatalf("deleted content escaped cached read: %s %v", got.raw, got.err)
	}
}

func TestDurableHistoryUsesNormalizedContractAcrossEngines(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	for _, engine := range []string{"claude", "codex", "pi", "omp", "dsh", "agy", "cursor", "traex", "opencode", "grok"} {
		c := proto.Session{SessionID: "engine-" + engine, Agent: engine, Assistant: true, Rev: 100}
		if err := s.historySync(t.Context(), database, "server", c); err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		h, err := database.ConversationHistory(t.Context(), "server", c.SessionID)
		if err != nil || h == nil || h.TailSeq == nil || *h.TailSeq != 6 {
			t.Fatalf("%s normalized coverage %+v %v", engine, h, err)
		}
	}
	rt.mu.Lock()
	reads := rt.reads
	rt.mu.Unlock()
	if reads != 20 {
		t.Fatalf("fake runtime page reads %d, want ingestion plus summary-generation verification per engine", reads)
	}
}

func TestDurableHistorySameRevisionRewriteAndLateNotification(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.history = "h2"
	rt.items = []proto.Item{{ID: "replaced", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "rewritten"}}
	rt.mu.Unlock()
	f.setSessions("server", c)
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"main-chat","history":"h2","items":[]}`))
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replaced" {
		t.Fatalf("same-revision stale cache %+v", p)
	}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"main-chat","history":"h1","items":[{"id":"old-event","seq":8}]}`))
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h.History != "h2" {
		t.Fatalf("late notification resurrected old generation %+v %v", h, err)
	}
	rows, err := database.ConversationItems(t.Context(), "server", "main-chat", "h2", 0, 100, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID != "replaced" {
		t.Fatalf("late event was stored %+v %v", rows, err)
	}
}

// A source answers a cursor past its end (one from an earlier numbering of it) with that end, as the runtime SDK
// does. The durable history keeps the source's end: stored at the reader's cursor, the tail claimed lines the
// source never had.
func TestDurableHistoryKeepsTheSourceEndForACursorPastIt(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.sdkEnd = true
	rt.mu.Unlock()
	if p := readHistory(t, s, "main-chat", map[string]any{"after_seq": 0, "lazy_images": true}); len(p.Items) != 6 {
		t.Fatalf("first read %+v", p)
	}
	if p := readHistory(t, s, "main-chat", map[string]any{"after_seq": 9, "lazy_images": true}); len(p.Items) != 0 || intValue(p.ToSeq) != 6 {
		t.Fatalf("read past the end %+v", p)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h == nil || h.TailSeq == nil || *h.TailSeq != 6 || fmt.Sprint(h.Coverage) != "[{0 6}]" {
		t.Fatalf("stored end %+v %v", h, err)
	}
}

func TestDurableHistoryDoesNotHydrateFromAnotherGeneration(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items[5].Text = "preview"
	rt.items[5].FullSize = 100
	rt.mu.Unlock()
	f.answerNode("session.item", func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.history = "h2"
		rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "current generation"}}
		return proto.Item{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "wrong generation hydration"}, nil
	})
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); !errors.Is(err, db.ErrConversationHistoryChanged) {
		t.Fatalf("mixed-generation hydration accepted: %v", err)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", c.SessionID); err != nil || h != nil {
		t.Fatalf("invalid hydrated page was committed %+v %v", h, err)
	}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ConversationItems(t.Context(), "server", c.SessionID, "h2", 0, 100, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID != "replacement" {
		t.Fatalf("retry mixed source generations %+v %v", rows, err)
	}
}

func TestDurableHistoryBacksOffUnsupportedSourcesUntilNewActivity(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.history = ""
	rt.mu.Unlock()
	s.historySweep(t.Context(), database)
	rt.mu.Lock()
	first := rt.reads
	rt.mu.Unlock()
	s.historySweep(t.Context(), database)
	rt.mu.Lock()
	second := rt.reads
	rt.history = "h1"
	rt.mu.Unlock()
	if first != 3 || second != first {
		t.Fatalf("legacy source polled repeatedly: %d -> %d", first, second)
	}
	s.NodeEvent("server", "sessions.revs", json.RawMessage(`{"revs":{"main-chat":100}}`))
	s.historySweep(t.Context(), database)
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h == nil || h.History != "h1" {
		t.Fatalf("new activity did not release source backoff %+v %v", h, err)
	}
}

func TestDurableHistoryMissingGenerationKeepsLegacyRuntimeFallback(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.history = ""
	rt.mu.Unlock()
	if page := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(page.Items) != 6 {
		t.Fatalf("legacy runtime fallback %+v", page)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", "main-chat"); err != nil || h != nil {
		t.Fatalf("invented coverage without source identity %+v %v", h, err)
	}
}

func TestDurableHistorySummaryReadCannotCrossARewrite(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	f.answerNode(DeviceAssistantHandoff, func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.history = "h2"
		rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "new generation"}}
		return map[string]any{"summary": "summary after the rewrite"}, nil
	})
	err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100})
	if !errors.Is(err, db.ErrConversationHistoryChanged) {
		t.Fatalf("cross-generation summary accepted: %v", err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h == nil || h.History != "h1" || h.SummaryKnown || h.Summary != "" {
		t.Fatalf("summary leaked across generation %+v %v", h, err)
	}
}

func TestDurableHistoryKeepsLatestNarrationAndUnfoldedCatchup(t *testing.T) {
	s, _, _, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items = []proto.Item{
		{ID: "question", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check progress"},
		{ID: "thinking", Seq: 2, Role: "assistant", Kind: proto.KindThinking, Text: "checking"},
		{ID: "tool", Seq: 3, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call-1", ToolName: "Read"},
		{ID: "result", Seq: 4, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call-1", Text: "result"},
		{ID: "narration", Seq: 5, Role: "assistant", Kind: proto.KindText, Narration: true, Text: "current progress"},
	}
	rt.mu.Unlock()
	for range 2 {
		p := readHistory(t, s, "side-chat", map[string]any{"fold_work": true, "lazy_images": true})
		if len(p.Items) < 2 || p.Items[len(p.Items)-1].ID != "narration" {
			t.Fatalf("latest commentary disappeared %+v", p.Items)
		}
	}
	p := readHistory(t, s, "side-chat", map[string]any{"after_seq": 0, "fold_work": true, "lazy_images": true})
	if len(p.Items) != 5 || p.HasMore != nil {
		t.Fatalf("catch-up folded or made an older-page claim %+v", p)
	}
}

func TestDurableHistoryPagingRetainsSourceLimits(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items = nil
	for seq := 1; seq <= 200; seq++ {
		role := "assistant"
		if seq%2 == 1 {
			role = "user"
		}
		rt.items = append(rt.items, proto.Item{ID: fmt.Sprintf("i%d", seq), Seq: seq, Role: role, Kind: proto.KindText, Text: "words"})
	}
	rt.mu.Unlock()
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true})
	if len(p.Items) != 80 || p.Items[0].Seq != 121 || p.HasMore == nil || !*p.HasMore {
		t.Fatalf("default latest-page contract %+v", p)
	}
	p = readHistory(t, s, "main-chat", map[string]any{"turns": 50, "lazy_images": true})
	if len(p.Items) != 20 || p.Items[0].Seq != 181 {
		t.Fatalf("turn cap changed %+v", p)
	}
	p = readHistory(t, s, "main-chat", map[string]any{"after_seq": 0, "lazy_images": true})
	if len(p.Items) != 200 || p.MoreAfter || p.HasMore != nil {
		t.Fatalf("catch-up cursor contract %+v", p)
	}
}

func TestDurableHistoryPartialTailFillsLargerOnlineWindow(t *testing.T) {
	for _, params := range []map[string]any{
		{"turns": 3, "lazy_images": true},
		{"limit": 80, "lazy_images": true},
	} {
		t.Run(fmt.Sprint(params), func(t *testing.T) {
			s, _, _, rt := historyFixture(t)
			if p := readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true}); len(p.Items) != 2 {
				t.Fatalf("seed tail %+v", p)
			}
			rt.mu.Lock()
			before := rt.reads
			rt.mu.Unlock()
			p := readHistory(t, s, "side-chat", params)
			rt.mu.Lock()
			after := rt.reads
			rt.mu.Unlock()
			if len(p.Items) != 6 || p.Items[0].ID != "u1" || after <= before {
				t.Fatalf("partial cache hid available source: %+v, reads %d -> %d", p, before, after)
			}
		})
	}
}

func TestDurableHistoryPartialHandoffPrefersSourceAndMarksOfflineFallback(t *testing.T) {
	s, _, _, rt := historyFixture(t)
	readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	var handoff mainHandoff
	params := map[string]any{"session_id": "side-chat"}
	if err := s.call(t.Context(), "server", DeviceAssistantHandoff, params, &handoff); err != nil || handoff.Summary != "source engine compaction" {
		t.Fatalf("partial cache hid source handoff: %+v %v", handoff, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	handoff = mainHandoff{}
	if err := s.call(t.Context(), "server", DeviceAssistantHandoff, params, &handoff); err != nil || !handoff.Partial || len(handoff.Recent) != 2 {
		t.Fatalf("partial offline handoff misrepresented: %+v %v", handoff, err)
	}
}

type memHistoryStore struct {
	*fleetMem
	mu      sync.Mutex
	history map[searchKey]*db.ConversationHistory
	items   map[searchKey]map[string]conversationlog.Item
}

func newMemHistoryStore() *memHistoryStore {
	return &memHistoryStore{
		fleetMem: newFleetMem(),
		history:  map[searchKey]*db.ConversationHistory{},
		items:    map[searchKey]map[string]conversationlog.Item{},
	}
}

func (m *memHistoryStore) ConversationHistory(_ context.Context, machine, sid string) (*db.ConversationHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.history[searchKey{machine, sid}]
	if h == nil {
		return nil, nil
	}
	cp := *h
	cp.Coverage = append([]conversationlog.Range(nil), h.Coverage...)
	if h.TailSeq != nil {
		v := *h.TailSeq
		cp.TailSeq = &v
	}
	return &cp, nil
}

func (m *memHistoryStore) PutConversationPage(_ context.Context, p db.ConversationPage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := searchKey{p.MachineID, p.SessionID}
	h := m.history[k]
	if h == nil || h.History != p.History {
		if h != nil && p.ExpectedHistory != "" && p.ExpectedHistory != h.History {
			return db.ErrConversationHistoryChanged
		}
		h = &db.ConversationHistory{MachineID: p.MachineID, SessionID: p.SessionID, History: p.History}
		m.history[k] = h
		m.items[k] = map[string]conversationlog.Item{}
	}
	h.Revision = p.Revision
	h.Coverage = conversationlog.MergeRanges(h.Coverage, conversationlog.Range{From: p.FromSeq, Through: p.ThroughSeq})
	if p.Tail {
		v := p.ThroughSeq
		h.TailSeq = &v
	}
	for _, it := range p.Items {
		row, err := conversationlog.Bound(it)
		if err != nil {
			return err
		}
		m.items[k][it.ID] = row
	}
	return nil
}

func (m *memHistoryStore) ConversationItems(_ context.Context, machine, sid, history string, from, through, limit int, newest bool) ([]conversationlog.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := searchKey{machine, sid}
	h := m.history[k]
	if h == nil || h.History != history {
		return nil, nil
	}
	out := make([]conversationlog.Item, 0, len(m.items[k]))
	for _, row := range m.items[k] {
		if row.Item.Seq > from && row.Item.Seq <= through {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.Seq < out[j].Item.Seq })
	if limit > 0 && len(out) > limit {
		if newest {
			out = out[len(out)-limit:]
		} else {
			out = out[:limit]
		}
	}
	return out, nil
}

func (m *memHistoryStore) ConversationItem(_ context.Context, machine, sid, history, itemID string) (*conversationlog.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := searchKey{machine, sid}
	h := m.history[k]
	if h == nil || h.History != history {
		return nil, nil
	}
	row, ok := m.items[k][itemID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (m *memHistoryStore) PutConversationSummary(_ context.Context, machine, sid, history string, revision int64, summary string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.history[searchKey{machine, sid}]
	if h == nil || h.History != history {
		return db.ErrConversationHistoryChanged
	}
	h.Revision = revision
	h.Summary = summary
	h.SummaryKnown = true
	return nil
}

func (m *memHistoryStore) ConversationHistoryStats(_ context.Context) (db.ConversationHistoryStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var stats db.ConversationHistoryStats
	stats.Conversations = int64(len(m.history))
	for _, rows := range m.items {
		stats.Items += int64(len(rows))
	}
	return stats, nil
}

func TestConversationHistoryOfflineIdleProbeBackoff(t *testing.T) {
	st := newMemHistoryStore()
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	setSettings(t, s, func(cfg *Settings) { cfg.Main = "main-chat"; cfg.Leaders = []string{"lead-chat"} })
	for _, id := range []string{"main-chat", "lead-chat", "side-chat"} {
		f.known[id] = true
	}
	rt := &historyRuntime{history: "h1", items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "question one"},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "answer one"},
		{ID: "u2", Seq: 3, Role: "user", Kind: proto.KindText, Text: "question two"},
		{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "answer two"},
		{ID: "u3", Seq: 5, Role: "user", Kind: proto.KindText, Text: "question three"},
		{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "answer three"},
	}}
	f.answerNode(DeviceSessionItems, rt.page)
	f.answerNode(DeviceAssistantHandoff, func(_ string, _ json.RawMessage) (any, error) {
		return map[string]any{"summary": "compacted summary"}, nil
	})

	now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	s.history.mu.Lock()
	s.history.now = func() time.Time { return now }
	s.history.mu.Unlock()

	offlineSess := proto.Session{SessionID: "side-chat", Assistant: true, Status: proto.StatusOffline, Rev: 100}
	activeSess := proto.Session{SessionID: "main-chat", Assistant: true, Status: proto.StatusIdle, Rev: 100}
	partialSess := proto.Session{SessionID: "lead-chat", Assistant: true, Status: proto.StatusOffline, Rev: 100}
	f.setSessions("server", offlineSess, activeSess)

	reads := func() int {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.reads
	}

	// Initial sweep covers both conversations and records their first probe time.
	s.historySweep(t.Context(), st)
	afterInitial := reads()
	if afterInitial == 0 {
		t.Fatal("initial sweep did not probe sessions")
	}

	// Between 30s and 10m: fully covered idle offline conversation skips redundant forced probe,
	// while an active conversation still re-probes at 30s.
	now = now.Add(45 * time.Second)
	beforeOffline := reads()
	if err := s.historySync(t.Context(), st, "server", offlineSess); err != nil {
		t.Fatal(err)
	}
	if got := reads(); got != beforeOffline {
		t.Fatalf("idle covered offline session re-probed after 45s: reads %d -> %d", beforeOffline, got)
	}
	if err := s.historySync(t.Context(), st, "server", activeSess); err != nil {
		t.Fatal(err)
	}
	if got := reads(); got <= beforeOffline {
		t.Fatalf("active session did not re-probe after 45s: reads %d -> %d", beforeOffline, got)
	}

	// Just under 10m: still skips forced probe for idle covered offline session.
	now = now.Add(9*time.Minute + 14*time.Second) // 9m59s since initial probe
	beforeNearLimit := reads()
	if err := s.historySync(t.Context(), st, "server", offlineSess); err != nil {
		t.Fatal(err)
	}
	if got := reads(); got != beforeNearLimit {
		t.Fatalf("idle covered offline session re-probed before 10m: reads %d -> %d", beforeNearLimit, got)
	}

	// Once 10m elapses, the periodic verification probe runs again.
	now = now.Add(2 * time.Second) // 10m01s since initial probe
	if err := s.historySync(t.Context(), st, "server", offlineSess); err != nil {
		t.Fatal(err)
	}
	afterPeriodic := reads()
	if afterPeriodic <= beforeNearLimit {
		t.Fatalf("idle covered offline session did not re-probe after 10m: reads %d -> %d", beforeNearLimit, afterPeriodic)
	}

	// Within the next 10m window, a revision change still syncs immediately.
	now = now.Add(2 * time.Minute)
	rt.mu.Lock()
	rt.items = append(rt.items, proto.Item{ID: "a4", Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "appended"})
	rt.mu.Unlock()
	offlineSess.Rev = 101
	f.setSessions("server", offlineSess, activeSess)
	if err := s.historySync(t.Context(), st, "server", offlineSess); err != nil {
		t.Fatal(err)
	}
	afterRevBump := reads()
	if afterRevBump <= afterPeriodic {
		t.Fatalf("offline session revision change did not sync immediately: reads %d -> %d", afterPeriodic, afterRevBump)
	}
	h, err := st.ConversationHistory(t.Context(), "server", offlineSess.SessionID)
	if err != nil || h == nil || h.Revision != 101 || h.TailSeq == nil || *h.TailSeq != 7 {
		t.Fatalf("revision change not persisted: %+v %v", h, err)
	}

	// Within the 10m window, an explicit session signal still triggers immediate sync.
	now = now.Add(time.Minute)
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"side-chat","history":"h1","items":[]}`))
	if err := s.historySync(t.Context(), st, "server", offlineSess); err != nil {
		t.Fatal(err)
	}
	afterSignal := reads()
	if afterSignal <= afterRevBump {
		t.Fatalf("explicit signal did not sync offline session immediately: reads %d -> %d", afterRevBump, afterSignal)
	}

	// An offline session whose durable coverage is still partial must sync immediately
	// even if probed recently.
	f.setSessions("server", offlineSess, activeSess, partialSess)
	readHistory(t, s, partialSess.SessionID, map[string]any{"turns": 1, "lazy_images": true})
	partialBefore, err := st.ConversationHistory(t.Context(), "server", partialSess.SessionID)
	if err != nil || partialBefore == nil || partialBefore.TailSeq == nil || conversationlog.Covers(partialBefore.Coverage, 0, *partialBefore.TailSeq) {
		t.Fatalf("expected partial coverage seed, got %+v %v", partialBefore, err)
	}
	s.history.mu.Lock()
	if s.history.probed == nil {
		s.history.probed = map[searchKey]time.Time{}
	}
	s.history.probed[searchKey{"server", partialSess.SessionID}] = now.Add(-2 * time.Minute)
	s.history.mu.Unlock()
	beforePartial := reads()
	if err := s.historySync(t.Context(), st, "server", partialSess); err != nil {
		t.Fatal(err)
	}
	if got := reads(); got <= beforePartial {
		t.Fatalf("uncovered offline session did not sync immediately: reads %d -> %d", beforePartial, got)
	}
	partialAfter, err := st.ConversationHistory(t.Context(), "server", partialSess.SessionID)
	if err != nil || partialAfter == nil || partialAfter.TailSeq == nil || !conversationlog.Covers(partialAfter.Coverage, 0, *partialAfter.TailSeq) {
		t.Fatalf("uncovered offline session was not fully backfilled: %+v %v", partialAfter, err)
	}
}
