package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// groupLark stands in for a plugin's Feishu connector that relates new messages to events: it hands one batch of a
// chat until every message of it is in an event, writes events the way the plugin does (id from the first message,
// meta = the messages, the Feishu block, its rule tier fyi) and groups by "structure" when asked (fallback).
type groupLark struct {
	mu        sync.Mutex
	batch     *connector.InboxThing // nil once written
	placed    map[string]bool
	appends   []connector.GroupAppend
	tools     []connector.GroupToolCall
	fallbacks []connector.GroupFallback
	fallback  connector.GroupFallbackResult
	failNext  map[string]int // event (or "" for a new one) → appends to fail first
	hold      chan struct{}  // when set, appends wait for it
	count     int            // when set, the count its answers say (the hub does not read it for an event)
	ruleTier  string         // when set, the rule tier its answers carry (else the chat's fyi)
	tier      string         // when set, the tier its answers carry (a rule tier the item takes, as a migrated thing does)
	kind      string         // when set, the conversation kind its append answers carry
	at        time.Time      // when set, the deterministic activity time its append answers carry
}

func (*groupLark) ID() string   { return "lark" }
func (*groupLark) Name() string { return "飞书" }
func (*groupLark) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapGroup, connector.CapMeta}, Kinds: []string{KindGroup},
		MetaDescription: "示例 meta 说明：messages 是这件事的消息，旧的在前"}
}
func (g *groupLark) Collect(context.Context) ([]connector.InboxThing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batch == nil {
		return nil, nil
	}
	return []connector.InboxThing{*g.batch}, nil
}
func (*groupLark) Reply(context.Context, connector.InboxReply) error { return nil }
func (g *groupLark) GroupTool(_ context.Context, c connector.GroupToolCall) (json.RawMessage, error) {
	g.mu.Lock()
	g.tools = append(g.tools, c)
	g.mu.Unlock()
	return json.RawMessage(`{"messages":[{"id":"m1050","sender":"甲","text":"示例：周四评审 A 方案","avatar":"avatar:1"}]}`), nil
}
func (g *groupLark) GroupAppend(_ context.Context, a connector.GroupAppend) (connector.InboxThing, error) {
	if g.hold != nil {
		<-g.hold
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.appends = append(g.appends, a)
	if g.failNext[a.Event] > 0 {
		g.failNext[a.Event]--
		return connector.InboxThing{}, fmt.Errorf("示例：写 %q 失败", a.Event)
	}
	var meta struct {
		Messages  []string `json:"messages"`
		Continues string   `json:"continues,omitempty"`
	}
	_ = json.Unmarshal(a.Meta, &meta)
	meta.Messages = append(meta.Messages, a.Messages...)
	if a.Continues != "" {
		meta.Continues = a.Continues
	}
	raw, _ := json.Marshal(meta)
	first := strings.TrimPrefix(meta.Messages[0], "m")
	last := strings.TrimPrefix(meta.Messages[len(meta.Messages)-1], "m")
	id := a.Event
	if id == "" {
		id = "lark:F1:" + first
	}
	for _, m := range a.Messages {
		g.placed[m] = true
	}
	if g.batch != nil && !slices.ContainsFunc(g.batch.Group.New, func(m string) bool { return !g.placed[m] }) {
		g.batch = nil
	}
	kind := cmp.Or(g.kind, KindGroup)
	at := g.at
	if at.IsZero() {
		at = time.Now().Add(-time.Minute)
	}
	return connector.InboxThing{ID: id, Thread: a.Thread, Kind: kind, Title: "示例兜底标题 " + id, Sender: "丙", Preview: "示例预览",
		Context: "示例群：" + strings.Join(meta.Messages, "、"), At: at, Last: last, Meta: raw, Tier: connector.Tier(g.tier),
		Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", Count: cmp.Or(g.count, len(meta.Messages)), RuleTier: cmp.Or(g.ruleTier, TierFYI),
			Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "F1", IsGroup: kind == KindGroup, StartPosition: first})}}, nil
}
func (g *groupLark) GroupFallback(_ context.Context, f connector.GroupFallback) (connector.GroupFallbackResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallbacks = append(g.fallbacks, f)
	return g.fallback, nil
}

func (g *groupLark) counts() (appends, tools, fallbacks int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.appends), len(g.tools), len(g.fallbacks)
}

// groupBatch is a batch of chat F1's two new messages.
func groupBatch() *connector.InboxThing {
	return &connector.InboxThing{Thread: "chat:F1", Group: &connector.ThingGroup{Batch: "chat:F1@1061", New: []string{"m1060", "m1061"}},
		Context: "10:05 丙：示例 A 那个周四下午可以 (m1060)\n10:06 丁：示例 B 的接口改了 (m1061)", Rules: "示例来源规则：接口变更都需要我", Last: "1061"}
}

// groupModel is a model that answers the relating prompts with replies in turn (the last one again after that), or
// fails (down); the judge is told nothing is handed over. It keeps the relating requests.
type groupModel struct {
	mu      sync.Mutex
	replies []string
	stops   []string // stop_reason per reply ("" none): "max_tokens" cuts it off, "refusal" is a decline
	hold    bool     // hold the response until the caller gives up, without a fixed sleep
	down    bool
	calls   []capturedCall
	budgets []int
}

func newGroupModel(t *testing.T, replies ...string) (*groupModel, *ai.Client) {
	t.Helper()
	m := &groupModel{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		reply := `{"handle":false,"reason":"示例"}`
		stop := ""
		if strings.Contains(in.System, "归到「事件」里") {
			m.mu.Lock()
			c := capturedCall{System: in.System}
			if len(in.Messages) > 0 {
				c.User = in.Messages[0].Content
			}
			m.calls = append(m.calls, c)
			m.budgets = append(m.budgets, in.MaxTokens)
			down := m.down
			hold := m.hold
			i := min(len(m.calls), len(m.replies)) - 1
			reply = m.replies[i]
			if i < len(m.stops) {
				stop = m.stops[i]
			}
			m.mu.Unlock()
			if hold {
				<-r.Context().Done()
				return
			}
			if down {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"example failure"}}`))
				return
			}
		}
		body := map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}}
		if stop != "" {
			body["stop_reason"] = stop
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return m, ai.New(ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"})
}

func (m *groupModel) asked() []capturedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedCall(nil), m.calls...)
}

func (m *groupModel) tokenBudgets() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.budgets...)
}

// groupHub is a hub with the connector and model, an open event of chat F1 (lark:F1:1051) and one done yesterday
// (lark:F1:900).
func groupHub(t *testing.T, g *groupLark, model *ai.Client) (*inboxHub, *inboxMemStore) {
	t.Helper()
	h, st := admitHub(t, model, g)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Rules = "示例全局规则" })
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	for _, it := range []*InboxItem{
		{ID: "lark:F1:1051", Source: "lark", Kind: KindGroup, Title: "示例 A 方案评审", Summary: "等你定时间", Tier: TierAction, Reason: "示例",
			Last: "1051", TriagedAt: "1051", TriagedBy: "model", Count: 1, UpdatedAt: now.Add(-time.Hour), Context: "示例",
			Meta: json.RawMessage(`{"messages":["m1051"]}`), Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"},
			Lark: &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true, StartPosition: "1051"}},
		{ID: "lark:F1:900", Source: "lark", Kind: KindGroup, Title: "示例上周的发布", Tier: TierFYI, Last: "900", TriagedAt: "900",
			UpdatedAt: yesterday, DoneAt: &yesterday, DoneBy: "user", Meta: json.RawMessage(`{"messages":["m900"]}`),
			Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}, Lark: &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true}},
	} {
		h.inbox.items[it.ID] = it
	}
	return h, st
}

// setGroupConversationKind makes the batch, its candidates and append answers agree on the conversation kind.
func setGroupConversationKind(g *groupLark, h *inboxHub, kind string) {
	g.batch.Kind = kind
	g.kind = kind
	for _, id := range []string{"lark:F1:1051", "lark:F1:900"} {
		it := h.inbox.items[id]
		it.Kind = kind
		it.Lark.IsGroup = kind == KindGroup
	}
}

// groupDMJoinFixture supplies two direct-chat candidates and deterministic append activity times.
func groupDMJoinFixture(t *testing.T, fallback connector.GroupFallbackResult) (*groupLark, *inboxHub, *InboxItem, *InboxItem) {
	t.Helper()
	now := time.Now()
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: fallback, at: now}
	h, _ := groupHub(t, g, nil)
	setGroupConversationKind(g, h, KindDM)
	open, done := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:900"]
	open.UpdatedAt = now.Add(-time.Hour)
	done.UpdatedAt = now.Add(-24 * time.Hour)
	doneAt := done.UpdatedAt
	done.DoneAt = &doneAt
	done.Count = 1
	return g, h, open, done
}

func (h *inboxHub) waitGrouping() { h.inbox.grouping.wg.Wait() }

const groupAnswerOK = `{"assign":[{"message":"m1060","event":"lark:F1:1051"},{"message":"m1061","event":"new:1"}],
	"events":[{"event":"lark:F1:1051","title":"示例 A 方案周四下午评审","summary":"丙说周四下午可以，等你确认","tier":"action","reason":"等你确认"},
	{"event":"new:1","title":"示例 B 接口变更","summary":"丁说 B 的接口改了","tier":"fyi","reason":"同步","continues":"lark:F1:900"}]}`

// The model reads the conversation (one tool call), puts each new message in an event (one existing, one new that
// continues a done one); the connector writes each event once and the items take the model's words, triaged_by model.
// The batch itself never becomes an item, and once written the connector stops handing it.
func TestInboxGroupByTheModel(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m1060","n":5}}`, groupAnswerOK)
	h, st := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 2 {
		t.Fatalf("%d model calls", len(calls))
	}
	sys := calls[0].System
	quotaClause := "这个会话里还开着的事（最多 " + fmt.Sprint(groupOpenMax) + " 件）和 " +
		fmt.Sprint(int(groupDoneFor/(24*time.Hour))) + " 天内处理过的事（最多 " + fmt.Sprint(groupDoneMax) +
		" 件），开着的在前（id、title、summary、tier、version、done、meta）"
	for _, want := range []string{"示例 meta 说明：messages 是这件事的消息", "示例来源规则：接口变更都需要我", "noise（噪音）", "最多 4 次", quotaClause} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "示例全局规则") {
		t.Error("the batch's own rules should replace the global ones")
	}
	if u := calls[0].User; !strings.Contains(u, "m1060、m1061") || !strings.Contains(u, `"id":"lark:F1:1051"`) || !strings.Contains(u, `"meta":{"messages":["m1051"]}`) ||
		!strings.Contains(u, `"done":true`) || !strings.Contains(u, "还能查 4 次") {
		t.Errorf("first input:\n%s", u)
	}
	if u := calls[1].User; !strings.Contains(u, `你调了 messages_before {"message":"m1060","n":5}`) || !strings.Contains(u, "示例：周四评审 A 方案") ||
		strings.Contains(u, "avatar:1") || !strings.Contains(u, "还能查 3 次") {
		t.Errorf("second input:\n%s", u)
	}
	if len(g.tools) != 1 || g.tools[0].Thread != "chat:F1" || g.tools[0].Name != connector.GroupMessagesBefore {
		t.Fatalf("tools %+v", g.tools)
	}
	if len(g.appends) != 2 || g.appends[0].Event != "lark:F1:1051" || string(g.appends[0].Meta) != `{"messages":["m1051"]}` ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060"}) || g.appends[1].Event != "" || g.appends[1].Meta != nil ||
		!slices.Equal(g.appends[1].Messages, []string{"m1061"}) || g.appends[1].Continues != "lark:F1:900" {
		t.Fatalf("appends %+v", g.appends)
	}
	old, fresh := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:1061"]
	if old.Title != "示例 A 方案周四下午评审" || old.Summary != "丙说周四下午可以，等你确认" || old.Tier != TierAction || old.TriagedBy != "model" ||
		old.TitleBy != TitleByModel || old.Last != "1060" || old.TriagedAt != old.Last || string(old.Meta) != `{"messages":["m1051","m1060"]}` || old.Count != 2 {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Tier != TierFYI || fresh.Title != "示例 B 接口变更" || fresh.TriagedBy != "model" || fresh.TitleBy != TitleByModel || fresh.TriagedAt != fresh.Last ||
		fresh.Group == nil || fresh.Group.Thread != "chat:F1" || fresh.Lark == nil || fresh.Lark.FeedID != "F1" || fresh.Source != "lark" ||
		!strings.Contains(string(fresh.Meta), `"continues":"lark:F1:900"`) {
		t.Fatalf("new event %+v", fresh)
	}
	if len(h.inbox.items) != 3 || !st.saved("lark:F1:1051") || !st.saved("lark:F1:1061") {
		t.Fatalf("%d items, saved %v", len(h.inbox.items), st.ids)
	}
	items, _ := inboxTriageOf(t, h)
	for _, x := range items {
		m := x.(map[string]any)
		if m["id"] == "lark:F1:1061" && (m["triaged_by"] != "model" || m["meta"] != nil || m["group"] != nil) {
			t.Fatalf("listed %v", m)
		}
	}

	// The connector stops handing the batch once every message is in an event: nothing more happens.
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if n, _, _ := g.counts(); n != 2 || len(model.asked()) != 2 {
		t.Fatalf("after: %d appends, %d model calls", n, len(model.asked()))
	}
}

