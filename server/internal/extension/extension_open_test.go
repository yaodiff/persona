package extension

import "testing"

// A full extension page may open an existing Persona session or to-do; a card may not, and other targets stay stripped.
func TestFullPageKeepsSessionAndTaskOpen(t *testing.T) {
	page, ok := Slot("nav_page")
	if !ok || page.Size != SizeFull {
		t.Fatalf("nav_page slot: %+v", page)
	}
	card, ok := Slot("overview_card")
	if !ok || card.Size != SizeCompact {
		t.Fatalf("overview_card slot: %+v", card)
	}
	view := func(target string) *View {
		return &View{Sections: []Section{{Items: []UIElem{
			{Type: "text", Text: "before"},
			{Type: "button", Title: "go", Action: &UIAction{ID: "go", Open: target}},
		}}}}
	}

	for _, name := range []string{"nav_page", "extension_page", "plugin_page"} {
		page, ok := Slot(name)
		if !ok || !FullPage(name) {
			t.Fatalf("%s slot: %+v", name, page)
		}
		v := view("persona://session/machine-1/session-1")
		if _, err := Fit(v, page, UIVersion); err != nil {
			t.Fatal(err)
		}
		if a := v.Sections[0].Items[1].Action; a == nil || a.Open == "" {
			t.Fatalf("%s lost its session open", name)
		}
	}

	v := view("persona://task/todo-1")
	if _, err := Fit(v, page, UIVersion); err != nil {
		t.Fatal(err)
	}
	if a := v.Sections[0].Items[1].Action; a == nil || a.Open == "" {
		t.Fatal("nav_page lost its task open")
	}

	v = view("persona://session/machine-1/session-1")
	if _, err := Fit(v, card, UIVersion); err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 1 {
		t.Fatalf("a card kept a session-open button: %#v", v.Sections[0].Items)
	}

	for _, name := range []string{"launcher_section", "machine_section", "inbox_item"} {
		section, ok := Slot(name)
		if !ok || section.Size != SizeFull || FullPage(name) {
			t.Fatalf("%s slot: %+v", name, section)
		}
		v = view("persona://session/machine-1/session-1")
		if _, err := Fit(v, section, UIVersion); err != nil {
			t.Fatal(err)
		}
		if len(v.Sections[0].Items) != 1 {
			t.Fatalf("%s kept a page-only session-open button: %#v", name, v.Sections[0].Items)
		}
	}

	v = view("https://example.com/one")
	if _, err := Fit(v, page, UIVersion); err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 1 {
		t.Fatalf("an https open survived on a page: %#v", v.Sections[0].Items)
	}
}
