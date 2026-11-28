package connector

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

// approvalFake is a generic confirm_always extension connector implementing ApprovalActions and IdentifiedConnector.
type approvalFake struct {
	id         string
	actions    []string
	confirmAll []string
	ident      ExtensionIdentity
	hasIdent   bool

	describeHook func(ctx context.Context, name string, args json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error)
	doHook       func(ctx context.Context, name string, args json.RawMessage, approval AutoConfirmApproval) (DoResult, error)

	describeCalls     int
	describeProtocols []string
	describeArgs      []json.RawMessage
	doCalls           int
	doApprovals       []AutoConfirmApproval
	doArgs            []json.RawMessage
}

func (f *approvalFake) ID() string   { return f.id }
func (f *approvalFake) Name() string { return "Example" }
func (f *approvalFake) Manifest() Manifest {
	return Manifest{
		Capabilities:  []Capability{CapStatus, CapActions},
		ConfirmAlways: f.confirmAll,
		Proxy:         true,
	}
}
func (f *approvalFake) Status(context.Context) Status { return Status{State: StateOK} }
func (f *approvalFake) ExtensionIdentity() (ExtensionIdentity, bool) {
	return f.ident, f.hasIdent
}
func (f *approvalFake) Actions() []proto.Tool {
	out := make([]proto.Tool, len(f.actions))
	for i, n := range f.actions {
		out[i] = proto.Tool{Name: n}
	}
	return out
}
func (f *approvalFake) Describe(ctx context.Context, name string, args json.RawMessage) (Action, string, error) {
	act, sum, _, err := f.DescribeWithApproval(ctx, name, args, "")
	return act, sum, err
}
func (f *approvalFake) DescribeWithApproval(ctx context.Context, name string, args json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
	f.describeCalls++
	f.describeProtocols = append(f.describeProtocols, protocol)
	f.describeArgs = append(f.describeArgs, append(json.RawMessage(nil), args...))
	if f.describeHook != nil {
		return f.describeHook(ctx, name, args, protocol)
	}
	return Action{Name: name, Visible: "example service"}, "Execute " + name, nil, nil
}
func (f *approvalFake) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	res, err := f.DoWithApproval(ctx, name, args, AutoConfirmApproval{})
	return res.Text, err
}
func (f *approvalFake) DoRef(ctx context.Context, name string, args json.RawMessage) (DoResult, error) {
	return f.DoWithApproval(ctx, name, args, AutoConfirmApproval{})
}
func (f *approvalFake) DoWithApproval(ctx context.Context, name string, args json.RawMessage, approval AutoConfirmApproval) (DoResult, error) {
	f.doCalls++
	f.doApprovals = append(f.doApprovals, approval)
	f.doArgs = append(f.doArgs, append(json.RawMessage(nil), args...))
	if f.doHook != nil {
		return f.doHook(ctx, name, args, approval)
	}
	return DoResult{Text: "executed " + name, MessageRef: "ref-1"}, nil
}

// legacyIdentFake implements Actions and IdentifiedConnector, but NOT ApprovalActions (an older connector).
type legacyIdentFake struct {
	*fake
	ident ExtensionIdentity
}

func (l legacyIdentFake) Manifest() Manifest {
	m := l.fake.Manifest()
	m.ConfirmAlways = []string{"example_comment"}
	return m
}
func (l legacyIdentFake) Actions() []proto.Tool {
	return []proto.Tool{{Name: "example_comment"}}
}
func (l legacyIdentFake) ExtensionIdentity() (ExtensionIdentity, bool) { return l.ident, true }

func pinAutoConfirmClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	prev := autoConfirmNow
	autoConfirmNow = func() time.Time { return cur }
	t.Cleanup(func() { autoConfirmNow = prev })
	return &cur
}

