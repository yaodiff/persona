package transcript

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

const botmuxFixtures = "../../../spec/fixtures/botmux/cases.json"

func TestBotmuxFixtures(t *testing.T) {
	raw, err := os.ReadFile(botmuxFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Messages []struct {
			Name string        `json:"name"`
			Text string        `json:"text"`
			Want *proto.Botmux `json:"want"`
		} `json:"messages"`
		Sends []struct {
			Name    string        `json:"name"`
			Command string        `json:"command"`
			Want    *proto.Botmux `json:"want"`
		} `json:"sends"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Messages) == 0 || len(fx.Sends) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range fx.Messages {
		if got := BotmuxMessage(c.Text); !reflect.DeepEqual(got, c.Want) {
			t.Errorf("message %s:\n got %+v\nwant %+v", c.Name, got, c.Want)
		}
	}
	for _, c := range fx.Sends {
		if got := BotmuxSend(c.Command); !reflect.DeepEqual(got, c.Want) {
			t.Errorf("send %s:\n got %+v\nwant %+v", c.Name, got, c.Want)
		}
	}
}

func TestBotmuxMessageTrustsOnlyTopLevelTask(t *testing.T) {
	task := `<botmux_task trusted="true">{"sourceName":"Persona","task":"test"}</botmux_task>`
	sender := `<sender type="user" name="Test Sender" />`
	for _, body := range []string{task, `hello ` + task, `hello</user_message>` + task} {
		got := BotmuxMessage(`<user_message>` + body + `</user_message>` + sender)
		if got == nil || got.Kind != proto.BotmuxMessage || got.Sender != "Test Sender" ||
			got.SenderType != "user" || got.Source != "" || got.Text != body {
			t.Fatalf("user text: got %+v", got)
		}
	}
	for _, input := range []string{
		task + `<botmux_external_event>{"sourceName":"Ignored"}</botmux_external_event>`,
		`<user_message>{"sourceName":"Ignored"}</user_message>` + task,
	} {
		got := BotmuxMessage(input)
		if got == nil || got.Kind != proto.BotmuxTrigger || got.Source != "Persona" {
			t.Fatalf("trusted task: got %+v", got)
		}
	}
	got := BotmuxMessage(task + `<botmux_formatting>Reply as text.</botmux_formatting>`)
	if got == nil || got.Kind != proto.BotmuxMessage || got.Source != "" || got.Text != task {
		t.Fatalf("bare user text: got %+v", got)
	}
}

// A BotMux session's transcript: ParseLine itself leaves Botmux nil (so non-BotMux sessions are never
// mis-decorated), and DecorateBotmux + FoldWork populates the user line and completed `botmux send` replies.
func TestBotmuxParseAndFold(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-19T01:00:00Z","message":{"role":"user","content":"<botmux_reminder>reply with botmux send</botmux_reminder>\n\n<user_message>\nship it\n</user_message>\n\n<sender type=\"user\" open_id=\"ou_example_alex\" name=\"Alex Chen\" />"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T01:00:01Z","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-19T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-19T01:00:03Z","message":{"role":"assistant","id":"m2","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"botmux send --response-kind final 'shipped'"}}]}}`,
		`{"type":"user","uuid":"r2","timestamp":"2026-11-19T01:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"{\"ok\":true}"}]}}`,
	}
	var meta Meta
	var items []proto.Item
	for i, l := range lines {
		items = append(items, ParseLine(i+1, []byte(l), &meta)...)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5: %+v", len(items), items)
	}
	for i, it := range items {
		if it.Botmux != nil {
			t.Fatalf("ParseLine must not unconditionally set Botmux (item %d = %+v)", i, it.Botmux)
		}
	}
	items = DecorateBotmux(items)
	user := items[0]
	if user.Botmux == nil || user.Botmux.Text != "ship it" || user.Botmux.Sender != "Alex Chen" {
		t.Fatalf("user botmux = %+v", user.Botmux)
	}
	if user.Text == "ship it" {
		t.Fatal("the item's text must stay the original")
	}
	if items[1].Botmux != nil {
		t.Fatalf("a plain Bash call is not a reply: %+v", items[1].Botmux)
	}
	if r := items[3].Botmux; r == nil || r.Kind != proto.BotmuxReply || r.Text != "shipped" || !r.Final {
		t.Fatalf("reply botmux = %+v", r)
	}
	folded := FoldWork(items)
	if len(folded) != 2 || folded[1].Fold == nil {
		t.Fatalf("folded = %+v", folded)
	}
	if got := folded[1].Fold.Botmux; len(got) != 1 || got[0].Text != "shipped" {
		t.Fatalf("fold botmux = %+v", got)
	}
}

