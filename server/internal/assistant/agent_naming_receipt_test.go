package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const receiptArgs = `{"machine":"mac","session_id":"target-agent","since":"2026-11-14T08:35:48.209Z","before":"2026-11-14T08:44:17.085Z"}`
const originalAutonameArgs = `{"machine":"mac","session_id":"target-agent"}`

type receiptStore struct {
	*fleetMem
	cards  int
	writes *atomic.Int32
	page   func(db.InteractionQuery) (db.InteractionPage, error)
}

func (s *receiptStore) InteractionsPage(ctx context.Context, q db.InteractionQuery) (db.InteractionPage, error) {
	s.cards++
	if s.page != nil {
		return s.page(q)
	}
	return s.fleetMem.InteractionsPage(ctx, q)
}

func (s *receiptStore) Setting(ctx context.Context, key string, value any) (bool, error) {
	return s.fleetMem.memStore.Setting(ctx, key, value)
}

func (s *receiptStore) PutSetting(ctx context.Context, key string, value any) error {
	s.writes.Add(1)
	return s.fleetMem.PutSetting(ctx, key, value)
}

func (s *receiptStore) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	s.writes.Add(1)
	return s.fleetMem.InsertInteraction(ctx, ix)
}

func (s *receiptStore) FinishInteraction(ctx context.Context, id, status string, r *proto.Resolution) error {
	s.writes.Add(1)
	if err := s.fleetMem.FinishInteraction(ctx, id, status, r); err != nil {
		return err
	}
	at := time.Now().UTC()
	s.fmu.Lock()
	defer s.fmu.Unlock()
	for i := range s.recs {
		if s.recs[i].Interaction.ID == id {
			s.recs[i].Resolution = r
			s.recs[i].ResolvedAt = &at
		}
	}
	return nil
}

func (s *receiptStore) AllowAlways(ctx context.Context, session, tool string, keys []string) error {
	s.writes.Add(1)
	return s.fleetMem.AllowAlways(ctx, session, tool, keys)
}

func (s *receiptStore) MarkSeen(ctx context.Context, machine, session string, at time.Time) error {
	s.writes.Add(1)
	return s.fleetMem.MarkSeen(ctx, machine, session, at)
}

func newReceiptFleetTest(t *testing.T, st *receiptStore) *fleetTest {
	t.Helper()
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
	return &fleetTest{t: t, s: s, d: d, f: f, st: st.fleetMem}
}

func receiptFixture(t *testing.T) (*fleetTest, *receiptStore, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	calls, writes := &atomic.Int32{}, &atomic.Int32{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		namingReply(w, "must not be called")
	}))
	t.Cleanup(model.Close)
	st := &receiptStore{fleetMem: newFleetMem(), writes: writes}
	ft := newReceiptFleetTest(t, st)
	ft.d.add("personal", "Personal")
	ft.f.hello.Assistant.MachineID = "personal"
	ft.f.hello.Assistant.Machines = []string{"personal", "mac"}
	ft.f.ownModels = true
	ft.f.hello.Models.TriageAI = &ai.Config{BaseURL: model.URL, APIKey: "fixture", Model: "sorting", ReasoningEffort: "off"}
	ft.f.sessions["mac"] = nil
	ft.sessions("mac", proto.Session{SessionID: "target-agent", Agent: "claude", Title: "Claude Code", Cwd: "/work/pdf-export-project", Status: proto.StatusIdle})
	ft.d.setSessions("personal", []proto.Session{
		{SessionID: "assistant-conversation", Agent: "claude", Title: "Persona", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle},
		{SessionID: "other-assistant", Agent: "claude", Title: "Persona", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle},
	})
	// Fixture-start events are already recorded by this point; assertions below measure only this read.
	ft.f.mu.Lock()
	ft.f.calls = nil
	ft.f.told = nil
	ft.f.mu.Unlock()
	return ft, st, calls, writes
}

