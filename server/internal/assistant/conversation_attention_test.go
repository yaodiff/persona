package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func attentionTranscript(t0 time.Time, promoted, laterInput bool) []proto.Item {
	items := []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, Text: "Fix the example", TS: t0},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "Fixed", TS: t0.Add(time.Minute)},
		{Seq: 3, Role: "user", Kind: proto.KindText, Text: "Check the task", Source: &proto.MessageSource{Kind: proto.SourceSupervision}, TS: t0.Add(2 * time.Minute)},
	}
	if promoted {
		items = append(items,
			proto.Item{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "notify", ToolName: "mcp__persona__notify_user", TS: t0.Add(3 * time.Minute)},
			proto.Item{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "notify", Text: "sent", TS: t0.Add(3 * time.Minute)})
	}
	items = append(items,
		proto.Item{Seq: 6, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "finish", ToolName: "mcp__persona__supervision_finish", TS: t0.Add(4 * time.Minute)},
		proto.Item{Seq: 7, Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"silent","summary":"Handled."}`, TS: t0.Add(4 * time.Minute)},
		proto.Item{Seq: 8, Kind: "turn_duration", TS: t0.Add(5 * time.Minute)})
	if laterInput {
		items = append(items,
			proto.Item{Seq: 9, Role: "user", Kind: proto.KindText, Text: "Actually, one more thing", TS: t0.Add(6 * time.Minute)},
			proto.Item{Seq: 10, Role: "assistant", Kind: proto.KindText, Text: "On it", TS: t0.Add(7 * time.Minute)})
	}
	return items
}

func attentionReads(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.itemReads
}

func TestMainConversationSilentRoundUnreadAndCache(t *testing.T) {
	ctx := context.Background()
	st := newFleetMem()
	s, f := newQuietServiceOn(t, st)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f.setItems(attentionTranscript(t0, false, false)...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute), SeenAt: t0.Add(2 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle {
		t.Fatalf("read earlier visible round + latest silent = %s, want idle", row.State)
	}
	if got := attentionReads(f); got != 1 {
		t.Fatalf("first classification used %d transcript reads, want 1", got)
	}
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("same revision was not cached: state=%s reads=%d", row.State, attentionReads(f))
	}

	// A manager restarted on the same store uses the persisted session+Rev entry.
	again := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
	withSettings(t, f, again)
	setSettings(t, again, func(v *Settings) { v.Main = "conv1" })
	row.State = proto.StateUnread
	again.decorateMainConversationAttention(&row, again.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("restart reread the transcript: state=%s reads=%d", row.State, attentionReads(f))
	}

	// seen_at before the earlier visible answer preserves that answer's unread mark.
	row.SeenAt = t0.Add(-time.Minute)
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateUnread {
		t.Fatalf("earlier visible unread was hidden by the later silent round: %s", row.State)
	}
}

func TestMainConversationAttentionPromotionsInputAndFailuresStayVisible(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		promoted   bool
		laterInput bool
		fail       bool
		zeroRev    bool
		working    bool
		pending    int
		wantReads  int
	}{
		{name: "promotion", promoted: true, wantReads: 1},
		{name: "later genuine input", laterInput: true, wantReads: 1},
		{name: "read failure fails open", fail: true, wantReads: 1},
		{name: "no revision fails open", zeroRev: true},
		{name: "working wins", working: true},
		{name: "needs you wins", pending: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.setItems(attentionTranscript(t0, tc.promoted, tc.laterInput)...)
			if tc.fail {
				f.itemsFail = 1
			}
			row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(8 * time.Minute)}
			if tc.zeroRev {
				row.Rev = 0
			}
			if tc.working {
				row.Status, row.State = proto.StatusWorking, proto.StateWorking
			}
			row.PendingInteractions = tc.pending
			s.decorateMainConversationAttention(&row, s.settings(ctx))
			want := proto.StateUnread
			if tc.working {
				want = proto.StateWorking
			}
			if row.State != want {
				t.Fatalf("state=%s, want %s", row.State, want)
			}
			if got := attentionReads(f); got != tc.wantReads {
				t.Fatalf("transcript reads=%d, want %d", got, tc.wantReads)
			}
		})
	}
}

func TestMainConversationSilentRoundWithoutEarlierWork(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	items := attentionTranscript(t0, false, false)
	f.setItems(items[2:]...) // a complete synthetic history containing only the silent supervision round
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateIdle {
		t.Fatalf("silent-only complete history remained unread: %s", row.State)
	}
}

func TestMainConversationInternalAcceptedRoundUnread(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		verdict string
		receipt string
		want    string
	}{
		{name: "internal accepted", verdict: reviewAccepted, receipt: `{"background":true,"message":"accepted"}`, want: proto.StateIdle},
		{name: "user requested accepted", verdict: reviewAccepted, receipt: `{"background":false,"message":"accepted"}`, want: proto.StateUnread},
		{name: "needs user", verdict: reviewNeedsUser, receipt: `{"background":true,"message":"card sent"}`, want: proto.StateUnread},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			round := attentionTranscript(t0, false, false)[2:]
			items := append([]proto.Item{
				round[0],
				{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "mcp__persona__task_review", ToolInput: json.RawMessage(`{"verdict":"` + tc.verdict + `"}`), TS: t0.Add(3 * time.Minute)},
				{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: tc.receipt, TS: t0.Add(3 * time.Minute)},
			}, round[1:]...)
			f.setItems(items...)
			row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute)}
			s.decorateMainConversationAttention(&row, s.settings(context.Background()))
			if row.State != tc.want {
				t.Fatalf("state=%s, want %s", row.State, tc.want)
			}
		})
	}
}

func TestMainConversationInternalAcceptedInvalidatesV1Cache(t *testing.T) {
	ctx := context.Background()
	st := newFleetMem()
	s, f := newQuietServiceOn(t, st)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	round := attentionTranscript(t0, false, false)[2:]
	items := append([]proto.Item{
		round[0],
		{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "mcp__persona__task_review", ToolInput: json.RawMessage(`{"verdict":"accepted"}`), TS: t0.Add(3 * time.Minute)},
		{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: `{"background":true,"message":"accepted"}`, TS: t0.Add(3 * time.Minute)},
	}, round[1:]...)
	f.setItems(items...)
	if err := st.PutSetting(ctx, "assistant.conversation_attention.v1", map[string]conversationAttentionEntry{
		conversationAttentionCacheKey("server", "conv1"): {Rev: 9, HasVisible: true, VisibleAt: t0.Add(3 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("old visible cache survived the policy change: state=%s reads=%d", row.State, attentionReads(f))
	}
}

func TestMainConversationAttentionPagesForEarlierVisible(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	items := attentionTranscript(t0, false, false)
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Before int `json:"before_seq"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Before == 0 {
			return map[string]any{"items": items[2:], "more_before": true}, nil
		}
		if p.Before == 3 {
			return map[string]any{"items": items[:2]}, nil
		}
		return map[string]any{"items": []proto.Item{}}, nil
	}}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, SeenAt: t0.Add(2 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateIdle || attentionReads(f) != 2 {
		t.Fatalf("older visible page: state=%s reads=%d, want idle after two reads", row.State, attentionReads(f))
	}
}

