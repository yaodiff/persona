package textfmt

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Clip and Cut count runes, not bytes; a cut replaces invalid bytes the way []rune does, an uncut string keeps them.
func TestClipAndCut(t *testing.T) {
	for _, c := range []struct {
		name, s   string
		n         int
		clip, cut string
	}{
		{"ascii", "abcdef", 3, "abc…", "abc"},
		{"cjk", "一二三四五", 3, "一二三…", "一二三"},
		{"exactly n runes", "一二三", 3, "一二三", "一二三"},
		{"invalid utf-8 cut", "a\xffbcd", 3, "a\uFFFDb…", "a\uFFFDb"},
		{"invalid utf-8 kept", "a\xff", 3, "a\xff", "a\xff"},
		{"n=0", "abc", 0, "…", ""},
		{"n=0 empty", "", 0, "", ""},
	} {
		if got := Clip(c.s, c.n); got != c.clip {
			t.Errorf("%s: Clip(%q, %d) = %q, want %q", c.name, c.s, c.n, got, c.clip)
		}
		if got := Cut(c.s, c.n); got != c.cut {
			t.Errorf("%s: Cut(%q, %d) = %q, want %q", c.name, c.s, c.n, got, c.cut)
		}
	}
}

func TestFormatting(t *testing.T) {
	if got := OneLine("  a\n\tb   c \n"); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
	if got := FirstLine("\n\n## 计划\n第一步"); got != "计划" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := Short("abcdef1234"); got != "abcdef12" {
		t.Errorf("Short = %q", got)
	}
	if Ago(time.Time{}) != "" || Ago(time.Now()) != "刚刚" || Ago(time.Now().Add(-5*time.Minute)) != "5 分钟前" ||
		Ago(time.Now().Add(-3*time.Hour)) != "3 小时前" || Ago(time.Now().Add(-49*time.Hour)) != "2 天前" {
		t.Error("Ago")
	}
	if StateNames[proto.StateNeedsYou] != "需要你" {
		t.Error("StateNames")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{""}, ""},
		{[]string{"", " \t\n", ""}, ""},
		{[]string{"a", "b"}, "a"},
		{[]string{"", "b", "c"}, "b"},
		{[]string{"  ", " b "}, " b "}, // as given, not trimmed
		{[]string{"\u00a0", "x"}, "x"}, // a no-break space is white space too
	} {
		if got := FirstNonEmpty(c.in...); got != c.want {
			t.Errorf("FirstNonEmpty(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgentLabel(t *testing.T) {
	for _, tc := range []struct {
		s    proto.Session
		want string
	}{
		{proto.Session{Title: "修登录", Cwd: "/code/app"}, "修登录"},
		{proto.Session{Title: "Claude Code", Cwd: "/code/app"}, "app"},
		{proto.Session{Cwd: "/code/app", SpaceName: "客户端"}, "客户端 › app"},
		{proto.Session{Title: "修登录", SpaceName: "客户端", SlotName: "修登录"}, "客户端 › 修登录"},
		{proto.Session{Title: "修登录", SpaceName: "客户端", SlotName: "左"}, "客户端 › 左「修登录」"},
	} {
		if got := AgentLabel(tc.s); got != tc.want {
			t.Errorf("AgentLabel(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// A plan's title shows in a push, the live activity and the Agent lists as plain text: no Markdown marks (#4387).
func TestPlanTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\n\n## 方案\n第一步", "方案"},
		{"**把缓存挪到 Postgres**\n细节", "把缓存挪到 Postgres"},
		{"- [ ] 第一步：改 `cache.go`", "第一步：改 cache.go"},
		{"1. 看 [设计](https://example.com/doc) 再动手", "看 设计 再动手"},
		{"> *先* 停掉 ~~旧的~~ 服务", "先 停掉 旧的 服务"},
		{"---\n\n# 计划", "计划"},
		{"直接一句话", "直接一句话"},
		{"改 `__init__.py` 和 `a**b`", "改 __init__.py 和 a**b"},
		{"重命名 my__var，算 2 ** 3", "重命名 my__var，算 2 ** 3"},
		{"", ""},
	} {
		if got := PlanTitle(tc.in); got != tc.want {
			t.Errorf("PlanTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A reply flattened to one line (node's snippet) keeps its words in a push: a table's cells joined, an HTML block's
// text without tags, comments, scripts or styles (#4462).
func TestPushTextOfAFlattenedReply(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"| 项 | 状态 | |---|---| | 构建 | 通过 |", "项 · 状态 · 构建 · 通过"},
		{"结果如下 | 模块 | 结果 | | :--- | ---: | | hub | 通过 |", "结果如下 · 模块 · 结果 · hub · 通过"},
		{"<details><summary>结果</summary>全部通过</details>", "结果 全部通过"},
		{"<details><summary>比较</summary>当 1 < 2 且 3 > 1 时通过</details>", "比较 当 1 < 2 且 3 > 1 时通过"},
		{"<details><summary>A &amp; B</summary>&lt;ok&gt;</details>", "A & B <ok>"},
		{"<script>alert(1)</script>", ""},
		{"<style>p{color:red}</style>", ""},
		{"<!--\n不该出现\n-->", ""},
		{"<div>正文<script>alert(1)</script></div>", "正文"},
		{"a | b 两个都行", "a | b 两个都行"},
		{"use `|---|---|` to mark", "use |---|---| to mark"},
		{"grep -E 'a|---|b'", "grep -E 'a|---|b'"},
		{"进度 |--| 50%", "进度 |--| 50%"},
		{"## 早报 **今天 3 个会**", "早报 今天 3 个会"},
		{"| 项 | 状态 |\n|---|---|\n| 构建 | 通过 |", "项\n状态\n构建\n通过"}, // a table on its own lines, as before
	} {
		if got := PushText(tc.in); got != tc.want {
			t.Errorf("PushText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Text that already holds what used to be the code-span marker ("\x00N\x00") comes back as it was, with no panic;
// a table's cell next to an HTML tag leaves no separator at the end of the line (review of #4475).
func TestPushTextKeepsWhatItIsGiven(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x000\x00 | a | b | |---|---|", "\x000\x00 · a · b"},
		{"`c` \x005\x00 | a | b | |---|---|", "c \x005\x00 · a · b"},
		{"`|x|` | a | b | |---|---|", "|x| · a · b"},
		{"<details><summary>表</summary>| a | b | |---|---| | 1 | 2 |</details>", "表 · a · b · 1 · 2"},
	} {
		if got := PushText(tc.in); got != tc.want {
			t.Errorf("PushText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// PlainMarkdown is a reply's text without its markup, for a push or a one-line preview.
func TestPlainMarkdown(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"## 根因\nhub 和 node 的 **会话** 不同步", "根因\nhub 和 node 的 会话 不同步"},
		{"**网页的改动**\n- **底部的「收起」**：改成 `Collapse`\n- 第二条", "网页的改动\n底部的「收起」：改成 Collapse\n第二条"},
		{"调 `POST /api/v1/sessions/{mid}/{sid}/interrupt` 停下", "调 POST /api/v1/sessions/{mid}/{sid}/interrupt 停下"},
		{"看 [PR](https://github.com/o/r/pull/1) 和 <https://example.com>", "看 PR 和 https://example.com"},
		{"> 引用 ~~删掉~~ *斜体*\n\n```go\nx := 1\n```", "引用 删掉 斜体\nx := 1"},
		{"| a | b |\n| - | - |\n| 1 | 2 |", "a\nb\n1\n2"},
		{"剪在 **加粗中间…", "剪在 加粗中间…"},
		{"剪在 `代码中间…", "剪在 代码中间…"},
		{"完成了", "完成了"},
		{"", ""},
	} {
		if got := PlainMarkdown(tc.in); got != tc.want {
			t.Errorf("PlainMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An assistant card's push says its fallback, not the card's JSON.
func TestPlainMarkdownOfACardIsItsFallback(t *testing.T) {
	text := "**Result**\n```persona-view\n{\"fallback\":\"Build complete\",\"sections\":[]}\n```\n"
	got := PlainMarkdown(text)
	if strings.Contains(got, "sections") || strings.Contains(got, "persona-view") || !strings.Contains(got, "Build complete") {
		t.Fatalf("push text %q", got)
	}
}

// PushText's handling stays out of PlainMarkdown and PlanTitle: they read these as they did.
func TestPlainMarkdownAndPlanTitleUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"<details><summary>计划</summary></details>": "",
		"改 `a|-|b`": "改 a|-|b",
		"| 项 | 状态 | |---|---| | 构建 | 通过 |": "| 项 | 状态 | |---|---| | 构建 | 通过 |",
	} {
		if got := PlanTitle(in); got != want {
			t.Errorf("PlanTitle(%q) = %q, want %q", in, got, want)
		}
		if got := PlainMarkdown(in); got != want {
			t.Errorf("PlainMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

// Text outside code gives the character a backslash escape or a character reference stands for, as a CommonMark
// renderer writes it, so a push or a one-line title shows "snake_case", not "snake\_case". Code spans and code blocks
// keep their text as it is; an escaped & starts no reference; what is not an escape or a known reference stays.
func TestPlainMarkdownResolvesEscapesAndReferences(t *testing.T) {
	for _, c := range []struct{ name, md, want string }{
		{"escaped punctuation", `Use snake\_case and 1\. step and \#tag and \*not bold\* done`, "Use snake_case and 1. step and #tag and *not bold* done"},
		{"escaped backslash", `a \\ b`, `a \ b`},
		{"escaped bracket", `a \[link\](x)`, "a [link](x)"},
		{"named references", `5 &lt; 6 &amp; 7 &gt; 3, &quot;q&quot; &copy; 2026`, `5 < 6 & 7 > 3, "q" © 2026`},
		{"numeric references", `&#39;x&#39; &#x7B;y&#x7D; &#169;`, "'x' {y} ©"},
		{"escaped ampersand", `\&amp; and \&lt;`, "&amp; and &lt;"},
		{"not an escape", `C:\Users \a`, `C:\Users \a`},
		{"not a reference", `AT&T &nosuchname; & &#; &lt`, `AT&T &nosuchname; & &#; &lt`},
		{"in a link text", `[a\_b &amp; c](https://example.com/x)`, "a_b & c"},
		{"in a heading and a list", "# a\\_b\n\n- c &amp; d\n- e\\*", "a_b\nc & d\ne*"},
		{"code span keeps its text", "`a\\_b &amp; c` and d\\_e &amp; f", "a\\_b &amp; c and d_e & f"},
		{"code block keeps its text", "```\na\\_b &amp; c\n```\n\nd\\_e", "a\\_b &amp; c\nd_e"},
	} {
		if got := PlainMarkdown(c.md); got != c.want {
			t.Errorf("%s: PlainMarkdown(%q) = %q, want %q", c.name, c.md, got, c.want)
		}
	}
	if got, want := PushText(`done: snake\_case &amp; 1\. step`), "done: snake_case & 1. step"; got != want {
		t.Errorf("PushText = %q, want %q", got, want)
	}
	if got, want := PlanTitle("## Plan for snake\\_case &amp; more\n\nbody"), "Plan for snake_case & more"; got != want {
		t.Errorf("PlanTitle = %q, want %q", got, want)
	}
}

func TestSearchMarkdownKeepsFoldedWordsWithoutHiddenHTML(t *testing.T) {
	input := "<details>\n<summary data-secret=\"attribute-token\">Result</summary>\n**foldedneedle** proof\n<script>script-token</script><style>style-token</style><!-- comment-token -->\n</details>"
	got := SearchMarkdown(input)
	for _, word := range []string{"Result", "foldedneedle", "proof"} {
		if !strings.Contains(got, word) {
			t.Fatalf("visible word %q lost in %q", word, got)
		}
	}
	for _, word := range []string{"attribute-token", "script-token", "style-token", "comment-token", "<details>"} {
		if strings.Contains(got, word) {
			t.Fatalf("hidden HTML %q included in %q", word, got)
		}
	}
	if PlainMarkdown(input) != "" {
		t.Fatal("search changed the legacy preview's HTML behavior")
	}
}
