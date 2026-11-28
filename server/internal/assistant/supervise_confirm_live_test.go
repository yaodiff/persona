package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// logicalHistoryMemStore combines in-memory durable conversation history with an
// in-memory logical conversation store so tests can exercise switched logical
// conversations (e.g., Claude Code -> Antigravity) without Postgres.
type logicalHistoryMemStore struct {
	*memHistoryStore
	lmu        sync.Mutex
	logical    []db.LogicalConversation
	dispatched map[string]bool
}

var (
	_ conversationHistoryStore = (*logicalHistoryMemStore)(nil)
	_ logicalConversationStore = (*logicalHistoryMemStore)(nil)
	_ logicalSegmentInputStore = (*logicalHistoryMemStore)(nil)
)

func newLogicalHistoryMemStore(conversations ...db.LogicalConversation) *logicalHistoryMemStore {
	return &logicalHistoryMemStore{
		memHistoryStore: newMemHistoryStore(),
		logical:         conversations,
		dispatched:      map[string]bool{},
	}
}

func (st *logicalHistoryMemStore) LogicalConversations(context.Context) ([]db.LogicalConversation, error) {
	st.lmu.Lock()
	defer st.lmu.Unlock()
	out := make([]db.LogicalConversation, len(st.logical))
	for i, c := range st.logical {
		c.Segments = slices.Clone(c.Segments)
		out[i] = c
	}
	return out, nil
}

