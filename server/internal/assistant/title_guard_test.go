package assistant

import "testing"

// The observed 2026-11-27 pollution and same-shape fragments must all be caught.
func TestTitleLineIsThinkingPositive(t *testing.T) {
	cases := []string{
		// Verbatim polluted todo titles from the incident.
		"We need answer title",
		"We need need output",
		"We need answer only",
		// The full untruncated reasoning sentence observed from a compatible thinking endpoint.
		"we need answer only one line chinese title <=20 chars no explanation",
		// Other opener families with enough meta-reasoning signal.
		"The user asks for the answer title only",
		"The user wants the output in JSON format",
		"Let's output the title now",
		"I need answer title",
		"I need reply output",
		"Okay, we need answer now",
		"OK, output the title and format it as json",
		"First, we need output json",
		"We need output JSON",
		"We need the answer format and the title",
		// Chinese chain-of-thought fragments.
		"我们需要输出标题",
		"我需要给出答案和标题",
		"让我们思考一下输出格式",
		"首先分析问题，再给出答案",
	}
	for _, in := range cases {
		if !titleLineIsThinking(in) {
			t.Errorf("titleLineIsThinking(%q) = false, want true", in)
		}
	}
}

// Ordinary titles, including opener-shaped and meta-word-shaped ones, must survive. This corpus is the
// false-kill gate: when the guard learns a new pattern, add the near-miss real title here too.
func TestTitleLineIsThinkingNegative(t *testing.T) {
	cases := []string{
		// Plain product titles.
		"Fix login crash on iOS 18",
		"Review PR 17191 before merge",
		"Buy milk and eggs after work",
		"Q4 launch plan",
		"2026 年度预算表",
		"修复搜索框和顶栏重叠",
		"周五发布灰度安排",
		"给妈妈打电话确认周末安排",
		"ログイン画面のクラッシュを修正する",
		"週次レポートを金曜までに作成",
		// Opener-shaped real titles.
		"We need new icons for the app",
		"We need server metrics dashboard",
		"We need design output by Friday",            // "output" as a real deliverable
		"We need answers from the legal team",        // plural, real-world answers
		"We need the final answer from the client",   // article breaks the telegraphic shape
		"We need a reply by Thursday",                // article
		"We need title deed scan for the house sale", // property title
		"I should answer Sarah's email tonight",
		"I should prepare the release notes",
		"I need to reply to the invite before noon",
		"I need to book the dentist",
		"I need reply to Sarah about the contract", // concrete recipient after the meta verb
		"Let's discuss the launch plan",
		"The user manual needs a rewrite",
		"First, cut the release branch",
		"Okay, book the team offsite venue",
		// Meta words without a monologue opener.
		"Answer client questions about pricing",
		"Reply to design review comments",
		"Update the API response schema",
		"Add JSON export to settings",
		"Format the hard drive for the backup disk",
		"标题栏在深色模式下颜色不对",
		"回复客户关于价格的邮件",
		"需要购买出差机票",
		"我们需要新版应用图标",
		"我们需要修复 CI 上的编译错误",
		"我应该回复客户邮件",
		"我需要准备明天的周报",
		"首先修复编译错误再跑测试",
		"首先用一行代码修复编译错误",
		"首先把待办批量改成短标题",
		"We need output only errors in the deploy log",
		"We need to answer the on-call questions",
		// Labels wrapping a real title: the label is stripped, not condemned.
		"Title: Q4 planning",
		"Output: quarterly metrics dashboard",
		"标题：登录页崩溃修复",
		// Empty and punctuation-only candidates are "nothing", not "leak".
		"",
		"   ",
	}
	for _, in := range cases {
		if titleLineIsThinking(in) {
			t.Errorf("titleLineIsThinking(%q) = true, want false", in)
		}
	}
}

func TestStripTitleThinking(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Fix login crash", "Fix login crash"},
		{"<think>We need answer title</think>Fix login", " Fix login"},
		{"<thinking>\nlong reasoning\n</thinking>\n修复登录", " \n修复登录"},
		{"<think>unclosed reasoning we need answer", ""},
		{"Good title<think>then reasoning to the end", "Good title"},
	} {
		if got := stripTitleThinking(c.in); got != c.want {
			t.Errorf("stripTitleThinking(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTitleReplyIsThinkingLeak(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want bool
	}{
		{"plain pollution", "We need answer title", true},
		{"known kind prefix carrying pollution", "功能 | We need answer title only", true},
		{"stray closing think tag", "</think>We need answer title", true},
		{"complete think block then real title", "<think>we need answer</think>修复登录崩溃", false},
		{"normal title", "修复 | 登录页崩溃", false},
		{"empty", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cleaned := stripTitleThinking(c.in)
			if got := titleReplyIsThinkingLeak(cleaned); got != c.want {
				t.Errorf("titleReplyIsThinkingLeak(strip(%q)) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestFirstTitleLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"only line", "only line"},
		{"  first  \nsecond", "first"},
		{"\n\n  real first\nnext", "real first"},
		{"\n  \n", ""},
	} {
		if got := firstTitleLine(c.in); got != c.want {
			t.Errorf("firstTitleLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
