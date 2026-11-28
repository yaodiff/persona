package channel

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestValidID(t *testing.T) {
	for id, ok := range map[string]bool{"wechat": true, "example_chat": true, "slack2": true, "deadbeef": false, "1abc": false,
		"Wechat": false, "": false, "a-b": false, strings.Repeat("a", 33): false} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) = %v", id, !ok)
		}
	}
}

// The source mark: a client_msg_id's prefix is a channel only when it is one of those there are.
func TestFromClientMsgID(t *testing.T) {
	known := []string{"feishu", "wechat"}
	for id, want := range map[string]string{"wechat-message:12": "wechat", "feishu-om_1": "feishu", "3f2a9c1e-1111-4222-8333-444455556666": "",
		"telegram-9": "", "wechat": "", "": ""} {
		if got := FromClientMsgID(id, known); got != want {
			t.Errorf("FromClientMsgID(%q) = %q, want %q", id, got, want)
		}
	}
	if ClientMsgID("wechat", "message:1") != "wechat-message:1" {
		t.Fatal(ClientMsgID("wechat", "message:1"))
	}
}

func TestItemValidate(t *testing.T) {
	good := Item{ID: "examplechat", Name: "Example Chat", Kind: KindIM, Connector: "examplechat", Bind: BindQR,
		Capabilities: Capabilities{Format: FormatPlain, MaxText: 4000, Asks: AsksText}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Item){
		"reserved id":     func(i *Item) { i.ID = "feishu" },
		"uuid-like id":    func(i *Item) { i.ID = "cafebabe" },
		"no name":         func(i *Item) { i.Name = " " },
		"kind":            func(i *Item) { i.Kind = KindApp },
		"format":          func(i *Item) { i.Capabilities.Format = "html" },
		"asks":            func(i *Item) { i.Capabilities.Asks = "cards" },
		"max_text":        func(i *Item) { i.Capabilities.MaxText = 50 },
		"bind alone":      func(i *Item) { i.Connector = "" },
		"bind value":      func(i *Item) { i.Bind = BindNone },
		"reply_only+push": func(i *Item) { i.Capabilities.ReplyOnly = true; i.Capabilities.Push = true },
	}
	for what, f := range bad {
		it := good
		f(&it)
		if it.Validate() == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	replyOnly := good
	replyOnly.Capabilities.ReplyOnly = true
	if err := replyOnly.Validate(); err != nil {
		t.Fatalf("reply_only rejected: %v", err)
	}
}

// A mention channel is a host-supported contribution too: it answers in someone else's chat, and may opt into the
// main conversation through its ordinary target setting. The other kinds remain invalid for an extension (#11130).
func TestMentionItemValidate(t *testing.T) {
	it := Item{ID: "examplechat", Name: "Example Chat", Kind: KindMention,
		Capabilities: Capabilities{Format: FormatPlain, MaxText: 4000, Asks: AsksNone}}
	if err := it.Validate(); err != nil {
		t.Fatalf("a mention contribution: %v", err)
	}
	it.Kind = KindVoice
	if err := it.Validate(); err == nil {
		t.Fatal("a voice contribution was accepted as a channel plugin")
	}
}

// /agent may have no words of its own: the owner asks from the conversation, topic and gathered context instead (#11130).
func TestMentionMessageWithOnlyOriginIsARequest(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"id":"om_empty","chat":"Release chat","topic":"Release 1.3","owner":true}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Empty() {
		t.Fatal("a mention with its origin was dropped as an empty message")
	}
}