func (st *logicalHistoryMemStore) LogicalConversation(_ context.Context, machine, session string) (*db.LogicalConversation, error) {
	st.lmu.Lock()
	defer st.lmu.Unlock()
	for _, c := range st.logical {
		if c.MachineID == machine && c.SessionID == session {
			cp := c
			cp.Segments = slices.Clone(c.Segments)
			return &cp, nil
		}
		for _, seg := range c.Segments {
			if seg.MachineID == machine && seg.SessionID == session {
				cp := c
				cp.Segments = slices.Clone(c.Segments)
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (st *logicalHistoryMemStore) WithConversationEpoch(ctx context.Context, machine, session string, epoch int64, fn func(*db.LogicalConversation) error) error {
	c, err := st.LogicalConversation(ctx, machine, session)
	if err != nil {
		return err
	}
	if c == nil || c.Epoch != epoch {
		return db.ErrConversationEpoch
	}
	return fn(c)
}

func (st *logicalHistoryMemStore) WithDrainedConversationEpoch(ctx context.Context, machine, session string, epoch int64, fn func(*db.LogicalConversation) error) error {
	return st.WithConversationEpoch(ctx, machine, session, epoch, fn)
}

func (st *logicalHistoryMemStore) AdoptConversationAliases(context.Context, string, string, string, string, string, []string) error {
	return nil
}
func (st *logicalHistoryMemStore) BeginConversationSwitch(context.Context, string, string, db.ConversationSwitch) error {
	return nil
}
func (st *logicalHistoryMemStore) PrepareConversationSegment(context.Context, string, string, string, conversationlog.Segment, int) error {
	return nil
}
func (st *logicalHistoryMemStore) ActivateConversationSegment(context.Context, string, string, string, int64) error {
	return nil
}
func (st *logicalHistoryMemStore) ResolveConversationSwitch(context.Context, string, string, string, string, string) error {
	return nil
}
func (st *logicalHistoryMemStore) ConversationSwitch(context.Context, string, string, string) (*db.ConversationSwitch, error) {
	return nil, nil
}
func (st *logicalHistoryMemStore) ObserveConversationSource(context.Context, string, string, string) error {
	return nil
}
func (st *logicalHistoryMemStore) ObserveConversationRevision(context.Context, string, string, int64) error {
	return nil
}
func (st *logicalHistoryMemStore) ConversationSegmentClosed(context.Context, string) error {
	return nil
}
func (st *logicalHistoryMemStore) EnqueueConversationInput(context.Context, string, string, string, int64, json.RawMessage) error {
	return nil
}
func (st *logicalHistoryMemStore) ConversationInputReceipt(context.Context, string, string, string, json.RawMessage) (string, error) {
	return "", nil
}
func (st *logicalHistoryMemStore) ClaimConversationInput(context.Context, string, string, int64) (*db.ConversationInput, error) {
	return nil, nil
}
func (st *logicalHistoryMemStore) ResolveConversationInput(context.Context, string, string, string, string) error {
	return nil
}
func (st *logicalHistoryMemStore) ConversationSegmentDispatched(_ context.Context, _, _, segID string) (bool, error) {
	st.lmu.Lock()
	defer st.lmu.Unlock()
	return st.dispatched[segID], nil
}
func (st *logicalHistoryMemStore) MarkConversationSegmentDispatched(_ context.Context, _, _, segID string) error {
	st.lmu.Lock()
	defer st.lmu.Unlock()
	st.dispatched[segID] = true
	return nil
}

func TestDirectSupervisionConfirmBypassesStaleHistoryCache(t *testing.T) {
	t.Run("durable_history_same_rev", func(t *testing.T) {
		store := newMemHistoryStore()
		s, f := newQuietServiceOn(t, store)
		t.Cleanup(func() { stopRelayTimer(s, "conv2") })
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		// Seed an idle conversation whose durable history cache matches its current Rev.
		// On Antigravity, session.send resolves after beginLiveTurn appends the live user item,
		// while the debounced sessions list still reports the pre-send Rev and no session.items
		// push fires for unwatched sessions.
		conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 100}
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 10},
			conv2,
		)
		baseItems := []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier user prompt", TS: time.Now().Add(-5 * time.Minute)},
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "earlier reply", TS: time.Now().Add(-4 * time.Minute)},
		}
		if err := store.PutConversationPage(ctx, db.ConversationPage{
			MachineID: "server", SessionID: "conv2", History: "h-agy-1", Revision: 100,
			FromSeq: 0, ThroughSeq: 2, Tail: true, Items: baseItems,
		}); err != nil {
			t.Fatal(err)
		}

		var liveMu sync.Mutex
		liveItems := slices.Clone(baseItems)
		liveReads := 0
		f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
			var p historyParams
			_ = json.Unmarshal(raw, &p)
			if p.SessionID != "conv2" {
				return map[string]any{"items": []proto.Item{}, "history": "h-other", "from_seq": 0, "to_seq": 0}, nil
			}
			liveMu.Lock()
			defer liveMu.Unlock()
			liveReads++
			out := slices.Clone(liveItems)
			return map[string]any{"items": out, "history": "h-agy-1", "from_seq": 0, "to_seq": len(out)}, nil
		})
		f.mu.Lock()
		prevSend := f.onAssistantSend
		f.onAssistantSend = func(f *fakeHub, p map[string]any) {
			if prevSend != nil {
				prevSend(f, p)
			}
			if p["session_id"] == "conv2" {
				text, _ := p["text"].(string)
				liveMu.Lock()
				seq := len(liveItems) + 1
				liveItems = append(liveItems, proto.Item{
					ID: "live-u", Seq: seq, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now(),
				})
				liveMu.Unlock()
			}
		}
		f.mu.Unlock()

		_, g := directSupervisionTask(t, s, f, "conv2", "Stale durable cache direct supervision")
		tries := relayTries(f, "conv2")

		if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("direct supervision digest was not accepted")
		}
		stopRelayTimer(s, "conv2")

		liveMu.Lock()
		gotReads := liveReads
		liveMu.Unlock()
		if gotReads == 0 {
			t.Fatal("supervision confirmation hit stale durable history cache instead of reading live transcript")
		}
		if q := s.queuedFor("conv2"); q != 0 {
			t.Fatalf("confirmed supervision send was falsely queued: queued=%d", q)
		}
		if got := triesOf(f, tries); len(got) != 1 || got[0] == "" {
			t.Fatalf("direct sends = %q, want one send with once_id", got)
		}
		if !s.supervising("conv2") {
			t.Fatal("confirmed supervision send lost its push hold")
		}
		receipt, err := s.toolSupervisionFinish(ctx, "conv2", json.RawMessage(`{"visibility":"silent","summary":"Reviewed task cleanly."}`))
		if err != nil || !strings.Contains(receipt, `"visibility":"silent"`) {
			t.Fatalf("supervision_finish failed after confirmed send: receipt=%q err=%v", receipt, err)
		}
	})

	t.Run("logical_switched_agy_segment_keeps_retired_segment_cached", func(t *testing.T) {
		frozenEnd := 2
		retired := placeholderSegment("seg-1", 1, "server", "conv2", "claude", "retired")
		retired.Launcher, retired.SourceHistory, retired.Offset, retired.FrozenEnd = "claude", "h-claude-1", 0, &frozenEnd
		active := placeholderSegment("seg-2", 2, "server", "agy-seg2", "agy", "active")
		active.Launcher, active.SourceHistory, active.Offset = "agy", "h-agy-2", 2
		logicalConv := db.LogicalConversation{
			MachineID: "server",
			SessionID: "conv2",
			Epoch:     2,
			History:   "logical-h2",
			Revision:  50,
			Segments:  []conversationlog.Segment{retired, active},
		}
		store := newLogicalHistoryMemStore(logicalConv)
		s, f := newQuietServiceOn(t, store)
		t.Cleanup(func() { stopRelayTimer(s, "conv2") })
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		f.mu.Lock()
		f.known["conv2"] = true
		f.known["agy-seg2"] = true
		f.mu.Unlock()
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 10},
			proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 20},
			proto.Session{SessionID: "agy-seg2", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 50},
		)

		retiredItems := []proto.Item{
			{ID: "c1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier claude prompt", TS: time.Now().Add(-30 * time.Minute)},
			{ID: "c2", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "earlier claude reply", TS: time.Now().Add(-29 * time.Minute)},
		}
		activeBase := []proto.Item{
			{ID: "a1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "first agy prompt", TS: time.Now().Add(-5 * time.Minute)},
			{ID: "a2", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "first agy reply", TS: time.Now().Add(-4 * time.Minute)},
		}
		if err := store.PutConversationPage(ctx, db.ConversationPage{
			MachineID: "server", SessionID: "conv2", History: "h-claude-1", Revision: 20,
			FromSeq: 0, ThroughSeq: 2, Tail: true, Items: retiredItems,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutConversationPage(ctx, db.ConversationPage{
			MachineID: "server", SessionID: "agy-seg2", History: "h-agy-2", Revision: 50,
			FromSeq: 0, ThroughSeq: 2, Tail: true, Items: activeBase,
		}); err != nil {
			t.Fatal(err)
		}

		var liveMu sync.Mutex
		activeItems := slices.Clone(activeBase)
		activeNodeReads := 0
		retiredNodeReads := 0
		var activeSends []string
		f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
			var p historyParams
			_ = json.Unmarshal(raw, &p)
			liveMu.Lock()
			defer liveMu.Unlock()
			switch p.SessionID {
			case "agy-seg2":
				activeNodeReads++
				out := slices.Clone(activeItems)
				return map[string]any{"items": out, "history": "h-agy-2", "from_seq": 0, "to_seq": len(out)}, nil
			case "conv2":
				retiredNodeReads++
				return nil, errors.New("retired segment file unavailable on node")
			default:
				return map[string]any{"items": []proto.Item{}, "history": "h-other", "from_seq": 0, "to_seq": 0}, nil
			}
		})
		f.mu.Lock()
		prevSend := f.onAssistantSend
		f.onAssistantSend = func(f *fakeHub, p map[string]any) {
			if prevSend != nil {
				prevSend(f, p)
			}
			if p["session_id"] == "agy-seg2" {
				text, _ := p["text"].(string)
				onceID, _ := p["once_id"].(string)
				liveMu.Lock()
				activeSends = append(activeSends, onceID)
				seq := len(activeItems) + 1
				activeItems = append(activeItems, proto.Item{
					ID: "agy-live-u", Seq: seq, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now(),
				})
				liveMu.Unlock()
			}
		}
		f.mu.Unlock()

		_, g := directSupervisionTask(t, s, f, "conv2", "Switched agy supervision")

		if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("logical supervision digest was not accepted")
		}
		stopRelayTimer(s, "conv2")

		liveMu.Lock()
		gotActiveReads, gotRetiredReads := activeNodeReads, retiredNodeReads
		gotSends := slices.Clone(activeSends)
		liveMu.Unlock()
		if gotActiveReads == 0 {
			t.Fatal("active agy segment confirmation did not bypass stale durable history cache")
		}
		if gotRetiredReads != 0 {
			t.Fatalf("retired frozen segment bypassed durable history cache: node reads=%d", gotRetiredReads)
		}
		if q := s.queuedFor("conv2"); q != 0 {
			t.Fatalf("confirmed logical agy supervision was falsely queued: queued=%d", q)
		}
		if len(gotSends) != 1 || gotSends[0] == "" {
			t.Fatalf("active segment sends = %q, want 1 send with once_id", gotSends)
		}
		if !s.supervising("conv2") {
			t.Fatal("confirmed logical agy supervision lost its push hold")
		}
		receipt, err := s.toolSupervisionFinish(ctx, "conv2", json.RawMessage(`{"visibility":"silent","summary":"Handled silently on agy."}`))
		if err != nil || !strings.Contains(receipt, `"visibility":"silent"`) {
			t.Fatalf("supervision_finish failed after confirmed logical send: receipt=%q err=%v", receipt, err)
		}
	})
}

