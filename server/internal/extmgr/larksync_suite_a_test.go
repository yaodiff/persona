package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	_ "github.com/nwverse/persona/server/internal/modules/all" // the lark module's real service and readers
)

// Mock suite A, end to end: dedup, revisions and alignment with the Lark gateway's content.
//
// Two bindings (the primary bot × account A, a sync-only bot × account B) read the same chat message and the same
// docx, through the manager's real sync route and connector (extmgr → internal/modules/lark/larksync → manager commit),
// from a fake open platform on httptest. Then the knowledge base takes what the connector hands over, next to what the
// gateway archived before: the document is merged into the gateway's item, the message the gateway already has is not
// written a second time.

type suiteAView struct {
	docRev int64
	drive  bool
	wiki   bool
}

type suiteAOpen struct {
	t     *testing.T
	srv   *httptest.Server
	now   func() time.Time
	msgAt time.Time

	mu    sync.Mutex
	views map[string]*suiteAView
	calls []string
}

const (
	suiteAChat     = "oc_team"
	suiteAChatName = "Team"
	suiteADocURL   = "https://tenant.feishu.cn/docx/doxP"
	suiteAText1    = "The rollout plan is ready for review"
	suiteAText2    = "Thanks, I will read it this afternoon"
)

func suiteABody(rev int64) string { return "Plan body at revision " + strconv.FormatInt(rev, 10) }

func newSuiteAOpen(t *testing.T, now func() time.Time, msgAt time.Time) *suiteAOpen {
	t.Helper()
	f := &suiteAOpen{t: t, now: now, msgAt: msgAt, views: map[string]*suiteAView{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *suiteAOpen) view(token string, v suiteAView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views[token] = &v
}

func (f *suiteAOpen) count(token, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, token+" ") && strings.HasSuffix(c, " "+path) {
			n++
		}
	}
	return n
}

func suiteAData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "success", "data": data})
}

func suiteAMessage(id, sender, text string, at time.Time) map[string]any {
	content, _ := json.Marshal(map[string]string{"text": text})
	return map[string]any{"message_id": id, "chat_id": suiteAChat, "msg_type": "text",
		"create_time": strconv.FormatInt(at.UnixMilli(), 10),
		"sender":      map[string]string{"id": sender, "sender_type": "user"},
		"body":        map[string]string{"content": string(content)}}
}

func (f *suiteAOpen) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.Header.Get("User-Agent") == "persona-ports" {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.calls = append(f.calls, token+" "+r.Method+" "+r.URL.Path)
	v, ok := f.views[token]
	var view suiteAView
	if ok {
		view = *v
	}
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("a call with an unknown token %q: %s %s", token, r.Method, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "invalid access token"})
		return
	}
	edited := strconv.FormatInt(f.now().Add(-10*time.Minute).Unix(), 10)
	switch p := r.URL.Path; {
	case p == "/open-apis/im/v1/chats":
		suiteAData(w, map[string]any{"items": []any{map[string]any{"chat_id": suiteAChat, "name": suiteAChatName, "chat_mode": "group"}}})
	case p == "/open-apis/im/v1/messages":
		var items []any
		if r.URL.Query().Get("container_id") == suiteAChat {
			items = []any{
				suiteAMessage("om_1", "ou_x", suiteAText1, f.msgAt),
				suiteAMessage("om_2", "ou_y", suiteAText2, f.msgAt.Add(20*time.Second)),
			}
		}
		suiteAData(w, map[string]any{"items": items})
	case strings.HasPrefix(p, "/open-apis/contact/v3/users/"):
		names := map[string]string{"ou_x": "Example Sender", "ou_y": "Another Sender"}
		suiteAData(w, map[string]any{"user": map[string]any{"name": names[strings.TrimPrefix(p, "/open-apis/contact/v3/users/")]}})
	case p == "/open-apis/search/v2/message":
		suiteAData(w, map[string]any{"items": []any{}})
	case p == "/open-apis/drive/v1/files":
		files := []any{}
		if view.drive {
			files = append(files, map[string]any{"token": "doxP", "name": "Plan", "type": "docx", "url": suiteADocURL, "modified_time": edited})
		}
		suiteAData(w, map[string]any{"files": files})
	case p == "/open-apis/wiki/v2/spaces":
		spaces := []any{}
		if view.wiki {
			spaces = append(spaces, map[string]any{"space_id": "sp1"})
		}
		suiteAData(w, map[string]any{"items": spaces})
	case p == "/open-apis/wiki/v2/spaces/sp1/nodes":
		suiteAData(w, map[string]any{"items": []any{map[string]any{"node_token": "wikP", "obj_token": "doxP", "obj_type": "docx",
			"title": "Plan", "obj_edit_time": edited}}})
	case p == "/open-apis/docx/v1/documents/doxP":
		suiteAData(w, map[string]any{"document": map[string]any{"revision_id": view.docRev, "title": "Plan"}})
	case p == "/open-apis/docx/v1/documents/doxP/blocks":
		suiteAData(w, map[string]any{"items": []any{
			map[string]any{"block_id": "doxP", "block_type": 1, "children": []string{"p1"},
				"page": map[string]any{"elements": []any{map[string]any{"text_run": map[string]any{"content": "Plan"}}}}},
			map[string]any{"block_id": "p1", "parent_id": "doxP", "block_type": 2,
				"text": map[string]any{"elements": []any{map[string]any{"text_run": map[string]any{"content": suiteABody(view.docRev)}}}}},
		}})
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

type suiteASource struct{ name string }

func (s suiteASource) Name() string        { return s.name }
func (s suiteASource) ConnectorID() string { return s.name }
func (suiteASource) Chats(context.Context, int) ([]kbwire.SourceChat, error) {
	return nil, nil
}
func (suiteASource) Messages(context.Context, kbwire.SourceChat, string, int) ([]kbwire.SourceMessage, string, bool, error) {
	return nil, "", false, nil
}
func (suiteASource) Download(context.Context, string, string) error {
	return errors.New("no attachments")
}

func suiteAChanges(t *testing.T, c connector.Archive) []connector.ChatBatch {
	t.Helper()
	var out []connector.ChatBatch
	cursor := ""
	for range 100 {
		batches, next, more, err := c.Changes(context.Background(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, batches...)
		cursor = next
		if !more {
			return out
		}
	}
	t.Fatal("the connector's changes never ended")
	return nil
}

func suiteABindings(refs []kbwire.SourceRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Binding+"/"+r.Entry)
	}
	slices.Sort(out)
	return out
}