// A malformed answer or an answer that fails validation is handed back once with the original error. The correction
// spends one call allowance, is not recorded as a tool call, and a valid correction is written by the model.
func TestInboxGroupReasksAnInvalidAnswer(t *testing.T) {
	badAnswer := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	for _, tc := range []struct {
		name, reply, problem string
	}{
		{name: "validation", reply: badAnswer, problem: "消息 m1061 没有归到事件"},
		{name: "not JSON", reply: "not JSON", problem: ai.ErrNoObject.Error()},
		{name: "decode", reply: `{"assign":[}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			model, client := newGroupModel(t, tc.reply, groupAnswerOK)
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			calls := model.asked()
			if len(calls) != 2 {
				t.Fatalf("%d model calls, want the initial call and one correction", len(calls))
			}
			if !strings.Contains(calls[1].User, "你上一次给的结果不合格：") || !strings.Contains(calls[1].User, "改好后重新给出完整结果") {
				t.Fatalf("correction input lacks the problem:\n%s", calls[1].User)
			}
			if tc.problem != "" && !strings.Contains(calls[1].User, tc.problem) {
				t.Errorf("correction input lacks %q:\n%s", tc.problem, calls[1].User)
			}
			if !strings.Contains(calls[1].User, "还能查 3 次") || strings.Contains(calls[1].User, "你调了") {
				t.Errorf("correction did not spend only one allowance:\n%s", calls[1].User)
			}
			if len(g.fallbacks) != 0 {
				t.Fatalf("a valid correction used the fallback: %+v", g.fallbacks)
			}
			fresh := h.inbox.items["lark:F1:1061"]
			if fresh == nil || fresh.Title != "示例 B 接口变更" || fresh.TriagedBy != "model" {
				t.Fatalf("corrected event %+v", fresh)
			}
		})
	}
}

// The other malformed replies A-4 names are corrected the same way: an object that never closes (ErrUnterminated) and
// one cut off at max_tokens even after the client's own doubling (ErrTruncated).
func TestInboxGroupReasksUnterminatedAndTruncated(t *testing.T) {
	cut := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","ti`
	for _, tc := range []struct {
		name    string
		replies []string
		stops   []string
		problem string
		calls   int
	}{
		{name: "unterminated", replies: []string{`{"assign":[{"message":"m1060"`, groupAnswerOK}, problem: "unterminated", calls: 2},
		// A closed object that does not decode into the answer (a json.UnmarshalTypeError, not a syntax error).
		{name: "type mismatch", replies: []string{`{"assign":[{"message":1060,"event":"lark:F1:1051"}]}`, groupAnswerOK}, problem: "cannot unmarshal", calls: 2},
		// JSONMax asks once more with twice the room itself; both cut, ErrTruncated is the grouping's to correct.
		{name: "truncated", replies: []string{cut, cut, groupAnswerOK}, stops: []string{"max_tokens", "max_tokens"}, problem: "max_tokens", calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			model, client := newGroupModel(t, tc.replies...)
			model.stops = tc.stops
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			calls := model.asked()
			if len(calls) != tc.calls {
				t.Fatalf("%d model calls, want %d", len(calls), tc.calls)
			}
			last := calls[len(calls)-1].User
			if !strings.Contains(last, "你上一次给的结果不合格：") || !strings.Contains(last, tc.problem) {
				t.Fatalf("correction input lacks %q:\n%s", tc.problem, last)
			}
			if len(g.fallbacks) != 0 {
				t.Fatalf("a valid correction used the fallback: %+v", g.fallbacks)
			}
			if fresh := h.inbox.items["lark:F1:1061"]; fresh == nil || fresh.TriagedBy != "model" {
				t.Fatalf("corrected event %+v", fresh)
			}
		})
	}
}

// The correction is one of the four allowances, so after it only three tool calls remain: a fourth request for a tool
// is refused (the batch goes to the fallback), and a bad answer arriving when one allowance is left is corrected with
// the prompt saying no more lookups are allowed.
func TestInboxGroupCorrectionSpendsAnAllowance(t *testing.T) {
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	t.Run("three tools after a correction", func(t *testing.T) {
		model, client := newGroupModel(t, bad, tool, tool, tool, tool)
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
			New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
		h, _ := groupHub(t, g, client)
		plan, why := h.askGroup(context.Background(), sourceOf(g), *groupBatch(), h.groupEvents("lark", "chat:F1", ""))
		if plan != nil || !strings.Contains(why, "还要查") {
			t.Fatalf("plan %+v, why %q", plan, why)
		}
		if len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls-1 {
			t.Fatalf("%d model calls and %d tool calls: one correction plus three tools", len(model.asked()), len(g.tools))
		}
	})
	t.Run("bad answer with one allowance left", func(t *testing.T) {
		model, client := newGroupModel(t, tool, tool, tool, bad, groupAnswerOK)
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
		h, _ := groupHub(t, g, client)
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		calls := model.asked()
		if len(calls) != groupCalls+1 {
			t.Fatalf("%d model calls, want three tools, the bad answer and its correction", len(calls))
		}
		if last := calls[groupCalls].User; !strings.Contains(last, "你上一次给的结果不合格：") || !strings.Contains(last, "不能再查了") {
			t.Fatalf("the correction with no allowance left:\n%s", last)
		}
		if h.inbox.items["lark:F1:1061"] == nil {
			t.Fatal("not related after the correction")
		}
	})
}

// A model that takes too long is a timeout, not a malformed answer: it is not asked again, and the fallback groups the
// batch (A-4: timeouts, holds, unconfigured models and transport errors are not corrected).
func TestInboxGroupTimeoutIsNotReasked(t *testing.T) {
	step, total := groupStepTimeout, groupTimeout
	groupStepTimeout, groupTimeout = 20*time.Millisecond, time.Second
	t.Cleanup(func() { groupStepTimeout, groupTimeout = step, total })
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
	model, client := newGroupModel(t, groupAnswerOK)
	model.hold = true
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("a timeout was retried: %d model calls", len(calls))
	}
	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback call(s)", len(g.fallbacks))
	}
}

// A model that declines the request is not asked again: the same input is declined again, so a second call only spends
// one (ai.ErrRefused). The fallback groups the batch.
func TestInboxGroupRefusalIsNotReasked(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
	model, client := newGroupModel(t, groupAnswerOK)
	model.stops = []string{"refusal"}
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("a refusal was retried: %d model calls", len(calls))
	}
	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback call(s)", len(g.fallbacks))
	}
}

// The correction is shown on the request right after the bad answer only: a tool call after a good correction carries
// no problem, and the allowance the correction spent still shows in "还能查 N 次".
func TestInboxGroupCorrectionShownOnce(t *testing.T) {
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	model, client := newGroupModel(t, bad, tool, groupAnswerOK)
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 3 {
		t.Fatalf("%d model calls, want bad, corrected tool call, answer", len(calls))
	}
	if !strings.Contains(calls[1].User, "你上一次给的结果不合格：") || !strings.Contains(calls[1].User, "还能查 3 次") {
		t.Fatalf("correction input:\n%s", calls[1].User)
	}
	if strings.Contains(calls[2].User, "你上一次给的结果不合格") {
		t.Errorf("the problem outlived the request it was for:\n%s", calls[2].User)
	}
	// One tool call and one correction: two allowances of the four, so the second tool call is still allowed.
	if !strings.Contains(calls[2].User, "还能查 2 次") {
		t.Errorf("allowance after a correction and a tool call:\n%s", calls[2].User)
	}
	if h.inbox.items["lark:F1:1061"] == nil {
		t.Fatal("not related after the correction")
	}
	// Every step, the correction included, carries the batch's computed budget (never the old 0 = default).
	budgets := model.tokenBudgets()
	want := groupOutputBudget(len(groupBatch().Group.New), 2)
	if len(budgets) != 3 {
		t.Fatalf("%d budgets for 3 calls: %v", len(budgets), budgets)
	}
	for i, b := range budgets {
		if b != want {
			t.Errorf("call %d budget %d, want %d", i, b, want)
		}
	}
}

// A valid answer opening four events is accepted and written: the 3-event limit is the prompt's guidance only. The
// extra events survive foldNoise (only noise is folded) and each is written once.
func TestInboxGroupAcceptsMoreThanThreeNewEvents(t *testing.T) {
	batch := groupBatch()
	batch.Group.New = []string{"m1060", "m1061", "m1062", "m1063"}
	var assign, events []string
	for i, m := range batch.Group.New {
		ev := fmt.Sprintf("new:%d", i+1)
		assign = append(assign, fmt.Sprintf(`{"message":%q,"event":%q}`, m, ev))
		events = append(events, fmt.Sprintf(`{"event":%q,"title":"示例事件 %d","summary":"示例","tier":"fyi","reason":"同步"}`, ev, i+1))
	}
	answer := `{"assign":[` + strings.Join(assign, ",") + `],"events":[` + strings.Join(events, ",") + `]}`
	g := &groupLark{batch: batch, placed: map[string]bool{}}
	model, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("%d model calls: a valid four-event answer should not be asked again", len(calls))
	}
	if len(g.appends) != 4 || len(g.fallbacks) != 0 {
		t.Fatalf("%d appends, %d fallback(s)", len(g.appends), len(g.fallbacks))
	}
	for _, m := range batch.Group.New {
		if it := h.inbox.items["lark:F1:"+strings.TrimPrefix(m, "m")]; it == nil || it.TriagedBy != "model" {
			t.Fatalf("event of %s: %+v", m, it)
		}
	}
}

