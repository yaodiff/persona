package assistant

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Joining again (a new install code into the same directory) changes who the manager is and nothing else: its
// sections of the configuration stay.
func TestWriteManagerConfigKeepsTheSections(t *testing.T) {
	dir := t.TempDir()
	c := ManagerConfig{ID: "work", Hub: "https://hub.example.com", Token: "first", Fingerprint: "fp"}
	if err := WriteManagerConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ManagerFile)
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), "{", `{"google": {"client_id": "mine"}, "moved_from_hub_json": {"keys": []},`, 1))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c.Token, c.Fingerprint = "second", ""
	if err := WriteManagerConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManagerConfig(dir)
	if err != nil || got.Token != "second" || got.Fingerprint != "" {
		t.Fatalf("%+v %v", got, err)
	}
	var raw map[string]json.RawMessage
	b, _ = os.ReadFile(path)
	if err := json.Unmarshal(b, &raw); err != nil || string(raw["google"]) == "" || raw["moved_from_hub_json"] == nil {
		t.Fatalf("sections lost: %s", b)
	}
	if _, ok := raw["fingerprint"]; ok {
		t.Fatalf("a cleared field stayed: %s", b)
	}
}

// managerIdentity is every key of ManagerConfig: WriteManagerConfig would keep a stale one otherwise.
func TestManagerIdentityKeys(t *testing.T) {
	var keys []string
	typ := reflect.TypeFor[ManagerConfig]()
	for i := range typ.NumField() {
		keys = append(keys, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	if !slices.Equal(keys, managerIdentity) {
		t.Fatalf("ManagerConfig's keys %v, managerIdentity %v", keys, managerIdentity)
	}
}

// Joining again over a manager.json that does not read keeps that file beside the new one (the code is spent).
func TestWriteManagerConfigOverAnUnreadableFile(t *testing.T) {
	for _, bad := range []string{`{"id":`, "", " \n"} {
		dir := t.TempDir()
		path := filepath.Join(dir, ManagerFile)
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteManagerConfig(dir, ManagerConfig{ID: "work", Hub: "https://hub.example.com", Token: "t"}); err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
		if c, err := ReadManagerConfig(dir); err != nil || c.Token != "t" {
			t.Fatalf("%q: %+v %v", bad, c, err)
		}
		kept, _ := filepath.Glob(path + ".unreadable-*")
		if len(kept) != 1 {
			t.Fatalf("%q: kept %v", bad, kept)
		}
		if b, _ := os.ReadFile(kept[0]); string(b) != bad {
			t.Fatalf("kept %q", b)
		}
	}
}

// A manager does not start without its sections: a manager.json that does not read (with or without a hub.json to
// move from), a broken marker, or sections of the wrong shape. Started anyway, it would run without the user's gate
// rules. A readable one, or none yet, starts; what lost to manager.json is said.
func TestMoveConfigFailsClosed(t *testing.T) {
	cases := []struct {
		name, hub, mine string
		ok              bool
	}{
		{"no files", "", "", true},
		{"old layout", `{"token":"t","lark_gate":[{"method":"CLI","path":"/docs/**","action":"deny"}]}`, "", true},
		{"not JSON, nothing to move", "", `{"lark_gate":`, false},
		{"empty, nothing to move", "", " ", false},
		{"empty, with hub.json", `{"token":"t","lark_gate":[{"method":"CLI","path":"/docs/**","action":"deny"}]}`, "\n", false},
		{"not JSON, with hub.json", `{"token":"t","google":{"client_id":"x"}}`, `{"lark_gate":`, false},
		{"broken marker", `{"token":"t","google":{"client_id":"x"}}`, `{"moved_from_hub_json":"yes"}`, false},
		{"wrong shape", `{"token":"t"}`, `{"lark_gate":{"method":"CLI"}}`, false},
		{"a gate rule the gate can't use", `{"token":"t"}`, `{"connector_gate":[{"connector":"github","action":"*","decision":"denied"}]}`, false},
		{"an auto-confirm policy the gate can't use", `{"token":"t"}`, `{"connector_auto_confirm":[{"extension":"example","connector":"example","actions":["example_*"]}]}`, false},
		{"hub.json's own part of the wrong shape", `{"token":"t","listen":18660}`, `{"lark_gate":[]}`, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if c.hub != "" {
			if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(c.hub), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		file := filepath.Join(dir, "managers", "assistant", ManagerFile)
		if c.mine != "" {
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(c.mine), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		ext, err := moveConfig(dir, file, nil, t.Logf)
		if (err == nil) != c.ok || (err == nil) != (ext != nil) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.name == "old layout" && len(ext.LarkGate) != 1 { // read after the move: the rule it just moved
			t.Fatalf("%s: lark_gate %+v", c.name, ext.LarkGate)
		}
		if err != nil && !strings.Contains(err.Error(), "does not start without its configuration") {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// hub.json's value that manager.json's wins over is said.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"google":{"client_id":"old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, ManagerFile)
	if err := os.WriteFile(file, []byte(`{"google":{"client_id":"mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var said []string
	ext, err := moveConfig(dir, file, nil, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	if ext.Google.ClientID != "mine" { // what was checked is what the extensions run with
		t.Fatalf("google %+v", ext.Google)
	}
	if len(said) != 1 || !strings.HasPrefix(said[0], "configuration: google: hub.json's copy differs") {
		t.Fatalf("said %q", said)
	}
}
