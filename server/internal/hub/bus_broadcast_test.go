package hub

// A manager's messages to the apps (ws.broadcast, bus_broadcast.go): only its types, about its own things, read with
// exact keys.

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// goFiles parses the Go files of dir, tests left out.
func goFiles(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = f
	}
	return out
}

// stringLit is e's value when it is a string literal.
func stringLit(e ast.Expr) (string, bool) {
	if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
		s, err := strconv.Unquote(b.Value)
		return s, err == nil
	}
	return "", false
}

// hubSentTypes are the types of the messages the hub's own code writes: "type": "x" in a map literal, Type: "x" in a
// struct literal, x["type"] = "x" and x.Type = "x" (a tuple assignment too), and "type":"x" inside a string.
func hubSentTypes(t *testing.T) []string {
	t.Helper()
	var out []string
	add := func(s string) { // a message type: lower case, digits, _ and . (not a PEM block's "PRIVATE KEY")
		if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789_.") == "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	inString := func(s string) {
		for _, part := range strings.Split(s, `"type":"`)[1:] {
			if end := strings.IndexByte(part, '"'); end > 0 {
				add(part[:end])
			}
		}
	}
	for _, f := range goFiles(t, ".") {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				k, isKey := stringLit(n.Key)
				id, isIdent := n.Key.(*ast.Ident)
				if v, ok := stringLit(n.Value); ok && (isKey && k == "type" || isIdent && id.Name == "Type") {
					add(v)
				}
			case *ast.AssignStmt:
				if len(n.Lhs) != len(n.Rhs) {
					break
				}
				for i, l := range n.Lhs {
					v, ok := stringLit(n.Rhs[i])
					if !ok {
						continue
					}
					switch l := l.(type) {
					case *ast.IndexExpr:
						if k, _ := stringLit(l.Index); k == "type" {
							add(v)
						}
					case *ast.SelectorExpr:
						if l.Sel.Name == "Type" {
							add(v)
						}
					}
				}
			case *ast.BasicLit:
				if s, ok := stringLit(n); ok {
					inString(s)
				}
			}
			return true
		})
	}
	return out
}

func wireStringConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	constants := map[string]string{}
	for _, file := range goFiles(t, dir) {
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				value := spec.(*ast.ValueSpec)
				for i, expr := range value.Values {
					if literal, ok := stringLit(expr); ok {
						constants[value.Names[i].Name] = literal
					}
				}
			}
		}
	}
	return constants
}

