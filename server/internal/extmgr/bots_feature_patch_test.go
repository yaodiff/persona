package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

type featurePatchStore struct {
	*botStore
	fail   atomic.Bool
	writes atomic.Int64
}

func (s *featurePatchStore) PutSetting(ctx context.Context, key string, value any) error {
	if s.fail.Load() {
		return errors.New("synthetic settings write failure")
	}
	s.writes.Add(1)
	return s.botStore.PutSetting(ctx, key, value)
}

type featurePatchWorld struct {
	h       *Manager
	store   *featurePatchStore
	mux     *http.ServeMux
	api     *httptest.Server
	entered chan struct{}
	release func()
}

func newFeaturePatchWorld(t *testing.T, reject bool) *featurePatchWorld {
	t.Helper()
	h := newTestManager(t)
	st := &featurePatchStore{botStore: newBotStore()}
	h.store = st
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	x := &featurePatchWorld{h: h, store: st, mux: http.NewServeMux(), entered: entered}
	x.release = func() { once.Do(func() { close(release) }) }
	x.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"fake","expire":7200}`)
		case "/open-apis/bot/v3/info":
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			if reject {
				fmt.Fprint(w, `{"code":99991,"msg":"synthetic validation failure"}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"bot":{"app_name":"Example","open_id":"ou_bot"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(x.api.Close)
	t.Cleanup(x.release)
	secret, err := h.sealKey("old-secret")
	if err != nil {
		t.Fatal(err)
	}
	// No transport is started: only BotInfo validation can contact the synthetic API.
	h.bots.loaded = true
	h.bots.set = FeishuBotSettings{AppID: "cli_old", SecretEnc: secret, Domain: x.api.URL,
		Features: defaultBotFeatures, Owner: &BotOwner{OpenID: "ou_old", ChatID: "oc_old"},
		Pairing: &botPairing{Code: "123456"}, TopicChats: []BotChat{{ChatID: "oc_group"}},
		AISources: []BotAISource{{MessageID: "om_source"}}}
	if err := h.saveBotSettingsLocked(); err != nil {
		t.Fatal(err)
	}
	h.botRoutes(x.mux)
	return x
}

func (x *featurePatchWorld) put(body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/bots/feishu", strings.NewReader(body))
	w := httptest.NewRecorder()
	x.mux.ServeHTTP(w, r)
	return w
}

func featurePatchWait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out: %s", what)
		var zero T
		return zero
	}
}

func (x *featurePatchWorld) saved(t *testing.T) FeishuBotSettings {
	t.Helper()
	var s FeishuBotSettings
	if ok, err := x.store.Setting(t.Context(), botSettingsKey, &s); err != nil || !ok {
		t.Fatalf("saved settings: %v %v", ok, err)
	}
	return s
}

func (x *featurePatchWorld) checkView(t *testing.T, s FeishuBotSettings) {
	t.Helper()
	w := httptest.NewRecorder()
	x.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/bots/feishu", nil))
	var view feishuBotView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var owner *botOwnerView
	if s.Owner != nil {
		owner = &botOwnerView{Name: s.Owner.Name, PairedAt: s.Owner.At.Unix()}
	}
	if w.Code != http.StatusOK || view.Features != s.Features || view.AppID != s.AppID || view.Enabled != s.Enabled || !reflect.DeepEqual(view.Owner, owner) {
		t.Fatalf("view differs from durable settings: %+v / %+v", view, s)
	}
	if strings.Contains(w.Body.String(), s.SecretEnc) || strings.Contains(w.Body.String(), "old-secret") || strings.Contains(w.Body.String(), "new-secret") {
		t.Fatal("settings view exposed credentials")
	}
	x.h.bots.mu.Lock()
	live := x.h.bots.set.copy()
	x.h.bots.mu.Unlock()
	if !reflect.DeepEqual(live, s) {
		t.Fatalf("live differs from durable settings: %+v / %+v", live, s)
	}
}

