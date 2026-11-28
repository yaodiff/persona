//go:build orcalive

package node

// A live check of the Orca bots (#16891) against the user's running Orca:
//
//	go test -tags orcalive -run OrcaBotsLive ./internal/node/ -v
//
// It only reads, through a read-only client: bots.roster, bots.chats, bots.items.list, worktree.ps, the chat tabs and the
// structured sessions' history, options and pending requests go out, and anything else is refused before it is sent. It shows the
// bots agentbot.roster finds, each one's Home Chat, and the open items agentbot.items reads. Then it runs the Orca
// bridge with a hub that never answers, and shows the card each item waiting on the owner becomes, and what folds under
// the bots' Home Chats. It prints names, titles and the cards' questions, never a token. Nothing is answered: the cards
// end handed back.
//
// With PERSONA_ORCA_LIVE_SEND set to a bot's name and PERSONA_ORCA_LIVE_TEXT to a harmless message, it then sends that
// one message to the bot's Home Chat (its only write) and waits up to five minutes for the bot's reply in the history.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

func liveOrcaMeta(t *testing.T) string {
	t.Helper()
	meta := os.Getenv("ORCA_LIVE_META") // TestMain points the default path away from the real Orca
	if meta == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		meta = filepath.Join(home, "Library", "Application Support", "orca", "orca-runtime.json")
	}
	if _, err := os.Stat(meta); err != nil {
		t.Skip("Orca not running")
	}
	return meta
}

func liveCall(t *testing.T, n *Node, method string, params, out any) error {
	t.Helper()
	raw, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := n.handleMethod(ctx, method, raw)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func TestOrcaBotsLive(t *testing.T) {
	meta := liveOrcaMeta(t)
	saved := orcaClient
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: meta, ReadOnly: true, Log: t.Logf} }
	t.Cleanup(func() { orcaClient = saved })
	n := &Node{}

	var roster proto.OrcaBotRoster
	if err := liveCall(t, n, "agentbot.roster", map[string]any{}, &roster); err != nil {
		t.Fatalf("agentbot.roster: %v", err)
	}
	t.Logf("Orca running: %v, %d bot(s)", roster.Running, len(roster.Bots))
	for _, b := range roster.Bots {
		t.Logf("bot %q (%s) id %s: Home Chat %q %s, folder %s, %d waiting on the owner, %d open", b.Name, b.Agent, b.BotID,
			b.HomeTitle, b.HomeSessionID, filepath.Base(b.Folder), b.NeedsYou, b.Open)
		var items proto.OrcaBotItems
		if err := liveCall(t, n, "agentbot.items", map[string]any{"bot_id": string(b.BotID)}, &items); err != nil {
			t.Errorf("agentbot.items %s: %v", b.Name, err)
			continue
		}
		for _, it := range items.Items {
			t.Logf("  item #%d %q: %s, raised in %q, question: %v", it.Number, it.Title, it.Status, it.ChatTitle, it.Question != nil)
		}
	}

	// The bridge, as a node whose engine is Orca runs it, with a hub that records the cards and never answers them.
	savedPoll := orcaBotsPoll
	orcaBotsPoll = 2 * time.Second
	t.Cleanup(func() { orcaBotsPoll = savedPoll })
	bn, hub := runtimeNode(t)
	release := make(chan struct{})
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution { <-release; return proto.Resolution{Dismiss: true} }
	hub.mu.Unlock()
	bn.runtimeHost.setOrcaBridge(true)
	t.Cleanup(func() { close(release); bn.runtimeHost.setOrcaBridge(false) })
	want := 0
	for _, b := range roster.Bots {
		want += b.NeedsYou
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		got := 0
		for _, ix := range hub.asked {
			if strings.HasPrefix(ix.ID, "orcaitem_") {
				got++
			}
		}
		hub.mu.Unlock()
		if got >= want && bn.orcaBotHomes() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond) // sleep-ok: a live wait on the user's Orca, bounded by the deadline above
	}
	if h := bn.orcaBotHomes(); h != nil {
		for _, home := range h.homes {
			t.Logf("folds under Home Chat session %s: sessions in %s", home.session, filepath.Base(home.folder))
		}
	}
	hub.mu.Lock()
	asked := append([]proto.Interaction(nil), hub.asked...)
	hub.mu.Unlock()
	cards := 0
	for _, ix := range asked {
		if !strings.HasPrefix(ix.ID, "orcaitem_") {
			continue
		}
		cards++
		t.Logf("card %s on session %s (%q), kind %s, format %s", ix.ID, ix.SessionID, ix.SessionTitle, ix.Kind, ix.Format)
		for _, q := range ix.Questions {
			var opts []string
			for _, o := range q.Options {
				opts = append(opts, o.Label)
			}
			first := strings.SplitN(q.Question, "\n", 2)[0]
			t.Logf("  question %q (multi %v, recommended %q): %s", first, q.MultiSelect, q.Recommend, strings.Join(opts, " | "))
		}
	}
	if cards < want {
		t.Errorf("%d card(s) for %d item(s) waiting on the owner", cards, want)
	}

	name, text := os.Getenv("PERSONA_ORCA_LIVE_SEND"), os.Getenv("PERSONA_ORCA_LIVE_TEXT")
	if name == "" || strings.TrimSpace(text) == "" {
		return
	}
	var bot proto.OrcaBot
	for _, b := range roster.Bots {
		if strings.EqualFold(b.Name, name) || string(b.BotID) == name {
			bot = b
		}
	}
	if bot.BotID == "" || bot.HomeSessionID == "" {
		t.Fatalf("no bot %q with an open Home Chat", name)
	}
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: meta, Log: t.Logf} } // the one write
	var before agents.BotPage
	if err := liveCall(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: string(bot.BotID), Limit: 5}, &before); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now()
	var receipt map[string]any
	req := agents.BotSendRequest{BotID: string(bot.BotID), SessionID: string(bot.HomeSessionID), Text: text,
		OperationID: orcart.OperationID(sentAt, "orcalive/"+string(bot.BotID)+"/"+text)}
	if err := liveCall(t, n, "agentbot.send", req, &receipt); err != nil {
		t.Fatalf("agentbot.send: %v", err)
	}
	t.Logf("sent to %q's Home Chat: accepted %v", bot.Name, receipt["accepted"])
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); {
		var page agents.BotPage
		if err := liveCall(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: string(bot.BotID), Limit: 5}, &page); err == nil && !page.Working {
			for _, m := range page.Messages {
				if m.Role == "assistant" && m.CreateTimeMs >= sentAt.UnixMilli() {
					for _, b := range m.Blocks {
						if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
							t.Logf("reply at %s: %s", time.UnixMilli(m.CreateTimeMs).UTC().Format(time.RFC3339), b.Text)
							return
						}
					}
				}
			}
		}
		time.Sleep(3 * time.Second) // sleep-ok: a live wait for the bot's reply, bounded by the deadline above
	}
	t.Fatal("no reply within five minutes")
}
