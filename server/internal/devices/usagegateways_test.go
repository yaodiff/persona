package devices

import (
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

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/usagesource"
)

func gw(source, ref, provider, name string, sampled time.Time, wins ...proto.UsageWindow) proto.UsageAccount {
	a := proto.UsageAccount{Provider: provider, Name: name, SampledAt: sampled, Windows: wins, Sources: []string{source}}
	if ref != "" {
		a.Ref = "s1:" + ref
	}
	return a
}

func local(provider, name string, sampled time.Time, wins ...proto.UsageWindow) proto.UsageAccount {
	return proto.UsageAccount{Provider: provider, Name: name, SampledAt: sampled, Windows: wins, Local: true}
}

func TestMergeLocalAndGatewayAreOneAccountWhenTheResetsAgree(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h5, d7 := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	in := []proto.UsageAccount{
		local("claude", "Claude Max", now.Add(-time.Minute), uw("5h", 300, 31, h5), uw("7d", 10080, 41, d7)),
		gw("Relay", "c1", "claude", "work seat", now.Add(-10*time.Minute), uw("5h", 300, 30, h5), uw("7d", 10080, 40, d7)),
		// another account of the gateway: other resets
		gw("Relay", "c2", "claude", "spare seat", now.Add(-time.Hour), uw("5h", 300, 2, now.Add(4*time.Hour)), uw("7d", 10080, 3, now.Add(5*24*time.Hour))),
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 {
		t.Fatalf("want 2 accounts: %+v", got)
	}
	var merged, spare *proto.UsageAccount
	for i := range got {
		switch got[i].Name {
		case "work seat":
			merged = &got[i]
		case "spare seat":
			spare = &got[i]
		}
	}
	if merged == nil || spare == nil {
		t.Fatalf("names: %+v", got)
	}
	// the gateway's name wins, the newest numbers (the machine's) win, both sources are listed
	if !merged.Local || len(merged.Sources) != 1 || merged.Sources[0] != "Relay" || merged.Windows[0].UsedPercent != 31 {
		t.Errorf("merged: %+v", merged)
	}
	if spare.Local || len(spare.Sources) != 1 {
		t.Errorf("spare: %+v", spare)
	}
}

func TestMergeKeepsTwoAccountsOfOneGatewayApartWhateverTheirResets(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		gw("Relay", "a", "claude", "seat a", now, uw("7d", 10080, 10, reset)),
		gw("Relay", "b", "claude", "seat b", now, uw("7d", 10080, 20, reset)),
	}
	if got := mergeUsageAccounts(in, now); len(got) != 2 {
		t.Fatalf("a gateway knows its accounts apart: %+v", got)
	}
	// but the same upstream account behind two gateways (refs of different sources) is one
	other := gw("Other", "z", "claude", "seat z", now, uw("7d", 10080, 11, reset))
	other.Ref = "s2:z"
	if got := mergeUsageAccounts([]proto.UsageAccount{in[0], other}, now); len(got) != 1 || len(got[0].Sources) != 2 {
		t.Fatalf("two gateways: %+v", got)
	}
}

