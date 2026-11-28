package larksync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/modules/lark/larkauth"
)

const testRedirect = "https://hub.example/lark-oauth"

// A new sync-only bot is registered by QR with the user scopes of its tier; the device code and the app's secret are
// sealed in top-level fields; adding the scopes to an existing app (the chat bot's) names that app.
func TestLarkSyncBotRegistersWithUserScopes(t *testing.T) {
	w := newServiceWorld(t)
	w.reg.polls = []botcard.RegisterTicket{{Status: botcard.RegisterPending},
		{Status: botcard.RegisterDone, AppID: "cli_new", AppSecret: "new-app-secret", Brand: "lark"}}
	code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots", `{"label":"Work","tier":"full_with_im"}`)
	if code != 200 {
		t.Fatalf("add bot: %d %v", code, out)
	}
	id, _ := out["bot_id"].(string)
	p, _ := out["prompt"].(map[string]any)
	if id == "" || p["step"] != stepRegister || p["kind"] != "qr" || p["qr"] == nil {
		t.Fatalf("add bot: %v", out)
	}
	opts := w.reg.lastBegin(t)
	if !slices.Equal(opts.UserScopes, larksyncwire.ScopesFor(larksyncwire.TierFull)) || opts.AppID != "" {
		t.Fatalf("registration options: %+v", opts)
	}
	raw := w.store.raw(botsKey)
	noSecretIn(t, "the answer", marshalJSON(t, out), "dc_sync_secret")
	noSecretIn(t, "bots.feishu.sync.bots", raw, "dc_sync_secret")
	if !strings.Contains(raw, `"registration_device_enc":"`) {
		t.Fatalf("the device code is not sealed at the top level: %s", raw)
	}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/"+id+"/register", `{"step":"register"}`); code != 200 || p["done"] != nil {
		t.Fatalf("pending: %d %v", code, p)
	}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/"+id+"/register", `{"step":"register"}`); code != 200 || p["done"] != true {
		t.Fatalf("done: %d %v", code, p)
	}
	raw = w.store.raw(botsKey)
	noSecretIn(t, "bots.feishu.sync.bots", raw, "new-app-secret", "dc_sync_secret")
	if strings.Contains(raw, "registration_device_enc") {
		t.Fatalf("the finished registration kept its device code: %s", raw)
	}
	_, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	bots, _ := view["bots"].([]any)
	if len(bots) != 2 {
		t.Fatalf("bots: %v", view)
	}
	if b := bots[1].(map[string]any); b["bot_id"] != id || b["app_id"] != "cli_new" || b["has_secret"] != true || b["domain"] != "lark" ||
		b["role"] != larksyncwire.RoleSyncOnly || b["tier"] != larksyncwire.TierFull || b["label"] != "Work" || b["approval"] != larksyncwire.ApprovalReady {
		t.Fatalf("sync bot: %v", b)
	}
	noSecretIn(t, "the view", marshalJSON(t, view), "new-app-secret")

	// The chat bot's app gets the user scopes added: the registration names it.
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/scopes", `{"tier":"full_with_im"}`); code != 200 || p["step"] != stepRegister {
		t.Fatalf("scopes: %d %v", code, p)
	}
	if opts := w.reg.lastBegin(t); opts.AppID != "cli_primary" || !slices.Equal(opts.UserScopes, larksyncwire.ScopesFor(larksyncwire.TierFull)) {
		t.Fatalf("adding scopes: %+v", opts)
	}
	w.reg.polls = []botcard.RegisterTicket{{Status: botcard.RegisterDone, AppID: "cli_primary"}}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/register", ""); code != 200 || p["done"] != true {
		t.Fatalf("scopes done: %d %v", code, p)
	}
	_, view = w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	if b := view["bots"].([]any)[0].(map[string]any); b["primary"] != true || b["tier"] != larksyncwire.TierFull || b["app_id"] != "cli_primary" {
		t.Fatalf("primary: %v", b)
	}
	if raw := w.store.raw("bots.feishu"); raw != "" {
		t.Fatalf("bots.feishu written: %s", raw)
	}

	// Removing a sync bot drops it with its bindings; the chat bot cannot be removed here.
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/sync/bots/primary", ""); code != 409 {
		t.Fatalf("removing the chat bot: %d", code)
	}
	key, _ := w.adopt(t, id, "ou_w", "acc-w", "ref-w", time.Hour)
	if code, view := w.do(t, "DELETE", "/api/v1/bots/feishu/sync/bots/"+id, ""); code != 200 || len(view["bots"].([]any)) != 1 || len(view["bindings"].([]any)) != 0 {
		t.Fatalf("remove: %d %v", code, view)
	}
	if st, _ := w.svc.load(context.Background()); st.binding(key) != nil {
		t.Fatal("the bot's binding stayed")
	}
}