func receiptRecord(machine, self, id, status string, at time.Time, allow *bool, input string) db.InteractionRecord {
	var resolution *proto.Resolution
	var resolved *time.Time
	if status != "pending" {
		at := at.Add(5 * time.Minute)
		resolved = &at
	}
	if allow != nil {
		resolution = &proto.Resolution{Allow: allow, Values: map[string]string{"secret": "must-not-leak"}, Reason: "must-not-leak", Text: "provider-must-not-leak"}
	}
	return db.InteractionRecord{Interaction: proto.Interaction{ID: id, MachineID: machine, SessionID: self, Kind: proto.IxPermission,
		ToolName: "agent_autoname", ToolInput: json.RawMessage(input), Summary: "must-not-leak", CreatedAt: at},
		Status: status, Resolution: resolution, ResolvedAt: resolved}
}

func receiptCall(t *testing.T, s *Service, machine, self, args string) (proto.ToolResult, map[string]any) {
	t.Helper()
	res := s.mcpCall(context.Background(), machine, self, "agent_autoname_receipt", json.RawMessage(args), true)
	var out map[string]any
	if len(res.Content) > 0 {
		if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil && !res.IsError {
			t.Fatalf("not a JSON receipt: %+v, %v", res, err)
		}
	}
	return res, out
}

func receiptAssertNoEffects(t *testing.T, ft *fleetTest, st *receiptStore, calls, writes *atomic.Int32) {
	t.Helper()
	if calls.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("a read invoked the model or wrote settings: model=%d writes=%d", calls.Load(), writes.Load())
	}
	if len(ft.toldOf(assistantwire.EventFleetSessions)) != 0 {
		t.Fatal("a read published a session list")
	}
	for _, got := range ft.f.calls {
		switch got.Method {
		case assistantwire.DeviceSessionRename, assistantwire.DeviceSessionSend, assistantwire.DeviceSessionSendOnce, assistantwire.DeviceSessionItems:
			t.Fatalf("a read invoked a node write or model request: %+v", got)
		}
	}
}

func TestAgentAutonameReceiptReadsOnlyThisCallersCardAndSnapshot(t *testing.T) {
	ft, st, calls, writes := receiptFixture(t)
	allow := true
	card := receiptRecord("personal", "assistant-conversation", "ix_original", "resolved",
		time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC), &allow, originalAutonameArgs)
	if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishInteraction(t.Context(), card.Interaction.ID, card.Status, card.Resolution); err != nil {
		t.Fatal(err)
	}
	// Other callers, tools and kinds are not this call's confirmation, even with the same target JSON.
	for _, rec := range []db.InteractionRecord{
		receiptRecord("personal", "other-assistant", "ix_other_caller", "resolved", card.Interaction.CreatedAt, &allow, originalAutonameArgs),
		receiptRecord("mac", "assistant-conversation", "ix_other_machine", "resolved", card.Interaction.CreatedAt, &allow, originalAutonameArgs),
	} {
		if err := st.InsertInteraction(t.Context(), rec.Interaction); err != nil {
			t.Fatal(err)
		}
	}
	named := time.Date(2026, 11, 14, 8, 40, 0, 0, time.UTC)
	putAgentNameRecord(t, ft.s, agentNameRecord{Machine: "mac", Session: "target-agent", Title: "Fix PDF export images", Requested: true, NamedAt: named, LastFailure: "not-a-request-reason"})
	beforeWrites := writes.Load()
	res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", receiptArgs)
	if res.IsError || out["confirmation"] != "found" || out["coverage"] != "complete" {
		t.Fatalf("receipt: %+v, %+v", res, out)
	}
	cardOut, _ := out["card"].(map[string]any)
	if cardOut["id"] != "ix_original" || cardOut["status"] != "resolved" || cardOut["allow"] != true || cardOut["resolved_at"] == nil || cardOut["resolved_at"] == "" {
		t.Fatalf("minimum confirmation: %+v", cardOut)
	}
	naming, _ := out["naming"].(map[string]any)
	if naming["requested"] != true || naming["title"] != "Fix PDF export images" || naming["status"] != "current" || naming["named_at"] != "2026-11-14T08:40:00Z" || naming["observed_at"] == nil {
		t.Fatalf("current snapshot: %+v", naming)
	}
	encoded, _ := json.Marshal(out)
	for _, forbidden := range []string{"must-not-leak", "LastFailure", "last_failure", "model_started", "retryable", "model_call_id", "not-a-request-reason"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("receipt leaks %q: %s", forbidden, encoded)
		}
	}
	if writes.Load() != beforeWrites {
		t.Fatalf("read wrote settings: %d → %d", beforeWrites, writes.Load())
	}
	if calls.Load() != 0 {
		t.Fatal("read invoked the naming model")
	}
	if len(st.recs) != 3 {
		t.Fatal("read changed the existing history")
	}
}

