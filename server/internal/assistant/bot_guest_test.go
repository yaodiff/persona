package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A group guest lists the reply tool and the connector tools the owner named, and nothing of the owner's own.
func TestGroupGuestListsOnlyItsTools(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil
	ft.s.ext = &guestExt{}
	s := ft.s
	ctx := context.Background()

	machine, session, err := s.StartGroupGuest(ctx, "oc_a/om_1", "answer this", "", []string{"lark_chats", "kb_folder_search"})
	if err != nil {
		t.Fatal(err)
	}
	got := sessionToolNames(t, s, machine, session)
	if !slices.Equal(got, []string{botGroupReplyTool, "lark_chats"}) {
		t.Fatalf("tools: %v", got)
	}
	key, ok, err := s.groupGuestOf(ctx, machine, session)
	if err != nil || !ok || key != "oc_a/om_1" {
		t.Fatalf("group guest: %q %v %v", key, ok, err)
	}
	// The owner's conversation never sees the reply tool.
	owner := sessionToolNames(t, s, machine, "owner")
	if slices.Contains(owner, botGroupReplyTool) {
		t.Fatalf("the owner's conversation lists the reply tool: %v", owner)
	}
}

// A tool that is not a connector's, or that the owner did not name, is dropped before the guest starts.
func TestGroupGuestDropsToolsThatFailTheGuestCheck(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil
	ft.s.ext = &guestExt{}
	s := ft.s

	_, session, err := s.StartGroupGuest(context.Background(), "oc_a/om_1", "answer this", "",
		[]string{"agent_start", "not_a_connector", "lark_chats"})
	if err != nil {
		t.Fatal(err)
	}
	b := &s.guests
	b.mu.Lock()
	g := b.m[session]
	b.mu.Unlock()
	if g == nil || !slices.Equal(g.Tools, []string{botGroupReplyTool, "lark_chats"}) {
		t.Fatalf("the guest kept a tool it should have dropped: %+v", g)
	}
}

