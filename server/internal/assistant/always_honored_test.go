package assistant

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// honored is a card's AlwaysHonored as the apps read it: "absent", "true" or "false".
func honored(ix proto.Interaction) string {
	switch {
	case ix.AlwaysHonored == nil:
		return "absent"
	case *ix.AlwaysHonored:
		return "true"
	}
	return "false"
}

// Whether "Always allow" is kept is the manager's word (#3590), from its two sources: a node's card says true only
// when the manager remembers its keys, and a node's own say is dropped; the manager's own cards say false unless they
// name what they keep, and carry their tool's name for people.
func TestAlwaysHonoredIsTheManagersWord(t *testing.T) {
	yes, no := true, false

	// A node's card with keys, on a manager that remembers: true. A node cannot claim it, nor name a tool for people.
	ft := newFleetTest(t)
	s := ft.s
	keyed := alwaysIx("ix_k", "conv", "tool:Bash")
	keyed.ToolTitle = "Something friendly"
	go func() { _, _ = s.nodeInteraction(context.Background(), keyed) }()
	got := ft.waitPending(1)[0]
	if honored(got) != "true" || got.ToolTitle != "" {
		t.Fatalf("a remembered card: always_honored %s, tool_title %q", honored(got), got.ToolTitle)
	}
	_ = s.resolve("ix_k", proto.Resolution{Allow: &no})
	ft.waitPending(0)

	for i, said := range []*bool{nil, &yes, &no} {
		id := fmt.Sprintf("ix_b%d", i)
		bare := alwaysIx(id, "agent") // no keys: an Agents session's card, its agent keeps Always itself
		bare.AlwaysHonored = said
		go func() { _, _ = s.nodeInteraction(context.Background(), bare) }()
		if got := ft.waitPending(1)[0]; honored(got) != "absent" {
			t.Fatalf("a node's card without keys (it said %v): always_honored %s", said, honored(got))
		}
		_ = s.resolve(id, proto.Resolution{Allow: &no})
		ft.waitPending(0)
	}

	// An assistant conversation's card with no keys (a gated command confirmed whole, #9075): nothing keeps Always there,
	// so false, and an Always answered anyway allows once. By the node's directory, else by the conversation listed.
	m, _ := s.fleetMachine("mac")
	ft.sessions("mac", proto.Session{SessionID: "listed", Agent: "claude", Cwd: m.assistantCwd(), Status: proto.StatusWorking})
	for _, c := range []struct{ id, session, cwd string }{{"ix_whole", "conv", m.assistantCwd()}, {"ix_listed", "listed", ""}} {
		whole := alwaysIx(c.id, c.session)
		whole.Cwd = c.cwd
		heard := make(chan proto.Resolution, 1)
		go func() { r, _ := s.nodeInteraction(context.Background(), whole); heard <- r }()
		if got := ft.waitPending(1)[0]; honored(got) != "false" {
			t.Fatalf("%s: an assistant conversation's card with no keys: always_honored %s", c.id, honored(got))
		}
		if err := s.resolve(c.id, proto.Resolution{Allow: &yes, Always: true}); err != nil {
			t.Fatal(err)
		}
		if r := <-heard; r.Allow == nil || !*r.Allow || r.Always {
			t.Fatalf("%s: an Always on a card that keeps none: %+v", c.id, r)
		}
		ft.waitPending(0)
	}

	// An assistant conversation's card the manager will not remember (only it keeps Always there): false. Without a
	// store, with more keys than are kept, with fields, or a health conversation's (confirmed each time, keys or not).
	many := make([]string, maxAlwaysKeys+1)
	for i := range many {
		many[i] = "tool:Bash"
	}
	fields := alwaysIx("ix_f", "conv", "tool:Bash")
	fields.Fields = []proto.Field{{Key: "password", Label: "Password", Secret: true}}
	for name, got := range map[string]proto.Interaction{
		"no store":           nodeCard(alwaysIx("ix_n", "conv", "tool:Bash"), false, false, true),
		"too many":           nodeCard(alwaysIx("ix_m", "conv", many...), true, false, true),
		"fields":             nodeCard(fields, true, false, true),
		"health":             nodeCard(alwaysIx("ix_h", "conv", "tool:Bash"), true, true, true),
		"health, no keys":    nodeCard(alwaysIx("ix_h2", "conv"), true, true, true),
		"assistant, no keys": nodeCard(alwaysIx("ix_a", "conv"), true, false, true), // a gated command confirmed whole (#9075)
	} {
		if honored(got) != "false" {
			t.Fatalf("%s: always_honored %s", name, honored(got))
		}
	}
	if got := nodeCard(alwaysIx("ix_q", "conv", "tool:Bash"), true, false, true); honored(got) != "true" {
		t.Fatalf("a remembered card: always_honored %s", honored(got))
	}

	// The manager's own cards, through the hub's ask (the extensions' tools, the hub's persona_settings): false, and
	// the tool by its name.
	ctx := context.Background()
	l := s.lang(ctx)
	n := 0
	own := func(ix proto.Interaction) proto.Interaction {
		t.Helper()
		n++
		id := fmt.Sprintf("ix_own%d", n)
		ix.ID, ix.MachineID, ix.SessionID, ix.Kind = id, "mac", "conv", proto.IxPermission
		ix.OutwardWrite = true // these cases exercise explicit owner grants, not internal tool review
		ix.ExpiresAt = time.Now().Add(time.Hour)
		go func() { _, _ = s.AskCard(ctx, ix) }()
		got := ft.waitPending(1)[0]
		_ = s.resolve(id, proto.Resolution{Allow: &no})
		ft.waitPending(0)
		return got
	}
	// An Always sent for a card that does not keep it (an older app) is dropped: the card was allowed once.
	honestID := "ix_honest"
	heard := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.AskCard(ctx, proto.Interaction{ID: honestID, MachineID: "mac", SessionID: "conv", Kind: proto.IxPermission, OutwardWrite: true,
			ToolName: "iphone_do", ExpiresAt: time.Now().Add(time.Hour)})
		heard <- r
	}()
	ft.waitPending(1)
	if err := s.resolve(honestID, proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-heard; r.Allow == nil || !*r.Allow || r.Always {
		t.Fatalf("an Always on a card that keeps none: %+v", r)
	}
	ft.waitPending(0)

	if got := own(proto.Interaction{ToolName: "iphone_do"}); honored(got) != "false" || got.ToolTitle != i18n.T(l, "assistant-tool_titles.iphone_do") || got.ToolTitle == "" {
		t.Fatalf("iphone_do's card: always_honored %s, tool_title %q", honored(got), got.ToolTitle)
	}
	if got := own(proto.Interaction{ToolName: "extension.ui"}); got.ToolTitle != i18n.T(l, "assistant-tool_titles.extension_ui") {
		t.Fatalf("extension.ui's title: %q", got.ToolTitle)
	}
	// A card that says what it keeps (the apps a computer may use) keeps it; a connector's tool has no name here.
	if got := own(proto.Interaction{ToolName: "mail_send", AlwaysLabel: "Always allow Mail"}); honored(got) != "true" || got.ToolTitle != "" {
		t.Fatalf("a card naming what it keeps: always_honored %s, tool_title %q", honored(got), got.ToolTitle)
	}
	// Not a permission card: nothing to say.
	if got := s.ownCard(ctx, proto.Interaction{Kind: proto.IxQuestion, ToolName: "iphone_do"}); honored(got) != "absent" || got.ToolTitle != "" {
		t.Fatalf("a question: always_honored %s, tool_title %q", honored(got), got.ToolTitle)
	}

	// The assistant's own cards (its to-do and health confirmations) the same.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.ask(ctx, proto.Interaction{ID: "ix_todo", MachineID: "mac", SessionID: "conv", Kind: proto.IxPermission, OutwardWrite: true,
			ToolName: "todo_delete", ExpiresAt: time.Now().Add(time.Hour)})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.questions.mu.Lock()
		q := s.questions.m["ix_todo"]
		s.questions.mu.Unlock()
		if q != nil {
			if honored(q.ix) != "false" || q.ix.ToolTitle != i18n.T(l, "assistant-tool_titles.todo_delete") {
				t.Fatalf("todo_delete's card: always_honored %s, tool_title %q", honored(q.ix), q.ix.ToolTitle)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the to-do card never showed")
		}
		<-time.After(5 * time.Millisecond)
	}
	_ = s.resolve("ix_todo", proto.Resolution{Allow: &no})
	<-done
}