func TestMessageGroupTopicRoutingFieldsAndEmpty(t *testing.T) {
	var m Message
	raw := `{"id":"om_1","text":"hello","chat_id":"oc_group_1","chat_kind":"topic","topic_id":"omt_42","thread":"legacy_key"}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.ChatID != "oc_group_1" || m.ChatKind != ChatKindTopic || m.TopicID != "omt_42" || m.Thread != "legacy_key" {
		t.Fatalf("decoded routing fields = %+v", m)
	}
	// Routing metadata alone does not constitute message content.
	onlyRouting := Message{ID: "om_2", ChatID: "oc_group_1", ChatKind: ChatKindGroup, TopicID: "omt_42", Thread: "legacy_key"}
	if !onlyRouting.Empty() {
		t.Fatalf("message with only routing metadata should be empty: %+v", onlyRouting)
	}
}

func TestThreadModeAndIdleHoursValidation(t *testing.T) {
	for _, tc := range []struct {
		mode ThreadMode
		ok   bool
	}{
		{ThreadModeChannelDefault, true},
		{ThreadModePerGroupAndTopic, true},
		{ThreadModePerTopic, true},
		{"", false},
		{"per_group", false},
	} {
		if err := tc.mode.Check(); (err == nil) != tc.ok {
			t.Errorf("ThreadMode(%q).Check() = %v, want ok=%v", tc.mode, err, tc.ok)
		}
	}
	if got := ThreadMode("").Effective(false); got != ThreadModeChannelDefault {
		t.Fatalf("Effective empty = %q, want %q", got, ThreadModeChannelDefault)
	}
	if got := ThreadModePerTopic.Effective(false); got != ThreadModePerTopic {
		t.Fatalf("Effective per_topic = %q, want %q", got, ThreadModePerTopic)
	}
	for _, tc := range []struct {
		hours int
		ok    bool
	}{
		{0, true},
		{1, true},
		{24, true},
		{MaxThreadIdleHours, true},
		{-1, false},
		{MaxThreadIdleHours + 1, false},
	} {
		if err := CheckThreadIdleHours(tc.hours); (err == nil) != tc.ok {
			t.Errorf("CheckThreadIdleHours(%d) = %v, want ok=%v", tc.hours, err, tc.ok)
		}
	}
}

func TestTargetCheck(t *testing.T) {
	for _, tc := range []struct {
		t  Target
		ok bool
	}{{Target{Kind: TargetMain}, true}, {Target{Kind: TargetNew}, true}, {Target{Kind: TargetConversation, MachineID: "m", SessionID: "s"}, true},
		{Target{Kind: TargetConversation}, false}, {Target{Kind: TargetMain, SessionID: "s"}, false}, {Target{Kind: TargetAgent}, false}} {
		if err := tc.t.Check(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.t, err)
		}
	}
}

func TestPluginFeatures(t *testing.T) {
	ids := func(fs []Feature) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(PluginFeatures("", Capabilities{Asks: AsksText, Push: true}, nil)); got != "asks,all_asks,pushes,sync" {
		t.Fatal(got)
	}
	if got := ids(PluginFeatures("", Capabilities{Asks: AsksNone}, nil)); got != "" {
		t.Fatal(got)
	}
	if got := ids(PluginFeatures("", Capabilities{Asks: AsksText, Push: true, ReplyOnly: true}, nil)); got != "asks,all_asks" {
		t.Fatal(got)
	}
}

var t0 = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

// Check: a working conversation marks the run seen; a quiet one waits a little; a gone one is given up on.
func TestRunCheck(t *testing.T) {
	r := &Run{Started: t0}
	if s := r.Check(Look{Now: t0.Add(5 * time.Second), Exists: true}); s.Do != Wait {
		t.Fatalf("quiet at 5 s: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(25 * time.Second), Exists: true}); s.Do != Read {
		t.Fatalf("quiet at 25 s: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(time.Second), Exists: true, Working: true}); s.Do != Wait || !r.Seen {
		t.Fatalf("working: %+v %v", s, r.Seen)
	}
	if s := r.Check(Look{Now: t0.Add(2 * time.Second), Exists: true}); s.Do != Read {
		t.Fatalf("seen, then idle: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(2 * time.Second), Exists: true, Waiting: true}); s.Do != Wait {
		t.Fatalf("asking the owner: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(5 * time.Hour)}); s.Do != Finish || s.End != EndOffline {
		t.Fatalf("offline past RunMost: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(4 * time.Minute), Online: true}); s.Do != Finish || s.End != EndGone || !strings.Contains(s.Note, "不见了") {
		t.Fatalf("gone: %+v", s)
	}
	if s := r.Check(Look{Now: t0.Add(5 * time.Hour), Exists: true}); s.Do != Finish || !strings.Contains(s.Note, "先不等了") {
		t.Fatalf("too long: %+v", s)
	}
	// A held run ends with a word as well: its progress card went back with its message, so an ending without a note
	// would be a quiet one and nothing would be said anywhere.
	held := &Run{Started: t0, Held: true}
	if s := held.Check(Look{Now: t0.Add(3 * time.Hour), Exists: true}); s.Do != Finish || !strings.Contains(s.Note, "先不等了") || s.End != EndGaveUp {
		t.Fatalf("held too long: %+v", s)
	}
}

// Holding for a continuation does not move the run's original start, including when another continuation is held later.
func TestRunHoldPreservesStarted(t *testing.T) {
	r := &Run{Started: t0, Base: 5, MsgIDs: []string{"wechat-1"}, Probe: "original"}
	r.Hold(10, t0.Add(time.Hour))
	r.Hold(20, t0.Add(90*time.Minute))
	if !r.Started.Equal(t0) {
		t.Fatalf("Started = %v after repeated holds, want the original %v", r.Started, t0)
	}
}

// HeldMost bounds the whole run from its original message; holding again must not renew the two-hour window.
func TestRunHeldMostStartsAtOriginalMessage(t *testing.T) {
	r := &Run{Started: t0}
	r.Hold(10, t0.Add(time.Hour))
	r.Hold(20, t0.Add(90*time.Minute))
	step := r.Check(Look{Now: t0.Add(HeldMost + time.Second), Exists: true})
	if step.Do != Finish || step.End != EndGaveUp {
		t.Fatalf("past HeldMost from the original start: %+v, want Finish/EndGaveUp", step)
	}
}

// Each hold still gets its own read and quiet-end grace, even though the absolute held deadline stays at the original
// message. A delayed wake or late words are not cut off merely because the original turn was old.
func TestRunHoldRenewsPerHoldGrace(t *testing.T) {
	r := &Run{Started: t0}
	r.Hold(10, t0.Add(time.Hour))
	r.Hold(20, t0.Add(90*time.Minute))
	if step := r.Check(Look{Now: t0.Add(90*time.Minute + 5*time.Second), Exists: true}); step.Do != Wait {
		t.Fatalf("new hold did not get its read grace: %+v", step)
	}
	if res := r.Decide(Reading{Found: true}, Look{Now: t0.Add(90*time.Minute + 5*time.Minute)}); res != nil {
		t.Fatalf("new hold did not get its quiet-end grace: %+v", res)
	}
	if res := r.Decide(Reading{Found: true}, Look{Now: t0.Add(90*time.Minute + HeldIdleMost + time.Second)}); res == nil || !res.Quiet {
		t.Fatalf("per-hold quiet grace did not expire: %+v", res)
	}
}

// Decide: background work answers now and holds; a held run whose work ended quietly says nothing; no words yet waits.
func TestRunDecide(t *testing.T) {
	r := &Run{Started: t0, Seen: true}
	res := r.Decide(Reading{Answer: "做完了", Found: true, Last: 20}, Look{Now: t0.Add(time.Minute), Background: 2})
	if res == nil || !res.Held || !strings.Contains(res.Note, "后台还有 2 个任务") {
		t.Fatalf("background: %+v", res)
	}
	r.Hold(res.Last, t0.Add(time.Minute))
	if !r.Held || r.Base != 20 || r.MsgIDs != nil {
		t.Fatalf("held %+v", r)
	}
	if res := (&Run{Started: t0, Seen: true}).Decide(Reading{Answer: "做了一半", Found: true, Stopped: "已中断"}, Look{Now: t0.Add(time.Minute), Failed: true}); res.End != EndStopped {
		t.Fatalf("stopped: %+v", res)
	}
	if res := (&Run{Started: t0, Seen: true}).Decide(Reading{Answer: "API Error: overloaded", Found: true}, Look{Now: t0.Add(time.Minute), Failed: true}); res.End != EndError {
		t.Fatalf("failed: %+v", res)
	}
	if res := (&Run{Started: t0, Seen: true}).Decide(Reading{Answer: "写了一半", Found: true}, Look{Now: t0.Add(time.Minute), Offline: true}); res.End != EndOffline {
		t.Fatalf("offline with words: %+v", res)
	}
	if res := r.Decide(Reading{Found: true, Last: 21}, Look{Now: t0.Add(2 * time.Minute)}); res != nil {
		t.Fatalf("held, quiet, soon: %+v", res)
	}
	if res := r.Decide(Reading{Found: true, Last: 21}, Look{Now: t0.Add(15 * time.Minute)}); res == nil || !res.Quiet {
		t.Fatalf("held, quiet, later: %+v", res)
	}
	fresh := &Run{Started: t0}
	if res := fresh.Decide(Reading{Found: true}, Look{Now: t0.Add(30 * time.Second)}); res != nil {
		t.Fatalf("no words, not seen: %+v", res)
	}
	if res := fresh.Decide(Reading{Found: true}, Look{Now: t0.Add(3 * time.Minute)}); res == nil || res.Answer != "" || res.Quiet {
		t.Fatalf("no words at all: %+v", res)
	}
}

// A held run's background wait can also end badly: the turn its background work started was interrupted or cut off by a
// model API error, or its computer went offline. Its quiet ending (nothing more to say) still carries that outcome, so
// the channel is not told the work finished when it did not (#4228).
func TestRunDecideHeldQuietEndKeepsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		rd   Reading
		look Look
		want End
	}{
		{"done", Reading{Found: true, Last: 21}, Look{}, EndDone},
		{"stopped", Reading{Found: true, Last: 21, Stopped: transcript.InterruptedNotice}, Look{}, EndStopped},
		{"api error", Reading{Found: true, Last: 21, Stopped: transcript.APIErrorPrefix + "overloaded"}, Look{}, EndError},
		{"failed turn", Reading{Found: true, Last: 21}, Look{Failed: true}, EndError},
		{"offline", Reading{Found: true, Last: 21}, Look{Offline: true}, EndOffline},
	} {
		look := tc.look
		look.Now = t0.Add(HeldIdleMost + time.Minute)
		res := (&Run{Started: t0, Held: true}).Decide(tc.rd, look)
		if res == nil || !res.Quiet || res.End != tc.want {
			t.Fatalf("%s: %+v", tc.name, res)
		}
		if res.Held {
			t.Fatalf("%s: a quiet ending is final: %+v", tc.name, res)
		}
	}
	// Why the turn ended comes along, as on an end without words that is not quiet.
	res := (&Run{Started: t0, Held: true}).Decide(Reading{Found: true, Last: 21, Stopped: transcript.InterruptedNotice},
		Look{Now: t0.Add(HeldIdleMost + time.Minute)})
	if res.Stopped != transcript.InterruptedNotice {
		t.Fatalf("stopped: %+v", res)
	}
}

func TestFindAndFold(t *testing.T) {
	items := []proto.Item{{Seq: 1, Role: "user", Kind: proto.KindText, Text: "上一个"}, {Seq: 2, Role: "user", Kind: proto.KindText, Text: "第二个问题",
		ClientMsgID: "wechat-b"}, {Seq: 3, Role: "user", Kind: proto.KindText, Text: "没有 id 的消息"}}
	if i, ok := (&Run{MsgIDs: []string{"wechat-b"}}).Find(items, nil); !ok || i != 1 {
		t.Fatal(i, ok)
	}
	if i, ok := (&Run{MsgIDs: []string{"wechat-x"}, Probe: "没有 id"}).Find(items, nil); !ok || i != 2 {
		t.Fatal(i, ok)
	}
	if _, ok := (&Run{MsgIDs: []string{"wechat-x"}}).Find(items, nil); ok {
		t.Fatal("found a message that is not there")
	}
	if i, ok := (&Run{}).Find(items, nil); !ok || i != 0 {
		t.Fatal("a run naming nothing takes everything", i, ok)
	}
	a := &Run{ReplyTo: "m1", Base: 5, Started: t0, MsgIDs: []string{"x-m1"}, Probe: "p1", Ext: map[string]string{"card": "c1"}}
	b := &Run{ReplyTo: "m2", Base: 8, Started: t0.Add(time.Minute), MsgIDs: []string{"x-m2"}}
	b.Fold(a)
	if b.Base != 5 || !b.Started.Equal(t0) || len(b.MsgIDs) != 2 || b.Probe != "p1" || len(b.Folded) != 1 || b.Folded[0].Ext["card"] != "c1" {
		t.Fatalf("folded %+v", b)
	}
}

func TestAnswerIsTheLastWords(t *testing.T) {
	items := []proto.Item{
		{Role: "assistant", Kind: proto.KindText, Text: "先看看"},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
		{Role: "user", Kind: proto.KindToolResult},
		{Role: "assistant", Kind: proto.KindText, Text: "结论一"},
		{Role: "assistant", Kind: proto.KindText, Text: "结论二"},
	}
	// "先看看" is a passing line before a tool call: left out.
	if got := Answer(items); len(got) != 2 || got[0].Text != "结论一" {
		t.Fatalf("%+v", got)
	}
	if got := Answer(items[:3]); len(got) != 1 || got[0].Text != "先看看" {
		t.Fatalf("%+v", got)
	}
}

// A long report, a tool call, a short closing line: the answer is both, from the report on, in order (not only the
// words after the last call).
func TestAnswerKeepsAReportBeforeAToolCall(t *testing.T) {
	report := "今天的进展：\n\n" + strings.Repeat("第一项已经合并，第二项在等 CI，第三项需要你决定。", 4)
	items := []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "汇报一下"},
		{Role: "assistant", Kind: proto.KindText, Text: "我先查一下。"},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
		{Role: "user", Kind: proto.KindToolResult},
		{Role: "assistant", Kind: proto.KindText, Text: report},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "mcp__persona__agent_send"},
		{Role: "user", Kind: proto.KindToolResult},
		{Role: "assistant", Kind: proto.KindText, Text: "已经发给它了。"},
	}
	got := Answer(items)
	if len(got) != 2 || got[0].Text != report || got[1].Text != "已经发给它了。" {
		t.Fatalf("answer %+v, want the report then the closing line", got)
	}
}

func TestAnswerSeparatesWokenReplyFromPriorDeliveredReply(t *testing.T) {
	items := []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "Run the checks."},
		{Role: "assistant", Kind: proto.KindText, Text: "Initial complete report."},
		{Role: "system", Kind: proto.KindNotice, Wake: true, Text: "后台任务有更新"},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Read"},
		{Role: "user", Kind: proto.KindToolResult, Text: "ok"},
		{Role: "assistant", Kind: proto.KindText, Text: "Follow-up task finished."},
	}
	got := Answer(items)
	if len(got) != 1 || got[0].Text != "Follow-up task finished." {
		t.Fatalf("answer %+v, want only the woken reply after the wake notice", got)
	}
}

// A turn that ends on the conversation's own notice (an API error, "interrupted") says why it stopped; one that ends on
// words, a tool call, or a notice that is no reason (a compaction, a task reporting back) does not.
func TestStoppedIsTheClosingNotice(t *testing.T) {
	apiErr := []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "查一下"},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
		{Role: "user", Kind: proto.KindToolResult},
		{Role: "system", Kind: proto.KindNotice, Text: "API 错误：overloaded（重试 10 次后放弃）"},
		{Role: "system", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{DurationMS: 1}},
	}
	if got := Stopped(apiErr); got != "API 错误：overloaded（重试 10 次后放弃）" {
		t.Fatalf("api error: %q", got)
	}
	if got := Stopped([]proto.Item{{Role: "assistant", Kind: proto.KindThinking, Text: "嗯"}, {Role: "system", Kind: proto.KindNotice, Text: "已中断"}}); got != "已中断" {
		t.Fatalf("interrupted: %q", got)
	}
	for name, items := range map[string][]proto.Item{
		"words":     {{Role: "system", Kind: proto.KindNotice, Text: "API 错误：x"}, {Role: "assistant", Kind: proto.KindText, Text: "好了"}},
		"tool":      apiErr[:3],
		"compacted": {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: "上下文已压缩", Compact: &proto.Compact{}}},
		"task":      {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: "后台任务完成", Task: &proto.TaskNote{}}},
		"nothing":   nil,
		// Notices that end a turn's items but say nothing of why it stopped (only API errors and "interrupted" count).
		"compact summary": {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: transcript.CompactSummaryNotice}},
		"compact notice":  {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: transcript.CompactNotice}},
		"codex compacted": {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: transcript.CompactedNotice}},
		"goal":            {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: "目标已达成：修好 bug"}},
		"codex event":     {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: "Codex 事件：Foo（完整记录在原会话中）"}},
		"command output":  {{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: "Set model to opus"}},
	} {
		if got := Stopped(items); got != "" {
			t.Fatalf("%s: %q", name, got)
		}
	}
	// A compaction after the stop (an idle conversation compacted, Persona's /compact) does not hide why it stopped.
	compacted := []proto.Item{
		{Role: "system", Kind: proto.KindNotice, Text: "上下文已压缩", Compact: &proto.Compact{Trigger: "auto"}},
		{Role: "system", Kind: proto.KindNotice, Text: transcript.CompactSummaryNotice},
		{Role: "system", Kind: proto.KindNotice, Text: transcript.CompactNotice},
	}
	for name, stop := range map[string]string{"error then compact": "API 错误：x", "interrupted then compact": transcript.InterruptedNotice} {
		items := append([]proto.Item{{Role: "assistant", Kind: proto.KindToolUse}, {Role: "system", Kind: proto.KindNotice, Text: stop}}, compacted...)
		if got := Stopped(items); got != stop {
			t.Fatalf("%s: %q", name, got)
		}
	}
	// Codex's compaction has no Compact flag, only its text: it is passed over too.
	bare := []proto.Item{{Role: "system", Kind: proto.KindNotice, Text: "API 错误：x"}, {Role: "system", Kind: proto.KindNotice, Text: transcript.CompactedNotice}}
	if got := Stopped(bare); got != "API 错误：x" {
		t.Fatalf("codex compaction: %q", got)
	}
	// An earlier turn's "interrupted" is not this turn's reason: the owner's message comes between.
	earlier := []proto.Item{{Role: "system", Kind: proto.KindNotice, Text: transcript.InterruptedNotice}, {Role: "user", Kind: proto.KindText, Text: "再试一次"},
		{Role: "system", Kind: proto.KindNotice, Text: transcript.CompactSummaryNotice}}
	if got := Stopped(earlier); got != "" {
		t.Fatalf("earlier turn's stop: %q", got)
	}
	long := transcript.APIErrorPrefix + strings.Repeat("错", StoppedMost+50)
	if got := Stopped([]proto.Item{{Role: "system", Kind: proto.KindNotice, Text: long}}); len([]rune(got)) != StoppedMost+1 {
		t.Fatalf("not cut: %d runes", len([]rune(got)))
	}
	if got := StopReason(" 第一行\n  第二行 "); got != "第一行 第二行" {
		t.Fatalf("one line: %q", got)
	}
}

// Decide carries why a turn without words stopped, and drops it when there is an answer.
func TestDecideCarriesStopped(t *testing.T) {
	r := &Run{Started: t0, Seen: true}
	if res := r.Decide(Reading{Found: true, Stopped: "已中断"}, Look{Now: t0.Add(time.Minute)}); res == nil || res.Stopped != "已中断" {
		t.Fatalf("empty: %+v", res)
	}
	if res := r.Decide(Reading{Answer: "好了", Found: true, Stopped: "已中断"}, Look{Now: t0.Add(time.Minute)}); res == nil || res.Stopped != "" {
		t.Fatalf("answered: %+v", res)
	}
}

func TestSplitKeepsCodeBlocksWhole(t *testing.T) {
	long := "开头\n```go\n" + strings.Repeat("fmt.Println(1)\n", 40) + "```\n结尾"
	parts := Split(long, 200)
	if len(parts) < 2 {
		t.Fatal(len(parts))
	}
	for _, p := range parts {
		if n := len([]rune(p)); n > 200 {
			t.Fatalf("a piece of %d", n)
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Fatalf("an open fence: %q", p)
		}
	}
	if got := Split("短", 0); len(got) != 1 {
		t.Fatal(got)
	}
}

// Asked in text, answered by a reply: numbers, words, one question at a time.
func TestAskAndReadAnswer(t *testing.T) {
	ix := proto.Interaction{ID: "q", Kind: proto.IxQuestion, SessionTitle: "周报", Questions: []proto.Question{
		{Question: "发给谁？", Options: []proto.QuestionOption{{Label: "张三"}, {Label: "李四"}}},
		{Question: "哪些部分？", MultiSelect: true, Options: []proto.QuestionOption{{Label: "进展"}, {Label: "风险"}, {Label: "计划"}}}}}
	a := NewAsk("wechat", ix, t0)
	text, ok := AskText("", ix, a)
	if !ok || !strings.HasPrefix(text, "【周报】（1/2）") || !strings.Contains(text, "2. 李四") {
		t.Fatalf("%q", text)
	}
	if _, done, again := ReadAnswer("", a, "2"); done || again != "" || a.Step != 1 {
		t.Fatalf("first answer: done=%v again=%q step=%d", done, again, a.Step)
	}
	r, done, _ := ReadAnswer("", a, "1 3")
	if !done || r.Answers["发给谁？"] != "李四" || r.Answers["哪些部分？"] != "进展, 计划" {
		t.Fatalf("%+v", r)
	}
	perm := proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"make deploy"}`)}
	pa := NewAsk("wechat", perm, t0)
	if text, _ := AskText("", perm, pa); !strings.Contains(text, "make deploy") {
		t.Fatalf("%q", text)
	}
	if _, _, again := ReadAnswer("", pa, "也许"); again == "" {
		t.Fatal("a reply that is not one was taken")
	}
	if r, done, _ := ReadAnswer("", pa, "拒绝。"); !done || r.Allow == nil || *r.Allow {
		t.Fatalf("%+v", r)
	}
	plan := &Ask{Kind: proto.IxPlan}
	if r, _, _ := ReadAnswer("", plan, "先别改数据库"); r.Approve == nil || *r.Approve || r.Feedback != "先别改数据库" {
		t.Fatalf("%+v", r)
	}
	if _, ok := AskText("", proto.Interaction{Kind: proto.IxPermission, Fields: []proto.Field{{Key: "pw"}}}, &Ask{}); ok {
		t.Fatal("a card with fields is answerable in text")
	}
}