// managerSentTypes are the types of the messages the managers' code sends the apps: the assistant's Service.event and
// broadcastEvent, its extensions' manager's broadcast (….broadcast(msg)) and broadcastExtension(kind, …)
// (extension_<kind> and plugin_<kind>). A message is a literal with a "type" key or Type field, assigned to the variable
// passed in the same function; one the test cannot read is an error, but for the extensions' own types
// (apiBroadcast: declared in their manifests) and broadcastExtension's.
func managerSentTypes(t *testing.T) []string {
	t.Helper()
	wireConstants := map[string]map[string]string{
		"assistantwire": wireStringConstants(t, "../assistantwire"),
		"extwire":       wireStringConstants(t, "../extwire"),
	}
	eventType := func(expr ast.Expr) (string, bool) {
		if sel, ok := expr.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok {
				s, ok := wireConstants[pkg.Name][sel.Sel.Name]
				return s, ok
			}
		}
		return stringLit(expr)
	}
	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	typeOf := func(e ast.Expr, body *ast.BlockStmt) (string, bool) {
		lit, _ := e.(*ast.CompositeLit)
		if id, ok := e.(*ast.Ident); ok && body != nil {
			ast.Inspect(body, func(n ast.Node) bool {
				a, ok := n.(*ast.AssignStmt)
				if !ok || len(a.Lhs) != len(a.Rhs) {
					return true
				}
				for i, l := range a.Lhs {
					if li, ok := l.(*ast.Ident); ok && li.Name == id.Name {
						if c, ok := a.Rhs[i].(*ast.CompositeLit); ok {
							lit = c
						}
					}
				}
				return true
			})
		}
		if lit == nil {
			return "", false
		}
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if k, _ := stringLit(kv.Key); k == "type" {
					return eventType(kv.Value)
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Type" {
					return eventType(kv.Value)
				}
			}
		}
		return "", false
	}
	for _, pkg := range []string{"assistant", "assistant/saved", "modules/recording", "modules/health", "extmgr", "devices"} {
		for name, f := range goFiles(t, filepath.Join("..", pkg)) {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || len(call.Args) == 0 {
						return true
					}
					recv, _ := sel.X.(*ast.Ident)
					switch {
					case sel.Sel.Name == "broadcastExtension":
						if kind, ok := stringLit(call.Args[0]); ok {
							add("extension_" + kind)
							add("plugin_" + kind)
						} else {
							t.Errorf("%s %s: broadcastExtension of a kind the test cannot read", name, fn.Name.Name)
						}
					case pkg == "assistant" && recv != nil && recv.Name == "s" && len(call.Args) == 2 &&
						(sel.Sel.Name == "event" || (sel.Sel.Name == "broadcastEvent" && fn.Name.Name != "event")),
						// a package moved out of assistant sends through the Host's Event, with the same two arguments
						pkg == "assistant/saved" && sel.Sel.Name == "Event" && len(call.Args) == 2,
						// These modules send through their Host the same way (their event, as the assistant's).
						(pkg == "modules/recording" || pkg == "modules/health") && sel.Sel.Name == "event" && len(call.Args) == 2,
						(pkg == "extmgr" || pkg == "devices") && sel.Sel.Name == "broadcast" && len(call.Args) == 1:
						if typ, ok := typeOf(call.Args[len(call.Args)-1], fn.Body); ok {
							add(typ)
						} else if fn.Name.Name != "apiBroadcast" && fn.Name.Name != "broadcastExtension" {
							t.Errorf("%s %s: a message whose type the test cannot read: use a literal with its event type", name, fn.Name.Name)
						}
					}
					return true
				})
			}
		}
	}
	return out
}

// The managers' types are exactly the ones their code sends, and none is the hub's; the hub's are all listed (so an
// extension declaring one cannot send it).
func TestBroadcastTypes(t *testing.T) {
	sent := managerSentTypes(t)
	for _, typ := range sent {
		if !managerMessageTypes[typ] {
			t.Errorf("a manager sends %q: add it to managerMessageTypes (bus_broadcast.go)", typ)
		}
	}
	for typ := range managerMessageTypes {
		if !slices.Contains(sent, typ) {
			t.Errorf("managerMessageTypes has %q, which no manager sends", typ)
		}
	}
	hub := hubSentTypes(t)
	t.Logf("the managers send %v; the hub %v", sent, hub)
	for _, want := range []string{"interaction", "sessions", "error", "sessions_delta", "terminal"} {
		if !slices.Contains(hub, want) {
			t.Fatalf("the scan of the hub's code missed %q: %v", want, hub)
		}
	}
	for _, typ := range hub {
		if managerMessageTypes[typ] {
			t.Errorf("%q is the hub's own message and a manager's", typ)
		}
		if !hubMessageTypes[typ] {
			t.Errorf("the hub sends the apps %q: add it to hubMessageTypes (bus_broadcast.go)", typ)
		}
	}
}

