package usagesource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "ag-test-key-0123456789"

var now = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

// fakeGateway is a relay as far as this adapter sees it: the account list, the stored usage, and a log of every
// request so that a test can say nothing else was called.
type fakeGateway struct {
	mu       sync.Mutex
	calls    []string // "METHOD path"
	refresh  []bool
	accounts string // the body of GET /accounts
	usage    func(ids []string) (int, string)
	key      string
	srv      *httptest.Server
}

func newGateway(t *testing.T, accounts string, usage func(ids []string) (int, string)) *fakeGateway {
	g := &fakeGateway{accounts: accounts, usage: usage, key: testKey}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
		http.NotFound(w, r)
		return
	}
	g.mu.Lock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path)
	g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+g.key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key `+r.Header.Get("Authorization")+`"}}`)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/admin/accounts" && r.URL.Query().Get("view") == "compact":
		_, _ = io.WriteString(w, g.accounts)
	case r.Method == "POST" && r.URL.Path == "/api/admin/accounts/usage":
		var in struct {
			IDs     []string `json:"ids"`
			Refresh bool     `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		g.refresh = append(g.refresh, in.Refresh)
		g.mu.Unlock()
		status, body := g.usage(in.IDs)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (g *fakeGateway) cfg() Config {
	return Config{Type: TypeRelay, URL: g.srv.URL + "/", Key: g.key}
}

const twoAccounts = `{"accounts":[
 {"id":"c1","name":"work codex","provider":"codex","status":"active"},
 {"id":"a1","name":"claude seat","provider":"claude","status":"cooldown"},
 {"id":"d1","name":"switched off","provider":"codex","status":"disabled"},
 {"id":"k1","name":"api pool","provider":"openrouter","status":"active"}]}`

const twoUsage = `{"accounts":[
 {"account_id":"c1","plan":"pro","plan_display_name":"ChatGPT Pro","checked_at":"2026-11-05T08:58:00Z","cached":true,"windows":[
   {"account_id":"c1","kind":"five_hour","scope":"account","used_ratio":0.25,"resets_at":"2026-11-05T12:00:00Z","source":"header","checked_at":"2026-11-05T08:58:00Z"},
   {"account_id":"c1","kind":"weekly","used_ratio":0.5,"resets_at":"2026-11-09T00:00:00Z","source":"header","checked_at":"2026-11-05T08:50:00Z"},
   {"account_id":"c1","kind":"credits","remaining":0,"source":"usage","checked_at":"2026-11-05T08:50:00Z"}]},
 {"account_id":"a1","plan":"max","checked_at":"2026-11-05T07:00:00Z","stale":true,"error":"upstream said something with the key","windows":[
   {"account_id":"a1","kind":"five_hour","used_ratio":0.9,"resets_at":"2026-11-05T11:00:00Z","source":"oauth_usage","checked_at":"2026-11-05T07:00:00Z"},
   {"account_id":"a1","kind":"weekly","used_ratio":0.4,"resets_at":"2026-11-08T00:00:00Z","source":"oauth_usage","checked_at":"2026-11-05T07:00:00Z"},
   {"account_id":"a1","kind":"weekly_fable","scope":"model","scoped_model":"Fable","used_ratio":1,"resets_at":"2026-11-08T00:00:00Z","source":"header","checked_at":"2026-11-05T07:00:00Z"}]},
 {"account_id":"k1","unsupported":true,"windows":[]}]}`

func echoUsage(body string) func([]string) (int, string) {
	return func([]string) (int, string) { return 200, body }
}

func TestRelayMapsAccountsAndWindows(t *testing.T) {
	g := newGateway(t, twoAccounts, echoUsage(twoUsage))
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accounts) != 2 {
		t.Fatalf("accounts: %+v", res.Accounts)
	}
	claude, codex := res.Accounts[0], res.Accounts[1] // sorted by ref: a1, c1
	if codex.Provider != "codex" || codex.Name != "work codex" || codex.Plan != "ChatGPT Pro" || codex.Ref != "c1" {
		t.Errorf("codex: %+v", codex)
	}
	if len(codex.Windows) != 2 || codex.Windows[0].Label != "5h" || codex.Windows[0].UsedPercent != 25 || codex.Windows[1].Label != "7d" ||
		codex.Windows[1].WindowMinutes != 10080 || codex.Windows[1].UsedPercent != 50 {
		t.Errorf("codex windows: %+v", codex.Windows)
	}
	if want := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC); codex.Windows[0].ResetsAt == nil || !codex.Windows[0].ResetsAt.Equal(want) {
		t.Errorf("reset: %v", codex.Windows[0].ResetsAt)
	}
	// the sample time is the newest check, not the time we asked
	if want := time.Date(2026, 11, 5, 8, 58, 0, 0, time.UTC); !codex.SampledAt.Equal(want) {
		t.Errorf("sampled %v want %v", codex.SampledAt, want)
	}
	if codex.Stale || codex.Problem != "" {
		t.Errorf("codex should be fresh: %+v", codex)
	}
	// claude: stale and errored, scoped window kept separate and labelled by model
	if !claude.Stale || claude.Problem != "error" || claude.Plan != "max" {
		t.Errorf("claude flags: %+v", claude)
	}
	if len(claude.Windows) != 3 || claude.Windows[2].Model != "Fable" || claude.Windows[2].Label != "7d" || claude.Windows[1].Model != "" {
		t.Errorf("claude windows: %+v", claude.Windows)
	}
	if res.Seen.Listed != 4 || res.Seen.Accounts != 2 || res.Seen.Windows != 5 || res.Seen.Providers["codex"] != 1 || res.Seen.Providers["claude"] != 1 {
		t.Errorf("seen: %+v", res.Seen)
	}
}

func TestRelayOnlyReadsAndNeverRefreshes(t *testing.T) {
	g := newGateway(t, twoAccounts, echoUsage(twoUsage))
	if _, err := Fetch(context.Background(), g.cfg(), now); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/admin/accounts", "POST /api/admin/accounts/usage"}
	if fmt.Sprint(g.calls) != fmt.Sprint(want) {
		t.Errorf("calls: %v", g.calls)
	}
	for _, r := range g.refresh {
		if r {
			t.Error("refresh=true was sent")
		}
	}
}

func TestRelayAsksForTheAccountsOfTheListOnly(t *testing.T) {
	var asked []string
	g := newGateway(t, twoAccounts, func(ids []string) (int, string) { asked = ids; return 200, `{"accounts":[]}` })
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil || len(res.Accounts) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if fmt.Sprint(asked) != "[c1 a1 k1]" { // the disabled one is not asked about
		t.Errorf("asked %v", asked)
	}
}

func TestRelayBatchesAndCaps(t *testing.T) {
	var list []string
	for i := 0; i < 650; i++ {
		list = append(list, fmt.Sprintf(`{"id":"id%d","name":"n","provider":"codex","status":"active"}`, i))
	}
	var sizes []int
	g := newGateway(t, `{"accounts":[`+strings.Join(list, ",")+`]}`, func(ids []string) (int, string) {
		sizes = append(sizes, len(ids))
		return 200, `{"accounts":[]}`
	})
	if _, err := Fetch(context.Background(), g.cfg(), now); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[100 100 100 100 100]" {
		t.Errorf("batches %v", sizes)
	}
}

func TestRelayOldGatewayReturnsABareArray(t *testing.T) {
	g := newGateway(t, `[{"id":"c1","name":"x","provider":"codex","status":"active","proxy_url":"socks5://secret","email":"a@example.com"}]`, echoUsage(twoUsage))
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil || len(res.Accounts) != 1 || res.Accounts[0].Name != "x" {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "socks5") || strings.Contains(string(b), "example.com") {
		t.Errorf("fields we do not use leaked: %s", b)
	}
}

func TestRelayWindowFromLimitAndRemaining(t *testing.T) {
	g := newGateway(t, `{"accounts":[{"id":"c1","name":"x","provider":"codex","status":"active"}]}`, echoUsage(`{"accounts":[{"account_id":"c1","windows":[
	  {"kind":"monthly","remaining":25,"limit":100,"checked_at":"2026-11-05T08:00:00Z"},
	  {"kind":"daily","checked_at":"2026-11-05T08:00:00Z"}]}]}`))
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	w := res.Accounts[0].Windows
	if len(w) != 1 || w[0].Label != "30d" || w[0].UsedPercent != 75 { // no number, no window
		t.Errorf("%+v", w)
	}
}

func TestRelayErrors(t *testing.T) {
	cases := []struct {
		name string
		mod  func(g *fakeGateway)
		code string
	}{
		{"wrong key", func(g *fakeGateway) { g.key = "other" }, CodeUnauthorized},
		{"scoped key without the route", func(g *fakeGateway) {
			g.usage = func([]string) (int, string) { return 403, `{}` }
		}, CodeForbidden},
		{"scoped key, foreign id", func(g *fakeGateway) {
			g.usage = func([]string) (int, string) { return 404, `{"error":{"code":"not_found"}}` }
		}, CodeNotFound},
		{"gateway error", func(g *fakeGateway) { g.usage = func([]string) (int, string) { return 503, `` } }, CodeServer},
		{"not json", func(g *fakeGateway) { g.usage = func([]string) (int, string) { return 200, `<html>` } }, CodeBadResponse},
		{"redirect", func(g *fakeGateway) {
			g.usage = func([]string) (int, string) { return 302, `` }
		}, CodeRedirect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateway(t, twoAccounts, echoUsage(twoUsage))
			tc.mod(g)
			cfg := g.cfg()
			cfg.Key = testKey
			_, err := Fetch(context.Background(), cfg, now)
			if CodeOf(err) != tc.code {
				t.Fatalf("code %q (%v), want %q", CodeOf(err), err, tc.code)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the key is in the error: %v", err)
			}
		})
	}
}

func TestUnreachableAndTimeout(t *testing.T) {
	g := newGateway(t, twoAccounts, echoUsage(twoUsage))
	cfg := g.cfg()
	g.srv.Close()
	if _, err := Fetch(context.Background(), cfg, now); CodeOf(err) != CodeUnreachable {
		t.Errorf("closed: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Fetch(ctx, Config{Type: TypeRelay, URL: slow.URL, Key: testKey}, now); CodeOf(err) != CodeTimeout {
		t.Errorf("slow: %v", err)
	}
}

func TestOversizedAnswerIsRefused(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[`)
		chunk := strings.Repeat(" ", 1<<20)
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, chunk)
		}
		_, _ = io.WriteString(w, `]}`)
	}))
	defer big.Close()
	if _, err := Fetch(context.Background(), Config{Type: TypeRelay, URL: big.URL, Key: testKey}, now); CodeOf(err) != CodeTooLarge {
		t.Errorf("%v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		" https://gw.example.com/ ":         "https://gw.example.com",
		"http://127.0.0.1:8080/base/?x=1#f": "http://127.0.0.1:8080/base",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("%q → %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "gw.example.com", "ftp://gw.example.com", "https://user:pw@gw.example.com", "https://", "javascript:alert(1)"} {
		if _, err := NormalizeURL(bad); CodeOf(err) != CodeBadConfig {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKindMinutes(t *testing.T) {
	for kind, want := range map[string]int{
		"five_hour": 300, "5h": 300, "weekly": 10080, "7d": 10080, "seven_day": 10080, "hourly": 60, "daily": 1440, "monthly": 43200,
		"weekly_fable": 10080, "gemini_argon_weekly": 10080, "45m": 45, "2w": 20160, "Five-Hour": 300,
	} {
		if got, ok := KindMinutes(kind); !ok || got != want {
			t.Errorf("%q → %d %v, want %d", kind, got, ok, want)
		}
	}
	for _, kind := range []string{"credits", "blocked", "", "gemini", "0h", "weird"} {
		if got, ok := KindMinutes(kind); ok {
			t.Errorf("%q → %d, should not be a window", kind, got)
		}
	}
}

func TestScopedModelFromTheKindWhenTheGatewayDoesNotSayIt(t *testing.T) {
	g := newGateway(t, `{"accounts":[{"id":"g1","name":"x","provider":"antigravity","status":"active"}]}`, echoUsage(`{"accounts":[{"account_id":"g1","windows":[
	  {"kind":"gemini_argon_weekly","scope":"model","used_ratio":0.1,"checked_at":"2026-11-05T08:00:00Z"}]}]}`))
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil || len(res.Accounts) != 1 || res.Accounts[0].Windows[0].Model != "gemini argon" {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
}

func TestGenericSource(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"accounts":[
		 {"id":"s1","provider":"Codex","name":"seat 1","plan":"Pro","sampled_at":"2026-11-05T08:30:00Z","windows":[
		   {"label":"7d","used_percent":40,"resets_at":"2026-11-09T00:00:00Z"},
		   {"label":"5h","window_minutes":300,"used_percent":130},
		   {"window_minutes":90,"used_percent":5},
		   {"label":"?","used_percent":5},
		   {"label":"1d"}]},
		 {"provider":"","windows":[{"label":"5h","used_percent":1}]},
		 {"provider":"kimi","windows":[]}]}`)
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Config{Type: TypeGeneric, URL: srv.URL + "/persona/usage", Key: testKey}, now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	a := res.Accounts[0]
	if auth != "Bearer "+testKey || a.Provider != "codex" || a.Ref != "s1" || len(a.Windows) != 3 {
		t.Fatalf("%q %+v", auth, a)
	}
	if a.Windows[0].WindowMinutes != 90 || a.Windows[1].UsedPercent != 100 || a.Windows[2].Label != "7d" {
		t.Errorf("%+v", a.Windows)
	}
}

// The generic reader preserves reset information for the manager's expiry and sibling checks.
func TestGenericAntigravityResetForms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[
			{"id":"claude","provider":"antigravity","name":"Antigravity · Claude / GPT","windows":[
				{"label":"5h","used_percent":0},
				{"label":"7d","used_percent":24,"resets_at":"2026-11-12T09:00:00.123Z"}]},
			{"id":"gemini","provider":"antigravity","name":"Antigravity · Gemini","windows":[
				{"label":"5h","used_percent":0,"resets_at":"1970-01-01T00:00:00Z"}]}]}`)
	}))
	defer srv.Close()
	res, err := Fetch(t.Context(), Config{Type: TypeGeneric, URL: srv.URL, Key: testKey}, now)
	if err != nil || len(res.Accounts) != 2 {
		t.Fatalf("generic poll: %+v, %v", res, err)
	}
	c, g := res.Accounts[0], res.Accounts[1]
	if c.Pass || c.Ref != "claude" || c.Name != "Antigravity · Claude / GPT" || len(c.Windows) != 2 || len(g.Windows) != 1 {
		t.Fatalf("generic seats: %+v", res.Accounts)
	}
	idle, timed, epoch := c.Windows[0], c.Windows[1], g.Windows[0]
	if idle.Model != "" || idle.WindowMinutes != 300 || idle.UsedPercent != 0 || idle.ResetsAt != nil ||
		timed.WindowMinutes != 10080 || timed.ResetsAt == nil || !timed.ResetsAt.Equal(now.Add(7*24*time.Hour+123*time.Millisecond)) ||
		epoch.ResetsAt == nil || !epoch.ResetsAt.Equal(time.Unix(0, 0)) {
		t.Fatalf("generic reset values: %+v, %+v", c.Windows, g.Windows)
	}
}