func TestAgentAutonameReceiptKeepsUnknownAndAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name, confirmation, coverage string
		cards                        int
		page                         func(db.InteractionQuery) (db.InteractionPage, error)
		prepare                      func(*receiptStore)
	}{
		{name: "no record", confirmation: "unknown", coverage: "complete"},
		{name: "multiple matching cards", confirmation: "ambiguous", coverage: "complete", cards: 2, prepare: func(st *receiptStore) {
			for i, status := range []string{"cancelled", "resolved"} {
				card := receiptRecord("personal", "assistant-conversation", fmt.Sprintf("ix_%d", i), status,
					time.Date(2026, 11, 14, 8, 35, 48, 209000000+i, time.UTC), nil, originalAutonameArgs)
				if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
					t.Fatal(err)
				}
				if err := st.FinishInteraction(t.Context(), card.Interaction.ID, card.Status, card.Resolution); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "history read error", confirmation: "unknown", coverage: "unknown", page: func(db.InteractionQuery) (db.InteractionPage, error) {
			return db.InteractionPage{}, errors.New("history unavailable")
		}},
		{name: "incomplete pages", confirmation: "unknown", coverage: "partial", page: func() func(db.InteractionQuery) (db.InteractionPage, error) {
			calls := 0
			return func(q db.InteractionQuery) (db.InteractionPage, error) {
				calls++
				if calls > 1 {
					if q.Before.IsZero() {
						t.Error("the second page lost its cursor")
					}
					at := q.Before
					return db.InteractionPage{NextBefore: &at, NextBeforeID: "ix_more"}, nil
				}
				card := receiptRecord("personal", "assistant-conversation", "ix_partial", "cancelled",
					time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC), nil, originalAutonameArgs)
				at := time.Date(2026, 11, 14, 8, 40, 0, 0, time.UTC)
				return db.InteractionPage{Records: []db.InteractionRecord{card}, NextBefore: &at, NextBeforeID: "ix_more"}, nil
			}
		}()},
		{name: "cursor reaches the lower bound", confirmation: "ambiguous", coverage: "partial", prepare: func(st *receiptStore) {
			// More than one page at the exact lower-bound timestamp. The real (created_at,id) cursor must not
			// mistake the last row of page one for the end of that timestamp.
			at := time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC)
			for i := 0; i < autonameReceiptPage*2; i++ {
				card := receiptRecord("personal", "assistant-conversation", fmt.Sprintf("ix_%03d", i), "cancelled", at, nil, originalAutonameArgs)
				if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, st, _, _ := receiptFixture(t)
			st.page = tc.page
			if tc.prepare != nil {
				tc.prepare(st)
			}
			args := receiptArgs
			var input autonameReceiptQuery
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				t.Fatal(err)
			}
			since, before, err := autonameReceiptBounds(input.Since, input.Before)
			if err != nil || since.IsZero() || before.IsZero() {
				t.Fatalf("receipt window: %v %v %v", since, before, err)
			}
			res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", args)
			if res.IsError || out["confirmation"] != tc.confirmation || out["coverage"] != tc.coverage {
				t.Fatalf("receipt: %+v, %+v", res, out)
			}
			if tc.name == "multiple matching cards" && len(out["cards"].([]any)) != 2 {
				t.Fatalf("ambiguous evidence was collapsed: %+v", out)
			}
			if tc.name == "no record" && out["naming"].(map[string]any)["status"] != "not_found" {
				t.Fatalf("missing ledger invented a title: %+v", out)
			}
		})
	}
}