// broadcastsTo has manager f say each of raws (bytes as they are: repeated or case-variant keys kept) and returns
// what app c got of them, each read with exact keys.
func broadcastsTo(t *testing.T, c *client, f *guardManager, raws ...string) []map[string]json.RawMessage {
	t.Helper()
	for _, raw := range raws {
		if !json.Valid([]byte(raw)) {
			t.Fatalf("not JSON: %s", raw)
		}
		if err := f.peer.Notify(assistantwire.EventBroadcast, map[string]any{"message": json.RawMessage(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	var out []map[string]json.RawMessage
	for _, s := range strings.Split(appGot(c, 500*time.Millisecond), "\n") {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &m) == nil && m != nil {
			out = append(out, m)
		}
	}
	return out
}

// marked: one of got carries mark.
func marked(got []map[string]json.RawMessage, mark string) bool {
	for _, m := range got {
		b, _ := json.Marshal(m)
		if bytes.Contains(b, []byte(mark)) {
			return true
		}
	}
	return false
}

// A manager's broadcast is one of the managers' types and never the hub's: B's card re-labelled as A's session's, A's
// sessions listed, A's card ended, an error on A's session or for every app, a type no manager sends: none reaches
// the apps. A manager's own messages about its own things do.
func TestABroadcastCannotSpeakForTheHub(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{ID: "ix_b", Kind: proto.IxPermission}}); err != nil {
		t.Fatal(err)
	}
	if err := a.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Kind: proto.IxPermission}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both cards", func() bool {
		return h.interactionOwner("ix_b") == "m-0b0b0b" && h.interactionOwner("ix_a") == assistantwire.AssistantID
	})
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"interaction","interaction":{"id":"ix_b","machine_id":"ma","session_id":"s3","kind":"permission","summary":"FORGED 1"}}`,
		`{"type":"sessions","machine_id":"ma","sessions":[{"session_id":"fake","title":"FORGED 2"}]}`,
		`{"type":"interaction_resolved","id":"ix_a","note":"FORGED 3"}`,
		`{"type":"error","machine_id":"ma","session_id":"s3","error":"FORGED 4"}`,
		`{"type":"error","error":"FORGED 5"}`,
		`{"type":"b_thing","note":"FORGED 6"}`,
		`{"type":"inbox","machine_id":"ma","note":"FORGED 7"}`,
		`{"type":"inbox","session_id":"s3","note":"FORGED 8"}`,
		`{"type":"assistant_feed","conversation":"s3","note":"FORGED 9"}`,
		`{"type":"inbox","machine_id":"mb","note":"B ABOUT ITS OWN"}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"assistant_feed","conversation":"s3","note":"A ABOUT ITS OWN"}`)...)
	if marked(got, "FORGED") {
		t.Fatalf("the apps got a forged message: %v", got)
	}
	if !marked(got, "B ABOUT ITS OWN") || !marked(got, "A ABOUT ITS OWN") {
		t.Fatalf("a manager's own messages did not reach the apps: %v", got)
	}
	if _, ok := h.pendingInteraction("ix_a"); !ok {
		t.Fatal("A's card went")
	}
}

func TestMessageSendStateCapabilityRequiresHubOwnership(t *testing.T) {
	count := 0
	for _, feature := range proto.Features {
		if feature == proto.FeatureMessageSendState {
			count++
		}
	}
	if count != 1 || !hubMessageTypes[proto.FeatureMessageSendState] || managerMessageTypes[proto.FeatureMessageSendState] {
		t.Fatalf("receipt capability must be declared once and reserved to the hub: count=%d", count)
	}
}

// The feature array includes extension claims, including on legacy hubs. The
// receipt version must be a separate, hub-authored hello field. The same probe
// runs against a40a73e1b's hello.go with PERSONA_TEST_LEGACY_RECEIPT_HELLO=1:
// forged feature claims remain visible there, but establish no receipt authority.
func TestReceiptHelloVersionIsHubAuthored(t *testing.T) {
	legacy := os.Getenv("PERSONA_TEST_LEGACY_RECEIPT_HELLO") == "1"
	if legacy {
		before := proto.Features
		proto.Features = slices.DeleteFunc(slices.Clone(before), func(feature string) bool { return feature == proto.FeatureMessageSendState })
		t.Cleanup(func() { proto.Features = before })
	}
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy feature report", true: "declared extension feature"}[declared], func(t *testing.T) {
			h, _, _ := pathsHub(t, quiet, quiet)
			h.keepExtReport("m-0b0b0b", &extwire.Report{EventsDeclared: declared,
				Features: []string{proto.FeatureMessageSendState},
				APIs:     []extwire.APIPrefix{{Prefix: "fixture", Events: []string{proto.FeatureMessageSendState}}}})
			raw, err := json.Marshal(h.hello())
			if err != nil {
				t.Fatal(err)
			}
			var hello map[string]json.RawMessage
			if err := json.Unmarshal(raw, &hello); err != nil {
				t.Fatal(err)
			}
			var features []string
			if err := json.Unmarshal(hello["features"], &features); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(features, proto.FeatureMessageSendState) {
				t.Fatal("fixture did not inject the informational feature")
			}
			version, present := hello[proto.FeatureMessageSendState]
			trusted := bytes.Equal(version, []byte("1"))
			if legacy {
				if present || trusted {
					t.Fatalf("legacy feature claim acquired receipt authority: %s", raw)
				}
			} else if !present || !trusted {
				t.Fatalf("hub-authored receipt version must be the integer 1: %s", raw)
			}
			t.Logf("legacy=%t feature_present=true receipt_version_present=%t receipt_trusted=%t", legacy, present, trusted)
		})
	}
}

// A receipt is the hub's transport statement, never an extension's event. Both
// legacy permissive reports and explicit declarations must reject a forgery,
// including one targeting a session the sending manager really owns.
func TestMessageSendStateBroadcastIsHubOnly(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy report", true: "declared extension event"}[declared], func(t *testing.T) {
			h, _, manager := pathsHub(t, quiet, quiet)
			h.keepExtReport("m-0b0b0b", &extwire.Report{EventsDeclared: declared,
				APIs: []extwire.APIPrefix{{Prefix: "fixture", Events: []string{"message_send_state", "hello"}}}})
			if !h.ownsRef("m-0b0b0b", "mb", "b1") {
				t.Fatal("fixture does not own the forged receipt's target")
			}
			app := wsApp(t, h)
			got := broadcastsTo(t, app, manager,
				`{"type":"message_send_state","machine_id":"mb","session_id":"b1","client_msg_id":"fixture-message","state":"processing","at":"2026-01-01T00:00:00Z","note":"FORGED_RECEIPT"}`,
				`{"type":"hello","features":["message_send_state"],"message_send_state":1,"note":"FORGED_HELLO"}`,
				`{"type":"inbox","machine_id":"mb","note":"LEGITIMATE_EVENT"}`)
			if marked(got, "FORGED_RECEIPT") || marked(got, "FORGED_HELLO") || !marked(got, "LEGITIMATE_EVENT") {
				t.Fatalf("receipt trust boundary: %v", got)
			}
		})
	}
}

// The hub reads a broadcast with exact keys, as the apps do: a key that differs from type, machine_id, session_id or
// conversation only by case, or any key twice in an object, at any depth, drops the message; what the apps get is the
// message as the hub read it.
func TestABroadcastReadsExactKeys(t *testing.T) {
	h, _, b := pathsHub(t, quiet, quiet)
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"interaction","Type":"inbox","interaction":{"id":"ix_b","machine_id":"ma","summary":"FORGED 1"}}`,
		`{"type":"inbox","machine_id":"ma","Machine_id":"","note":"FORGED 2"}`,
		`{"type":"inbox","session_id":"s3","SESSION_ID":"","note":"FORGED 3"}`,
		`{"type":"inbox","machine_id":"ma","machine_id":"mb","note":"FORGED 4"}`,
		`{"type":"inbox","machine_id":"mb","machine_id":"ma","note":"FORGED 5"}`,
		`{"type":"inbox","item":{"machine_id":"ma","Machine_ID":"mb"},"note":"FORGED 6"}`,
		`{"type":"inbox","note":"FORGED 7","note":"twice"}`,
		`{"type":"inbox","Conversation":"s3","note":"FORGED 8"}`,
		`{ "type" : "inbox",  "machine_id":"mb", "note":"B's <own>" }`)
	if marked(got, "FORGED") {
		t.Fatalf("the apps got a message the hub read otherwise: %v", got)
	}
	var own map[string]json.RawMessage
	for _, m := range got {
		if string(m["machine_id"]) == `"mb"` {
			own = m
		}
	}
	if own == nil || string(own["note"]) != `"B's <own>"` || string(own["type"]) != `"inbox"` {
		t.Fatalf("B's own message: %v (all: %v)", own, got)
	}
}