func TestQueuedSupervisionConfirmBypassesStaleHistoryCache(t *testing.T) {
	store := newMemHistoryStore()
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv2") })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()

	conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusWorking, Rev: 100}
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 10},
		conv2,
	)
	baseItems := []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier user prompt", TS: time.Now().Add(-5 * time.Minute)},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "earlier reply", TS: time.Now().Add(-4 * time.Minute)},
	}
	if err := store.PutConversationPage(ctx, db.ConversationPage{
		MachineID: "server", SessionID: "conv2", History: "h-agy-q", Revision: 100,
		FromSeq: 0, ThroughSeq: 2, Tail: true, Items: baseItems,
	}); err != nil {
		t.Fatal(err)
	}

	var liveMu sync.Mutex
	liveItems := slices.Clone(baseItems)
	liveReads := 0
	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p historyParams
		_ = json.Unmarshal(raw, &p)
		if p.SessionID != "conv2" {
			return map[string]any{"items": []proto.Item{}, "history": "h-other", "from_seq": 0, "to_seq": 0}, nil
		}
		liveMu.Lock()
		defer liveMu.Unlock()
		liveReads++
		out := slices.Clone(liveItems)
		return map[string]any{"items": out, "history": "h-agy-q", "from_seq": 0, "to_seq": len(out)}, nil
	})
	f.mu.Lock()
	prevSend := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if prevSend != nil {
			prevSend(f, p)
		}
		if p["session_id"] == "conv2" {
			text, _ := p["text"].(string)
			liveMu.Lock()
			seq := len(liveItems) + 1
			liveItems = append(liveItems, proto.Item{
				ID: "live-q-u", Seq: seq, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now(),
			})
			liveMu.Unlock()
		}
	}
	f.mu.Unlock()

	_, g := directSupervisionTask(t, s, f, "conv2", "Queued stale cache supervision")
	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("supervision digest was not queued while conversation was working")
	}
	if q := s.queuedFor("conv2"); q != 1 {
		t.Fatalf("queued count while working = %d, want 1", q)
	}

	// Conversation finishes its previous turn; drain delivers the queued supervision message
	// and must confirm against live transcript instead of stale Rev=100 DB cache.
	idleConv2 := conv2
	idleConv2.Status = proto.StatusIdle
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Rev: 10},
		idleConv2,
	)
	drainRelayTest(t, s, idleConv2)
	stopRelayTimer(s, "conv2")

	liveMu.Lock()
	gotReads := liveReads
	liveMu.Unlock()
	if gotReads == 0 {
		t.Fatal("queued supervision confirmation hit stale durable history cache")
	}
	if q := s.queuedFor("conv2"); q != 0 {
		t.Fatalf("confirmed queued supervision remained queued: %d", q)
	}
	if !s.supervising("conv2") {
		t.Fatal("confirmed queued supervision lost its push hold")
	}
}

