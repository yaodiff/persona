package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/proto"
)

// The modules setting knows the ids installed extensions may supply (modapi.ExtensionModules, #16254) on a manager
// without the built-in module of that id: the owner can switch off what an installed package supplies on any build.
func TestSetModuleSwitchAcceptsExtensionModules(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	m := testSlotModule{info: moduleTestInfo("switch-test")}
	f := newFakeHub().withMachine()
	f.hello.Locale = "en"
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{m}}))
	ext := modapi.ExtensionModules()
	if len(ext) == 0 {
		t.Fatal("modapi.ExtensionModules is empty")
	}
	id := string(ext[0].ID)
	if s.modules.Loaded(id) || s.modules.Skipped(id) != "" {
		t.Fatalf("the test manager must not know %s as a built-in module", id)
	}
	value := `{"disabled":["` + id + `"]}`
	done := make(chan string, 1)
	go func() {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main",
			Arguments: json.RawMessage(`{"action":"set","key":"modules","value":` + value + `}`)})
		done <- r.Content[0].Text
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatalf("%s: no approval card; tool returned %q", value, <-done)
	}
	yes := true
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID,
		Answer: json.RawMessage(mustMarshal(proto.Resolution{Allow: &yes}))}); err != nil {
		t.Fatal(err)
	}
	if out := resultOf(t, done, 5*time.Second); !strings.Contains(out, "next manager start") {
		t.Errorf("applied result = %q", out)
	}
	if got := ReadModuleSwitch(ctx, st, t.Logf); !slices.Equal(got, []string{id}) {
		t.Fatalf("stored disabled list = %v, want [%s]", got, id)
	}

	// An id that is in no list stays unknown, and the error names the extension modules too.
	r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main",
		Arguments: json.RawMessage(`{"action":"set","key":"modules","value":{"disabled":["volc2"]}}`)})
	if !r.IsError || !strings.Contains(r.Content[0].Text, `unknown module "volc2"`) || !strings.Contains(r.Content[0].Text, id) {
		t.Fatalf("unknown id result = %q (error: %v)", r.Content[0].Text, r.IsError)
	}
}