func TestMainConversationAttentionPageCapFailsOpen(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	base := attentionTranscript(t0, false, false)[2:]
	calls := 0
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		calls++
		page := append([]proto.Item(nil), base...)
		for i := range page {
			page[i].Seq += 97 - 10*(calls-1)
		}
		return map[string]any{"items": page, "more_before": true}, nil
	}}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateUnread || calls != conversationAttentionScan {
		t.Fatalf("unbounded or fail-closed scan: state=%s calls=%d", row.State, calls)
	}
}

func TestDirectUserInteraction(t *testing.T) {
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
		want bool
	}{
		{name: "question", ix: proto.Interaction{Kind: proto.IxQuestion}, want: true},
		{name: "elicitation", ix: proto.Interaction{Kind: proto.IxElicitation}, want: true},
		{name: "owner proposal", ix: proto.Interaction{Kind: proto.IxPermission, OwnerProposal: true}, want: true},
		{name: "agent permission", ix: proto.Interaction{Kind: proto.IxPermission}},
		{name: "plan", ix: proto.Interaction{Kind: proto.IxPlan}},
		{name: "terminal dialog", ix: proto.Interaction{Kind: proto.IxScreen}},
		{name: "delegating question", ix: proto.Interaction{Kind: proto.IxQuestion, Delegating: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := directUserInteraction(tc.ix); got != tc.want {
				t.Fatalf("directUserInteraction=%v, want %v", got, tc.want)
			}
		})
	}
}

