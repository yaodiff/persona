package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestInlineViewsUseOnlyTrustedAssistantItems(t *testing.T) {
	connection := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, connection, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	f := newFakeHub().withMachine()
	conversation := "inline-" + randomHex(8)
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true}, proto.Session{SessionID: "ordinary", Assistant: false})
	s := newServiceOn(t, f, database)
	card := `{"fallback":"Choose","sections":[{"items":[{"type":"button","title":"Yes","action":{"id":"yes","reply":"Trusted inline reply"}}]}]}`
	text := "```persona-view\ninvalid\n```\n~~~persona-view\n" + card + "\n~~~\n"
	items := []proto.Item{
		{ID: "user#0", Role: "user", Kind: proto.KindText, Text: text},
		{ID: "tool#0", Role: "assistant", Kind: proto.KindToolResult, Text: text},
		{ID: "example#0", Role: "assistant", Kind: proto.KindText, Text: "````markdown\n```persona-view\n" + card + "\n```\n````"},
		{ID: "partial#0", Role: "assistant", Kind: proto.KindText, Text: "```persona-view\n" + card},
		{ID: "truncated#0", Role: "assistant", Kind: proto.KindText, Text: text, FullSize: 100000},
		{ID: "trusted#0", Role: "assistant", Kind: proto.KindText, Text: text},
	}
	s.SessionItems(ctx, SessionItems{MachineID: "server", SessionID: conversation, Items: items})
	keys, err := database.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || len(keys) != 1 || keys[0] != "trusted#0/1" {
		t.Fatalf("keys %v %v", keys, err)
	}
	if strings.Count(f.eventTypes(), "assistant_view_block") != 1 {
		t.Fatalf("registration events %q", f.eventTypes())
	}
	s.SessionItems(ctx, SessionItems{MachineID: "server", SessionID: conversation, Items: items})
	if strings.Count(f.eventTypes(), "assistant_view_block") != 1 {
		t.Fatalf("replay broadcast another registration: %q", f.eventTypes())
	}
	s.SessionItems(ctx, SessionItems{MachineID: "server", SessionID: "ordinary", Items: items})
	keys, err = database.AssistantViewBlockKeys(ctx, "ordinary")
	if err != nil || len(keys) != 0 {
		t.Fatalf("ordinary keys %v %v", keys, err)
	}
	path := "/api/v1/assistant/conversations/" + conversation + "/views/" + url.PathEscape("trusted#0/1") + "/actions"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"action":"yes","client_msg_id":"client"}`)))
	if w.Code != 200 {
		t.Fatalf("escaped key submit %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, err := database.AssistantViewAction(ctx, conversation, "trusted#0/1")
		if err != nil {
			t.Fatal(err)
		}
		if a != nil && a.Status != "pending" {
			if !strings.HasPrefix(a.Text, "Trusted inline reply") {
				t.Fatalf("message %q", a.Text)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inline action delivery did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Source errors are diagnostics, not another executable view; valid content retains its block index.
	feedback, err := database.PeekAssistantViewErrors(ctx, conversation, "server", 5)
	if err != nil || len(feedback) < 1 {
		t.Fatalf("feedback %v %v", feedback, err)
	}
	var action map[string]any
	if json.Unmarshal(w.Body.Bytes(), &action) != nil || action["action"] == nil {
		t.Fatal("no action result")
	}
}

type inlineFullHub struct {
	*fakeHub
	full proto.Item
}

func (h inlineFullHub) DeviceCall(ctx context.Context, p assistantwire.DeviceCall) (json.RawMessage, error) {
	if p.Method == "session.item" {
		return json.Marshal(h.full)
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestInlineTurnRegistersWithoutAnAppAndReadsFullText(t *testing.T) {
	connection := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, connection, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	f := newFakeHub().withMachine()
	conversation := "unwatched-" + randomHex(8)
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true}, proto.Session{SessionID: "ordinary"})
	f.setItems(proto.Item{ID: "long#0", Role: "assistant", Kind: proto.KindText, Text: "A long conclusion before the card", FullSize: 100000})
	h := inlineFullHub{fakeHub: f, full: proto.Item{ID: "long#0", Role: "assistant", Kind: proto.KindText, Text: "Conclusion\n```persona-view\n{\"fallback\":\"Finished\",\"sections\":[]}\n```\n"}}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return database }, Logf: testLogf(t)}))
	ref := SessionRef{MachineID: "server", SessionID: conversation}
	s.registerInlineTurn(ctx, SessionRef{MachineID: "server", SessionID: "ordinary"})
	keys, err := database.AssistantViewBlockKeys(ctx, "ordinary")
	if err != nil || len(keys) != 0 {
		t.Fatalf("ordinary turn keys %v %v", keys, err)
	}
	s.registerInlineTurn(ctx, ref)
	keys, err = database.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || len(keys) != 1 || keys[0] != "long#0/0" {
		t.Fatalf("full reply keys %v %v", keys, err)
	}
}

type memViewError struct {
	errors    []string
	reported  bool
	createdAt time.Time
}

type memInlineViewStore struct {
	*memStore
	vmu                  sync.Mutex
	blocks               map[string]map[string]db.AssistantViewBlockRow
	viewErrs             map[[3]string]*memViewError
	passMarks            map[[2]string]time.Time
	failRegisterKey      string
	registrationAttempts []string
	errorWrites          int
}

var (
	_ Store           = (*memInlineViewStore)(nil)
	_ inlineViewStore = (*memInlineViewStore)(nil)
)

func newMemInlineViewStore() *memInlineViewStore {
	return &memInlineViewStore{
		memStore:  &memStore{},
		blocks:    map[string]map[string]db.AssistantViewBlockRow{},
		viewErrs:  map[[3]string]*memViewError{},
		passMarks: map[[2]string]time.Time{},
	}
}

