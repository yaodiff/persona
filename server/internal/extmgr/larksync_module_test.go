package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// larkSyncMemStore is botStore with the sync's Postgres tables in memory for extmgr integration tests.
type larkSyncMemStore struct {
	*botStore
	now func() time.Time

	lmu       sync.Mutex
	cursors   map[string]map[string]string
	index     map[string]larksyncwire.IndexRecord
	outbox    []db.LarkSyncOutbox
	seq       int64
	commitErr error
}

func newLarkSyncMemStore(now func() time.Time) *larkSyncMemStore {
	return &larkSyncMemStore{
		botStore: newBotStore(),
		now:      now,
		cursors:  map[string]map[string]string{},
		index:    map[string]larksyncwire.IndexRecord{},
	}
}

var _ larksyncwire.Store = (*larkSyncMemStore)(nil)

func (s *larkSyncMemStore) LarkSyncCursors(_ context.Context, binding string) (map[string]string, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	out := map[string]string{}
	for k, v := range s.cursors[binding] {
		out[k] = v
	}
	return out, nil
}

func (s *larkSyncMemStore) DeleteLarkSyncCursors(_ context.Context, binding string) error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	delete(s.cursors, binding)
	return nil
}

func (s *larkSyncMemStore) LarkSyncIndex(_ context.Context, keys []string) (map[string]larksyncwire.IndexRecord, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	out := map[string]larksyncwire.IndexRecord{}
	for _, k := range keys {
		if r, ok := s.index[k]; ok {
			out[k] = r
		}
	}
	return out, nil
}

func (s *larkSyncMemStore) CommitLarkSyncRun(_ context.Context, binding string, cursors map[string]string, recs []larksyncwire.IndexRecord, outbox []db.LarkSyncOutbox) error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	if len(cursors) > 0 && s.cursors[binding] == nil {
		s.cursors[binding] = map[string]string{}
	}
	for k, v := range cursors {
		s.cursors[binding][k] = v
	}
	for _, r := range recs {
		have, ok := s.index[r.Key]
		if ok {
			if r.Revision < have.Revision {
				r.Revision, r.BodyHash = have.Revision, have.BodyHash
			}
			r.Sources = larksyncwire.MergeSources(have.Sources, r.Sources)
		}
		s.index[r.Key] = r
	}
	for _, o := range outbox {
		s.seq++
		o.Seq, o.CreatedAt = s.seq, s.now()
		s.outbox = append(s.outbox, o)
	}
	return nil
}

func (s *larkSyncMemStore) LarkSyncOutboxAfter(_ context.Context, kind larksyncwire.OutboxKind, after int64, limit int) ([]db.LarkSyncOutbox, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	var out []db.LarkSyncOutbox
	for _, o := range s.outbox {
		if o.Kind == kind && o.Seq > after && len(out) < limit {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *larkSyncMemStore) LarkSyncDocsSince(_ context.Context, since time.Time) ([]db.LarkSyncOutbox, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	latest := map[string]db.LarkSyncOutbox{}
	for _, o := range s.outbox {
		if o.Kind == db.LarkSyncDoc && !o.CreatedAt.Before(since) {
			latest[o.CanonicalKey] = o
		}
	}
	var out []db.LarkSyncOutbox
	for _, o := range latest {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b db.LarkSyncOutbox) int { return int(a.Seq - b.Seq) })
	return out, nil
}

func (s *larkSyncMemStore) PruneLarkSyncOutbox(_ context.Context, before time.Time) (int64, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	n := len(s.outbox)
	s.outbox = slices.DeleteFunc(s.outbox, func(o db.LarkSyncOutbox) bool { return o.CreatedAt.Before(before) })
	return int64(n - len(s.outbox)), nil
}

func (s *larkSyncMemStore) raw(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.vals[key])
}

type syncFakeRegistrar struct {
	mu     sync.Mutex
	begins []larksyncwire.RegisterOptions
}

func (r *syncFakeRegistrar) Begin(ctx context.Context, region, name string) (botcard.RegisterChallenge, error) {
	return r.BeginWith(ctx, region, name, larksyncwire.RegisterOptions{})
}

func (r *syncFakeRegistrar) BeginWith(_ context.Context, _, _ string, opts larksyncwire.RegisterOptions) (botcard.RegisterChallenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begins = append(r.begins, opts)
	return botcard.RegisterChallenge{DeviceCode: "dc_primary", URL: "https://accounts.example.com/register?user_code=UC",
		ExpiresIn: 600, Interval: 5}, nil
}

