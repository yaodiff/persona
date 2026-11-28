package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	_ "github.com/nwverse/persona/server/internal/modules/all" // the real lark module behind botport.SyncService
)

// larkSyncExtStore combines extensionStore (extension records, secrets, settings) with larkSyncMemStore (cursors,
// dedup index, outbox) so real extension processes can receive events emitted by the real module's sync service.
type larkSyncExtStore struct {
	*extensionStore
	sync *larkSyncMemStore
}

var _ larksyncwire.Store = (*larkSyncExtStore)(nil)

func (s *larkSyncExtStore) LarkSyncCursors(ctx context.Context, binding string) (map[string]string, error) {
	return s.sync.LarkSyncCursors(ctx, binding)
}

func (s *larkSyncExtStore) DeleteLarkSyncCursors(ctx context.Context, binding string) error {
	return s.sync.DeleteLarkSyncCursors(ctx, binding)
}

func (s *larkSyncExtStore) LarkSyncIndex(ctx context.Context, keys []string) (map[string]larksyncwire.IndexRecord, error) {
	return s.sync.LarkSyncIndex(ctx, keys)
}

func (s *larkSyncExtStore) CommitLarkSyncRun(ctx context.Context, binding string, cursors map[string]string, recs []larksyncwire.IndexRecord, outbox []db.LarkSyncOutbox) error {
	return s.sync.CommitLarkSyncRun(ctx, binding, cursors, recs, outbox)
}

func (s *larkSyncExtStore) LarkSyncOutboxAfter(ctx context.Context, kind larksyncwire.OutboxKind, after int64, limit int) ([]db.LarkSyncOutbox, error) {
	return s.sync.LarkSyncOutboxAfter(ctx, kind, after, limit)
}

func (s *larkSyncExtStore) LarkSyncDocsSince(ctx context.Context, since time.Time) ([]db.LarkSyncOutbox, error) {
	return s.sync.LarkSyncDocsSince(ctx, since)
}

func (s *larkSyncExtStore) PruneLarkSyncOutbox(ctx context.Context, before time.Time) (int64, error) {
	return s.sync.PruneLarkSyncOutbox(ctx, before)
}

// larkEventOpen is a scriptable Open Platform for the real larksync.Syncer. The extmgr tests reach the module through
// Manager.routes and botport.SyncService; no removed extmgr run helpers or direct module imports are involved.
type larkEventOpen struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	messages map[string][]larksyncwire.Message
	chats    map[string]larksyncwire.Message
	names    map[string]string
}

func newLarkEventOpen(t *testing.T) *larkEventOpen {
	t.Helper()
	f := &larkEventOpen{t: t, messages: map[string][]larksyncwire.Message{}, chats: map[string]larksyncwire.Message{}, names: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *larkEventOpen) setMessages(msgs []larksyncwire.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = map[string][]larksyncwire.Message{}
	f.chats = map[string]larksyncwire.Message{}
	for _, m := range msgs {
		f.messages[m.ChatID] = append(f.messages[m.ChatID], m)
		if _, ok := f.chats[m.ChatID]; !ok {
			f.chats[m.ChatID] = m
		}
	}
}

func (f *larkEventOpen) setNames(names map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = map[string]string{}
	for id, name := range names {
		f.names[id] = name
	}
}

func larkEventData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "success", "data": data})
}

