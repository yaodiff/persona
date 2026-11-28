package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A hook's directory is where the model's last `cd` left the shell (#2368): a health conversation's card from
// health/reports or /tmp carried no sign of the health space, and went to the lock screen and the channels in full.
// The manager places a card of one of its own headless conversations in that conversation's directory, the row its
// node lists, for every node version. An agent's card, another manager's conversation's and one with no row keep
// their own; a card from inside a health space keeps that (a mix-up can only hide more).
func TestNodeCardsArePlacedInTheConversationsDirectory(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ft := newFleetTest(t)
	const health, assistant = "/home/u/.persona/health", "/home/u/.persona/assistant"
	ft.sessions("mac",
		proto.Session{SessionID: "health-1", Cwd: health},
		proto.Session{SessionID: "main-1", Cwd: assistant},
		proto.Session{SessionID: "agent-1", Cwd: "/work/app", PaneID: "w1:p1"},
		proto.Session{SessionID: "other-1", Cwd: "/home/u/.persona/health-m-0c0c0c"}, // another manager's, left behind
	)
	for _, c := range []struct{ name, session, cwd, want string }{
		{"health, from /tmp", "health-1", "/tmp/x", health},
		{"health, from its reports", "health-1", health + "/reports", health},
		{"health, from the data directory", "health-1", "/home/u/.persona/data", health},
		{"main, from a skill's folder", "main-1", assistant + "/.claude/skills/x", assistant},
		{"main, from inside health", "main-1", health + "/x", health + "/x"},
		{"an agent", "agent-1", "/work/app/web", "/work/app/web"},
		{"another manager's", "other-1", "/tmp/x", "/tmp/x"},
		{"no row", "gone-1", "/tmp/y", "/tmp/y"},
	} {
		ix := proto.Interaction{MachineID: "mac", SessionID: c.session, Cwd: c.cwd}
		ft.s.placeCard(&ix)
		if ix.Cwd != c.want {
			t.Errorf("%s: placed in %q, want %q", c.name, ix.Cwd, c.want)
		}
	}

	// Through a node's interaction.create: the card that waits is in the health space, and neither the Lark bot's
	// "all cards" nor a channel's "all questions" (ixSpace) takes it.
	ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true, AllCards: true}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ask := func(id, session, cwd string) {
		ix := proto.Interaction{ID: id, SessionID: session, Cwd: cwd, Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
			ExpiresAt: time.Now().Add(time.Hour)}
		params, _ := json.Marshal(map[string]any{"interaction": ix})
		go func() { _, _, _ = ft.s.NodeRequest(ctx, "mac", "interaction.create", params) }()
	}
	ask("ix_health", "health-1", "/tmp/x")
	ask("ix_main", "main-1", assistant+"/.claude/skills/x")
	byID := map[string]proto.Interaction{}
	for _, ix := range ft.waitPending(2) {
		byID[ix.ID] = ix
	}
	h, m := byID["ix_health"], byID["ix_main"]
	if h.Cwd != health || ft.s.ixSpace(ctx, h) != SpaceHealth {
		t.Fatalf("the health card waits in %q (health %v)", h.Cwd, ft.s.ixSpace(ctx, h) == SpaceHealth)
	}
	if ft.s.feishuCardWanted(ctx, h) {
		t.Error("the Lark bot's all cards takes a health conversation's card")
	}
	if m.Cwd != assistant || ft.s.ixSpace(ctx, m) == SpaceHealth || !ft.s.feishuCardWanted(ctx, m) {
		t.Errorf("the main conversation's card: %q, health %v, posted %v", m.Cwd, ft.s.ixSpace(ctx, m) == SpaceHealth, ft.s.feishuCardWanted(ctx, m))
	}
}

// ixSpace puts a card in the health space when its directory is the space's or inside it; a directory that only
// starts the same is not. spaceOf, which says which space a conversation is in, stays exact: a row in health/reports is not one.
func TestHealthCardsCountTheSpacesFolders(t *testing.T) {
	ft := newFleetTest(t)
	ctx := context.Background()
	for cwd, want := range map[string]bool{
		"/home/u/.persona/health":         true,
		"/home/u/.persona/health/reports": true,
		"/home/u/.persona/health-bin":     false,
		"/tmp/health":                     false,
	} {
		if got := ft.s.ixSpace(ctx, proto.Interaction{MachineID: "mac", Cwd: cwd}) == SpaceHealth; got != want {
			t.Errorf("ixSpace(%s) == health = %v, want %v", cwd, got, want)
		}
	}
	if spaceOf(proto.Session{Cwd: "/home/u/.persona/health/reports"}) != "" {
		t.Error("spaceOf takes a folder of the health space for the space")
	}
	ft.sessions("mac", proto.Session{SessionID: "drift-1", Cwd: "/home/u/.persona/health/reports"})
	listed := false
	for _, s := range ft.lastList("mac") {
		if s.SessionID == "drift-1" {
			listed = true
			if s.Assistant || s.Space != "" {
				t.Errorf("a pane-less row in health/reports is listed as the assistant's: %+v", s)
			}
		}
	}
	if !listed {
		t.Fatal("the row is not listed")
	}
}