func (r *syncFakeRegistrar) Poll(context.Context, string, string) (botcard.RegisterTicket, error) {
	return botcard.RegisterTicket{Status: botcard.RegisterPending}, nil
}

func doSyncReq(t *testing.T, mux http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(withPrincipal(req.Context(), principal{Role: roleFull}))
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// With the lark module switched off (or left out of the build: persona_without_lark, persona_core_only), lark sync
// is absent: its routes answer 404, no larksync connector is registered, PrimaryTier defaults to TierDocs, Loop
// returns immediately, and previously stored sync settings stay untouched.
func TestLarkSyncFollowsItsModule(t *testing.T) {
	h := newTestManager(t)
	h.opts.Modules = moduleswitch.Off("lark")
	st := newLarkSyncMemStore(time.Now)
	savedPrefs := `{"schedule_hour":5}`
	st.vals[larksyncwire.SettingPrefs] = json.RawMessage(savedPrefs)
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()

	if h.connectors.Get(larksyncwire.ConnectorID) != nil {
		t.Fatal("larksync connector registered with lark module off")
	}
	if got := h.larkSyncPrimaryTier(context.Background()); got != larksyncwire.TierDocs {
		t.Fatalf("larkSyncPrimaryTier with module off = %q, want %q", got, larksyncwire.TierDocs)
	}
	h.larkSyncLoop(context.Background()) // returns at once

	mux := http.NewServeMux()
	h.botRoutes(mux)
	if code, _ := doSyncReq(t, mux, http.MethodGet, "/api/v1/bots/feishu/sync", ""); code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/bots/feishu/sync with module off = %d, want 404", code)
	}
	if got := st.raw(larksyncwire.SettingPrefs); got != savedPrefs {
		t.Fatalf("stored sync settings changed with module off: %s -> %s", savedPrefs, got)
	}
}

// The chat bot's first registration (binding it by QR) asks the user scopes of the sync's primary tier.
func TestLarkSyncPrimaryRegistrationAsksUserScopes(t *testing.T) {
	h := newTestManager(t)
	h.store = newLarkSyncMemStore(time.Now)
	reg := &syncFakeRegistrar{}
	h.bots.registrar = reg
	if _, ok := h.beginFeishuRegistration(context.Background(), ""); !ok {
		t.Fatal("the registration did not begin")
	}
	if len(reg.begins) != 1 || !slices.Equal(reg.begins[0].UserScopes, larksyncwire.ScopesDocs) || reg.begins[0].AppID != "" {
		t.Fatalf("first registration: %+v", reg.begins)
	}
}

// The chat bot's view keys before the sync (bots.go feishuBotView): the sync adds none.
var legacyBotViewKeys = []string{"enabled", "app_id", "domain", "has_secret", "from_config", "features", "connected", "error",
	"last_event_at", "bot_name", "owner", "pairing", "topic_chats", "nudge_after", "source_report_delay_minutes",
	"source_report_full_reply", "target", "agents", "larkcli"}