// The browser authorization: the redirect URL and the scopes are configured on the app once (under review: approval
// awaiting), the PKCE verifier is sealed, and the return with state and code binds the account.
func TestLarkSyncBrowserAuthorization(t *testing.T) {
	w := newServiceWorld(t)
	w.ua.configure = larksyncwire.RedirectReport{Configured: true, Published: true, Awaiting: true}
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"https://hub.example/elsewhere"}`); code != 400 {
		t.Fatalf("a redirect that is not /lark-oauth: %d", code)
	}
	code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`)
	if code != 200 || out["mode"] != modeBrowser || !strings.Contains(out["url"].(string), "state=") {
		t.Fatalf("authorize: %d %v", code, out)
	}
	if len(w.ua.configured) != 1 || w.ua.configured[0] != "cli_primary "+testRedirect {
		t.Fatalf("configured: %v", w.ua.configured)
	}
	req := w.ua.authorized[0]
	if len(req.State) != 64 || len(req.Verifier) < 43 || req.RedirectURI != testRedirect || !slices.Equal(req.Scopes, larksyncwire.ScopesDocs) {
		t.Fatalf("auth request: %+v", req)
	}
	raw := w.store.raw(bindingsKey)
	noSecretIn(t, "bots.feishu.sync.bindings", raw, req.Verifier)
	noSecretIn(t, "the answer", marshalJSON(t, out), req.Verifier)
	if !strings.Contains(raw, `"auth_verifier_enc":"`) {
		t.Fatalf("the verifier is not sealed: %s", raw)
	}
	// Configured once: authorizing again goes straight to the browser.
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`); code != 200 || len(w.ua.configured) != 1 {
		t.Fatalf("again: %d, configured %v", code, w.ua.configured)
	}
	req = w.ua.authorized[1]

	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"not-it","code":"c1"}`); code != 400 {
		t.Fatalf("an unknown state: %d", code)
	}
	w.ua.exchange["c1"] = larksyncwire.Token{AccessToken: "user-access-1", RefreshToken: "user-refresh-1", ExpiresAt: w.now().Add(2 * time.Hour),
		OpenID: "ou_me", TenantKey: "tk", Name: "Me", Scopes: larksyncwire.ScopesDocs}
	code, view := w.do(t, "POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"`+req.State+`","code":"c1"}`)
	if code != 200 {
		t.Fatalf("oauth: %d %v", code, view)
	}
	if got := w.ua.exchanged[0]; got != "c1 "+testRedirect+" "+req.Verifier {
		t.Fatalf("exchanged %q", got)
	}
	bindings := view["bindings"].([]any)
	accounts := view["accounts"].([]any)
	if len(bindings) != 1 || len(accounts) != 1 {
		t.Fatalf("view: %v", view)
	}
	b := bindings[0].(map[string]any)
	if b["health"] != larksyncwire.HealthAwaiting || b["bot_id"] != larksyncwire.PrimaryBot || b["account_id"] != "feishu:tk:ou_me" ||
		b["enabled"] != true || b["reauth_due_days"] != float64(365) {
		t.Fatalf("binding: %v", b)
	}
	if a := accounts[0].(map[string]any); a["label"] != "Me" || a["brand"] != "feishu" {
		t.Fatalf("account: %v", a)
	}
	raw = w.store.raw(bindingsKey)
	noSecretIn(t, "bots.feishu.sync.bindings", raw, "user-access-1", "user-refresh-1", req.Verifier)
	noSecretIn(t, "the view", marshalJSON(t, view), "user-access-1", "user-refresh-1", "_enc")
	if strings.Contains(raw, "~pending") {
		t.Fatalf("the authorization under way stayed: %s", raw)
	}
	// The state is used once.
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"`+req.State+`","code":"c1"}`); code != 400 {
		t.Fatalf("the state was used again: %d", code)
	}
	// A state that ran out is refused.
	w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`)
	w.advance(authFor + time.Second)
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"`+w.ua.authorized[2].State+`","code":"c1"}`); code != 400 {
		t.Fatalf("an expired state: %d", code)
	}
	// The tick drops it once it ran out.
	w.advance(2 * time.Minute)
	w.svc.remind(context.Background())
	if raw := w.store.raw(bindingsKey); strings.Contains(raw, "~pending") {
		t.Fatalf("an expired authorization stayed: %s", raw)
	}
}

// An app without the patch permission cannot get the redirect URL: the device flow (a QR code) authorizes instead;
// when that is not offered either, the owner is shown the redirect URL to paste and the app's settings page.
func TestLarkSyncAuthorizationFallsBack(t *testing.T) {
	w := newServiceWorld(t)
	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierDocs)
	w.ua.configure = larksyncwire.RedirectReport{MissingPatch: true, ConsoleURL: "https://open.example.com/app/cli_sync/safe"}
	code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/bsync/authorize", `{"redirect_uri":"`+testRedirect+`","tier":"full_with_im"}`)
	p, _ := out["prompt"].(map[string]any)
	if code != 200 || out["mode"] != modeDevice || p["kind"] != "qr" || p["qr"] == nil || p["user_code"] != "UC-1" {
		t.Fatalf("device: %d %v", code, out)
	}
	noSecretIn(t, "the answer", marshalJSON(t, out), "device_secret")
	noSecretIn(t, "bots.feishu.sync.bindings", w.store.raw(bindingsKey), "device_secret")
	if !strings.Contains(w.store.raw(bindingsKey), `"auth_device_enc":"`) {
		t.Fatal("the device code is not sealed")
	}
	w.ua.devicePoll = []larksyncwire.DevicePoll{{Status: larksyncwire.DeviceSlowDown}, {Status: larksyncwire.DevicePending},
		{Status: larksyncwire.DeviceDone, Token: larksyncwire.Token{AccessToken: "dev-access", RefreshToken: "dev-refresh",
			ExpiresAt: w.now().Add(2 * time.Hour), OpenID: "ou_dev", TenantKey: "tk2"}}}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/bsync/authorize/poll", ""); code != 200 || p["interval"] != float64(10) || p["done"] != nil {
		t.Fatalf("slow down: %d %v", code, p)
	}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/bsync/authorize/poll", ""); code != 200 || p["done"] != nil {
		t.Fatalf("pending: %d %v", code, p)
	}
	if code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/bsync/authorize/poll", ""); code != 200 || p["done"] != true || p["account"] != i18n.T(w.host.UserLang(), "hub-larksync.account_n", i18n.Args{"n": 1}) {
		t.Fatalf("done: %d %v", code, p)
	}
	_, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	b := view["bindings"].([]any)[0].(map[string]any)
	if b["bot_id"] != "bsync" || b["health"] != larksyncwire.HealthOK || b["tier"] != larksyncwire.TierFull {
		t.Fatalf("device binding: %v", b)
	}
	if bot := view["bots"].([]any)[1].(map[string]any); bot["approval"] != larksyncwire.ApprovalDegraded {
		t.Fatalf("the bot's app: %v", bot)
	}
	noSecretIn(t, "bots.feishu.sync.bindings", w.store.raw(bindingsKey), "dev-access", "dev-refresh", "device_secret")

	// No device flow either: the owner pastes the redirect URL; the browser URL works once they did.
	w.ua.deviceErr = errors.New("device flow not offered")
	code, out = w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`)
	if code != 200 || out["mode"] != modeManual || out["redirect_uri"] != testRedirect || out["console_url"] == "" ||
		!strings.Contains(out["url"].(string), "client_id=cli_primary") {
		t.Fatalf("manual: %d %v", code, out)
	}
}

// End-to-end through the real larkauth -> feishubot client: when the platform answers 99991672 (missing
// application:application:patch) or 99991676 (token no permission) on PATCH .../config, authorization tries the
// device flow first instead of jumping straight to manual redirect pasting, and falls back to manual only when
// the device endpoint itself is unavailable.
func TestLarkSyncAuthorizeMissingPatchScopeFallsBackToDeviceFlow(t *testing.T) {
	for _, errCode := range []int{99991672, 99991676} {
		t.Run(strconv.Itoa(errCode), func(t *testing.T) {
			var deviceOK atomic.Bool
			deviceOK.Store(true)
			var deviceHits, publishHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t-1", "expire": 7200})
				case "/open-apis/application/v7/applications/cli_primary/config":
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"code": errCode,
						"msg":  "Access denied. One of the following scopes is required: [application:application:patch]",
					})
				case "/open-apis/application/v7/applications/cli_primary/publish":
					publishHits.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
				case "/oauth/v1/device_authorization":
					deviceHits.Add(1)
					if !deviceOK.Load() {
						w.WriteHeader(http.StatusNotFound)
						_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "msg": "not found"})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"device_code":               "dc_1",
						"user_code":                 "UC-PATCH",
						"verification_uri_complete": "https://accounts.example.com/device?user_code=UC-PATCH",
						"expires_in":                600,
						"interval":                  5,
					})
				default:
					t.Errorf("unexpected platform request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			w := newServiceWorld(t)
			w.svc.userAuth = larkauth.NewUserAuth(srv.URL, srv.URL, srv.Client())

			code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`)
			p, _ := out["prompt"].(map[string]any)
			if code != 200 || out["mode"] != modeDevice || p["user_code"] != "UC-PATCH" {
				t.Fatalf("code %d: got %d %v, want mode=%s with device prompt", errCode, code, out, modeDevice)
			}
			if deviceHits.Load() != 1 || publishHits.Load() != 0 {
				t.Fatalf("code %d: deviceHits=%d publishHits=%d, want 1 and 0", errCode, deviceHits.Load(), publishHits.Load())
			}

			// When device authorization is not offered, it degrades to manual redirect setup.
			deviceOK.Store(false)
			code, out = w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`"}`)
			if code != 200 || out["mode"] != modeManual || out["redirect_uri"] != testRedirect ||
				out["console_url"] != botcard.DomainFeishu+"/app/cli_primary/safe" {
				t.Fatalf("code %d manual fallback: got %d %v", errCode, code, out)
			}
			if deviceHits.Load() != 2 || publishHits.Load() != 0 {
				t.Fatalf("code %d manual fallback: deviceHits=%d publishHits=%d, want 2 and 0", errCode, deviceHits.Load(), publishHits.Load())
			}
		})
	}
}

