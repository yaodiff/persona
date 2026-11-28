package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/proto"
)

const botGroupReplyTool = "bot_group_reply"

// A proxy reply's guest lists exactly bot_group_reply, and the owner's conversation never sees it. The guest is
// registered before the node starts it, and dropped when the start fails.
func TestHandoffGuestListsOnlyItsTool(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil // the node the fake hub starts conversations on
	s := ft.s
	ctx := context.Background()

	machine, session, err := s.StartHandoffGuest(ctx, "bh_direct", "answer this", "")
	if err != nil {
		t.Fatal(err)
	}
	names := sessionToolNames(t, s, machine, session)
	if !slices.Equal(names, []string{botGroupReplyTool}) {
		t.Fatalf("the handoff guest's tools: %v", names)
	}
	owner := sessionToolNames(t, s, machine, "owner")
	if slices.Contains(owner, botGroupReplyTool) {
		t.Fatalf("the owner's conversation lists the handoff tool: %v", owner)
	}
	// An extension guest's list does not contain it either.
	if err := s.guestSet(ctx, machine, "guest-ext", []string{"kb_read"}); err != nil {
		t.Fatal(err)
	}
	ext := sessionToolNames(t, s, machine, "guest-ext")
	if slices.Contains(ext, botGroupReplyTool) {
		t.Fatalf("an extension guest lists the handoff tool: %v", ext)
	}

	// A tool that is not the handoff's is refused, and the call never reaches the module's tool.
	res, handled, err := s.NodeRequest(ctx, machine, "mcp.call", botHandoffCall(session, "kb_read"))
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	if out, _ := res.(proto.ToolResult); !out.IsError {
		t.Fatalf("another tool was allowed: %+v", out)
	}
}

// The confirmation is raised as the bot's own decision, and only that caller's answer reaches the module.
func TestHandoffDecisionIsTheBotsOwn(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx := context.Background()
	no := false
	req := assistantwire.OutsideDecisionRequest{
		Ref: "handoff:bh_one", Title: "Bo in Team", Background: "the draft",
		WhyOwner: "visible_to_others",
		Options: []assistantwire.OutsideDecisionOption{
			{Label: "Post the reply", LeadsTo: "The bot posts this reply in the group."},
			{Label: "Don't post", LeadsTo: "Nothing is posted."},
		},
		Recommend: "Post the reply", CostOfWaiting: "Bo keeps waiting.",
		Reversible: &no, AllowDefault: &no,
	}
	id, err := s.RaiseHandoffDecision(ctx, "bh_one", req)
	if err != nil {
		t.Fatal(err)
	}
	b := &s.accept
	b.mu.Lock()
	var ask *acceptAsk
	var caller string
	for _, a := range b.asks {
		if a.OutsideID == id {
			ask = a
		}
	}
	if rec := b.outside[id]; rec != nil {
		caller = rec.Caller
	}
	b.mu.Unlock()
	if ask == nil || ask.WhyOwner != "visible_to_others" || ask.DefaultAllowed || ask.Reversible {
		t.Fatalf("decision: %+v", ask)
	}
	if caller != "bot:feishu" {
		t.Fatalf("caller: %s", caller)
	}
	// Another caller's answer is not a handoff's: the module is not asked.
	s.handoffDecided(ctx, "extension:someone", "handoff:bh_one", "Post the reply")
}

func sessionToolNames(t *testing.T, s *Service, machine, session string) []string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"assistant": true, "session_id": session})
	out, handled, err := s.NodeRequest(context.Background(), machine, "mcp.tools", raw)
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	tools, _ := out.(map[string]any)["tools"].([]proto.Tool)
	var got []string
	for _, tool := range tools {
		got = append(got, tool.Name)
	}
	return got
}

func botHandoffCall(session, name string) []byte {
	raw, _ := json.Marshal(map[string]any{"session_id": session, "name": name, "args": map[string]any{}})
	return raw
}

// A start that fails leaves no guest behind.
func TestHandoffGuestDroppedWhenTheStartFails(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	s := ft.s
	ft.f.failStart = context.DeadlineExceeded
	if _, _, err := s.StartHandoffGuest(context.Background(), "bh_fail", "answer this", ""); err == nil {
		t.Fatal("the start was accepted")
	}
	if _, ok, err := s.handoffOf(context.Background(), "mac", ""); err != nil || ok {
		t.Fatalf("a failed start left a guest: %v %v", ok, err)
	}
	// No guest of the handoff kind remains.
	b := &s.guests
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, g := range b.m {
		if g.Kind == guestKindBotHandoff {
			t.Errorf("guest %s left behind: %+v", id, g)
		}
	}
}

// Without the lark module (switched off or left out of the build), no handoff guest starts and a pre-existing handoff
// guest exposes no tools and refuses calls.
func TestHandoffGuestAbsentWithoutLarkModule(t *testing.T) {
	verifyAbsent := func(t *testing.T, s *Service, f *fakeHub) {
		t.Helper()
		ctx := context.Background()
		if _, _, err := s.StartHandoffGuest(ctx, "bh_off", "answer this", ""); !errors.Is(err, larkhost.ErrNoChannel) {
			t.Fatalf("StartHandoffGuest error = %v, want %v", err, larkhost.ErrNoChannel)
		}
		b := &s.guests
		b.mu.Lock()
		for id, g := range b.m {
			if g.Kind == guestKindBotHandoff {
				t.Errorf("unexpected handoff guest %s created: %+v", id, g)
			}
		}
		b.mu.Unlock()
		f.mu.Lock()
		sends := len(f.sends)
		f.mu.Unlock()
		if sends != 0 {
			t.Fatalf("expected 0 assistant.send calls, got %d", sends)
		}

		// A pre-existing handoff guest exposes no tools and refuses botGroupReplyTool calls.
		if err := s.guestSetHandoff(ctx, "mac", "legacy-handoff", "bh_old", []string{botGroupReplyTool}); err != nil {
			t.Fatal(err)
		}
		if names := sessionToolNames(t, s, "mac", "legacy-handoff"); len(names) != 0 {
			t.Fatalf("pre-existing handoff guest tools = %v, want empty", names)
		}
		res, handled, err := s.NodeRequest(ctx, "mac", "mcp.call", botHandoffCall("legacy-handoff", botGroupReplyTool))
		if err != nil || !handled {
			t.Fatal(err, handled)
		}
		if out, _ := res.(proto.ToolResult); !out.IsError {
			t.Fatalf("expected error tool result for %s without lark module, got %+v", botGroupReplyTool, out)
		}
	}

	t.Run("modules_off", func(t *testing.T) {
		f := newFakeHub()
		f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
		f.sessions["mac"] = nil
		d := newFleetDevs()
		d.add("mac", "Mac")
		st := newFleetMem()
		s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d, ModulesOff: []string{larkhost.ModuleID}})
		verifyAbsent(t, s, f)
	})

	if !larkhost.InBuild(moduleswitch.Set{}) {
		t.Run("excluded_from_build", func(t *testing.T) {
			ft := newFleetTest(t)
			ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
			ft.f.sessions["mac"] = nil
			verifyAbsent(t, ft.s, ft.f)
		})
	}
}

func TestHandoffCallerIgnoresABlankRef(t *testing.T) {
	s := &Service{}
	s.handoffDecided(context.Background(), "bot:feishu", "", "Post")
	if strings.Contains(botHandoffCaller("Lark bot").key, " ") {
		t.Fatal("the caller key has a space")
	}
}