func TestMergeScopedWindowsAreKeptApartAndNeverIdentifyAnAccount(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(3 * 24 * time.Hour)
	scoped := uw("7d", 10080, 100, reset)
	scoped.Model = "Fable"
	in := []proto.UsageAccount{
		gw("Relay", "a", "claude", "seat a", now, uw("5h", 300, 5, now.Add(time.Hour)), uw("7d", 10080, 10, reset), scoped),
		// another account whose only window shares the scoped one's reset second: not the same account
		gw("Relay", "b", "claude", "seat b", now, uw("7d", 10080, 50, reset.Add(time.Hour)), scoped),
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, a := range got {
		if a.Name == "seat a" && (len(a.Windows) != 3 || a.Windows[2].Model != "Fable" || a.Windows[1].Model != "") {
			t.Errorf("windows: %+v", a.Windows)
		}
	}
	if id := usageAccountID(got[0]); !strings.HasPrefix(id, "claude-") {
		t.Errorf("id %q", id)
	}
}

func TestMergeGatewayStaleness(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(24 * time.Hour)
	idle := gw("Relay", "a", "codex", "idle seat", now.Add(-2*time.Hour), uw("7d", 10080, 10, reset)) // older than a machine's 30 minutes, within a gateway's
	old := gw("Relay", "b", "codex", "old seat", now.Add(-usageGatewayStaleAfter-time.Minute), uw("7d", 10080, 20, now.Add(48*time.Hour)))
	flagged := gw("Relay", "c", "codex", "flagged seat", now, uw("7d", 10080, 30, now.Add(72*time.Hour)))
	flagged.Stale, flagged.Problem = true, "error"
	machine := local("codex", "Codex", now.Add(-2*time.Hour), uw("7d", 10080, 40, now.Add(96*time.Hour)))
	got := map[string]proto.UsageAccount{}
	for _, a := range mergeUsageAccounts([]proto.UsageAccount{idle, old, flagged, machine}, now) {
		got[a.Name] = a
	}
	if got["idle seat"].Stale || !got["old seat"].Stale || !got["flagged seat"].Stale || got["flagged seat"].Problem != "error" || !got["Codex"].Stale {
		t.Errorf("%+v", got)
	}
}

func TestGatewaysPollKeepLastAnswerWhenUnreachableAndMarkItStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var fail atomic.Bool
	var calls atomic.Int32
	var g usageGateways
	g.fetch = func(_ context.Context, cfg usagesource.Config, at time.Time) (usagesource.Result, error) {
		calls.Add(1)
		if cfg.Key != "secret-key" || cfg.Type != "relay" {
			t.Errorf("config: %+v", cfg)
		}
		if fail.Load() {
			return usagesource.Result{}, &usagesource.Error{Code: usagesource.CodeUnreachable}
		}
		a := proto.UsageAccount{Provider: "codex", Name: "seat", SampledAt: at, Ref: "c1", Windows: []proto.UsageWindow{uw("7d", 10080, 9, at.Add(time.Hour))}}
		return usagesource.Result{Accounts: []proto.UsageAccount{a}, Seen: usagesource.Seen{Accounts: 1, Windows: 1, Providers: map[string]int{"codex": 1}}}, nil
	}
	g.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Relay", Type: "relay", URL: "https://gw.example.com", Key: "secret-key"}}}, func() {}, func(string, ...any) {})

	g.pollDue(context.Background(), now)
	got := g.accounts(now)
	if len(got) != 1 || got[0].Sources[0] != "Relay" || got[0].Ref != "s1:c1" || got[0].Stale || got[0].Problem != "" {
		t.Fatalf("%+v", got)
	}
	// not due again within the interval
	g.pollDue(context.Background(), now.Add(time.Minute))
	if calls.Load() != 1 {
		t.Errorf("asked %d times", calls.Load())
	}
	// due after 5 minutes; the gateway is down: the last answer stays, stale and marked
	fail.Store(true)
	g.pollDue(context.Background(), now.Add(6*time.Minute))
	got = g.accounts(now.Add(6 * time.Minute))
	if calls.Load() != 2 || len(got) != 1 || !got[0].Stale || got[0].Problem != "unreachable" {
		t.Fatalf("%d %+v", calls.Load(), got)
	}
	if st := g.status("s1"); st.Error != usagesource.CodeUnreachable || st.OKAt == nil || st.Seen == nil || st.Seen.Accounts != 1 {
		t.Errorf("status %+v", st)
	}
	// and it comes back
	fail.Store(false)
	g.pollDue(context.Background(), now.Add(12*time.Minute))
	if got = g.accounts(now.Add(12 * time.Minute)); len(got) != 1 || got[0].Stale || got[0].Problem != "" {
		t.Fatalf("%+v", got)
	}
}