// An answer that is still invalid after its correction is the model being unavailable: the connector's fallback groups
// the batch, with the events the model would have seen. A new event keeps its rule tier ("By rule", triaged_by rules) for good. An existing
// event keeps its words and triage, with triaged_at advanced so the model coming back sorts nothing again.
func TestInboxGroupFallsBackOnABadAnswer(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}},
		New:    []connector.GroupNew{{Key: "k1", Messages: []string{"m1061"}, RuleTier: connector.TierNoise, Title: "示例结构分组"}}}}
	model, client := newGroupModel(t, `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 2 || !strings.Contains(calls[1].User, "你上一次给的结果不合格：") {
		t.Fatalf("bad answer was not corrected once: %d calls", len(calls))
	}
	h.inbox.grouping.mu.Lock()
	why := h.inbox.grouping.why["lark"]
	h.inbox.grouping.mu.Unlock()
	if !strings.Contains(why, "重问后仍不合格") {
		t.Errorf("fallback reason %q does not name the failed correction", why)
	}

	if len(g.fallbacks) != 1 || g.fallbacks[0].Batch != "chat:F1@1061" || len(g.fallbacks[0].Events) != 2 ||
		g.fallbacks[0].Events[0].ID != "lark:F1:1051" || string(g.fallbacks[0].Events[0].Meta) != `{"messages":["m1051"]}` || !g.fallbacks[0].Events[1].Done {
		t.Fatalf("fallback asked %+v", g.fallbacks)
	}
	old, fresh := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:1061"]
	if old.Tier != TierAction || old.Reason != "示例" || old.TriagedBy != "model" || old.Summary != "等你定时间" ||
		old.Title != "示例 A 方案评审" || old.Count != 2 || old.Last != "1060" || old.TriagedAt != old.Last {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Tier != TierNoise || fresh.Title != "示例结构分组" || fresh.TriagedBy != "rules" || fresh.TitleBy != TitleByRules || fresh.Reason != "按规则" {
		t.Fatalf("new event %+v", fresh)
	}
	before := len(model.asked())
	h.triageInbox(context.Background())
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock()
	if len(model.asked()) != before || old.TriagedBy != "model" {
		t.Fatal("an event triaged at this version was sorted again")
	}
}

// Only a fallback-new event loses a redundant conversation-name prefix; model titles and existing events stay unchanged.
func TestInboxGroupTitlePrefixOnlyChangesFallbackNewEvents(t *testing.T) {
	tests := []struct {
		name, where, title, want string
		byModel, existing        bool
	}{
		{name: "matching prefix", where: "示例群", title: "示例群：周四评审改期", want: "周四评审改期"},
		{name: "conversation name only", where: "示例群", title: "示例群", want: "示例群"},
		{name: "empty remainder", where: "示例群", title: "示例群：", want: "示例群："},
		{name: "ascii colon", where: "示例群", title: "示例群: 周四评审改期", want: "示例群: 周四评审改期"},
		{name: "no conversation", title: "示例群：周四评审改期", want: "示例群：周四评审改期"},
		{name: "other conversation", where: "示例群", title: "别的群：周四评审改期", want: "别的群：周四评审改期"},
		{name: "model title", where: "示例群", title: "示例群：周四评审改期", want: "示例群：周四评审改期", byModel: true},
		{name: "existing event", where: "示例群", title: "示例群：兜底标题", want: "示例 A 方案评审", existing: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{placed: map[string]bool{}}
			h, _ := groupHub(t, g, nil)
			planned := plannedEvent{messages: []string{"m1060"}, title: tc.title, tier: TierFYI}
			if tc.existing {
				planned.existing = "lark:F1:1051"
			}
			thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{Thread: "chat:F1", Event: planned.existing, Messages: planned.messages})
			if err != nil {
				t.Fatal(err)
			}
			thing.Tags = []connector.Tag{{Key: connector.TagChat, Name: tc.where}}
			got := h.putEvent(sourceOf(g), "chat:F1", planned, thing, tc.byModel)
			if got == nil || got.Title != tc.want {
				t.Fatalf("event %+v, want title %q", got, tc.want)
			}
		})
	}
}

// Fallback appends preserve an existing event's triage whether the model or rules originally sorted it. In particular,
// noise stays folded and an urgent event stays urgent; only the message count and version advance.
func TestInboxGroupFallbackKeepsExistingTriage(t *testing.T) {
	tests := []struct {
		name, title, summary, tier, reason, triagedBy string
		urgent                                        bool
	}{
		{name: "noise", title: "示例折叠标题", summary: "示例折叠总结", tier: TierNoise, reason: "示例折叠理由", triagedBy: "model"},
		{name: "rules", title: "示例规则标题", summary: "示例规则总结", tier: TierFYI, reason: "按规则", triagedBy: "rules"},
		{name: "urgent", title: "示例紧急标题", summary: "示例紧急总结", tier: TierAction, reason: "示例紧急理由", triagedBy: "model", urgent: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
				Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:1051"]
			it.Title, it.Summary, it.Tier, it.Reason, it.TriagedBy = tc.title, tc.summary, tc.tier, tc.reason, tc.triagedBy
			it.Urgent = tc.urgent
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if it.Title != tc.title || it.Summary != tc.summary || it.Tier != tc.tier || it.Reason != tc.reason ||
				it.TriagedBy != tc.triagedBy || it.Urgent != tc.urgent || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last {
				t.Fatalf("existing event %+v", it)
			}
		})
	}
}

// A connector that keeps no meta (the hub composes its events): fallback messages joining an existing event keep its
// words and triage as well, and the rule tier the connector's thing carries is not recorded as a retriage.
func TestInboxGroupFallbackKeepsExistingTriageWithoutMeta(t *testing.T) {
	batch := chatBatch()
	batch.Tier, batch.Why = connector.TierNoise, "示例来源规则：这是噪音"
	g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m2", Event: "examplechat:chat:C1#m1"}, {Message: "m3", Event: "examplechat:chat:C1#m1"}}}}
	model, client := newGroupModel(t, chatAnswer)
	model.down = true
	h := chatHub(t, g, client)
	it := h.inbox.items["examplechat:chat:C1#m1"]
	it.Summary, it.Reason = "示例总结：等你定时间", "示例理由：等你定时间"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if g.fallbacks != 1 {
		t.Fatalf("%d fallback call(s)", g.fallbacks)
	}
	if it.Title != "示例 A 方案评审" || it.Summary != "示例总结：等你定时间" || it.Tier != TierAction || it.Reason != "示例理由：等你定时间" ||
		it.TriagedBy != "model" || it.Count != 3 || it.Last != "3" || it.TriagedAt != it.Last {
		t.Fatalf("existing event %+v", it)
	}
	if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
		t.Fatalf("the event's rule tier was recorded as a retriage: %v", k)
	}
}

// A policy the user set still gives an existing event its tier when fallback messages join it: the words and the
// triager stay, the tier and reason are the policy's.
func TestInboxGroupFallbackKeepsPolicyTier(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason = TierFYI, "示例理由：同步"
	h.inbox.mu.Lock()
	h.inbox.policies = []InboxPolicy{{ID: "p1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "示例项目群", Tier: TierAction}}
	h.inbox.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if it.Tier != TierAction || it.Reason == "示例理由：同步" || it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" ||
		it.TriagedBy != "model" || it.Count != 3 || it.TriagedAt != it.Last {
		t.Fatalf("existing event %+v", it)
	}
	if k := chainKinds(it); k[len(k)-1] != ChainTriaged {
		t.Fatalf("the policy's tier was not recorded: %v", k)
	}
}

// An event a processing conversation is working on keeps its summary, tier, reason and urgency when new messages
// join it, whatever tier the rule carried by this batch says: the conversation decides, and a provisional rule tier
// is not recorded as a retriage. Both ways an event's messages are written (the connector keeps the meta, or the hub
// composes it).
func TestInboxGroupHandledEventKeepsItsTriage(t *testing.T) {
	t.Run("meta", func(t *testing.T) {
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierNoise, tier: TierNoise, fallback: connector.GroupFallbackResult{
			Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
		h, _ := groupHub(t, g, nil)
		it := h.inbox.items["lark:F1:1051"]
		it.Urgent = true
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		if it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierAction || it.Reason != "示例" ||
			it.TriagedBy != "model" || !it.Urgent || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last {
			t.Fatalf("handled event %+v", it)
		}
		if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
			t.Fatalf("the batch's rule tier was recorded as a retriage: %v", k)
		}
	})
	t.Run("no meta", func(t *testing.T) {
		batch := chatBatch()
		batch.Tier, batch.Why = connector.TierNoise, "示例来源规则：这是噪音"
		g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
			Assign: []connector.GroupAssign{{Message: "m2", Event: "examplechat:chat:C1#m1"}, {Message: "m3", Event: "examplechat:chat:C1#m1"}}}}
		model, client := newGroupModel(t, chatAnswer)
		model.down = true
		h := chatHub(t, g, client)
		it := h.inbox.items["examplechat:chat:C1#m1"]
		it.Summary, it.Reason = "示例总结：等你定时间", "示例理由：等你定时间"
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		if it.Title != "示例 A 方案评审" || it.Summary != "示例总结：等你定时间" || it.Tier != TierAction ||
			it.Reason != "示例理由：等你定时间" || it.TriagedBy != "model" || it.Count != 3 || it.Last != "3" || it.TriagedAt != it.Last {
			t.Fatalf("handled event %+v", it)
		}
		if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
			t.Fatalf("the batch's rule tier was recorded as a retriage: %v", k)
		}
	})
}

// The model down (or none set up): the fallback groups it. A model that only ever wants tools stops after four.
func TestInboxGroupFallsBackWithoutTheModel(t *testing.T) {
	fallback := connector.GroupFallbackResult{New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}}}
	for _, c := range []string{"down", "none", "tools"} {
		t.Run(c, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: fallback}
			model, client := newGroupModel(t, `{"tool":"search","args":{"query":"示例","n":5}}`)
			model.down = c == "down"
			if c == "none" {
				client = nil
			}
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()
			fresh := h.inbox.items["lark:F1:1060"]
			if len(g.fallbacks) != 1 || fresh == nil || fresh.Tier != TierAction || fresh.TriagedBy != "rules" || fresh.Count != 2 ||
				fresh.Title != "示例兜底标题 lark:F1:1060" {
				t.Fatalf("%d fallback(s), event %+v", len(g.fallbacks), fresh)
			}
			if c == "tools" && (len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls) {
				t.Fatalf("%d model calls, %d tool calls", len(model.asked()), len(g.tools))
			}
			if c == "down" && len(model.asked()) != 1 {
				t.Fatalf("transport failure was retried: %d model calls", len(model.asked()))
			}
		})
	}
}

// An invalid answer after the last allowed call goes straight to the fallback: there is no allowance for a correction.
func TestInboxGroupDoesNotReaskWhenNoCallsRemain(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	model, client := newGroupModel(t, tool, tool, tool, tool, bad, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	plan, why := h.askGroup(context.Background(), sourceOf(g), *groupBatch(), h.groupEvents("lark", "chat:F1", ""))
	if plan != nil || !strings.Contains(why, "模型的结果不合格") {
		t.Fatalf("plan %+v, why %q", plan, why)
	}
	if len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls {
		t.Fatalf("%d model calls and %d tool calls, want %d and %d", len(model.asked()), len(g.tools), groupCalls+1, groupCalls)
	}
}

// A batch handed again while it is being related (the next round, or twice in one collection) is related once.
func TestInboxGroupBatchOnce(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, hold: make(chan struct{})}
	model, client := newGroupModel(t, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	twice := []connector.InboxThing{*groupBatch(), *groupBatch()}
	if rest := h.relateBatches(context.Background(), sourceOf(g), twice); len(rest) != 0 {
		t.Fatalf("batches left as things: %+v", rest)
	}
	h.collectInbox(context.Background(), roundPoll) // still writing the first: not again
	waitFor(t, "the model asked", func() bool { return len(model.asked()) == 1 })
	h.collectInbox(context.Background(), roundPoll)
	close(g.hold)
	h.waitGrouping()
	if n, _, _ := g.counts(); n != 2 || len(model.asked()) != 1 {
		t.Fatalf("%d appends, %d model calls", n, len(model.asked()))
	}
}

// An event whose append fails is tried again alone; the others are written once.
func TestInboxGroupRetriesOneEvent(t *testing.T) {
	wait := groupRetryWait
	groupRetryWait = time.Millisecond
	t.Cleanup(func() { groupRetryWait = wait })
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, failNext: map[string]int{"": 1}}
	_, client := newGroupModel(t, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	var existing, fresh int
	for _, a := range g.appends {
		if a.Event == "" {
			fresh++
		} else {
			existing++
		}
	}
	if existing != 1 || fresh != 2 || h.inbox.items["lark:F1:1061"] == nil {
		t.Fatalf("appends: %d to the existing event, %d to the new one", existing, fresh)
	}
}

// A connector that does not declare group: its batches are not items, nor related.
func TestInboxGroupNeedsTheCapability(t *testing.T) {
	c := &inboxConnector{things: []connector.InboxThing{*groupBatch()}}
	h, st := admitHub(t, nil, c)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if len(h.inbox.items) != 0 || len(st.ids) != 0 {
		t.Fatalf("items %v", h.inbox.items)
	}
}

// The response budget grows with the batch but stays within the model's lower and upper bounds.
func TestInboxGroupOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		fresh, candidates, want int
	}{
		{fresh: 1, candidates: 0, want: 1024},
		{fresh: 8, candidates: 2, want: 1440},
		{fresh: 100, candidates: 100, want: 4096},
		// The floor: a small batch, and a batch whose candidates are many still counts only the messages.
		{fresh: 0, candidates: 0, want: 1024},
		{fresh: 3, candidates: 0, want: 1024},
		{fresh: 2, candidates: 10, want: 1024},
		// Above the floor: 4 messages and 4 candidates → 256 + 192 + 640.
		{fresh: 4, candidates: 4, want: 1088},
		// min(fresh, candidates+3) tops out at the messages themselves.
		{fresh: 8, candidates: 100, want: 1920},
		// The ceiling.
		{fresh: 1000, candidates: 1000, want: 4096},
	} {
		if got := groupOutputBudget(tc.fresh, tc.candidates); got != tc.want {
			t.Errorf("groupOutputBudget(%d, %d) = %d, want %d", tc.fresh, tc.candidates, got, tc.want)
		}
	}
}

// askGroup passes the computed budget to the model, rather than relying on JSON's default budget.
func TestInboxGroupPassesOutputBudget(t *testing.T) {
	batch := groupBatch()
	batch.Group.New = []string{"m1060", "m1061", "m1062", "m1063", "m1064", "m1065", "m1066", "m1067"}
	assign := make([]string, 0, len(batch.Group.New))
	for _, id := range batch.Group.New {
		assign = append(assign, fmt.Sprintf(`{"message":%q,"event":"new:1"}`, id))
	}
	answer := `{"assign":[` + strings.Join(assign, ",") + `],"events":[{"event":"new:1","title":"示例","tier":"fyi"}]}`
	g := &groupLark{batch: batch, placed: map[string]bool{}}
	model, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	events := h.groupEvents("lark", batch.Thread, "")
	plan, why := h.askGroup(context.Background(), sourceOf(g), *batch, events)
	if plan == nil || why != "" {
		t.Fatalf("plan %+v, why %q", plan, why)
	}
	budgets := model.tokenBudgets()
	if len(budgets) != 1 || budgets[0] != 1440 {
		t.Fatalf("model budgets %v, want [1440]", budgets)
	}
}

// The rules an answer is held to.
func TestInboxGroupAnswerChecks(t *testing.T) {
	seen := map[string]groupEvent{"e1": {ID: "e1"}, "e0": {ID: "e0", Done: true}}
	fresh := []string{"m1", "m2"}
	ev := func(id, tier string) string {
		return fmt.Sprintf(`{"event":%q,"title":"示例","tier":%q}`, id, tier)
	}
	for name, c := range map[string]struct {
		answer string
		ok     bool
	}{
		"ok":             {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"new:1"}],"events":[` + ev("e1", "fyi") + `,` + ev("new:1", "action") + `]}`, true},
		"continues done": {`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:1"}],"events":[{"event":"new:1","title":"示例","tier":"fyi","continues":"e0"}]}`, true},
		"unassigned":     {`{"assign":[{"message":"m1","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"twice":          {`{"assign":[{"message":"m1","event":"e1"},{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"not new":        {`{"assign":[{"message":"m1","event":"e1"},{"message":"m9","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"unseen":         {`{"assign":[{"message":"m1","event":"e7"},{"message":"m2","event":"e7"}],"events":[` + ev("e7", "fyi") + `]}`, false},
		"done":           {`{"assign":[{"message":"m1","event":"e0"},{"message":"m2","event":"e0"}],"events":[` + ev("e0", "fyi") + `]}`, true},
		"bad tier":       {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[` + ev("e1", "urgent") + `]}`, false},
		"no title":       {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[{"event":"e1","tier":"fyi"}]}`, false},
		"undescribed":    {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"new:1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"continues open": {`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:1"}],"events":[{"event":"new:1","title":"示例","tier":"fyi","continues":"e1"}]}`, false},
		"bad new name":   {`{"assign":[{"message":"m1","event":"new:x"},{"message":"m2","event":"new:x"}],"events":[` + ev("new:x", "fyi") + `]}`, false},
	} {
		var a groupAnswer
		if err := json.Unmarshal([]byte(c.answer), &a); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := checkGroupAnswer(a, fresh, seen); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The prompt suggests at most groupNewMax new events, but a larger valid answer is accepted.
	var a groupAnswer
	_ = json.Unmarshal([]byte(`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:2"},{"message":"m3","event":"new:3"},
		{"message":"m4","event":"new:4"}],"events":[`+ev("new:1", "fyi")+`,`+ev("new:2", "fyi")+`,`+ev("new:3", "fyi")+`,`+ev("new:4", "fyi")+`]}`), &a)
	plan, err := checkGroupAnswer(a, []string{"m1", "m2", "m3", "m4"}, seen)
	if err != nil || len(plan.events) != 4 {
		t.Errorf("four new events: plan %+v, err %v", plan, err)
	}
}

// The events tool: the conversation's events only (not another chat whose id starts the same), open ones first,
// recent done ones included and old done ones excluded; a query narrows them.
func TestInboxGroupEvents(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	long := now.Add(-4 * 24 * time.Hour)
	add := func(id, thread, title string, done *time.Time, at time.Time) {
		h.inbox.items[id] = &InboxItem{ID: id, Title: title, UpdatedAt: at, DoneAt: done, Group: &InboxGroup{Connector: "lark", Thread: thread}}
	}
	add("a", "chat:F1", "示例甲", nil, now.Add(-time.Hour))
	add("b", "chat:F1", "示例乙", &now, now)
	add("c", "chat:F1", "示例丙", &long, long)
	add("d", "chat:F10", "示例丁", nil, now)
	add("e", "chat:F1#t1", "示例戊", nil, now.Add(-2*time.Hour))
	for i := range 12 {
		add(fmt.Sprintf("x%d", i), "chat:F2", "示例", nil, now)
	}
	ids := func(evs []groupEvent) (out []string) {
		for _, e := range evs {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(h.groupEvents("lark", "chat:F1", "")); !slices.Equal(got, []string{"a", "e", "b"}) {
		t.Fatalf("events %v", got)
	}
	if got := ids(h.groupEvents("lark", "chat:F1", "乙")); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("query %v", got)
	}
	if got := h.groupEvents("lark", "chat:F2", ""); len(got) != groupOpenMax {
		t.Fatalf("%d listed", len(got))
	}
	if got := h.groupEvents("github", "chat:F1", ""); len(got) != 0 {
		t.Fatalf("another connector's: %v", ids(got))
	}
}

func addGroupCandidate(h *inboxHub, id, title string, at time.Time, done bool) {
	it := &InboxItem{ID: id, Title: title, Tier: TierFYI, Count: 1, UpdatedAt: at,
		Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}}
	if done {
		it.DoneAt = &at
	}
	h.inbox.items[id] = it
}

func groupCandidateIDs(events []groupEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

// Open and recently done events have independent quotas. Each retained group is newest first, with all open events first.
func TestInboxGroupEventsIndependentQuotas(t *testing.T) {
	tests := []struct {
		name               string
		open, done         int
		wantOpen, wantDone int
	}{
		{name: "open events do not consume the done quota", open: 12, done: 4, wantOpen: groupOpenMax, wantDone: 4},
		{name: "done events do not consume the open quota", open: 3, done: 9, wantOpen: 3, wantDone: groupDoneMax},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := admitHub(t, nil)
			now := time.Now()
			add := func(prefix string, count int, done bool, start time.Time) []string {
				ids := make([]string, 0, count)
				for i := range count {
					id := fmt.Sprintf("%s-%02d", prefix, i)
					at := start.Add(-time.Duration(i) * time.Minute)
					addGroupCandidate(h, id, "示例", at, done)
					ids = append(ids, id)
				}
				return ids
			}
			open := add("open", tc.open, false, now)
			done := add("done", tc.done, true, now.Add(-time.Hour))
			want := slices.Clone(open[:tc.wantOpen])
			want = append(want, done[:tc.wantDone]...)

			got := h.groupEvents("lark", "chat:F1", "")
			if ids := groupCandidateIDs(got); !slices.Equal(ids, want) {
				t.Fatalf("candidates %v, want %v", ids, want)
			}
			for i, event := range got {
				if wantDone := i >= tc.wantOpen; event.Done != wantDone {
					t.Errorf("candidate %d %s: done %v, want %v", i, event.ID, event.Done, wantDone)
				}
			}
		})
	}
}

// Equal activity times use the same id tie-break as newerItem, so repeated candidate lists keep their exact order.
func TestInboxGroupEventsEqualActivityOrder(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	for _, id := range []string{"open-a", "open-b"} {
		addGroupCandidate(h, id, "示例", now, false)
	}
	doneAt := now.Add(-time.Hour)
	for _, id := range []string{"done-a", "done-b"} {
		addGroupCandidate(h, id, "示例", doneAt, true)
	}
	want := []string{"open-b", "open-a", "done-b", "done-a"}
	for range 20 {
		if ids := groupCandidateIDs(h.groupEvents("lark", "chat:F1", "")); !slices.Equal(ids, want) {
			t.Fatalf("candidates %v, want the deterministic order %v", ids, want)
		}
	}
}

// Query filtering happens before either cap: newer non-matches take no slots, and both matching groups still truncate.
func TestInboxGroupEventsQueryPrecedesIndependentCaps(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	wantOpen := make([]string, 0, groupOpenMax)
	for i := 0; i < groupOpenMax+2; i++ {
		id := fmt.Sprintf("open-%02d", i)
		title := "unrelated"
		if i > 0 {
			title = "needle open"
			if i <= groupOpenMax {
				wantOpen = append(wantOpen, id)
			}
		}
		addGroupCandidate(h, id, title, now.Add(-time.Duration(i)*time.Minute), false)
	}
	wantDone := make([]string, 0, groupDoneMax)
	for i := 0; i < groupDoneMax+2; i++ {
		id := fmt.Sprintf("done-%02d", i)
		title := "unrelated"
		if i > 0 {
			title = "needle done"
			if i <= groupDoneMax {
				wantDone = append(wantDone, id)
			}
		}
		addGroupCandidate(h, id, title, now.Add(-time.Hour-time.Duration(i)*time.Minute), true)
	}
	want := append(wantOpen, wantDone...)

	got := h.groupEvents("lark", "chat:F1", "needle")
	if ids := groupCandidateIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("query candidates %v, want %v", ids, want)
	}
	var open, done int
	for _, event := range got {
		if event.Done {
			done++
		} else {
			open++
		}
	}
	if open != groupOpenMax || done != groupDoneMax {
		t.Fatalf("query retained %d open and %d done, want %d and %d", open, done, groupOpenMax, groupDoneMax)
	}
}

// The fallback receives the same independently capped candidates as the model, including done events that an aggregate
// open-first cap would have hidden. IDs, order, done flags and meta all match groupEvents.
func TestInboxGroupFallbackGetsIndependentCandidates(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, Title: "示例结构分组"}}}}
	h, _ := groupHub(t, g, nil)
	now := time.Now()
	for i := 1; i < groupOpenMax; i++ {
		addGroupCandidate(h, fmt.Sprintf("open-%02d", i), "示例", now.Add(-time.Duration(i+1)*time.Hour), false)
	}
	for i := 1; i <= groupDoneMax; i++ {
		at := now.Add(-24*time.Hour - time.Duration(i)*time.Hour)
		addGroupCandidate(h, fmt.Sprintf("done-%02d", i), "示例", at, true)
	}
	want := h.groupEvents("lark", "chat:F1", "")
	var open, done int
	for _, event := range want {
		if event.Done {
			done++
		} else {
			open++
		}
	}
	if open != groupOpenMax || done != groupDoneMax {
		t.Fatalf("fixture retained %d open and %d done, want %d and %d", open, done, groupOpenMax, groupDoneMax)
	}

	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback calls", len(g.fallbacks))
	}
	got := g.fallbacks[0].Events
	if len(got) != len(want) {
		t.Fatalf("fallback received %d candidates, want %d", len(got), len(want))
	}
	metaText := func(meta json.RawMessage) string {
		if len(meta) == 0 {
			return "null" // JSON carries an absent RawMessage as null across the connector call.
		}
		return string(meta)
	}
	for i, event := range want {
		if got[i].ID != event.ID || got[i].Done != event.Done || metaText(got[i].Meta) != metaText(event.Meta) {
			t.Errorf("fallback candidate %d = %+v, want %+v", i, got[i], event)
		}
	}
}

