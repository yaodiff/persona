package assistant

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// testDecl is a hub with one gateway channel served over Messages (native) and Responses (an experimental bridge for
// m-exp), run by Claude Code and Codex through two aliases, on m1 (in scope, everything ready) and m2 (out of scope).
func testDecl() execDecl {
	d := execDecl{runtimes: mergeRuntimeCaps(builtinRuntimeCaps(), nil), channels: map[string]proto.ChannelSupply{},
		machines: map[string]proto.MachineReadiness{}}
	d.channels["gw"] = proto.ChannelSupply{Name: "gw", Kind: proto.ChannelManaged, Runtimes: []string{"claude", "codex"},
		Endpoints: []proto.WireProtocol{proto.ProtocolAnthropicMessages, proto.ProtocolOpenAIResponses},
		Models: []proto.ChannelModel{
			{Canonical: "m1", Efforts: []string{"low", "high"}, Edges: []proto.ChannelEdge{
				{Protocol: proto.ProtocolAnthropicMessages, Tier: proto.EdgeNative},
				{Protocol: proto.ProtocolOpenAIResponses, Tier: proto.EdgeBridgedStable}}},
			{Canonical: "m-exp", Edges: []proto.ChannelEdge{
				{Protocol: proto.ProtocolAnthropicMessages, Tier: proto.EdgeNative},
				{Protocol: proto.ProtocolOpenAIResponses, Tier: proto.EdgeBridgedExperimental, Warnings: []string{"tool calls may be dropped"}}}},
		}, Aliases: []string{"gw-claude", "gw-codex"}}
	d.machines["m1"] = proto.MachineReadiness{Machine: "m1", Online: true, Known: true, Runtimes: []string{"claude", "codex"},
		Channels: []string{"gw"}}
	d.machines["m2"] = proto.MachineReadiness{Machine: "m2", Online: true, Known: true, Runtimes: []string{"claude"}}
	d.aliases = []execAlias{
		{name: "gw-claude", spec: proto.ExecutionSpec{Runtime: "claude", Channel: "gw"}},
		{name: "gw-codex", spec: proto.ExecutionSpec{Runtime: "codex", Channel: "gw"}},
	}
	return d
}

func reasonCodes(v execVerdict) []proto.CombinationReasonCode {
	var out []proto.CombinationReasonCode
	for _, r := range v.reasons {
		out = append(out, r.code)
	}
	return out
}

func TestExecutionSpecInheritsEmptyDimensions(t *testing.T) {
	top := proto.ExecutionSpec{Model: " m1 "}.Trimmed()
	got := top.Inherit(proto.ExecutionSpec{Runtime: "codex", Channel: "gw", Model: "m0", Effort: "high"})
	if want := (proto.ExecutionSpec{Runtime: "codex", Channel: "gw", Model: "m1", Effort: "high"}); got != want {
		t.Fatalf("inherit = %+v, want %+v", got, want)
	}
	if !(proto.ExecutionSpec{}).IsZero() || top.IsZero() {
		t.Fatal("IsZero")
	}
	if r, ok := proto.NativeChannelRuntime(proto.NativeChannel("agy")); !ok || r != "agy" {
		t.Fatalf("native channel round trip: %q %v", r, ok)
	}
	if l, ok := proto.ShellChannelLauncher(proto.ShellChannel("glm")); !ok || l != "glm" {
		t.Fatalf("shell channel round trip: %q %v", l, ok)
	}
	if _, ok := proto.NativeChannelRuntime("gw"); ok {
		t.Fatal("a managed channel read as native")
	}
}

