package extension

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// lark.messages reaches only enabled, compatible extensions that subscribed to the topic AND hold an explicit
// lark.messages.read grant. Other scopes (such as kb.read or inbox.read) and legacy hub_api full access do not
// cover it.
func TestLarkMessagesEventSubscribersNeedExplicitScope(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for _, tc := range []struct {
		id        string
		grant     Permissions
		subscribe string
		enabled   bool
		problem   string
	}{
		{"lark-reader", Permissions{Hub: []string{ScopeLarkMessagesRead}}, `"lark.messages"`, true, ""},
		{"lark-and-kb", Permissions{Hub: []string{ScopeLarkMessagesRead, ScopeKBRead}}, `"lark.messages","kb.changed"`, true, ""},
		{"kb-only", Permissions{Hub: []string{ScopeKBRead}}, `"lark.messages"`, true, ""},
		{"inbox-only", Permissions{Hub: []string{ScopeInboxRead}}, `"lark.messages"`, true, ""},
		{"none", Permissions{}, `"lark.messages"`, true, ""},
		{"legacy", Permissions{HubAPI: true}, `"lark.messages"`, true, ""},
		{"not-subscribed", Permissions{Hub: []string{ScopeLarkMessagesRead}}, `"agent.finished"`, true, ""},
		{"disabled", Permissions{Hub: []string{ScopeLarkMessagesRead}}, `"lark.messages"`, false, ""},
		{"broken", Permissions{Hub: []string{ScopeLarkMessagesRead}}, `"lark.messages"`, true, "incompatible"},
	} {
		m := &Manifest{
			ID:          tc.id,
			Version:     "1.0.0",
			Permissions: Permissions{Hub: []string{ScopeLarkMessagesRead, ScopeKBRead}},
			Contributes: map[string]json.RawMessage{
				"events": json.RawMessage(`[{"id":"example","subscribe":[` + tc.subscribe + `]}]`),
			},
		}
		if err := r.AddBuiltin(m, t.TempDir(), tc.enabled); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[tc.id].Record.Granted = tc.grant
		r.extensions[tc.id].Problem = tc.problem
		r.mu.Unlock()
	}

	if EventScopes["lark.messages"] != ScopeLarkMessagesRead {
		t.Fatalf("EventScopes[lark.messages] = %q, want %q", EventScopes["lark.messages"], ScopeLarkMessagesRead)
	}
	if InEveryScope(ScopeLarkMessagesRead) {
		t.Fatalf("%s must be named-only (excluded from InEveryScope)", ScopeLarkMessagesRead)
	}
	want := []string{"lark-and-kb", "lark-reader"}
	if got := r.EventSubscribers("lark.messages", EventScopes["lark.messages"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("lark.messages subscribers = %v, want %v", got, want)
	}
}

// Subscribing to lark.messages and requesting lark.messages.read require explicit owner consent on install and on
// upgrades (including upgrades from a legacy hub_api grant), with localized explanations in every supported language.
func TestLarkMessagesEventAndScopeRequireConsent(t *testing.T) {
	old := with(t, func(m map[string]any) {
		m["activation_events"] = []any{"on_startup"}
		m["permissions"] = map[string]any{"hub_api": true}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "ev", "subscribe": []any{"agent.finished"}}}}
	})
	next := with(t, func(m map[string]any) {
		m["activation_events"] = []any{"on_startup"}
		m["permissions"] = map[string]any{"hub": []any{ScopeLarkMessagesRead}}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "ev", "subscribe": []any{"lark.messages"}}}}
	})
	before, err := Parse(old)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse(next)
	if err != nil {
		t.Fatal(err)
	}
	installed := &Extension{Record: Record{Granted: before.Permissions, GrantedDigest: Digest(before)}, Manifest: before, Full: before}
	if !MorePermissions(installed, after) {
		t.Fatal("upgrading from legacy hub_api to lark.messages.read + lark.messages must require consent")
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		scopeTitle, ok := ScopeTitleIn(lang, ScopeLarkMessagesRead)
		if !ok || strings.Contains(scopeTitle, "extension-permissions.") {
			t.Fatalf("%s missing scope title: %q", lang, scopeTitle)
		}
		eventText := permissionText(lang, "event_lark_messages")
		if strings.Contains(eventText, "extension-permissions.") {
			t.Fatalf("%s missing event text: %q", lang, eventText)
		}
		added := AddedIn(lang, before, after)
		hasScope := slices.ContainsFunc(added, func(l PermissionLine) bool {
			return l.Key == "hub" && strings.Contains(l.Detail, scopeTitle)
		})
		hasEvent := slices.ContainsFunc(added, func(l PermissionLine) bool {
			return l.Key == "events" && strings.Contains(l.Detail, eventText)
		})
		if !hasScope || !hasEvent {
			t.Fatalf("%s upgrade consent lines missing scope=%v event=%v: %+v", lang, hasScope, hasEvent, added)
		}
	}
}