// Real Store pagination, including (created_at,id) at the lower bound: a second same-timestamp card is still
// eligible on the next page. The old shortcut would have declared the window complete and returned one card.
// Same timestamp, two pages: both matching cards survive the real (created_at,id) cursor, so the result is
// ambiguous, not a single-card match. One page must never be mistaken for the whole lower-bound timestamp.
func TestAgentAutonameReceiptSameTimestampAcrossPagesIsAmbiguous(t *testing.T) {
	ft, st, calls, writes := receiptFixture(t)
	at := time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC)
	for _, id := range []string{"ix_a", "ix_b"} {
		card := receiptRecord("personal", "assistant-conversation", id, "cancelled", at, nil, originalAutonameArgs)
		if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
			t.Fatal(err)
		}
	}
	res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", receiptArgs)
	if res.IsError || out["confirmation"] != "ambiguous" || out["coverage"] != "complete" {
		t.Fatalf("same-timestamp cards collapsed: %+v, %+v", res, out)
	}
	cards := out["cards"].([]any)
	if len(cards) != 2 {
		t.Fatalf("missing a card from the next page: %+v", cards)
	}
	first, _ := cards[0].(map[string]any)
	second, _ := cards[1].(map[string]any)
	if first["id"] != "ix_b" || second["id"] != "ix_a" {
		t.Fatalf("the (created_at,id) cursor order was not respected: %+v", cards)
	}
	if st.cards != 1 {
		t.Fatalf("both same-timestamp cards must fit the first page: %d", st.cards)
	}
	writes.Store(0)
	receiptAssertNoEffects(t, ft, st, calls, writes)
}

// Same timestamp across the page cap: the real (created_at,id) cursor must not declare the window complete just
// because page one ended at since. With more matches than the cap, coverage stays partial.
func TestAgentAutonameReceiptSameTimestampPageCapIsNotComplete(t *testing.T) {
	ft, st, calls, writes := receiptFixture(t)
	at := time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC)
	for i := 0; i < autonameReceiptPage*2; i++ {
		card := receiptRecord("personal", "assistant-conversation", fmt.Sprintf("ix_%03d", i), "cancelled", at, nil, originalAutonameArgs)
		if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
			t.Fatal(err)
		}
	}
	res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", receiptArgs)
	if res.IsError || out["confirmation"] != "ambiguous" || out["coverage"] != "partial" {
		t.Fatalf("the page cap was not retained: %+v, %+v", res, out)
	}
	if len(out["cards"].([]any)) != autonameReceiptPage*2 {
		t.Fatalf("same-timestamp cards were lost across the real cursor: %+v", out)
	}
	if st.cards != autonameReceiptMaxPage {
		t.Fatalf("the bounded reader did not stop at its page cap: %d", st.cards)
	}
	writes.Store(0)
	receiptAssertNoEffects(t, ft, st, calls, writes)
}

// The first Store query carries the exclusive upper bound, so a large number of later cards cannot crowd out the
// original two-field autoname card. [since,before) is consistent with the existing pager, not a local inclusive filter.
func TestAgentAutonameReceiptWindowBoundsAtStoreAndLaterCardsCannotCrowdIt(t *testing.T) {
	ft, st, calls, writes := receiptFixture(t)
	at := time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC)
	card := receiptRecord("personal", "assistant-conversation", "ix_original", "resolved", at, nil, originalAutonameArgs)
	if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		later := receiptRecord("personal", "assistant-conversation", fmt.Sprintf("ix_later_%03d", i), "cancelled", at.Add(time.Hour+time.Duration(i)*time.Second), nil, originalAutonameArgs)
		if err := st.InsertInteraction(t.Context(), later.Interaction); err != nil {
			t.Fatal(err)
		}
	}
	upper := receiptRecord("personal", "assistant-conversation", "ix_upper", "cancelled", time.Date(2026, 11, 14, 8, 44, 17, 85000000, time.UTC), nil, originalAutonameArgs)
	if err := st.InsertInteraction(t.Context(), upper.Interaction); err != nil {
		t.Fatal(err)
	}
	res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", receiptArgs)
	if res.IsError || out["confirmation"] != "found" || out["coverage"] != "complete" || out["card"].(map[string]any)["id"] != "ix_original" {
		t.Fatalf("window was crowded out: %+v, %+v", res, out)
	}
	if st.cards != 1 {
		t.Fatalf("window required unrelated later pages: %d", st.cards)
	}
	writes.Store(0)
	receiptAssertNoEffects(t, ft, st, calls, writes)
}