// The three levels: native runs silently and is recommended, an experimental bridge runs with its notice and is never
// recommended, a combination with no common protocol is blocked with the reason and alternatives.
func TestValidateCombinationLevels(t *testing.T) {
	d := testDecl()
	native := d.judge(proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "m1"}, "m1")
	if native.level() != proto.CombinationAllowed || !native.recommended() || native.launcher != "gw-claude" || len(native.reasons) != 0 {
		t.Fatalf("native = %+v", native)
	}
	stable := d.judge(proto.ExecutionSpec{Runtime: "codex", Channel: "gw", Model: "m1"}, "m1")
	if stable.level() != proto.CombinationAllowed || stable.tier != proto.EdgeBridgedStable || stable.protocol != proto.ProtocolOpenAIResponses {
		t.Fatalf("stable bridge = %+v", stable)
	}
	exp := d.judge(proto.ExecutionSpec{Runtime: "codex", Channel: "gw", Model: "m-exp"}, "m1")
	if exp.level() != proto.CombinationNotice || exp.recommended() || exp.blocked() ||
		!slices.Contains(reasonCodes(exp), proto.ReasonBridgedExperimental) || !slices.Contains(reasonCodes(exp), proto.ReasonBridgeWarning) {
		t.Fatalf("experimental bridge = %+v", exp)
	}
	w := exp.wire(i18n.Lang("zh-Hans"))
	if w.Level != proto.CombinationNotice || len(w.Reasons) == 0 || !strings.Contains(w.Reasons[0].Text, "兼容桥接") {
		t.Fatalf("experimental bridge worded = %+v", w)
	}
	blocked := d.resolve(proto.ExecutionSpec{Runtime: "agy", Channel: "gw", Model: "m1"}, "m1")
	if !blocked.blocked() || reasonCodes(blocked)[0] != proto.ReasonNoCommonProtocol || len(blocked.alternatives) == 0 {
		t.Fatalf("blocked = %+v", blocked)
	}
	if blocked.err() == nil || blocked.wire(i18n.Lang("en")).Level != proto.CombinationBlocked {
		t.Fatalf("blocked err/wire = %v", blocked.err())
	}
	for _, alt := range blocked.alternatives {
		if alt.Model != "m1" || d.judge(alt, "m1").blocked() {
			t.Fatalf("alternative %+v does not run m1 on m1", alt)
		}
	}
	if v := d.judge(proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "nope"}, ""); reasonCodes(v)[0] != proto.ReasonModelNotOffered {
		t.Fatalf("unknown model = %+v", v)
	}
	if v := d.judge(proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "m1"}, "m2"); !slices.Contains(reasonCodes(v), proto.ReasonChannelOutOfScope) || !v.blocked() {
		t.Fatalf("out of scope = %+v", v)
	}
	if v := d.judge(proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "m1"}, "m9"); !slices.Contains(reasonCodes(v), proto.ReasonMachineUnknown) {
		t.Fatalf("unknown machine = %+v", v)
	}
}

// An empty runtime or channel is filled as the hub would run it: the best runnable alias.
func TestResolveFillsEmptyDimensions(t *testing.T) {
	d := testDecl()
	v := d.resolve(proto.ExecutionSpec{Channel: "gw", Model: "m-exp"}, "m1")
	if v.blocked() || v.spec.Runtime != "claude" || v.launcher != "gw-claude" || !v.recommended() {
		t.Fatalf("resolve = %+v, want the native claude alias over the experimental codex one", v)
	}
	v = d.resolve(proto.ExecutionSpec{Runtime: "codex", Model: "m1"}, "m1")
	if v.blocked() || v.spec.Channel != "gw" || v.launcher != "gw-codex" {
		t.Fatalf("resolve runtime only = %+v", v)
	}
}

func TestEffortClampedAndSuffixFolded(t *testing.T) {
	d := testDecl()
	v := d.judge(proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "m1", Effort: "max"}, "")
	if v.level() != proto.CombinationNotice || v.wireEffort != "high" || !slices.Contains(reasonCodes(v), proto.ReasonEffortClamped) {
		t.Fatalf("clamp = %+v", v)
	}
	if got := clampEffort("medium", []string{"high", "max"}); got != "high" {
		t.Fatalf("clamp below the weakest = %q", got)
	}
	models := channelModels([]proto.ProviderModel{{ID: "m"}, {ID: "m-high"}, {ID: "m-low"}, {ID: "solo-high"}}, proto.ProtocolOpenAIChat)
	if len(models) != 2 || models[0].Canonical != "m" || !slices.Equal(models[0].Efforts, []string{"low", "high"}) ||
		len(models[0].Edges) != 1 || models[0].Edges[0].EffortVia != proto.EffortModelSuffix {
		t.Fatalf("folded = %+v", models)
	}
	if models[1].Canonical != "solo-high" {
		t.Fatalf("an id whose base is not listed is a model of its own: %+v", models[1])
	}
	ch := proto.ChannelSupply{Name: "s", Kind: proto.ChannelManaged, Runtimes: []string{"pi"},
		Endpoints: []proto.WireProtocol{proto.ProtocolOpenAIChat}, Models: models}
	d.channels["s"] = ch
	d.runtimes["pi"] = proto.RuntimeCapability{Runtime: "pi", Protocols: []proto.WireProtocol{proto.ProtocolOpenAIChat}}
	d.aliases = append(d.aliases, execAlias{name: "s-pi", spec: proto.ExecutionSpec{Runtime: "pi", Channel: "s"}})
	v = d.judge(proto.ExecutionSpec{Runtime: "pi", Channel: "s", Model: "m-high"}, "")
	if v.blocked() || v.spec.Model != "m" || v.spec.Effort != "high" || v.wireModel != "m-high" {
		t.Fatalf("suffix model = %+v", v)
	}
}