func TestGenericSourceKeepsTheFirstAccountOfARepeatedID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[
		 {"id":"acct-1","provider":"codex","name":"first","windows":[{"label":"5h","used_percent":10}]},
		 {"id":" acct-1 ","provider":"codex","name":"repeat","windows":[{"label":"5h","used_percent":90},{"label":"7d","used_percent":50}]},
		 {"provider":"claude","name":"no id","windows":[{"label":"5h","used_percent":20}]}]}`)
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Config{Type: TypeGeneric, URL: srv.URL, Key: testKey}, now)
	if err != nil || len(res.Accounts) != 2 {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	if a := res.Accounts[0]; a.Ref != "acct-1" || a.Name != "first" || len(a.Windows) != 1 || a.Windows[0].UsedPercent != 10 {
		t.Errorf("first acct-1 not kept: %+v", a)
	}
	if b := res.Accounts[1]; b.Ref != "" || b.Provider != "claude" {
		t.Errorf("the account without an id: %+v", b)
	}
	if s := res.Seen; s.Listed != 2 || s.Accounts != 2 || s.Windows != 2 || s.Providers["codex"] != 1 || s.Providers["claude"] != 1 {
		t.Errorf("seen %+v", s)
	}
}

func TestGenericSourceCapsKeptWindowsNotRawEntries(t *testing.T) {
	var rawWindows []string
	for i := 0; i < 20; i++ {
		rawWindows = append(rawWindows, `{"label":"5h"}`)
	}
	for i := 0; i < 10; i++ {
		rawWindows = append(rawWindows, `{"label":"credits","used_percent":0}`)
	}
	for i := 0; i < 5; i++ {
		rawWindows = append(rawWindows, `{"label":"5h","used_percent":20}`)
	}
	rawWindows = append(rawWindows, `{"label":"7d","used_percent":55}`)
	for i := 1; i <= 35; i++ {
		rawWindows = append(rawWindows, fmt.Sprintf(`{"label":"7d","model":"m%02d","used_percent":10}`, i))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[{"id":"acct-1","provider":"codex","name":"seat","windows":[`+strings.Join(rawWindows, ",")+`]}]}`)
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Config{Type: TypeGeneric, URL: srv.URL, Key: testKey}, now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	got := res.Accounts[0].Windows
	if len(got) != WindowsMost || res.Seen.Windows != WindowsMost {
		t.Fatalf("windows = %d (seen %d), want %d", len(got), res.Seen.Windows, WindowsMost)
	}
	if got[0].Label != "5h" || got[0].UsedPercent != 20 || got[1].Label != "7d" || got[1].Model != "" || got[1].UsedPercent != 55 {
		t.Fatalf("first windows = %+v", got[:2])
	}
}

