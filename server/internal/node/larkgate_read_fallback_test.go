package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The read shapes the assistant was refused as Lark writes (#11193), as it wrote them: a timeout wrapper, output piped
// or redirected to a local file, and literal for loops that print `$?`. lark-cli's risk lookup gets no answer for
// `calendar +agenda` and `im +chat-messages-list` here (the test node's lark-cli does not exist).
var larkReadFallbackReads = []struct{ name, command string }{
	{"docs fetch piped", `timeout 60 lark-cli docs +fetch --as user --doc "https://example.larksuite.com/docx/AAA" 2>&1 | head -c 60000`},
	{"docs fetch to file", `timeout 60 lark-cli docs +fetch --as user --doc "https://example.larksuite.com/docx/AAA" > /tmp/prd.txt 2>&1; echo exit=$?; wc -c /tmp/prd.txt`},
	{"api GET loop with exit status", `for d in AAA BBB; do lark-cli api GET /open-apis/docx/v1/documents/$d/raw_content --as user > doc_$d.json 2>&1; echo "$d exit=$?"; wc -c doc_$d.json; head -c 200 doc_$d.json; echo; done`},
	{"api GET loop braced exit status", `for d in AAA BBB; do lark-cli api GET /open-apis/docx/v1/documents/$d/raw_content --as user > doc_$d.json; echo "${d} ${?}"; done`},
	{"calendar agenda piped", `timeout 30 lark-cli calendar +agenda --start 2026-11-16T00:00:00+08:00 --end 2026-11-16T23:59:59+08:00 2>&1 | head -100`},
	{"calendar agenda after cd", `cd /tmp; timeout 60 lark-cli calendar +agenda --start 2026-11-12 --end 2026-11-18 2>&1 | head -150`},
	{"chat messages loop into jq", `for c in oc_1 oc_2; do echo "=== $c"; lark-cli im +chat-messages-list --as user --chat-id $c --start 2026-11-17T14:00:00+08:00 --order desc --page-size 15 --format json 2>&1 | jq -r 'if .ok then ((.data.items // [])[] | [.create_time] | @tsv) else .error.message end'; done`},
	{"task list shortcut", `lark-cli task +get-my-tasks --as user | head -50`},
	{"generated list method", `lark-cli calendar events list --params '{"calendar_id":"primary"}' 2>&1 | head`},
}

// Writes still ask, also when lark-cli gives no risk level and inside the loops the reads above use.
// bypassCards is the card a bypassPermissions assistant still gets: sends as the user, deletion and writes of unknown
// risk are hard boundaries there; an ordinary write lark-cli reports as one (`docs +create`) runs without a card, as
// before (docs/assistant/assistant.md).
var larkReadFallbackWrites = []struct {
	name, command string
	bypassCards   int
}{
	{"message send", `lark-cli im +messages-send --as user --chat-id oc_1 --text hi`, 1},
	{"message reply", `timeout 30 lark-cli im +messages-reply --message-id om_1 --text ok 2>&1 | head`, 1},
	{"reaction", `lark-cli im +reactions-create --message-id om_1 --emoji THUMBSUP`, 1},
	{"document create", `lark-cli docs +create --title notes --markdown hi > /tmp/out.txt 2>&1; echo exit=$?`, 0},
	{"document update of unknown risk", `lark-cli docs +update --doc AAA --markdown hi`, 1},
	{"api POST", `lark-cli api POST /open-apis/im/v1/messages --data '{"receive_id":"oc_1"}' --as user`, 1},
	{"api DELETE in loop with exit status", `for m in om_1 om_2; do lark-cli api DELETE /open-apis/im/v1/messages/$m; echo "$m exit=$?"; done`, 1},
	{"send in loop with exit status", `for c in oc_1 oc_2; do lark-cli im +messages-send --chat-id $c --text hi; echo "exit=$?"; done`, 1},
	{"exit status in a path", `for d in AAA; do lark-cli api GET /open-apis/docx/v1/documents/$d$?/raw_content; done`, 1},
}

func TestLarkReadFallbackBypass(t *testing.T) {
	for _, tc := range larkReadFallbackReads {
		t.Run("read/"+tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, 0) })
	}
	for _, tc := range larkReadFallbackWrites {
		t.Run("write/"+tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.bypassCards) })
	}
}