func TestValidateAutoConfirmPolicies(t *testing.T) {
	valid := []AutoConfirmPolicy{
		{
			Name:          "small-tier",
			Extension:     "example-ext",
			Connector:     "example",
			Actions:       []string{"example_transfer", "example_cancel"},
			Protocol:      ApprovalProtocolGuardedAutoConfirmV1,
			MinVersion:    "1.2.0",
			MaxVersion:    "2.0.0",
			Sources:       []string{"installed", "builtin"},
			MarketKey:     "market:official",
			MaxTTLSeconds: 60,
		},
		{
			Extension: "example.ext_2",
			Connector: "ex2",
			Actions:   []string{"ex2_submit"},
			Disabled:  true,
		},
	}
	if err := ValidateAutoConfirmPolicies(valid); err != nil {
		t.Fatalf("valid policies failed: %v", err)
	}

	badCases := []struct {
		name string
		p    AutoConfirmPolicy
	}{
		{"empty extension", AutoConfirmPolicy{Connector: "example", Actions: []string{"example_do"}}},
		{"wildcard extension", AutoConfirmPolicy{Extension: "example-*", Connector: "example", Actions: []string{"example_do"}}},
		{"uppercase extension", AutoConfirmPolicy{Extension: "Example", Connector: "example", Actions: []string{"example_do"}}},
		{"empty connector", AutoConfirmPolicy{Extension: "example-ext", Actions: []string{"example_do"}}},
		{"wildcard connector", AutoConfirmPolicy{Extension: "example-ext", Connector: "*", Actions: []string{"example_do"}}},
		{"no actions", AutoConfirmPolicy{Extension: "example-ext", Connector: "example"}},
		{"wildcard action", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_*"}}},
		{"question wildcard action", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_a?"}}},
		{"wrong action prefix", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"other_do"}}},
		{"bare prefix action", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_"}}},
		{"duplicate action", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do", "example_do"}}},
		{"unsupported protocol", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, Protocol: "custom-v1"}},
		{"bad min version", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MinVersion: "v1.0.0"}},
		{"bad max version", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MaxVersion: "1.0"}},
		{"inverted version range", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MinVersion: "2.0.0", MaxVersion: "1.9.9"}},
		{"unknown source", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, Sources: []string{"remote"}}},
		{"duplicate source", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, Sources: []string{"installed", "installed"}}},
		{"whitespace market key", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MarketKey: "bad key"}},
		{"negative ttl", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MaxTTLSeconds: -1}},
		{"ttl over hard max", AutoConfirmPolicy{Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}, MaxTTLSeconds: AutoConfirmHardMaxTTLSeconds + 1}},
		{"control char in name", AutoConfirmPolicy{Name: "line1\nline2", Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}}},
		{"leading space in name", AutoConfirmPolicy{Name: " padded", Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}}},
		{"overlong name", AutoConfirmPolicy{Name: strings.Repeat("a", 129), Extension: "example-ext", Connector: "example", Actions: []string{"example_do"}}},
	}
	for _, tc := range badCases {
		if err := ValidateAutoConfirmPolicies([]AutoConfirmPolicy{tc.p}); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.name)
		}
	}
}

