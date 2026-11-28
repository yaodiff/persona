package extmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestComposeLinkShape(t *testing.T) {
	for _, ok := range []struct {
		raw  string
		id   string
		text string
	}{
		{"persona://compose?prompt=today&text=What%20happened%20today%3F", "today", "What happened today?"},
		{"persona://compose/?prompt=today&text=What%20happened%20today%3F", "today", "What happened today?"},
		{"jarvis://compose?prompt=today&text=What%20happened%20today%3F", "today", "What happened today?"},
		{"persona://compose?prompt=today%2Dplan&text=today-plan", "today-plan", "today-plan"},
	} {
		id, text, got := composePrompt(ok.raw)
		if !got || id != ok.id || text != ok.text {
			t.Errorf("%s: %q %q %v", ok.raw, id, text, got)
		}
	}
	for _, bad := range []string{
		"persona://compose",
		"persona://compose?prompt=",
		"persona://compose?prompt=a",
		"persona://compose?text=a",
		"persona://compose?prompt=%20",
		"persona://compose?prompt=a&text=",
		"persona://compose?prompt=a&text=%20%20",
		"persona://compose?prompt=a&text=a&x=1",
		"persona://compose/x?prompt=a&text=a",
		"persona://user:pass@compose?prompt=a&text=a",
		"persona://compose?prompt=a&text=a#x",
		"https://compose?prompt=a",
		"persona://compose?prompt=a&prompt=b&text=a",
		"persona://compose?prompt=" + strings.Repeat("a", 65) + "&text=a",
		"persona://compose?prompt=a&text=" + strings.Repeat("a", 8001),
	} {
		if id, text, ok := composePrompt(bad); ok {
			t.Errorf("%q: allowed as %q %q", bad, id, text)
		}
	}
}

func TestComposeLinkNeedsItsOwnQuickPrompt(t *testing.T) {
	m := &extension.Manifest{Contributes: map[string]json.RawMessage{
		"prompts": json.RawMessage(`[
			{"id":"today","kind":"quick","title":"Today","text":"What happened today?"},
			{"id":"plan","kind":"instructions","file":"plan.md"}
		]`),
	}}
	page := extension.UIItem{ID: "page", Placement: "extension_page"}
	for _, ok := range []string{
		"persona://compose?prompt=today&text=What%20happened%20today%3F",
		"jarvis://compose/?prompt=today&text=What%20happened%20today%3F",
	} {
		if err := checkOpenURL("example", m, page, "", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"persona://compose?prompt=missing&text=What%20happened%20today%3F",
		"persona://compose?prompt=plan&text=plan",
		"persona://compose?prompt=another-extension-prompt&text=What%20happened%20today%3F",
		"persona://compose?prompt=today&text=Something%20else",
		"persona://compose?prompt=today&prompt=plan&text=What%20happened%20today%3F",
	} {
		if err := checkOpenURL("example", m, page, "", bad); err == nil {
			t.Errorf("%s: allowed", bad)
		}
	}
	if err := checkOpenURL("example", nil, page, "", "persona://compose?prompt=today&text=What%20happened%20today%3F"); err == nil {
		t.Error("a compose link without a manifest was allowed")
	}
	if err := checkOpenURL("example", m, page, "", "https://example.com"); err != nil {
		t.Errorf("https: %v", err)
	}
}