// Every object of a broadcast, at any depth, names only the caller's machines, sessions and conversations: a feed
// item, a plan's step or a session in a list naming another manager's is dropped with its message; a machine_id that
// is not a string too.
func TestABroadcastChecksNestedRefs(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a"}}, Profile: &assistantwire.Profile{MachineID: "ma", Main: "conv-a"}}, &res); err != nil {
		t.Fatal(err)
	}
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"assistant_feed","item":{"id":"f2","conversation":"conv-a","body":{"text":"FORGED 1"}}}`,
		`{"type":"task_plan","plan":{"id":"p","steps":[{"machine_id":"mb"},{"machine_id":"ma","title":"FORGED 2"}]}}`,
		`{"type":"inbox","items":[{"session":{"session_id":"s3","title":"FORGED 3"}}]}`,
		`{"type":"inbox","machine_id":7,"note":"FORGED 4"}`,
		`{"type":"inbox","item":{"machine_id":{"id":"ma"}},"note":"FORGED 5"}`,
		`{"type":"task_plan","plan":{"id":"p","steps":[{"machine_id":"mb","session_id":"b1","title":"B's plan"}],"conversation":null}}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"assistant_feed","item":{"id":"f3","conversation":"conv-a","body":{"text":"A's feed"}}}`)...)
	if marked(got, "FORGED") {
		t.Fatalf("the apps got a nested reference to A's: %v", got)
	}
	if !marked(got, "B's plan") || !marked(got, "A's feed") {
		t.Fatalf("a manager's own nested references were dropped: %v", got)
	}
}

// The types a manager's own extensions declare (their api contributions' events) are its to send, and only its; a
// declared type that is the hub's stays the hub's.
func TestAnExtensionsDeclaredTypes(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	h.keepExtReport("m-0b0b0b", &extwire.Report{EventsDeclared: true, APIs: []extwire.APIPrefix{{Prefix: "bx", Events: []string{"bx_changed", "interaction"}}}})
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"bx_changed","note":"B's declared"}`,
		`{"type":"interaction","interaction":{"id":"ix_b","summary":"FORGED 1"}}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"bx_changed","note":"FORGED 2"}`)...)
	if marked(got, "FORGED") || !marked(got, "B's declared") {
		t.Fatalf("declared types: %v", got)
	}
}

