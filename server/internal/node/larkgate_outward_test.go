package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestLarkCheckTagsConnectorOutwardWrites(t *testing.T) {
	for _, tc := range []struct {
		name, mode, command string
		wantOutward         bool
	}{
		{
			name:        "command gate post message",
			mode:        proto.ModeBypass,
			command:     `ex POST /conversations/oc_example/messages '{"text":"hello"}'`,
			wantOutward: true,
		},
		{
			name:        "command gate delete",
			mode:        proto.ModeBypass,
			command:     `ex DELETE /chats/oc_example/members/u1`,
			wantOutward: true,
		},
		{
			name:        "command gate confirm-required read",
			mode:        proto.ModeDefault,
			command:     `ex GET /links/open --url-query 'url=https://example.test'`,
			wantOutward: false,
		},
		{
			name:        "lark-cli send as owner",
			mode:        proto.ModeBypass,
			command:     `lark-cli im +messages-send --as user --chat-id oc_example --text hello`,
			wantOutward: true,
		},
		{
			name:        "lark-cli api post as owner",
			mode:        proto.ModeBypass,
			command:     `lark-cli api POST /open-apis/im/v1/messages --data '{"msg_type":"text"}'`,
			wantOutward: true,
		},
		{
			name:        "lark-cli send as bot in default mode",
			mode:        proto.ModeDefault,
			command:     `lark-cli im +messages-send --as bot --chat-id oc_example --text hello`,
			wantOutward: false,
		},
		{
			name:        "whole-command lark-cli write in loop",
			mode:        proto.ModeBypass,
			command:     `for c in oc_a oc_b; do lark-cli im +messages-send --chat-id $c --text hello; done`,
			wantOutward: true,
		},
		{
			name:        "whole-command lark-cli bot-only send with other command",
			mode:        proto.ModeDefault,
			command:     `lark-cli im +messages-send --as bot --chat-id oc_example --text hello; ls /tmp`,
			wantOutward: false,
		},
		{
			name:        "whole-command connector gate delete in loop",
			mode:        proto.ModeBypass,
			command:     `for i in 1 2; do ex DELETE /messages/$i; done`,
			wantOutward: true,
		},
		{
			name:        "whole-command lark read beside variable command",
			mode:        proto.ModeBypass,
			command:     `lark-cli docs +fetch --doc-id ex; $CMD POST /api/v1/items`,
			wantOutward: true,
		},
		{
			name:        "whole-command unnamed dynamic command",
			mode:        proto.ModeBypass,
			command:     `CMD=$SCRIPT_PATH; $CMD POST /api/v1/items`,
			wantOutward: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			needsLarkGateForCommand(t, tc.command)
			cfg := olCfg()
			cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
			n, fh := readCardBypassNode(t, cfg)
			n.larkRisks = map[string]string{
				"\x00docs +fetch":            "read",
				"\x00im +messages-send":      "write",
				"\x00im +chat-messages-list": "read",
			}
			n.persistModes(testThread, tc.mode, "")
			_, _, err := n.larkCheck(context.Background(), testThread, t.TempDir(), tc.command,
				proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			cards := fh.interactions()
			if len(cards) != 1 {
				t.Fatalf("cards = %d, want 1", len(cards))
			}
			if cards[0].OutwardWrite != tc.wantOutward {
				t.Fatalf("card.OutwardWrite = %v, want %v (summary=%q)", cards[0].OutwardWrite, tc.wantOutward, cards[0].Summary)
			}
		})
	}

	t.Run("whole-command read-only pipeline", func(t *testing.T) {
		needsLarkGate(t)
		cfg := olCfg()
		cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
		n, fh := readCardBypassNode(t, cfg)
		n.larkRisks = map[string]string{"\x00im +chat-messages-list": "read"}
		_, _, err := n.larkCheck(context.Background(), testThread, t.TempDir(), readScriptCommand(t),
			proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		cards := fh.interactions()
		if len(cards) != 1 || cards[0].OutwardWrite {
			t.Fatalf("cards = %+v, want 1 non-outward card", cards)
		}
	})
}

func TestRuntimeAskStripsSpoofedOutwardWrite(t *testing.T) {
	n, hub := runtimeNode(t)
	rt := dialRuntime(t, n, map[string]any{
		"runtime": "test-rt",
		"agent":   "test",
		"caps":    map[string]any{"send": true},
	})
	if err := rt.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{{"session_id": "s-spoof", "cwd": "/tmp", "status": "working"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "runtime session registered", func() bool { return n.runtimes.ownerOf("s-spoof") != nil })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res proto.Resolution
	if err := rt.peer.Call(ctx, "interaction.create", map[string]any{
		"interaction": map[string]any{
			"kind":          proto.IxPermission,
			"session_id":    "s-spoof",
			"tool_name":     "Bash",
			"summary":       "run command",
			"outward_write": true,
		},
	}, &res); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.asked) != 1 || hub.asked[0].OutwardWrite {
		t.Fatalf("hub asked = %+v, want 1 card with OutwardWrite stripped", hub.asked)
	}
}