// Upgrading an existing docs_standard binding to full_with_im.
func TestLarkSyncUpgradeExistingBindingToFullWithIM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		botID string
		appID string
		sync  bool
	}{
		{name: "primary", botID: larksyncwire.PrimaryBot, appID: "cli_primary"},
		{name: "sync_only", botID: "bsync", appID: "cli_sync", sync: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newServiceWorld(t)
			if tc.sync {
				w.addSyncBot(t, tc.botID, tc.appID, larksyncwire.TierDocs)
			}
			if _, err := w.svc.change(context.Background(), func(st *syncState) error {
				if tc.botID == larksyncwire.PrimaryBot {
					st.prefs.PrimaryRedirect = testRedirect
				} else {
					st.bot(tc.botID).Redirect = testRedirect
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			key, _ := w.adopt(t, tc.botID, "ou_up", "acc-docs", "ref-docs", 2*time.Hour)
			_, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
			if got := view["bindings"].([]any)[0].(map[string]any)["tier"]; got != larksyncwire.TierDocs {
				t.Fatalf("before upgrade: binding tier = %v, want %s", got, larksyncwire.TierDocs)
			}

			// Step 1: add full_with_im scopes to the existing app via QR registration.
			code, p := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/"+tc.botID+"/scopes", `{"tier":"full_with_im"}`)
			if code != 200 || p["step"] != stepRegister || p["kind"] != "qr" {
				t.Fatalf("scopes begin: %d %v", code, p)
			}
			opts := w.reg.lastBegin(t)
			if opts.AppID != tc.appID || !slices.Equal(opts.UserScopes, larksyncwire.ScopesFor(larksyncwire.TierFull)) {
				t.Fatalf("scopes begin opts = %+v, want app %s with TierFull scopes", opts, tc.appID)
			}
			w.reg.polls = []botcard.RegisterTicket{{Status: botcard.RegisterDone, AppID: tc.appID}}
			if code, p = w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/"+tc.botID+"/register", `{"step":"register"}`); code != 200 || p["done"] != true {
				t.Fatalf("scopes poll: %d %v", code, p)
			}

			// Bot is now full_with_im, but the existing binding stays docs_standard until re-authorized.
			_, view = w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
			if got := view["bindings"].([]any)[0].(map[string]any)["tier"]; got != larksyncwire.TierDocs {
				t.Fatalf("after scopes before reauth: binding tier = %v, want %s", got, larksyncwire.TierDocs)
			}

			// Step 2: authorize again with full_with_im -> re-configures redirect/scopes and upgrades the binding.
			w.ua.configure = larksyncwire.RedirectReport{Configured: true, Published: true}
			code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/"+tc.botID+"/authorize", `{"redirect_uri":"`+testRedirect+`","tier":"full_with_im"}`)
			if code != 200 || out["mode"] != modeBrowser {
				t.Fatalf("authorize: %d %v", code, out)
			}
			if len(w.ua.configured) != 1 || w.ua.configured[0] != tc.appID+" "+testRedirect {
				t.Fatalf("configured after scopes upgrade = %v, want [%s %s]", w.ua.configured, tc.appID, testRedirect)
			}
			req := w.ua.authorized[len(w.ua.authorized)-1]
			if !slices.Equal(req.Scopes, larksyncwire.ScopesFor(larksyncwire.TierFull)) {
				t.Fatalf("authorize scopes = %v, want TierFull", req.Scopes)
			}

			w.ua.exchange["c-full"] = larksyncwire.Token{
				AccessToken: "acc-full", RefreshToken: "ref-full", ExpiresAt: w.now().Add(2 * time.Hour),
				OpenID: "ou_up", TenantKey: "t1", Name: "Account ou_up", Scopes: larksyncwire.ScopesFor(larksyncwire.TierFull),
			}
			code, view = w.do(t, "POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"`+req.State+`","code":"c-full"}`)
			if code != 200 {
				t.Fatalf("oauth: %d %v", code, view)
			}
			b := view["bindings"].([]any)[0].(map[string]any)
			if b["key"] != key || b["tier"] != larksyncwire.TierFull || b["health"] != larksyncwire.HealthOK {
				t.Fatalf("upgraded binding: %v", b)
			}
			if got := w.binding(t, key).Tier; got != larksyncwire.TierFull {
				t.Fatalf("stored binding tier = %q, want %s", got, larksyncwire.TierFull)
			}
		})
	}
}

// Twenty callers asking a binding's token while it ends soon: one refresh, every caller gets the new token, and it was
// saved (sealed) before any of them got it.
func TestLarkSyncRefreshIsSingleFlight(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc-1", "ref-1", 5*time.Minute)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.ua.refresh = func(_ larksyncwire.App, rt string) (larksyncwire.Token, error) {
		once.Do(func() { close(entered) })
		<-release
		return larksyncwire.Token{AccessToken: "acc-2", RefreshToken: "ref-2", ExpiresAt: w.now().Add(2 * time.Hour)}, nil
	}
	shared, _ := w.svc.tokenFunc(key)
	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := range 20 {
		token := shared
		if i%2 == 1 {
			token, _ = w.svc.tokenFunc(key)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := token(ctx)
			if err != nil || tok != "acc-2" {
				errs <- "token: " + tok
				return
			}
			b := w.binding(t, key)
			if got, _ := w.svc.open(b.AccessTokenEnc); got != "acc-2" {
				errs <- "not saved yet: " + got
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if n := w.ua.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes: %d", n)
	}
	b := w.binding(t, key)
	access, _ := w.svc.open(b.AccessTokenEnc)
	refresh, _ := w.svc.open(b.RefreshTokenEnc)
	if access != "acc-2" || refresh != "ref-2" || !b.TokenExpiresAt.Equal(w.now().Add(2*time.Hour)) {
		t.Fatalf("saved: %s %s %v", access, refresh, b.TokenExpiresAt)
	}
	noSecretIn(t, "bots.feishu.sync.bindings", w.store.raw(bindingsKey), "acc-2", "ref-2")

	for range 50 {
		if tok, err := shared(ctx); err != nil || tok != "acc-2" {
			t.Fatalf("asked again: %q %v", tok, err)
		}
	}
	if n := w.ua.refreshes.Load(); n != 1 {
		t.Fatalf("a good token was refreshed: %d refreshes", n)
	}
}

// A refused refresh token leaves the binding needs_reauth: it is not refreshed again, not run, and the view says so,
// until the owner authorizes again.
func TestLarkSyncInvalidGrantNeedsReauth(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc-1", "ref-1", time.Minute)
	w.ua.refresh = func(larksyncwire.App, string) (larksyncwire.Token, error) {
		return larksyncwire.Token{}, &larksyncwire.Error{Kind: larksyncwire.ErrKindInvalidGrant, Op: "refresh", Code: 20037, Msg: "expired"}
	}
	token, _ := w.svc.tokenFunc(key)
	for range 3 {
		if _, err := token(ctx); errorKindOf(err) != larksyncwire.ErrKindInvalidGrant {
			t.Fatalf("token: %v", err)
		}
	}
	if n := w.ua.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes: %d, want 1 (no retry storm)", n)
	}
	b := w.binding(t, key)
	if b.Health != larksyncwire.HealthNeedsReauth || isDue(b, w.now().Add(72*time.Hour)) {
		t.Fatalf("binding: %+v", b)
	}
	_, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	if h := view["bindings"].([]any)[0].(map[string]any)["health"]; h != larksyncwire.HealthNeedsReauth {
		t.Fatalf("view health %v", h)
	}
	// Authorizing again heals it.
	w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc-3", "ref-3", 2*time.Hour)
	if b := w.binding(t, key); b.Health != larksyncwire.HealthOK || !isDue(b, w.now()) {
		t.Fatalf("authorized again: %+v", b)
	}
}

// A token refused part way through a run (revoked before it ran out): the run refreshes once and reads again; refused
// again, the binding needs authorizing again.
func TestLarkSyncRunRefreshesOnARefusedToken(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, acc := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc-1", "ref-1", 2*time.Hour)
	var seen []string
	refuse := "acc-1"
	w.script(larksyncwire.PrimaryBot, acc, func(ctx context.Context, _ larksyncwire.Binding, token func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		tok, err := token(ctx)
		if err != nil {
			return larksyncwire.Result{}, err
		}
		seen = append(seen, tok)
		if tok == refuse {
			return larksyncwire.Result{Cursors: map[string]string{"drive": "1"}}, &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "drive", Code: 99991663}
		}
		return larksyncwire.Result{Cursors: map[string]string{"drive": "2"}}, nil
	})
	if err := w.svc.runSync(ctx, key); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1] != "acc-ref-1+" || w.ua.refreshes.Load() != 1 {
		t.Fatalf("tokens %v, refreshes %d", seen, w.ua.refreshes.Load())
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthOK || w.store.cursorsOf(key)["drive"] != "2" {
		t.Fatalf("after the refresh: %+v %v", b, w.store.cursorsOf(key))
	}
	// Refused even after a good refresh: authorize again.
	w.script(larksyncwire.PrimaryBot, acc, func(ctx context.Context, _ larksyncwire.Binding, token func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		if _, err := token(ctx); err != nil {
			return larksyncwire.Result{}, err
		}
		return larksyncwire.Result{}, &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "im", Code: 99991663}
	})
	if err := w.svc.runSync(ctx, key); errorKindOf(err) != larksyncwire.ErrKindAuth {
		t.Fatalf("run: %v", err)
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthNeedsReauth || w.ua.refreshes.Load() != 2 || w.store.cursorsOf(key)["drive"] != "2" {
		t.Fatalf("refused twice: %+v, %d refreshes", b, w.ua.refreshes.Load())
	}
}