const testChannel = `{"base_url":"https://gw.example.com","api_key":"` + fakeKey + `",
	"endpoints":["anthropic_messages","openai_responses"],
	"models":[{"canonical":"m1","efforts":["low","high"]}],
	"bindings":[{"runtime":"claude","alias":"gw-claude"},{"runtime":"codex","alias":"gw-codex","model":"m1"}]}`

// One channel, one key, one scope: two runtimes run it through two launcher aliases, which every launcher reader
// sees and launchers.managed never keeps; they are edited only through the channel.
func TestSharedChannelAliases(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw", testChannel); code != 200 {
		t.Fatal(code, raw)
	}
	list := s.storedLaunchers(ctx)
	if len(list) != 2 {
		t.Fatalf("aliases = %+v", list)
	}
	for _, l := range list {
		if l.channelAlias != "gw" || l.Channel != "gw" || l.KeySealed == "" || l.KeySealed != list[0].KeySealed || l.BaseURL != "https://gw.example.com" {
			t.Fatalf("alias %s = %+v", l.Name, l)
		}
	}
	if list[1].Agent != "codex" || list[1].Model != "m1" || list[0].Agent != "" {
		t.Fatalf("bindings = %+v", list)
	}
	var raw []json.RawMessage
	if _, err := s.store().Setting(ctx, launchersManagedKey, &raw); err != nil || len(raw) != 0 {
		t.Fatalf("launchers.managed keeps the aliases: %d %v", len(raw), err)
	}
	if code, body := launcherCall(t, mux, "GET", "/api/v1/launchers", ""); code != 200 || !strings.Contains(body, `"gw-codex"`) || strings.Contains(body, fakeKey) {
		t.Fatalf("launcher list = %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw-claude", `{"base_url":"https://x.example.com","model":"m1"}`); code != 400 {
		t.Fatalf("alias edit = %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "DELETE", "/api/v1/launchers/gw-codex", ""); code != 400 {
		t.Fatalf("alias delete = %d %s", code, body)
	}
	code, body := launcherCall(t, mux, "GET", "/api/v1/execution/channels", "")
	if code != 200 || strings.Contains(body, fakeKey) || strings.Contains(body, "key_sealed") || !strings.Contains(body, `"has_key":true`) {
		t.Fatalf("channel view = %d %s", code, body)
	}
	// A runtime that speaks none of the channel's protocols can't be bound.
	bad := strings.Replace(testChannel, `"runtime":"codex"`, `"runtime":"agy"`, 1)
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw", bad); code != 400 {
		t.Fatalf("agy on a Messages/Responses channel = %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "GET", "/api/v1/execution/catalog", ""); code != 200 || !strings.Contains(body, `"launcher":"gw-codex"`) {
		t.Fatalf("catalog = %d %s", code, body)
	}
	code, body = launcherCall(t, mux, "POST", "/api/v1/execution/validate", `{"spec":{"runtime":"codex","channel":"gw","model":"m1"}}`)
	if code != 200 || !strings.Contains(body, `"launcher":"gw-codex"`) || !strings.Contains(body, `"level":"allowed"`) {
		t.Fatalf("validate = %d %s", code, body)
	}
}

// Every change is a rollback point; a rollback restores it (the launchers a change folded in included) and is a point
// itself, so it can be undone.
func TestChannelLedgerRollback(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"gw-claude","base_url":"https://old.example.com","model":"m1","api_key":"`+fakeKey+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	folded := strings.Replace(testChannel, `"bindings"`, `"replace_launchers":["gw-claude"],"bindings"`, 1)
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw", folded); code != 200 {
		t.Fatal(code, raw)
	}
	if l, _ := s.managedNamed(ctx, "gw-claude"); l.channelAlias != "gw" {
		t.Fatalf("gw-claude not folded into the channel: %+v", l)
	}
	moved := strings.Replace(testChannel, "https://gw.example.com", "https://new.example.com", 1)
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw?x=1", strings.Replace(moved, `{"base_url"`, `{"revision":2,"base_url"`, 1)); code != 409 {
		t.Fatalf("a stale revision = %d %s", code, raw)
	}
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw", strings.Replace(moved, `{"base_url"`, `{"revision":1,"base_url"`, 1)); code != 200 {
		t.Fatal(code, raw)
	}
	if l, _ := s.managedNamed(ctx, "gw-codex"); l.BaseURL != "https://new.example.com" {
		t.Fatalf("after the move: %+v", l)
	}
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/execution/channels/rollback", ""); code != 200 {
		t.Fatal(code, raw)
	}
	if l, _ := s.managedNamed(ctx, "gw-codex"); l.BaseURL != "https://gw.example.com" {
		t.Fatalf("rolled back: %+v", l)
	}
	v, err := s.channelCatalogView(ctx)
	if err != nil || v.Revision != 3 || len(v.Rollbacks) != 2 || v.Rollbacks[0].Action != "rollback" {
		t.Fatalf("ledger = %+v %v", v, err)
	}
	// Back to before the channel: gw-claude is the launcher of its own again, its key kept.
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/execution/channels/rollback", `{}`); code != 200 {
		t.Fatal(code, raw)
	}
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/execution/channels/rollback", `{"revision":`+jsonInt(v.Rollbacks[1].Revision)+`}`); code != 200 {
		t.Fatal(code, raw)
	}
	l, ok := s.managedNamed(ctx, "gw-claude")
	if !ok || l.channelAlias != "" || l.BaseURL != "https://old.example.com" || l.KeySealed == "" {
		t.Fatalf("gw-claude restored = %+v %v", l, ok)
	}
	if _, ok := s.managedNamed(ctx, "gw-codex"); ok {
		t.Fatal("the channel's alias outlived the channel")
	}
	if code, raw := launcherCall(t, mux, "DELETE", "/api/v1/execution/channels/nope", ""); code != 404 {
		t.Fatalf("delete unknown = %d %s", code, raw)
	}
}

