package extension

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func navigateView() *View {
	return &View{Sections: []Section{{Items: []UIElem{{
		Type: wire.ComponentCode, Key: "code", Path: "main.go", Text: "package main\n", StartLine: 1,
		Navigate: &wire.Action{ID: "nav"},
	}}}}}
}

// An app of UI version 13 keeps a code's navigate; an older one, or a native stream, gets the same code without it.
func TestNavigateIsLeftOutBelow13(t *testing.T) {
	page, _ := Slot("nav_page")
	v := navigateView()
	if _, err := Fit(v, page, 13); err != nil {
		t.Fatal(err)
	}
	if e := items(v)[0]; e.Navigate == nil || e.Navigate.ID != "nav" {
		t.Fatalf("13 keeps navigate: %+v", e)
	}
	for _, version := range []int{4, 5, 12} {
		v := navigateView()
		if _, err := Fit(v, page, version); err != nil {
			t.Fatalf("%d: %v", version, err)
		}
		if e := items(v)[0]; e.Type != wire.ComponentCode || e.Navigate != nil || e.Text != "package main\n" {
			t.Fatalf("%d: %+v", version, e)
		}
	}
	v = navigateView()
	if _, err := fit(v, page, 13, nil, true); err != nil {
		t.Fatal(err)
	}
	if items(v)[0].Navigate != nil {
		t.Fatal("a native stream has no navigate")
	}
}

// A native stream view may carry a code's navigate (stream.schema.json lists it): DecodeCheckedStreamViewJSON takes it,
// and FitNativeFor's downgrade13 leaves it out of what the native app gets.
func TestFitNativeStripsNavigate(t *testing.T) {
	page, _ := Slot("nav_page")
	raw := []byte(`{"sections":[{"key":"s","items":[{"key":"code","type":"code","path":"main.go","text":"package main\n","navigate":{"id":"nav"}}]}]}`)
	if _, err := wire.DecodeCheckedStreamViewJSON(raw); err != nil {
		t.Fatalf("a stream view with navigate is refused: %v", err)
	}
	got, err := FitNativeFor(raw, page, UIVersion, nil, i18n.En)
	if err != nil {
		t.Fatalf("FitNativeFor: %v", err)
	}
	if strings.Contains(string(got), "navigate") || !strings.Contains(string(got), `"type":"code"`) || !strings.Contains(string(got), "package main") {
		t.Fatalf("want the code without navigate, got %s", got)
	}
}