// The return page is the web app's /lark-oauth, at the root or under a reverse proxy's path prefix; nothing else.
func TestLarkSyncRedirectRule(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://hub.example/lark-oauth":         true,
		"http://192.0.2.1:8080/lark-oauth":       true,
		"https://hub.example/persona/lark-oauth": true,
		"https://hub.example/lark-oauth/":        false,
		"https://hub.example/elsewhere":          false,
		"https://hub.example/lark-oauth?x=1":     false,
		"https://hub.example/lark-oauth#x":       false,
		"https://user@hub.example/lark-oauth":    false,
		"https://hub.example/a/../lark-oauth":    false,
		"ftp://hub.example/lark-oauth":           false,
		"/lark-oauth":                            false,
	} {
		if got := validRedirect(raw); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
}

func TestLarkSyncReauthorizationReconfiguresMissingScopesWithSameRedirect(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_owner", "example-access", "example-refresh", 24*time.Hour)
	w.ua.configure = larksyncwire.RedirectReport{Configured: true, Published: true}
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.prefs.PrimaryRedirect = testRedirect
		b := st.binding(key)
		b.Health, b.Tier = larksyncwire.HealthScopeMissing, larksyncwire.TierFull
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"`+testRedirect+`","tier":"full_with_im"}`)
	if code != 200 || out["mode"] != modeBrowser {
		t.Fatalf("authorize: status=%d mode=%v", code, out["mode"])
	}
	if !slices.Equal(w.ua.configured, []string{"cli_primary " + testRedirect}) {
		t.Fatal("missing scopes did not reconfigure an already known redirect")
	}
	if !slices.Equal(w.ua.authorized[0].Scopes, larksyncwire.ScopesFor(larksyncwire.TierFull)) {
		t.Fatal("reauthorization did not request the expanded full scope pack")
	}
}