// A failed read says why the numbers are old (#1902): a key the gateway turned away is not a network problem, and the
// admin page's reason and the bars' agree. The source failing outranks a relay's own per-account "error".
func TestGatewaysFailedReadSaysWhy(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for code, want := range map[string]string{
		usagesource.CodeUnauthorized: "rejected",
		usagesource.CodeForbidden:    "rejected",
		usagesource.CodeUnreachable:  "unreachable",
		usagesource.CodeTimeout:      "unreachable",
		usagesource.CodeNotFound:     "failed",
		usagesource.CodeServer:       "failed",
		usagesource.CodeBadResponse:  "failed",
		usagesource.CodeTooLarge:     "failed",
		usagesource.CodeRedirect:     "failed",
		usagesource.CodeBadConfig:    "failed",
	} {
		var fail atomic.Bool
		var g usageGateways
		g.fetch = func(_ context.Context, _ usagesource.Config, at time.Time) (usagesource.Result, error) {
			if fail.Load() {
				return usagesource.Result{}, &usagesource.Error{Code: code, Status: 401}
			}
			a := proto.UsageAccount{Provider: "codex", Name: "seat", SampledAt: at, Ref: "c1", Problem: "error", Windows: []proto.UsageWindow{uw("7d", 10080, 9, at.Add(time.Hour))}}
			return usagesource.Result{Accounts: []proto.UsageAccount{a}}, nil
		}
		g.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Relay", Type: "relay", URL: "https://gw.example.com", Key: "k"}}}, func() {}, func(string, ...any) {})
		g.pollDue(context.Background(), now)
		if got := g.accounts(now); len(got) != 1 || got[0].Problem != "error" {
			t.Fatalf("%s: before the failure %+v", code, got)
		}
		fail.Store(true)
		g.pollDue(context.Background(), now.Add(6*time.Minute))
		got := g.accounts(now.Add(6 * time.Minute))
		if len(got) != 1 || !got[0].Stale || got[0].Problem != want {
			t.Errorf("%s: want %q, got %+v", code, want, got)
		}
		if st := g.status("s1"); st.Error != code {
			t.Errorf("%s: status %+v", code, st)
		}
	}
}

func TestGatewaysOffSwitchesAndEdits(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var calls atomic.Int32
	var g usageGateways
	g.fetch = func(_ context.Context, cfg usagesource.Config, at time.Time) (usagesource.Result, error) {
		calls.Add(1)
		return usagesource.Result{Accounts: []proto.UsageAccount{{Provider: "codex", Name: cfg.URL, SampledAt: at, Windows: []proto.UsageWindow{uw("7d", 10080, 1, at.Add(time.Hour))}}}}, nil
	}
	off := false
	src := UsageSourceConfig{ID: "s1", Name: "G", Type: "generic", URL: "https://one.example.com"}
	g.init(UsageConfig{Enabled: &off, Sources: []UsageSourceConfig{src}}, func() {}, nil)
	g.pollDue(context.Background(), now)
	if calls.Load() != 0 || len(g.accounts(now)) != 0 {
		t.Fatal("the global off switch still asked")
	}
	g.setConfig(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "G", Type: "generic", URL: "https://one.example.com", Enabled: &off}}})
	g.pollDue(context.Background(), now)
	if calls.Load() != 0 {
		t.Fatal("a source that is off was asked")
	}
	g.setConfig(UsageConfig{Sources: []UsageSourceConfig{src}})
	g.pollDue(context.Background(), now)
	if calls.Load() != 1 || len(g.accounts(now)) != 1 {
		t.Fatalf("%d", calls.Load())
	}
	// renaming keeps the samples and shows the new name; pointing it elsewhere forgets them until it is read
	src.Name = "Renamed"
	g.setConfig(UsageConfig{Sources: []UsageSourceConfig{src}})
	if a := g.accounts(now); len(a) != 1 || a[0].Sources[0] != "Renamed" {
		t.Fatalf("%+v", a)
	}
	src.URL = "https://two.example.com"
	g.setConfig(UsageConfig{Sources: []UsageSourceConfig{src}})
	if len(g.accounts(now)) != 0 {
		t.Fatal("samples of the old address kept")
	}
	g.pollDue(context.Background(), now.Add(time.Second))
	if a := g.accounts(now); len(a) != 1 || a[0].Name != "https://two.example.com" {
		t.Fatalf("%+v", a)
	}
	g.setConfig(UsageConfig{})
	if len(g.accounts(now)) != 0 {
		t.Fatal("a removed source still shows")
	}
}