func (m *memInlineViewStore) RegisterAssistantViewBlock(_ context.Context, row db.AssistantViewBlockRow) (bool, error) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	m.registrationAttempts = append(m.registrationAttempts, row.Key)
	if m.failRegisterKey != "" && row.Key == m.failRegisterKey {
		m.failRegisterKey = ""
		return false, errors.New("transient block store failure")
	}
	conv := m.blocks[row.Conversation]
	if conv == nil {
		conv = map[string]db.AssistantViewBlockRow{}
		m.blocks[row.Conversation] = conv
	}
	if _, exists := conv[row.Key]; exists {
		return false, nil
	}
	conv[row.Key] = row
	return true, nil
}

func (m *memInlineViewStore) AssistantViewBlockKeys(_ context.Context, conversation string) ([]string, error) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	conv := m.blocks[conversation]
	out := make([]string, 0, len(conv))
	for k := range conv {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

func (m *memInlineViewStore) block(conversation, key string) (db.AssistantViewBlockRow, bool) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	row, ok := m.blocks[conversation][key]
	return row, ok
}

func (m *memInlineViewStore) RecordAssistantViewErrors(_ context.Context, conversation, machine, item string, problems []string, at time.Time) error {
	if conversation == "" || machine == "" || item == "" {
		return nil
	}
	m.vmu.Lock()
	defer m.vmu.Unlock()
	m.errorWrites++
	scope := [2]string{conversation, machine}
	if len(problems) == 0 {
		if at.IsZero() {
			if row := m.viewErrs[[3]string{conversation, machine, item}]; row != nil {
				row.reported = true
			}
			return nil
		}
		if cur := m.passMarks[scope]; at.After(cur) {
			m.passMarks[scope] = at
		}
		for k, row := range m.viewErrs {
			if k[0] == conversation && k[1] == machine && !row.reported && (k[2] == item || row.createdAt.Before(at)) {
				row.reported = true
			}
		}
		return nil
	}
	passed := m.passMarks[scope]
	if passed.IsZero() {
		passed = time.Unix(0, 0).UTC()
	}
	redone := !at.IsZero() && !at.After(passed)
	key := [3]string{conversation, machine, item}
	if existing := m.viewErrs[key]; existing != nil {
		if slices.Equal(existing.errors, problems) {
			return nil
		}
		existing.errors = slices.Clone(problems)
		existing.reported = redone
		return nil
	}
	m.viewErrs[key] = &memViewError{errors: slices.Clone(problems), reported: redone, createdAt: at}
	return nil
}

func (m *memInlineViewStore) pendingViewErrorItems(conversation, machine string) []string {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	var out []string
	for k, row := range m.viewErrs {
		if k[0] == conversation && k[1] == machine && !row.reported {
			out = append(out, k[2])
		}
	}
	slices.Sort(out)
	return out
}

func (m *memInlineViewStore) ackViewErrorItem(conversation, machine, item string) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	if row := m.viewErrs[[3]string{conversation, machine, item}]; row != nil {
		row.reported = true
	}
}

// runtimePageTurns mirrors runtime-sdk/lib/items.js page() and node/agypane.go:
// every user text/image item (including background task wakes, reminders, and relays) counts as a turn,
// capped at 10 turns.
func runtimePageTurns(items []proto.Item, turns int) []proto.Item {
	if turns <= 0 {
		return slices.Clone(items)
	}
	want := min(turns, 10)
	seen := 0
	start := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role == "user" && (it.Kind == proto.KindText || it.Kind == proto.KindImage) &&
			!(i > 0 && items[i-1].Role == "user" && items[i-1].Seq == it.Seq) {
			seen++
			if seen == want {
				start = i
				break
			}
		}
	}
	return slices.Clone(items[start:])
}

func (m *memInlineViewStore) Feed(context.Context, string, time.Time, int) ([]db.FeedRow, error) {
	return nil, nil
}

const antigravityInlineCard = "```persona-view\n" +
	`{"fallback":"Choose","sections":[{"items":[{"type":"row","title":"Use option A","action":{"id":"choose-a","reply":"Use option A"}}]}]}` + "\n```"

func TestAntigravityInlineTurnRecoversLateExport(t *testing.T) {
	for _, priorTurns := range []bool{false, true} {
		name := "first turn"
		if priorTurns {
			name = "follow-up already visible"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				f := newFakeHub().withMachine()
				f.ownModels = true
				st := newMemInlineViewStore()
				s := newServiceOn(t, f, st)
				r := SessionRef{MachineID: "server", SessionID: "agy-fixture"}
				f.setSessions(r.MachineID, proto.Session{SessionID: r.SessionID, Assistant: true, Status: proto.StatusDone, Rev: 8})
				final := proto.Item{ID: "8@0123456789abcdef0123#1", Seq: 8, TS: time.Now(), Role: "assistant", Kind: proto.KindText, Text: antigravityInlineCard}
				temporary := final
				temporary.ID = "8@abcdef0123456789abcd#0"
				var exported bool
				f.page = func(turns int) []proto.Item {
					if priorTurns && turns == 1 {
						return nil
					}
					if exported {
						return []proto.Item{final}
					}
					return []proto.Item{temporary}
				}
				s.registerInlineTurn(ctx, r)
				s.registerInlineTurnSettled(ctx, r.MachineID, r.SessionID)
				elapse(5 * time.Second) // after the original settle read and first retry
				key := wire.AssistantBlockKey(final.ID, 0)
				if keys, _ := st.AssistantViewBlockKeys(ctx, r.SessionID); slices.Contains(keys, key) {
					t.Fatalf("exported key acknowledged before export: %v", keys)
				}
				exported = true // same session revision, no app watch or list event
				elapse(15 * time.Second)
				if keys, _ := st.AssistantViewBlockKeys(ctx, r.SessionID); !slices.Contains(keys, key) {
					t.Fatalf("missing final key after retry: %v", keys)
				}
				row, ok := st.block(r.SessionID, key)
				if !ok || row.ItemID != final.ID || row.MachineID != r.MachineID {
					t.Fatalf("wrong registration provenance: %+v", row)
				}
				view, err := wire.ParseAssistantView(row.View)
				if err != nil {
					t.Fatal(err)
				}
				if text, _, err := viewReplyMessage(view, key, viewActionInput{Action: "choose-a"}); err != nil || !strings.HasPrefix(text, "Use option A") {
					t.Fatalf("declared reply: %q, %v", text, err)
				}
				if _, _, err := viewReplyMessage(view, key, viewActionInput{Action: "undeclared"}); err == nil {
					t.Fatal("accepted an undeclared action")
				}
				events := f.eventTypes()
				s.reconcileInlineTurn(ctx, r)
				if got := f.eventTypes(); got != events {
					t.Fatalf("replay broadcast again: %q -> %q", events, got)
				}
			})
		})
	}
}