// jsonContainsPostgresJSONBNUL reports whether raw JSON contains a raw NUL byte or an unescaped \u0000
// escape sequence, which Postgres rejects for jsonb values with SQLSTATE 22P05.
func jsonContainsPostgresJSONBNUL(raw []byte) bool {
	if bytes.IndexByte(raw, 0) >= 0 {
		return true
	}
	inString := false
	for i := 0; i < len(raw); i++ {
		if !inString {
			if raw[i] == '"' {
				inString = true
			}
			continue
		}
		if raw[i] == '"' {
			inString = false
			continue
		}
		if raw[i] == '\\' {
			if i+5 < len(raw) && (raw[i+1] == 'u' || raw[i+1] == 'U') && string(raw[i+2:i+6]) == "0000" {
				return true
			}
			i++ // skip the escaped character (including '\\')
		}
	}
	return false
}

// jsonbSafeAttentionStore wraps fleetMem and enforces Postgres jsonb's SQLSTATE 22P05 NUL rejection on PutSetting.
type jsonbSafeAttentionStore struct {
	*fleetMem
	lastRaw map[string][]byte
}

func (s *jsonbSafeAttentionStore) PutSetting(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if jsonContainsPostgresJSONBNUL(raw) {
		return errors.New("ERROR: unsupported Unicode escape sequence (SQLSTATE 22P05)")
	}
	if s.lastRaw == nil {
		s.lastRaw = map[string][]byte{}
	}
	s.lastRaw[key] = raw
	return s.fleetMem.PutSetting(ctx, key, v)
}

func TestConversationAttentionCacheKeyJSONBSafeAndCollisionFree(t *testing.T) {
	if got := conversationAttentionCacheKey("server", "conv1"); got != "server|conv1" {
		t.Fatalf("standard key = %q, want %q", got, "server|conv1")
	}
	pairs := [][2]string{
		{"server", "conv1"},
		{"a|b", "c"},
		{"a", "b|c"},
		{"a%7Cb", "c"},
		{"worker/a", "b"},
		{"worker", "a/b"},
		{"m", ""},
		{"", "m"},
		{"m\x001", "s\x002"},
	}
	seen := make(map[string][2]string, len(pairs))
	entries := make(map[string]conversationAttentionEntry, len(pairs))
	for i, pair := range pairs {
		key := conversationAttentionCacheKey(pair[0], pair[1])
		if strings.ContainsRune(key, '\x00') {
			t.Fatalf("key for %q contains NUL byte: %q", pair, key)
		}
		if prev, dup := seen[key]; dup {
			t.Fatalf("key collision between %q and %q: both encoded to %q", prev, pair, key)
		}
		seen[key] = pair
		entries[key] = conversationAttentionEntry{Rev: int64(i + 1), Silent: true}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if jsonContainsPostgresJSONBNUL(raw) {
		t.Fatalf("serialized conversation attention payload contains NUL / \\u0000: %s", raw)
	}
}

func TestConversationAttentionPersistenceJSONBSafeAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := &jsonbSafeAttentionStore{fleetMem: newFleetMem()}
	s, f := newQuietServiceOn(t, st)
	logs := &logBuffer{}
	s.logf = logs.logf
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f.setItems(attentionTranscript(t0, false, false)...)

	row := proto.Session{
		MachineID: "server", SessionID: "conv1", Assistant: true,
		Status: proto.StatusDone, State: proto.StateUnread, Rev: 9,
		UpdatedAt: t0.Add(5 * time.Minute), SeenAt: t0.Add(2 * time.Minute),
	}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle {
		t.Fatalf("state = %s, want idle", row.State)
	}
	// Also persist an entry whose machine ID contains '|' to verify collision-safe escaping in JSONB.
	pipeEntry := conversationAttentionEntry{Rev: 4, Silent: true, HasVisible: true, VisibleAt: t0.Add(time.Minute)}
	s.putConversationAttention(ctx, "box|1", "conv-pipe", pipeEntry)

	if strings.Contains(logs.String(), "22P05") || strings.Contains(logs.String(), "save conversation attention:") {
		t.Fatalf("unexpected persistence error logs:\n%s", logs.String())
	}
	raw := st.lastRaw[conversationAttentionKey]
	if len(raw) == 0 {
		t.Fatalf("expected persisted payload under %q", conversationAttentionKey)
	}
	if jsonContainsPostgresJSONBNUL(raw) {
		t.Fatalf("persisted JSON contains NUL / \\u0000: %s", raw)
	}

	var stored map[string]conversationAttentionEntry
	if ok, err := st.Setting(ctx, conversationAttentionKey, &stored); err != nil || !ok {
		t.Fatalf("Setting(%q) = ok:%v err:%v", conversationAttentionKey, ok, err)
	}
	if got := stored["server|conv1"]; got.Rev != 9 || !got.Silent {
		t.Fatalf("stored entry under server|conv1 = %+v", got)
	}
	if got := stored[conversationAttentionCacheKey("box|1", "conv-pipe")]; got != pipeEntry {
		t.Fatalf("stored pipe-machine entry = %+v, want %+v", got, pipeEntry)
	}

	// Restart on the same store and verify full round-trip without re-reading the transcript.
	again, f2 := newQuietServiceOn(t, st)
	f2.setItems(attentionTranscript(t0, false, false)...)
	row.State = proto.StateUnread
	again.decorateMainConversationAttention(&row, again.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f2) != 0 {
		t.Fatalf("restart round-trip failed: state=%s reads=%d", row.State, attentionReads(f2))
	}
	gotPipe, ok := again.conversationAttentionCached("box|1", "conv-pipe", 4)
	if !ok || gotPipe != pipeEntry {
		t.Fatalf("restart pipe-machine lookup = %+v, %v; want %+v", gotPipe, ok, pipeEntry)
	}
}