// A channel's own quota source is its aliases' usage pool; an admission group's quota_pool still wins.
func TestChannelQuotaHeadroom(t *testing.T) {
	s, d := routingRig(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 7, 0, 0, 0, time.UTC)
	in := proto.ChannelConfig{Name: "gw", BaseURL: "https://gw.example.com", Endpoints: []proto.WireProtocol{proto.ProtocolOpenAIResponses},
		Quota:    &proto.ChannelQuota{Source: "pool-a", Provider: "codex"},
		Models:   []proto.ChannelModel{{Canonical: "gpt-x", QuotaPool: "gpt"}},
		Bindings: []proto.ChannelBinding{{Runtime: "codex", Alias: "gw-codex"}}}
	if err := s.saveChannel(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	a := proto.UsageAccount{ID: "a", Provider: "codex", Ref: "pool-a:1", SampledAt: now,
		Windows: []proto.UsageWindow{paceAt(now, "5h", 300, time.Hour, 10, "gpt")}}
	b := proto.UsageAccount{ID: "b", Provider: "codex", Ref: "pool-b:1", SampledAt: now,
		Windows: []proto.UsageWindow{paceAt(now, "5h", 300, time.Hour, 0, "")}}
	d.accounts = []proto.UsageAccount{a, b}
	got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-codex", "gpt-x", now)
	if !got.Known || got.Accounts != 1 || math.Abs(got.H-0.07) > 1e-9 {
		t.Fatalf("channel pool headroom = %+v, want pool-a's 7 points", got)
	}
	withLauncherGroups(s, LauncherGroup{Name: "g", Launchers: []string{"gw-codex"}, Cap: 2,
		QuotaPool: &GatewayQuotaPool{Source: "pool-b", Provider: "codex"}})
	got = s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-codex", "gpt-x", now)
	if !got.Known || got.Accounts != 1 || math.Abs(got.H-0.17) > 1e-9 {
		t.Fatalf("the group's pool must win: %+v", got)
	}
}

// A tier candidate or a pick given as runtime + channel runs on the alias; one that keeps them follows a rebinding.
func TestFourDimensionsProjectOntoLaunchers(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/execution/channels/gw", testChannel); code != 200 {
		t.Fatal(code, raw)
	}
	tiers := map[string]ModelTier{"dev": {Runtime: "codex", Channel: "gw", Model: "m1"},
		"old": {Launcher: "gw-claude", Model: "m1"}}
	if err := s.projectTiers(ctx, tiers); err != nil {
		t.Fatal(err)
	}
	if tiers["dev"].Launcher != "gw-codex" || tiers["old"].Runtime != "" {
		t.Fatalf("projected = %+v", tiers)
	}
	if err := s.projectTiers(ctx, map[string]ModelTier{"x": {Runtime: "agy", Channel: "gw"}}); err == nil {
		t.Fatal("agy on gw projected")
	}
	list := s.reprojectCandidates(ctx, []TierCandidate{{Launcher: "renamed", Runtime: "codex", Channel: "gw"}, {Launcher: "plain"}})
	if list[0].Launcher != "gw-codex" || list[1].Launcher != "plain" {
		t.Fatalf("reprojected = %+v", list)
	}
	p := modelPick{Launcher: "gw-codex", Runtime: "codex", Channel: "gw"}
	if w := p.wire(); w.Runtime != "codex" || w.Channel != "gw" || w.Launcher != "gw-codex" {
		t.Fatalf("pick wire = %+v", w)
	}
	in, err := s.projectPick(ctx, "sid", "", proto.ModelPick{Launcher: "gw-claude", Channel: "other"})
	if err != nil || in.Runtime != "claude" || in.Channel != "gw" {
		t.Fatalf("a launcher wins over the dimensions given: %+v %v", in, err)
	}
}

