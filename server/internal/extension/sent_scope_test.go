package extension

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestSentReadShownOnInstallAndUpgrade(t *testing.T) {
	old := &Manifest{ID: "example", Version: "1.0.0", Permissions: Permissions{Hub: []string{ScopeKBRead}}}
	next := &Manifest{ID: "example", Version: "1.1.0", Permissions: Permissions{Hub: []string{ScopeKBRead, ScopeSentRead}}}
	installed := &Extension{Manifest: old, Full: old, Record: Record{Granted: old.Permissions, GrantedDigest: Digest(old)}}
	if !MorePermissions(installed, next) || Digest(old) == Digest(next) {
		t.Fatal("adding sent.read must require fresh consent")
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		title, ok := ScopeTitleIn(lang, ScopeSentRead)
		if !ok || title == "" || strings.Contains(title, "extension-permissions.") {
			t.Fatalf("%s: missing sent.read wording: %q", lang, title)
		}
		for _, lines := range [][]PermissionLine{ExplainIn(lang, next), AddedIn(lang, old, next)} {
			if !slices.ContainsFunc(lines, func(line PermissionLine) bool {
				return line.Key == "hub" && strings.Contains(line.Detail, title)
			}) {
				t.Errorf("%s: sent.read missing from consent: %+v", lang, lines)
			}
		}
	}
}

func TestSentReadChecksGrantedScope(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for _, tc := range []struct {
		id    string
		grant Permissions
		want  bool
	}{
		{"declared-only", Permissions{Hub: []string{ScopeKBRead}}, false},
		{"granted", Permissions{Hub: []string{ScopeKBRead, ScopeSentRead}}, true},
		{"legacy", Permissions{HubAPI: true}, true},
	} {
		m := &Manifest{ID: tc.id, Version: "1.0.0", Permissions: Permissions{Hub: []string{ScopeKBRead, ScopeSentRead}},
			Contributes: map[string]json.RawMessage{"events": json.RawMessage(`[{"id":"example","subscribe":["kb.changed"]}]`)}}
		if err := r.AddBuiltin(m, t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[tc.id].Record.Granted = tc.grant
		r.mu.Unlock()
		if got := r.HubScopeAllowed(tc.id, ScopeSentRead); got != tc.want {
			t.Errorf("%s: sent.read = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestKnowledgeImportScopeAlwaysNeedsSeparateConsent(t *testing.T) {
	old := &Manifest{ID: "example", Version: "1.0.0", Permissions: Permissions{HubAPI: true}}
	next := &Manifest{ID: "example", Version: "2.0.0", Permissions: Permissions{Hub: []string{ScopeKBImportStatus, ScopeSentRead}}}
	installed := &Extension{Manifest: old, Full: old, Record: Record{Granted: old.Permissions, GrantedDigest: Digest(old)}}
	if !MorePermissions(installed, next) || Digest(old) == Digest(next) || slices.Contains(EveryScope(), ScopeKBImportStatus) {
		t.Fatal("a legacy full-hub grant must not carry import progress access on upgrade")
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		title, ok := ScopeTitleIn(lang, ScopeKBImportStatus)
		if !ok || title == "" || strings.Contains(title, "extension-permissions.") {
			t.Fatalf("%s: missing import scope translation", lang)
		}
		for _, lines := range [][]PermissionLine{ExplainIn(lang, next), AddedIn(lang, old, next)} {
			if !slices.ContainsFunc(lines, func(l PermissionLine) bool { return l.Key == "hub" && strings.Contains(l.Detail, title) }) {
				t.Fatalf("%s: missing import scope from consent", lang)
			}
		}
	}
}
