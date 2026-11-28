package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// commandInputCardOf is a node's command-input card for session s1 (node/agyinput.go), in the node's own words.
func commandInputCardOf(id string) proto.Interaction {
	input, _ := json.Marshal(proto.CommandInput{Command: "example-cli login", Task: "task-7",
		Prompt: "Enter the authorization code:", Output: "visit https://auth.example.com/authorize", Masked: true})
	return proto.Interaction{ID: id, SessionID: "s1", Kind: proto.IxPermission, ToolName: proto.CommandInputTool,
		ToolInput: input, Agent: "agy", AgentLabel: "Antigravity", Summary: "node words",
		Fields:    []proto.Field{{Key: proto.CommandInputField, Label: "Enter the authorization code:", Secret: true}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

// A command-input card comes only from a node's card list, in the node's shape: a hook's interaction.create or an
// extension's fleet.ask naming the tool is refused, and so is a listed card in another shape (a field that is not
// secret would keep what is typed in the history).
func TestCommandInputCardOnlyFromTheNodesList(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params, _ := json.Marshal(map[string]any{"interaction": commandInputCardOf("ci_hook")})
	if _, handled, err := ft.s.NodeRequest(ctx, "mac", "interaction.create", params); !handled || !errors.Is(err, errReservedCard) {
		t.Fatalf("interaction.create: handled %v, err %v; want it refused", handled, err)
	}
	if _, err := ft.s.askHub(ctx, commandInputCardOf("ci_ext")); !errors.Is(err, errReservedCard) {
		t.Fatalf("fleet.ask: err %v, want it refused", err)
	}
	plain := commandInputCardOf("ci_plain")
	plain.Fields[0].Secret = false
	optional := commandInputCardOf("ci_optional")
	optional.Fields[0].Optional = true
	other := commandInputCardOf("ix_other")
	listCodex(ft.s, plain, optional, other)
	if got := pendingIDs(ft.s); len(got) != 0 {
		t.Fatalf("cards in another shape were kept: %v", got)
	}
	if told := ft.toldOf(assistantwire.EventFleetInteraction); len(told) != 0 {
		t.Fatalf("the phone was told of %d forged cards", len(told))
	}
}

// The node's card reaches the apps in the owner's language: what it asks, the tool's name, the prompt as the label of
// its one secret, required field, and nothing that grants more than this one answer.
func TestCommandInputCardInTheOwnersLanguage(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	ft.f.mu.Lock()
	ft.f.hello.Locale = string(i18n.En)
	ft.f.mu.Unlock()
	ix := commandInputCardOf("ci_0123456789abcdef")
	ix.AlwaysLabel = "always"
	listCodex(ft.s, ix)
	cards := ft.s.pendingInteractions()
	if len(cards) != 1 {
		t.Fatalf("pending %+v", cards)
	}
	got := cards[0]
	if got.Summary != "A command is waiting for your input: Enter the authorization code:" || got.ToolTitle != "Command input" ||
		got.AlwaysLabel != "" || got.AlwaysHonored != nil || got.MachineID != "mac" {
		t.Fatalf("card: %+v", got)
	}
	if len(got.Fields) != 1 || got.Fields[0].Label != "Enter the authorization code:" || !got.Fields[0].Secret || got.Fields[0].Optional ||
		got.Fields[0].Placeholder != "Type what the command asks for" {
		t.Fatalf("fields: %+v", got.Fields)
	}
	if told := ft.toldOf(assistantwire.EventFleetInteraction); len(told) != 1 || strings.Contains(string(told[0]), "node words") {
		t.Fatalf("told %d cards: %s", len(told), told)
	}
}

// The line goes to the node as a Codex card's answer does, and the card ends once the node typed it.
func TestCommandInputAnswerGoesToTheNode(t *testing.T) {
	ft, n := newCardFleet(t, false)
	listCodex(ft.s, commandInputCardOf("ci_0123456789abcdef"))
	yes := true
	if err := ft.s.resolve("ci_0123456789abcdef", proto.Resolution{Allow: &yes,
		Values: map[string]string{proto.CommandInputField: "test-code-123"}}); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	got := n.answers
	n.mu.Unlock()
	if len(got) != 1 || got[0]["id"] != "ci_0123456789abcdef" || got[0]["session_id"] != "s1" {
		t.Fatalf("node asked %v", got)
	}
	r, _ := got[0]["resolution"].(map[string]any)
	if values, _ := r["values"].(map[string]any); values[proto.CommandInputField] != "test-code-123" {
		t.Fatalf("resolution %v", r)
	}
	if len(pendingIDs(ft.s)) != 0 {
		t.Fatalf("still pending after the line went in: %v", pendingIDs(ft.s))
	}
}

// A refusal reaches whoever answered in their language and the card stays answerable; a prompt that moved on ends it.
func TestCommandInputRefusals(t *testing.T) {
	yes := true
	answer := proto.Resolution{Allow: &yes, Values: map[string]string{proto.CommandInputField: "test-code-123"}}
	for code, key := range map[string]string{
		"command_input_unreachable": "command-input.unreachable",
		"command_input_refused":     "command-input.refused_plain",
		"command_input_unconfirmed": "command-input.unconfirmed",
		"command_input_line":        "command-input.line_control",
	} {
		ft, n := newCardFleet(t, false)
		listCodex(ft.s, commandInputCardOf("ci_0123456789abcdef"))
		n.mu.Lock()
		n.err = &rpc.Error{Code: code, Message: "node words"}
		n.mu.Unlock()
		err := ft.s.resolve("ci_0123456789abcdef", answer)
		var m *i18n.Message
		if !errors.As(err, &m) || m.Key != key {
			t.Fatalf("%s: err %v, want %s", code, err, key)
		}
		if pendingIDs(ft.s)["ci_0123456789abcdef"] == "" {
			t.Fatalf("%s: the card did not stay", code)
		}
	}
	ft, n := newCardFleet(t, false)
	listCodex(ft.s, commandInputCardOf("ci_0123456789abcdef"))
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.PromptChanged, Message: "moved on"}
	n.mu.Unlock()
	var re *rpc.Error
	if err := ft.s.resolve("ci_0123456789abcdef", answer); !errors.As(err, &re) || re.Code != screen.PromptChanged {
		t.Fatalf("err %v", err)
	}
	if len(pendingIDs(ft.s)) != 0 {
		t.Fatalf("a prompt that moved on stayed: %v", pendingIDs(ft.s))
	}
}

// A command waiting for input is not the Agent's own screen: a screen card for the same session stays beside it.
func TestCommandInputCardLeavesTheScreenCard(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()["scr_mac_s1"] = &pending{ix: proto.Interaction{ID: "scr_mac_s1", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen},
		local: func(context.Context, proto.Resolution) error { return nil }}
	ft.s.fleet.mu.Unlock()
	listCodex(ft.s, commandInputCardOf("ci_0123456789abcdef"))
	if got := pendingIDs(ft.s); len(got) != 2 || got["scr_mac_s1"] == "" {
		t.Fatalf("pending %v", got)
	}
}