func TestAgentAutonameReceiptOriginalTwoFieldCardLayouts(t *testing.T) {
	for _, input := range []string{
		`{"machine":"mac","session_id":"target-agent"}`,
		`{ "session_id": "target-agent", "machine": "mac" }`,
		"{\n  \"session_id\":\"target-agent\",\n  \"machine\":\"mac\"\n}",
	} {
		if !autonameReceiptTargetInput(json.RawMessage(input), "mac", "target-agent") {
			t.Fatalf("a valid original two-field card did not match: %s", input)
		}
	}
	for _, input := range []string{
		`{"machine":"other","session_id":"target-agent"}`,
		`{"machine":"mac","session_id":"target-a"}`,
		`{"machine":"mac","session_id":"target-agent","since":"2026-11-14T08:35:48Z"}`,
		`{"machine":"mac","session_id":"target-agent","extra":true}`,
		`{"machine":"mac"}`,
		`{`,
		`[]`,
		`{"machine":"mac","session_id":"target-agent"} {}`,
	} {
		if autonameReceiptTargetInput(json.RawMessage(input), "mac", "target-agent") {
			t.Fatalf("an invalid or different target matched: %s", input)
		}
	}
}

// End-to-end: the actual write tool emits only machine/session_id. A later four-field evidence query must still
// find that card; the receipt query's own window never becomes the card's ToolInput.
func TestAgentAutonameReceiptFindsARealTwoFieldAutonameCard(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { namingReply(w, "must not run") }))
	t.Cleanup(model.Close)
	st := &receiptStore{fleetMem: newFleetMem(), writes: &atomic.Int32{}}
	ft := newReceiptFleetTest(t, st)
	ft.d.add("personal", "Personal")
	ft.f.hello.Assistant.MachineID = "personal"
	ft.f.hello.Assistant.Machines = []string{"personal", "mac"}
	ft.f.ownModels = true
	ft.f.hello.Models.TriageAI = &ai.Config{BaseURL: model.URL, APIKey: "fixture", Model: "sorting", ReasoningEffort: "off"}
	ft.f.sessions["mac"] = nil
	ft.f.items = []proto.Item{{Seq: 1, Role: "user", Kind: proto.KindText, Text: "修复 PDF 导出时丢失图片的问题。"}, {Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "正在检查导出图片的路径。"}}
	ft.sessions("mac", proto.Session{SessionID: "target-agent", Agent: "claude", Title: "Claude Code", TitleSource: proto.TitleSourceManual, Cwd: "/work/pdf-export-project", TitleManual: true, Status: proto.StatusIdle})
	ft.d.setSessions("personal", []proto.Session{{SessionID: "assistant-conversation", Agent: "claude", Title: "Persona", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle}})
	pending := make(chan *proto.Interaction, 1)
	go func() {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) { // sleep-ok: pendingQuestions has no change signal
			if cards := ft.s.pendingQuestions(); len(cards) == 1 {
				card := cards[0]
				if !bytes.Equal(card.ToolInput, json.RawMessage(originalAutonameArgs)) {
					t.Errorf("actual autoname input changed: %s", card.ToolInput)
				}
				pending <- &card
				return
			}
		}
		t.Error("the actual tool made no confirmation card")
		pending <- nil
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ft.s.toolAgentAutoname(ctx, "personal", "assistant-conversation", json.RawMessage(originalAutonameArgs))
		done <- err
	}()
	var card *proto.Interaction
	select {
	case card = <-pending:
	case <-time.After(6 * time.Second):
		t.Fatal("the actual tool did not ask")
	}
	if card == nil {
		t.Fatal("missing real autoname card")
	}
	allow := false
	if _, err := ft.s.Action(t.Context(), Action{Op: "answer", InteractionID: card.ID, Answer: mustJSON(proto.Resolution{Allow: &allow})}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("declining the confirmation failed: %v", err)
	}
	// Read the persisted history, not the in-memory pending status. A refusal never starts a model call.
	var history db.InteractionRecord
	for deadline := time.Now().Add(5 * time.Second); ; {
		page, err := st.InteractionsPage(t.Context(), db.InteractionQuery{Machine: "personal", Session: "assistant-conversation", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) == 1 && page.Records[0].Status == "cancelled" && page.Records[0].ResolvedAt != nil {
			history = page.Records[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the actual confirmation never ended: %+v", page.Records)
		}
		time.Sleep(5 * time.Millisecond) // sleep-ok: history is persisted asynchronously; wait for its actual cancelled record
	}
	since := card.CreatedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
	before := card.CreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	args := fmt.Sprintf(`{"machine":"mac","session_id":"target-agent","since":%q,"before":%q}`, since, before)
	res, out := receiptCall(t, ft.s, "personal", "assistant-conversation", args)
	if res.IsError || out["confirmation"] != "found" || out["coverage"] != "complete" || out["card"].(map[string]any)["id"] != card.ID {
		t.Fatalf("real two-field card was not found: %+v, %+v", res, out)
	}
	if _, hasAllow := out["card"].(map[string]any)["allow"]; hasAllow {
		t.Fatal("an unanswered confirmation was presented as approved")
	}
	if out["card"].(map[string]any)["status"] != history.Status {
		t.Fatalf("the actual canceled confirmation's status was not preserved: %+v, history=%+v", out, history)
	}
	if history.ResolvedAt == nil {
		t.Fatal("the persisted canceled card has no resolved_at")
	}
	if out["card"].(map[string]any)["resolved_at"] != history.ResolvedAt.Format(time.RFC3339Nano) {
		t.Fatalf("the actual resolved_at was not preserved: %+v, history=%+v", out, history)
	}
	for _, forbidden := range []string{originalAutonameArgs, "model_started", "retryable", "must-not-leak"} {
		if strings.Contains(res.Content[0].Text, forbidden) {
			t.Fatalf("receipt leaks input or invents an outcome: %s", res.Content[0].Text)
		}
	}
	if len(ft.d.callsTo("mac", assistantwire.DeviceSessionItems)) != 0 {
		t.Fatal("an unapproved confirmation reached the conversation reader")
	}
	if got := agentNameRecordOf(ft.s, "mac", "target-agent"); got.Title != "" || got.Requested {
		t.Fatalf("an unapproved confirmation wrote a name: %+v", got)
	}
}

func TestAgentAutonameReceiptRejectsMalformedOrBroadQueries(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"unknown caller and credentials", `{"machine":"mac","session_id":"target-agent","caller_machine":"other","caller_session":"other","token":"private"}`},
		{"session prefix", `{"machine":"mac","session_id":"target-a"}`},
		{"whitespace ids", `{"machine":" mac ","session_id":"target-agent"}`},
		{"machine name instead of exact id", `{"machine":"Mac","session_id":"target-agent"}`},
		{"one time bound", `{"machine":"mac","session_id":"target-agent","since":"2026-11-14T08:35:48Z"}`},
		{"invalid time", `{"machine":"mac","session_id":"target-agent","since":"yesterday","before":"2026-11-14T08:44:17Z"}`},
		{"backwards", `{"machine":"mac","session_id":"target-agent","since":"2026-11-14T08:44:17Z","before":"2026-11-14T08:35:48Z"}`},
		{"wide window", `{"machine":"mac","session_id":"target-agent","since":"2026-11-13T08:35:47Z","before":"2026-11-14T08:35:48Z"}`},
		{"extra document", receiptArgs + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, st, calls, writes := receiptFixture(t)
			res, _ := receiptCall(t, ft.s, "personal", "assistant-conversation", tc.args)
			if !res.IsError {
				t.Fatalf("accepted: %+v", res)
			}
			receiptAssertNoEffects(t, ft, st, calls, writes)
		})
	}
}