func TestConversationAttentionLegacyNULKeyMigration(t *testing.T) {
	ctx := context.Background()
	base := newFleetMem()
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	// Seed a legacy store value with NUL-delimited keys, including a newer canonical conflict and a newer legacy conflict.
	legacyEntries := map[string]conversationAttentionEntry{
		"server\x00conv1":          {Rev: 9, Silent: true, HasVisible: true, VisibleAt: t0.Add(time.Minute)},
		"box|1\x00conv2":           {Rev: 3, Silent: true},
		"server|conv-canonical":    {Rev: 12, Silent: true},
		"server\x00conv-canonical": {Rev: 5, Silent: false},
		"server|conv-older":        {Rev: 4, Silent: false},
		"server\x00conv-older":     {Rev: 8, Silent: true},
	}
	if err := base.PutSetting(ctx, conversationAttentionKey, legacyEntries); err != nil {
		t.Fatal(err)
	}

	st := &jsonbSafeAttentionStore{fleetMem: base}
	s, f := newQuietServiceOn(t, st)
	logs := &logBuffer{}
	s.logf = logs.logf
	f.setItems(attentionTranscript(t0, false, false)...)

	// Reading must find migrated legacy entries without hitting the transcript RPC.
	row := proto.Session{
		MachineID: "server", SessionID: "conv1", Assistant: true,
		Status: proto.StatusDone, State: proto.StateUnread, Rev: 9,
		UpdatedAt: t0.Add(5 * time.Minute), SeenAt: t0.Add(2 * time.Minute),
	}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 0 {
		t.Fatalf("legacy NUL cache was not read on load: state=%s reads=%d", row.State, attentionReads(f))
	}
	if e, ok := s.conversationAttentionCached("box|1", "conv2", 3); !ok || !e.Silent {
		t.Fatalf("legacy NUL key with '|' machine failed to migrate: %+v, %v", e, ok)
	}
	if e, ok := s.conversationAttentionCached("server", "conv-canonical", 12); !ok || !e.Silent {
		t.Fatalf("canonical higher-rev entry lost to stale legacy key: %+v, %v", e, ok)
	}
	if e, ok := s.conversationAttentionCached("server", "conv-older", 8); !ok || !e.Silent {
		t.Fatalf("legacy higher-rev entry did not supersede stale canonical key: %+v, %v", e, ok)
	}

	// Inject a direct in-memory legacy NUL key as well; reads must still accept it and the next save must scrub it.
	s.convAttention.mu.Lock()
	s.convAttention.entries["mem\x00legacy"] = conversationAttentionEntry{Rev: 6, Silent: true}
	s.convAttention.mu.Unlock()
	if e, ok := s.conversationAttentionCached("mem", "legacy", 6); !ok || !e.Silent {
		t.Fatalf("in-memory legacy NUL key lookup failed: %+v, %v", e, ok)
	}

	// Saving another entry must succeed under strict JSONB NUL validation and emit zero NUL keys.
	s.putConversationAttention(ctx, "server", "conv-new", conversationAttentionEntry{Rev: 2, Silent: true})
	if strings.Contains(logs.String(), "22P05") || strings.Contains(logs.String(), "save conversation attention:") {
		t.Fatalf("saving after legacy load failed:\n%s", logs.String())
	}
	raw := st.lastRaw[conversationAttentionKey]
	if len(raw) == 0 || jsonContainsPostgresJSONBNUL(raw) {
		t.Fatalf("persisted JSON after migration invalid or contains NUL: %s", raw)
	}

	var stored map[string]conversationAttentionEntry
	if ok, err := st.Setting(ctx, conversationAttentionKey, &stored); err != nil || !ok {
		t.Fatalf("Setting(%q) = ok:%v err:%v", conversationAttentionKey, ok, err)
	}
	for k := range stored {
		if strings.ContainsRune(k, '\x00') {
			t.Fatalf("migrated store still contains NUL key %q", k)
		}
	}
	for _, wantKey := range []string{
		conversationAttentionCacheKey("server", "conv1"),
		conversationAttentionCacheKey("box|1", "conv2"),
		conversationAttentionCacheKey("server", "conv-canonical"),
		conversationAttentionCacheKey("server", "conv-older"),
		conversationAttentionCacheKey("mem", "legacy"),
		conversationAttentionCacheKey("server", "conv-new"),
	} {
		if _, ok := stored[wantKey]; !ok {
			t.Fatalf("migrated store missing expected key %q; got keys: %v", wantKey, stored)
		}
	}
}