// Outside bypassPermissions the gate lets the reads run (or leaves a loop to the conversation's own permission mode)
// without a Lark card, and still asks about every write.
func TestLarkReadFallbackDefaultMode(t *testing.T) {
	needsLarkGate(t)
	check := func(t *testing.T, command string, wantCard bool) {
		t.Helper()
		n := codexTestNode(t)
		fh := withFakeHub(t, n)
		cwd := t.TempDir()
		p, c, _ := newFakeCodex(t, n, cwd)
		p.id, c.threadID = testThread, testThread
		n.procs[testThread] = p
		n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
		n.larkCfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
		n.larkRisks = map[string]string{"\x00docs +fetch": "read", "\x00docs +create": "write"}
		n.noteModes(testThread, "default", "")
		fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
		raw, _ := json.Marshal(map[string]string{"command": command})
		out, err := n.larkGate(context.Background(), hookInput{SessionID: testThread, Cwd: cwd, ToolInput: raw}, proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		decision := ""
		if out != nil {
			decision, _ = out.(map[string]any)["hookSpecificOutput"].(map[string]any)["permissionDecision"].(string)
		}
		if cards := len(fh.interactions()); (cards > 0) != wantCard || wantCard != (decision == "deny") {
			t.Fatalf("decision=%q cards=%d; want card=%v", decision, cards, wantCard)
		}
	}
	for _, tc := range larkReadFallbackReads {
		t.Run("read/"+tc.name, func(t *testing.T) { check(t, tc.command, false) })
	}
	for _, tc := range larkReadFallbackWrites {
		t.Run("write/"+tc.name, func(t *testing.T) { check(t, tc.command, true) })
	}
}

func TestLarkCLINamedRead(t *testing.T) {
	for _, tc := range []struct {
		words string
		read  bool
	}{
		{"calendar +agenda", true}, {"docs +fetch", true}, {"docs +search", true}, {"drive +search", true},
		{"im +chat-messages-list", true}, {"im +messages-mget", true}, {"im +chat-search", true},
		{"task +get-my-tasks", true}, {"calendar events list", true}, {"im chats get", true},
		{"contact users batch_get", true}, {"im messages search", true},

		{"im +messages-send", false}, {"im +messages-reply", false}, {"im +reactions-create", false},
		{"docs +create", false}, {"docs +update", false}, {"calendar +create", false}, {"calendar +freebusy", false},
		{"drive +download", false}, {"drive +upload", false}, {"im messages create", false},
		{"calendar events delete", false}, {"im chats patch", false}, {"im +search-replace", false},
		{"+agenda", false}, {"calendar", false}, {"calendar +", false}, {"im +messages-get extra", false},
		{"calendar +agenda list", false}, {"im list", false},
	} {
		t.Run(tc.words, func(t *testing.T) {
			if got := larkCLINamedRead(strings.Fields(tc.words)); got != tc.read {
				t.Fatalf("larkCLINamedRead(%q) = %v, want %v", tc.words, got, tc.read)
			}
		})
	}
}

// What lark-cli reports wins over the name: a command it calls a write stays one.
func TestLarkCLIReportedRiskWinsOverName(t *testing.T) {
	v := classifyLark(`lark-cli calendar +agenda --start 2026-11-16`, nil, "", func([]string) string { return "write" })
	if len(v.calls) != 1 || v.calls[0].Risk != "write" || !v.cliWrite {
		t.Fatalf("verdict = %+v", v)
	}
	v = classifyLark(`lark-cli calendar +agenda --start 2026-11-16`, nil, "", func([]string) string { return "" })
	if len(v.calls) != 1 || v.calls[0].Risk != "read" || v.cliWrite {
		t.Fatalf("verdict = %+v", v)
	}
}
