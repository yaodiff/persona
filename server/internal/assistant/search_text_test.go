package assistant

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The index keeps what spec/fixtures/chat-search says of each item (its "index"), the rule the web's search in chat
// reads too (#6281, #6282).
func TestSearchIndexTextFollowsTheSharedRule(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/fixtures/chat-search/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name  string     `json:"name"`
			Item  proto.Item `json:"item"`
			Index *string    `json:"index"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 10 {
		t.Fatalf("only %d cases read", len(fixture.Cases))
	}
	for _, c := range fixture.Cases {
		got, ok := searchIndexText(c.Item)
		switch {
		case c.Index == nil && ok:
			t.Errorf("%s: indexed %q, want nothing", c.Name, got)
		case c.Index != nil && (!ok || got != *c.Index):
			t.Errorf("%s: indexed %q (%v), want %q", c.Name, got, ok, *c.Index)
		}
	}
}

func TestSearchIndexTextLeavesOutWhatIsNotTheUsersWords(t *testing.T) {
	notice := "🔔 任务「README 去掉扩展列表」做完了（公司电脑）\n（这是 Persona 的任务通知，不是用户发的消息。）" // i18n-ignore: an injected template's shape
	for name, it := range map[string]proto.Item{
		"another session's message": {Role: "user", Kind: proto.KindText, Text: "from the other one", From: "worker-1"},
		"only view errors":          {Role: "user", Kind: proto.KindText, Text: "<persona-view-errors>\n- bad\n</persona-view-errors>"},
		"a thought":                 {Role: "assistant", Kind: proto.KindThinking, Text: "thinking it over"},
		"a tool's output":           {Role: "user", Kind: proto.KindToolResult, Text: "ok"},
	} {
		if got, ok := searchIndexText(it); ok {
			t.Errorf("%s: indexed %q", name, got)
		}
	}
	// read off the text when the hub has not marked it, as the hub's withInjected does; a message the app sent is the user's
	if prompt := (proto.Item{Role: "user", Kind: proto.KindText, Text: notice}); searchInjected(prompt) {
		if _, ok := searchIndexText(prompt); ok {
			t.Error("an unmarked injected notice was indexed")
		}
	}
	if _, ok := searchIndexText(proto.Item{Role: "user", Kind: proto.KindText, Text: notice, ClientMsgID: "c1"}); !ok {
		t.Error("the user's own message was left out because it reads like a notice")
	}
}

func TestSearchWordsFoldCaseAndWidth(t *testing.T) {
	if got := searchWords("  ＷＥＥＫＬＹ Report  weekly 日历 "); !reflect.DeepEqual(got, []string{"weekly", "report", "日历"}) {
		t.Fatalf("words %q", got)
	}
	if got := searchWords("   "); got != nil {
		t.Fatalf("blank query gave %q", got)
	}
	if got := searchWords("a b c d e f g h i j"); len(got) != searchMaxWords {
		t.Fatalf("%d words kept, want %d", len(got), searchMaxWords)
	}
	if !searchHolds(searchFold("周末去青石巷 BlueLane 看看"), searchWords("bluelane 青石")) || searchHolds("abc", nil) {
		t.Fatal("searchHolds")
	}
}

func TestSearchSnippetMarksMatchesInUTF16(t *testing.T) {
	snippet, ranges := searchSnippet("我想去青石巷 BlueLane 看看", searchWords("bluelane"))
	if snippet != "我想去青石巷 BlueLane 看看" || !reflect.DeepEqual(ranges, [][2]int{{7, 15}}) {
		t.Fatalf("snippet %q ranges %v", snippet, ranges)
	}
	// a character outside the BMP counts two before the match, as the apps' strings do
	if _, r := searchSnippet("😀 Ｋｅｙ here", searchWords("key")); !reflect.DeepEqual(r, [][2]int{{3, 6}}) {
		t.Fatalf("ranges %v", r)
	}
	// cut around the first match, on one line, with ellipses; every match marked
	long := ""
	for range 30 {
		long += "前文"
	}
	snippet, ranges = searchSnippet(long+"\n\n铜钥匙 挂在门后。\n"+long+long, searchWords("铜钥匙"))
	if []rune(snippet)[0] != '…' || []rune(snippet)[len([]rune(snippet))-1] != '…' {
		t.Fatalf("not cut: %q", snippet)
	}
	// "…", then the 40 characters before the match: 38 of the first line and its two line breaks as one space
	if !reflect.DeepEqual(ranges, [][2]int{{40, 43}}) {
		t.Fatalf("ranges %v in %q", ranges, snippet)
	}
}

// A reply is indexed as the chat shows it (#7072): its words without Markdown's markup, so a snippet shows no marker
// and words the markup split are found together; what is not markup stays.
func TestSearchPlainKeepsAReplysWordsWithoutTheMarkup(t *testing.T) {
	for _, c := range []struct{ md, want string }{
		{"2. **照片同步脚本**（Mac Studio）", "照片同步脚本（Mac Studio）"},
		{"# 标题 \n> > 引用的 *话*", "标题 \n引用的 话"},
		{"- [ ] 待办：~~旧的~~ 新的\n  * 嵌套 _斜体_ 项", "待办：旧的 新的\n嵌套 斜体 项"},
		{"见 [文档](https://example.com/a_b) 和 ![图](x.png) 和 [引用][1]", "见 文档 和 图 和 引用"},
		{"用 `**args` 和 `` a ` b `` 调用", "用 **args 和 a ` b 调用"},
		{"```go\nx := 1 // **keep**\n```\n后面 **粗**", "x := 1 // **keep**\n后面 粗"},
		{"| a | b |\n|---|---|\n| 1 | 2 |\n\n***", "a b\n1 2"},
		{"2 * 3 * 4，snake_case_name，\\*星\\*，未闭合的 ` 反引号", "2 * 3 * 4，snake_case_name，*星*，未闭合的 ` 反引号"},
		{"__粗__ 和 _斜_ 和 __a__ __b__", "粗 和 斜 和 a b"},
		{"plain words only", "plain words only"},
	} {
		if got := searchPlain(c.md); got != c.want {
			t.Errorf("searchPlain(%q) = %q, want %q", c.md, got, c.want)
		}
	}
	// the user's words stay as typed (neither app renders them as Markdown); a reply's split words are found together
	user, _ := searchIndexText(proto.Item{Role: "user", Kind: proto.KindText, Text: "**不是** Markdown"})
	reply, _ := searchIndexText(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "**照片**同步脚本"})
	if user != "**不是** Markdown" || !searchHolds(searchFold(reply), searchWords("照片同步")) {
		t.Fatalf("user %q reply %q", user, reply)
	}
	if snippet, ranges := searchSnippet(reply, searchWords("照片同步")); snippet != "照片同步脚本" || !reflect.DeepEqual(ranges, [][2]int{{0, 4}}) {
		t.Fatalf("snippet %q ranges %v", snippet, ranges)
	}
}