func TestGatewayIntervalConfig(t *testing.T) {
	if (UsageConfig{}).interval() != 5*time.Minute || (UsageConfig{PollMinutes: 1}).interval() != time.Minute || (UsageConfig{PollMinutes: 99999}).interval() != 24*time.Hour {
		t.Error("interval")
	}
}

// The whole chain through the route, with a fake gateway behind it: the machines say nothing, the gateway has an account.
func TestUsageWindowsRouteIncludesGatewayAccounts(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * time.Hour).UTC().Format(time.RFC3339)
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/admin/accounts":
			fmt.Fprint(w, `{"accounts":[{"id":"c1","name":"seat one","provider":"codex","status":"active"}]}`)
		case "/api/admin/accounts/usage":
			fmt.Fprintf(w, `{"accounts":[{"account_id":"c1","plan_display_name":"Pro","checked_at":%q,"windows":[{"kind":"five_hour","used_ratio":0.4,"resets_at":%q,"checked_at":%q}]}]}`, now.UTC().Format(time.RFC3339), reset, now.UTC().Format(time.RFC3339))
		default:
			w.WriteHeader(404)
		}
	}))
	defer gate.Close()
	h := newTestDevices(t)
	h.usageGW.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Relay", Type: "relay", URL: gate.URL, Key: "k"}}}, h.usageWin.invalidate, h.logf)
	h.usageGW.pollDue(context.Background(), now)
	mux := h.Handler(http.NotFoundHandler()) // the route as the hub relays it
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/usage/windows", nil))
	body := w.Body.String()
	for _, want := range []string{`"name":"seat one"`, `"plan":"Pro"`, `"sources":["Relay"]`, `"used_percent":40`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	// quoted: an account's id is a hex hash, which can hold c1 by chance (#4178)
	if strings.Contains(body, `"local"`) || strings.Contains(body, `"k"`) || strings.Contains(body, `"c1"`) {
		t.Errorf("a gateway-only account is not local, and its key and ids stay out: %s", body)
	}
}

// A relay read with an ordinary API key (#12311): its key self-service document feeds the same bars, named after the
// provider and plan, and neither the key nor the gateway's account ids reach the answer.
func TestUsageWindowsRouteIncludesAKeysRelayAccounts(t *testing.T) {
	now := time.Now()
	at, reset := now.UTC().Format(time.RFC3339), now.Add(3*time.Hour).UTC().Format(time.RFC3339)
	var paths []string
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer an-api-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/me/usage" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"account_quota_shared":true,"accounts":[
		 {"account_id":"acct-1","provider":"claude","plan":"max","windows":[{"kind":"five_hour","scope":"account","used_ratio":0.4,"resets_at":%[2]q,"checked_at":%[1]q},{"kind":"weekly_fable","scope":"model","scoped_model":"Fable","used_ratio":0.1,"resets_at":%[2]q,"checked_at":%[1]q}]},
		 {"account_id":"acct-2","provider":"claude","plan":"max","windows":[{"kind":"weekly","scope":"account","used_ratio":0.83,"resets_at":%[2]q,"checked_at":%[1]q}]}]}`, at, reset)
	}))
	defer gate.Close()
	h := newTestDevices(t)
	h.usageGW.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Team relay", Type: "relay_key", URL: gate.URL + "/v1", Key: "an-api-key"}}}, h.usageWin.invalidate, h.logf)
	h.usageGW.pollDue(context.Background(), now)
	if len(paths) != 1 || paths[0] != "GET /me/usage" {
		t.Fatalf("one read of the document: %v", paths)
	}
	mux := h.Handler(http.NotFoundHandler())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/usage/windows", nil))
	body := w.Body.String()
	for _, want := range []string{`"name":"Claude Max"`, `"name":"Claude Max 2"`, `"plan":"max"`, `"sources":["Team relay"]`, `"used_percent":40`, `"used_percent":83`, `"model":"Fable"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "an-api-key") || strings.Contains(body, "acct-") || strings.Contains(body, `"local"`) {
		t.Errorf("the key and the gateway's ids stay out: %s", body)
	}
	if inv := h.usageGW.gatewayAccounts(); len(inv.Sources) != 0 || len(inv.Accounts) != 0 {
		t.Errorf("a key sees no pool inventory: %+v", inv)
	}
}

