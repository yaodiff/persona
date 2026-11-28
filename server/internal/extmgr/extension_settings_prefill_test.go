package extmgr

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func settingsPrefillManifest() *extension.Manifest {
	return &extension.Manifest{
		Configuration: json.RawMessage(`{
			"type":"object",
			"properties":{
				"host":{"type":"string"},
				"port":{"type":"integer","minimum":1,"maximum":65535},
				"enabled":{"type":"boolean"},
				"repos":{"type":"array","items":{"type":"string"}},
				"limits":{"type":"object","properties":{"max":{"type":"integer"},"label":{"type":"string"}}},
				"api_key":{"type":"string"},
				"accounts":{"type":"array","maxItems":2,"x-secret":{"name":"entry_key","title":"Entry key"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string"}}}}
			},
			"required":["host"]
		}`),
		Secrets: []extension.SecretSpec{{Name: "api_key", Title: "API key"}},
	}
}

func settingsPrefillLink(id, patch string) string {
	return extension.SettingsURL(id) + "?set=" + url.QueryEscape(patch)
}

func TestOpenOwnSettingsPrefill(t *testing.T) {
	m := settingsPrefillManifest()
	page := extension.UIItem{ID: "page", Placement: "nav_page"}
	for _, patch := range []string{
		`{}`,
		`{"host":"new.example.com"}`,
		`{"port":8080,"enabled":true,"repos":["~/code"],"limits":{"max":5,"label":"Main"}}`,
		`{"host":"` + strings.Repeat("例", 500) + `"}`,
	} {
		if err := checkOpenURL("example-gateway", m, page, "", settingsPrefillLink("example-gateway", patch)); err != nil {
			t.Errorf("%s: %v", patch, err)
		}
	}
	if err := checkOpenURL("example-gateway", m, page, "", extension.SettingsURL("example-gateway")); err != nil {
		t.Fatalf("the plain own-settings link: %v", err)
	}
}

func TestOpenOwnSettingsPrefillRefusesWhatIsNotOrdinaryOwnConfiguration(t *testing.T) {
	m := settingsPrefillManifest()
	page := extension.UIItem{ID: "page", Placement: "nav_page"}
	base := extension.SettingsURL("example-gateway")
	for _, raw := range []string{
		settingsPrefillLink("example-notes", `{"host":"other.example.com"}`),
		base + "/../example-notes?set=%7B%7D",
		base + "/?set=%7B%7D",
		base + "?set=",
		base + "?set=%7B%7D&x=1",
		base + "?set=%7B%7D&set=%7B%7D",
		base + "?set=%7B%7D#fragment",
		"persona://user:pass@settings/extensions/example-gateway?set=%7B%7D",
		base + "?set=%7B",
		base + "?set=%5B%5D",
		base + "?set=null",
		settingsPrefillLink("example-gateway", `{"unknown":"x"}`),
		settingsPrefillLink("example-gateway", `{"api_key":"secret"}`),
		settingsPrefillLink("example-gateway", `{"accounts":[{"name":"one"}]}`),
		settingsPrefillLink("example-gateway", `{"limits":{"unknown":1}}`),
		settingsPrefillLink("example-gateway", `{"port":"8080"}`),
		settingsPrefillLink("example-gateway", `{"port":70000}`),
		settingsPrefillLink("example-gateway", `{"host":null}`),
		base + "?set=" + strings.Repeat("a", settingsPrefillMost+1),
	} {
		if err := checkOpenURL("example-gateway", m, page, "", raw); err == nil {
			t.Errorf("%s: allowed", raw)
		}
	}
	if err := checkOpenURL("example-gateway", nil, page, "", settingsPrefillLink("example-gateway", `{"host":"new.example.com"}`)); err == nil {
		t.Error("a prefill without a manifest was allowed")
	}
}
