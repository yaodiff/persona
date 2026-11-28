package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

// homeBotCaller is a node for a bot bound by its id: describe and history answer from the bot's open Home Chat
// (home), and a send must name it.
type homeBotCaller struct {
	mu    sync.Mutex
	home  string
	sends []map[string]any
	reads []map[string]any
}

func (c *homeBotCaller) CallBot(_ context.Context, machine, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if machine != "machine-b" {
		return &rpc.Error{Code: "not_found", Message: "wrong machine"}
	}
	switch method {
	case "agentbot.describe", "agentbot.history":
		c.reads = append(c.reads, p)
		if p["bot_id"] != "bot-1" || p["session_id"] != nil {
			return &rpc.Error{Code: "invalid", Message: "a bot binding reads by its bot only"}
		}
		return botTestDecode(out, BotPage{CanSend: true, RankTimeMs: 10, Messages: []Message{}, SessionID: c.home})
	case "agentbot.send":
		c.sends = append(c.sends, p)
		if p["bot_id"] != "bot-1" || p["session_id"] != c.home {
			return &rpc.Error{Code: "conflict", Message: "bot started a new Home Chat"}
		}
		return botTestDecode(out, map[string]any{"accepted": true})
	}
	return &rpc.Error{Code: "invalid", Message: "unexpected " + method}
}

// A found bot's registration is bound to the bot, its public id derived from the computer and the bot (the same every
// time, another for another computer or bot) and never naming an Orca session.
func TestDiscoveredBotRegistrationIsStableAndBoundToTheBot(t *testing.T) {
	a := DiscoveredBot("machine-b", "bot-1", "Helper")
	if a != DiscoveredBot("machine-b", "bot-1", "Helper") {
		t.Fatal("the registration of the same bot changed")
	}
	if a.ID == DiscoveredBot("machine-c", "bot-1", "Helper").ID || a.ID == DiscoveredBot("machine-b", "bot-2", "Helper").ID {
		t.Fatal("two bots share a public id")
	}
	if !strings.HasPrefix(a.ID, "orca-") || a.Provider != "orca" || a.BotID != "bot-1" || a.SessionID != "" || a.Title != "Helper" ||
		a.MachineID != "machine-b" || strings.Contains(a.ID, "bot-1") {
		t.Fatalf("registration %+v", a)
	}
	if _, err := NewBotRegistry(nil, nil).Source(a); err != nil {
		t.Fatalf("a found bot is not a valid registration: %v", err)
	}
	if DiscoveredBot("machine-b", "bot-1", "  ").Title != "bot-1" {
		t.Fatal("a bot without a name has no title")
	}
}

// A registration binds exactly one of a session and a bot.
func TestBotRegistrationBindsASessionOrABot(t *testing.T) {
	r := NewBotRegistry(nil, nil)
	for _, reg := range []BotRegistration{
		{ID: "both", Provider: "orca", Title: "Both", MachineID: "m", SessionID: "s", BotID: "b"},
		{ID: "neither", Provider: "orca", Title: "Neither", MachineID: "m"},
	} {
		if _, err := r.Source(reg); err == nil {
			t.Errorf("%s accepted", reg.ID)
		}
	}
	for _, reg := range []BotRegistration{
		{ID: "session", Provider: "orca", Title: "Session", MachineID: "m", SessionID: "s"},
		{ID: "bot", Provider: "orca", Title: "Bot", MachineID: "m", BotID: "b"},
	} {
		if _, err := r.Source(reg); err != nil {
			t.Errorf("%s refused: %v", reg.ID, err)
		}
	}
}

// A send to a bot-bound endpoint goes to the Home Chat it resolved and the operation is fixed to that chat: the same
// clientId after the bot started a new Home Chat conflicts before anything is sent, while a new message follows the bot
// to the new chat.
func TestBotBoundEndpointPinsSendsToTheResolvedHomeChat(t *testing.T) {
	caller := &homeBotCaller{home: "chat-a"}
	svc := NewService(&memKV{m: map[string][]byte{}}, nil, nil)
	src, err := NewBotRegistry(&memKV{m: map[string][]byte{}}, caller).Source(DiscoveredBot("machine-b", "bot-1", "Helper"))
	if err != nil {
		t.Fatal(err)
	}
	svc.Add(src)
	id := src.Name() + ":" + BotSessionKey
	ctx := context.Background()
	const clientID = "0b0e9c8e-6f1a-4c3e-9d2b-4a5f6e7d8c9b"
	if _, err := svc.Send(ctx, id, clientID, "Reply with one word.", "test"); err != nil {
		t.Fatal(err)
	}
	if len(caller.sends) != 1 || caller.sends[0]["session_id"] != "chat-a" || caller.sends[0]["bot_id"] != "bot-1" {
		t.Fatalf("sends %v", caller.sends)
	}
	caller.mu.Lock()
	caller.home = "chat-b" // the bot started a new Home Chat
	caller.mu.Unlock()
	src.ops.mu.Lock()
	src.ops.loaded = false // as after a manager restart: the book is read back from the store
	src.ops.mu.Unlock()
	_, err = src.Send(ctx, BotSessionKey, clientID, "Reply with one word.")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		t.Fatalf("a retry after the reset: %v", err)
	}
	if len(caller.sends) != 1 {
		t.Fatalf("the retry after the reset reached the node (%d sends)", len(caller.sends))
	}
	if _, err := svc.Send(ctx, id, "", "A new message.", "test"); err != nil {
		t.Fatal(err)
	}
	if len(caller.sends) != 2 || caller.sends[1]["session_id"] != "chat-b" {
		t.Fatalf("a new message did not follow the bot to its new Home Chat: %v", caller.sends)
	}
	page, err := svc.Messages(ctx, id, "", 5)
	if err != nil || page.Session == nil || page.Session.Title != "Helper" {
		t.Fatalf("messages %+v %v", page, err)
	}
}