func TestInlineViewPageRepairsHistoricalCardAndFeed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f := newFakeHub().withMachine()
		f.ownModels = true
		st := newMemInlineViewStore()
		s := newServiceOn(t, f, st)
		bus := newNoticeBus(t, s).b
		f.setSessions("server",
			proto.Session{SessionID: "agy-history", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "agy-working", Assistant: true, Status: proto.StatusWorking},
			proto.Session{SessionID: "agy-ordinary", Status: proto.StatusIdle},
		)
		card := proto.Item{ID: "7@abcdef0123456789abcd#1", Seq: 7, Role: "assistant", Kind: proto.KindText, Text: antigravityInlineCard}
		for _, sid := range []string{"agy-history", "agy-working", "agy-ordinary"} {
			p := SessionItems{MachineID: "server", SessionID: sid, Items: []proto.Item{
				card, {ID: "user-text", Role: "user", Kind: proto.KindText, Text: antigravityInlineCard},
			}}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := bus.event(ctx, busEvent{name: assistantwire.EventInlineViewPage, params: raw}); err != nil {
					t.Fatal(err)
				}
			}
			synctest.Wait()
			w := serve(s, "GET", "/api/v1/assistant/conversations/"+sid+"/feed", "")
			var feed struct {
				Keys []string `json:"view_blocks"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &feed) != nil {
				t.Fatalf("feed for %s: %d %s", sid, w.Code, w.Body.String())
			}
			var want []string
			if sid == "agy-history" {
				want = []string{wire.AssistantBlockKey(card.ID, 0)}
			}
			if !slices.Equal(feed.Keys, want) {
				t.Fatalf("feed for %s: keys %v, want %v", sid, feed.Keys, want)
			}
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
			t.Fatalf("historical card ack count = %d, want 1", got)
		}
	})
}

func TestInlineViewPageRepairsWithoutRecordingErrors(t *testing.T) {
	f := newFakeHub().withMachine()
	st := newMemInlineViewStore()
	s := newServiceOn(t, f, st)
	const sid = "history-with-invalid-view"
	f.setSessions("server", proto.Session{SessionID: sid, Assistant: true, Status: proto.StatusDone})
	full := proto.Item{
		ID: "reply#0", Role: "assistant", Kind: proto.KindText,
		Text: "```persona-view\ninvalid JSON\n```\n\n" + antigravityInlineCard,
	}
	f.answerNode("session.item", func(string, json.RawMessage) (any, error) { return full, nil })
	preview := full
	preview.FullSize = len(full.Text)
	preview.Text = ""
	p := SessionItems{MachineID: "server", SessionID: sid, Items: []proto.Item{preview}}
	s.repairInlineViewPage(t.Context(), p)
	if got, _ := st.AssistantViewBlockKeys(t.Context(), sid); !slices.Equal(got, []string{"reply#0/1"}) {
		t.Fatalf("registered keys = %v, want the valid block at index 1", got)
	}
	if st.errorWrites != 0 {
		t.Fatalf("history repair wrote view errors %d times", st.errorWrites)
	}
	s.registerInlineViews(t.Context(), p)
	if got := st.pendingViewErrorItems(sid, "server"); !slices.Equal(got, []string{"reply#0"}) {
		t.Fatalf("live scan must still report the invalid full-text block: %v", got)
	}
}

func TestInlineViewPageSkipsKnownKeysAndRegistersMissingBlocks(t *testing.T) {
	f := newFakeHub().withMachine()
	st := newMemInlineViewStore()
	s := newServiceOn(t, f, st)
	const sid = "history-with-known-view"
	f.setSessions("server", proto.Session{SessionID: sid, Assistant: true, Status: proto.StatusInterrupted})
	p := SessionItems{MachineID: "server", SessionID: sid, Items: []proto.Item{{
		ID: "reply#0", Role: "assistant", Kind: proto.KindText, Text: antigravityInlineCard,
	}}}
	s.repairInlineViewPage(t.Context(), p)
	p.Items[0].Text += "\n\n" + antigravityInlineCard
	s.repairInlineViewPage(t.Context(), p)
	s.repairInlineViewPage(t.Context(), p)
	want := []string{"reply#0/0", "reply#0/1"}
	if !slices.Equal(st.registrationAttempts, want) {
		t.Fatalf("registration attempts = %v, want %v", st.registrationAttempts, want)
	}
}

func TestInlineTurnRegistersFinalIDAfterSettleAndIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.ownModels = true
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		st := newMemInlineViewStore()
		s := newServiceOn(t, f, st)
		t.Cleanup(func() {
			if q := s.fleet.q.ch; q != nil {
				close(q)
			}
		})
		const delay = time.Minute
		f.mu.Lock()
		f.hello.DoneDelayMS = delay.Milliseconds()
		f.mu.Unlock()

		ctx := context.Background()
		conversation := "agy-settle-test"
		f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true, Status: proto.StatusDone})
		card := `{"fallback":"Choose","sections":[{"items":[{"type":"button","title":"Yes","action":{"id":"yes","reply":"Choose yes"}}]}]}`
		text := "Options:\n```persona-view\n" + card + "\n```\n"

		// At TurnEnded, agy has only the temporary unexported item id (before native transcript flush).
		f.setItems(proto.Item{ID: "1785#0", Seq: 1785, Role: "assistant", Kind: proto.KindText, Text: text})
		s.TurnEnded(ctx, SessionRef{MachineID: "server", SessionID: conversation})
		synctest.Wait()

		keys, err := st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0"}) {
			t.Fatalf("after TurnEnded: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
			t.Fatalf("after TurnEnded: assistant_view_block count=%d, want 1 (%q)", got, f.eventTypes())
		}

		// Native transcript flushes with the final persisted item id before settleDelay elapses.
		f.setItems(proto.Item{ID: "1790@abcd#1", Seq: 1790, Role: "assistant", Kind: proto.KindText, Text: text})
		elapse(delay - time.Millisecond)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0"}) {
			t.Fatalf("before settleDelay: keys=%v err=%v", keys, err)
		}

		// Once settleDelay elapses, afterSettle registers the final persisted item id and broadcasts it once.
		elapse(time.Millisecond)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after settleDelay: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after settleDelay: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}

		// Re-running registration for the same final item id is idempotent and broadcasts no duplicate event.
		s.registerInlineTurnSettled(ctx, "server", conversation)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after idempotent replay: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after idempotent replay: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}
	})
}

func TestInlineTurnRegistersWhenStoppedAssistantRevAdvances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub()
		f.ownModels = true
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		d := newFleetDevs()
		d.add("mac", "Mac")
		f.setSessions("mac") // transcript reads use the same machine the fleet lists
		st := newMemInlineViewStore()
		s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
		ft := &fleetTest{t: t, s: s, d: d, f: f}
		t.Cleanup(func() {
			if q := s.fleet.q.ch; q != nil {
				close(q)
			}
		})
		const delay = time.Minute
		f.mu.Lock()
		f.hello.DoneDelayMS = delay.Milliseconds()
		f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: delay.Milliseconds(), LongTurnMS: 60_000}
		f.mu.Unlock()

		ctx := context.Background()
		f.setSessions("mac") // the node's transcript reads go through the hub
		machine, ok := s.fleetMachine("mac")
		if !ok {
			t.Fatal("missing fleet machine mac")
		}
		conversation := "agy-rev-advance"
		card := `{"fallback":"Choose","sections":[{"items":[{"type":"button","title":"Yes","action":{"id":"yes","reply":"Choose yes"}}]}]}`
		text := "Options:\n```persona-view\n" + card + "\n```\n"

		ft.sessions("mac", proto.Session{
			SessionID: conversation,
			Agent:     "agy",
			Cwd:       machine.assistantCwd(),
			Status:    proto.StatusWorking,
			Rev:       1780,
			UpdatedAt: time.Now(),
		})
		synctest.Wait()

		// Turn ends before native transcript export; only the temporary item id is available.
		f.setItems(proto.Item{ID: "1785#0", Seq: 1785, Role: "assistant", Kind: proto.KindText, Text: text})
		ft.sessions("mac", proto.Session{
			SessionID: conversation,
			Agent:     "agy",
			Cwd:       machine.assistantCwd(),
			Status:    proto.StatusDone,
			Rev:       1785,
			LastText:  "Options",
			UpdatedAt: time.Now(),
		})
		synctest.Wait()

		keys, err := st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0"}) {
			t.Fatalf("after TurnEnded: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
			t.Fatalf("after TurnEnded: assistant_view_block count=%d, want 1 (%q)", got, f.eventTypes())
		}

		// Native transcript flushes while still within the settle window, advancing rev to 1790.
		f.setItems(proto.Item{ID: "1790@abcd#1", Seq: 1790, Role: "assistant", Kind: proto.KindText, Text: text})
		ft.sessions("mac", proto.Session{
			SessionID: conversation,
			Agent:     "agy",
			Cwd:       machine.assistantCwd(),
			Status:    proto.StatusDone,
			Rev:       1790,
			LastText:  "Options",
			UpdatedAt: time.Now(),
		})
		synctest.Wait()

		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after rev advance: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after rev advance: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}

		// Once afterSettle fires later, re-registration is idempotent and emits no duplicate event.
		elapse(delay)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after settleDelay: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after settleDelay: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}
	})
}

func TestInlineTurnRegistersPreviousTurnFinalIDAfterImmediateBackgroundWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.ownModels = true
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		st := newMemInlineViewStore()
		s := newServiceOn(t, f, st)
		t.Cleanup(func() {
			if q := s.fleet.q.ch; q != nil {
				close(q)
			}
		})
		const delay = time.Minute
		f.mu.Lock()
		f.hello.DoneDelayMS = delay.Milliseconds()
		f.mu.Unlock()

		ctx := context.Background()
		conversation := "agy-bg-wake-inline-view"
		f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true, Status: proto.StatusDone})
		card := `{"fallback":"Choose","sections":[{"items":[{"type":"button","title":"Yes","action":{"id":"yes","reply":"Choose yes"}}]}]}`
		cardText := "Options:\n```persona-view\n" + card + "\n```\n"
		tA := time.Now().UTC()

		var mu sync.Mutex
		var transcript []proto.Item
		var sawInlineWindow bool
		setTranscript := func(items ...proto.Item) {
			mu.Lock()
			transcript = slices.Clone(items)
			mu.Unlock()
		}
		f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
			var p struct {
				Turns      int  `json:"turns"`
				LazyImages bool `json:"lazy_images"`
			}
			_ = json.Unmarshal(raw, &p)
			mu.Lock()
			items := runtimePageTurns(transcript, p.Turns)
			if p.Turns == inlineViewTurns && p.LazyImages {
				sawInlineWindow = true
			}
			mu.Unlock()
			return map[string]any{"items": items}, nil
		})

		// Turn A ends with an inline persona-view card under its temporary unexported item id.
		turnAUser := proto.Item{ID: "1784#0", Seq: 1784, Role: "user", Kind: proto.KindText, TS: tA.Add(-time.Second), Text: "What should we pick?"}
		turnATemp := proto.Item{ID: "1785#0", Seq: 1785, Role: "assistant", Kind: proto.KindText, TS: tA, Text: cardText}
		setTranscript(turnAUser, turnATemp)

		s.TurnEnded(ctx, SessionRef{MachineID: "server", SessionID: conversation})
		synctest.Wait()

		keys, err := st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0"}) {
			t.Fatalf("after Turn A ended: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
			t.Fatalf("after Turn A ended: assistant_view_block count=%d, want 1 (%q)", got, f.eventTypes())
		}

		// Seconds later (before Turn A flushes or settles), a background task wake starts and finishes Turn B.
		turnBUser := proto.Item{ID: "1786#0", Seq: 1786, Role: "user", Kind: proto.KindText, Wake: true, TS: tA.Add(2 * time.Second), Text: "Background task finished."}
		turnBReply := proto.Item{ID: "1787#0", Seq: 1787, Role: "assistant", Kind: proto.KindText, TS: tA.Add(3 * time.Second), Text: "Noted the background task update."}
		setTranscript(turnAUser, turnATemp, turnBUser, turnBReply)

		elapse(3 * time.Second)
		s.TurnEnded(ctx, SessionRef{MachineID: "server", SessionID: conversation})
		synctest.Wait()

		// Turn A has not flushed yet, so rescanning [Turn A (temp), Turn B] is idempotent with no duplicate event.
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0"}) {
			t.Fatalf("after Turn B ended (before flush): keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
			t.Fatalf("after Turn B ended (before flush): assistant_view_block count=%d, want 1 (%q)", got, f.eventTypes())
		}

		// Turn A's native transcript flushes with its final persisted item id, and another reminder wake (Turn C) lands
		// before Turn A's settleDelay elapses.
		turnAFinal := proto.Item{ID: "1790@abcd#1", Seq: 1790, Role: "assistant", Kind: proto.KindText, TS: tA, Text: cardText}
		turnCUser := proto.Item{ID: "1791#0", Seq: 1791, Role: "user", Kind: proto.KindText, Wake: true, TS: tA.Add(10 * time.Second), Text: "Reminder due."}
		turnCReply := proto.Item{ID: "1792#0", Seq: 1792, Role: "assistant", Kind: proto.KindText, TS: tA.Add(11 * time.Second), Text: "Reminder acknowledged."}
		flushed := []proto.Item{turnAUser, turnAFinal, turnBUser, turnBReply, turnCUser, turnCReply}
		setTranscript(flushed...)

		// Confirm the root cause: requesting only turns:1 omits Turn A's final persisted item id completely.
		for _, it := range runtimePageTurns(flushed, 1) {
			if it.ID == turnAFinal.ID {
				t.Fatalf("turns:1 unexpectedly included %s; test setup did not reproduce the bug", turnAFinal.ID)
			}
		}

		// Turn A's settleDelay elapses: registerInlineTurnSettled scans recent turns and registers Turn A's final id.
		elapse(delay - 3*time.Second)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after Turn A settleDelay: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after Turn A settleDelay: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}
		origRow, ok := st.block(conversation, "1790@abcd#1/0")
		if !ok {
			t.Fatal("missing final registered block row")
		}

		// Turn B's settleDelay elapses and an explicit replay runs: fully idempotent, no duplicate broadcasts or edits.
		elapse(3 * time.Second)
		s.registerInlineTurnSettled(ctx, "server", conversation)
		keys, err = st.AssistantViewBlockKeys(ctx, conversation)
		if err != nil || !slices.Equal(keys, []string{"1785#0/0", "1790@abcd#1/0"}) {
			t.Fatalf("after Turn B settleDelay and replay: keys=%v err=%v", keys, err)
		}
		if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
			t.Fatalf("after Turn B settleDelay and replay: assistant_view_block count=%d, want 2 (%q)", got, f.eventTypes())
		}
		afterRow, ok := st.block(conversation, "1790@abcd#1/0")
		if !ok || !afterRow.CreatedAt.Equal(origRow.CreatedAt) || string(afterRow.View) != string(origRow.View) {
			t.Fatalf("rescan mutated stored block: before=%+v after=%+v", origRow, afterRow)
		}
		mu.Lock()
		usedWindow := sawInlineWindow
		mu.Unlock()
		if !usedWindow {
			t.Fatalf("expected registerInlineTurn to request turns=%d with lazy_images=true", inlineViewTurns)
		}
	})
}

func TestInlineTurnMultiTurnRescanIdempotentForErrorsAndBlocks(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	st := newMemInlineViewStore()
	s := newServiceOn(t, f, st)
	conversation := "multi-turn-idempotent"
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true, Status: proto.StatusDone})
	ref := SessionRef{MachineID: "server", SessionID: conversation}

	validCard := "```persona-view\n{\"fallback\":\"Pick\",\"sections\":[{\"items\":[{\"type\":\"button\",\"title\":\"OK\",\"action\":{\"id\":\"ok\",\"reply\":\"OK\"}}]}]}\n```\n"
	invalidCard := "```persona-view\n{\"sections\":[]}\n```\n"
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)

	var mu sync.Mutex
	var transcript []proto.Item
	setTranscript := func(items ...proto.Item) {
		mu.Lock()
		transcript = slices.Clone(items)
		mu.Unlock()
	}
	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Turns int `json:"turns"`
		}
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		items := runtimePageTurns(transcript, p.Turns)
		mu.Unlock()
		return map[string]any{"items": items}, nil
	})

	// Turn 1: valid card at t0.
	turn1 := []proto.Item{
		{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, TS: t0.Add(-time.Second), Text: "turn 1"},
		{ID: "turn1#0", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: validCard},
	}
	setTranscript(turn1...)
	s.registerInlineTurn(ctx, ref)

	origBlock, ok := st.block(conversation, "turn1#0/0")
	if !ok {
		t.Fatal("turn1#0/0 not registered")
	}
	if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
		t.Fatalf("after Turn 1: events=%d want 1", got)
	}

	// Turn 2: invalid card at t0+1m. Rescanning [Turn 1, Turn 2] must NOT let Turn 1 clear Turn 2's error.
	turn2 := []proto.Item{
		{ID: "u2#0", Seq: 3, Role: "user", Kind: proto.KindText, TS: t0.Add(59 * time.Second), Text: "turn 2"},
		{ID: "turn2#0", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: invalidCard},
	}
	setTranscript(append(slices.Clone(turn1), turn2...)...)
	s.registerInlineTurn(ctx, ref)
	s.registerInlineTurnSettled(ctx, "server", conversation)

	if got := st.pendingViewErrorItems(conversation, "server"); !slices.Equal(got, []string{"turn2#0"}) {
		t.Fatalf("pending errors after Turn 2 rescan=%v, want [turn2#0]", got)
	}
	if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 1 {
		t.Fatalf("after Turn 2 rescan: duplicate assistant_view_block emitted (%d)", got)
	}
	if afterBlock, _ := st.block(conversation, "turn1#0/0"); !afterBlock.CreatedAt.Equal(origBlock.CreatedAt) || string(afterBlock.View) != string(origBlock.View) {
		t.Fatalf("turn1 block mutated on rescan: before=%+v after=%+v", origBlock, afterBlock)
	}

	// Ack Turn 2's error, then a background wake (Turn 3, plain text) runs. Rescanning must NOT reopen Turn 2's
	// acked error or attribute it to Turn 3.
	st.ackViewErrorItem(conversation, "server", "turn2#0")
	turn3 := []proto.Item{
		{ID: "u3#0", Seq: 5, Role: "user", Kind: proto.KindText, Wake: true, TS: t0.Add(2*time.Minute - time.Second), Text: "wake"},
		{ID: "turn3#0", Seq: 6, Role: "assistant", Kind: proto.KindText, TS: t0.Add(2 * time.Minute), Text: "Background wake handled."},
	}
	setTranscript(append(append(slices.Clone(turn1), turn2...), turn3...)...)
	s.registerInlineTurn(ctx, ref)
	if got := st.pendingViewErrorItems(conversation, "server"); len(got) != 0 {
		t.Fatalf("acked Turn 2 error resurfaced or leaked to Turn 3: %v", got)
	}

	// Turn 4: valid card at t0+3m advances the pass watermark. Even if Turn 2 later flushes to a new item id with
	// its original timestamp (t0+1m < t0+3m), rescanning must treat it as already redone and keep pending errors empty.
	turn4 := []proto.Item{
		{ID: "u4#0", Seq: 7, Role: "user", Kind: proto.KindText, TS: t0.Add(3*time.Minute - time.Second), Text: "turn 4"},
		{ID: "turn4#0", Seq: 8, Role: "assistant", Kind: proto.KindText, TS: t0.Add(3 * time.Minute), Text: validCard},
	}
	setTranscript(append(append(append(slices.Clone(turn1), turn2...), turn3...), turn4...)...)
	s.registerInlineTurn(ctx, ref)

	turn2Flushed := []proto.Item{
		turn2[0],
		{ID: "turn2@flushed#1", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: invalidCard},
	}
	setTranscript(append(append(append(slices.Clone(turn1), turn2Flushed...), turn3...), turn4...)...)
	s.registerInlineTurnSettled(ctx, "server", conversation)

	if got := st.pendingViewErrorItems(conversation, "server"); len(got) != 0 {
		t.Fatalf("flushed older invalid Turn 2 resurfaced after newer valid Turn 4: %v", got)
	}
	keys, err := st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"turn1#0/0", "turn4#0/0"}) {
		t.Fatalf("registered keys=%v err=%v", keys, err)
	}
	if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
		t.Fatalf("after Turn 4 and rescan: assistant_view_block count=%d, want 2", got)
	}
}