func TestDirectSupervisionConfirmWaitsBrieflyForAsyncTranscriptFlush(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv2") })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()

	conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		conv2,
	)

	// Simulate an async transcript writer (or coalesced pre-send snapshot) where
	// assistant.send succeeds, the first two session.items reads miss, and the
	// third read observes the freshly flushed supervision user item.
	var mu sync.Mutex
	var sentText string
	reads := 0
	f.mu.Lock()
	f.omitSentItems = true
	prevSend := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if prevSend != nil {
			prevSend(f, p)
		}
		if p["session_id"] == "conv2" {
			mu.Lock()
			sentText, _ = p["text"].(string)
			mu.Unlock()
		}
	}
	f.mu.Unlock()
	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p historyParams
		_ = json.Unmarshal(raw, &p)
		if p.SessionID != "conv2" {
			return map[string]any{"items": []proto.Item{}}, nil
		}
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads < 3 || sentText == "" {
			return map[string]any{"items": []proto.Item{}}, nil
		}
		return map[string]any{"items": []proto.Item{
			{ID: "flushed-u", Seq: 1, Role: "user", Kind: proto.KindText, Text: sentText, TS: time.Now()},
		}}, nil
	})

	_, g := directSupervisionTask(t, s, f, "conv2", "Delayed flush direct supervision")
	tries := relayTries(f, "conv2")

	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("delayed-flush direct supervision was not accepted")
	}
	stopRelayTimer(s, "conv2")

	mu.Lock()
	gotReads := reads
	mu.Unlock()
	if gotReads != 3 {
		t.Fatalf("transcript reads = %d, want 3 (initial miss + settle checks)", gotReads)
	}
	if q := s.queuedFor("conv2"); q != 0 {
		t.Fatalf("delayed-flush supervision was falsely queued: queued=%d", q)
	}
	if got := triesOf(f, tries); len(got) != 1 || got[0] == "" {
		t.Fatalf("direct sends = %q, want 1 send with once_id", got)
	}
	if !s.supervising("conv2") {
		t.Fatal("delayed-flush confirmed supervision lost its push hold")
	}
	receipt, err := s.toolSupervisionFinish(ctx, "conv2", json.RawMessage(`{"visibility":"silent","summary":"Flushed and confirmed."}`))
	if err != nil || !strings.Contains(receipt, `"visibility":"silent"`) {
		t.Fatalf("supervision_finish failed after settle confirmation: receipt=%q err=%v", receipt, err)
	}
}