// No `**` of a reply is left in its index (#7195): bold that holds code, wraps a numbered title, nests, spans two
// lines or has no partner loses its stars; `2 ** 3` and `a**b` read as expressions and stay, as does code.
func TestSearchPlainLeavesNoStrongMarkers(t *testing.T) {
	for _, c := range []struct{ md, want string }{
		{"计划写好了。\n**步骤一 - `make test`**", "计划写好了。\n步骤一 - make test"},
		{"结论：**先跑 `go vet` 再提交**，然后合并", "结论：先跑 go vet 再提交，然后合并"},
		{"上一段\n\n**1. 准备 `config.json`**\n内容", "上一段\n\n1. 准备 config.json\n内容"},
		{"**`--dry-run`** 只打印", "--dry-run 只打印"},
		{"**第一行\n第二行**", "第一行\n第二行"},
		{"只有开头的 **没有结尾", "只有开头的 没有结尾"},
		{"**a **b** c**，***粗斜***，***没收", "a b c，粗斜，没收"},
		{"见 [`config.json`](https://example.com/c) 和 *用 `x` 时*", "见 config.json 和 用 x 时"},
		{"2 ** 3 = 8，a**b\nx**2 和 **注意**", "2 ** 3 = 8，a**b\nx**2 和 注意"},
		{"用 `**kwargs` 和 `a\\*b`", "用 **kwargs 和 a*b"},
		{"`\U000F0000` 在代码里", "\U000F0000 在代码里"},
	} {
		if got := searchPlain(c.md); got != c.want {
			t.Errorf("searchPlain(%q) = %q, want %q", c.md, got, c.want)
		}
	}
	// a match's range is counted on the text without the stars
	reply, _ := searchIndexText(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "结论：**先跑 `go vet` 再提交**"})
	if snippet, ranges := searchSnippet(reply, searchWords("go vet 再提交")); snippet != "结论：先跑 go vet 再提交" || !reflect.DeepEqual(ranges, [][2]int{{6, 8}, {9, 12}, {13, 16}}) {
		t.Fatalf("snippet %q ranges %v", snippet, ranges)
	}
}