func TestInlineTurnRescanWithPostgresPreservesActionsAndErrors(t *testing.T) {
	connection := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, connection, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	f := newFakeHub().withMachine()
	conversation := "inline-rescan-pg-" + randomHex(8)
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true, Status: proto.StatusDone})
	s := newServiceOn(t, f, database)
	ref := SessionRef{MachineID: "server", SessionID: conversation}

	validCard := "```persona-view\n{\"fallback\":\"Choose\",\"sections\":[{\"items\":[{\"type\":\"button\",\"title\":\"Yes\",\"action\":{\"id\":\"yes\",\"reply\":\"Trusted choice\"}}]}]}\n```\n"
	invalidCard := "```persona-view\n{\"sections\":[]}\n```\n"
	t0 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)

	var mu sync.Mutex
	var transcript []proto.Item
	setTranscript := func(items ...proto.Item) {
		mu.Lock()
		transcript = slices.Clone(items)
		mu.Unlock()
	}
	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Turns int `json:"turns"`
		}
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		items := runtimePageTurns(transcript, p.Turns)
		mu.Unlock()
		return map[string]any{"items": items}, nil
	})

	// Register Turn A's initial card and record a submitted user action against it.
	turnAUser := proto.Item{ID: "uA#0", Seq: 1, Role: "user", Kind: proto.KindText, TS: t0.Add(-time.Second), Text: "Pick one"}
	turnATemp := proto.Item{ID: "turnA#0", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: validCard}
	setTranscript(turnAUser, turnATemp)
	s.registerInlineTurn(ctx, ref)

	clientMsgID := "client-" + randomHex(8)
	actionRow := db.AssistantViewActionRow{
		Conversation: conversation,
		Key:          "turnA#0/0",
		Action:       "yes",
		ClientMsgID:  clientMsgID,
		MessageID:    "message-" + randomHex(8),
		MachineID:    "server",
		Text:         "Trusted choice",
		At:           t0.Add(5 * time.Second),
	}
	if _, first, err := database.BeginAssistantViewAction(ctx, actionRow, 1); err != nil || !first {
		t.Fatalf("BeginAssistantViewAction: first=%v err=%v", first, err)
	}

	// Turn A flushes to its final persisted item id while Turn B (invalid card) and Turn C (background wake) follow.
	turnAFinal := proto.Item{ID: "turnA@final#1", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: validCard}
	turnBUser := proto.Item{ID: "uB#0", Seq: 3, Role: "user", Kind: proto.KindText, TS: t0.Add(59 * time.Second), Text: "Next"}
	turnBInvalid := proto.Item{ID: "turnB#0", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: invalidCard}
	turnCUser := proto.Item{ID: "uC#0", Seq: 5, Role: "user", Kind: proto.KindText, Wake: true, TS: t0.Add(2*time.Minute - time.Second), Text: "Background wake"}
	turnCWake := proto.Item{ID: "turnC#0", Seq: 6, Role: "assistant", Kind: proto.KindText, TS: t0.Add(2 * time.Minute), Text: "Done."}
	setTranscript(turnAUser, turnAFinal, turnBUser, turnBInvalid, turnCUser, turnCWake)

	s.registerInlineTurn(ctx, ref)
	s.registerInlineTurnSettled(ctx, "server", conversation)

	keys, err := database.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"turnA#0/0", "turnA@final#1/0"}) {
		t.Fatalf("keys after rescan=%v err=%v", keys, err)
	}
	if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 2 {
		t.Fatalf("assistant_view_block count=%d, want 2", got)
	}
	gotAction, err := database.AssistantViewAction(ctx, conversation, "turnA#0/0")
	if err != nil || gotAction == nil || gotAction.Action != "yes" || gotAction.ClientMsgID != clientMsgID {
		t.Fatalf("submitted action lost or altered: %+v %v", gotAction, err)
	}
	pending, err := database.PeekAssistantViewErrors(ctx, conversation, "server", 5)
	if err != nil || len(pending) != 1 || pending[0].ItemID != "turnB#0" {
		t.Fatalf("Turn B error cleared or corrupted by Turn A rescan: %+v %v", pending, err)
	}

	// Ack Turn B's error and rescan again: it must stay acked.
	if n, err := database.AckAssistantViewErrors(ctx, conversation, "server", []string{pending[0].ItemID}, []string{pending[0].Digest}); err != nil || n != 1 {
		t.Fatalf("AckAssistantViewErrors: n=%d err=%v", n, err)
	}
	s.registerInlineTurnSettled(ctx, "server", conversation)
	pending, err = database.PeekAssistantViewErrors(ctx, conversation, "server", 5)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acked Turn B error resurfaced after rescan: %+v %v", pending, err)
	}

	// Turn D (newer valid card) lands while Turn B flushes to a new item id at its original earlier timestamp:
	// newest-first traversal advances passed_at on Turn D first so the older flushed Turn B is marked redone.
	turnBFlushed := proto.Item{ID: "turnB@flushed#1", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: invalidCard}
	turnDUser := proto.Item{ID: "uD#0", Seq: 7, Role: "user", Kind: proto.KindText, TS: t0.Add(3*time.Minute - time.Second), Text: "Fixed"}
	turnDValid := proto.Item{ID: "turnD#0", Seq: 8, Role: "assistant", Kind: proto.KindText, TS: t0.Add(3 * time.Minute), Text: validCard}
	setTranscript(turnAUser, turnAFinal, turnBUser, turnBFlushed, turnCUser, turnCWake, turnDUser, turnDValid)

	s.registerInlineTurn(ctx, ref)
	keys, err = database.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"turnA#0/0", "turnA@final#1/0", "turnD#0/0"}) {
		t.Fatalf("keys after Turn D=%v err=%v", keys, err)
	}
	if got := strings.Count(f.eventTypes(), "assistant_view_block"); got != 3 {
		t.Fatalf("assistant_view_block count after Turn D=%d, want 3", got)
	}
	pending, err = database.PeekAssistantViewErrors(ctx, conversation, "server", 5)
	if err != nil || len(pending) != 0 {
		t.Fatalf("older flushed invalid Turn B resurfaced after Turn D: %+v %v", pending, err)
	}
}