func TestLarkSyncLegacySavedBotKeepsWorking(t *testing.T) {
	needsLarkModule(t)
	api := newFakeOpenAPI(t)
	first := newTestManager(t)
	st := newLarkSyncMemStore(time.Now)
	first.store = st
	withAssistant(first)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first.startBots(ctx)
	mux := http.NewServeMux()
	first.botRoutes(mux)
	if code, out := doSyncReq(t, mux, "PUT", "/api/v1/bots/feishu",
		fmt.Sprintf(`{"enabled":true,"app_id":"cli_legacy","app_secret":"legacy-secret","domain":%q,"features":{"main":true}}`, api.srv.URL)); code != 200 {
		t.Fatalf("enable: %d %v", code, out)
	}
	saved := st.raw(botSettingsKey)

	h := newTestManager(t)
	h.secret = first.secret
	st2 := newLarkSyncMemStore(time.Now)
	st2.vals[botSettingsKey] = []byte(saved)
	h.store = st2
	withAssistant(h)
	h.startBots(ctx)
	mux2 := http.NewServeMux()
	h.botRoutes(mux2)
	h.bots.mu.Lock()
	started := h.bots.api != nil
	h.bots.mu.Unlock()
	if !started {
		t.Fatal("the saved bot did not start")
	}
	code, view := doSyncReq(t, mux2, "GET", "/api/v1/bots/feishu", "")
	if code != 200 || view["app_id"] != "cli_legacy" || view["enabled"] != true || view["has_secret"] != true || view["from_config"] != false {
		t.Fatalf("view: %d %v", code, view)
	}
	for k := range view {
		if !slices.Contains(legacyBotViewKeys, k) {
			t.Errorf("the chat bot's view gained %q", k)
		}
	}
	code, syncView := doSyncReq(t, mux2, "GET", "/api/v1/bots/feishu/sync", "")
	bots, _ := syncView["bots"].([]any)
	if code != 200 || len(bots) != 1 || len(syncView["bindings"].([]any)) != 0 || syncView["schedule_hour"] != float64(3) {
		t.Fatalf("sync view: %d %v", code, syncView)
	}
	if b := bots[0].(map[string]any); b["bot_id"] != larksyncwire.PrimaryBot || b["app_id"] != "cli_legacy" || b["primary"] != true ||
		b["has_secret"] != true || b["role"] != larksyncwire.RoleChatAndSync || b["tier"] != larksyncwire.TierDocs {
		t.Fatalf("primary: %v", b)
	}
	rawView, _ := json.Marshal(syncView)
	if strings.Contains(string(rawView), "legacy-secret") {
		t.Fatalf("the sync view leaked the secret: %s", rawView)
	}
	for _, k := range []string{larksyncwire.SettingPrefs, larksyncwire.SettingBots, larksyncwire.SettingBindings, larksyncwire.SettingAccounts} {
		if raw := st2.raw(k); raw != "" {
			t.Fatalf("%s written: %s", k, raw)
		}
	}
	if st2.raw(botSettingsKey) != saved {
		t.Fatalf("bots.feishu changed: %s -> %s", saved, st2.raw(botSettingsKey))
	}
}

// hub.json's bots.feishu alone (no settings at all) is the primary bot too.
func TestLarkSyncHubJSONOnlyBot(t *testing.T) {
	needsLarkModule(t)
	h := newTestManager(t)
	h.cfg.Bots.Feishu = FeishuBotConfig{AppID: "cli_primary", AppSecret: "primary-secret", Domain: "feishu"}
	st := newLarkSyncMemStore(time.Now)
	h.store = st
	mux := http.NewServeMux()
	h.botRoutes(mux)

	code, view := doSyncReq(t, mux, "GET", "/api/v1/bots/feishu", "")
	if code != 200 || view["app_id"] != "cli_primary" || view["from_config"] != true {
		t.Fatalf("view: %d %v", code, view)
	}
	_, syncView := doSyncReq(t, mux, "GET", "/api/v1/bots/feishu/sync", "")
	bots := syncView["bots"].([]any)
	if len(bots) != 1 || bots[0].(map[string]any)["app_id"] != "cli_primary" || bots[0].(map[string]any)["has_secret"] != true {
		t.Fatalf("sync view: %v", syncView)
	}

	// No app anywhere: the view lists no bot, and authorizing the primary one says there is no app.
	h.cfg.Bots.Feishu = FeishuBotConfig{}
	_, syncView = doSyncReq(t, mux, "GET", "/api/v1/bots/feishu/sync", "")
	if len(syncView["bots"].([]any)) != 0 {
		t.Fatalf("no app: %v", syncView)
	}
	if code, _ := doSyncReq(t, mux, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"https://hub.example/lark-oauth"}`); code != http.StatusConflict {
		t.Fatalf("no app: %d", code)
	}
}

// db.SealedSettings lists every sealed lark sync setting key and field declared in larksyncwire, and excludes accounts.
func TestLarkSyncSealedFieldsAreReSealed(t *testing.T) {
	for _, c := range []struct {
		key    string
		list   bool
		fields []string
	}{
		{larksyncwire.SettingPrefs, false, larksyncwire.SealedPrefsFields},
		{larksyncwire.SettingBots, true, larksyncwire.SealedBotsFields},
		{larksyncwire.SettingBindings, true, larksyncwire.SealedBindingsFields},
	} {
		i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == c.key })
		if i < 0 || db.SealedSettings()[i].List != c.list {
			t.Errorf("%s is not in db.SealedSettings (list %v)", c.key, c.list)
			continue
		}
		if !slices.Equal(db.SealedSettings()[i].Fields, c.fields) {
			t.Errorf("%s fields = %v, want %v", c.key, db.SealedSettings()[i].Fields, c.fields)
		}
	}
	if slices.ContainsFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == larksyncwire.SettingAccounts }) {
		t.Error("bots.feishu.sync.accounts is listed as sealed")
	}
}
