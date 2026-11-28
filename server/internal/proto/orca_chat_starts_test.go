package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// Machine.OrcaChatStarts is a closed set on the wire (#17650): "on" or "off" where the switch applies, the key left out
// where it does not, and nothing else valid.
func TestOrcaChatStartsOnTheWire(t *testing.T) {
	for on, want := range map[bool]OrcaChatStarts{true: OrcaChatStartsOn, false: OrcaChatStartsOff} {
		if got := OrcaChatStartsOf(on); got != want || !got.Valid() {
			t.Fatalf("OrcaChatStartsOf(%v) = %q", on, got)
		}
	}
	for _, bad := range []OrcaChatStarts{"", "On", "unknown"} {
		if bad.Valid() {
			t.Fatalf("%q is valid", bad)
		}
	}
	b, _ := json.Marshal(Machine{ID: "m1"})
	if strings.Contains(string(b), "orca_chat_starts") {
		t.Fatalf("absent is written: %s", b)
	}
	b, _ = json.Marshal(Machine{ID: "m1", OrcaChatStarts: OrcaChatStartsOff})
	if !strings.Contains(string(b), `"orca_chat_starts":"off"`) {
		t.Fatalf("off: %s", b)
	}
}