func TestInlineViewsNewestFirstAndSkipsRescannedFullText(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	st := newMemInlineViewStore()
	s := newServiceOn(t, f, st)
	conversation := "inline-newest-first-full-cache"
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true, Status: proto.StatusDone})
	ref := SessionRef{MachineID: "server", SessionID: conversation}

	card := "Conclusion\n```persona-view\n{\"fallback\":\"Finished\",\"sections\":[{\"items\":[{\"type\":\"button\",\"title\":\"OK\",\"action\":{\"id\":\"ok\",\"reply\":\"OK\"}}]}]}\n```\n"
	t0 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)

	var mu sync.Mutex
	var transcript []proto.Item
	var fetched []string
	fullItems := map[string]proto.Item{
		"olderLong#0":       {ID: "olderLong#0", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: card},
		"essayLong#0":       {ID: "essayLong#0", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: "A long plain-text report without any card."},
		"newerLong#0":       {ID: "newerLong#0", Seq: 6, Role: "assistant", Kind: proto.KindText, TS: t0.Add(2 * time.Minute), Text: card},
		"olderLong@final#1": {ID: "olderLong@final#1", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: card},
		"retryLong#0":       {ID: "retryLong#0", Seq: 8, Role: "assistant", Kind: proto.KindText, TS: t0.Add(3 * time.Minute), Text: card},
	}
	failFetch := map[string]bool{"olderLong#0": true}
	setTranscript := func(items ...proto.Item) {
		mu.Lock()
		transcript = slices.Clone(items)
		mu.Unlock()
	}
	takeFetched := func() []string {
		mu.Lock()
		out := slices.Clone(fetched)
		fetched = nil
		mu.Unlock()
		return out
	}

	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Turns int `json:"turns"`
		}
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		items := runtimePageTurns(transcript, p.Turns)
		mu.Unlock()
		return map[string]any{"items": items}, nil
	})
	f.answerNode("session.item", func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			ItemID string `json:"item_id"`
		}
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		fetched = append(fetched, p.ItemID)
		fail := failFetch[p.ItemID]
		item, ok := fullItems[p.ItemID]
		mu.Unlock()
		if fail {
			return nil, errors.New("slow node fetch failed")
		}
		if !ok {
			return nil, errors.New("item not found")
		}
		return item, nil
	})

	// Scan 1: newest-first fetches newerLong#0 before olderLong#0. Even though olderLong#0 fails, newerLong#0
	// is already registered and broadcast.
	u1 := proto.Item{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, TS: t0.Add(-time.Second), Text: "first"}
	olderTrunc := proto.Item{ID: "olderLong#0", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: "cut", FullSize: 100000}
	u2 := proto.Item{ID: "u2#0", Seq: 5, Role: "user", Kind: proto.KindText, TS: t0.Add(2*time.Minute - time.Second), Text: "second"}
	newerTrunc := proto.Item{ID: "newerLong#0", Seq: 6, Role: "assistant", Kind: proto.KindText, TS: t0.Add(2 * time.Minute), Text: "cut", FullSize: 100000}
	setTranscript(u1, olderTrunc, u2, newerTrunc)

	s.registerInlineTurn(ctx, ref)

	if got := takeFetched(); !slices.Equal(got, []string{"newerLong#0", "olderLong#0"}) {
		t.Fatalf("Scan 1 session.item order=%v, want [newerLong#0 olderLong#0]", got)
	}
	keys, err := st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"newerLong#0/0"}) {
		t.Fatalf("Scan 1 keys=%v err=%v", keys, err)
	}

	// Scan 2: olderLong#0 recovers and a 0-card long essay is added in between.
	// newerLong#0 is already cached and must NOT be re-fetched; essayLong#0 and olderLong#0 are fetched newest-first.
	mu.Lock()
	delete(failFetch, "olderLong#0")
	mu.Unlock()
	uEssay := proto.Item{ID: "uEssay#0", Seq: 3, Role: "user", Kind: proto.KindText, TS: t0.Add(59 * time.Second), Text: "report"}
	essayTrunc := proto.Item{ID: "essayLong#0", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: "cut essay", FullSize: 100000}
	setTranscript(u1, olderTrunc, uEssay, essayTrunc, u2, newerTrunc)

	s.registerInlineTurn(ctx, ref)

	if got := takeFetched(); !slices.Equal(got, []string{"essayLong#0", "olderLong#0"}) {
		t.Fatalf("Scan 2 session.item calls=%v, want [essayLong#0 olderLong#0]", got)
	}
	keys, err = st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"newerLong#0/0", "olderLong#0/0"}) {
		t.Fatalf("Scan 2 keys=%v err=%v", keys, err)
	}

	// Scan 3: rescanning all three already-scanned long replies triggers ZERO session.item round trips.
	s.registerInlineTurnSettled(ctx, "server", conversation)
	if got := takeFetched(); len(got) != 0 {
		t.Fatalf("Scan 3 unexpectedly refetched already-scanned full items: %v", got)
	}

	// Scan 4: olderLong#0 flushes to a new final item id AND essayLong#0 grows (FullSize changes):
	// both must be fetched while newerLong#0 stays skipped.
	olderFinalTrunc := proto.Item{ID: "olderLong@final#1", Seq: 2, Role: "assistant", Kind: proto.KindText, TS: t0, Text: "cut", FullSize: 100000}
	essayGrown := proto.Item{ID: "essayLong#0", Seq: 4, Role: "assistant", Kind: proto.KindText, TS: t0.Add(time.Minute), Text: "cut essay", FullSize: 120000}
	setTranscript(u1, olderFinalTrunc, uEssay, essayGrown, u2, newerTrunc)

	s.registerInlineTurn(ctx, ref)

	if got := takeFetched(); !slices.Equal(got, []string{"essayLong#0", "olderLong@final#1"}) {
		t.Fatalf("Scan 4 session.item calls=%v, want [essayLong#0 olderLong@final#1]", got)
	}
	keys, err = st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Equal(keys, []string{"newerLong#0/0", "olderLong#0/0", "olderLong@final#1/0"}) {
		t.Fatalf("Scan 4 keys=%v err=%v", keys, err)
	}

	// Scan 5: a transient RegisterAssistantViewBlock error must NOT cache the FullSize > 0 item, so the next scan
	// retries session.item and registers it cleanly.
	st.vmu.Lock()
	st.failRegisterKey = "retryLong#0/0"
	st.vmu.Unlock()
	uRetry := proto.Item{ID: "uRetry#0", Seq: 7, Role: "user", Kind: proto.KindText, TS: t0.Add(3*time.Minute - time.Second), Text: "retry"}
	retryTrunc := proto.Item{ID: "retryLong#0", Seq: 8, Role: "assistant", Kind: proto.KindText, TS: t0.Add(3 * time.Minute), Text: "cut", FullSize: 100000}
	setTranscript(u1, olderFinalTrunc, uEssay, essayGrown, u2, newerTrunc, uRetry, retryTrunc)

	s.registerInlineTurn(ctx, ref)
	if got := takeFetched(); !slices.Equal(got, []string{"retryLong#0"}) {
		t.Fatalf("Scan 5 first attempt session.item calls=%v, want [retryLong#0]", got)
	}
	keys, err = st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || slices.Contains(keys, "retryLong#0/0") {
		t.Fatalf("Scan 5 first attempt should have failed transiently: keys=%v err=%v", keys, err)
	}

	s.registerInlineTurnSettled(ctx, "server", conversation)
	if got := takeFetched(); !slices.Equal(got, []string{"retryLong#0"}) {
		t.Fatalf("Scan 5 retry session.item calls=%v, want [retryLong#0]", got)
	}
	keys, err = st.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || !slices.Contains(keys, "retryLong#0/0") {
		t.Fatalf("Scan 5 retry failed to register retryLong#0/0: keys=%v err=%v", keys, err)
	}
	s.registerInlineTurnSettled(ctx, "server", conversation)
	if got := takeFetched(); len(got) != 0 {
		t.Fatalf("after retry succeeded, rescan unexpectedly refetched: %v", got)
	}
}