func TestAgentAutonameReceiptCallerAndExtensionIsolation(t *testing.T) {
	ft, st, calls, writes := receiptFixture(t)
	for _, tc := range []struct {
		name, machine, self string
		assistant           bool
		guestHint           bool
	}{
		{"ordinary agent claims assistant", "mac", "target-agent", true, false},
		{"work machine", "mac", "target-agent", true, false},
		{"other assistant conversation", "personal", "other-assistant", true, false},
		{"unknown guest", "personal", "assistant-conversation", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "work machine" {
				ft.d.machines["mac"].trust = "work"
			}
			if tc.name == "other assistant conversation" {
				allow := true
				card := receiptRecord("personal", "assistant-conversation", "ix_original", "resolved",
					time.Date(2026, 11, 14, 8, 35, 48, 209000000, time.UTC), &allow, originalAutonameArgs)
				if err := st.InsertInteraction(t.Context(), card.Interaction); err != nil {
					t.Fatal(err)
				}
				if err := st.FinishInteraction(t.Context(), card.Interaction.ID, card.Status, card.Resolution); err != nil {
					t.Fatal(err)
				}
				writes.Store(0)
			}
			cardCount := len(st.recs)
			res := receiptRequestWithGuest(t, ft.s, tc.machine, tc.self, tc.assistant, tc.guestHint, receiptArgs)
			if tc.name == "other assistant conversation" {
				if res.IsError {
					t.Fatalf("another assistant may read only its own history: %+v", res)
				}
				var out map[string]any
				if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil || out["confirmation"] != "unknown" || out["card"] != nil {
					t.Fatalf("another caller read the original assistant's card: %s, %v", res.Content[0].Text, err)
				}
			} else if !res.IsError {
				t.Fatalf("caller was authorized: %+v", res)
			}
			if len(st.recs) != cardCount {
				t.Fatalf("a read created or resolved a card: before=%d after=%d", cardCount, len(st.recs))
			}
			receiptAssertNoEffects(t, ft, st, calls, writes)
		})
	}
	if res := ft.s.mcpCall(t.Context(), "mac", "target-agent", "agent_autoname_receipt", json.RawMessage(receiptArgs), false); !res.IsError {
		t.Fatal("an ordinary Agent got a receipt")
	}
	for _, tool := range ft.s.AgentTools() {
		if tool.Name == "agent_autoname_receipt" {
			t.Fatal("the ordinary Agent catalog contains the assistant's receipt")
		}
	}
}