func TestUnknownTypeAndBadAddress(t *testing.T) {
	if _, err := Fetch(context.Background(), Config{Type: "nope", URL: "https://gw.example.com"}, now); CodeOf(err) != CodeBadConfig {
		t.Error(err)
	}
	if _, err := Fetch(context.Background(), Config{Type: TypeRelay, URL: "https://u:p@gw.example.com"}, now); CodeOf(err) != CodeBadConfig {
		t.Error(err)
	}
}

// TestMapRelayCapsWindowsAndClips: the relay mapping keeps at most WindowsMost windows and clips the model and the
// account reference, as the generic source and the inventory do, so an oversized gateway answer cannot flood the
// merged usage list every client gets.
func TestMapRelayCapsWindowsAndClips(t *testing.T) {
	ratio := 0.5
	var windows []relayWindow
	for i := 0; i < 40; i++ {
		model := fmt.Sprintf("m%02d-", i) + strings.Repeat("模", 200)
		windows = append(windows, relayWindow{Kind: "weekly", Scope: "model", ScopedModel: model, UsedRatio: &ratio})
	}
	a := relayAccount{ID: strings.Repeat("账", 300), Provider: "claude", Name: "n"}
	acct, n, ok := mapRelay(a, relayEntry{Windows: windows}, now)
	if !ok {
		t.Fatal("no account")
	}
	if n != WindowsMost || len(acct.Windows) != WindowsMost {
		t.Errorf("windows = %d (%d), want %d", len(acct.Windows), n, WindowsMost)
	}
	for _, w := range acct.Windows {
		if c := len([]rune(w.Model)); c > 40 {
			t.Fatalf("model has %d runes", c)
		}
	}
	if c := len([]rune(acct.Ref)); c > 128 {
		t.Errorf("ref has %d runes", c)
	}
}
