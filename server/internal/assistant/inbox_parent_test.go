package assistant

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// A new event that continues a done one hangs under it, set by continues (#2128).
func TestInboxParentFromContinues(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	fresh := h.inbox.items["lark:F1:1061"]
	if fresh == nil || fresh.Parent == nil || *fresh.Parent != (InboxParent{Kind: ParentInbox, ID: "lark:F1:900", By: ParentByContinues}) {
		t.Fatalf("new event's parent %+v", fresh)
	}
	if p := h.inbox.items["lark:F1:1051"].Parent; p != nil {
		t.Fatalf("an event that continues nothing got parent %+v", p)
	}
	items, _ := inboxTriageOf(t, h)
	for _, x := range items {
		m := x.(map[string]any)
		if m["id"] == "lark:F1:1061" {
			p, _ := m["parent"].(map[string]any)
			if p["kind"] != "inbox" || p["id"] != "lark:F1:900" || p["by"] != "continues" {
				t.Fatalf("listed parent %v", m["parent"])
			}
		}
	}
}

func parentHub(t *testing.T, ids ...string) *inboxHub {
	t.Helper()
	h := newInboxHub(t)
	h.inbox.loaded.Store(true)
	base := time.Now().Add(-time.Hour)
	for i, id := range ids {
		h.inbox.items[id] = &InboxItem{ID: id, Source: "mail", Kind: "mail", Title: id, StartedAt: base.Add(time.Duration(i) * time.Minute),
			UpdatedAt: base}
	}
	return h
}

// The user hangs an item under another item, a todo, a session or a personal Agent; one that would make a cycle, sit too
// deep, name an unknown kind or a missing item is refused, and null or {} takes it off.
func TestInboxParentPutAndDelete(t *testing.T) {
	h := parentHub(t, "a", "b", "c")
	mux := h.routes()
	put := func(id, body string) (int, map[string]any) {
		return inboxCall(t, mux, "PUT", "/api/v1/inbox/"+id+"/parent", body)
	}
	if code, out := put("b", `{"kind":"inbox","id":"a"}`); code != 200 {
		t.Fatalf("b under a: %d %v", code, out)
	} else if p, _ := out["parent"].(map[string]any); p["by"] != "user" || p["id"] != "a" {
		t.Fatalf("answer %v", out)
	}
	if code, _ := put("c", `{"kind":"inbox","id":"b"}`); code != 200 {
		t.Fatalf("c under b: %d", code)
	}
	if !h.st.saved("b") || !h.st.saved("c") {
		t.Fatalf("not saved: %v", h.st.ids)
	}
	for name, c := range map[string]struct {
		id, body string
		code     int
	}{
		"cycle":       {"a", `{"kind":"inbox","id":"c"}`, 409},
		"itself":      {"a", `{"kind":"inbox","id":"a"}`, 409},
		"kind":        {"a", `{"kind":"chat","id":"x"}`, 400},
		"empty id":    {"a", `{"kind":"todo","id":""}`, 400},
		"agent id":    {"a", `{"kind":"agent","id":"nosource"}`, 400},
		"agent key":   {"a", `{"kind":"agent","id":"source:"}`, 400},
		"no parent":   {"a", `{"kind":"inbox","id":"nope"}`, 404},
		"no item":     {"nope", `{"kind":"todo","id":"t1"}`, 404},
		"not json":    {"a", `{`, 400},
		"control chr": {"a", `{"kind":"todo","id":"t\n1"}`, 400},
	} {
		if code, out := put(c.id, c.body); code != c.code {
			t.Errorf("%s: %d %v, want %d", name, code, out, c.code)
		}
	}
	if code, _ := put("a", `{"kind":"session","id":"m1/s1"}`); code != 200 {
		t.Fatalf("a under a session: %d", code)
	}
	if code, out := put("a", `{"kind":"agent","id":"gateway:agent/key"}`); code != 200 {
		t.Fatalf("a under a personal Agent: %d %v", code, out)
	}
	if code, out := put("c", `null`); code != 200 || out["parent"] != nil {
		t.Fatalf("clear with null: %d %v", code, out)
	}
	if code, _ := put("b", `{}`); code != 200 || h.inbox.items["b"].Parent != nil {
		t.Fatalf("clear with {}: %d", code)
	}
	if h.inbox.items["c"].Parent != nil {
		t.Fatal("c still has a parent")
	}
}

// A chain is at most maxParentDepth parents long, the todo or session on top counted.
func TestInboxParentDepth(t *testing.T) {
	var ids []string
	for i := 0; i <= maxParentDepth+1; i++ {
		ids = append(ids, fmt.Sprint("i", i))
	}
	h := parentHub(t, ids...)
	b := &h.inbox
	h.inbox.items["i0"].Parent = &InboxParent{Kind: ParentTodo, ID: "t1", By: ParentByUser}
	for i := 1; i < maxParentDepth; i++ { // i1 … i7 under i0: i7 has 8 levels above it (i6 … i0, t1)
		if err := b.parentFits(ids[i], ids[i-1]); err != nil {
			t.Fatalf("%s under %s: %v", ids[i], ids[i-1], err)
		}
		h.inbox.items[ids[i]].Parent = &InboxParent{Kind: ParentInbox, ID: ids[i-1], By: ParentByUser}
	}
	if err := b.parentFits(ids[maxParentDepth], ids[maxParentDepth-1]); err != errParentDeep {
		t.Fatalf("9 levels: %v", err)
	}
}