func TestLarkSyncSuiteA(t *testing.T) {
	needsLarkModule(t)
	ctx := context.Background()
	h := newTestManager(t)
	store := newLarkSyncMemStore(time.Now)
	h.store = store

	var clockMu sync.Mutex
	clock := time.Now().UTC().Truncate(time.Minute)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}

	// om_1 at 10 s past a minute: an even second, so the gateway's copy one second later is in the same 2 s bucket.
	msgAt := now().Add(-30 * time.Minute).Add(10 * time.Second)
	open := newSuiteAOpen(t, now, msgAt)
	h.cfg.Bots.Feishu = FeishuBotConfig{AppID: "cli_primary", AppSecret: "primary-secret", Domain: open.srv.URL}

	seal := func(plain string) string {
		s, err := h.sealKey(plain)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	accA, accB := "feishu:t1:ou_a", "feishu:t1:ou_b"
	keyA, keyB := "primary~acct_a", "syncb~acct_b"
	authAt := now()
	expAt := authAt.Add(30 * 24 * time.Hour)

	if err := store.PutSetting(ctx, larksyncwire.SettingBots, []map[string]any{{
		"bot_id":     "syncb",
		"label":      "Sync B",
		"app_id":     "cli_syncb",
		"secret_enc": seal("syncb-secret"),
		"domain":     open.srv.URL,
		"role":       larksyncwire.RoleSyncOnly,
		"tier":       larksyncwire.TierFull,
		"approval":   larksyncwire.ApprovalReady,
		"created_at": authAt,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(ctx, larksyncwire.SettingAccounts, []map[string]any{
		{"account_id": accA, "label": "Account ou_a", "brand": "feishu", "tenant_key": "t1", "open_id": "ou_a", "name": "Account ou_a"},
		{"account_id": accB, "label": "Account ou_b", "brand": "feishu", "tenant_key": "t1", "open_id": "ou_b", "name": "Account ou_b"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(ctx, larksyncwire.SettingBindings, []map[string]any{
		{
			"key": keyA, "bot_id": larksyncwire.PrimaryBot, "account_id": accA,
			"tier": larksyncwire.TierFull, "enabled": true,
			"access_token_enc": seal("tok-a"), "refresh_token_enc": seal("ref-a"),
			"token_expires_at": expAt, "authorized_at": authAt,
			"granted_scopes": larksyncwire.ScopesFor(larksyncwire.TierFull),
			"health":         larksyncwire.HealthOK,
		},
		{
			"key": keyB, "bot_id": "syncb", "account_id": accB,
			"tier": larksyncwire.TierFull, "enabled": true,
			"access_token_enc": seal("tok-b"), "refresh_token_enc": seal("ref-b"),
			"token_expires_at": expAt, "authorized_at": authAt,
			"granted_scopes": larksyncwire.ScopesFor(larksyncwire.TierFull),
			"health":         larksyncwire.HealthOK,
		},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.botRoutes(mux)

	run := func(key string) {
		t.Helper()
		if code, body := doSyncReq(t, mux, http.MethodPost, "/api/v1/bots/feishu/sync/bindings/"+key+"/run", ""); code != 200 {
			t.Fatalf("start run %s: %d %v", key, code, body)
		}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		deadline := time.After(15 * time.Second)
		for {
			_, view := doSyncReq(t, mux, http.MethodGet, "/api/v1/bots/feishu/sync", "")
			var found map[string]any
			for _, raw := range view["bindings"].([]any) {
				m := raw.(map[string]any)
				if m["key"] == key {
					found = m
					break
				}
			}
			if found == nil {
				t.Fatalf("binding %s missing from view", key)
			}
			if running, _ := found["running"].(bool); !running {
				if health, _ := found["health"].(string); health != larksyncwire.HealthOK {
					t.Fatalf("run %s: health %s (%v)", key, health, found["error"])
				}
				break
			}
			select {
			case <-tick.C:
			case <-deadline:
				t.Fatalf("run %s did not finish", key)
			}
		}
		advance(time.Hour)
	}

	conn := h.larkSyncConnector()
	if conn == nil {
		t.Fatal("larkSyncConnector returned nil with lark module in build")
	}
	arch := conn.(connector.Archive)
	itemsSrc := conn.(connector.Items)
	const msgKey, docKey = larksyncwire.KeyMessage + "om_1", larksyncwire.KeyDoc + "doxP"

	// 1. Dedup across bindings. A finds the docx in its Drive, B as the wiki node wikP; both read om_1 in the chat.
	open.view("tok-a", suiteAView{docRev: 1, drive: true})
	open.view("tok-b", suiteAView{docRev: 1, wiki: true})
	run(keyA)
	run(keyB)
	if got, want := suiteABindings(store.index[msgKey].Sources), []string{keyA + "/chat", keyB + "/chat"}; !slices.Equal(got, want) {
		t.Fatalf("om_1's sources %v, want %v", got, want)
	}
	if got, want := suiteABindings(store.index[docKey].Sources), []string{keyA + "/drive", keyB + "/wiki"}; !slices.Equal(got, want) {
		t.Fatalf("doxP's sources %v, want %v", got, want)
	}
	if rec := store.index[docKey]; rec.Revision != 1 || rec.Kind != "doc" {
		t.Fatalf("doxP's record: %+v", rec)
	}
	batches := suiteAChanges(t, arch)
	if len(batches) != 1 || batches[0].Chat.ID != suiteAChat || batches[0].Chat.Name != suiteAChatName {
		t.Fatalf("batches: %+v", batches)
	}
	var ids []string
	for _, m := range batches[0].Messages {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"om_1", "om_2"}) {
		t.Fatalf("handed-over messages %v, want om_1 and om_2 once each", ids)
	}
	if m := batches[0].Messages[0]; m.SenderName != "Example Sender" || m.Text != suiteAText1 || !m.At.Equal(msgAt) || m.Mine {
		t.Fatalf("om_1 handed over as %+v", m)
	}
	items, err := itemsSrc.ItemSource().Items(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %+v, %v", items, err)
	}
	if it := items[0]; it.CanonicalKey != docKey || it.Revision != 1 || it.Content != suiteABody(1) || it.URL != suiteADocURL || it.Source != larksyncwire.ConnectorID {
		t.Fatalf("item: %+v", it)
	}
	if n := open.count("tok-b", "/open-apis/docx/v1/documents/doxP/blocks"); n != 0 {
		t.Fatalf("B read the body %d times", n)
	}

	// 2. Revisions. The document moves to rev2 and A reads it: handed over again, the newer body, still one item.
	open.view("tok-a", suiteAView{docRev: 2, drive: true})
	run(keyA)
	if rec := store.index[docKey]; rec.Revision != 2 || rec.BodyHash == "" {
		t.Fatalf("doxP's record after rev2: %+v", rec)
	}
	hash2 := store.index[docKey].BodyHash
	items, err = itemsSrc.ItemSource().Items(ctx)
	if err != nil || len(items) != 1 || items[0].Revision != 2 || items[0].Content != suiteABody(2) || items[0].BodyHash != hash2 {
		t.Fatalf("items after rev2: %+v, %v", items, err)
	}
	open.view("tok-b", suiteAView{docRev: 1, drive: true, wiki: true})
	docRows := func() int {
		store.lmu.Lock()
		defer store.lmu.Unlock()
		n := 0
		for _, o := range store.outbox {
			if o.Kind == db.LarkSyncDoc {
				n++
			}
		}
		return n
	}
	before := docRows()
	run(keyB)
	rec := store.index[docKey]
	if rec.Revision != 2 || rec.BodyHash != hash2 {
		t.Fatalf("the late rev1 moved the record: %+v", rec)
	}
	if got, want := suiteABindings(rec.Sources), []string{keyA + "/drive", keyB + "/drive", keyB + "/wiki"}; !slices.Equal(got, want) {
		t.Fatalf("doxP's sources after the late read %v, want %v", got, want)
	}
	if after := docRows(); after != before {
		t.Fatalf("the late rev1 was handed over: %d document rows, were %d", after, before)
	}
	if n := open.count("tok-b", "/open-apis/docx/v1/documents/doxP/blocks"); n != 0 {
		t.Fatalf("B read the stale body %d times", n)
	}
	if n := open.count("tok-a", "/open-apis/docx/v1/documents/doxP/blocks"); n != 2 {
		t.Fatalf("A read the body %d times, want twice (rev1, rev2)", n)
	}
	items, err = itemsSrc.ItemSource().Items(ctx)
	if err != nil || len(items) != 1 || items[0].Revision != 2 || items[0].Content != suiteABody(2) {
		t.Fatalf("items after the late rev1: %+v, %v", items, err)
	}
	if batches := suiteAChanges(t, arch); len(batches) != 1 {
		t.Fatalf("the later runs handed messages over again: %+v", batches)
	}

	// 3. Alignment with the gateway.
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &kb.Engine{Store: st, Logf: func(string, ...any) {}}
	gwChat := kbwire.SourceChat{ID: "feed1", Name: suiteAChatName, Kind: "group"}
	if n, err := e.Ingest(ctx, suiteASource{"lark"}, gwChat, []kbwire.SourceMessage{
		{ID: "g1", SenderID: "u_x", SenderName: "Example Sender", Text: suiteAText1, At: msgAt.Add(time.Second)},
	}); err != nil || n != 1 {
		t.Fatalf("gateway ingest: %d, %v", n, err)
	}
	if err := st.PutItem(&kb.Item{Kind: kb.KindDoc, Source: "lark", ID: "gw_plan", Title: "Plan", URL: suiteADocURL, Seen: now()}); err != nil {
		t.Fatal(err)
	}
	e.Align = kb.NewGatewayAligner(e.Store)

	written := 0
	for _, b := range suiteAChanges(t, arch) {
		n, err := e.Ingest(ctx, suiteASource{larksyncwire.ConnectorID}, b.Chat, b.Messages)
		if err != nil {
			t.Fatal(err)
		}
		written += n
	}
	if written != 1 {
		t.Fatalf("%d messages written, want 1 (om_2; om_1 is the gateway's)", written)
	}
	gwText := st.Since(kb.ChatID("lark", "feed1"), time.Time{}, 1<<20)
	if n := strings.Count(gwText, suiteAText1); n != 1 {
		t.Fatalf("the gateway chat holds om_1 %d times:\n%s", n, gwText)
	}
	syncID := kb.ChatID(larksyncwire.ConnectorID, suiteAChat)
	syncText := st.Since(syncID, time.Time{}, 1<<20)
	if strings.Contains(syncText, suiteAText1) || !strings.Contains(syncText, "Another Sender: "+suiteAText2) {
		t.Fatalf("the sync's chat:\n%s", syncText)
	}
	raw, err := os.ReadFile(filepath.Join(st.Dir, kb.KindChat, ".crosswalk.json"))
	if err != nil {
		t.Fatalf("crosswalk: %v", err)
	}
	var walk map[string]string
	if err := json.Unmarshal(raw, &walk); err != nil || walk[syncID] != kb.ChatID("lark", "feed1") || len(walk) != 1 {
		t.Fatalf("crosswalk %s (%v)", raw, err)
	}

	for range 2 {
		if err := e.CollectItems(ctx, itemsSrc.ItemSource(), larksyncwire.ConnectorID); err != nil {
			t.Fatal(err)
		}
		docs := st.Items(kb.KindDoc)
		if len(docs) != 1 {
			var got []string
			for _, d := range docs {
				got = append(got, d.Source+"/"+d.ID)
			}
			t.Fatalf("documents %v, want the gateway's one", got)
		}
		d := docs[0]
		if d.ID != "gw_plan" || d.Source != "lark" || d.Content != suiteABody(2) || d.Revision != 2 || d.BodyHash != hash2 || d.CanonicalKey != docKey {
			t.Fatalf("the gateway's document: %+v", d)
		}
		var channels []string
		for _, s := range d.Sources {
			channels = append(channels, s.Channel+"/"+s.Entry)
		}
		if want := []string{"lark/" + larksyncwire.EntryGateway, larksyncwire.ConnectorID + "/" + larksyncwire.EntryDrive}; !slices.Equal(channels, want) {
			t.Fatalf("the document's sources %v, want %v", channels, want)
		}
		if d.Sources[1].Binding != keyA || d.Sources[1].Account != accA {
			t.Fatalf("the sync's source: %+v", d.Sources[1])
		}
	}
}
