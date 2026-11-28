package i18n

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The notes a node writes on its Paseo and Orca engines (the canonical Chinese of node-engine) reach each reader in
// their language, the reason inside them as the node wrote it; any other note stays as it is.
func TestEngineTextLanguages(t *testing.T) {
	reason := "SyntaxError: Unexpected token '??='"
	for _, c := range []struct {
		key  string
		args Args
	}{
		{"node-engine.paseo_unreachable", Args{"reason": reason}},
		{"node-engine.paseo_not_connected", nil},
		{"node-engine.paseo_not_connected_why", Args{"reason": "paseo: Paseo refused the connection (4401); trying again"}},
		{"node-engine.paseo_outdated", Args{"version": "0.14.0", "minimum": "0.15.0"}},
		// Orca's, from before #17037 too: the canonical Chinese is the words nodes wrote then.
		{"node-engine.orca_incompatible", nil},
	} {
		canonical := T(Default, c.key, c.args)
		for _, l := range []Lang{En, ZhHans, Ja} {
			if got, want := EngineText(l, canonical), T(l, c.key, c.args); got != want {
				t.Errorf("%s in %s: %q, want %q", c.key, l, got, want)
			}
		}
		if got := EngineText("", canonical); got != canonical {
			t.Errorf("%s with no language: %q", c.key, got)
		}
	}
	if got := EngineText(En, T(Default, "node-engine.paseo_unreachable", Args{"reason": reason})); got != "Paseo is running, but Persona can’t connect to it: "+reason {
		t.Fatalf("en: %q", got)
	}
	for _, raw := range []string{"an older node's own words", "exited: exit status 1", ""} {
		for _, l := range []Lang{En, ZhHans, Ja} {
			if got := EngineText(l, raw); got != raw {
				t.Fatalf("another note changed in %s: %q", l, got)
			}
		}
	}
}

// EngineNotes copies the engines only when a note changes: a machine's snapshot is shared by every reader.
func TestEngineNotesCopies(t *testing.T) {
	note := T(Default, "node-engine.paseo_outdated", Args{"version": "0.14.0", "minimum": "0.15.0"})
	engines := []proto.Engine{{ID: "herdr", Available: true}, {ID: "paseo", Available: true, Version: "0.14.0", Note: note}}
	got := EngineNotes(Ja, engines)
	if got[1].Note != T(Ja, "node-engine.paseo_outdated", Args{"version": "0.14.0", "minimum": "0.15.0"}) || got[0] != engines[0] {
		t.Fatalf("ja: %+v", got)
	}
	if engines[1].Note != note {
		t.Fatal("the snapshot changed")
	}
	plain := []proto.Engine{{ID: "orca", Note: "an older node's own words"}}
	if got := EngineNotes(En, plain); &got[0] != &plain[0] {
		t.Fatal("copied with nothing to change")
	}
	if got := EngineNotes(En, nil); got != nil {
		t.Fatalf("nil: %v", got)
	}
}
