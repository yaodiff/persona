package node

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// The rule the manager makes from a ledger row (connector.ChatRuleFor) and serves to this node (connector.LarkRules)
// is read by the node's own gate: the text messages of that conversation go without a card, and everything else asks:
// another conversation, a reply (in either path), images and files, a health conversation's write. A hand-written
// rule before it still wins.
func TestServedChatRuleIsReadByTheNodesGate(t *testing.T) {
	user, err := connector.ChatRuleFor("lark", "POST /conversations/7123/messages", json.RawMessage(`{"conversation":"7123"}`))
	if err != nil {
		t.Fatal(err)
	}
	served := connector.LarkRules([]connector.Rule{user.Rule}, nil) // what assistantLark hands over after the hand-written ones

	health := filepath.Join(t.TempDir(), ".persona", "health")
	if err := os.MkdirAll(health, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		rules []proto.LarkRule
		cwd   string
		cmd   string
		allow bool // let through; false: refused, after a card the user declined
		cards int
	}{
		{"the chat of the rule", served, "", `ex POST /conversations/7123/messages '{"text":"hi"}'`, true, 0},
		{"a trailing slash is the same route", served, "", `ex POST /conversations/7123/messages/ '{"text":"hi"}'`, true, 0},
		{"through the API's prefix", served, "", `ex POST /api/v1/conversations/7123/messages '{"text":"hi"}'`, true, 0},
		{"another conversation", served, "", `ex POST /conversations/8456/messages '{"text":"hi"}'`, false, 1},
		{"a conversation whose id starts the same", served, "", `ex POST /conversations/71234/messages '{"text":"hi"}'`, false, 1},
		{"a reply", served, "", `ex POST /messages/om_1/replies '{"text":"hi"}'`, false, 1},
		{"a reply under the conversation", served, "", `ex POST /conversations/7123/messages/om_1/replies '{"text":"hi"}'`, false, 1},
		{"a file of the chat", served, "", `ex POST /conversations/7123/files '{"name":"x"}'`, false, 1},
		{"a path that climbs out of the conversation", served, "", `ex POST /conversations/7123/messages/../../8456/messages '{"text":"hi"}'`, false, 1},
		{"an encoded id", served, "", `ex POST /conversations/71%32%33/messages '{"text":"hi"}'`, false, 1},
		{"a health conversation's write", served, health, `ex POST /conversations/7123/messages '{"text":"hi"}'`, false, 1},
		// The hook's directory is where the model's last `cd` left it (#2368): a folder inside the space is the space.
		{"a health conversation's write from its reports", served, filepath.Join(health, "reports"), `ex POST /conversations/7123/messages '{"text":"hi"}'`, false, 1},
		{"a hand-written deny first", append([]proto.LarkRule{{Method: "POST", Path: "/conversations/7123/messages", Action: "deny"}}, served...), "",
			`ex POST /conversations/7123/messages '{"text":"hi"}'`, false, 0},
		{"a hand-written confirm first", append([]proto.LarkRule{{Method: "POST", Path: "/conversations/*/messages", Action: "confirm"}}, served...), "",
			`ex POST /conversations/7123/messages '{"text":"hi"}'`, false, 1},
		{"no rule at all", nil, "", `ex POST /conversations/7123/messages '{"text":"hi"}'`, false, 1},
	} {
		n := codexTestNode(t)
		fh := withFakeHub(t, n)
		fh.mu.Lock()
		fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
		fh.mu.Unlock()
		n.mu.Lock()
		n.larkCfg, n.larkCfgAt = olCfg(c.rules...), time.Now().Add(time.Hour)
		n.mu.Unlock()
		cwd := c.cwd
		if cwd == "" {
			cwd = t.TempDir()
		}
		d, _, err := n.larkCheck(context.Background(), "s1", cwd, c.cmd, proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil || (d == "allow") != c.allow || len(fh.interactions()) != c.cards {
			t.Errorf("%s: %q, %v, %d cards; want allow=%v, %d cards", c.name, d, err, len(fh.interactions()), c.allow, c.cards)
		}
	}
}

// The gate proxy, which every request of the gated command passes however it was made, holds to the served rule the
// same way: the conversation of the rule goes through unasked, written in any way that means the same path (the proxy
// reads the path decoded once, and forwards that), and anything else, another conversation, a path that climbs out
// of it, a reply, a file, another method, needs the phone's yes first and so is refused here.
func TestGateProxyHoldsToTheServedChatRule(t *testing.T) {
	user, err := connector.ChatRuleFor("lark", "POST /conversations/F1/messages", json.RawMessage(`{"conversation":"F1"}`))
	if err != nil {
		t.Fatal(err)
	}
	api := newFakeAPI(t)
	n, _ := gateNode(t, api.URL, connector.LarkRules([]connector.Rule{user.Rule}, nil)...)
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, c := range []struct {
		name, method, path string
		want               int
		reached            string // the path the API saw, when the call went on
	}{
		{"the conversation of the rule", "POST", "/api/v1/conversations/F1/messages", http.StatusOK, "/api/v1/conversations/F1/messages"},
		{"the same path, an id character encoded", "POST", "/api/v1/conversations/F%31/messages", http.StatusOK, "/api/v1/conversations/F1/messages"},
		{"another conversation", "POST", "/api/v1/conversations/F2/messages", http.StatusForbidden, ""},
		{"a longer id", "POST", "/api/v1/conversations/F11/messages", http.StatusForbidden, ""},
		{"a path that climbs out of it, encoded", "POST", "/api/v1/conversations/F1%2F..%2FF2/messages", http.StatusBadRequest, ""},
		{"a path that climbs out of it, in two segments", "POST", "/api/v1/conversations/F1/messages/../../F2/messages", http.StatusBadRequest, ""},
		{"a reply", "POST", "/api/v1/messages/M1/replies", http.StatusForbidden, ""},
		{"a reply under the conversation", "POST", "/api/v1/conversations/F1/messages/M1/replies", http.StatusForbidden, ""},
		{"a file", "POST", "/api/v1/conversations/F1/files", http.StatusForbidden, ""},
		{"another method", "PUT", "/api/v1/conversations/F1/messages", http.StatusForbidden, ""},
	} {
		before := len(api.requests())
		code, _ := send(t, c.method, base+c.path, `{"text":"hi"}`)
		seen := api.requests()[before:]
		if code != c.want || (c.reached == "") != (len(seen) == 0) || len(seen) > 0 && seen[0].Path != c.reached {
			t.Errorf("%s: %d, the API saw %+v; want %d reaching %q", c.name, code, seen, c.want, c.reached)
		}
	}
}
