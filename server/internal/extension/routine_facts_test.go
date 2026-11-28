package extension

import (
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestRoutineFactsConsent(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		delete(m, "activation_events")
		m["contributes"] = map[string]any{"routine_facts": []map[string]string{{"id": "outlook", "title": "Outlook"}}}
		m["permissions"] = map[string]bool{"routine_facts": true}
	}))
	if err != nil {
		t.Fatal(err)
	}
	items := m.RoutineFacts()
	if len(items) != 1 || items[0].ID != "outlook" || items[0].Title != "Outlook" || PointVersions()["routine_facts"] != 1 {
		t.Fatalf("wrong routine provider or advertised point version: %v", items)
	}
	old := *m
	old.Permissions.RoutineFacts = false
	if old.Permissions.Covers(m.Permissions) || !m.Permissions.Covers(old.Permissions) || Digest(&old) == Digest(m) {
		t.Fatal("adding routine_facts must require a new grant")
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		hasGrant := func(lines []PermissionLine) bool {
			return slices.ContainsFunc(lines, func(line PermissionLine) bool {
				return line.Key == "routine_facts" && line.Title != "" && line.Detail != "" &&
					!strings.Contains(line.Title+line.Detail, "extension-permissions.")
			})
		}
		if !hasGrant(ExplainIn(lang, m)) || !hasGrant(AddedIn(lang, &old, m)) || hasGrant(AddedIn(lang, m, m)) {
			t.Errorf("%s: missing or repeated routine_facts disclosure", lang)
		}
	}
}

func TestRoutineFactsInvalidDeclarations(t *testing.T) {
	item := map[string]string{"id": "outlook", "title": "Outlook"}
	for _, tc := range []struct {
		name    string
		items   []map[string]string
		granted bool
		runtime bool
		want    string
	}{
		{"grant", []map[string]string{item}, false, true, "permissions.routine_facts"},
		{"runtime", []map[string]string{item}, true, false, "runtime that speaks the protocol"},
		{"duplicate", []map[string]string{item, item}, true, true, "twice"},
		{"id", []map[string]string{{"id": "../provider", "title": "Outlook"}}, true, true, "invalid id"},
		{"name", []map[string]string{{"id": "outlook", "title": " "}}, true, true, "title"},
		{"control", []map[string]string{{"id": "outlook", "title": "first\nsecond"}}, true, true, "title"},
		{"length", []map[string]string{{"id": "outlook", "title": strings.Repeat("a", 81)}}, true, true, "title"},
		{"extra", []map[string]string{{"id": "outlook", "title": "Outlook", "prompt": "extra"}}, true, true, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(with(t, func(m map[string]any) {
				delete(m, "activation_events")
				m["contributes"] = map[string]any{"routine_facts": tc.items}
				m["permissions"] = map[string]bool{"routine_facts": tc.granted}
				if !tc.runtime {
					delete(m, "runtime")
				}
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