func TestLastGoodKeptAndRestorable(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	if code, body := launcherCall(t, mux, "GET", "/api/v1/assistant/conversations/sid-1/model/last-good", ""); code != 200 || !strings.Contains(body, `"last_good":null`) {
		t.Fatalf("none kept = %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "POST", "/api/v1/assistant/conversations/sid-1/model/restore", ""); code != 404 {
		t.Fatalf("restore with none = %d %s", code, body)
	}
	at := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	g := proto.LastGoodSpec{Spec: proto.ExecutionSpec{Runtime: "claude", Channel: "gw", Model: "m1"}, Launcher: "gw-claude", Machine: "server", At: at}
	s.keepLastGood(ctx, "sid-1", g)
	var stored map[string]proto.LastGoodSpec
	if _, err := s.store().Setting(ctx, lastGoodKey, &stored); err != nil || stored["sid-1"].Launcher != "gw-claude" {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	// The same execution a minute later is not written again.
	later := g
	later.At = at.Add(time.Minute)
	s.keepLastGood(ctx, "sid-1", later)
	if got, _ := s.lastGoodOf(ctx, "sid-1"); !got.At.Equal(at) {
		t.Fatalf("rewritten for the same execution: %+v", got)
	}
	code, body := launcherCall(t, mux, "GET", "/api/v1/assistant/conversations/sid-1/model/last-good", "")
	if code != 200 || !strings.Contains(body, `"launcher":"gw-claude"`) {
		t.Fatalf("kept = %d %s", code, body)
	}
	// Restoring goes through every check of a pick: sid-1 is no assistant conversation here, so nothing is picked.
	if code, body := launcherCall(t, mux, "POST", "/api/v1/assistant/conversations/sid-1/model/restore", ""); code == 200 || code >= 500 {
		t.Fatalf("restore on no assistant conversation = %d %s", code, body)
	}
}

// The channels' sealed fields, and those of the ledger that keeps their earlier versions, are re-sealed by the key
// rotation.
func TestTheChannelsSealedFieldsAreReSealed(t *testing.T) {
	for key, typ := range map[string]reflect.Type{execChannelsKey: reflect.TypeOf(storedChannel{}),
		execChannelLedgerKey: reflect.TypeOf(ledgerChannel{}), execChannelLedgerLaunchersKey: reflect.TypeOf(ledgerLauncher{})} {
		i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == key })
		if i < 0 || !db.SealedSettings()[i].List {
			t.Fatalf("%s is not a list in db.SealedSettings", key)
		}
		sealed := sealedJSONFields(typ)
		if len(sealed) == 0 {
			t.Fatalf("no sealed field found in %s", typ)
		}
		for _, name := range sealed {
			if !slices.Contains(db.SealedSettings()[i].Fields, name) {
				t.Errorf("%s's %s is sealed and not re-sealed by the key rotation", typ, name)
			}
		}
	}
	if got := persistedLaunchers(nil); got == nil {
		t.Fatal("persistedLaunchers(nil) must store an empty list, not null")
	}
}