// Pause real BotInfo validation after the real settings route has read its input.
// The unrelated switch PUT must finish first and survive the late credential commit.
func TestBotFeaturePatchConcurrentCredentials(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name                             string
		features                         string
		newApp, validationFail, saveFail bool
		wantAgents, wantRead             bool
	}{
		{name: "partial same app", features: `,"features":{"agents":true}`, wantAgents: true, wantRead: true},
		{name: "partial new app", features: `,"features":{"agents":true}`, newApp: true, wantAgents: true, wantRead: true},
		{name: "explicit false", features: `,"features":{"main":false}`, wantRead: true},
		{name: "omitted", wantRead: true},
		{name: "null", features: `,"features":null`, wantRead: true},
		{name: "empty", features: `,"features":{}`, wantRead: true},
		{name: "null member", features: `,"features":{"read_group":null}`, wantRead: true},
		{name: "explicit full replace", features: `,"features":{"main":false,"cards":false,"all_cards":false,"pushes":false,"sync":false,"agents":true,"reports":false,"board":false,"workbench":false,"only_conclusions":false,"report_steps":false,"answer_new_card":false,"topic_cot":false,"dm_cot":false,"cot_answer":false,"read_group":false}`, wantAgents: true},
		{name: "validation failure", features: `,"features":{"agents":true}`, newApp: true, validationFail: true, wantRead: true},
		{name: "credential save failure", features: `,"features":{"agents":true}`, newApp: true, saveFail: true, wantRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newFeaturePatchWorld(t, tc.validationFail)
			id := "cli_old"
			if tc.newApp {
				id = "cli_new"
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- x.put(fmt.Sprintf(`{"app_id":%q,"app_secret":"new-secret","enabled":true%s}`, id, tc.features))
			}()
			featurePatchWait(t, x.entered, "credential validation started")
			fast := make(chan *httptest.ResponseRecorder, 1)
			go func() { fast <- x.put(`{"features":{"read_group":true}}`) }()
			w := featurePatchWait(t, fast, "independent feature PUT without waiting on validation")
			if w.Code != http.StatusOK {
				t.Fatalf("independent PUT: %d %s", w.Code, w.Body)
			}
			// A pairing callback can also replace the owner during validation. Its latest state must survive
			// a same-app commit or a failed commit; changing app ID still unpairs it.
			x.h.bots.mu.Lock()
			x.h.bots.set.Owner = &BotOwner{OpenID: "ou_new", ChatID: "oc_new"}
			err := x.h.saveBotSettingsLocked()
			x.h.bots.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			before := x.saved(t)
			x.checkView(t, before)
			x.store.takeAudit()
			x.store.fail.Store(tc.saveFail)
			x.release()
			w = featurePatchWait(t, done, "late credential PUT finished")
			failed := tc.validationFail || tc.saveFail
			if failed && w.Code == http.StatusOK || !failed && w.Code != http.StatusOK {
				t.Fatalf("late PUT: %d %s", w.Code, w.Body)
			}
			s := x.saved(t)
			if s.Features.Agents != tc.wantAgents || s.Features.ReadGroup != tc.wantRead {
				t.Fatalf("lost independent switch: %+v", s.Features)
			}
			if failed {
				if !reflect.DeepEqual(s, before) {
					t.Fatalf("failed commit changed settings: %+v / %+v", s, before)
				}
				if rows := x.store.takeAudit(); len(rows) != 0 {
					t.Fatalf("failed commit audited success: %+v", rows)
				}
			} else {
				if s.AppID != id || !s.Enabled {
					t.Fatalf("credentials or enabled not committed: %+v", s)
				}
				plain, err := x.h.openKey(s.SecretEnc)
				if err != nil || plain != "new-secret" {
					t.Fatalf("new secret not sealed: %v", err)
				}
				if tc.newApp {
					if s.Owner != nil || s.Pairing != nil || len(s.TopicChats) != 0 || len(s.AISources) != 0 {
						t.Fatalf("new app retained old owner state: %+v", s)
					}
				} else if s.Owner == nil || s.Owner.OpenID != "ou_new" || len(s.TopicChats) != 1 {
					t.Fatalf("same app lost current owner state: %+v", s)
				}
				if tc.name == "explicit false" && s.Features.Main {
					t.Fatal("explicit false was lost")
				}
			}
			if botChatAllowed(s, "oc_old") || botChatAllowed(s, "oc_stranger") {
				t.Fatal("old or unrelated owner chat regained permission")
			}
			wantCurrentOwner := failed || !tc.newApp
			if botChatAllowed(s, "oc_new") != wantCurrentOwner || botChatAllowed(s, "oc_group") != wantCurrentOwner {
				t.Fatal("current owner chat permissions changed")
			}
			x.checkView(t, s)
		})
	}
}