func TestOfflineNodeCannotClaimASource(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var u usageWindowsHub
	src := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex", SampledAt: now, Sources: []string{"Relay"}, Problem: "unreachable", Windows: []proto.UsageWindow{uw("7d", 10080, 1, now.Add(time.Hour))}}}}}, nil
	}}
	got := u.poll(context.Background(), []usageSource{src}, now)
	if len(got) != 1 || !got[0].Local || len(got[0].Sources) != 0 || got[0].Problem != "" {
		t.Fatalf("a node's words about where it came from are not taken: %+v", got)
	}
}

func TestGatewayAccountsKeepsEachSourcesListAndMarksAFailedReadStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var fail atomic.Bool
	var g usageGateways
	g.fetch = func(_ context.Context, cfg usagesource.Config, at time.Time) (usagesource.Result, error) {
		if fail.Load() {
			return usagesource.Result{}, &usagesource.Error{Code: usagesource.CodeUnauthorized}
		}
		if cfg.Type == "generic" {
			return usagesource.Result{Accounts: []proto.UsageAccount{{Provider: "codex", Name: "g", SampledAt: at, Windows: []proto.UsageWindow{uw("7d", 10080, 1, at.Add(time.Hour))}}}}, nil
		}
		return usagesource.Result{Complete: true, Seen: usagesource.Seen{Listed: 2, Providers: map[string]int{}},
			Inventory: []proto.GatewayAccount{
				{AccountID: "k1", Provider: "kiro", Status: "active", Schedulable: true, Windows: []proto.GatewayWindow{{Kind: "kiro_bonus"}}},
				{AccountID: "k3", Provider: "kiro", Status: "disabled", Stale: true, Windows: []proto.GatewayWindow{}},
			}}, nil
	}
	g.init(UsageConfig{Sources: []UsageSourceConfig{
		{ID: "s1", Name: "Relay", Type: "relay", URL: "https://gw.example.com", Key: "secret-key"},
		{ID: "s2", Name: "Other", Type: "generic", URL: "https://other.example.com", Key: "other-key"},
		{ID: "s3", Name: "Not yet", Type: "relay", URL: "https://later.example.com", Key: "later-key", Enabled: new(bool)},
	}}, func() {}, nil)

	if got := g.gatewayAccounts(); len(got.Sources) != 1 || got.Sources[0].CheckedAt != nil || len(got.Accounts) != 0 ||
		got.Sources[0].Error != usagesource.CodePending {
		t.Fatalf("before the first read: %+v", got) // listed as pending, nothing yet; the generic one and the one switched off are not
	}
	g.pollDue(context.Background(), now)
	got := g.gatewayAccounts()
	if len(got.Sources) != 1 || got.Sources[0].ID != "s1" || got.Sources[0].Type != "relay" || !got.Sources[0].Complete ||
		got.Sources[0].Listed != 2 || got.Sources[0].CheckedAt == nil || got.Sources[0].Error != "" {
		t.Fatalf("sources %+v", got.Sources)
	}
	if len(got.Accounts) != 2 || got.Accounts[0].Source != "s1" || got.Accounts[0].Stale || got.Accounts[1].Status != "disabled" || !got.Accounts[1].Stale {
		t.Fatalf("accounts %+v", got.Accounts)
	}
	// the copy given out is not the one kept
	got.Accounts[0].Windows[0].Kind = "changed"
	if g.gatewayAccounts().Accounts[0].Windows[0].Kind != "kiro_bonus" {
		t.Error("the answer shares the kept list")
	}
	b, _ := json.Marshal(g.gatewayAccounts())
	for _, leak := range []string{"secret-key", "gw.example.com", "other-key", "later-key"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q in %s", leak, b)
		}
	}
	// the gateway refuses the key now: the last list stays, stale, with the code
	fail.Store(true)
	g.pollDue(context.Background(), now.Add(6*time.Minute))
	got = g.gatewayAccounts()
	if got.Sources[0].Error != usagesource.CodeUnauthorized || len(got.Accounts) != 2 || !got.Accounts[0].Stale {
		t.Fatalf("after a failed read: %+v", got)
	}
	// pointing it elsewhere forgets the list
	g.setConfig(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Relay", Type: "relay", URL: "https://moved.example.com", Key: "secret-key"}}})
	if got = g.gatewayAccounts(); len(got.Accounts) != 0 || got.Sources[0].CheckedAt != nil || got.Sources[0].Error != usagesource.CodePending {
		t.Fatalf("after a move: %+v", got)
	}
	off := false
	g.setConfig(UsageConfig{Enabled: &off, Sources: []UsageSourceConfig{{ID: "s1", Type: "relay", URL: "https://moved.example.com"}}})
	if got = g.gatewayAccounts(); got.Sources == nil || got.Accounts == nil || len(got.Sources) != 0 {
		t.Fatalf("switched off: %+v", got)
	}
}