// A connector's parent is kept unless the user set one; it replaces one continues set; a bad one is ignored.
func TestInboxParentOffers(t *testing.T) {
	h := parentHub(t, "a", "b")
	b := &h.inbox
	it := h.inbox.items["b"]
	b.offerParent(it, &InboxParent{Kind: ParentInbox, ID: "a", By: ParentByContinues})
	b.offerParent(it, thingParent(connector.InboxThing{Parent: &connector.ThingParent{Kind: "todo", ID: "t1"}}))
	if *it.Parent != (InboxParent{Kind: ParentTodo, ID: "t1", By: ParentByConnector}) {
		t.Fatalf("connector's did not replace continues: %+v", it.Parent)
	}
	b.offerParent(it, thingParent(connector.InboxThing{Parent: &connector.ThingParent{Kind: "agent", ID: "gateway:agent/key"}}))
	if *it.Parent != (InboxParent{Kind: ParentAgent, ID: "gateway:agent/key", By: ParentByConnector}) {
		t.Fatalf("connector's personal Agent was not kept: %+v", it.Parent)
	}
	b.offerParent(it, &InboxParent{Kind: ParentInbox, ID: "a", By: ParentByContinues})
	if it.Parent.By != ParentByConnector {
		t.Fatalf("continues replaced the connector's: %+v", it.Parent)
	}
	it.Parent = &InboxParent{Kind: ParentSession, ID: "m/s", By: ParentByUser}
	b.offerParent(it, thingParent(connector.InboxThing{Parent: &connector.ThingParent{Kind: "todo", ID: "t2"}}))
	if it.Parent.By != ParentByUser {
		t.Fatalf("the user's was replaced: %+v", it.Parent)
	}
	if p := thingParent(connector.InboxThing{Parent: &connector.ThingParent{Kind: "chat", ID: "x"}}); p != nil {
		t.Fatalf("bad kind kept: %+v", p)
	}
	h.inbox.items["a"].Parent = &InboxParent{Kind: ParentInbox, ID: "b", By: ParentByUser}
	c := h.inbox.items["a"]
	c.Parent = nil
	h.inbox.items["b"].Parent = &InboxParent{Kind: ParentInbox, ID: "a", By: ParentByContinues}
	b.offerParent(c, &InboxParent{Kind: ParentInbox, ID: "b", By: ParentByConnector})
	if c.Parent != nil {
		t.Fatalf("a cycle was offered and kept: %+v", c.Parent)
	}
}

// The tree of an item lists its ancestors up to the todo on top and its children oldest first; a todo's lists the
// items right under it, and a personal Agent can end the chain.
func TestInboxTree(t *testing.T) {
	h := parentHub(t, "a", "b", "c", "d")
	h.inbox.items["a"].Parent = &InboxParent{Kind: ParentTodo, ID: "t1", By: ParentByUser}
	h.inbox.items["b"].Parent = &InboxParent{Kind: ParentInbox, ID: "a", By: ParentByContinues}
	h.inbox.items["d"].Parent = &InboxParent{Kind: ParentInbox, ID: "b", By: ParentByUser}
	h.inbox.items["c"].Parent = &InboxParent{Kind: ParentInbox, ID: "b", By: ParentByUser}
	mux := h.routes()
	code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/inbox/b", "")
	if code != 200 {
		t.Fatalf("tree: %d %v", code, out)
	}
	ups, _ := out["ancestors"].([]any)
	kids, _ := out["children"].([]any)
	if len(ups) != 2 || ups[0].(map[string]any)["id"] != "a" || ups[0].(map[string]any)["item"] == nil ||
		ups[1].(map[string]any)["kind"] != "todo" || ups[1].(map[string]any)["item"] != nil {
		t.Fatalf("ancestors %v", ups)
	}
	if len(kids) != 2 || kids[0].(map[string]any)["id"] != "c" || kids[1].(map[string]any)["id"] != "d" {
		t.Fatalf("children %v", kids)
	}
	if code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/todo/t1", ""); code != 200 || len(out["children"].([]any)) != 1 || out["item"] != nil {
		t.Fatalf("todo tree: %d %v", code, out)
	}
	if code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/session/m1/s1", ""); code != 200 || len(out["children"].([]any)) != 0 {
		t.Fatalf("session tree: %d %v", code, out)
	}
	h.inbox.items["d"].Parent = &InboxParent{Kind: ParentAgent, ID: "gateway:agent/key", By: ParentByUser}
	if code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/inbox/d", ""); code != 200 {
		t.Fatalf("personal Agent parent tree: %d %v", code, out)
	} else if ups := out["ancestors"].([]any); len(ups) != 1 || ups[0].(map[string]any)["kind"] != "agent" || ups[0].(map[string]any)["item"] != nil {
		t.Fatalf("personal Agent ancestors %v", ups)
	}
	if code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/agent/gateway:agent%2Fkey", ""); code != 200 || len(out["children"].([]any)) != 1 {
		t.Fatalf("personal Agent tree: %d %v", code, out)
	}
	if code, _ := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/inbox/nope", ""); code != 404 {
		t.Fatalf("missing item: %d", code)
	}
	if code, _ := inboxCall(t, mux, "GET", "/api/v1/inbox/tree/chat/x", ""); code != 400 {
		t.Fatalf("bad kind: %d", code)
	}
}