func TestBotFeaturePatchCompatibility(t *testing.T) {
	needsLarkModule(t)
	x := newFeaturePatchWorld(t, false)
	for _, body := range []string{`{"features":{"read_group":true}}`, `{"features":{"main":false}}`, `{}`, `{"features":null}`, `{"features":{}}`} {
		if w := x.put(body); w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", body, w.Code, w.Body)
		}
	}
	s := x.saved(t)
	if !s.Features.ReadGroup || s.Features.Main || !s.Features.Reports || !s.Features.Cards || !s.Features.TopicCot {
		t.Fatalf("omitted or older-client fields changed: %+v", s.Features)
	}
	x.checkView(t, s)
	replacement := BotFeatures{Agents: true}
	if err := x.h.updateBot(t.Context(), nil, nil, nil, nil, &botFeatureUpdate{replace: &replacement}); err != nil {
		t.Fatal(err)
	}
	s = x.saved(t)
	if s.Features != replacement {
		t.Fatalf("internal full replacement merged omitted fields: %+v", s.Features)
	}
	x.checkView(t, s)
}

func TestBotFeaturePatchRejectsInvalidBeforeValidation(t *testing.T) {
	needsLarkModule(t)
	for _, features := range []string{`[]`, `true`, `"wrong"`, `{"agents":"wrong"}`, `{"read_group":1}`} {
		t.Run(features, func(t *testing.T) {
			x := newFeaturePatchWorld(t, false)
			before := x.saved(t)
			writes := x.store.writes.Load()
			w := x.put(`{"app_id":"cli_new","app_secret":"new-secret","features":` + features + `}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid features accepted: %d %s", w.Code, w.Body)
			}
			select {
			case <-x.entered:
				t.Fatal("invalid features reached credential validation")
			default:
			}
			if x.store.writes.Load() != writes || !reflect.DeepEqual(x.saved(t), before) {
				t.Fatal("invalid features changed settings")
			}
		})
	}
}

func TestBotFeaturePatchSaveFailure(t *testing.T) {
	needsLarkModule(t)
	x := newFeaturePatchWorld(t, false)
	before := x.saved(t)
	x.store.fail.Store(true)
	w := x.put(`{"enabled":true,"features":{"agents":true,"read_group":true}}`)
	if w.Code == http.StatusOK {
		t.Fatal("failed switch-only save reported success")
	}
	x.checkView(t, before)
	if rows := x.store.takeAudit(); len(rows) != 0 {
		t.Fatalf("failed switch-only save audited success: %+v", rows)
	}
}

// The authenticated route keeps the existing boundary: a full device may save bot settings;
// unknown credentials and even an extension with all scopes cannot change them.
func TestBotFeaturePatchPermissions(t *testing.T) {
	needsLarkModule(t)
	x := newFeaturePatchWorld(t, false)
	routes := x.h.routes()
	extensionToken := x.h.extTokens.issueAll("example-extension", "Example")
	before := x.saved(t)
	for _, tc := range []struct {
		token string
		code  int
	}{{"unknown", http.StatusUnauthorized}, {extensionToken, http.StatusForbidden}} {
		code, _ := call(t, routes, http.MethodPut, "/api/v1/bots/feishu", tc.token, `{"features":{"read_group":true}}`)
		if code != tc.code {
			t.Fatalf("settings permission: %d, want %d", code, tc.code)
		}
		if !reflect.DeepEqual(x.saved(t), before) {
			t.Fatal("refused request changed settings")
		}
	}
	token := deviceToken("feature-patch-device", roleFull)
	t.Cleanup(func() { testDevices.Delete(token) })
	code, _ := call(t, routes, http.MethodPut, "/api/v1/bots/feishu", token, `{"features":{"read_group":true}}`)
	if code != http.StatusOK || !x.saved(t).Features.ReadGroup {
		t.Fatalf("full device settings save: %d", code)
	}
}

// Model the decisive read/write order without a scheduler race: configure prepares its
// request against the live features, then updateBot's lazy load observes a newer durable
// settings write. Both entry points use the real RPC/PUT handlers; no transport is started.
func TestBotFeaturePatchConfigureKeepsCommitState(t *testing.T) {
	needsLarkModule(t)
	x := newFeaturePatchWorld(t, false)
	if w := x.put(`{"features":{"read_group":true}}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	// Keep the newer PUT durable, while the preparation read sees the earlier snapshot.
	// The next locked settings load restores the newer value before the RPC commits.
	x.h.bots.mu.Lock()
	x.h.bots.set.Features = defaultBotFeatures
	x.h.bots.loaded = false
	x.h.bots.mu.Unlock()
	_, handled, err := x.h.AppCall(t.Context(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuConfigure, Params: json.RawMessage(`{"features":{"agents":true}}`)})
	if !handled || err != nil {
		t.Fatalf("configure RPC: handled=%v err=%v", handled, err)
	}
	s := x.saved(t)
	if !s.Features.Agents || !s.Features.ReadGroup || !s.Features.Reports {
		t.Fatalf("configure lost another request's switch: %+v", s.Features)
	}
	x.checkView(t, s)
	select {
	case <-x.entered:
		t.Fatal("feature RPC started app configuration")
	default:
	}
}

func TestBotFeaturePatchConfigureSemantics(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name, params  string
		initial, want BotFeatures
		bad, fail     bool
	}{
		{name: "all cards enables cards", params: `{"features":{"all_cards":true}}`, initial: BotFeatures{ReadGroup: true}, want: BotFeatures{AllCards: true, Cards: true, ReadGroup: true}},
		{name: "all cards wins over cards false", params: `{"features":{"cards":false,"all_cards":true}}`, initial: BotFeatures{ReadGroup: true}, want: BotFeatures{AllCards: true, Cards: true, ReadGroup: true}},
		{name: "all cards off keeps cards", params: `{"features":{"all_cards":false}}`, initial: BotFeatures{AllCards: true, Cards: true, ReadGroup: true}, want: BotFeatures{Cards: true, ReadGroup: true}},
		{name: "explicit false", params: `{"features":{"cards":false}}`, initial: defaultBotFeatures, want: BotFeatures{Main: true, Reports: true, TopicCot: true}},
		{name: "empty", params: `{"features":{}}`, initial: defaultBotFeatures, want: defaultBotFeatures},
		{name: "omitted", params: `{}`, initial: defaultBotFeatures, want: defaultBotFeatures},
		{name: "unknown rejects whole request", params: `{"enabled":true,"features":{"agents":true,"unknown":true}}`, initial: defaultBotFeatures, want: defaultBotFeatures, bad: true},
		{name: "failed write rolls back", params: `{"enabled":true,"features":{"all_cards":true}}`, initial: defaultBotFeatures, want: defaultBotFeatures, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newFeaturePatchWorld(t, false)
			x.h.bots.set.Features = tc.initial
			if err := x.h.saveBotSettingsLocked(); err != nil {
				t.Fatal(err)
			}
			before := x.saved(t)
			x.store.fail.Store(tc.fail)
			_, handled, err := x.h.AppCall(t.Context(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuConfigure, Params: json.RawMessage(tc.params)})
			if !handled || (err != nil) != (tc.bad || tc.fail) {
				t.Fatalf("configure RPC: handled=%v err=%v", handled, err)
			}
			s := x.saved(t)
			if s.Features != tc.want {
				t.Fatalf("features=%+v want=%+v", s.Features, tc.want)
			}
			if (tc.bad || tc.fail) && !reflect.DeepEqual(before, s) {
				t.Fatal("rejected configure changed settings")
			}
			x.checkView(t, s)
			select {
			case <-x.entered:
				t.Fatal("feature RPC started app configuration")
			default:
			}
		})
	}
}
