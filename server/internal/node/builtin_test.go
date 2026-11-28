package node

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/node/iphoneport"
	"github.com/nwverse/persona/server/internal/proto"
)

// The built-in programs answer plugin.node.call exactly as their old method names do: the same JSON, the same errors
// (#1830 step 5). Only what changes nothing on this machine is run here: no install, grant, lock or cookie import.
func TestBuiltinProgramsAnswerLikeTheirOldNames(t *testing.T) {
	n, _ := testNode(t) // the old names go through handle's prelude: a node with its state
	n.dir, n.logger = filepath.Join(n.home, ".persona"), log.New(os.Stderr, "", 0)
	for _, c := range []struct {
		old, id, method string
		params          any
	}{
		{"computer.status", "cua", "status", map[string]any{}},
		{"computer.call", "cua", "call", map[string]any{"session": "s", "tool": "not_a_tool", "args": map[string]any{}}},
		{"computer.call", "cua", "call", map[string]any{"session": "s", "tool": "click", "args": "not an object"}},
		{"computer.nolock", "cua", "nolock", map[string]any{"on": false}},
		{"browser.status", "ego", "status", map[string]any{}},
		{"browser.call", "ego", "call", map[string]any{}},
		// imessage keeps the connector protocol's names (#1830 step 6); not a Mac here: the same list and refusals.
		{"connector.list", "imessage", "connector.list", map[string]any{}},
		{"connector.chats", "imessage", "connector.chats", map[string]any{"id": "imessage", "limit": 5}},
		{"connector.changes", "imessage", "connector.changes", map[string]any{"id": "imessage"}},
	} {
		oldOut, oldErr := pluginCall(t, n, c.old, c.params)
		raw, _ := json.Marshal(c.params)
		newOut, newErr := pluginCall(t, n, "plugin.node.call", proto.PluginNodeCall{ID: c.id, Method: c.method, Params: raw})
		if (oldErr == nil) != (newErr == nil) || (oldErr != nil && oldErr.Error() != newErr.Error()) {
			t.Errorf("%s: errors %v / %v", c.old, oldErr, newErr)
			continue
		}
		if oldErr != nil {
			continue
		}
		a, _ := json.Marshal(oldOut)
		b, _ := json.Marshal(newOut)
		if !sameIgnoringChecked(t, a, b) {
			t.Errorf("%s: %s\n  plugin.node.call: %s", c.old, a, b)
		}
	}
	// A method the program does not have: unknown either way (the hub's "personad is too old" reads "unknown method").
	if _, err := pluginCall(t, n, "computer.nope", map[string]any{}); err == nil || err.Error() != "unknown method computer.nope" {
		t.Errorf("computer.nope: %v", err)
	}
	if _, err := pluginCall(t, n, "plugin.node.call", proto.PluginNodeCall{ID: "cua", Method: "nope"}); err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Errorf("cua nope: %v", err)
	}
}

// sameIgnoringChecked: two answers alike but for when they were checked (status says when it looked).
func sameIgnoringChecked(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y map[string]any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	delete(x, "checked")
	delete(y, "checked")
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// Every old name the node answers is one of the built-in programs' methods (the hub may send either).
func TestEveryOldNameIsABuiltinMethod(t *testing.T) {
	src, err := os.ReadFile("node.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, `case "computer.status", "computer.call"`)
	j := strings.Index(s[i:], "return legacyBuiltinCall")
	if i < 0 || j < 0 {
		t.Fatal("the old names' case in node.go")
	}
	var names []string
	for _, f := range strings.Split(s[i+len("case "):i+j], ",") {
		if name := strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(f), ":")), `"`); name != "" {
			names = append(names, name)
		}
	}
	if len(names) != 19 {
		t.Fatalf("%d old names: %v", len(names), names)
	}
	builtinSrc, _ := os.ReadFile("builtin.go")
	for _, name := range names {
		prefix, short, _ := strings.Cut(name, ".")
		id := ""
		for i, pre := range builtinPrograms {
			if pre == prefix+"." {
				id = i
			}
		}
		if id == "" || !strings.Contains(string(builtinSrc), `"`+short+`"`) {
			t.Errorf("%s: no method %q of a built-in program", name, short)
		}
	}
}

// The node says it has the built-in programs; the hub never starts them as packages, and a sync leaves no trace of
// them on disk.
func TestBuiltinProgramsInInfoAndNeverAPackage(t *testing.T) {
	n, _ := pluginTestNode(t)
	if !slices.Equal(builtinProgramIDs, []string{"cua", "ego", "imessage", "iphone"}) {
		t.Fatalf("builtin_programs %v", builtinProgramIDs)
	}
	// node.info lists iphone only with the phone-extra module (internal/node/iphoneport).
	want := []string{"cua", "ego", "imessage"}
	if iphoneport.Get(moduleswitch.Set{}) != nil {
		want = append(want, "iphone")
	}
	if got := builtinProgramsInfo(); !slices.Equal(got, want) {
		t.Fatalf("node.info's builtin_programs %v, want %v", got, want)
	}
	for _, id := range builtinProgramIDs {
		s := proto.PluginNodeStart{ID: id, Version: "1.0.0", Command: "x", RPC: true, Keep: extension.KeepLease}
		if _, err := pluginCall(t, n, "plugin.node.start", s); err == nil || !strings.Contains(err.Error(), "built into") {
			t.Errorf("start %s: %v", id, err)
		}
	}
	if _, err := pluginCall(t, n, "plugin.node.sync", proto.PluginNodeSync{Programs: []proto.PluginNodeProgram{}, Installed: []string{"cua", "ego"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range builtinProgramIDs {
		if _, err := os.Stat(filepath.Join(n.dir, nodePluginsDir, id)); !os.IsNotExist(err) {
			t.Errorf("%s has a directory: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(n.dir, "plugins")); !os.IsNotExist(err) {
		t.Errorf("the hub's plugins directory was made: %v", err)
	}
}