func TestUsageGatewayAccountsOverTheDevicesMethod(t *testing.T) {
	h := newTestDevices(t)
	for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`false`), json.RawMessage(`{"future_field":true}`)} {
		out, handled, err := h.Request(t.Context(), devicewire.MethodUsageGatewayAccounts, params)
		if err != nil || !handled {
			t.Fatalf("params %s: handled %v, err %v", params, handled, err)
		}
		if raw, err := json.Marshal(out); err != nil || string(raw) != `{"sources":[],"accounts":[]}` {
			t.Fatalf("params %s: no sources: %s, err %v", params, raw, err)
		}
	}
}

// A hub.json written before the relay type had its name still reads: any type that is not generic was the relay one.
func TestUsageSourceConfigReadsOlderTypeNames(t *testing.T) {
	var cfg UsageConfig
	raw := `{"sources":[{"id":"a","type":"generic"},{"id":"b","type":"relay"},{"id":"c","type":"some-earlier-name"},{"id":"d"},{"id":"e","type":"relay_key"}]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range cfg.Sources {
		got = append(got, s.Type)
	}
	if want := []string{"generic", "relay", "relay", "", "relay_key"}; !slices.Equal(got, want) {
		t.Fatalf("types = %q, want %q", got, want)
	}
}

// A source of type extension is asked through the manager's hook, its rows stay as the extension sent them (not merged
// with a computer's account of the same provider) and go last, in its order.
func TestExtensionSourceRowsAreNotMerged(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var g usageGateways
	asked := 0
	g.fetchExtension = func(_ context.Context, ext, launcher string) (json.RawMessage, error) {
		asked++
		if ext != "example-usage" || launcher != "pool" {
			t.Errorf("asked %q %q", ext, launcher)
		}
		return json.RawMessage(`{"accounts": [
			{"provider": "claude", "name": "Pool", "headline": "available 5/6", "windows": [{"label": "5h", "used_percent": 40, "group": "5h"}, {"label": "7d", "used_percent": 10, "group": "weekly"}]},
			{"provider": "claude", "name": "Seat 1", "notice": "no numbers"}]}`), nil
	}
	g.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Pool", Type: "extension", Extension: "example-usage", Launcher: "pool"}}}, func() {}, func(string, ...any) {})
	g.pollDue(context.Background(), now)
	got := g.accounts(now)
	if asked != 1 || len(got) != 2 || !got[0].Pass || got[0].Sources[0] != "Pool" || got[0].Ref != "" {
		t.Fatalf("%d %+v", asked, got)
	}
	machine := local("claude", "Claude", now, uw("5h", 300, 40, now.Add(time.Hour)), uw("7d", 10080, 10, now.Add(48*time.Hour)))
	merged := mergeUsageAccounts(append([]proto.UsageAccount{machine}, got...), now)
	if len(merged) != 3 || merged[0].Name != "Claude" || merged[1].Name != "Pool" || merged[2].Name != "Seat 1" {
		t.Fatalf("%+v", merged)
	}
	if merged[1].Headline != "available 5/6" || merged[1].Windows[0].Group != "5h" || merged[1].StaleAt == nil || merged[1].ID == merged[2].ID || merged[1].ID == merged[0].ID {
		t.Fatalf("%+v", merged[1])
	}
	if merged[2].Windows == nil || merged[2].Notice != "no numbers" {
		t.Fatalf("%+v", merged[2])
	}
	// The hook failing keeps the last answer and says why; no hook at all is a configuration problem.
	g.fetchExtension = func(context.Context, string, string) (json.RawMessage, error) {
		return nil, errors.New("extension is not running")
	}
	g.pollDue(context.Background(), now.Add(6*time.Minute))
	if got := g.accounts(now.Add(6 * time.Minute)); len(got) != 2 || !got[0].Stale || got[0].Problem != "unreachable" {
		t.Fatalf("%+v", got)
	}
	var none usageGateways
	none.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s2", Name: "X", Type: "extension", Extension: "e", Launcher: "l"}}}, func() {}, func(string, ...any) {})
	none.pollDue(context.Background(), now)
	if st := none.status("s2"); st.Error != usagesource.CodeBadConfig {
		t.Fatalf("%+v", st)
	}
}

func TestExtensionSourceAccountIDsSurviveReordering(t *testing.T) {
	now := time.Now()
	var g usageGateways
	makeRows := func(first, second string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"accounts":[
			{"provider":"claude","id":%q,"name":%q,"notice":"unavailable"},
			{"provider":"claude","id":%q,"name":%q,"notice":"unavailable"},
			{"provider":"claude","name":"No identity","notice":"unavailable"}]}`, first, first, second, second))
	}
	raw := makeRows("one", "two")
	g.fetchExtension = func(context.Context, string, string) (json.RawMessage, error) {
		return raw, nil
	}
	g.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Pool", Type: "extension", Extension: "example-usage", Launcher: "pool"}}}, func() {}, func(string, ...any) {})
	poll := func(at time.Time) []proto.UsageAccount {
		g.pollDue(context.Background(), at)
		rows := mergeUsageAccounts(g.accounts(at), at)
		if len(rows) != 3 || rows[0].ID == rows[1].ID || rows[1].ID == rows[2].ID || rows[0].ID == rows[2].ID {
			t.Fatalf("expected three distinct account IDs, got %+v", rows)
		}
		return rows
	}
	before := poll(now)
	raw = makeRows("two", "one")
	after := poll(now.Add(6 * time.Minute))
	if after[0].Name != "two" || after[0].ID != before[1].ID || after[1].ID != before[0].ID || after[2].ID != before[2].ID {
		t.Fatalf("reorder changed identity: before %+v, after %+v", before, after)
	}
	raw = makeRows("one", "one")
	duplicate := poll(now.Add(12 * time.Minute))
	if duplicate[0].ID == before[0].ID || duplicate[1].ID == before[0].ID {
		t.Fatalf("ambiguous extension identity was used: %+v", duplicate)
	}
}