func TestGuardedAutoConfirmHappyPathAndByteIdenticalArgs(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	clock := pinAutoConfirmClock(t, now)

	const secretToken = "ac_tok_sensitive_1234567890"
	c := &approvalFake{
		id:         "example",
		actions:    []string{"example_submit"},
		confirmAll: []string{"example_submit"},
		ident: ExtensionIdentity{
			Extension: "example-ext",
			Version:   "1.4.0",
			Source:    "installed",
			MarketKey: "market:main",
		},
		hasIdent: true,
		describeHook: func(_ context.Context, name string, _ json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
			if protocol != ApprovalProtocolGuardedAutoConfirmV1 {
				return Action{Name: name, Visible: "example service"}, "Submit action", nil, nil
			}
			return Action{Name: name, Visible: "example service"}, "Submit small request\nDetails line", &AutoConfirmGrant{
				Token:     secretToken,
				ExpiresAt: now.Add(45 * time.Second),
			}, nil
		},
	}

	r := NewRegistry([]Rule{{Connector: "*", Action: "*", Decision: Allow}}) // confirm_always still clamps Allow
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAutoConfirmPolicies([]AutoConfirmPolicy{{
		Name:          "small-submit",
		Extension:     "example-ext",
		Connector:     "example",
		Actions:       []string{"example_submit"},
		MinVersion:    "1.0.0",
		MaxVersion:    "2.0.0",
		MarketKey:     "market:main",
		MaxTTLSeconds: 60,
	}}); err != nil {
		t.Fatal(err)
	}

	var sent []Sent
	r.SetSent(func(_ context.Context, s Sent) { sent = append(sent, s) })
	asked := 0
	ask := func(context.Context, string, json.RawMessage) bool { asked++; return true }

	rawArgs := json.RawMessage(`{"z":1, "a":"keep-exact-bytes", "nested":{"k":2}}`)
	res := r.Call(WithOwnerCaller(context.Background()), "example_submit", rawArgs, ask)
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if asked != 0 {
		t.Fatalf("expected 0 phone cards, got %d", asked)
	}
	if c.describeCalls != 1 || c.describeProtocols[0] != ApprovalProtocolGuardedAutoConfirmV1 {
		t.Fatalf("describe calls=%d protocols=%v", c.describeCalls, c.describeProtocols)
	}
	if c.doCalls != 1 {
		t.Fatalf("do calls=%d, want 1", c.doCalls)
	}
	if !bytes.Equal(c.describeArgs[0], rawArgs) || !bytes.Equal(c.doArgs[0], rawArgs) {
		t.Fatalf("args drifted: describe=%s do=%s want=%s", c.describeArgs[0], c.doArgs[0], rawArgs)
	}
	if got := c.doApprovals[0]; got.Mode != ApprovalModeAutoConfirm || got.Token != secretToken {
		t.Fatalf("unexpected approval forwarded: %+v", got)
	}
	if len(sent) != 1 {
		t.Fatalf("sent count=%d, want 1", len(sent))
	}
	s := sent[0]
	if s.Through.Kind != ThroughAutoConfirm || s.Through.Rule != nil || s.Through.AutoConfirm == nil {
		t.Fatalf("unexpected let-through: %+v", s.Through)
	}
	ac := s.Through.AutoConfirm
	if ac.Policy != "small-submit" || ac.Extension != "example-ext" || ac.Version != "1.4.0" ||
		ac.Source != "installed" || ac.Protocol != ApprovalProtocolGuardedAutoConfirmV1 || !ac.OwnerAuthorized {
		t.Fatalf("unexpected audit metadata: %+v", ac)
	}
	if rawSent, _ := json.Marshal(s); bytes.Contains(rawSent, []byte(secretToken)) {
		t.Fatalf("approval token leaked into Sent record: %s", rawSent)
	}

	// Token expiring between gate validation and DoWithApproval fails closed without calling Do or recording Sent.
	nowCalls := 0
	autoConfirmNow = func() time.Time {
		nowCalls++
		if nowCalls == 1 {
			return now
		}
		return now.Add(50 * time.Second)
	}
	c.describeHook = func(_ context.Context, name string, _ json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
		return Action{Name: name, Visible: "example service"}, "Submit expiring", &AutoConfirmGrant{
			Token:     "tok_expires_before_do",
			ExpiresAt: now.Add(30 * time.Second),
		}, nil
	}
	beforeDo := c.doCalls
	res = r.Call(WithOwnerCaller(context.Background()), "example_submit", rawArgs, ask)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "expired") || c.doCalls != beforeDo || len(sent) != 1 || asked != 0 {
		t.Fatalf("token expiring before Do must fail closed: res=%+v doCalls=%d sent=%d asked=%d", res, c.doCalls, len(sent), asked)
	}
	autoConfirmNow = func() time.Time { return *clock }

	// Failed DoWithApproval returns the error directly and never retries plain Do or records Sent.
	c.describeHook = func(_ context.Context, name string, _ json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
		return Action{Name: name, Visible: "example service"}, "Submit failing", &AutoConfirmGrant{
			Token:     "tok_will_fail",
			ExpiresAt: clock.Add(30 * time.Second),
		}, nil
	}
	c.doHook = func(_ context.Context, _ string, _ json.RawMessage, approval AutoConfirmApproval) (DoResult, error) {
		return DoResult{}, errors.New("approval token rejected by extension")
	}
	beforeDo = c.doCalls
	res = r.Call(WithOwnerCaller(context.Background()), "example_submit", rawArgs, ask)
	if !res.IsError || c.doCalls != beforeDo+1 || len(sent) != 1 || asked != 0 {
		t.Fatalf("failed DoWithApproval must not retry or record sent: res=%+v doCalls=%d sent=%d asked=%d", res, c.doCalls, len(sent), asked)
	}

	// Registry.Run bypasses gate and sends no approval fields.
	c.doHook = nil
	res = r.Run(WithOwnerCaller(context.Background()), "example_submit", rawArgs)
	if res.IsError {
		t.Fatalf("Run failed: %+v", res)
	}
	if last := c.doApprovals[len(c.doApprovals)-1]; last.Mode != "" || last.Token != "" {
		t.Fatalf("Registry.Run must never forward approval fields: %+v", last)
	}
}