// The texts read in the user's language; a reply is understood in any of the three, whatever that language is.
func TestAskInLanguages(t *testing.T) {
	perm := proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"make deploy"}`)}
	if text, _ := AskText(i18n.En, perm, NewAsk("wechat", perm, t0)); !strings.Contains(text, "Wants your approval for “Bash”") {
		t.Fatalf("en: %q", text)
	}
	if text, _ := AskText(i18n.Ja, perm, NewAsk("wechat", perm, t0)); !strings.Contains(text, "「Bash」の承認") {
		t.Fatalf("ja: %q", text)
	}
	if text, _ := AskText("", perm, NewAsk("wechat", perm, t0)); !strings.Contains(text, "要你批准「Bash」") {
		t.Fatalf("no language: %q", text)
	}
	for text, allow := range map[string]bool{"允许": true, "Allow": true, "yes!": true, "許可": true, "はい。": true,
		"拒绝": false, "deny": false, "No": false, "拒否": false, "いいえ": false} {
		r, done, _ := ReadAnswer(i18n.En, &Ask{Kind: proto.IxPermission}, text)
		if !done || r.Allow == nil || *r.Allow != allow {
			t.Errorf("%q: done=%v %+v", text, done, r)
		}
	}
	for text, mode := range map[string]string{"1": "bypassPermissions", "自动执行": "bypassPermissions", "approve and run": "bypassPermissions",
		"自動実行": "bypassPermissions", "2": "acceptEdits", "批准": "acceptEdits", "Approve": "acceptEdits", "承認": "acceptEdits"} {
		r, _, _ := ReadAnswer(i18n.Ja, &Ask{Kind: proto.IxPlan}, text)
		if r.Approve == nil || !*r.Approve || r.Mode != mode {
			t.Errorf("%q: %+v", text, r)
		}
	}
	if _, _, again := ReadAnswer(i18n.En, &Ask{Kind: proto.IxPermission}, "maybe"); again != "Reply “allow” or “deny”." {
		t.Fatalf("%q", again)
	}
	if got := Answered(i18n.Ja, proto.Resolution{Allow: new(bool)}); got != "拒否しました。" {
		t.Fatal(got)
	}
	if got := Duration(i18n.En, 3*time.Minute+4*time.Second) + " / " + Duration(i18n.Ja, 2*time.Hour+5*time.Minute) + " / " +
		Duration("", 12*time.Second); got != "3m 4s / 2時間5分 / 12 秒" {
		t.Fatal(got)
	}
	r := &Run{Started: t0}
	if s := r.Check(Look{Now: t0.Add(5 * time.Hour), Exists: true, Lang: i18n.En}); !strings.Contains(s.Note, "Still not done after 5h 0m") {
		t.Fatal(s.Note)
	}
	if res := r.Decide(Reading{Answer: "done"}, Look{Now: t0.Add(time.Minute), Background: 1, Lang: i18n.En}); res.Note !=
		"1 background task is still running. I'll tell you when it's done." {
		t.Fatal(res.Note)
	}
	if f := PluginFeatures(i18n.Ja, Capabilities{Asks: AsksText}, nil); f[0].Title != "質問と承認" {
		t.Fatal(f[0].Title)
	}
	if f := PluginFeatures("", Capabilities{Asks: AsksText}, nil); f[0].Title != "提问和批准" {
		t.Fatal(f[0].Title)
	}
}

// An option number typed on a Chinese keyboard (full-width) or with a full stop is still that number: a plan is not
// sent back with a full-width "1" as the note, a terminal dialog is answered, a question's option becomes its label (#3413).
func TestOptionNumbersFullWidthAndPunctuated(t *testing.T) {
	for text, mode := range map[string]string{"１": "bypassPermissions", "1。": "bypassPermissions", " 1. ": "bypassPermissions",
		"２": "acceptEdits", "2！": "acceptEdits", "２。": "acceptEdits", "1、": "bypassPermissions", "１．": "bypassPermissions"} {
		r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text)
		if !done || r.Approve == nil || !*r.Approve || r.Mode != mode || r.Feedback != "" {
			t.Errorf("plan %q: %+v", text, r)
		}
	}
	for _, text := range []string{"3", "1?", "１？", "01", "10"} {
		if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text); r.Approve == nil || *r.Approve || r.Feedback != text {
			t.Errorf("plan %q is not a choice: %+v", text, r)
		}
	}
	if _, done, again := ReadAnswer("", &Ask{Kind: proto.IxScreen, Options: []string{"yes", "no"}}, "３"); done || again == "" {
		t.Error("screen: ３ of 2 options was taken")
	}
	if _, done, again := ReadAnswer("", &Ask{Kind: proto.IxPermission}, "1"); done || again == "" {
		t.Error("permission: a number was taken")
	}
	for _, text := range []string{"２", "2。", "2."} {
		r, done, again := ReadAnswer("", &Ask{Kind: proto.IxScreen, Options: []string{"yes", "no"}}, text)
		if !done || again != "" || r.Option != "no" {
			t.Errorf("screen %q: done=%v again=%q %+v", text, done, again, r)
		}
	}
	q := []proto.Question{{Question: "哪些？", MultiSelect: true, Options: []proto.QuestionOption{{Label: "进展"}, {Label: "风险"}, {Label: "计划"}}}}
	r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: q}, "１，３")
	if !done || r.Answers["哪些？"] != "进展, 计划" {
		t.Errorf("question: %+v", r)
	}
	if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: q}, "1，3 。"); r.Answers["哪些？"] != "进展, 计划" {
		t.Errorf("question with a stray full stop: %+v", r)
	}
	one := []proto.Question{{Question: "发给谁？", Options: []proto.QuestionOption{{Label: "张三"}, {Label: "李四"}}}}
	if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: one}, "２。"); r.Answers["发给谁？"] != "李四" {
		t.Errorf("question: %+v", r)
	}
	for _, text := range []string{"３", "1 2"} {
		if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: one}, text); r.Answers["发给谁？"] != text {
			t.Errorf("question %q is the answer itself: %+v", text, r)
		}
	}
}

// A phone keyboard types a list number in a shape: a circled digit, a paired bracket around it (half- or full-width,
// CJK corner or lenticular), or a right parenthesis after it, 1) in half- or full-width. They answer a plan, a
// terminal dialog and a question like a plain digit (#4327).
func TestOptionNumberPhoneShapes(t *testing.T) {
	for text, mode := range map[string]string{
		"①": "bypassPermissions", "②": "acceptEdits", "①。": "bypassPermissions", "②、": "acceptEdits",
		"（1）": "bypassPermissions", "(1)": "bypassPermissions", "【1】": "bypassPermissions", "[1]": "bypassPermissions",
		"「1」": "bypassPermissions", "『1』": "bypassPermissions", "1)": "bypassPermissions", "1）": "bypassPermissions",
		"（2）": "acceptEdits", "(2)": "acceptEdits", "【2】": "acceptEdits", "[2]": "acceptEdits",
		"「2」": "acceptEdits", "『2』": "acceptEdits", "2)": "acceptEdits", "2）": "acceptEdits",
	} {
		r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text)
		if !done || r.Approve == nil || !*r.Approve || r.Mode != mode || r.Feedback != "" {
			t.Errorf("plan %q: %+v", text, r)
		}
	}
	// A question mark asks what the number is; a shape past the choices or around a non-number is the note itself.
	for _, text := range []string{"①？", "①?", "（1）?", "(1)？", "1)？", "1)?", "01)", "③", "⑴", "⓪", "（01）", "（0）", "（ 1 ）", "（1）删掉"} {
		if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text); r.Approve == nil || *r.Approve || r.Feedback != text {
			t.Errorf("plan %q is not a choice: %+v", text, r)
		}
	}
	// number() itself: the circled range's both ends and the shapes it does not read.
	for text, n := range map[string]int{"①": 1, "⑳": 20, "②": 2, "（12）": 12, "[20]": 20, "3)": 3} {
		if got, ok := number(text); !ok || got != n {
			t.Errorf("number(%q) = %d, %v; want %d", text, got, ok, n)
		}
	}
	for _, text := range []string{"㉑", "⑴", "⓪", "01)", "（01）", "（1）删掉", "1)？"} {
		if n, ok := number(text); ok {
			t.Errorf("number(%q) = %d, true; want false", text, n)
		}
	}
	screen := &Ask{Kind: proto.IxScreen, Options: []string{"a", "b", "c"}}
	for text, want := range map[string]string{"①": "a", "③": "c", "（3）": "c", "【1】": "a", "[2]": "b",
		"「2」": "b", "『3』": "c", "1)": "a", "2）": "b"} {
		if r, done, again := ReadAnswer("", screen, text); !done || again != "" || r.Option != want {
			t.Errorf("screen %q: done=%v again=%q %+v", text, done, again, r)
		}
	}
	for _, text := range []string{"④", "（4）", "①？", "（01）"} {
		if _, done, again := ReadAnswer("", screen, text); done || again == "" {
			t.Errorf("screen %q was taken", text)
		}
	}
	if _, done, again := ReadAnswer("", &Ask{Kind: proto.IxPermission}, "①"); done || again == "" {
		t.Error("permission: a circled number was taken")
	}
	multi := []proto.Question{{Question: "哪些？", MultiSelect: true,
		Options: []proto.QuestionOption{{Label: "进展"}, {Label: "风险"}, {Label: "计划"}}}}
	for text, want := range map[string]string{"①，③": "进展, 计划", "(2)": "风险", "【3】": "计划", "「2」": "风险"} {
		if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: multi}, text); !done || r.Answers["哪些？"] != want {
			t.Errorf("question %q: %+v", text, r)
		}
	}
	one := []proto.Question{{Question: "发给谁？", Options: []proto.QuestionOption{{Label: "张三"}, {Label: "李四"}}}}
	if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: one}, "①"); r.Answers["发给谁？"] != "张三" {
		t.Errorf("question ①: %+v", r)
	}
	if r, _, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: one}, "①？"); r.Answers["发给谁？"] != "①？" {
		t.Errorf("question ①？ is the answer itself: %+v", r)
	}
}

// Other everyday ways to say it answer a permission or a plan: "approve" in Chinese, approved/okay/yeah/yep, Chinese okay/fine/alright, and a
// comma, enumeration comma or a wave left at the end. The match is exact: a negation never becomes an approval just
// for holding a positive word (#4375).
func TestAnswerApprovalWordVariants(t *testing.T) {
	allow := []string{"批准", "Approved", "APPROVED", "okay", "OKAY", "yeah", "yep", "好了", "好吧", "行吧",
		"批准，", "好的、", "ok~", "行～", "Yeah.", "批准．", "okay,"}
	for _, text := range allow {
		if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPermission}, text); !done || r.Allow == nil || !*r.Allow {
			t.Errorf("permission %q allowed: done=%v %+v", text, done, r)
		}
	}
	deny := []string{"不批准", "不行", "不要", "不允许", "不同意", "不批准，", "不行~", "拒绝～"}
	for _, text := range deny {
		if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPermission}, text); !done || r.Allow == nil || *r.Allow {
			t.Errorf("permission %q denied: done=%v %+v", text, done, r)
		}
	}
	// Neither word alone, nor a question, is taken: the user is asked again.
	for _, text := range []string{"准", "吧", "不好", "批准吗", "批准吧？", "不批准吧？", "批准了再说", "也许批准"} {
		if _, done, again := ReadAnswer("", &Ask{Kind: proto.IxPermission}, text); done || again == "" {
			t.Errorf("permission %q was taken: done=%v", text, done)
		}
	}
	// A plan: the run words bypass; "approve" in Chinese alone still answers option 2; the negation sends it back with no note.
	for text, mode := range map[string]string{
		"批准并运行": "bypassPermissions", "批准运行": "bypassPermissions", "批准并运行，": "bypassPermissions",
		"批准运行~": "bypassPermissions", "批准并自动执行": "bypassPermissions",
		"批准": "acceptEdits", "批准。": "acceptEdits", "行吧": "acceptEdits",
	} {
		if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text); !done || r.Approve == nil || !*r.Approve || r.Mode != mode {
			t.Errorf("plan %q: done=%v %+v", text, done, r)
		}
	}
	for _, text := range []string{"不批准", "不批准。", "不行～"} {
		if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxPlan}, text); !done || r.Approve == nil || *r.Approve || r.Feedback != "" {
			t.Errorf("plan %q sent back without a note: %+v", text, r)
		}
	}
	// A terminal dialog answers with numbers, so the new words and tails are not choices there.
	if _, done, again := ReadAnswer("", &Ask{Kind: proto.IxScreen, Options: []string{"yes", "no"}}, "批准，"); done || again == "" {
		t.Error("screen: a word was taken as an option")
	}
	// A free-answer question is untouched by the word lists: a wave or comma stays part of the answer.
	q := []proto.Question{{Question: "备注？"}}
	if r, done, _ := ReadAnswer("", &Ask{Kind: proto.IxQuestion, Questions: q}, "ok~"); !done || r.Answers["备注？"] != "ok~" {
		t.Errorf("question: %+v", r)
	}
}

// A recalled message comes out of a run that answers others too (#5248); a run answering only it is not changed.
func TestRunUnfold(t *testing.T) {
	r := &Run{ReplyTo: "m2", MsgIDs: []string{"x-m0", "x-m1", "x-m2"}, Ext: map[string]string{"reaction": "r2", "card": "c2"},
		Folded: []Folded{{ReplyTo: "m0", Ext: map[string]string{"reaction": "r0"}}, {ReplyTo: "m1", Ext: map[string]string{"reaction": "r1"}}}}
	if got, ok := r.Unfold("m0", "x-m0"); !ok || got.ReplyTo != "m0" || got.Ext["reaction"] != "r0" ||
		len(r.Folded) != 1 || r.Folded[0].ReplyTo != "m1" || !slices.Equal(r.MsgIDs, []string{"x-m1", "x-m2"}) || r.ReplyTo != "m2" {
		t.Fatalf("a folded one: %+v, %v", r, got)
	}
	// Its own message: the latest folded one takes its place, with its reaction; the progress card stays.
	if got, ok := r.Unfold("m2", "x-m2"); !ok || got.ReplyTo != "m2" || got.Ext["reaction"] != "r2" ||
		r.ReplyTo != "m1" || r.Ext["reaction"] != "r1" || r.Ext["card"] != "c2" || len(r.Folded) != 0 || !slices.Equal(r.MsgIDs, []string{"x-m1"}) {
		t.Fatalf("its own: %+v, %v", r, got)
	}
	if _, ok := r.Unfold("m1", "x-m1"); ok || r.ReplyTo != "m1" {
		t.Fatalf("the only message was unfolded: %+v", r)
	}
	if _, ok := (&Run{ReplyTo: "a", Folded: []Folded{{ReplyTo: "b"}}}).Unfold("z", "x-z"); ok {
		t.Fatal("a message it does not answer")
	}
}
