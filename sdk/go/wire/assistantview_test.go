package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAssistantExamples(t *testing.T) {
	files, _ := filepath.Glob("../../../schema/extension/examples/assistant/*.json")
	if len(files) < 7 {
		t.Fatal("assistant examples missing")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			v, err := ParseAssistantView(raw)
			if err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			_ = json.Unmarshal(raw, &want)
			_ = json.Unmarshal(out, &got)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip differs\nwant %s\n got %s", raw, out)
			}
		})
	}
}

func TestAssistantViewRefuses(t *testing.T) {
	cases := map[string]string{
		"null":                  `null`,
		"fallback":              `{"sections":[]}`,
		"blank fallback":        `{"fallback":"  ","sections":[]}`,
		"bad component":         `{"fallback":"summary","sections":[{"items":[{"type":"text"}]}]}`,
		"unknown component":     `{"fallback":"summary","sections":[{"items":[{"type":"future"}]}]}`,
		"bare extension action": `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"choose","action":{"id":"x"}}]}]}`,
		"two actions":           `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"choose","action":{"id":"x","reply":"yes","pop":true}}]}]}`,
		"submit open":           `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"choose","action":{"id":"x","open":"https://example.com","submit":true}}]}]}`,
		"missing page":          `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"detail","action":{"id":"x","push":{"page":"missing"}}}]}]}`,
		"extension push":        `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"detail","action":{"id":"x","push":{"page":"p","ui":"other"}}}]}],"pages":{"p":{"sections":[]}}}`,
		"nested pages":          `{"fallback":"summary","sections":[],"pages":{"p":{"sections":[],"pages":{"q":{"sections":[]}}}}}`,
		"bad page name":         `{"fallback":"summary","sections":[],"pages":{"../p":{"sections":[]}}}`,
		"duplicate action":      `{"fallback":"summary","sections":[{"items":[{"type":"button","title":"one","action":{"id":"x","reply":"one"}}]}],"pages":{"p":{"sections":[{"items":[{"type":"button","title":"two","action":{"id":"x","reply":"two"}}]}]}}}`,
		"secret":                `{"fallback":"summary","sections":[{"items":[{"type":"field","name":"password","label":"Password","kind":"secret"}]}]}`,
		"bad html height":       `{"fallback":"summary","sections":[{"items":[{"type":"html","html":"<p>ok</p>","height":9999}]}]}`,
		"wrong row cells":       `{"fallback":"summary","sections":[{"items":[{"type":"table","columns":[{"title":"x"}],"rows":[["a","b"]]}]}]}`,
		"deep JSON":             strings.Repeat(`{"pages":{"p":`, 20) + `{}` + strings.Repeat(`}}`, 20),
		"large":                 `{"fallback":"summary","sections":[],"ignored":"` + strings.Repeat("a", AssistantViewMost) + `"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAssistantView([]byte(raw)); err == nil {
				t.Fatal("accepted invalid assistant view")
			}
		})
	}
}

func TestAssistantComponentShorthand(t *testing.T) {
	v, err := ParseAssistantView([]byte(`{"type":"metric","label":"Count","value":"3","fallback":"Count is 3"}`))
	if err != nil || v.Fallback != "Count is 3" || len(v.Sections) != 1 || v.Sections[0].Items[0].Type != "metric" {
		t.Fatalf("got %#v, %v", v, err)
	}
	if _, err := ParseAssistantView([]byte(`{"type":"metric","label":"Count","value":"3"}`)); err == nil {
		t.Fatal("accepted shorthand without fallback")
	}
}

func TestAssistantOpen(t *testing.T) {
	for _, target := range []string{"https://example.com/page", "persona://session/example-machine/abc", "persona://session/example-machine/abc", "persona://task/task_1", "persona://kb/notes/example.md", "persona://file/example-machine/%2Ftmp%2Fexample.txt"} {
		if err := CheckAssistantOpen(target); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"javascript:alert(1)", "http://example.com", "https://user:pass@example.com", "https://:443", "https://example.com\n", "persona://settings", "persona://session/a", "persona://session/a/b/c", "persona://task/a?x=1", "persona://kb/../secret", "persona://kb/%2e%2e/secret", "persona://kb/a%2Fb", "persona://kb/%E0%", "persona://file/machine/relative", "persona://file/a%2Fb/%2Ftmp", "persona://task/%0a"} {
		if err := CheckAssistantOpen(target); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}

func TestStripAssistantFields(t *testing.T) {
	v := View{Fallback: "summary", Pages: map[string]View{"detail": {Sections: []Section{}}}, Sections: []Section{{Items: []Component{
		{Type: "row", Title: "readable", Action: &Action{ID: "reply", Reply: "yes"}},
		{Type: "button", Title: "reply", Action: &Action{ID: "reply-button", Reply: "yes"}},
		{Type: "html", Html: "<p>untrusted</p>"},
		{Type: "button", Title: "extension", Action: &Action{ID: "extension", Push: &Push{UI: "detail", State: "state"}}},
	}}}}
	dropped := StripAssistantFields(&v)
	if v.Fallback != "" || v.Pages != nil || len(v.Sections[0].Items) != 2 || v.Sections[0].Items[0].Action != nil || len(dropped) != 4 {
		t.Fatalf("got %#v, dropped %v", v, dropped)
	}
	if err := CheckView(&v); err != nil {
		t.Fatal(err)
	}
	if a := v.Sections[0].Items[1].Action; a == nil || a.Push == nil || a.Push.UI != "detail" || a.Push.State != "state" {
		t.Fatal("extension push changed")
	}
}

func TestStripAssistantFieldsDropsAssistantOnlyTabs(t *testing.T) {
	cases := map[string]Action{
		"reply":     {ID: "switch", Reply: "One"},
		"open":      {ID: "switch", Open: "https://example.com/one"},
		"pop":       {ID: "switch", Pop: true},
		"push page": {ID: "switch", Push: &Push{Page: "one"}},
	}
	for name, action := range cases {
		t.Run(name, func(t *testing.T) {
			v := View{
				Tabs: &Tabs{
					Name: "view", Value: "one",
					Items:  []Tab{{Value: "one", Title: "One"}, {Value: "two", Title: "Two"}},
					Action: action,
				},
				Sections: []Section{{Title: "keep me", Items: []Component{{Type: "text", Text: "body"}}}},
			}
			dropped := StripAssistantFields(&v)
			if v.Tabs != nil || !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
				t.Fatalf("assistant-only tabs survived: tabs=%#v dropped=%v", v.Tabs, dropped)
			}
			if len(v.Sections) != 1 || v.Sections[0].Title != "keep me" || len(v.Sections[0].Items) != 1 {
				t.Fatalf("sections changed while dropping tabs: %#v", v.Sections)
			}
		})
	}
}

func TestStripAssistantFieldsCoversAllActionSlots(t *testing.T) {
	replyAct := func(id string) Action { return Action{ID: id, Reply: "smuggled"} }
	fileOpenAct := func(id string) Action { return Action{ID: id, Open: "persona://file/m1/%2Fetc%2Fhosts"} }
	pageOpenAct := func(id string) Action { return Action{ID: id, Open: "persona://session/m1/s1"} }
	extAct := func(id string) Action { return Action{ID: id, Push: &Push{UI: "detail", State: "s"}} }

	makeFullView := func() View {
		return View{
			Title: "Audit",
			Review: &Review{
				Count: 1,
				Name:  "review_body",
				Buttons: []ComposerButton{
					{Title: "Bad", Action: replyAct("rev-bad")},
					{Title: "Keep", Action: extAct("rev-ok")},
				},
				Discard: &Action{ID: "rev-discard", Open: "persona://file/m1/%2Fetc%2Fhosts"},
			},
			Sections: []Section{{
				Title: "All Slots",
				Items: []Component{
					{
						Type:  ComponentComposer,
						Name:  "comp_mixed",
						Value: "",
						Buttons: []ComposerButton{
							{Title: "Bad", Action: replyAct("comp-bad")},
							{Title: "Keep", Action: extAct("comp-ok")},
						},
					},
					{
						Type: ComponentComposer,
						Name: "comp_only_bad",
						Buttons: []ComposerButton{
							{Title: "OnlyBad", Action: fileOpenAct("comp-only-bad")},
						},
					},
					{
						Type:  ComponentRow,
						Key:   "row-all-bad",
						Title: "Row all bad",
						Control: &RowControl{
							Kind:   "switch",
							Name:   "toggle_a",
							Action: replyAct("ctrl-bad"),
						},
						Actions: []RowAction{
							{Title: "Smuggle", Action: replyAct("ra-bad")},
						},
					},
					{
						Type:  ComponentRow,
						Key:   "row-mixed",
						Title: "Row mixed",
						Actions: []RowAction{
							{Title: "Bad", Action: fileOpenAct("ra-file")},
							{Title: "Nav", Action: pageOpenAct("ra-nav")},
							{Title: "Keep", Action: extAct("ra-ok")},
						},
					},
					{
						Type: ComponentGrid,
						Tiles: []Tile{
							{
								Key:    "tile-1",
								Title:  "Tile 1",
								Action: &Action{ID: "tile-act-bad", Reply: "go"},
								Control: &RowControl{
									Kind:   "switch",
									Name:   "tile_sw",
									Action: fileOpenAct("tile-ctrl-bad"),
								},
								Actions: []RowAction{
									{Title: "Bad", Action: replyAct("tile-ra-bad")},
								},
							},
							{
								Key:    "tile-2",
								Title:  "Tile 2",
								Action: &Action{ID: "tile-nav", Open: "persona://session/m1/s1"},
								Actions: []RowAction{
									{Title: "Bad", Action: replyAct("tile2-bad")},
									{Title: "Keep", Action: extAct("tile2-ok")},
								},
							},
						},
					},
					{
						Type: ComponentCarousel,
						Tiles: []Tile{
							{
								Key:    "carousel-tile",
								Title:  "Carousel Tile",
								Action: &Action{ID: "carousel-act-bad", Reply: "go"},
								Control: &RowControl{
									Kind:   "switch",
									Name:   "carousel_sw",
									Action: fileOpenAct("carousel-ctrl-bad"),
								},
								Actions: []RowAction{
									{Title: "Bad", Action: replyAct("carousel-ra-bad")},
									{Title: "Keep", Action: extAct("carousel-ra-ok")},
								},
							},
						},
					},
					{
						Type: ComponentBreadcrumb,
						Crumbs: []Crumb{
							{Title: "Root", Action: &Action{ID: "crumb-bad", Reply: "go"}},
							{Title: "Nav", Action: &Action{ID: "crumb-nav", Open: "persona://session/m1/s1"}},
							{Title: "Leaf", Action: &Action{ID: "crumb-ok", Push: &Push{UI: "detail"}}},
						},
					},
					{
						Type:    ComponentFileTree,
						Entries: []TreeEntry{{Path: "a.go", Kind: "file"}},
						Expand:  &Action{ID: "tree-expand", Reply: "go"},
					},
					{
						Type: ComponentCode,
						Text: "package main",
						Load: &Action{ID: "code-load", Pop: true},
					},
					{
						Type:       ComponentDiff,
						Path:       "a.go",
						Load:       &Action{ID: "diff-load", Reply: "go"},
						Expand:     &Action{ID: "diff-expand", Reply: "go"},
						Comment:    &Action{ID: "diff-comment", Reply: "go"},
						Context:    &Action{ID: "diff-context", Reply: "go"},
						MarkViewed: &Action{ID: "diff-viewed", Reply: "go"},
						Draft:      &Action{ID: "diff-draft", Reply: "go"},
						Threads: []Thread{{
							Side:     "new",
							Line:     1,
							Comments: []Comment{{Author: "alice", Body: "hi"}},
							Reply:    &Action{ID: "th-reply", Reply: "go"},
							Resolve:  &Action{ID: "th-resolve", Open: "https://example.com"},
						}},
					},
					{
						Type:     ComponentCommentThread,
						Comments: []Comment{{Author: "alice", Body: "hi"}},
						Reply:    &Action{ID: "ct-reply", Push: &Push{Page: "hidden"}},
					},
				},
			}},
		}
	}

	for _, tc := range []struct {
		name               string
		keepPageNavigation bool
		strip              func(*View) []string
	}{
		{name: "StripAssistantFields", keepPageNavigation: false, strip: StripAssistantFields},
		{name: "StripAssistantFieldsForPage", keepPageNavigation: true, strip: StripAssistantFieldsForPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := makeFullView()
			if err := CheckView(&v); err != nil {
				t.Fatalf("input view must be valid before stripping: %v", err)
			}
			dropped := tc.strip(&v)
			if !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
				t.Fatalf("dropped=%v, want [assistant_action]", dropped)
			}
			if err := CheckView(&v); err != nil {
				t.Fatalf("stripped view failed CheckView: %v", err)
			}
			for _, a := range Actions(&v) {
				if assistantOnlyAction(a, tc.keepPageNavigation) {
					t.Errorf("assistant-only action survived stripping: %#v", a)
				}
			}
			if v.Review == nil || len(v.Review.Buttons) != 1 || v.Review.Buttons[0].Action.ID != "rev-ok" || v.Review.Discard != nil {
				t.Fatalf("unexpected review after strip: %#v", v.Review)
			}
			// comp_only_bad must be dropped, while comp_mixed keeps its allowed button.
			var composers []Component
			for _, e := range v.Sections[0].Items {
				if e.Type == ComponentComposer {
					composers = append(composers, e)
				}
			}
			if len(composers) != 1 || composers[0].Name != "comp_mixed" || len(composers[0].Buttons) != 1 {
				t.Fatalf("unexpected composers after strip: %#v", composers)
			}
			// Check keepPageNavigation behavior on persona://session/m1/s1 actions and survival of legitimate actions/items.
			ids := map[string]bool{}
			for _, a := range Actions(&v) {
				ids[a.ID] = true
			}
			for _, navID := range []string{"ra-nav", "tile-nav", "crumb-nav"} {
				if ids[navID] != tc.keepPageNavigation {
					t.Errorf("action %q present=%v, want %v", navID, ids[navID], tc.keepPageNavigation)
				}
			}
			for _, keepID := range []string{"rev-ok", "comp-ok", "ra-ok", "tile2-ok", "carousel-ra-ok", "crumb-ok"} {
				if !ids[keepID] {
					t.Errorf("legitimate action %q was unexpectedly stripped", keepID)
				}
			}
			var sawRowAllBad, sawTile1 bool
			for _, e := range v.Sections[0].Items {
				if e.Type == ComponentRow && e.Key == "row-all-bad" {
					sawRowAllBad = true
					if e.Control != nil || e.Actions != nil {
						t.Errorf("row-all-bad kept control/actions: %#v", e)
					}
				}
				if e.Type == ComponentGrid {
					for _, tile := range e.Tiles {
						if tile.Key == "tile-1" {
							sawTile1 = true
							if tile.Action != nil || tile.Control != nil || tile.Actions != nil {
								t.Errorf("tile-1 kept stripped slots: %#v", tile)
							}
						}
					}
				}
			}
			if !sawRowAllBad || !sawTile1 {
				t.Fatalf("expected stripped row-all-bad (%v) and tile-1 (%v) to survive", sawRowAllBad, sawTile1)
			}
		})
	}

	t.Run("review with only assistant buttons is dropped cleanly", func(t *testing.T) {
		v := View{
			Review: &Review{
				Count:   1,
				Name:    "rev",
				Buttons: []ComposerButton{{Title: "Send", Action: replyAct("r1")}},
			},
			Sections: []Section{{Items: []Component{{Type: ComponentText, Text: "ok"}}}},
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("input view invalid: %v", err)
		}
		dropped := StripAssistantFields(&v)
		if v.Review != nil || !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
			t.Fatalf("review=%#v dropped=%v", v.Review, dropped)
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("stripped view failed CheckView: %v", err)
		}
	})

	t.Run("review with only assistant discard keeps valid buttons", func(t *testing.T) {
		v := View{
			Review: &Review{
				Count:   1,
				Name:    "rev",
				Buttons: []ComposerButton{{Title: "Keep", Action: extAct("r-ok")}},
				Discard: &Action{ID: "r-bad", Reply: "discard"},
			},
			Sections: []Section{{Items: []Component{{Type: ComponentText, Text: "ok"}}}},
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("input view invalid: %v", err)
		}
		dropped := StripAssistantFields(&v)
		if !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
			t.Fatalf("dropped=%v, want [assistant_action]", dropped)
		}
		if v.Review == nil || v.Review.Discard != nil || len(v.Review.Buttons) != 1 || v.Review.Buttons[0].Action.ID != "r-ok" {
			t.Fatalf("unexpected review: %#v", v.Review)
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("stripped view failed CheckView: %v", err)
		}
	})

	t.Run("section with only fully stripped composer follows empty-section retention rules", func(t *testing.T) {
		v := View{
			Sections: []Section{
				{
					Title: "Dropped section",
					Items: []Component{{
						Type:    ComponentComposer,
						Name:    "c1",
						Buttons: []ComposerButton{{Title: "Bad", Action: replyAct("c1-bad")}},
					}},
				},
				{
					Title:  "Kept section with footer",
					Footer: "Keep footer",
					Items: []Component{{
						Type:    ComponentComposer,
						Name:    "c2",
						Buttons: []ComposerButton{{Title: "Bad", Action: replyAct("c2-bad")}},
					}},
				},
			},
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("input view invalid: %v", err)
		}
		dropped := StripAssistantFields(&v)
		if !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
			t.Fatalf("dropped=%v, want [assistant_action]", dropped)
		}
		if len(v.Sections) != 1 || v.Sections[0].Title != "Kept section with footer" || len(v.Sections[0].Items) != 0 {
			t.Fatalf("unexpected sections: %#v", v.Sections)
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("stripped view failed CheckView: %v", err)
		}
	})
}

// TestStripAssistantFieldsDriftGuard reflects over every Action slot reachable from wire.View (excluding
// View.Pages, which is dropped wholesale) so future schema additions cannot silently bypass stripping.
func TestStripAssistantFieldsDriftGuard(t *testing.T) {
	actionType := reflect.TypeOf(Action{})
	viewType := reflect.TypeOf(View{})

	type step struct {
		fieldIdx int
	}
	var paths [][]step
	var walk func(typ reflect.Type, prefix []step, seen map[reflect.Type]bool)
	walk = func(typ reflect.Type, prefix []step, seen map[reflect.Type]bool) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ == actionType {
			cp := append([]step(nil), prefix...)
			paths = append(paths, cp)
			return
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		nextSeen := make(map[reflect.Type]bool, len(seen)+1)
		for k, v := range seen {
			nextSeen[k] = v
		}
		nextSeen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			if typ == viewType && f.Name == "Pages" {
				continue
			}
			walk(f.Type, append(prefix, step{fieldIdx: i}), nextSeen)
		}
	}
	walk(viewType, nil, map[reflect.Type]bool{})
	if len(paths) < 25 {
		t.Fatalf("expected >=25 Action slots reachable from View, found %d", len(paths))
	}

	var populate func(v reflect.Value, path []step) string
	populate = func(v reflect.Value, path []step) string {
		if len(path) == 0 {
			switch v.Type() {
			case actionType:
				v.Set(reflect.ValueOf(Action{ID: "drift", Reply: "smuggled"}))
			case reflect.PointerTo(actionType):
				a := Action{ID: "drift", Reply: "smuggled"}
				v.Set(reflect.ValueOf(&a))
			default:
				t.Fatalf("unexpected leaf type %v", v.Type())
			}
			return ""
		}
		f := v.Field(path[0].fieldIdx)
		name := v.Type().Field(path[0].fieldIdx).Name
		if len(path) == 1 {
			populate(f, nil)
			return name
		}
		switch f.Kind() {
		case reflect.Pointer:
			elem := reflect.New(f.Type().Elem())
			f.Set(elem)
			return name + "." + populate(elem.Elem(), path[1:])
		case reflect.Slice:
			slice := reflect.MakeSlice(f.Type(), 1, 1)
			f.Set(slice)
			elem := slice.Index(0)
			if elem.Kind() == reflect.Pointer {
				p := reflect.New(elem.Type().Elem())
				elem.Set(p)
				elem = p.Elem()
			}
			return name + "[]." + populate(elem, path[1:])
		case reflect.Struct:
			return name + "." + populate(f, path[1:])
		default:
			t.Fatalf("unsupported container kind %v on field %s", f.Kind(), name)
			return name
		}
	}

	var hasSmuggledAction func(v reflect.Value) bool
	hasSmuggledAction = func(v reflect.Value) bool {
		if !v.IsValid() {
			return false
		}
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return false
			}
			return hasSmuggledAction(v.Elem())
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if hasSmuggledAction(v.Index(i)) {
					return true
				}
			}
			return false
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if hasSmuggledAction(iter.Value()) {
					return true
				}
			}
			return false
		case reflect.Struct:
			if v.Type() == actionType {
				return v.Interface().(Action).Reply != ""
			}
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() && hasSmuggledAction(v.Field(i)) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}

	for _, path := range paths {
		var v View
		desc := populate(reflect.ValueOf(&v).Elem(), path)
		t.Run(desc, func(t *testing.T) {
			dropped := StripAssistantFields(&v)
			if !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
				t.Fatalf("%s: dropped=%v, want [assistant_action]", desc, dropped)
			}
			if got := Actions(&v); len(got) != 0 {
				t.Fatalf("%s: wire.Actions still returned %#v after StripAssistantFields", desc, got)
			}
			if hasSmuggledAction(reflect.ValueOf(&v)) {
				t.Fatalf("%s: smuggled Action survived in View struct after StripAssistantFields", desc)
			}
		})
	}
}

func TestHasImageRefAndAssistantViewRejectDiffThreadAvatarRef(t *testing.T) {
	diffWithRef := Component{
		Type:  "diff",
		Path:  "a.go",
		Hunks: []Hunk{{Header: "@@ -1 +1 @@", Lines: []string{"+x"}}},
		Threads: []Thread{{
			Side: "new",
			Line: 1,
			Comments: []Comment{{
				ID:     "c1",
				Author: "alice",
				Body:   "looks good",
				Avatar: &Avatar{Ref: "ext-pic"},
			}},
		}},
	}
	if !HasImageRef(&diffWithRef) {
		t.Fatal("HasImageRef returned false for diff.threads[].comments[].avatar.ref")
	}
	diffWithHTTPS := Component{
		Type:  "diff",
		Path:  "a.go",
		Hunks: []Hunk{{Header: "@@ -1 +1 @@", Lines: []string{"+x"}}},
		Threads: []Thread{{
			Side: "new",
			Line: 1,
			Comments: []Comment{{
				ID:     "c1",
				Author: "alice",
				Body:   "looks good",
				Avatar: &Avatar{URL: "https://example.com/a.png"},
			}},
		}},
	}
	if HasImageRef(&diffWithHTTPS) {
		t.Fatal("HasImageRef returned true for diff thread comment avatar with https url")
	}
	validView := &View{Fallback: "diff", Sections: []Section{{Items: []Component{diffWithHTTPS}}}}
	if err := CheckAssistantView(validView); err != nil {
		t.Fatalf("CheckAssistantView rejected valid diff with https avatar: %v", err)
	}

	v := &View{Fallback: "diff", Sections: []Section{{Items: []Component{diffWithRef}}}}
	if err := CheckAssistantView(v); err == nil || !strings.Contains(err.Error(), "not ref") {
		t.Fatalf("CheckAssistantView accepted or gave wrong error for diff thread avatar ref: %v", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAssistantView(raw); err == nil || !strings.Contains(err.Error(), "not ref") {
		t.Fatalf("ParseAssistantView accepted or gave wrong error for diff thread avatar ref: %v", err)
	}
	if _, err := ParseAgentPage(raw); err == nil || !strings.Contains(err.Error(), "not ref") {
		t.Fatalf("ParseAgentPage accepted or gave wrong error for diff thread avatar ref: %v", err)
	}
}
