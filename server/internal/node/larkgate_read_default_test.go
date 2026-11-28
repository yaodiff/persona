package node

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func gateReadDefaultNode(t *testing.T) (*Node, *fakeHub, proto.AssistantLark) {
	t.Helper()
	gate := exampleGate("http://example.test")
	gate.Rules = []proto.LarkRule{
		{Method: "GET", Path: "/links/open", Action: "confirm"},
		{Method: "*", Path: "**", Action: "confirm"},
	}
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{gate}}
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
	n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", exited: make(chan struct{})}
	n.persistModes(testThread, proto.ModeDefault, "")
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: boolp(false)} }
	return n, fh, cfg
}

func TestCommandGateReadsDoNotConfirmButWritesKeepTheirActionCard(t *testing.T) {
	for _, path := range []string{
		"/conversations/1001/messages",
		"/search",
		"/search/in-chat?feedId=1001",
		"/messages/2002",
		"/media/image-key",
		"/documents/read?token=doc-1",
	} {
		t.Run(path, func(t *testing.T) {
			n, fh, _ := gateReadDefaultNode(t)
			decision, reason, err := n.larkCheck(context.Background(), testThread, t.TempDir(), "ex GET "+path,
				proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || decision != "allow" || len(fh.interactions()) != 0 {
				t.Fatalf("decision=%q reason=%q err=%v cards=%v, want a read without a card", decision, reason, err, fh.interactions())
			}
		})
	}

	n, fh, _ := gateReadDefaultNode(t)
	decision, _, err := n.larkCheck(context.Background(), testThread, t.TempDir(), `ex POST /conversations/1001/messages '{"text":"hello"}'`,
		proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
	cards := fh.interactions()
	if err != nil || decision != "deny" || len(cards) != 1 || !strings.Contains(cards[0].Summary, "发消息") {
		t.Fatalf("write decision=%q err=%v cards=%v, want one correctly classified send card", decision, err, cards)
	}
}

func TestCommandGateGenericConfirmCatchAllIgnoresSlashSpelling(t *testing.T) {
	_, _, cfg := gateReadDefaultNode(t)
	for _, pattern := range []string{"**", "/**", "**/", "/**/"} {
		cfg.Gates[0].Rules = []proto.LarkRule{{Method: "*", Path: pattern, Action: "confirm"}}
		if got := larkDecision(cfg, larkCall{Gate: 0, Method: "GET", Path: "/messages/2002"}); got != "allow" {
			t.Fatalf("catch-all %q: got %q, want allow", pattern, got)
		}
	}
}

func TestCommandGateDeletionGETAndHEADStillConfirm(t *testing.T) {
	for _, mode := range []string{proto.ModeDefault, proto.ModeBypass} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, rules := range []struct {
				name  string
				rules []proto.LarkRule
			}{
				{name: "no matching rule"},
				{name: "generic confirm", rules: []proto.LarkRule{{Method: "*", Path: "**", Action: "confirm"}}},
			} {
				t.Run(mode+"/"+method+"/"+rules.name, func(t *testing.T) {
					n, fh, _ := gateReadDefaultNode(t)
					n.larkCfg.Gates[0].Rules = rules.rules
					n.persistModes(testThread, mode, "")
					fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: boolp(true)} }
					decision, _, err := n.larkCheck(context.Background(), testThread, t.TempDir(), "ex "+method+" /messages/om_1/recall",
						proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
					cards := fh.interactions()
					if err != nil || decision != "allow" || len(cards) != 1 || !strings.Contains(cards[0].Summary, "删除") {
						t.Fatalf("deletion decision=%q err=%v cards=%v, want one confirmed deletion", decision, err, cards)
					}
					decision, _, err = n.larkCheck(context.Background(), testThread, t.TempDir(), "ex "+method+" /messages/om_1",
						proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil || decision != "allow" || len(fh.interactions()) != 1 {
						t.Fatalf("ordinary read decision=%q err=%v cards=%v, want no additional card", decision, err, fh.interactions())
					}
				})
			}
		}
	}
}

func TestCommandGateConfirmedReadIsNotLabelledAWrite(t *testing.T) {
	n, fh, cfg := gateReadDefaultNode(t)
	call := larkCall{Gate: 0, Method: "GET", Path: "/links/open"}
	if got := larkDecision(cfg, call); got != "confirm" {
		t.Fatalf("explicit read rule = %q, want confirm", got)
	}
	if summary := larkSummary(cfg.Gates, []larkCall{call}); !strings.Contains(summary, "读操作") || strings.Contains(summary, "写操作") {
		t.Fatalf("read card summary = %q, want a read, not a write", summary)
	}
	decision, _, err := n.larkCheck(context.Background(), testThread, t.TempDir(), "ex GET /links/open",
		proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
	cards := fh.interactions()
	if err != nil || decision != "deny" || len(cards) != 1 || !strings.Contains(cards[0].Summary, "读操作") || strings.Contains(cards[0].Summary, "写操作") {
		t.Fatalf("decision=%q err=%v cards=%v, want one read card without write wording", decision, err, cards)
	}
}