// manager.json's extension source keeps its extension and launcher through a read and a rewrite, and a different one is
// another source (its samples are not the old one's).
func TestExtensionSourceConfigRoundTrip(t *testing.T) {
	var c UsageConfig
	if err := json.Unmarshal([]byte(`{"sources": [{"id": "s1", "name": "Pool", "type": "extension", "extension": "example-usage", "launcher": "pool"}]}`), &c); err != nil {
		t.Fatal(err)
	}
	s := c.Sources[0]
	if s.Type != "extension" || s.Extension != "example-usage" || s.Launcher != "pool" {
		t.Fatalf("%+v", s)
	}
	other := s
	other.Launcher = "pool2"
	if s.fingerprint() == other.fingerprint() {
		t.Fatal("same fingerprint")
	}
}

// A usage extension's protection line per window reaches GET /api/v1/usage/windows as protect_percent, different per
// account, and is absent where the extension did not say.
func TestUsageWindowsRouteCarriesAnExtensionsProtectionLines(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h := newTestDevices(t)
	h.usageGW.fetchExtension = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"accounts": [
			{"provider": "claude", "name": "Seat A", "windows": [{"label": "5h", "used_percent": 40, "protect_percent": 98}, {"label": "7d", "used_percent": 10, "protect_percent": 99}]},
			{"provider": "claude", "name": "Seat B", "windows": [{"label": "5h", "used_percent": 5, "protect_percent": 90}, {"label": "7d", "used_percent": 20}]}]}`), nil
	}
	h.usageGW.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "s1", Name: "Pool", Type: "extension", Extension: "example-usage", Launcher: "pool"}}}, h.usageWin.invalidate, h.logf)
	h.usageGW.pollDue(context.Background(), now)
	mux := h.Handler(http.NotFoundHandler())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/usage/windows", nil))
	var out struct {
		Accounts []struct {
			Name    string `json:"name"`
			Windows []struct {
				Label          string   `json:"label"`
				ProtectPercent *float64 `json:"protect_percent"`
			} `json:"windows"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Accounts) != 2 {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	want := map[string]*float64{"Seat A 5h": f64(98), "Seat A 7d": f64(99), "Seat B 5h": f64(90), "Seat B 7d": nil}
	for _, a := range out.Accounts {
		for _, win := range a.Windows {
			k := a.Name + " " + win.Label
			exp := want[k]
			if (exp == nil) != (win.ProtectPercent == nil) || (exp != nil && *exp != *win.ProtectPercent) {
				t.Errorf("%s: protect_percent %v, want %v", k, win.ProtectPercent, exp)
			}
			delete(want, k)
		}
	}
	if len(want) != 0 {
		t.Errorf("windows missing from the answer: %v", want)
	}
}

