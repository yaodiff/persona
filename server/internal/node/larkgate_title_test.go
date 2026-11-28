package node

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A whole-command card names a service only when the command shows it (#4890): a command named by a variable, such
// as a local admin script calling its own HTTP API, is asked about as an ordinary Bash command, not as a Lark write.
func TestWholeCommandCardNamesOnlyWhatTheCommandShows(t *testing.T) {
	const admin = `ADMIN=~/bin/example-admin; $ADMIN POST /api/items/example/test /tmp/item.json | jq -c '{ok}'; ` +
		`$ADMIN GET /api/rules | jq -r '.[].id' > /tmp/ids; for id in $(cat /tmp/ids); do $ADMIN DELETE /api/rules/$id; done`
	for _, tc := range []struct {
		name, command, tool string
		lark                bool // the card says it writes as the user on a service
		write               bool // the gate classified a known write for display routing
	}{
		{"admin script through a variable", admin, "Bash", false, false},
		{"variable command name", "CMD=$SCRIPT_PATH; $CMD POST /api/v2/items/example", "Bash", false, false},
		{"lark-cli write in a loop", "for value in one; do lark-cli im +messages-send --text hello; done", "飞书", true, true},
		{"lark-cli through a variable", "L=lark-cli; $L im +messages-send --text hello; git status", "飞书", true, true},
		{"lark-cli path through a variable", "L=/usr/local/bin/lark-cli; $L im +messages-send --text hello; git status", "飞书", true, true},
		{"gate write through a variable path", "for value in one; do ex POST $route; done", "示例", true, true},
		{"gate call it cannot read", "for value in one; do ex events list; done", "示例", true, false},
		{"unknown lark-cli command", "lark-cli $CMD", "飞书", true, false},
		{"unknown lark-cli shortcut with a write suffix", "lark-cli im +invented-send", "飞书", true, false},
		{"method with a write prefix", "for value in one; do ex POSTPONE /items; done", "示例", true, false},
		{"variable gate HTTP method", "ex $METHOD /links/open", "示例", true, false},
		{"gate read beside a variable command", "ex GET /health; $ADMIN POST /api/items", "示例", true, false},
	} {
		for _, mode := range []string{"default", proto.ModeBypass} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				needsLarkGateForCommand(t, tc.command)
				n := codexTestNode(t)
				fh := withFakeHub(t, n)
				const sid = "22222222-2222-4222-8222-222222222222"
				cfg := olCfg()
				cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
				n.mu.Lock()
				n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
				n.procs[sid] = &assistantProc{id: sid, agent: "claude", exited: make(chan struct{})}
				n.mu.Unlock()
				n.persistModes(sid, mode, "")
				fh.mu.Lock()
				fh.answer = func(proto.Interaction) proto.Resolution {
					no := false
					return proto.Resolution{Allow: &no}
				}
				fh.mu.Unlock()
				decision, reason, err := n.larkCheck(context.Background(), sid, t.TempDir(), tc.command,
					proto.Interaction{SessionID: sid, ToolName: "Bash", ExpiresAt: time.Now().Add(time.Minute)})
				cards := fh.interactions()
				if err != nil || decision != "deny" || len(cards) != 1 {
					t.Fatalf("decision=%q reason=%q err=%v cards=%d, want one card, declined", decision, reason, err, len(cards))
				}
				card := cards[0]
				if card.Kind != proto.IxPermission || card.ToolName != tc.tool {
					t.Fatalf("card %q / %q, want a permission card for %q", card.Kind, card.ToolName, tc.tool)
				}
				if card.WriteOperation != tc.write {
					t.Fatalf("card write classification = %v, want %v", card.WriteOperation, tc.write)
				}
				if !strings.Contains(card.Summary, strings.TrimSpace(strings.SplitN(tc.command, ";", 2)[0])) {
					t.Fatalf("card does not show the command: %q", card.Summary)
				}
				writeSummary := strings.HasPrefix(card.Summary, "以你的身份在"+tc.tool+"上写")
				if tc.name == "unknown lark-cli shortcut with a write suffix" {
					// The existing unknown-command summary names the service action
					// specifically; it must not be mistaken for write routing metadata.
					writeSummary = strings.HasPrefix(card.Summary, "以你的身份在"+tc.tool+"上改群 / 会话")
				}
				if got := writeSummary; got != tc.lark {
					t.Fatalf("card summary %q: says it writes on a service = %v, want %v", card.Summary, got, tc.lark)
				}
				if !tc.lark && (strings.Contains(card.Summary, "飞书") || strings.Contains(reason, "飞书")) {
					t.Fatalf("a command that never names Lark is labeled Lark: summary %q, reason %q", card.Summary, reason)
				}
				if tc.lark && !strings.Contains(reason, tc.tool) {
					t.Fatalf("decline reason %q does not name %q", reason, tc.tool)
				}
			})
		}
	}
}

func TestCommandGateReadConfirmationIsNotAWriteOperation(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	const sid = "22222222-2222-4222-8222-222222222222"
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
	n.procs[sid] = &assistantProc{id: sid, agent: "claude", exited: make(chan struct{})}
	n.mu.Unlock()
	n.persistModes(sid, "default", "")
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		no := false
		return proto.Resolution{Allow: &no}
	}
	fh.mu.Unlock()
	decision, _, err := n.larkCheck(context.Background(), sid, t.TempDir(), "ex GET /links/open",
		proto.Interaction{SessionID: sid, ToolName: "Bash", ExpiresAt: time.Now().Add(time.Minute)})
	cards := fh.interactions()
	if err != nil || decision != "deny" || len(cards) != 1 {
		t.Fatalf("read confirmation: decision=%q err=%v cards=%d", decision, err, len(cards))
	}
	card := cards[0]
	if card.WriteOperation || len(card.AlwaysKeys) == 0 || !strings.HasPrefix(card.AlwaysKeys[0], "lark:GET") {
		t.Fatalf("GET confirmation was marked as a write: %+v", card)
	}
}