func receiptRequestWithGuest(t *testing.T, s *Service, machine, self string, assistant, guest bool, args string) proto.ToolResult {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"session_id": self, "name": "agent_autoname_receipt", "assistant": assistant, "guest": guest, "args": json.RawMessage(args)})
	out, handled, err := s.NodeRequest(t.Context(), machine, "mcp.call", params)
	if err != nil || !handled {
		t.Fatalf("mcp.call: handled=%v, err=%v", handled, err)
	}
	return out.(proto.ToolResult)
}

func TestAgentAutonameReceiptLedgerReadFailureAndCorruption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ledger string
		fail   bool
	}{
		{"read failure", "", true},
		{"corrupt ledger", "{", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFleetMem()
			st.vals = map[string][]byte{}
			if tc.fail {
				st.failKey(agentNamingKey, errors.New("ledger unavailable"))
			} else {
				st.vals[agentNamingKey] = []byte(tc.ledger)
			}
			s := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return st }, Logf: testLogf(t)})
			if _, err := s.autonameReceiptNaming(t.Context(), "mac", "target-agent"); err == nil {
				t.Fatal("a failed or corrupt ledger was presented as a usable naming snapshot")
			}
			if st.puts != 0 {
				t.Fatalf("a read wrote settings: %d", st.puts)
			}
		})
	}
}

func TestAgentAutonameReceiptCatalogHasOnlyNarrowReadArguments(t *testing.T) {
	s := newFleetTest(t).s
	for _, tool := range s.Tools(t.Context()) {
		if tool.Name != "agent_autoname_receipt" {
			continue
		}
		var schema struct {
			Required             []string       `json:"required"`
			Properties           map[string]any `json:"properties"`
			AdditionalProperties any            `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(schema.Required, []string{"machine", "session_id"}) || len(schema.Properties) != 4 || schema.AdditionalProperties != false {
			t.Fatalf("schema: %s", tool.InputSchema)
		}
		for _, forbidden := range []string{"url", "headers", "token", "caller_machine", "caller_session", "request_id", "query", "sql"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Fatalf("arbitrary or impersonating argument %q: %s", forbidden, tool.InputSchema)
			}
		}
		return
	}
	t.Fatal("assistant catalog lacks the receipt")
}