// groupChat is a connector that groups but keeps no meta (no group.append): the hub writes its events. Its tool and
// fallback may be missing (they answer an unknown method).
type groupChat struct {
	mu                 sync.Mutex
	batch              *connector.InboxThing
	noTool, noFallback bool
	tools, fallbacks   int
	fallback           connector.GroupFallbackResult
}

func (*groupChat) ID() string   { return "examplechat" }
func (*groupChat) Name() string { return "Example Chat" }
func (*groupChat) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapGroup}}
}
func (g *groupChat) Collect(context.Context) ([]connector.InboxThing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batch == nil {
		return nil, nil
	}
	return []connector.InboxThing{*g.batch}, nil
}
func (*groupChat) Reply(context.Context, connector.InboxReply) error { return nil }
func (g *groupChat) GroupTool(context.Context, connector.GroupToolCall) (json.RawMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tools++
	if g.noTool {
		return nil, &rpc.Error{Code: "unknown_method", Message: "unknown method group.tool"}
	}
	return json.RawMessage(`{"messages":[]}`), nil
}
func (g *groupChat) GroupFallback(context.Context, connector.GroupFallback) (connector.GroupFallbackResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallbacks++
	if g.noFallback {
		return connector.GroupFallbackResult{}, &rpc.Error{Code: "unknown_method", Message: "unknown method group.fallback"}
	}
	return g.fallback, nil
}

