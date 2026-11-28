package extension

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestPermissionLanguagePreservesConsentAndAuthoredData(t *testing.T) {
	m, err := Parse(withNode(t, func(node, _ map[string]any) {
		node["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "files": true, "macos_privacy": true}
	}))
	if err != nil {
		t.Fatal(err)
	}
	m.Permissions = Permissions{Network: []string{"api.example.com"}, Hub: append(HubScopes, ScopeCallPrefix+"example"),
		LauncherKeys: true, GhLogin: true, UsageGateways: true, AgentRuntimes: true}
	m.Secrets[0].Title = "草稿" // Authored data can equal a hub-owned label.
	m.Contributes["ui"] = json.RawMessage(`[{"id":"nav","title":"草稿","placement":"nav_page"},{"id":"card","title":"草稿","placement":"capsule_card"}]`)
	m.Contributes["events"] = json.RawMessage(`[{"id":"events","subscribe":["kb.changed","kb.forgotten","inbox.changed","agent.needs_you","agent.finished","todo.changed","todo.decision","lark.messages"]}]`)
	m.Tools = []ToolSpec{{Name: "example-cli", Title: "草稿", Version: "1.0.0", NPM: &ToolNPM{Package: "example-cli"}}}
	before, _ := json.Marshal(m)
	digest, canonical := Digest(m), Explain(m)
	old := &Manifest{}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		lines := ExplainIn(l, m)
		if len(lines) != len(canonical) {
			t.Fatalf("%s changed consent lines", l)
		}
		for i, line := range lines {
			if line.Key != canonical[i].Key || strings.Contains(line.Title+line.Detail, "extension-permissions.") {
				t.Fatalf("%s invalid permission %v", l, line)
			}
			if line.Key == "runs_code" && line.Title != i18n.T(l, "extension-permissions.runs_code_title") {
				t.Fatal("program permission not translated")
			}
			if line.Key == "ui_surfaces" && (!strings.Contains(line.Detail, "草稿") || !strings.Contains(line.Detail, i18n.T(l, "extension-permissions.slot_nav_page"))) {
				t.Fatal("placement copy or authored title changed")
			}
			if line.Key == "secrets" && line.Detail != "草稿" {
				t.Fatal("authored secret title translated")
			}
		}
		if l == i18n.ZhHans && !reflect.DeepEqual(lines, canonical) {
			t.Fatal("legacy language changed")
		}
		for _, line := range AddedIn(l, old, m) {
			if strings.Contains(line.Title+line.Detail, "extension-permissions.") {
				t.Fatalf("missing upgrade translation: %+v", line)
			}
		}
		full := *m
		full.Permissions.HubAPI, full.Permissions.Hub = true, nil
		if ExplainIn(l, &full)[2].Title != i18n.T(l, "extension-permissions.hub_api_title") {
			t.Fatal("legacy full-token disclosure missing")
		}
		for _, slot := range UISlots {
			if slot.Shown && strings.HasPrefix(permissionText(l, "slot_"+slot.Name), "extension-permissions.") {
				t.Fatal("missing slot translation", slot.Name)
			}
		}
	}
	after, _ := json.Marshal(m)
	if string(before) != string(after) || Digest(m) != digest {
		t.Fatal("presentation changed manifest or digest")
	}
}

func TestPreviewLanguageCopiesCapturedConsent(t *testing.T) {
	m, err := Parse([]byte(exampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	old := *m
	old.Permissions.Network = nil
	pv := &Preview{ID: "example", Manifest: m, previous: &old, Permissions: Explain(m),
		AddedPermissions: Added(&old, m), Digest: Digest(m)}
	pv.Bundle = []*Preview{{Manifest: m, Permissions: Explain(m), Digest: Digest(m)}}
	before, _ := json.Marshal(pv)
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		out := pv.In(l)
		if out == pv || out.Manifest != m || out.Digest != pv.Digest || out.Bundle[0] == pv.Bundle[0] {
			t.Fatal("preview changed consent identity or returned original")
		}
		if !reflect.DeepEqual(out.Permissions, ExplainIn(l, m)) || !reflect.DeepEqual(out.AddedPermissions, AddedIn(l, &old, m)) {
			t.Fatal("preview not regenerated from captured consent")
		}
		out.Permissions[0].Title = "changed response"
	}
	after, _ := json.Marshal(pv)
	if string(before) != string(after) || pv.In("") != pv {
		t.Fatal("stored or legacy preview changed")
	}
}

func TestUIFallbackLanguageAndAuthoredContent(t *testing.T) {
	page, _ := Slot("nav_page")
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		v := codeExample(t, "code-review-v5.json")
		if _, err := Fit(v, page, 4, l); err != nil {
			t.Fatal(err)
		}
		if v.Sections[0].Items[0].Text != i18n.T(l, "extension-ui-fallback.review_drafts", i18n.Args{"count": 2}) ||
			v.Sections[0].Items[2].Title != i18n.T(l, "extension-ui-fallback.discard") {
			t.Fatalf("%s review fallback not localized", l)
		}
		v = &View{Sections: []Section{{More: &wire.More{Action: UIAction{ID: "more"}}, Items: []UIElem{
			{Type: "loading"}, {Type: "loading", Title: "草稿"}, {Type: "text", Text: "草稿"},
		}}}}
		if _, err := Fit(v, page, 1, l); err != nil {
			t.Fatal(err)
		}
		items := v.Sections[0].Items
		if items[0].Text != i18n.T(l, "extension-ui-fallback.loading") || items[3].Title != i18n.T(l, "extension-ui-fallback.load_more") {
			t.Fatal("loading or pagination fallback not localized")
		}
		if items[1].Text != "草稿" || items[2].Text != "草稿" {
			t.Fatal("extension-authored content translated")
		}
		widget := &View{Sections: []Section{{Items: []UIElem{{Type: "loading"}}}}}
		if _, err := FitWidget(widget, "small", 5, l); err != nil || widget.Sections[0].Items[0].Text != i18n.T(l, "extension-ui-fallback.loading") {
			t.Fatalf("%s widget fallback: %+v, %v", l, widget, err)
		}
	}
}
