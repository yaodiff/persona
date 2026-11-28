package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

// larkSentManager is a manager with the ledger in memory and a computer "box" connected.
func larkSentManager(t *testing.T) (*Manager, *sentLedgerStore) {
	t.Helper()
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	box := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	h.machines, h.byID["box"] = append(h.machines, box), box
	return h, st
}

func larkSentParams(t *testing.T, s proto.LarkSent, extra map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		for k, v := range extra {
			m[k] = v
		}
		if raw, err = json.Marshal(m); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

// A write a node's gate proxy told of is a row of connector lark: the call as the action, where it went, what it said,
// the computer and conversation it ran for and how the gate let it through, in the shapes the gated writes use.
func TestRecordNodeSentWritesARow(t *testing.T) {
	h, st := larkSentManager(t)
	ctx := context.Background()
	send := func(s proto.LarkSent) {
		t.Helper()
		if _, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, s, nil)); err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
	}

	send(proto.LarkSent{Command: "ex", Method: "post", Path: "/conversations/oc_1/messages", Text: "hello", SessionID: "conv1",
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_9", AnsweredBy: proto.AnsweredByAssistant, EditedFrom: "original text"}})
	send(proto.LarkSent{Method: "POST", Path: "/messages/om_2/reactions",
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughAlways, EditedFrom: "must not leak"}})
	send(proto.LarkSent{Method: "POST", Path: "/conversations", Text: "hi",
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughRule, Rule: &proto.LarkRule{Method: "POST", Path: "/conversations", Body: map[string]any{"type": "p2p"}, Action: "allow"}}})
	send(proto.LarkSent{Method: "DELETE", Path: "/messages/om_3",
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughRule, Rule: &proto.LarkRule{Method: "*", Path: "**", Action: "allow"}}})
	send(proto.LarkSent{Method: "GET", Path: "/links/open",
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_get"}})

	rows := st.all()
	if len(rows) != 5 {
		t.Fatalf("rows %+v", rows)
	}
	r := rows[0]
	if r.Connector != connector.Lark || r.Action != "POST /conversations/oc_1/messages" || r.Text != "hello" || r.Summary != r.Action || r.At.IsZero() {
		t.Fatalf("row %+v", r)
	}
	if target := objectOf(t, r.Target); target["method"] != "POST" || target["path"] != "/conversations/oc_1/messages" || target["conversation"] != "oc_1" || target["command"] != "ex" || len(target) != 4 {
		t.Fatalf("target %s", r.Target)
	}
	if origin := objectOf(t, r.Origin); origin["machine_id"] != "box" || origin["session_id"] != "conv1" || len(origin) != 2 {
		t.Fatalf("origin %s", r.Origin)
	}
	if through := objectOf(t, r.LetThrough); through["kind"] != "card" || through["interaction_id"] != "ix_9" || through["answered_by"] != "assistant" || through["edited_from"] != "original text" || len(through) != 4 {
		t.Fatalf("let through %s", r.LetThrough)
	}

	if target := objectOf(t, rows[1].Target); target["message"] != "om_2" || target["conversation"] != nil || len(target) != 3 {
		t.Fatalf("target %s", rows[1].Target)
	}
	if origin := objectOf(t, rows[1].Origin); origin["session_id"] != nil || len(origin) != 1 {
		t.Fatalf("a write the node knew no conversation of: origin %s", rows[1].Origin)
	}
	if through := objectOf(t, rows[1].LetThrough); through["kind"] != "conversation_always" || len(through) != 1 {
		t.Fatalf("let through %s", rows[1].LetThrough)
	}

	through := objectOf(t, rows[2].LetThrough)
	rule, _ := through["rule"].(map[string]any)
	if through["kind"] != "rule" || rule["connector"] != "lark" || rule["action"] != "POST /conversations" || rule["decision"] != "allow" ||
		objectOf(t, mustJSON(rule["match"]))["type"] != "p2p" || len(rule) != 4 {
		t.Fatalf("let through %s", rows[2].LetThrough)
	}
	if target := objectOf(t, rows[2].Target); len(target) != 2 { // a path with no ids and a call with no command name none
		t.Fatalf("target %s", rows[2].Target)
	}
	if rule, _ := objectOf(t, rows[3].LetThrough)["rule"].(map[string]any); rule["action"] != "*" || rule["match"] != nil {
		t.Fatalf("a rule for any call: %s", rows[3].LetThrough)
	}
	if rows[4].Action != "GET /links/open" {
		t.Fatalf("the side-effecting GET was not recorded: %+v", rows[4])
	}
}

// Only a computer of this manager's tells it of a send, and the computer is the connection's: whatever the params
// name as the machine, the origin, the connector or the action is not believed.
func TestRecordNodeSentTrustsTheConnectionOnly(t *testing.T) {
	h, st := larkSentManager(t)
	ctx := context.Background()
	s := proto.LarkSent{Method: "POST", Path: "/conversations/oc_1/messages", Text: "hi", LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughAlways}}

	for _, machine := range []string{"", "stranger"} {
		_, err := h.RecordNodeSent(ctx, machine, larkSentParams(t, s, nil))
		wantRPCCode(t, "machine "+machine, err, extension.CodePermissionDenied)
	}
	if rows := st.all(); len(rows) != 0 {
		t.Fatalf("rows from a computer nobody knows: %+v", rows)
	}

	forged := map[string]any{"machine_id": "stranger", "origin": map[string]any{"machine_id": "stranger", "extension": "x"},
		"connector": "github", "action": "other", "summary": "forged"}
	if _, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, s, forged)); err != nil {
		t.Fatal(err)
	}
	rows := st.all()
	if len(rows) != 1 || rows[0].Connector != "lark" || rows[0].Action != "POST /conversations/oc_1/messages" || rows[0].MessageRef != "" || rows[0].Summary != rows[0].Action {
		t.Fatalf("rows %+v", rows)
	}
	if origin := objectOf(t, rows[0].Origin); origin["machine_id"] != "box" || len(origin) != 1 {
		t.Fatalf("origin %s", rows[0].Origin)
	}
	if through := objectOf(t, rows[0].LetThrough); through["kind"] != "conversation_always" || len(through) != 1 {
		t.Fatalf("let through %s", rows[0].LetThrough)
	}
}

