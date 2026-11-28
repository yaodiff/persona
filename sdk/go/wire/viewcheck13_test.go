package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

// A code's navigate (UI version 13) is one of the view's actions: Actions() returns it, so its push state is held to
// StateMost and the hub's push and assistant checks see it like any other action.
func TestCodeNavigateIsAnAction(t *testing.T) {
	var v View
	raw := `{"sections":[{"items":[{"type":"code","path":"a.go","text":"package a","navigate":{"id":"nav"}}]}]}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	acts := Actions(&v)
	if len(acts) != 1 || acts[0].ID != "nav" {
		t.Fatalf("Actions() = %+v, want the code's navigate", acts)
	}
	if err := CheckView(&v); err != nil {
		t.Fatal(err)
	}

	big := `{"sections":[{"items":[{"type":"code","path":"a.go","text":"package a","navigate":{"id":"nav","push":{"title":"Usages","state":"` + strings.Repeat("x", StateMost+1) + `"}}}]}]}`
	var w View
	if err := json.Unmarshal([]byte(big), &w); err != nil {
		t.Fatal(err)
	}
	if err := CheckView(&w); err == nil {
		t.Fatal("a navigate push with state over StateMost passed CheckView")
	}
}