func TestBotmuxTurnsFixtures(t *testing.T) {
	raw, err := os.ReadFile(botmuxFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		TurnsCases []struct {
			Name           string               `json:"name"`
			Status         int                  `json:"status"`
			Response       *BotmuxTurnsResponse `json:"response"`
			WantOwnerActor bool                 `json:"want_owner_actor"`
			Items          []proto.Item         `json:"items"`
			WantBotmux     []*proto.Botmux      `json:"want_botmux"`
		} `json:"turns_cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.TurnsCases) == 0 {
		t.Fatal("no turns_cases")
	}
	for _, tc := range fx.TurnsCases {
		t.Run(tc.Name, func(t *testing.T) {
			gotOwner := tc.Status == 200 && tc.Response != nil && tc.Response.HasCapability(BotmuxCapOwnerActor)
			if gotOwner != tc.WantOwnerActor {
				t.Fatalf("ownerActor = %v, want %v", gotOwner, tc.WantOwnerActor)
			}
			// Simulate ParseLine's wrapper fallback first, then overlay structured turns when available.
			items := append([]proto.Item(nil), tc.Items...)
			origTexts := make([]string, len(items))
			for i := range items {
				origTexts[i] = items[i].Text
			}
			markBotmux(items)
			if tc.Status == 200 && tc.Response != nil && tc.Response.OK && tc.Response.Schema == BotmuxTurnsSchema {
				items = ApplyBotmuxTurns(items, tc.Response.Turns, "session-one")
			}
			if len(items) != len(tc.WantBotmux) {
				t.Fatalf("items = %d, want %d", len(items), len(tc.WantBotmux))
			}
			for i, want := range tc.WantBotmux {
				if items[i].Text != origTexts[i] {
					t.Errorf("item[%d].Text mutated", i)
				}
				if !reflect.DeepEqual(items[i].Botmux, want) {
					t.Errorf("item[%d].Botmux:\n got %+v\nwant %+v", i, items[i].Botmux, want)
				}
				if items[i].Botmux != nil {
					b, _ := json.Marshal(items[i].Botmux)
					if s := string(b); containsAny(s, "ou_example_", "om_example_") {
						t.Errorf("item[%d].Botmux leaked Lark ID: %s", i, s)
					}
				}
			}
		})
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func TestBotmuxSendUnquotedHeredocExpansion(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string // "" means nil
	}{
		{"quoted_single_dollar", "botmux send <<'EOF'\nhello $USER $(date) `id`\nEOF", "hello $USER $(date) `id`"},
		{"quoted_double_dollar", "botmux send <<\"EOF\"\nhello $USER\nEOF", "hello $USER"},
		{"quoted_backslash_delim", "botmux send <<\\EOF\nhello $USER\nEOF", "hello $USER"},
		{"unquoted_plain", "botmux send <<EOF\nplain reply without expansion\nEOF", "plain reply without expansion"},
		{"unquoted_var", "botmux send <<EOF\nhello $USER\nEOF", ""},
		{"unquoted_braced_var", "botmux send <<EOF\nhello ${USER}\nEOF", ""},
		{"unquoted_cmd_sub", "botmux send <<EOF\nnow: $(date)\nEOF", ""},
		{"unquoted_arith", "botmux send <<EOF\nsum: $((1+2))\nEOF", ""},
		{"unquoted_backtick", "botmux send <<EOF\nnow: `date`\nEOF", ""},
		{"unquoted_escaped_dollar", "botmux send <<EOF\ncost \\$5\nEOF", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BotmuxSend(tc.cmd)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || got.Text != tc.want {
				t.Fatalf("got %+v, want text %q", got, tc.want)
			}
		})
	}
}

func TestBotmuxDecorateAndFoldSkipRunningAndErroredSends(t *testing.T) {
	items := []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "<user_message>\nhi\n</user_message>"},
		// 1) Still running (no tool_result)
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu_run", ToolInput: json.RawMessage(`{"command":"botmux send 'in flight'"}`)},
		// 2) Tool error (IsError: true)
		{ID: "a2", Seq: 3, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu_err", ToolInput: json.RawMessage(`{"command":"botmux send 'failed err'"}`)},
		{ID: "r2", Seq: 4, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu_err", IsError: true, Text: "command failed"},
		// 3) BotMux ok:false JSON error
		{ID: "a3", Seq: 5, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu_okfalse", ToolInput: json.RawMessage(`{"command":"botmux send 'failed json'"}`)},
		{ID: "r3", Seq: 6, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu_okfalse", Text: `{"ok":false,"error":"rate_limited"}`},
		// 4) Succeeded
		{ID: "a4", Seq: 7, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu_ok", ToolInput: json.RawMessage(`{"command":"botmux send --response-kind final 'delivered'"}`)},
		{ID: "r4", Seq: 8, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu_ok", Text: `{"ok":true}`},
	}

	got := DecorateBotmux(items)
	if got[1].Botmux != nil {
		t.Fatalf("running send must not be decorated: %+v", got[1].Botmux)
	}
	if got[2].Botmux != nil {
		t.Fatalf("is_error send must not be decorated: %+v", got[2].Botmux)
	}
	if got[4].Botmux != nil {
		t.Fatalf("ok:false send must not be decorated: %+v", got[4].Botmux)
	}
	if got[6].Botmux == nil || got[6].Botmux.Text != "delivered" || !got[6].Botmux.Final {
		t.Fatalf("succeeded send = %+v, want delivered/final", got[6].Botmux)
	}

	// Even if an errored item somehow had Botmux pre-populated, FoldWork must exclude it from Fold.Botmux.
	withForced := append([]proto.Item(nil), got[2:]...)
	withForced[0].Botmux = &proto.Botmux{Kind: proto.BotmuxReply, Text: "should not appear"}
	folded := FoldWork(withForced)
	if len(folded) != 1 || folded[0].Fold == nil {
		t.Fatalf("expected 1 fold item, got %+v", folded)
	}
	if len(folded[0].Fold.Botmux) != 1 || folded[0].Fold.Botmux[0].Text != "delivered" {
		t.Fatalf("Fold.Botmux must exclude failed sends, got %+v", folded[0].Fold.Botmux)
	}
}

func TestBotmuxDecorateSendReceipt(t *testing.T) {
	for _, test := range []struct {
		receipt string
		want    bool
	}{
		{`{"success":false,"error":"rate_limited"}`, false},
		{`{"ok":true,"success":false}`, false},
		{`{"ok":false,"success":true}`, false},
		{"output\n" + `{"success":false}`, false},
		{`{"success":true,"messageId":"reply"}`, true},
	} {
		t.Run(test.receipt, func(t *testing.T) {
			items := []proto.Item{
				{Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "send", ToolInput: json.RawMessage(`{"command":"botmux send hello"}`)},
				{Kind: proto.KindToolResult, ToolUseID: "send", Text: test.receipt},
			}
			if got := DecorateBotmux(items)[0].Botmux; (got != nil) != test.want {
				t.Fatalf("decoration = %+v, want reply: %v", got, test.want)
			}
		})
	}
}

func TestBotmuxDecorateSendOutcome(t *testing.T) {
	for _, status := range []string{
		"", proto.OutcomeOK, proto.OutcomeBackground, proto.OutcomeTimedOut,
		proto.OutcomeFailed, proto.OutcomeNoMatch, proto.OutcomeDenied, proto.OutcomeBlocked,
		proto.OutcomeInterrupted, "unknown",
	} {
		t.Run("status="+status, func(t *testing.T) {
			items := []proto.Item{
				{Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "send", ToolInput: json.RawMessage(`{"command":"botmux send 'hello'"}`)},
				{Kind: proto.KindToolResult, ToolUseID: "send", Outcome: &proto.ToolOutcome{Status: status}},
			}
			if got := DecorateBotmux(items)[0].Botmux; (got != nil) != (status == "" || status == proto.OutcomeOK) {
				t.Fatalf("unexpected decoration for outcome %q: %+v", status, got)
			}
		})
	}
}

func TestBotmuxDecorateSendShellStatus(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		want    bool
	}{
		{"semicolon", `/missing/botmux send 'hello'; true`, false},
		{"or", `botmux send 'hello' || true`, false},
		{"pipe", `botmux send 'hello' | cat`, false},
		{"background", `botmux send 'hello' &`, false},
		{"newline", "botmux send 'hello'\ntrue", false},
		{"masked chain", `cd /tmp && botmux send 'hello' && echo ok || true`, false},
		{"background chain", `cd /tmp && botmux send 'hello' &`, false},
		{"heredoc then newline", "botmux send <<'EOF'\nhello\nEOF\ntrue", false},
		{"and chain", "cd /tmp &&\nbotmux send 'hello' && echo ok", true},
		{"quoted operators", "botmux send 'hello; || | &\nworld' # comment", true},
		{"heredoc", "cd /tmp && botmux send <<'EOF'\nhello; || | &\nEOF", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": test.command})
			if err != nil {
				t.Fatal(err)
			}
			items := []proto.Item{
				{Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "send", ToolInput: input},
				{Kind: proto.KindToolResult, ToolUseID: "send", Text: "ok"},
			}
			if got := DecorateBotmux(items)[0].Botmux; (got != nil) != test.want {
				t.Fatalf("decoration = %+v, want present = %t", got, test.want)
			}
		})
	}
}

func TestBotmuxTurnsReplyMatchingSkipsFailuresAndHandlesMultiSend(t *testing.T) {
	userText := "<user_message>\nrun\n</user_message>"
	anchor := &BotmuxTurnAnchor{CLIID: "claude-code", InputSha256: sha256Hex(userText)}

	t.Run("skips failed, ok:false, invalid-flag, and running sends", func(t *testing.T) {
		items := []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText},
			// Failed with IsError
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: json.RawMessage(`{"command":"botmux send --card-file /tmp/a.json"}`)},
			{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", IsError: true, Text: "exit 1"},
			// Failed with ok:false
			{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t2", ToolInput: json.RawMessage(`{"command":"botmux send 'retry 1'"}`)},
			{ID: "r2", Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t2", Text: `{"ok":false,"error":"bad"}`},
			// Invalid unknown flag even with ok result
			{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t3", ToolInput: json.RawMessage(`{"command":"botmux send --unknown-flag foo"}`)},
			{ID: "r3", Seq: 7, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t3", Text: "ok"},
			// Succeeded real send (card-file)
			{ID: "a4", Seq: 8, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t4", ToolInput: json.RawMessage(`{"command":"botmux send --card-file /tmp/b.json"}`)},
			{ID: "r4", Seq: 9, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t4", Text: `{"success":true,"messageId":"om_card"}`},
		}
		turns := []BotmuxTurn{{
			TurnID:  "turn1",
			Seq:     1,
			Inbound: []BotmuxTurnInbound{{Text: "run", Via: "lark"}},
			Replies: []BotmuxTurnReply{{MessageID: "om_card", ResponseKind: "final", Text: "Confirmed card reply"}},
			Anchor:  anchor,
		}}
		got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
		for _, idx := range []int{1, 3, 5} {
			if got[idx].Botmux != nil {
				t.Fatalf("item %d must not receive reply overlay: %+v", idx, got[idx].Botmux)
			}
		}
		if got[7].Botmux == nil || got[7].Botmux.Text != "Confirmed card reply" || !got[7].Botmux.Final {
			t.Fatalf("succeeded item 7 = %+v, want Confirmed card reply", got[7].Botmux)
		}
	})

	t.Run("multi-send in one Bash command follows receipt order", func(t *testing.T) {
		items := []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText},
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: json.RawMessage(`{"command":"botmux send --content-file /tmp/1.md && botmux send --content-file /tmp/2.md"}`)},
			{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: "sent\n" +
				`{"success":true,"messageId":"om_one"}` + "\n" + `{"success":true,"messageId":"om_two"}`},
			{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t2", ToolInput: json.RawMessage(`{"command":"botmux send --response-kind final --card-file /tmp/3.json"}`)},
			{ID: "r2", Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t2", Text: `{"success":true,"messageId":"om_three"}`},
		}
		turns := []BotmuxTurn{{
			TurnID:  "turn1",
			Seq:     1,
			Inbound: []BotmuxTurnInbound{{Text: "run", Via: "lark"}},
			Replies: []BotmuxTurnReply{
				{MessageID: "om_three", ResponseKind: "final", Text: "Part three"},
				{MessageID: "om_two", ResponseKind: "progress", Text: "Part two"},
				{MessageID: "om_one", ResponseKind: "progress", Text: "Part one"},
			},
			Anchor: anchor,
		}}
		got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
		if got[1].Botmux == nil || got[1].Botmux.Text != "Part one\n\nPart two" {
			t.Fatalf("multi-send item 1 = %+v", got[1].Botmux)
		}
		if got[3].Botmux == nil || got[3].Botmux.Text != "Part three" || !got[3].Botmux.Final {
			t.Fatalf("item 3 = %+v", got[3].Botmux)
		}
	})

	t.Run("missing receipt keeps wrapper fallback", func(t *testing.T) {
		items := []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText},
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: json.RawMessage(`{"command":"botmux send 'wrapper one'"}`)},
			{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: "ok"},
		}
		turns := []BotmuxTurn{{
			TurnID:  "turn1",
			Seq:     1,
			Inbound: []BotmuxTurnInbound{{Text: "run", Via: "lark"}},
			Replies: []BotmuxTurnReply{
				{MessageID: "om_other", ResponseKind: "final", Text: "Structured reply"},
			},
			Anchor: anchor,
		}}
		got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
		if got[1].Botmux == nil || got[1].Botmux.Text != "wrapper one" || got[1].Botmux.Final {
			t.Fatalf("missing receipt must keep wrapper fallback, got %+v", got[1].Botmux)
		}
	})

	t.Run("omitted reply never shifts later replies even with equal counts", func(t *testing.T) {
		items := []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText},
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: json.RawMessage(`{"command":"botmux send 'first fallback'"}`)},
			{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: `{"success":true,"messageId":"om_missing"}`},
			{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t2", ToolInput: json.RawMessage(`{"command":"botmux send --card-file /tmp/second.json"}`)},
			{ID: "r2", Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t2", Text: `{"success":true,"messageId":"om_second"}`},
		}
		turns := []BotmuxTurn{{
			TurnID: "turn1",
			Replies: []BotmuxTurnReply{
				{MessageID: "om_second", ResponseKind: "final", Text: "Second card"},
			},
			Anchor: anchor,
		}}
		for _, extra := range []bool{false, true} {
			if extra {
				turns[0].Replies = append(turns[0].Replies, BotmuxTurnReply{
					MessageID: "om_unseen", ResponseKind: "progress", Text: "Unseen reply",
				})
			}
			got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
			if got[1].Botmux == nil || got[1].Botmux.Text != "first fallback" || got[1].Botmux.Final {
				t.Fatalf("extra=%t: first send must retain its fallback, got %+v", extra, got[1].Botmux)
			}
			if got[3].Botmux == nil || got[3].Botmux.Text != "Second card" || !got[3].Botmux.Final {
				t.Fatalf("extra=%t: second send must receive its final reply, got %+v", extra, got[3].Botmux)
			}
		}
	})
}

func TestBotmuxTriggerUnknownSourceIsEmpty(t *testing.T) {
	userText := "opaque trigger text"
	items := []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText}}
	turns := []BotmuxTurn{{
		TurnID:  "t1",
		Seq:     1,
		Inbound: []BotmuxTurnInbound{{Text: "run cron", Via: "trigger"}},
		Anchor:  &BotmuxTurnAnchor{CLIID: "claude-code", InputSha256: sha256Hex(userText)},
	}}
	got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
	if got[0].Botmux == nil || got[0].Botmux.Kind != proto.BotmuxTrigger {
		t.Fatalf("got %+v, want trigger", got[0].Botmux)
	}
	if got[0].Botmux.Source != "" {
		t.Fatalf("unknown trigger source must stay empty, got %q", got[0].Botmux.Source)
	}
}

func TestBotmuxInputSha256MatchesTruncatedLongMessage(t *testing.T) {
	longRaw := "<user_message>\n" + strings.Repeat("abcdefghij", pageTextLen/5) + "\n</user_message>"
	if len(longRaw) <= pageTextLen {
		t.Fatalf("test message too short: %d <= %d", len(longRaw), pageTextLen)
	}
	item, cut := light(proto.Item{ID: "u_long#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: longRaw}, false)
	if !cut || item.Text == longRaw {
		t.Fatalf("expected light() to clip long message")
	}
	turns := []BotmuxTurn{{
		TurnID:  "t_long",
		Seq:     1,
		Inbound: []BotmuxTurnInbound{{Text: "full structured text", Sender: &BotmuxTurnSender{Type: "user", Name: "Alex"}, Via: "lark"}},
		Anchor:  &BotmuxTurnAnchor{CLIID: "claude-code", InputSha256: sha256Hex(longRaw)},
	}}
	got := ApplyBotmuxTurns(DecorateBotmux([]proto.Item{item}), turns, "session-one")
	if got[0].Botmux == nil || got[0].Botmux.Text != "full structured text" || got[0].Botmux.Sender != "Alex" {
		t.Fatalf("clipped user item failed to match inputSha256: %+v", got[0].Botmux)
	}
}

func TestBotmuxSendShellParametersAndANSIQuotes(t *testing.T) {
	for _, parameter := range []string{"$?", "$*", "$@", "$#", "$$", "$-", "$!"} {
		if got := BotmuxSend("botmux send " + parameter); got != nil {
			t.Errorf("unquoted %s guessed a reply: %+v", parameter, got)
		}
		if got := BotmuxSend("botmux send '" + parameter + "'"); got == nil || got.Text != parameter {
			t.Errorf("quoted %s lost its literal value: %+v", parameter, got)
		}
	}
	for _, quoted := range []string{`$'\x41'`, `$'\101'`, `$'\u0041'`, `$'\cA'`, `$'\q'`} {
		if got := BotmuxSend("botmux send " + quoted); got != nil {
			t.Errorf("unsupported %s guessed a reply: %+v", quoted, got)
		}
	}
	if got := BotmuxSend(`botmux send $'line\nit\'s\tready\\now'`); got == nil || got.Text != "line\nit's\tready\\now" {
		t.Fatalf("ANSI-C escapes decoded incorrectly: %+v", got)
	}
}

func TestBotmuxSendShellGlobs(t *testing.T) {
	for _, pattern := range []string{"*", "pre?", "file[ab]", `[a"b"]`} {
		if got := BotmuxSend("botmux send " + pattern); got != nil {
			t.Errorf("unquoted %s guessed a reply: %+v", pattern, got)
		}
		if got := BotmuxSend("botmux send '" + pattern + "'"); got == nil || got.Text != pattern {
			t.Errorf("quoted %s lost its literal value: %+v", pattern, got)
		}
	}
	for _, test := range []struct{ arg, want string }{
		{`"*?"`, "*?"},
		{`\*\?\[ab]`, "*?[ab]"},
		{`array[`, "array["},
		{`[ end]`, "[ end]"},
	} {
		if got := BotmuxSend("botmux send " + test.arg); got == nil || got.Text != test.want {
			t.Errorf("arg %s decoded as %+v, want %q", test.arg, got, test.want)
		}
	}
}

func TestBotmuxInputHashRequiresCompatibleAnchor(t *testing.T) {
	user := func(id, text string) proto.Item {
		return proto.Item{ID: id, Role: "user", Kind: proto.KindText, Text: text}
	}
	makeTurn := func(session, entry, text string) BotmuxTurn {
		return BotmuxTurn{
			Anchor:  &BotmuxTurnAnchor{CLISessionID: session, CLIEntryID: entry, InputSha256: sha256Hex("ok")},
			Inbound: []BotmuxTurnInbound{{Text: text}},
		}
	}
	for _, test := range []struct {
		name  string
		items []proto.Item
		turns []BotmuxTurn
		want  []string // decoded text per item, "" for none
	}{
		{"another session", []proto.Item{user("entry#0", "ok"), user("earlier", "other")},
			[]BotmuxTurn{makeTurn("session-two", "", "wrong")}, []string{"", ""}},
		{"a loaded entry keeps its turn", []proto.Item{user("entry#0", "ok"), user("earlier", "ok")},
			[]BotmuxTurn{makeTurn("session-one", "earlier", "matched")}, []string{"", "matched"}},
		{"an earlier identical item without a record", []proto.Item{user("old", "ok"), user("new", "ok")},
			[]BotmuxTurn{makeTurn("session-one", "new", "matched")}, []string{"", "matched"}},
		{"an unloaded entry falls back to hash", []proto.Item{user("entry#0", "ok"), user("earlier", "other")},
			[]BotmuxTurn{makeTurn("session-one", "positional-7", "matched")}, []string{"matched", ""}},
		{"session match", []proto.Item{user("entry#0", "ok"), user("earlier", "other")},
			[]BotmuxTurn{makeTurn("session-two", "", "wrong"), makeTurn("session-one", "", "matched")}, []string{"matched", ""}},
		{"entry prefix", []proto.Item{user("entry#0", "ok"), user("earlier", "ok")},
			[]BotmuxTurn{makeTurn("session-one", "earlier", "second"), makeTurn("session-one", "entry", "matched")}, []string{"matched", "second"}},
		{"ambiguous turns", []proto.Item{user("entry#0", "ok"), user("earlier", "other")},
			[]BotmuxTurn{makeTurn("session-one", "", "first"), makeTurn("session-one", "", "second")}, []string{"", ""}},
		{"ambiguous items", []proto.Item{user("entry#0", "ok"), user("earlier", "ok")},
			[]BotmuxTurn{makeTurn("session-one", "", "matched")}, []string{"", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := ApplyBotmuxTurns(test.items, test.turns, "session-one")
			for i, want := range test.want {
				got := items[i].Botmux
				if want == "" && got != nil || want != "" && (got == nil || got.Text != want) {
					t.Fatalf("item %d matched %+v, want %q", i, got, want)
				}
			}
		})
	}
}

func TestBotmuxDecorateMediaOnlySend(t *testing.T) {
	items := []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "<user_message>\nscreenshot?\n</user_message>"},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: json.RawMessage(`{"command":"botmux send --image shot.png"}`)},
		{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: `{"success":true,"messageId":"om_image"}`},
		{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t2", ToolInput: json.RawMessage(`{"command":"botmux send --no-mention"}`)},
		{ID: "r2", Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t2", Text: "ok"},
	}
	got := DecorateBotmux(items)
	if want := (&proto.Botmux{Kind: proto.BotmuxReply, Attachments: 1}); !reflect.DeepEqual(got[1].Botmux, want) {
		t.Fatalf("media-only send = %+v, want %+v", got[1].Botmux, want)
	}
	if got[3].Botmux != nil {
		t.Fatalf("a send with neither text nor attachments was decorated: %+v", got[3].Botmux)
	}
	folded := FoldWork(got[1:])
	if len(folded) != 1 || folded[0].Fold == nil || len(folded[0].Fold.Botmux) != 1 || folded[0].Fold.Botmux[0].Attachments != 1 {
		t.Fatalf("fold lost the media-only reply: %+v", folded)
	}
}

func TestBotmuxSendCountIgnoresInlineComments(t *testing.T) {
	userText := "<user_message>\nrun\n</user_message>"
	anchor := &BotmuxTurnAnchor{CLIID: "claude-code", InputSha256: sha256Hex(userText)}
	for _, comment := range []string{"# not --dry-run", "# --help", "# --unknown", "#--dry-run; botmux send --card-file /tmp/x.json"} {
		t.Run(comment, func(t *testing.T) {
			cmd := "botmux send --card-file /tmp/reply.json " + comment
			if n := countBotmuxSendInvocations(cmd); n != 1 {
				t.Fatalf("countBotmuxSendInvocations(%q) = %d, want 1", cmd, n)
			}
			input, _ := json.Marshal(map[string]string{"command": cmd})
			items := []proto.Item{
				{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: userText},
				{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: input},
				{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: `{"success":true,"messageId":"om_card"}`},
			}
			turns := []BotmuxTurn{{
				TurnID:  "turn1",
				Seq:     1,
				Replies: []BotmuxTurnReply{{MessageID: "om_card", ResponseKind: "final", Text: "Card reply"}},
				Anchor:  anchor,
			}}
			got := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")
			if got[1].Botmux == nil || got[1].Botmux.Text != "Card reply" || !got[1].Botmux.Final {
				t.Fatalf("structured card reply = %+v", got[1].Botmux)
			}
		})
	}
}

func TestBotmuxSendCallsComments(t *testing.T) {
	for _, test := range []struct {
		cmd  string
		want [][]string
	}{
		{"botmux send --card-file a.json # not --dry-run", [][]string{{"botmux", "send", "--card-file", "a.json"}}},
		{"# setup; botmux send --dry-run\nbotmux send x", [][]string{{"botmux", "send", "x"}}},
		{"botmux send done#1 '#q' \"#d\" \\#e", [][]string{{"botmux", "send", "done#1", "#q", "#d", "#e"}}},
		{"botmux send <<'EOF' # heading next\n# Heading; --dry-run\nEOF\necho ok # done", [][]string{{"botmux", "send"}}},
	} {
		if got, ok := botmuxSendCalls(test.cmd); !ok || !reflect.DeepEqual(got, test.want) {
			t.Errorf("botmuxSendCalls(%q) = %q, %t, want %q", test.cmd, got, ok, test.want)
		}
	}
	if n := countBotmuxSendInvocations("botmux send --card-file a.json '# --dry-run'"); n != 1 {
		t.Fatalf("a quoted # is a word: count = %d", n)
	}
	if n := countBotmuxSendInvocations("botmux send --card-file a.json \\# --dry-run"); n != 0 {
		t.Fatalf("an escaped # does not start a comment: count = %d", n)
	}
}

func botmuxSendItems(command, result string) []proto.Item {
	input, _ := json.Marshal(map[string]string{"command": command})
	return []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "<user_message>\nrun\n</user_message>"},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t1", ToolInput: input},
		{ID: "r1", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t1", Text: result},
	}
}

func TestBotmuxSendTrailingShellIsStructural(t *testing.T) {
	for _, test := range []struct {
		name, command, want string
	}{
		{"quoted text in a trailing command", "botmux send 'done' && logger 'botmux send finished'", "done"},
		{"double-quoted text in a trailing command", `botmux send 'done' && echo "botmux send ok"`, "done"},
		{"comment", "botmux send 'done' # botmux send later", "done"},
		{"comment line", "botmux send 'done' &&\n# then botmux send nothing else\necho sent", "done"},
		{"here-document body of a trailing command", "botmux send 'done' && cat <<'EOF'\nbotmux send x\nEOF", "done"},
		{"second send", "botmux send 'one' && botmux send 'two'", ""},
		{"second send by path", "botmux send 'one'; /usr/local/bin/botmux send two", ""},
		{"second send with a prefix", "botmux send 'one' && LANG=C botmux send two", ""},
		{"second send in a substitution", "botmux send 'one' && echo $(botmux send two)", ""},
		{"second send in a block", "botmux send 'one' && if true; then botmux send two; fi", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := BotmuxSend(test.command)
			if test.want == "" && got != nil || test.want != "" && (got == nil || got.Text != test.want) {
				t.Fatalf("BotmuxSend(%q) = %+v, want %q", test.command, got, test.want)
			}
			if test.want == "" {
				return
			}
			if got := DecorateBotmux(botmuxSendItems(test.command, "ok"))[1].Botmux; got == nil || got.Text != test.want {
				t.Fatalf("delivered reply left undecorated: %+v", got)
			}
		})
	}
}

func TestBotmuxSendHeredocKeepsCommandBoundary(t *testing.T) {
	for _, test := range []struct {
		name, command, want string
	}{
		{"second send after the delimiter", "botmux send <<'EOF'\nhi\nEOF\nbotmux send 'two'", ""},
		{"second send after a chained command", "botmux send <<'EOF' && true\nhi\nEOF\nbotmux send 'two'", ""},
		{"second send on the command's line", "botmux send <<'EOF' && botmux send 'two'\nhi\nEOF", ""},
		{"chained command on the command's line", "botmux send <<'EOF' && logger done\nhi\nEOF", "hi"},
		{"chained command after the delimiter", "botmux send <<'EOF' &&\nhi\nEOF\necho sent", "hi"},
		{"comment after the delimiter", "botmux send <<'EOF'\nhi\nEOF\n# botmux send later", "hi"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := BotmuxSend(test.command)
			if test.want == "" && got != nil || test.want != "" && (got == nil || got.Text != test.want) {
				t.Fatalf("BotmuxSend(%q) = %+v, want %q", test.command, got, test.want)
			}
			if test.want == "" {
				return
			}
			if got := DecorateBotmux(botmuxSendItems(test.command, "ok"))[1].Botmux; got == nil || got.Text != test.want {
				t.Fatalf("delivered reply left undecorated: %+v", got)
			}
		})
	}
	// the delimiter line ends a line: what follows it starts a command of its own
	if _, _, rest, ok := shellWords("botmux send <<'EOF' && true\nhi\nEOF\nbotmux send 'two'"); !ok || rest != "&& true\nbotmux send 'two'" {
		t.Fatalf("rest = %q, %t", rest, ok)
	}
}

func TestBotmuxSendCountMatchesDecoder(t *testing.T) {
	for _, test := range []struct {
		name, command string
		count         int
		decoded       string // BotmuxSend's text, "" when it cannot read the reply
		overlay       string // the reply shown once the receipt matches a structured record
	}{
		{"cd then content file", "cd /tmp && botmux send --content-file reply.md", 1, "", "Full reply from the file"},
		{"cd then content file, status kept", "cd /tmp && botmux send --content-file reply.md && echo sent", 1, "", "Full reply from the file"},
		{"cd then text", "cd /tmp && botmux send 'hi'", 1, "hi", "Full reply from the file"},
		{"environment prefix", "LANG=C botmux send 'hi'", 1, "hi", "Full reply from the file"},
		{"cd then environment prefix", "cd /tmp && LANG=C botmux send 'hi'", 1, "hi", "Full reply from the file"},
		{"status masked", "botmux send 'hi' || true", 1, "", "Full reply from the file"},
		{"in a block", "if true; then botmux send --card-file a.json; fi", 1, "", "Full reply from the file"},
		{"in a subshell", "(cd /tmp && botmux send --content-file reply.md)", 1, "", "Full reply from the file"},
		{"quoted send is a word", "echo 'botmux send hi'", 0, "", ""},
		{"commented send", "true # botmux send hi", 0, "", ""},
		{"dry run", "cd /tmp && botmux send --dry-run 'hi'", 0, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if n := countBotmuxSendInvocations(test.command); n != test.count {
				t.Fatalf("countBotmuxSendInvocations(%q) = %d, want %d", test.command, n, test.count)
			}
			got := BotmuxSend(test.command)
			if test.decoded == "" && got != nil && botmuxSendStatusPreserved(test.command) || test.decoded != "" && (got == nil || got.Text != test.decoded) {
				t.Fatalf("BotmuxSend(%q) = %+v, want %q", test.command, got, test.decoded)
			}
			if got != nil && test.count != 1 {
				t.Fatalf("decoded a send the counter does not count: %+v", got)
			}
			items := botmuxSendItems(test.command, `{"success":true,"messageId":"om_reply"}`)
			turns := []BotmuxTurn{{
				TurnID:  "turn1",
				Seq:     1,
				Replies: []BotmuxTurnReply{{MessageID: "om_reply", ResponseKind: "final", Text: "Full reply from the file"}},
				Anchor:  &BotmuxTurnAnchor{CLIID: "claude-code", InputSha256: sha256Hex(items[0].Text)},
			}}
			reply := ApplyBotmuxTurns(DecorateBotmux(items), turns, "session-one")[1].Botmux
			if test.overlay == "" && reply != nil || test.overlay != "" && (reply == nil || reply.Text != test.overlay || !reply.Final) {
				t.Fatalf("overlay = %+v, want %q", reply, test.overlay)
			}
		})
	}
}