// chatBatch is a batch of chat C1: a heading, m2 over two lines (naming m1, which it answers), m3.
func chatBatch() *connector.InboxThing {
	return &connector.InboxThing{Thread: "chat:C1", Kind: KindGroup, Sender: "丁", Preview: "示例 B 的接口改了",
		Group:   &connector.ThingGroup{Batch: "chat:C1@3", New: []string{"m2", "m3"}},
		Context: "示例群\n[10:05] 丙：示例 A 那个周四下午可以 (id=m2, 回复 m1)\n  第二行\n[10:06] 丁：示例 B 的接口改了 (id=m3)",
		At:      time.Now().Add(-time.Minute), Last: "3"}
}

// chatHub is a hub with a connector that keeps no meta and one open event the hub wrote for it before.
func chatHub(t *testing.T, g *groupChat, model *ai.Client) *inboxHub {
	t.Helper()
	h, _ := admitHub(t, model, g)
	it := &InboxItem{ID: "examplechat:chat:C1#m1", Source: "examplechat", Kind: KindGroup, Title: "示例 A 方案评审", Tier: TierAction,
		Last: "1", TriagedAt: "1", TriagedBy: "model", Count: 1, UpdatedAt: time.Now().Add(-time.Hour),
		Context: "示例群\n[10:01] 甲：示例 A 方案什么时候评审 (id=m1)", Group: &InboxGroup{Connector: "examplechat", Thread: "chat:C1"},
		Connector: &InboxConnector{Thread: "chat:C1"}}
	h.inbox.items[it.ID] = it
	return h
}

const chatAnswer = `{"assign":[{"message":"m2","event":"examplechat:chat:C1#m1"},{"message":"m3","event":"new:1"}],
	"events":[{"event":"examplechat:chat:C1#m1","title":"示例 A 方案周四评审","summary":"丙说周四下午可以","tier":"fyi","reason":"同步"},
	{"event":"new:1","title":"示例 B 接口变更","summary":"丁说接口改了","tier":"action","reason":"要跟进"}]}`

// A connector that groups but keeps no meta: the hub writes its events from the batch, with no call to it: a new event
// gets an id of its own and the text of its messages (with the heading), an existing one its new messages after what it
// had, the batch's last and preview; title, summary and tier are the model's; no meta.
func TestInboxGroupWithoutMeta(t *testing.T) {
	g := &groupChat{batch: chatBatch()}
	_, client := newGroupModel(t, chatAnswer)
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	old, fresh := h.inbox.items["examplechat:chat:C1#m1"], h.inbox.items["examplechat:chat:C1#m3"]
	if old.Context != "示例群\n[10:01] 甲：示例 A 方案什么时候评审 (id=m1)\n[10:05] 丙：示例 A 那个周四下午可以 (id=m2, 回复 m1)\n  第二行" ||
		old.Last != "3" || old.Preview != "示例 B 的接口改了" || old.Count != 2 || old.Title != "示例 A 方案周四评审" ||
		old.Tier != TierFYI || old.TriagedBy != "model" || old.Meta != nil {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Context != "示例群\n[10:06] 丁：示例 B 的接口改了 (id=m3)" || fresh.Tier != TierAction ||
		fresh.Title != "示例 B 接口变更" || fresh.Connector == nil || fresh.Connector.Thread != "chat:C1" || fresh.Count != 1 ||
		fresh.Group == nil || fresh.Source != "examplechat" || fresh.Meta != nil || fresh.TriagedAt != fresh.Last {
		t.Fatalf("new event %+v", fresh)
	}
	if len(h.inbox.items) != 2 {
		t.Fatalf("%d items", len(h.inbox.items))
	}
}

// A connector without a tool: the model is told it is not there and goes on with what it has.
func TestInboxGroupToolMissing(t *testing.T) {
	g := &groupChat{batch: chatBatch(), noTool: true}
	model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m2","n":5}}`, chatAnswer)
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	calls := model.asked()
	if g.tools != 1 || len(calls) != 2 || !strings.Contains(calls[1].User, "这个工具用不了") {
		t.Fatalf("%d tool call(s), %d model call(s)", g.tools, len(calls))
	}
	if h.inbox.items["examplechat:chat:C1#m3"] == nil {
		t.Fatal("not related after the missing tool")
	}
}

// A connector without a fallback: with the model down the batch waits (nothing written) and comes in once the model
// is back, as an inbox item without a fallback does.
func TestInboxGroupWithoutFallbackWaits(t *testing.T) {
	g := &groupChat{batch: chatBatch(), noFallback: true}
	model, client := newGroupModel(t, chatAnswer)
	model.down = true
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if g.fallbacks != 1 || len(h.inbox.items) != 1 || h.inbox.items["examplechat:chat:C1#m1"].Last != "1" {
		t.Fatalf("written without the model or a fallback: %d fallback call(s), %d items", g.fallbacks, len(h.inbox.items))
	}
	model.mu.Lock()
	model.down = false
	model.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if h.inbox.items["examplechat:chat:C1#m3"] == nil {
		t.Fatal("not written once the model is back")
	}
}

// What a batch's text gives each event: the lines of its messages (an id as a word of its own: m1 is not m10; id=<id>,
// else the last id a line names: an answer naming what it answers is still its own), continuation lines with them, the
// heading with new events; a text naming none is the whole text.
func TestInboxEventText(t *testing.T) {
	text := "群：示例\n[1] 甲：一 (m1)\n  接着说\n[2] 乙：二，回 m1 (m10)\n[3] 丙：三 (id=m2, 回复 m10)"
	all := []string{"m1", "m10", "m2"}
	for _, c := range []struct {
		mine []string
		head bool
		want string
	}{
		{[]string{"m1"}, true, "群：示例\n[1] 甲：一 (m1)\n  接着说"},
		{[]string{"m10"}, false, "[2] 乙：二，回 m1 (m10)"},
		{[]string{"m1", "m2"}, false, "[1] 甲：一 (m1)\n  接着说\n[3] 丙：三 (id=m2, 回复 m10)"},
	} {
		if got := eventText(text, all, c.mine, c.head); got != c.want {
			t.Errorf("%v: %q", c.mine, got)
		}
	}
	if got := eventText("没有 id 的文字", all, []string{"m1"}, true); got != "没有 id 的文字" {
		t.Errorf("no ids: %q", got)
	}
}

// The hub keeps an event's count: a new one starts at the messages put in it and every later batch adds its own; what
// the connector says (its meta may keep only the newest messages) is not read. The event's start stays.
func TestInboxGroupEventCount(t *testing.T) {
	batch := func(last string, msgs ...string) *connector.InboxThing {
		return &connector.InboxThing{Thread: "chat:F1", Group: &connector.ThingGroup{Batch: "chat:F1@" + last, New: msgs},
			Context: "示例：" + strings.Join(msgs, "、"), Last: last}
	}
	assign := func(event string, msgs ...string) string {
		var a []string
		for _, m := range msgs {
			a = append(a, fmt.Sprintf(`{"message":%q,"event":%q}`, m, event))
		}
		return `{"assign":[` + strings.Join(a, ",") + `],"events":[{"event":"` + event + `","title":"示例","summary":"示例","tier":"fyi"}]}`
	}
	g := &groupLark{batch: batch("1061", "m1060", "m1061"), placed: map[string]bool{}, count: 99}
	_, client := newGroupModel(t, assign("new:1", "m1060", "m1061"), assign("lark:F1:1060", "m1062"), assign("lark:F1:1060", "m1063", "m1064"))
	h, _ := groupHub(t, g, client)
	round := func(next *connector.InboxThing) *InboxItem {
		t.Helper()
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()
		g.mu.Lock()
		g.batch = next
		g.mu.Unlock()
		return h.inbox.items["lark:F1:1060"]
	}
	it := round(batch("1062", "m1062"))
	if it == nil || it.Count != 2 {
		t.Fatalf("new event: %+v", it)
	}
	started := it.StartedAt
	if it = round(batch("1064", "m1063", "m1064")); it.Count != 3 || !it.StartedAt.Equal(started) {
		t.Fatalf("after one more: count %d, started %v (was %v)", it.Count, it.StartedAt, started)
	}
	if it = round(nil); it.Count != 5 || !it.StartedAt.Equal(started) || it.Last != "1064" {
		t.Fatalf("after two more: count %d, last %s", it.Count, it.Last)
	}
}

// A done event is a valid assignment target. The optional reopen flag is retained in the plan; omitted means reopen.
func TestInboxGroupAnswerReopen(t *testing.T) {
	seen := map[string]groupEvent{"e0": {ID: "e0", Done: true}}
	for _, tc := range []struct {
		name, field string
		want        *bool
	}{
		{name: "omitted"},
		{name: "true", field: `,"reopen":true`, want: func() *bool { v := true; return &v }()},
		{name: "false", field: `,"reopen":false`, want: func() *bool { v := false; return &v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := `{"assign":[{"message":"m1","event":"e0"}],"events":[{"event":"e0","title":"示例","tier":"fyi"` + tc.field + `}]}`
			var out groupAnswer
			if err := json.Unmarshal([]byte(answer), &out); err != nil {
				t.Fatal(err)
			}
			plan, err := checkGroupAnswer(out, []string{"m1"}, seen)
			if err != nil || len(plan.events) != 1 {
				t.Fatalf("plan %+v, err %v", plan, err)
			}
			got := plan.events[0].reopen
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("reopen %v, want %v", got, tc.want)
			}
		})
	}
}