// A manager from before the report carried its extensions' types (a report without events_declared, or the old one
// the hub kept): any type but the hub's goes from it, still read with exact keys and about its own things only. A
// manager with no report yet sends only the managers' own types.
func TestALegacyReportPassesItsExtensionsTypes(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	h.keepExtReport("m-0b0b0b", &extwire.Report{APIs: []extwire.APIPrefix{{Prefix: "lark"}}}) // an older manager's
	h.keepExtReport(assistantwire.AssistantID, nil)
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"lark","machine_id":"mb","note":"B's legacy"}`,
		`{"type":"interaction","interaction":{"id":"ix_b","summary":"FORGED 1"}}`,
		`{"type":"lark","Type":"x","note":"FORGED 2"}`,
		`{"type":"lark","note":"FORGED 3","note":"twice"}`,
		`{"type":"lark","machine_id":"ma","note":"FORGED 4"}`,
		`{"type":"lark","items":[{"session_id":"s3","note":"FORGED 5"}]}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"lark","note":"FORGED 6"}`)...) // no report: not its type
	if marked(got, "FORGED") || !marked(got, "B's legacy") {
		t.Fatalf("a legacy report's types: %v", got)
	}
}

// A card checked once the apps were told: one whose machine moved while it was kept is taken back even when the
// move's withdrawal took it first (it was shown after), and another manager's card with the id stays.
func TestACardShownAfterAMoveIsTakenBack(t *testing.T) {
	h := newTestHub(t)
	app := wsApp(t, h)
	h.dropCard("m-0b0b0b", "ix_gone") // withdrawn by the move already, then shown: taken back
	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{"ix_a": {ID: "ix_a", Manager: assistantwire.AssistantID}}
	h.reported.mu.Unlock()
	h.dropCard("m-0b0b0b", "ix_a") // A's: stays
	got := appGot(app, 300*time.Millisecond)
	if !strings.Contains(got, `"ix_gone"`) || strings.Contains(got, `"ix_a"`) {
		t.Fatalf("the apps were told: %s", got)
	}
	if _, ok := h.pendingInteraction("ix_a"); !ok {
		t.Fatal("A's card went")
	}
}

// exactJSON: one JSON object, no key twice in an object, no case variant of a checked key at any depth or of the stamp
// at the top level.
func TestExactJSON(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{"type":"inbox","a":{"b":[1,{"c":2}]},"d":[]}`: true,
		`{"type":"inbox","machine_ids":"x","Note":"x"}`: true,
		`{"type":"inbox"} {"type":"inbox"}`:             false,
		`{"type":"inbox","a":1,"a":2}`:                  false,
		`{"type":"inbox","a":[{"b":1,"b":1}]}`:          false,
		`{"type":"inbox","TYPE":"x"}`:                   false,
		`{"type":"inbox","a":{"Session_Id":"x"}}`:       false,
		`{"type":"inbox","a":[[{"CONVERSATION":1}]]}`:   false,
		`{"type":"inbox","Manager":"x"}`:                false,
		`{"type":"inbox","a":{"Manager":"x"}}`:          true, // the sender's own field, below the stamp
		`{"type":`:                                      false,
		``:                                              false,
	} {
		if err := exactJSON([]byte(raw)); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// What a manager tells the apps says whose it is: another manager's message carries top-level "manager" (its id, over
// one it put there itself), from ws.broadcast and from its outbox alike; the assistant's carries none, as it never did.
func TestABroadcastSaysWhoseItIs(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b,
		`{"type":"inbox","machine_id":"mb","note":"b-own"}`,
		`{"type":"assistant_schedule","manager":"assistant","note":"b-claims-a"}`)
	id := "o1_" + randomHex(6) // the hand-over record stays in the database for two hours: a test run again needs its own
	if err := b.call(t, assistantwire.MethodOutbox, assistantwire.Outbox{Items: []assistantwire.OutboxItem{
		{ID: id, At: time.Now(), Message: json.RawMessage(`{"type":"todos","note":"b-outbox"}`)}}}, nil); err != nil {
		t.Fatal(err)
	}
	got = append(got, broadcastsTo(t, app, a,
		`{"type":"assistant_feed","conversation":"s3","note":"a-own"}`,
		`{"type":"inbox","manager":"m-0b0b0b","note":"a-claims-b"}`)...)
	whose := map[string]string{}
	for _, m := range got {
		var note, manager string
		_ = json.Unmarshal(m["note"], &note)
		if raw, ok := m["manager"]; ok {
			_ = json.Unmarshal(raw, &manager)
			if manager == "" {
				manager = "(" + string(raw) + ")"
			}
		}
		whose[note] = manager
	}
	want := map[string]string{"b-own": "m-0b0b0b", "b-claims-a": "m-0b0b0b", "b-outbox": "m-0b0b0b", "a-own": "", "a-claims-b": ""}
	if !reflect.DeepEqual(whose, want) {
		t.Fatalf("whose each message is: %v, want %v", whose, want)
	}
	// A case variant of the stamp beside it, from either: dropped, as for the keys the hub reads.
	got = broadcastsTo(t, app, b, `{"type":"inbox","Manager":"assistant","note":"FORGED 1"}`, `{"type":"inbox","MANAGER":"x","note":"FORGED 2"}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"inbox","Manager":"m-0b0b0b","note":"FORGED 3"}`)...)
	if marked(got, "FORGED") {
		t.Fatalf("a case variant of manager reached the apps: %v", got)
	}
	// Below the top level it is the sender's own field (an extension event carrying a contact record): the message goes
	// out whole, the stamp beside it.
	var nested map[string]json.RawMessage
	for _, m := range broadcastsTo(t, app, b, `{"type":"inbox","item":{"name":"Ann","Manager":"Bo"},"note":"nested"}`) {
		if string(m["note"]) == `"nested"` {
			nested = m
		}
	}
	if nested == nil {
		t.Fatal("a nested Manager field dropped the message")
	}
	var stamp string
	var item map[string]string
	_ = json.Unmarshal(nested["manager"], &stamp)
	_ = json.Unmarshal(nested["item"], &item)
	if stamp != "m-0b0b0b" || item["Manager"] != "Bo" {
		t.Fatalf("stamp %q, item %v", stamp, item)
	}
}