// A tool the guest was not given is refused, and the reply tool is the module's, not the assistant's.
func TestGroupGuestRefusesEverythingElse(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil
	s := ft.s
	ctx := context.Background()

	machine, session, err := s.StartGroupGuest(ctx, "oc_a/om_1", "answer this", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent_start", "kb_read", "phone_call", "lark_chats"} {
		res, handled, err := s.NodeRequest(ctx, machine, "mcp.call", botHandoffCall(session, name))
		if err != nil || !handled {
			t.Fatal(err, handled)
		}
		out, _ := res.(proto.ToolResult)
		if !out.IsError {
			t.Fatalf("%s was allowed: %+v", name, out)
		}
	}
	// The reply tool is the module's: with a turn the module is answering, the call reaches it and it posts through
	// feishu.text carrying the group key's turn, instead of running as one of the assistant's tools.
	if err := ft.st.PutSetting(ctx, "bots.feishu.guest_convs", map[string]any{
		"oc_a/om_1": map[string]any{
			"key": "oc_a/om_1", "chat_id": "oc_a", "started_at": time.Now(), "last_at": time.Now(),
			"turn": map[string]any{"id": "bg_1", "state": "accepted", "at": time.Now()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var posted []json.RawMessage
	ft.f.answerApp(AppFeishu, assistantwire.FeishuText, func(params json.RawMessage) (any, error) {
		posted = append(posted, append(json.RawMessage(nil), params...))
		return map[string]any{}, nil
	})
	raw, _ := json.Marshal(map[string]any{"session_id": session, "name": botGroupReplyTool, "args": map[string]any{"text": "here is the answer"}})
	res, handled, err := s.NodeRequest(ctx, machine, "mcp.call", raw)
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	if out, _ := res.(proto.ToolResult); out.IsError || len(posted) != 1 || !bytes.Contains(posted[0], []byte(`"guest":"bg_1"`)) {
		t.Fatalf("the reply tool did not reach the module: %+v, posted %s", out, posted)
	}
}

// A work machine never hosts a group guest: it lists no tools and refuses every call.
func TestGroupGuestGetsNoWorkMachine(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.d.add("work", "Work").trust = "work"
	s := ft.s
	ctx := context.Background()
	if err := s.guestSetGroup(ctx, "work", "sess-work", "oc_a/om_1", []string{botGroupReplyTool}); err != nil {
		t.Fatal(err)
	}
	if got := sessionToolNames(t, s, "work", "sess-work"); len(got) != 0 {
		t.Fatalf("a work machine listed tools: %v", got)
	}
	res, handled, err := s.NodeRequest(ctx, "work", "mcp.call", botHandoffCall("sess-work", botGroupReplyTool))
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	if out, _ := res.(proto.ToolResult); !out.IsError {
		t.Fatalf("a work machine ran the tool: %+v", out)
	}
}

// A follow-up is one session.send_once carrying the turn's id. A session the node no longer has is larkhost.ErrGuestGone.
func TestGroupGuestFollowUpIsSentOnce(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.sessions["mac"] = nil // the node the fake hub delivers session.send_once to
	s := ft.s
	ctx := context.Background()
	if err := s.SendGroupGuest(ctx, "mac", "sess-1", "bg_2", "the next question"); err != nil {
		t.Fatal(err)
	}
	sent := ft.f.sentOnce
	if len(sent) != 1 || sent[0]["client_msg_id"] != "bg:bg_2" || sent[0]["session_id"] != "sess-1" {
		t.Fatalf("sent: %v", sent)
	}
	ft.f.failSendOnce = map[string]error{"mac/sess-1": &rpc.Error{Code: codeSessionNotFound}}
	if err := s.SendGroupGuest(ctx, "mac", "sess-1", "bg_3", "again"); !errors.Is(err, larkhost.ErrGuestGone) {
		t.Fatalf("err: %v", err)
	}
}

// A start that fails leaves no guest behind.
func TestGroupGuestDroppedWhenTheStartFails(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil
	ft.f.failAssistant = context.DeadlineExceeded
	s := ft.s
	if _, _, err := s.StartGroupGuest(context.Background(), "oc_a/om_1", "answer this", "", nil); err == nil {
		t.Fatal("the start was accepted")
	}
	b := &s.guests
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, g := range b.m {
		if g.Kind == guestKindBotGroup {
			t.Errorf("guest %s left behind: %+v", id, g)
		}
	}
}

// A group guest starts in the guest directory with no profile of the owner's (no snapshot, no memory, no skills),
// and a work machine never hosts one.
func TestGroupGuestGetsNoSnapshotAndNoWorkMachine(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "mac", Machines: []string{"mac"}}
	ft.f.sessions["mac"] = nil
	s := ft.s
	ctx := context.Background()

	if _, _, err := s.StartGroupGuest(ctx, "oc_a/om_1", "answer this", "", nil); err != nil {
		t.Fatal(err)
	}
	if len(ft.f.sends) != 1 {
		t.Fatalf("starts: %d", len(ft.f.sends))
	}
	sent := ft.f.sends[0]
	if sent["cwd"] != "/home/me/.persona/guest" {
		t.Fatalf("cwd: %v", sent["cwd"])
	}
	if _, has := sent["config_dir"]; has {
		t.Fatalf("the guest started with the owner's profile: %v", sent["config_dir"])
	}
	if len(ft.f.prepared) != 0 {
		t.Fatalf("the owner's assistant directory was prepared: %v", ft.f.prepared)
	}
	// The only machine left is a work one, so the guest has nowhere to start.
	ft.d.add("work", "Work").trust = "work"
	ft.f.hello.Assistant = assistantwire.Placement{MachineID: "work", Machines: []string{"work"}}
	ft.f.sessions["work"] = nil
	if _, _, err := s.StartGroupGuest(ctx, "oc_a/om_2", "answer this", "", nil); err == nil {
		t.Fatal("a work machine hosted the guest")
	}
	if len(ft.f.sends) != 1 {
		t.Fatalf("the guest started on the work machine: %d starts", len(ft.f.sends))
	}
}