func (f *larkEventOpen) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.Header.Get("User-Agent") == "persona-ports" {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p := r.URL.Path; {
	case p == "/open-apis/authen/v2/oauth/token":
		larkEventData(w, map[string]any{
			"access_token": "access-refreshed", "refresh_token": "refresh-example",
			"expires_in": 7200, "refresh_token_expires_in": 2592000, "token_type": "Bearer",
		})
	case p == "/open-apis/authen/v1/user_info":
		larkEventData(w, map[string]any{"open_id": "ou_owner_example", "tenant_key": "tenant_example", "name": "Owner"})
	case p == "/open-apis/im/v1/chats":
		ids := make([]string, 0, len(f.chats))
		for id := range f.chats {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		items := make([]any, 0, len(ids))
		for _, id := range ids {
			m := f.chats[id]
			kind := m.ChatType
			if kind == "" {
				kind = "group"
			}
			items = append(items, map[string]any{"chat_id": id, "name": m.ChatName, "chat_mode": kind})
		}
		larkEventData(w, map[string]any{"items": items})
	case p == "/open-apis/im/v1/messages":
		msgs := append([]larksyncwire.Message(nil), f.messages[r.URL.Query().Get("container_id")]...)
		sort.SliceStable(msgs, func(i, j int) bool {
			if msgs[i].At.Equal(msgs[j].At) {
				return msgs[i].ID < msgs[j].ID
			}
			return msgs[i].At.Before(msgs[j].At)
		})
		items := make([]any, 0, len(msgs))
		for _, m := range msgs {
			content := map[string]any{"text": m.Text}
			if len(m.Files) > 0 {
				content["file_key"] = m.Files[0].Ref
				content["file_name"] = m.Files[0].Name
			}
			raw, _ := json.Marshal(content)
			items = append(items, map[string]any{
				"message_id":  m.ID,
				"chat_id":     m.ChatID,
				"msg_type":    m.Type,
				"create_time": fmt.Sprint(m.At.UnixMilli()),
				"sender":      map[string]string{"id": m.SenderID, "sender_type": "user", "name": f.names[m.SenderID]},
				"body":        map[string]string{"content": string(raw)},
			})
		}
		larkEventData(w, map[string]any{"items": items})
	case p == "/open-apis/search/v2/message":
		larkEventData(w, map[string]any{"items": []string{}})
	case p == "/open-apis/drive/v1/files":
		larkEventData(w, map[string]any{"files": []any{}})
	case p == "/open-apis/wiki/v2/spaces":
		larkEventData(w, map[string]any{"items": []any{}})
	case strings.HasPrefix(p, "/open-apis/contact/v3/users/"):
		id := strings.TrimPrefix(p, "/open-apis/contact/v3/users/")
		larkEventData(w, map[string]any{"user": map[string]any{"name": f.names[id]}})
	case strings.HasPrefix(p, "/open-apis/im/v1/chats/") && strings.HasSuffix(p, "/members"):
		larkEventData(w, map[string]any{"items": []any{}})
	case strings.HasPrefix(p, "/open-apis/im/v1/chats/"):
		id := strings.TrimPrefix(p, "/open-apis/im/v1/chats/")
		m := f.chats[id]
		larkEventData(w, map[string]any{"name": m.ChatName})
	default:
		f.t.Errorf("unexpected Open Platform call %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

// seedLarkEventBinding writes the public settings contract consumed by the module. Backfill versions are deliberately
// ahead of the module's current versions so only the explicit backfill case below runs that repair pass.
func seedLarkEventBinding(t *testing.T, h *Manager, key, botID, accountID string, now time.Time) {
	t.Helper()
	access, err := h.sealKey("access-example")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := h.sealKey("refresh-example")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := h.store.PutSetting(ctx, larksyncwire.SettingAccounts, []map[string]any{{
		"account_id": accountID, "label": "Work", "brand": "feishu", "tenant_key": "tenant_example",
		"open_id": "ou_owner_example", "name": "Owner",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutSetting(ctx, larksyncwire.SettingBindings, []map[string]any{{
		"key": key, "bot_id": botID, "account_id": accountID, "tier": larksyncwire.TierFull, "enabled": true,
		"authorized_at": now.Add(-time.Hour), "token_expires_at": time.Now().Add(2 * time.Hour),
		"refresh_expires_at": time.Now().Add(30 * 24 * time.Hour), "access_token_enc": access, "refresh_token_enc": refresh,
		"granted_scopes": larksyncwire.ScopesFor(larksyncwire.TierFull), "health": larksyncwire.HealthOK,
		"backfill_version": 1 << 20, "links_backfill_version": 1 << 20,
	}}); err != nil {
		t.Fatal(err)
	}
}

func updateLarkEventBinding(t *testing.T, h *Manager, key string, update func(map[string]any)) {
	t.Helper()
	ctx := context.Background()
	var rows []map[string]any
	ok, err := h.store.Setting(ctx, larksyncwire.SettingBindings, &rows)
	if err != nil || !ok {
		t.Fatalf("read bindings: ok=%v err=%v", ok, err)
	}
	found := false
	for _, row := range rows {
		if row["key"] == key {
			update(row)
			found = true
		}
	}
	if !found {
		t.Fatalf("binding %s not found", key)
	}
	if err := h.store.PutSetting(ctx, larksyncwire.SettingBindings, rows); err != nil {
		t.Fatal(err)
	}
}

// runLarkEventSync drives the module through its real manager route and waits for the asynchronous run. The route
// reports run failures through binding health, so the helper turns a non-OK terminal health into an error.
func runLarkEventSync(t *testing.T, mux http.Handler, key string) error {
	t.Helper()
	if code, out := doSyncReq(t, mux, http.MethodPost, "/api/v1/bots/feishu/sync/bindings/"+key+"/run", ""); code != http.StatusOK {
		return fmt.Errorf("start run: %d %v", code, out)
	}
	var finalErr error
	var final map[string]any
	waitLong(t, "sync run "+key+" finished", func() bool {
		code, view := doSyncReq(t, mux, http.MethodGet, "/api/v1/bots/feishu/sync", "")
		if code != http.StatusOK {
			finalErr = fmt.Errorf("read run view: %d %v", code, view)
			return true
		}
		bindings, _ := view["bindings"].([]any)
		for _, raw := range bindings {
			b, _ := raw.(map[string]any)
			if b["key"] != key {
				continue
			}
			if running, _ := b["running"].(bool); running {
				return false
			}
			final = b
			return true
		}
		return false
	})
	if finalErr != nil {
		return finalErr
	}
	if final == nil {
		t.Fatalf("binding %s disappeared before run finished", key)
	}
	health, _ := final["health"].(string)
	if health != larksyncwire.HealthOK {
		return fmt.Errorf("sync health %s: %v", health, final["error"])
	}
	return nil
}

// lark.messages reaches a running extension only when it subscribed to the topic AND was explicitly granted the
// named-only lark.messages.read scope. kb.read, no permissions, and legacy hub_api are not enough. Message bodies,
// chat names, sender names and attachments are never carried in the event payload.
func TestLarkMessagesEventsReachOnlyAuthorizedExtensions(t *testing.T) {
	needsLarkModule(t)
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		permissions string
		subscribe   []string
		allowed     bool
	}{
		{"lark-reader", `{"hub":["lark.messages.read"]}`, []string{"lark.messages", "agent.finished"}, true},
		{"kb-reader", `{"hub":["kb.read"]}`, []string{"lark.messages", "agent.finished"}, false},
		{"none", `{}`, []string{"lark.messages", "agent.finished"}, false},
		{"legacy-hub-api", `{"hub_api":true}`, []string{"lark.messages", "agent.finished"}, false},
		{"authorized-not-subscribed", `{"hub":["lark.messages.read"]}`, []string{"agent.finished"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestManager(t)
			extStore := newExtensionStore()
			syncStore := newLarkSyncMemStore(func() time.Time { return now })
			h.store = &larkSyncExtStore{extensionStore: extStore, sync: syncStore}
			open := newLarkEventOpen(t)
			h.cfg.Bots.Feishu = FeishuBotConfig{AppID: "cli_example", AppSecret: "example-secret", Domain: open.srv.URL}
			startTestExtensions(t, h)

			bindingKey := larksyncwire.PrimaryBot + "~feishu:tenant_example:ou_owner_example"
			accountID := "feishu:tenant_example:ou_owner_example"
			seedLarkEventBinding(t, h, bindingKey, larksyncwire.PrimaryBot, accountID, now)

			mux := h.routes()
			syncMux := http.NewServeMux()
			h.botRoutes(syncMux)
			token := h.cfg.Token
			dir := uiPackage(t)
			path := filepath.Join(dir, extension.ManifestFile)
			raw, _ := os.ReadFile(path)
			var manifest map[string]any
			_ = json.Unmarshal(raw, &manifest)
			var permissions map[string]any
			_ = json.Unmarshal([]byte(tc.permissions), &permissions)
			permissions["launcher_keys"] = true
			manifest["permissions"] = permissions
			manifest["contributes"].(map[string]any)["events"] = []any{map[string]any{
				"id":        "lark",
				"subscribe": tc.subscribe,
			}}
			raw, _ = json.Marshal(manifest)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", token, `{"source":"dir:`+dir+`"}`)
			if code != 200 {
				t.Fatalf("preview %d %v", code, pv)
			}
			code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", token, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"]))
			if code != 200 {
				t.Fatalf("install %d %v", code, out)
			}
			waitLong(t, "lark subscriber running", func() bool {
				p := h.extensions.get("example-ui")
				return p != nil && p.proc != nil && p.proc.Ready()
			})

			t1 := now.Add(-2 * time.Minute)
			t2 := now.Add(-1 * time.Minute)
			open.setNames(map[string]string{"ou_sender_1": "Secret Person One", "ou_sender_2": "Secret Person Two"})
			open.setMessages([]larksyncwire.Message{
				{
					ID: "om_example_2", ChatID: "oc_example_chat_2", ChatName: "Private Project Group", ChatType: "group",
					SenderID: "ou_sender_2", SenderName: "Secret Person Two", Type: "interactive", Text: "Secret meeting body two", At: t2,
					Files: []larksyncwire.Attachment{{Ref: "ref_secret", Name: "notes.pdf", Kind: "file"}},
				},
				{
					ID: "om_example_1", ChatID: "oc_example_chat_1", ChatName: "Private Direct Chat", ChatType: "p2p",
					SenderID: "ou_sender_1", SenderName: "Secret Person One", Type: "text", Text: "Secret message body one", At: t1,
				},
			})

			if err := runLarkEventSync(t, syncMux, bindingKey); err != nil {
				t.Fatalf("lark sync run: %v", err)
			}
			waitEventQueuesEmpty(t, h)

			view := func() string {
				_, out := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/home", token, `{}`)
				return fmt.Sprint(out["view"])
			}

			// Emit agent.finished as an in-order sentinel on the same extension stdio stream.
			h.extensions.emit("agent.finished", map[string]any{})
			if !tc.allowed {
				waitLong(t, "sentinel event delivered", func() bool { return strings.Contains(view(), "value:1") })
				if v := view(); strings.Contains(v, "lark.messages") || strings.Contains(v, "om_example_1") || strings.Contains(v, "oc_example_chat_1") {
					t.Fatalf("lark.messages reached an unauthorized/unsubscribed extension: %s", v)
				}
				return
			}

			// One single batched event for the 2 messages + 1 sentinel = value:2.
			waitLong(t, "lark.messages and sentinel delivered", func() bool { return strings.Contains(view(), "value:2") })
			v := view()
			for _, want := range []string{
				`<lark.messages `,
				`"binding":"` + bindingKey + `"`,
				`"page":1`,
				`"pages":1`,
				`"total":2`,
				`"chat_id":"oc_example_chat_1"`,
				`"message_id":"om_example_1"`,
				`"sender_open_id":"ou_sender_1"`,
				`"type":"text"`,
				`"at":"` + t1.Format(time.RFC3339) + `"`,
				`"chat_id":"oc_example_chat_2"`,
				`"message_id":"om_example_2"`,
				`"sender_open_id":"ou_sender_2"`,
				`"type":"interactive"`,
				`"at":"` + t2.Format(time.RFC3339) + `"`,
			} {
				if !strings.Contains(v, want) {
					t.Fatalf("lark.messages lacks %s: %s", want, v)
				}
			}
			// Strictly no message body, chat names, sender names, or attachment metadata.
			for _, leak := range []string{
				"Secret message body one",
				"Secret meeting body two",
				"Private Direct Chat",
				"Private Project Group",
				"Secret Person One",
				"Secret Person Two",
				"notes.pdf",
				"ref_secret",
				`"text":`,
				`"chat_name":`,
				`"sender_name":`,
				`"files":`,
			} {
				if strings.Contains(v, leak) {
					t.Fatalf("lark.messages leaked %s: %s", leak, v)
				}
			}
			// Chronological ordering: om_example_1 (t1) precedes om_example_2 (t2).
			if strings.Index(v, `"om_example_1"`) > strings.Index(v, `"om_example_2"`) {
				t.Fatalf("messages not ordered chronologically: %s", v)
			}

			// Disabling the contribution or the extension stops further delivery.
			if err := h.extensions.reg.SetContribution(context.Background(), "example-ui", "events", "lark", false, "test"); err != nil {
				t.Fatal(err)
			}
			if ids := h.extensions.reg.EventSubscribers(larksyncwire.EventLarkMessages, extension.EventScopes[larksyncwire.EventLarkMessages]); len(ids) != 0 {
				t.Fatalf("disabled contribution still subscribed: %v", ids)
			}
			if err := h.extensions.reg.SetContribution(context.Background(), "example-ui", "events", "lark", true, "test"); err != nil {
				t.Fatal(err)
			}
			if err := h.extensions.reg.SetEnabled(context.Background(), "example-ui", false, "test"); err != nil {
				t.Fatal(err)
			}
			if ids := h.extensions.reg.EventSubscribers(larksyncwire.EventLarkMessages, extension.EventScopes[larksyncwire.EventLarkMessages]); len(ids) != 0 {
				t.Fatalf("disabled extension still subscribed: %v", ids)
			}
		})
	}
}

// Each sync round emits one batched event for small batches, paginates large batches at larkMessagesPageSize, emits
// nothing when there are no new messages or when the run/commit fails, does not emit for sender-name backfill
// repairs, and keeps its own event queue isolated from KB events.
func TestLarkSyncRunEmitsBatchedAndPaginatedLarkMessages(t *testing.T) {
	needsLarkModule(t)
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	h := newTestManager(t)
	store := newLarkSyncMemStore(func() time.Time { return now })
	h.store = store
	open := newLarkEventOpen(t)
	h.cfg.Bots.Feishu = FeishuBotConfig{AppID: "cli_sync", AppSecret: "sync-secret", Domain: open.srv.URL}
	h.extensions = &extensions{h: h}
	holdEvents(&h.extensions.events)
	holdEvents(&h.extensions.larkEvents)

	drainLarkEvents := func() []larksyncwire.MessagesEvent {
		q := &h.extensions.larkEvents
		q.mu.Lock()
		pending := append([]extensionNotice(nil), q.pending...)
		q.pending = nil
		q.mu.Unlock()
		out := make([]larksyncwire.MessagesEvent, 0, len(pending))
		for _, n := range pending {
			if n.topic != larksyncwire.EventLarkMessages {
				t.Fatalf("unexpected topic in larkEvents: %q", n.topic)
			}
			raw, err := json.Marshal(n.payload)
			if err != nil {
				t.Fatal(err)
			}
			var ev larksyncwire.MessagesEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			out = append(out, ev)
		}
		return out
	}

	key := larksyncwire.PrimaryBot + "~feishu:tenant_example:ou_owner_example"
	accountID := "feishu:tenant_example:ou_owner_example"
	seedLarkEventBinding(t, h, key, larksyncwire.PrimaryBot, accountID, now)
	mux := http.NewServeMux()
	h.botRoutes(mux)

	// 1. Small batch (3 messages across 2 chats) -> 1 single event, NOT 3 events.
	base := now
	open.setMessages([]larksyncwire.Message{
		{ID: "om_3", ChatID: "oc_b", SenderID: "ou_3", Type: "post", Text: "body 3", At: base.Add(3 * time.Second)},
		{ID: "om_1", ChatID: "oc_a", SenderID: "ou_1", Type: "text", Text: "body 1", At: base.Add(1 * time.Second)},
		{ID: "om_2", ChatID: "oc_a", SenderID: "ou_2", Type: "image", Text: "body 2", At: base.Add(2 * time.Second)},
	})
	if err := runLarkEventSync(t, mux, key); err != nil {
		t.Fatalf("small batch run: %v", err)
	}
	evs := drainLarkEvents()
	if len(evs) != 1 {
		t.Fatalf("small batch emitted %d events, want 1", len(evs))
	}
	if evs[0].Binding != key || evs[0].Page != 1 || evs[0].Pages != 1 || evs[0].Total != 3 || len(evs[0].Messages) != 3 {
		t.Fatalf("unexpected small batch event: %+v", evs[0])
	}
	if evs[0].Messages[0].MessageID != "om_1" || evs[0].Messages[1].MessageID != "om_2" || evs[0].Messages[2].MessageID != "om_3" {
		t.Fatalf("messages not sorted chronologically: %+v", evs[0].Messages)
	}
	if evs[0].Messages[0].Type != larksyncwire.MessageTypeText || evs[0].Messages[1].Type != larksyncwire.MessageTypeImage || evs[0].Messages[2].Type != larksyncwire.MessageTypePost {
		t.Fatalf("unexpected message types: %+v", evs[0].Messages)
	}

	// 2. Large batch (250 messages > larkMessagesPageSize=100) -> 3 paginated events (100, 100, 50).
	const largeCount = 250
	msgs := make([]larksyncwire.Message, 0, largeCount)
	for i := range largeCount {
		msgs = append(msgs, larksyncwire.Message{
			ID: fmt.Sprintf("om_big_%03d", i), ChatID: fmt.Sprintf("oc_chat_%d", i%5),
			SenderID: fmt.Sprintf("ou_sender_%d", i%7), Type: "text", Text: "body must not be emitted",
			At: base.Add(time.Duration(i+1) * time.Second),
		})
	}
	open.setMessages(msgs)
	if err := runLarkEventSync(t, mux, key); err != nil {
		t.Fatalf("large batch run: %v", err)
	}
	evs = drainLarkEvents()
	if len(evs) != 3 {
		t.Fatalf("large batch of %d emitted %d pages, want 3", largeCount, len(evs))
	}
	wantLens := []int{100, 100, 50}
	seen := 0
	for i, ev := range evs {
		if ev.Binding != key || ev.Page != i+1 || ev.Pages != 3 || ev.Total != largeCount || len(ev.Messages) != wantLens[i] {
			t.Fatalf("page %d header/size mismatch: %+v", i+1, ev)
		}
		for _, item := range ev.Messages {
			wantID := larksyncwire.MessageID(fmt.Sprintf("om_big_%03d", seen))
			if item.MessageID != wantID {
				t.Fatalf("item %d got %q, want %q", seen, item.MessageID, wantID)
			}
			seen++
		}
	}

	// 3. Zero new messages -> 0 events.
	open.setMessages(nil)
	if err := runLarkEventSync(t, mux, key); err != nil {
		t.Fatalf("empty run: %v", err)
	}
	if evs := drainLarkEvents(); len(evs) != 0 {
		t.Fatalf("empty run emitted %d events, want 0", len(evs))
	}

	// 4. Commit failure -> 0 events (nothing committed to outbox).
	store.lmu.Lock()
	store.commitErr = errors.New("db unavailable")
	store.lmu.Unlock()
	open.setMessages([]larksyncwire.Message{{ID: "om_fail_commit", ChatID: "oc_a", SenderID: "ou_1", Type: "text", At: base}})
	if err := runLarkEventSync(t, mux, key); err == nil {
		t.Fatal("expected commit error")
	}
	store.lmu.Lock()
	store.commitErr = nil
	store.lmu.Unlock()
	if evs := drainLarkEvents(); len(evs) != 0 {
		t.Fatalf("failed commit emitted %d events, want 0", len(evs))
	}

	// 5. Run failure -> 0 events. Cancelling the manager's run context makes the real reader fail before commit.
	open.setMessages([]larksyncwire.Message{{ID: "om_fail_run", ChatID: "oc_a", SenderID: "ou_1", Type: "text", At: base}})
	oldRunCtx := h.ctx
	failedRunCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	h.ctx = failedRunCtx
	if err := runLarkEventSync(t, mux, key); err == nil {
		t.Fatal("expected run error")
	}
	h.ctx = oldRunCtx
	if evs := drainLarkEvents(); len(evs) != 0 {
		t.Fatalf("failed run emitted %d events, want 0", len(evs))
	}

	// 6. Sender-name backfill repair alone does not emit lark.messages.
	updateLarkEventBinding(t, h, key, func(row map[string]any) { row["backfill_version"] = 0 })
	names := map[string]string{"ou_1": "Resolved Name", "ou_2": "Resolved Name", "ou_3": "Resolved Name"}
	for i := range 7 {
		names[fmt.Sprintf("ou_sender_%d", i)] = "Resolved Name"
	}
	open.setNames(names)
	open.setMessages(nil)
	if err := runLarkEventSync(t, mux, key); err != nil {
		t.Fatalf("backfill run: %v", err)
	}
	if evs := drainLarkEvents(); len(evs) != 0 {
		t.Fatalf("sender-name backfill repair emitted %d events, want 0", len(evs))
	}

	// 7. Queue isolation: a flood of lark.messages pages leaves KB events untouched.
	h.AssistantSaid(assistantwire.EventKBChanged, json.RawMessage(`{"kind":"chats","id":"example-chat","what":"messages"}`))
	for i := range extensionEventLimit + 10 {
		h.emitLarkMessages(key, []larksyncwire.Message{{
			ID: fmt.Sprintf("om_flood_%d", i), ChatID: "oc_flood", SenderID: "ou_flood", Type: "text",
			At: base.Add(time.Duration(i) * time.Second),
		}})
	}
	h.extensions.events.mu.Lock()
	kbPending := len(h.extensions.events.pending)
	h.extensions.events.mu.Unlock()
	if kbPending != 1 {
		t.Fatalf("KB queue had %d pending after Lark event flood, want 1", kbPending)
	}
}

func TestLarkMessageEventPagesTypeNormalization(t *testing.T) {
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	evs := larkMessageEventPages("primary~feishu:tenant_example:ou_owner", []larksyncwire.Message{
		{ID: "om_trimmed", ChatID: "oc_1", SenderID: "ou_1", Type: "  post  ", At: base},
		{ID: "om_custom", ChatID: "oc_1", SenderID: "ou_1", Type: "sticker", At: base.Add(time.Second)},
		{ID: "om_blank", ChatID: "oc_1", SenderID: "ou_1", Type: "   ", At: base.Add(2 * time.Second)},
	}, 10)
	if len(evs) != 1 || len(evs[0].Messages) != 3 {
		t.Fatalf("unexpected pages: %+v", evs)
	}
	if got := evs[0].Messages[0].Type; got != larksyncwire.MessageTypePost {
		t.Fatalf("trimmed type = %q, want %q", got, larksyncwire.MessageTypePost)
	}
	if got := evs[0].Messages[1].Type; got != larksyncwire.MessageType("sticker") {
		t.Fatalf("custom type = %q, want %q", got, "sticker")
	}
	if got := evs[0].Messages[2].Type; got != "" {
		t.Fatalf("blank type = %q, want empty", got)
	}

	fields := fieldsOf(evs[0])
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 3 {
		t.Fatalf("decoded messages = %d, want 3", len(decoded.Messages))
	}
	if got := decoded.Messages[0]["type"]; got != "post" {
		t.Fatalf("decoded[0].type = %v, want %q", got, "post")
	}
	if got := decoded.Messages[1]["type"]; got != "sticker" {
		t.Fatalf("decoded[1].type = %v, want %q", got, "sticker")
	}
	if _, hasType := decoded.Messages[2]["type"]; hasType {
		t.Fatalf("blank message type must be omitted from JSON: %v", decoded.Messages[2])
	}
}