func TestGuardedAutoConfirmPrecedenceAndFallbacks(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	pinAutoConfirmClock(t, now)

	baseIdent := ExtensionIdentity{
		Extension: "example-ext",
		Version:   "1.2.0",
		Source:    "installed",
		MarketKey: "market:main",
	}
	basePolicy := AutoConfirmPolicy{
		Extension:     "example-ext",
		Connector:     "example",
		Actions:       []string{"example_submit"},
		MinVersion:    "1.0.0",
		MaxVersion:    "1.5.0",
		MarketKey:     "market:main",
		MaxTTLSeconds: 60,
	}
	validGrant := func() *AutoConfirmGrant {
		return &AutoConfirmGrant{Token: "tok_ok_1", ExpiresAt: now.Add(30 * time.Second)}
	}

	newEnv := func(rules []Rule, policies []AutoConfirmPolicy, ident ExtensionIdentity, hasIdent bool, grantFn func(protocol string) *AutoConfirmGrant) (*Registry, *approvalFake, *int, *[]Sent) {
		c := &approvalFake{
			id:         "example",
			actions:    []string{"example_submit", "example_other"},
			confirmAll: []string{"example_submit", "example_other"},
			ident:      ident,
			hasIdent:   hasIdent,
			describeHook: func(_ context.Context, name string, _ json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
				var g *AutoConfirmGrant
				if grantFn != nil {
					g = grantFn(protocol)
				}
				return Action{Name: name, Visible: "example service"}, "Summary for " + name, g, nil
			},
		}
		r := NewRegistry(rules)
		if err := r.Add(c); err != nil {
			t.Fatal(err)
		}
		if err := r.SetAutoConfirmPolicies(policies); err != nil {
			t.Fatal(err)
		}
		var asked int
		var sent []Sent
		r.SetSent(func(_ context.Context, s Sent) { sent = append(sent, s) })
		return r, c, &asked, &sent
	}

	t.Run("explicit deny blocks before describe", func(t *testing.T) {
		r, c, asked, sent := newEnv(
			[]Rule{{Connector: "example", Action: "example_submit", Decision: Deny}},
			[]AutoConfirmPolicy{basePolicy}, baseIdent, true,
			func(string) *AutoConfirmGrant { return validGrant() },
		)
		res := r.Call(WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{"amount":1}`), func(context.Context, string, json.RawMessage) bool { *asked++; return true })
		if !res.IsError || res.ErrorKind != proto.ToolErrorDeclined || c.describeCalls != 0 || c.doCalls != 0 || *asked != 0 || len(*sent) != 0 {
			t.Fatalf("deny failed: res=%+v desc=%d do=%d asked=%d sent=%d", res, c.describeCalls, c.doCalls, *asked, len(*sent))
		}
	})

	t.Run("repeated case-folded json args refused before describe", func(t *testing.T) {
		r, c, asked, sent := newEnv(
			nil, []AutoConfirmPolicy{basePolicy}, baseIdent, true,
			func(string) *AutoConfirmGrant { return validGrant() },
		)
		res := r.Call(WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{"amount":1,"AMOUNT":9999}`), func(context.Context, string, json.RawMessage) bool { *asked++; return true })
		if !res.IsError || res.ErrorKind != proto.ToolErrorDeclined || c.describeCalls != 0 || c.doCalls != 0 || *asked != 0 || len(*sent) != 0 {
			t.Fatalf("repeated field failed: res=%+v desc=%d do=%d asked=%d sent=%d", res, c.describeCalls, c.doCalls, *asked, len(*sent))
		}
	})

	cardFallbackCases := []struct {
		name           string
		ctx            context.Context
		rules          []Rule
		policies       []AutoConfirmPolicy
		ident          ExtensionIdentity
		hasIdent       bool
		action         string
		grantFn        func(protocol string) *AutoConfirmGrant
		wantNegotiated bool
	}{
		{
			name:           "no policy configured (default off)",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       nil,
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "disabled policy",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{func() AutoConfirmPolicy { p := basePolicy; p.Disabled = true; return p }()},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "explicit confirm rule overrides auto-confirm",
			ctx:            WithOwnerCaller(context.Background()),
			rules:          []Rule{{Connector: "example", Action: "example_submit", Decision: Confirm}},
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "ConfirmEach overrides auto-confirm",
			ctx:            ConfirmEach(WithOwnerCaller(context.Background())),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "default unmarked context is non-owner",
			ctx:            context.Background(),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "WithoutOwnerCaller clears owner eligibility",
			ctx:            WithoutOwnerCaller(WithOwnerCaller(context.Background())),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "action not listed in policy",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_other",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "missing extension identity",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{},
			hasIdent:       false,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "extension id mismatch",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "other-ext", Version: "1.2.0", Source: "installed", MarketKey: "market:main"},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "untrusted hub.json source under default sources",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "example-ext", Version: "1.2.0", Source: "hub.json", MarketKey: "market:main"},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "remote socket extension rejected",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "example-ext", Version: "1.2.0", Source: "installed", MarketKey: "market:main", Remote: true},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "version below min_version",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "example-ext", Version: "0.9.9", Source: "installed", MarketKey: "market:main"},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "version above max_version",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "example-ext", Version: "1.5.1", Source: "installed", MarketKey: "market:main"},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "market_key mismatch",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          ExtensionIdentity{Extension: "example-ext", Version: "1.2.0", Source: "installed", MarketKey: "market:other"},
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return validGrant() },
			wantNegotiated: false,
		},
		{
			name:           "extension omits auto_confirm (e.g. above its guard limit)",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return nil },
			wantNegotiated: true,
		},
		{
			name:     "empty token falls back to card",
			ctx:      WithOwnerCaller(context.Background()),
			policies: []AutoConfirmPolicy{basePolicy},
			ident:    baseIdent,
			hasIdent: true,
			action:   "example_submit",
			grantFn: func(string) *AutoConfirmGrant {
				return &AutoConfirmGrant{Token: "", ExpiresAt: now.Add(30 * time.Second)}
			},
			wantNegotiated: true,
		},
		{
			name:     "whitespace token falls back to card",
			ctx:      WithOwnerCaller(context.Background()),
			policies: []AutoConfirmPolicy{basePolicy},
			ident:    baseIdent,
			hasIdent: true,
			action:   "example_submit",
			grantFn: func(string) *AutoConfirmGrant {
				return &AutoConfirmGrant{Token: "tok bad", ExpiresAt: now.Add(30 * time.Second)}
			},
			wantNegotiated: true,
		},
		{
			name:     "oversized token falls back to card",
			ctx:      WithOwnerCaller(context.Background()),
			policies: []AutoConfirmPolicy{basePolicy},
			ident:    baseIdent,
			hasIdent: true,
			action:   "example_submit",
			grantFn: func(string) *AutoConfirmGrant {
				return &AutoConfirmGrant{Token: strings.Repeat("a", AutoConfirmTokenMaxLen+1), ExpiresAt: now.Add(30 * time.Second)}
			},
			wantNegotiated: true,
		},
		{
			name:           "expired token falls back to card",
			ctx:            WithOwnerCaller(context.Background()),
			policies:       []AutoConfirmPolicy{basePolicy},
			ident:          baseIdent,
			hasIdent:       true,
			action:         "example_submit",
			grantFn:        func(string) *AutoConfirmGrant { return &AutoConfirmGrant{Token: "tok_exp", ExpiresAt: now} },
			wantNegotiated: true,
		},
		{
			name:     "TTL exceeding policy max_ttl_seconds falls back to card",
			ctx:      WithOwnerCaller(context.Background()),
			policies: []AutoConfirmPolicy{basePolicy},
			ident:    baseIdent,
			hasIdent: true,
			action:   "example_submit",
			grantFn: func(string) *AutoConfirmGrant {
				return &AutoConfirmGrant{Token: "tok_long", ExpiresAt: now.Add(61 * time.Second)}
			},
			wantNegotiated: true,
		},
	}

	for _, tc := range cardFallbackCases {
		t.Run(tc.name, func(t *testing.T) {
			r, c, asked, sent := newEnv(tc.rules, tc.policies, tc.ident, tc.hasIdent, tc.grantFn)
			res := r.Call(tc.ctx, tc.action, json.RawMessage(`{"amount":10}`), func(context.Context, string, json.RawMessage) bool {
				*asked++
				return true
			})
			if res.IsError {
				t.Fatalf("unexpected error: %+v", res)
			}
			if *asked != 1 {
				t.Fatalf("expected 1 phone card, got %d", *asked)
			}
			if c.describeCalls != 1 {
				t.Fatalf("expected Describe to be called once, got %d", c.describeCalls)
			}
			gotProto := c.describeProtocols[0]
			if tc.wantNegotiated && gotProto != ApprovalProtocolGuardedAutoConfirmV1 {
				t.Fatalf("expected negotiated protocol %q, got %q", ApprovalProtocolGuardedAutoConfirmV1, gotProto)
			}
			if !tc.wantNegotiated && gotProto != "" {
				t.Fatalf("expected no approval_protocol negotiation, got %q", gotProto)
			}
			if c.doCalls != 1 || c.doApprovals[0].Mode != "" || c.doApprovals[0].Token != "" {
				t.Fatalf("card fallback must call Do without approval fields: doCalls=%d approvals=%+v", c.doCalls, c.doApprovals)
			}
			if len(*sent) != 1 || (*sent)[0].Through.Kind != ThroughCard || (*sent)[0].Through.AutoConfirm != nil {
				t.Fatalf("card fallback must record ThroughCard: %+v", *sent)
			}
		})
	}
}