// A report that is not a write, or does not say how it got through, is refused and nothing is written; the text is cut
// to 4000 characters and cleaned like a gated write's; without a database there is no ledger.
func TestRecordNodeSentChecksWhatItIsTold(t *testing.T) {
	h, st := larkSentManager(t)
	ctx := context.Background()
	always := proto.LarkLetThrough{Kind: proto.LarkThroughAlways}
	for name, s := range map[string]proto.LarkSent{
		"a read":              {Method: "GET", Path: "/me", LetThrough: always},
		"no method":           {Path: "/me", LetThrough: always},
		"no path":             {Method: "POST", LetThrough: always},
		"a path past a host":  {Method: "POST", Path: "api.example.test/x", LetThrough: always},
		"no way through":      {Method: "POST", Path: "/x"},
		"an unknown way":      {Method: "POST", Path: "/x", LetThrough: proto.LarkLetThrough{Kind: "auto"}},
		"a rule left out":     {Method: "POST", Path: "/x", LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughRule}},
		"a rule that confirm": {Method: "POST", Path: "/x", LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughRule, Rule: &proto.LarkRule{Method: "*", Path: "**", Action: "confirm"}}},
	} {
		_, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, s, nil))
		wantRPCCode(t, name, err, "invalid_params")
	}
	if _, err := h.RecordNodeSent(ctx, "box", json.RawMessage(`[1]`)); err == nil {
		t.Error("params that are no object were taken")
	}
	if rows := st.all(); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}

	long := strings.Repeat("好", connector.SentTextMost+50)
	if _, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, proto.LarkSent{Method: "PUT", Path: "/docs/" + strings.Repeat("x", 500), Text: long + "\x00",
		SessionID: "conv\x00", LetThrough: always}, nil)); err != nil {
		t.Fatal(err)
	}
	r := st.all()[0]
	if r.Text != strings.Repeat("好", connector.SentTextMost) || len([]rune(r.Action)) > sentValueMost+10 || strings.Contains(string(r.Origin), `\u0000`) {
		t.Fatalf("row %q… %d characters of action, origin %s", r.Text[:6], len([]rune(r.Action)), r.Origin)
	}

	h.store = newKeepStore() // no ledger behind it
	_, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, proto.LarkSent{Method: "POST", Path: "/x", LetThrough: always}, nil))
	wantRPCCode(t, "without a database", err, "no_database")
}

