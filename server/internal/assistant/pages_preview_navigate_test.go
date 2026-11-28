package assistant

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A draft page whose code can be followed (UI version 13's navigate) is previewed read-only: the navigate goes with
// the other actions, so the preview passes the assistant card's checks and keeps the code instead of falling back to
// plain text.
func TestPagePreviewDropsCodeNavigate(t *testing.T) {
	page := &wire.View{Fallback: "main.go", Sections: []wire.Section{{Key: "code", Items: []wire.Component{{
		Type: wire.ComponentCode, Key: "code", Path: "main.go", Text: "package main\n",
		Navigate: &wire.Action{ID: "nav"},
	}}}}}
	v := pagePreview(page, "Code", "Read the code.", i18n.En)
	if _, err := wire.ParseAssistantView(pageJSON(v)); err != nil {
		t.Fatalf("preview invalid: %v", err)
	}
	if len(v.Sections) == 0 || len(v.Sections[0].Items) == 0 {
		t.Fatalf("preview has no sections: %+v", v)
	}
	code := v.Sections[0].Items[0]
	if code.Type != wire.ComponentCode || code.Navigate != nil || code.Text != "package main\n" {
		t.Fatalf("want the code without navigate, got %+v", code)
	}
	if page.Sections[0].Items[0].Navigate == nil {
		t.Fatal("the preview changed the draft itself")
	}
}
