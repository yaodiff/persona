package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func readCardBypassNode(t *testing.T, cfg proto.AssistantLark) (*Node, *fakeHub) {
	t.Helper()
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
	n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", exited: make(chan struct{})}
	n.persistModes(testThread, proto.ModeBypass, "")
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: boolp(false)} }
	return n, fh
}

func readScriptCommand(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "parse_rx.py")
	if err := os.WriteFile(script, []byte("import json, sys\nwith open(sys.argv[1]) as f:\n    print(len(json.load(f)))\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "cd /tmp && lark-cli im +chat-messages-list --as user --chat-id oc_example --start 2026-01-01 --end 2026-01-02 --page-all 2>/dev/null > /tmp/c10.json; python3 " + script + " /tmp/c10.json"
}

func TestLarkWholeCommandCardKeepsWriteWordingForMixedOrUnknownCalls(t *testing.T) {
	needsLarkGate(t)
	for _, tc := range []struct{ name, command string }{
		{name: "mixed read and send", command: readScriptCommand(t) + `; lark-cli im +messages-send --text hello`},
		{name: "no identified read", command: `python3 /tmp/parse_rx.py lark-cli`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := olCfg()
			cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
			n, fh := readCardBypassNode(t, cfg)
			n.larkRisks = map[string]string{"\x00im +chat-messages-list": "read"}
			_, _, _ = n.larkCheck(context.Background(), testThread, t.TempDir(), tc.command,
				proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			cards := fh.interactions()
			if len(cards) != 1 || !strings.Contains(cards[0].Summary, "上写（命令较复杂，整条显示）") {
				t.Fatalf("whole-command card summary = %q, want the existing non-read wording", cards[0].Summary)
			}
		})
	}
}

func TestLarkWholeCommandCardSaysReadNotWrite(t *testing.T) {
	needsLarkGate(t)
	cfg := olCfg()
	cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
	n, fh := readCardBypassNode(t, cfg)
	n.larkRisks = map[string]string{"\x00im +chat-messages-list": "read"}
	_, _, _ = n.larkCheck(context.Background(), testThread, t.TempDir(), readScriptCommand(t),
		proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
	cards := fh.interactions()
	if len(cards) != 1 || !strings.Contains(cards[0].Summary, "读") || strings.Contains(cards[0].Summary, "写") {
		t.Fatalf("whole-command card summary = %q, want a read-only description", cards[0].Summary)
	}
}