// Follow-up messages append to the handled event itself. Reopening is the default: the model's triage replaces the old
// version, completion clears, and received is recorded before reopened with the item's source as via.
func TestInboxGroupReopensADoneEvent(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
		"events":[{"event":"lark:F1:900","title":"示例发布跟进","summary":"对方补充了时间","tier":"action","reason":"继续追问","urgent":true}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID || g.appends[0].Continues != "" ||
		string(g.appends[0].Meta) != `{"messages":["m900"]}` || !slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if h.inbox.items["lark:F1:1060"] != nil || len(h.inbox.items) != 2 {
		t.Fatalf("a continuation was opened instead: %v", h.inbox.items)
	}
	if !it.open() || it.DoneBy != "" || it.Title != "示例发布跟进" || it.Summary != "对方补充了时间" ||
		it.Tier != TierAction || it.Reason != "继续追问" || it.TriagedBy != "model" || it.TitleBy != TitleByModel || !it.Urgent ||
		it.Count != 3 || it.Last != "1061" || it.TriagedAt != "1061" || it.Read {
		t.Fatalf("reopened event %+v", it)
	}
	kinds := chainKinds(it)
	if len(kinds) < 3 || kinds[0] != ChainReceived || kinds[1] != ChainReopened || !slices.Contains(kinds, ChainTriaged) {
		t.Fatalf("chain %v", kinds)
	}
	if got := it.Chain[1]; got.Via != "lark" || got.Text != "又有新消息，重新打开" {
		t.Fatalf("reopened entry %+v", got)
	}
}

// An explicit reopen:false is a closeout: the messages and version advance, but completion, words, triage and read state stay.
func TestInboxGroupCloseoutKeepsADoneEvent(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
		"events":[{"event":"lark:F1:900","title":"不应改掉的标题","summary":"不应改掉的总结","tier":"action","reason":"不应改掉的理由","urgent":true,"reopen":false}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Summary, it.Reason, it.TriagedBy = "示例原总结", "示例原理由", "model"
	doneAt := *it.DoneAt
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if it.open() || it.DoneAt == nil || !it.DoneAt.Equal(doneAt) || it.DoneBy != "user" ||
		it.Title != "示例上周的发布" || it.Summary != "示例原总结" || it.Tier != TierFYI || it.Reason != "示例原理由" ||
		it.TriagedBy != "model" || it.Urgent || !it.Read || it.Count != 3 || it.Last != "1061" || it.TriagedAt != "1061" ||
		it.Preview != "示例预览" || string(it.Meta) != `{"messages":["m900","m1060","m1061"]}` {
		t.Fatalf("closeout event %+v", it)
	}
	if kinds := chainKinds(it); len(kinds) != 1 || kinds[0] != ChainReceived {
		t.Fatalf("closeout chain %v", kinds)
	}
	if h.inbox.items["lark:F1:1060"] != nil {
		t.Fatal("a closeout opened another event")
	}
}

// A mute wins over the default reopen. The done item takes the closeout path directly, without a reopened-then-muted chain.
func TestInboxGroupMutedDoneEventStaysDone(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
		"events":[{"event":"lark:F1:900","title":"示例新标题","tier":"action","reason":"继续追问"}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read, it.Reason = 1, true, "示例原理由"
	h.inbox.mutes = []InboxMute{{ID: "m1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "示例群"}}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if it.open() || it.DoneBy != "user" || it.Title != "示例上周的发布" || it.Tier != TierFYI ||
		it.Reason != "示例原理由" || !it.Read || it.Count != 3 || it.TriagedAt != "1061" {
		t.Fatalf("muted event %+v", it)
	}
	if kinds := chainKinds(it); len(kinds) != 1 || kinds[0] != ChainReceived {
		t.Fatalf("muted chain %v", kinds)
	}
}

// The same title guard applies to an open event: grouping still retiers it but cannot replace words written by the
// handling conversation or the user, and their provenance stays unchanged.
func TestInboxGroupModelKeepsEstablishedTitle(t *testing.T) {
	for _, titleBy := range []string{TitleByAssistant, TitleByUser} {
		t.Run(titleBy, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			_, client := newGroupModel(t, groupAnswerOK)
			h, _ := groupHub(t, g, client)
			it := h.inbox.items["lark:F1:1051"]
			it.Tier, it.TitleBy, it.Summary = TierFYI, titleBy, "已经写过的总结"
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if it.Title != "示例 A 方案评审" || it.Summary != "已经写过的总结" || it.TitleBy != titleBy ||
				it.Tier != TierAction || it.Reason != "等你确认" || it.TriagedBy != "model" {
				t.Fatalf("open event %+v", it)
			}
		})
	}
}

// Titles and summaries written by the handling conversation or the user survive a reopen; the new triage still applies.
func TestInboxGroupReopenKeepsEstablishedTitle(t *testing.T) {
	for _, titleBy := range []string{TitleByAssistant, TitleByUser} {
		t.Run(titleBy, func(t *testing.T) {
			answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
				"events":[{"event":"lark:F1:900","title":"模型新标题","summary":"模型新总结","tier":"action","reason":"继续追问"}]}`
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			_, client := newGroupModel(t, answer)
			h, _ := groupHub(t, g, client)
			it := h.inbox.items["lark:F1:900"]
			it.Count, it.TitleBy, it.Summary = 1, titleBy, "已经写过的总结"
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if !it.open() || it.Title != "示例上周的发布" || it.Summary != "已经写过的总结" || it.TitleBy != titleBy ||
				it.Tier != TierAction || it.Reason != "继续追问" || !slices.Contains(chainKinds(it), ChainReopened) {
				t.Fatalf("reopened event %+v", it)
			}
		})
	}
}

// An established handling conversation receives a reopened event's messages even when the new tier is only FYI.
// putEvent marks it looking so the unchanged escalate handled branch, rather than the new-conversation tier gate, takes it.
func TestInboxGroupReopenReturnsToHandlingConversation(t *testing.T) {
	for _, tier := range []string{TierAction, TierFYI} {
		t.Run(tier, func(t *testing.T) {
			g := &groupLark{placed: map[string]bool{}}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:900"]
			it.Count = 1
			it.Agent = &InboxAgent{State: AgentSkipped, MachineID: "server", SessionID: "s1", Of: it.Last,
				Pending: []inboxNote{{Name: prompts.NoteClosed}}}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: "server", SessionID: "s1"}
			h.inbox.items = map[string]*InboxItem{it.ID: it}
			thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{Thread: "chat:F1", Event: it.ID, Meta: it.Meta, Messages: []string{"m1060"}})
			if err != nil {
				t.Fatal(err)
			}
			planned := plannedEvent{existing: it.ID, messages: []string{"m1060"}, title: "示例重开", summary: "示例新进展", tier: tier, reason: "继续"}
			if got := h.putEvent(sourceOf(g), "chat:F1", planned, thing, true); got != it {
				t.Fatalf("putEvent %p, want %p", got, it)
			}
			if len(it.Agent.Pending) != 0 {
				t.Fatalf("the old close note survived reopening: %+v", it.Agent.Pending)
			}
			now := time.Now()
			writes := h.inbox.escalate(now.Add(-time.Hour), now)
			if len(writes) != 1 || it.Agent.SessionID != "s1" || it.Agent.Of != "1060" || !it.Agent.Wake ||
				len(it.Agent.Pending) != 1 || it.Agent.Pending[0].Name != prompts.NoteNewMessages ||
				it.Handling.State != HandlingLooking {
				t.Fatalf("escalation %+v, agent %+v, handling %+v", writes, it.Agent, it.Handling)
			}
		})
	}
}

// If the model saw an open event but the user filed it before the append, the same event is appended and reopened;
// no new continuation event is created.
func TestInboxGroupAppendsToEventDoneMeanwhile(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	now := time.Now()
	it.DoneAt, it.DoneBy, it.Count = &now, "user", 1
	plan := &groupPlan{model: true, events: []plannedEvent{{existing: it.ID, messages: []string{"m1060"},
		title: "示例归档后继续", summary: "示例新进展", tier: TierFYI, reason: "补充"}}}
	h.writeGroup(context.Background(), sourceOf(g), *groupBatch(), plan)

	if len(g.appends) != 1 || g.appends[0].Event != it.ID || g.appends[0].Continues != "" {
		t.Fatalf("appends %+v", g.appends)
	}
	if h.inbox.items["lark:F1:1060"] != nil || !it.open() || it.DoneBy != "" || it.Title != "示例归档后继续" ||
		!slices.Contains(chainKinds(it), ChainReopened) {
		t.Fatalf("event done meanwhile %+v", it)
	}
}

// Done noise and done stream events are not reopen candidates. Open events of the same shapes remain visible.
func TestInboxGroupEventsSkipsDoneStreams(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	add := func(id string, done bool, tier string, count int) {
		it := &InboxItem{ID: id, UpdatedAt: now, Tier: tier, Count: count, Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}}
		if done {
			it.DoneAt = &now
		}
		h.inbox.items[id] = it
	}
	add("done-noise", true, TierNoise, 1)
	add("done-large", true, TierFYI, groupDoneCountMax)
	add("done-ok", true, TierFYI, groupDoneCountMax-1)
	add("open-noise", false, TierNoise, 1)
	add("open-large", false, TierFYI, groupDoneCountMax)
	got := map[string]bool{}
	for _, e := range h.groupEvents("lark", "chat:F1", "") {
		got[e.ID] = true
	}
	for _, id := range []string{"done-ok", "open-noise", "open-large"} {
		if !got[id] {
			t.Errorf("%s missing from %v", id, got)
		}
	}
	for _, id := range []string{"done-noise", "done-large"} {
		if got[id] {
			t.Errorf("%s should be excluded from %v", id, got)
		}
	}
}

// Noise folding leaves every plan entry aimed at a done event alone: it neither absorbs those messages nor redirects
// them to a new event, while unrelated new noise still folds into the conversation's open noise event.
func TestInboxGroupFoldNoiseSkipsDoneEvents(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	add := func(id string, done bool, tier string) {
		it := &InboxItem{ID: id, Tier: tier, Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}}
		if done {
			it.DoneAt = &now
		}
		h.inbox.items[id] = it
	}
	add("open-noise", false, TierNoise)
	add("done-noise", true, TierNoise)
	add("done-action", true, TierAction)
	add("done-noise-high", true, TierNoise)
	plan := &groupPlan{events: []plannedEvent{
		{existing: "done-noise", messages: []string{"m1"}, tier: TierNoise},
		{messages: []string{"m2"}, tier: TierNoise},
		{existing: "done-action", messages: []string{"m3"}, tier: TierNoise},
		{existing: "done-noise-high", messages: []string{"m4"}, tier: TierAction},
	}}
	h.inbox.foldNoise("lark", "chat:F1", plan)

	if len(plan.events) != 4 {
		t.Fatalf("folded plan %+v", plan.events)
	}
	for i, id := range []string{"done-noise", "open-noise", "done-action", "done-noise-high"} {
		if plan.events[i].existing != id || len(plan.events[i].messages) != 1 || plan.events[i].messages[0] != fmt.Sprintf("m%d", i+1) {
			t.Errorf("plan %d %+v, want only %s's own message", i, plan.events[i], id)
		}
	}
}

// A connector that keeps no meta reopens a done event the same way: the hub composes the append from the batch, the
// event takes the model's words, clears completion and records received then reopened; no continuation is opened.
func TestInboxGroupReopensDoneEventWithoutMeta(t *testing.T) {
	answer := `{"assign":[{"message":"m2","event":"examplechat:chat:C1#m1"},{"message":"m3","event":"examplechat:chat:C1#m1"}],
		"events":[{"event":"examplechat:chat:C1#m1","title":"示例 A 方案回到评审","summary":"丙说周四下午可以","tier":"action","reason":"等你定"}]}`
	g := &groupChat{batch: chatBatch()}
	_, client := newGroupModel(t, answer)
	h := chatHub(t, g, client)
	it := h.inbox.items["examplechat:chat:C1#m1"]
	done := time.Now().Add(-2 * time.Hour)
	it.DoneAt, it.DoneBy, it.Count, it.Tier = &done, "user", 1, TierFYI
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if !it.open() || it.DoneBy != "" || it.Title != "示例 A 方案回到评审" || it.Summary != "丙说周四下午可以" ||
		it.Tier != TierAction || it.Reason != "等你定" || it.TriagedBy != "model" || it.TitleBy != TitleByModel ||
		it.Count != 3 || it.Meta != nil || !strings.Contains(it.Context, "m2") || !strings.Contains(it.Context, "m3") {
		t.Fatalf("reopened event %+v", it)
	}
	if kinds := chainKinds(it); len(kinds) != 3 || kinds[0] != ChainReceived || kinds[1] != ChainReopened || kinds[2] != ChainTriaged {
		t.Fatalf("chain %v", kinds)
	}
	if len(h.inbox.items) != 1 || h.inbox.items["examplechat:chat:C1#m3"] != nil {
		t.Fatalf("a continuation was opened instead: %d items", len(h.inbox.items))
	}
}

// reopen has no effect on an open event: the model's words apply as before and no reopened entry is recorded.
func TestInboxGroupReopenFalseOnAnOpenEvent(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:1051"},{"message":"m1061","event":"new:1"}],
		"events":[{"event":"lark:F1:1051","title":"示例 A 方案周四下午","summary":"丙说周四下午可以","tier":"fyi","reason":"同步","reopen":false},
		{"event":"new:1","title":"示例 B 接口变更","summary":"丁说接口改了","tier":"fyi","reason":"同步"}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	it := h.inbox.items["lark:F1:1051"]
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if !it.open() || it.Title != "示例 A 方案周四下午" || it.Tier != TierFYI || it.Reason != "同步" || it.TriagedBy != "model" {
		t.Fatalf("open event %+v", it)
	}
	if kinds := chainKinds(it); slices.Contains(kinds, ChainReopened) {
		t.Fatalf("an open event was reopened: %v", kinds)
	}
}

// The model saw an open event the user filed before its append, and the fallback wrote it (the model was unavailable
// between the two steps): the fallback keeps its structural title and rule tier as it does for open events, and the
// event reopens rather than gaining a continuation.
func TestInboxGroupFallbackReopensAnEventDoneMeanwhile(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	now := time.Now()
	it.DoneAt, it.DoneBy, it.Count = &now, "user", 1
	plan := &groupPlan{events: []plannedEvent{{existing: it.ID, messages: []string{"m1060"}}}}
	h.writeGroup(context.Background(), sourceOf(g), *groupBatch(), plan)

	if len(g.appends) != 1 || g.appends[0].Event != it.ID || g.appends[0].Continues != "" {
		t.Fatalf("appends %+v", g.appends)
	}
	if !it.open() || it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierAction ||
		it.TriagedBy != "model" || !slices.Contains(chainKinds(it), ChainReopened) {
		t.Fatalf("fallback race %+v", it)
	}
}

// A batch written twice (the connector hands it again) reopens the event once: the second write is the same last.
func TestInboxGroupReopenOnce(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count = 1
	plan := &groupPlan{model: true, events: []plannedEvent{{existing: it.ID, messages: []string{"m1060"},
		title: "示例重开", summary: "示例进展", tier: TierAction, reason: "继续"}}}
	h.writeGroup(context.Background(), sourceOf(g), *groupBatch(), plan)
	h.writeGroup(context.Background(), sourceOf(g), *groupBatch(), plan)
	n := 0
	for _, k := range chainKinds(it) {
		if k == ChainReopened {
			n++
		}
	}
	if n != 1 || it.Count != 2 {
		t.Fatalf("%d reopened entries, count %d, chain %v", n, it.Count, chainKinds(it))
	}
}

// A close note still queued when the event reopens is dropped with it: the conversation's next delivery carries the
// new messages alone, never a stop followed by them.
func TestInboxGroupReopenDropsTheClosedNote(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}}
	h, _ := groupHub(t, g, nil)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Context = 1, "示例群：m900"
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Of: it.Last,
		Pending: []inboxNote{{Name: prompts.NoteClosed, Text: "user"}}}
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: "server", SessionID: "s1"}
	thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{Thread: "chat:F1", Event: it.ID, Meta: it.Meta,
		Messages: []string{"m1060", "m1061"}})
	if err != nil {
		t.Fatal(err)
	}
	planned := plannedEvent{existing: it.ID, messages: []string{"m1060", "m1061"}, title: "示例发布跟进",
		summary: "对方补充了时间", tier: TierAction, reason: "继续追问"}
	// The batch the connector hands over is the one groupBatch's messages make: the append's answer carries its last.
	thing.Last = "1061"
	if got := h.putEvent(sourceOf(g), "chat:F1", planned, thing, true); got != it {
		t.Fatalf("putEvent %p, want %p", got, it)
	}
	h.inbox.mu.Lock()
	for _, n := range it.Agent.Pending {
		if n.Name == prompts.NoteClosed {
			h.inbox.mu.Unlock()
			t.Fatalf("the stop note is still queued: %+v", it.Agent.Pending)
		}
	}
	h.inbox.mu.Unlock()
	now := time.Now()
	if writes := h.inbox.escalate(now.Add(-time.Hour), now); len(writes) != 1 {
		t.Fatalf("escalated %d", len(writes))
	}
	h.deliverPending(context.Background())
	sent := notesOf(h.f, "s1")
	if len(sent) != 1 || !strings.Contains(sent[0], "有了新消息") {
		t.Fatalf("the conversation was told %v", sent)
	}
	for _, txt := range sent {
		if strings.Contains(txt, "不用再做") || strings.Contains(txt, "停下") {
			t.Fatalf("the filed event's stop note was sent after the reopen:\n%s", txt)
		}
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if !it.open() || it.Handling.State != HandlingLooking {
		t.Fatalf("reopened event %+v, handling %+v", it, it.Handling)
	}
}

// With the AI twin off, or a policy that keeps the item the user's, a reopened event goes back to the open list as
// any item would, and no processing conversation is woken: the twin's release would otherwise say it failed.
func TestInboxGroupReopenLeavesTheTwinAlone(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
		"events":[{"event":"lark:F1:900","title":"示例发布跟进","summary":"对方补充了时间","tier":"action","reason":"继续追问"}]}`
	for _, c := range []struct {
		name   string
		enable bool
		twin   string
	}{
		{name: "twin off"},
		{name: "policy never", enable: true, twin: TwinNever},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			_, client := newGroupModel(t, answer)
			h, _ := groupHub(t, g, client)
			h.setConfig(func(cfg *assistantwire.InboxConfig) { cfg.Agent.Enabled = c.enable })
			it := h.inbox.items["lark:F1:900"]
			it.Count, it.Context = 1, "示例群：m900"
			it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Of: it.Last}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: "server", SessionID: "s1"}
			if c.twin != "" {
				h.inbox.mu.Lock()
				h.inbox.policies = []InboxPolicy{{ID: "p1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "示例群", Twin: c.twin}}
				h.inbox.mu.Unlock()
			}
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if !it.open() || it.DoneBy != "" || it.Tier != TierAction || !slices.Contains(chainKinds(it), ChainReopened) {
				t.Fatalf("reopened event %+v", it)
			}
			if it.Handling.State == HandlingLooking || it.status() != StatusAction {
				t.Fatalf("the twin was woken on a reopened event: handling %+v, status %q", it.Handling, it.status())
			}
			h.inbox.mu.Lock()
			for _, n := range it.Agent.Pending {
				if n.Name == prompts.NoteNewMessages {
					h.inbox.mu.Unlock()
					t.Fatalf("new messages queued for a conversation the twin is not running: %+v", it.Agent.Pending)
				}
			}
			h.inbox.mu.Unlock()
			if got := notesOf(h.f, "s1"); len(got) != 0 {
				t.Fatalf("the conversation was told %v", got)
			}
			h.handPass(context.Background()) // the twin's pass: nothing it did on the item is released
			h.inbox.mu.Lock()
			defer h.inbox.mu.Unlock()
			if it.Handling.State == HandlingLooking || it.Handling.State == HandlingFailed || !it.open() ||
				it.status() != StatusAction || it.Tier != TierAction {
				t.Fatalf("after the twin's pass: event %+v, handling %+v, status %q", it, it.Handling, it.status())
			}
			if got := notesOf(h.f, "s1"); len(got) != 0 {
				t.Fatalf("the twin's pass told the conversation %v", got)
			}
		})
	}
}

// The same reopen with the twin on: it goes back to the conversation, which the new messages wake.
func TestInboxGroupReopenWakesTheTwin(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
		"events":[{"event":"lark:F1:900","title":"示例发布跟进","summary":"对方补充了时间","tier":"fyi","reason":"同步"}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	h.setConfig(func(cfg *assistantwire.InboxConfig) { cfg.Agent.Enabled = true })
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Context = 1, "示例群：m900"
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Of: it.Last}
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: "server", SessionID: "s1"}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if !it.open() || it.Handling.State != HandlingLooking || it.Agent.SessionID != "s1" {
			t.Fatalf("reopened event %+v, handling %+v", it, it.Handling)
		}
	}()
	waitFor(t, "the new messages told to the conversation", func() bool {
		return strings.Contains(strings.Join(notesOf(h.f, "s1"), "\n"), "有了新消息")
	})
}

// A-8: a fallback-new entry joins the latest direct-chat candidate inside the two-hour activity window. The existing
// event keeps its words and triager, while a higher rule tier raises its tier and replaces the reason with the rule's.
func TestInboxGroupDMFallbackNewJoinsLatestOpen(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction, Title: "示例结构标题"}},
	})
	it.Tier, it.Reason = TierFYI, "示例原理由"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the fallback opened another event: %v", h.inbox.items)
	}
	if it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierAction || it.Reason != "按规则" ||
		it.TriagedBy != "model" || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last ||
		string(it.Meta) != `{"messages":["m1051","m1060","m1061"]}` {
		t.Fatalf("joined event %+v", it)
	}
	kinds := chainKinds(it)
	if len(kinds) == 0 || kinds[0] != ChainReceived || slices.Contains(kinds, ChainReopened) {
		t.Fatalf("chain %v", kinds)
	}
}

