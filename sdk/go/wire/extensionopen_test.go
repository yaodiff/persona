package wire

import "testing"

func viewWithOpen(target string) View {
	return View{Sections: []Section{{Items: []Component{
		{Type: "text", Text: "before"},
		{Type: "row", Title: "open it", Action: &Action{ID: "open", Open: target}},
	}}}}
}

func actionAt(v View) *Action { return v.Sections[0].Items[1].Action }

func TestStripAssistantFieldsForPageKeepsSessionAndTaskNavigation(t *testing.T) {
	for _, target := range []string{
		"persona://session/machine-1/session-1",
		"persona://task/todo-1",
		"persona://session/a%252Fb/s1",
		"persona://task/todo%3Fv1%23top",
	} {
		if err := CheckExtensionOpen(target); err != nil {
			t.Fatalf("%s: CheckExtensionOpen rejected valid target: %v", target, err)
		}
		v := viewWithOpen(target)
		dropped := StripAssistantFieldsForPage(&v)
		if a := actionAt(v); a == nil || a.Open != target {
			t.Fatalf("%s: page open was dropped (%v)", target, dropped)
		}
		if err := CheckView(&v); err != nil {
			t.Fatalf("%s: kept view is invalid: %v", target, err)
		}
	}
}

func TestStripAssistantFieldsForPageDropsOtherAndMalformedTargets(t *testing.T) {
	for _, target := range []string{
		"https://example.com/one",
		"https://session/machine-1/session-1",
		"https://task/todo-1",
		"https://session",
		"https://task/evil?x=1#frag",
		"HTTPS://session/machine-1/session-1",
		"http://session/machine-1/session-1",
		"mailto:user@example.com",
		"persona://kb/projects/notes.md",
		"persona://file/machine-1/%2Fetc%2Fhosts",
		"persona://session",
		"persona://session/only-one-segment",
		"persona://task",
		"persona://task/",
		"persona://session:443/machine-1/session-1",
		"persona://user@session/machine-1/session-1",
		"PERSONA://SESSION/machine-1/session-1",
		"persona://session/machine-1/session-1?evil=1",
		"persona://session/machine-1/session-1#frag",
		"persona://session/machine-1/session-1?",
		"persona://session/machine-1/session-1#",
		"persona://task/todo-1?#",
		"persona://session/../settings",
		"persona://session/%2e%2e/settings",
		"persona://session/./settings",
		"persona://task/.",
		"persona://task/..",
		"persona://session/m%2Fsé",
		"persona://session/m%2Fs ",
		"persona://session/m1/%FF",
	} {
		if err := CheckExtensionOpen(target); err == nil {
			t.Fatalf("%s: CheckExtensionOpen accepted disallowed target", target)
		}
		v := viewWithOpen(target)
		dropped := StripAssistantFieldsForPage(&v)
		if a := actionAt(v); a != nil {
			t.Fatalf("%s: non-page target survived: %#v", target, a)
		}
		if len(dropped) == 0 || dropped[0] != "assistant_action" {
			t.Fatalf("%s: dropped %v", target, dropped)
		}
	}
}

func TestCheckAssistantOpenHardening(t *testing.T) {
	for _, target := range []string{
		"persona://file/machine-1/%2Fetc%2Fhosts",
		"persona://file/machine-1/%2Ftmp%2Fcafé.txt",
		"persona://kb/100%25.md",
		"persona://kb/notes%3Fv1%23top.md",
		"https://example.com/path?x=1#frag",
	} {
		if err := CheckAssistantOpen(target); err != nil {
			t.Fatalf("%s: CheckAssistantOpen rejected valid target: %v", target, err)
		}
	}
	for _, target := range []string{
		"persona://session/../settings",
		"persona://session/%2e%2e/settings",
		"persona://task/..",
		"persona://file/../%2Fetc%2Fhosts",
		"persona://session/m%2Fsé",
		"persona://session/m%2Fs ",
		"persona://kb/a%2Fbé",
		"persona://session/m/s?",
		"persona://session/m/s#",
		"persona://task/t1?#",
		"persona://session/m/%FF",
		"persona://kb/a/%FF",
	} {
		if err := CheckAssistantOpen(target); err == nil {
			t.Fatalf("%s: CheckAssistantOpen accepted disallowed target", target)
		}
	}
}

func TestStripAssistantFieldsOnCardsStillDropsSessionNavigation(t *testing.T) {
	v := viewWithOpen("persona://session/machine-1/session-1")
	dropped := StripAssistantFields(&v)
	if a := actionAt(v); a != nil {
		t.Fatalf("a card kept a session open: %#v", a)
	}
	if len(dropped) == 0 || dropped[0] != "assistant_action" {
		t.Fatalf("dropped %v", dropped)
	}
}

func TestStripAssistantFieldsForPageDropsHTTPSLookalikeInNestedSlots(t *testing.T) {
	v := View{
		Sections: []Section{{Items: []Component{{Type: "text", Text: "keep"}}}},
		Tabs:     &Tabs{Action: Action{ID: "tab-open", Open: "https://session/machine-1/session-1"}},
		Review: &Review{
			Count: 1,
			Name:  "review_body",
			Buttons: []ComposerButton{
				{Title: "smuggle", Action: Action{ID: "rev-open", Open: "https://task/todo-1"}},
				{Title: "legit", Action: Action{ID: "rev-ok", Open: "persona://task/todo-1"}},
			},
			Discard: &Action{ID: "rev-discard", Open: "https://session/evil"},
		},
	}
	dropped := StripAssistantFieldsForPage(&v)
	if len(dropped) != 1 || dropped[0] != "assistant_action" {
		t.Fatalf("dropped = %v, want [assistant_action]", dropped)
	}
	if v.Tabs != nil {
		t.Fatalf("expected https://session Tabs.Action to drop Tabs, got %#v", v.Tabs)
	}
	if v.Review == nil || len(v.Review.Buttons) != 1 || v.Review.Buttons[0].Action.ID != "rev-ok" {
		t.Fatalf("expected only persona://task review button to remain, got %#v", v.Review)
	}
	if v.Review.Discard != nil {
		t.Fatalf("expected https://session Review.Discard to be stripped, got %#v", v.Review.Discard)
	}
	if err := CheckView(&v); err != nil {
		t.Fatalf("CheckView failed after stripping: %v", err)
	}
}