func TestDirectSupervisionConfirmRealFailurePreservesQueueAndRetryInvariants(t *testing.T) {
	t.Run("silent_loss_keeps_looks_1_and_retries_under_same_once_id", func(t *testing.T) {
		store := &memStore{}
		s, f := newQuietServiceOn(t, store)
		t.Cleanup(func() { stopRelayTimer(s, "conv2") })
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			conv2,
		)
		_, g := directSupervisionTask(t, s, f, "conv2", "Real silent drop task")
		tries := relayTries(f, "conv2")
		f.mu.Lock()
		f.omitSentItems = true
		f.mu.Unlock()

		if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("unconfirmed direct supervision was not persisted")
		}
		stopRelayTimer(s, "conv2")

		got := triesOf(f, tries)
		msg := savedRelay(t, store, "conv2")
		if len(got) != 1 || msg.OnceID != got[0] || msg.TriedAt.IsZero() || msg.DoubtedAt.IsZero() || msg.Looks != 1 || msg.LookAfter.IsZero() {
			t.Fatalf("persisted silent-drop relay = %+v, tries=%q, want Looks=1 and same once_id", msg, got)
		}
		if s.supervising("conv2") {
			t.Fatal("truly unconfirmed supervision kept push hold active")
		}

		// Look 2 still misses without early resend.
		advanceRelayLook(t, s, "conv2", 1)
		drainRelayTest(t, s, conv2)
		stopRelayTimer(s, "conv2")
		if len(triesOf(f, tries)) != 1 || s.queuedFor("conv2") != 1 {
			t.Fatalf("retried before look budget: tries=%q queued=%d", triesOf(f, tries), s.queuedFor("conv2"))
		}

		// Look 3 resends under the exact same once_id and confirms once the item lands.
		f.mu.Lock()
		f.omitSentItems = false
		f.mu.Unlock()
		advanceRelayLook(t, s, "conv2", 2)
		drainRelayTest(t, s, conv2)
		stopRelayTimer(s, "conv2")
		got = triesOf(f, tries)
		if len(got) != 2 || got[0] != got[1] || s.queuedFor("conv2") != 0 {
			t.Fatalf("retry after look budget: tries=%q queued=%d", got, s.queuedFor("conv2"))
		}
	})

	t.Run("read_error_stops_immediately_without_settle_spin", func(t *testing.T) {
		store := &memStore{}
		s, f := newQuietServiceOn(t, store)
		t.Cleanup(func() { stopRelayTimer(s, "conv2") })
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			conv2,
		)
		_, g := directSupervisionTask(t, s, f, "conv2", "Unreadable transcript stop check")
		f.mu.Lock()
		f.itemsFail = 1
		f.mu.Unlock()

		if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("unreadable transcript send was not persisted")
		}
		stopRelayTimer(s, "conv2")

		f.mu.Lock()
		reads := f.itemReads
		f.mu.Unlock()
		if reads != 1 {
			t.Fatalf("transcript reads on error = %d, want 1 (no settle spin on read error)", reads)
		}
		msg := savedRelay(t, store, "conv2")
		if msg.Looks != 1 || s.queuedFor("conv2") != 1 {
			t.Fatalf("persisted relay on read error = %+v queued=%d, want Looks=1 queued=1", msg, s.queuedFor("conv2"))
		}
	})
}