// The latest candidate by activity is done even though the supplied candidate list puts open events first. A direct
// candidate kind is enough when an older batch has no kind. The join uses the ordinary default reopen path.
func TestInboxGroupDMFallbackNewReopensLatestDone(t *testing.T) {
	g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	g.batch.Kind = ""
	now := time.Now()
	open.UpdatedAt = now.Add(-time.Hour)
	done.UpdatedAt = now.Add(-30 * time.Minute)
	done.Summary, done.Reason, done.TriagedBy, done.Read = "示例原总结", "示例原理由", "model", true
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 1 || len(g.fallbacks[0].Events) != 2 ||
		g.fallbacks[0].Events[0].ID != open.ID || g.fallbacks[0].Events[0].Done ||
		g.fallbacks[0].Events[1].ID != done.ID || !g.fallbacks[0].Events[1].Done {
		t.Fatalf("fallback candidates %+v", g.fallbacks)
	}
	if len(g.appends) != 1 || g.appends[0].Event != done.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the fallback opened another event: %v", h.inbox.items)
	}
	if !done.open() || done.DoneBy != "" || done.Read || done.Title != "示例上周的发布" || done.Summary != "示例原总结" ||
		done.Tier != TierAction || done.Reason != "按规则" || done.TriagedBy != "model" || done.Count != 3 ||
		done.Last != "1061" || string(done.Meta) != `{"messages":["m900","m1060","m1061"]}` {
		t.Fatalf("reopened event %+v", done)
	}
	kinds := chainKinds(done)
	if len(kinds) < 2 || kinds[0] != ChainReceived || kinds[1] != ChainReopened {
		t.Fatalf("chain %v", kinds)
	}
}

