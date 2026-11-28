package assistant

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const webFontsPath = "/api/v1/settings/web-fonts"

func TestWebFontSettingsValidationAndDefaults(t *testing.T) {
	s, _ := navigationService(t, newSettingsBoardStore())
	w := boardCall(s, "GET", webFontsPath, "", roleAnswer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sans":{"preset":"default"},"mono":{"preset":"default"}`) {
		t.Fatalf("defaults: %d %s", w.Code, w.Body)
	}
	w = boardCall(s, "PUT", webFontsPath, `{"sans":{"preset":"inter"}}`, roleAnswer)
	if w.Code != http.StatusForbidden {
		t.Fatalf("answer access changed fonts: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{
		`{}`, `{"sans":null}`, `{"sans":{"preset":"inter"},"mono":{"preset":"menlo"}}`,
		`{"sans":{"preset":"__proto__"}}`, `{"mono":{"preset":"menlo","family":"Courier"}}`,
		`{"sans":{"preset":"custom","family":"I; color:red"}}`, `{"mono":{"preset":"custom","family":"\u0000"}}`,
		`{"sans":{"preset":"custom","family":"` + strings.Repeat("A", 101) + `"}}`,
	} {
		if w := boardCall(s, "PUT", webFontsPath, body, roleFull); w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body)
		}
	}
}

func TestWebFontSettingsPersistAcrossManagers(t *testing.T) {
	database := todoTestDB(t)
	a, _ := navigationService(t, database)
	b, _ := navigationService(t, database)
	for _, body := range []string{`{"sans":{"preset":"custom","family":"  A \"B\"  "}}`, `{"mono":{"preset":"jetbrains"}}`} {
		if w := boardCall(a, "PUT", webFontsPath, body, roleFull); w.Code != http.StatusOK {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
	}
	w := boardCall(b, "GET", webFontsPath, "", roleFull)
	want := webFonts{Sans: webFontChoice{Preset: "custom", Family: `A "B"`}, Mono: webFontChoice{Preset: "jetbrains"}}
	var got webFonts
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil || got != want {
		t.Fatalf("second service: %d %+v (%v), want %+v", w.Code, got, err, want)
	}
	w = boardCall(b, "PUT", webFontsPath, `{"sans":{"preset":"default"}}`, roleFull)
	want.Sans = webFontChoice{Preset: "default"}
	got = webFonts{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil || got != want {
		t.Fatalf("reset one choice: %d %+v (%v), want %+v", w.Code, got, err, want)
	}
}