func f64(v float64) *float64 { return &v }

func TestDirectUsageDiscoveryNeedsNoSavedSource(t *testing.T) {
	now := time.Now()
	source := NewDirectUsageSource("example-usage", "pool", "Direct pool", now)
	installed := true
	var g usageGateways
	g.discoverDirect = func() []DirectUsageSource {
		if installed {
			return []DirectUsageSource{source}
		}
		return nil
	}
	calls := 0
	g.fetchDirect = func(_ context.Context, got DirectUsageSource) (json.RawMessage, error) {
		calls++
		if got != source {
			t.Errorf("source %+v, want %+v", got, source)
		}
		return json.RawMessage(`{"accounts":[{"id":"opaque","provider":"claude","name":"Direct","windows":[{"label":"5h","used_percent":20}]}]}`), nil
	}
	g.init(UsageConfig{}, func() {}, nil)
	g.pollDue(context.Background(), now)
	if rows := g.accounts(now); len(rows) != 1 || rows[0].Sources[0] != "Direct pool" || calls != 1 || len(g.config().Sources) != 0 {
		t.Fatalf("rows=%+v calls=%d config=%+v", rows, calls, g.config())
	}
	source = NewDirectUsageSource(source.Extension(), source.Contribution(), source.Name(), now.Add(time.Second))
	g.pollDue(context.Background(), source.UpdatedAt())
	if calls != 2 {
		t.Fatal("changed settings must invalidate and repoll the source")
	}
	installed = false
	g.pollDue(context.Background(), now.Add(2*time.Second))
	if len(g.accounts(now.Add(2*time.Second))) != 0 || calls != 2 {
		t.Fatal("removed contribution retained usage or was polled")
	}
}