// Recent candidates outside the activity window do not absorb a fallback-new entry.
func TestInboxGroupDMFallbackNewOutsideWindowOpensEvent(t *testing.T) {
	g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction, Title: "示例独立事件"}},
	})
	now := time.Now()
	open.UpdatedAt = now.Add(-3 * time.Hour)
	done.UpdatedAt = now.Add(-4 * time.Hour)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != "" {
		t.Fatalf("appends %+v", g.appends)
	}
	fresh := h.inbox.items["lark:F1:1060"]
	if fresh == nil || fresh.Title != "示例独立事件" || fresh.Tier != TierAction || fresh.TriagedBy != "rules" || fresh.Count != 2 {
		t.Fatalf("new event %+v", fresh)
	}
	if len(h.inbox.items) != 3 || open.Count != 1 || open.Last != "1051" || done.Count != 1 || done.Last != "900" {
		t.Fatalf("items %v", h.inbox.items)
	}
}

// Group and bot conversations retain the old behavior: fallback-new entries open their own event.
func TestInboxGroupFallbackNewOnlyJoinsDirectChats(t *testing.T) {
	for _, kind := range []string{KindGroup, KindBot} {
		t.Run(kind, func(t *testing.T) {
			g, h, open, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
				New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction, Title: "示例独立事件"}},
			})
			setGroupConversationKind(g, h, kind)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if len(g.appends) != 1 || g.appends[0].Event != "" {
				t.Fatalf("appends %+v", g.appends)
			}
			fresh := h.inbox.items["lark:F1:1060"]
			if fresh == nil || fresh.Kind != kind || fresh.Title != "示例独立事件" || fresh.Count != 2 {
				t.Fatalf("new event %+v", fresh)
			}
			if len(h.inbox.items) != 3 || open.Count != 1 || open.Last != "1051" {
				t.Fatalf("items %v", h.inbox.items)
			}
		})
	}
}

// Several fallback-new entries join the same target in fallback plan order, whether written together or in sequence.
func TestInboxGroupDMFallbackNewEntriesKeepPlanOrder(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{New: []connector.GroupNew{
		{Key: "k1", Messages: []string{"m1060"}, RuleTier: connector.TierFYI, Title: "示例结构事件一"},
		{Key: "k2", Messages: []string{"m1061"}, RuleTier: connector.TierAction, Title: "示例结构事件二"},
	}})
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	var messages []string
	for _, a := range g.appends {
		if a.Event != it.ID {
			t.Fatalf("append to another event: %+v", a)
		}
		messages = append(messages, a.Messages...)
	}
	if !slices.Equal(messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appended messages %v", messages)
	}
	if len(h.inbox.items) != 2 || it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" ||
		it.Count != 3 || it.Last != "1061" || string(it.Meta) != `{"messages":["m1051","m1060","m1061"]}` {
		t.Fatalf("joined event %+v, items %v", it, h.inbox.items)
	}
}

// A matching mute keeps a joined done event done and adds no reopened entry.
func TestInboxGroupDMFallbackNewMutedDoneStaysDone(t *testing.T) {
	g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	now := time.Now()
	open.UpdatedAt = now.Add(-time.Hour)
	done.UpdatedAt = now.Add(-30 * time.Minute)
	done.Summary, done.Read = "示例原总结", true
	h.inbox.mutes = []InboxMute{{ID: "m1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "示例对话"}}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != done.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if done.open() || done.DoneBy != "user" || !done.Read || done.Title != "示例上周的发布" || done.Summary != "示例原总结" ||
		done.Tier != TierAction || done.Reason != "按规则" || done.Count != 3 || done.Last != "1061" ||
		string(done.Meta) != `{"messages":["m900","m1060","m1061"]}` {
		t.Fatalf("muted event %+v", done)
	}
	kinds := chainKinds(done)
	if !slices.Contains(kinds, ChainReceived) || slices.Contains(kinds, ChainReopened) {
		t.Fatalf("chain %v", kinds)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the muted join opened another event: %v", h.inbox.items)
	}
}

// Reopening a done target does not mark its handling as looking when the twin is disabled.
func TestInboxGroupDMFallbackNewReopenLeavesTwinDisabled(t *testing.T) {
	_, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	now := time.Now()
	open.UpdatedAt = now.Add(-time.Hour)
	done.UpdatedAt = now.Add(-30 * time.Minute)
	h.setConfig(func(cfg *assistantwire.InboxConfig) { cfg.Agent.Enabled = false })
	done.Context = "示例对话：m900"
	done.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Of: done.Last}
	done.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: "server", SessionID: "s1"}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if !done.open() || done.DoneBy != "" || !slices.Contains(chainKinds(done), ChainReopened) {
		t.Fatalf("reopened event %+v", done)
	}
	if done.Handling.State == HandlingLooking {
		t.Fatalf("the disabled twin was woken: %+v", done.Handling)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	for _, n := range done.Agent.Pending {
		if n.Name == prompts.NoteNewMessages {
			t.Fatalf("new messages queued for the disabled twin: %+v", done.Agent.Pending)
		}
	}
}

// Fallback validation accepts a direct assignment to any supplied done candidate, independently of the direct-chat
// fallback-new rewrite. Its messages follow the same default reopen path and monotonic fallback tier floor.
func TestInboxGroupFallbackAssignsDoneEvent(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierAction, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:900"}, {Message: "m1061", Event: "lark:F1:900"}}}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Summary, it.Reason, it.TriagedBy = "示例原总结", "示例原理由", "model"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 1 || len(g.fallbacks[0].Events) != 2 ||
		g.fallbacks[0].Events[0].Done || !g.fallbacks[0].Events[1].Done || g.fallbacks[0].Events[1].ID != it.ID {
		t.Fatalf("fallback candidates %+v", g.fallbacks)
	}
	if len(g.appends) != 1 || g.appends[0].Event != it.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if !it.open() || it.DoneBy != "" || it.Read || it.Title != "示例上周的发布" || it.Summary != "示例原总结" ||
		it.Tier != TierAction || it.Reason != "按规则" || it.TriagedBy != "model" || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("reopened event %+v", it)
	}
	kinds := chainKinds(it)
	if len(kinds) != 3 || kinds[0] != ChainReceived || kinds[1] != ChainReopened || kinds[2] != ChainTriaged {
		t.Fatalf("chain %v", kinds)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the assignment opened another event: %v", h.inbox.items)
	}
}

// Rewriting never lowers an existing tier. Noise is the lowest tier, so even an action rule leaves a noise target alone;
// when the tier does not rise, the original reason, triager and urgency all survive.
func TestInboxGroupDMFallbackNewNeverDowngradesTier(t *testing.T) {
	for _, tc := range []struct {
		name, existing, rule string
		urgent               bool
	}{
		{name: "higher existing tier", existing: TierAction, rule: TierFYI, urgent: true},
		{name: "existing noise", existing: TierNoise, rule: TierAction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
				New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.Tier(tc.rule)}},
			})
			it.Tier, it.Reason, it.Urgent = tc.existing, "示例原理由", tc.urgent
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if len(g.appends) != 1 || g.appends[0].Event != it.ID {
				t.Fatalf("appends %+v", g.appends)
			}
			if it.Tier != tc.existing || it.Reason != "示例原理由" || it.TriagedBy != "model" || it.Urgent != tc.urgent ||
				it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Count != 3 || it.Last != "1061" {
				t.Fatalf("joined event %+v", it)
			}
			if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
				t.Fatalf("the rewrite opened another event: %v", h.inbox.items)
			}
		})
	}
}

// A rewritten join still leaves an event's tier to the processing conversation already handling it. The messages join
// that event, but the rule floor does not retriage it or replace its reason.
func TestInboxGroupDMFallbackNewHandledKeepsTier(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	it.Tier, it.Reason = TierFYI, "示例原理由"
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Tier != TierFYI || it.Reason != "示例原理由" || it.TriagedBy != "model" ||
		it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("handled event %+v", it)
	}
	if kinds := chainKinds(it); slices.Contains(kinds, ChainTriaged) {
		t.Fatalf("the rule floor retriaged a handled event: %v", kinds)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the rewrite opened another event: %v", h.inbox.items)
	}
}

// The events tool's quotas never hide an open event: ten open events stay ten when many done ones are eligible too, and
// the done side fills its own six slots from the newest eligible ones, not from slots the open side left over.
func TestInboxGroupEventsOpenQuotaSurvivesManyDone(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	for i := range 10 {
		addGroupCandidate(h, fmt.Sprintf("open-%02d", i), "示例", now.Add(-time.Duration(i)*time.Minute), false)
	}
	for i := range 12 {
		at := now.Add(-24*time.Hour - time.Duration(i)*time.Minute)
		addGroupCandidate(h, fmt.Sprintf("done-%02d", i), "示例", at, true)
	}
	got := h.groupEvents("lark", "chat:F1", "")
	want := make([]string, 0, groupOpenMax+groupDoneMax)
	for i := range groupOpenMax {
		want = append(want, fmt.Sprintf("open-%02d", i))
	}
	for i := range groupDoneMax {
		want = append(want, fmt.Sprintf("done-%02d", i))
	}
	if ids := groupCandidateIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("candidates %v, want %v", ids, want)
	}
}

// The prompt's numbers come from the constants: changing the constant changes the rendered prompt, and rendering zero
// values (as a caller that forgets them would) shows the numbers unset rather than silently keeping the old sentence.
func TestInboxGroupPromptNumbersBindToConstants(t *testing.T) {
	text := prompts.RenderInboxGroup(prompts.InboxGroup{MaxCalls: groupCalls, NewMax: groupNewMax,
		OpenMax: groupOpenMax, DoneMax: groupDoneMax, DoneDays: int(groupDoneFor / (24 * time.Hour))})
	want := fmt.Sprintf("还开着的事（最多 %d 件）和 %d 天内处理过的事（最多 %d 件）", groupOpenMax, int(groupDoneFor/(24*time.Hour)), groupDoneMax)
	if !strings.Contains(text, want) {
		t.Fatalf("prompt lacks %q:\n%s", want, text)
	}
	if strings.Contains(text, "最多 10 件（id") {
		t.Errorf("the old hard-coded sentence is still in the template:\n%s", text)
	}
}