// The ids a path names are read the way REST paths say them, or not at all.
func TestGatePathIDs(t *testing.T) {
	for path, want := range map[string]map[string]string{
		"/conversations/oc_1/messages":         {"conversation": "oc_1"},
		"/conversations/oc_1/messages/om_2":    {"conversation": "oc_1", "message": "om_2"},
		"/messages/om_2/reactions":             {"message": "om_2"},
		"/conversations":                       {},
		"/conversations/":                      {},
		"/conversations//messages":             {},
		"/messages/om_1/forward/messages/om_2": {},
		"/search/page":                         {},
		"/conversations/*/read-position":       {},
	} {
		got := gatePathIDs(path)
		if len(got) != len(want) {
			t.Errorf("%s: %v, want %v", path, got, want)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %v, want %v", path, got, want)
			}
		}
	}
}

// The id the API's answer gave is the row's message_ref only for a route that creates one message in a conversation its
// path names (the text send and the attachments'): another write's answer may carry the id of a message that is not the
// one it wrote, and a wrong ref is worse than none. The id is the node's word as the API said it, trimmed, with no NUL,
// and none at all when it is over 200 characters (not cut: a cut id points at nothing); a report without one (an older
// node, or an answer that had none) is a row all the same.
func TestRecordNodeSentKeepsTheMessageRefOfACreationRoute(t *testing.T) {
	h, st := larkSentManager(t)
	ctx := context.Background()
	always := proto.LarkLetThrough{Kind: proto.LarkThroughAlways}
	for _, c := range []struct{ name, method, path, ref, want string }{
		{"a text send", "POST", "/conversations/oc_1/messages", "om_1", "om_1"},
		{"an id of the API's own form", "POST", "/conversations/oc_1/messages", "x:123/45", "x:123/45"},
		{"an image", "POST", "/conversations/oc_1/images", "om_2", "om_2"},
		{"a file", "POST", "/conversations/oc_1/files", "om_3", "om_3"},
		{"an attachment", "POST", "/conversations/oc_1/attachments", "om_4", "om_4"},
		{"trimmed and without NUL", "POST", "/conversations/oc_1/messages", " om_\x005 \n", "om_5"},
		{"exactly 200 characters", "POST", "/conversations/oc_1/messages", strings.Repeat("长", 200), strings.Repeat("长", 200)},
		{"over 200 characters is none, not cut", "POST", "/conversations/oc_1/messages", strings.Repeat("长", 201), ""},
		{"an answer with none", "POST", "/conversations/oc_1/messages", "", ""},
		{"a reply", "POST", "/messages/om_1/replies", "om_new", ""},
		{"a forward", "POST", "/messages/om_1/forward", "om_new", ""},
		{"a reaction", "POST", "/messages/om_1/reactions", "om_1", ""},
		{"a read position", "POST", "/conversations/oc_1/read-position", "om_77", ""},
		{"a conversation's settings", "POST", "/conversations/oc_1/settings", "om_77", ""},
		{"an edit", "PUT", "/conversations/oc_1/messages", "om_8", ""},
		{"no conversation", "POST", "/conversations//messages", "om_9", ""},
		{"a wildcard conversation", "POST", "/conversations/*/messages", "om_9", ""},
		{"a deeper path", "POST", "/conversations/oc_1/messages/om_1", "om_9", ""},
		{"a new conversation", "POST", "/conversations", "oc_new", ""},
		{"another collection's messages", "POST", "/threads/oc_1/messages", "om_9", ""},
		{"another collection's files", "POST", "/documents/d1/files", "om_9", ""},
	} {
		before := len(st.all())
		if _, err := h.RecordNodeSent(ctx, "box", larkSentParams(t, proto.LarkSent{Method: c.method, Path: c.path, Text: "hi", MessageRef: c.ref, LetThrough: always}, nil)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		rows := st.all()
		if len(rows) != before+1 || rows[before].MessageRef != c.want || rows[before].Text != "hi" {
			t.Errorf("%s: %+v, want message_ref %q", c.name, rows[before:], c.want)
		}
	}
}