func TestConversationAttentionPersistencePostgres(t *testing.T) {
	database := todoTestDB(t)
	ctx := t.Context()
	s, f := newQuietServiceOn(t, database)
	logs := &logBuffer{}
	s.logf = logs.logf
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f.setItems(attentionTranscript(t0, false, false)...)

	row := proto.Session{
		MachineID: "server", SessionID: "conv1", Assistant: true,
		Status: proto.StatusDone, State: proto.StateUnread, Rev: 9,
		UpdatedAt: t0.Add(5 * time.Minute), SeenAt: t0.Add(2 * time.Minute),
	}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle {
		t.Fatalf("state = %s, want idle", row.State)
	}
	s.putConversationAttention(ctx, "box|1", "conv-pipe", conversationAttentionEntry{Rev: 4, Silent: true})
	if strings.Contains(logs.String(), "22P05") || strings.Contains(logs.String(), "save conversation attention:") {
		t.Fatalf("Postgres JSONB save failed:\n%s", logs.String())
	}

	var stored map[string]conversationAttentionEntry
	if ok, err := database.Setting(ctx, conversationAttentionKey, &stored); err != nil || !ok {
		t.Fatalf("Postgres Setting(%q) = ok:%v err:%v", conversationAttentionKey, ok, err)
	}
	if got := stored["server|conv1"]; got.Rev != 9 || !got.Silent {
		t.Fatalf("Postgres stored server|conv1 = %+v", got)
	}
	if got := stored[conversationAttentionCacheKey("box|1", "conv-pipe")]; got.Rev != 4 || !got.Silent {
		t.Fatalf("Postgres stored pipe-machine entry = %+v", got)
	}

	again, f2 := newQuietServiceOn(t, database)
	f2.setItems(attentionTranscript(t0, false, false)...)
	row.State = proto.StateUnread
	again.decorateMainConversationAttention(&row, again.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f2) != 0 {
		t.Fatalf("Postgres restart round-trip failed: state=%s reads=%d", row.State, attentionReads(f2))
	}
}