func TestGuardedAutoConfirmLegacyConnectorAndExplicitSourceOptIn(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	pinAutoConfirmClock(t, now)

	// Legacy connector without ApprovalActions stays on the card path even when a policy matches.
	f := &fake{id: "example", status: Status{State: StateOK}}
	r := NewRegistry(nil)
	if err := r.Add(legacyIdentFake{fake: f, ident: ExtensionIdentity{Extension: "example-ext", Version: "1.0.0", Source: "installed"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAutoConfirmPolicies([]AutoConfirmPolicy{{
		Extension: "example-ext",
		Connector: "example",
		Actions:   []string{"example_comment"},
	}}); err != nil {
		t.Fatal(err)
	}
	asked := 0
	res := r.Call(WithOwnerCaller(context.Background()), "example_comment", json.RawMessage(`{"text":"hi"}`), func(context.Context, string, json.RawMessage) bool {
		asked++
		return true
	})
	if res.IsError || asked != 1 {
		t.Fatalf("legacy connector: res=%+v asked=%d", res, asked)
	}

	// Explicit sources: ["hub.json"] allows a hub.json extension when configured, and invalid update fails closed.
	c := &approvalFake{
		id:         "example",
		actions:    []string{"example_submit"},
		confirmAll: []string{"example_submit"},
		ident:      ExtensionIdentity{Extension: "local-ext", Version: "1.0.0", Source: "hub.json"},
		hasIdent:   true,
		describeHook: func(_ context.Context, name string, _ json.RawMessage, protocol string) (Action, string, *AutoConfirmGrant, error) {
			if protocol == ApprovalProtocolGuardedAutoConfirmV1 {
				return Action{Name: name, Visible: "example service"}, "Submit", &AutoConfirmGrant{Token: "tok_local", ExpiresAt: now.Add(30 * time.Second)}, nil
			}
			return Action{Name: name, Visible: "example service"}, "Submit", nil, nil
		},
	}
	r2 := NewRegistry(nil)
	if err := r2.Add(c); err != nil {
		t.Fatal(err)
	}
	if err := r2.SetAutoConfirmPolicies([]AutoConfirmPolicy{{
		Extension: "local-ext",
		Connector: "example",
		Actions:   []string{"example_submit"},
		Sources:   []string{"hub.json"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Invalid replacement must fail and keep previous valid policy intact.
	if err := r2.SetAutoConfirmPolicies([]AutoConfirmPolicy{{Extension: "*", Connector: "example", Actions: []string{"example_submit"}}}); err == nil {
		t.Fatal("expected invalid SetAutoConfirmPolicies to fail")
	}
	asked = 0
	res = r2.Call(WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{}`), func(context.Context, string, json.RawMessage) bool {
		asked++
		return true
	})
	if res.IsError || asked != 0 {
		t.Fatalf("opted-in hub.json source: res=%+v asked=%d", res, asked)
	}
}
