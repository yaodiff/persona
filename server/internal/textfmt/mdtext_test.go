package textfmt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipMarkdown(t *testing.T) {
	short := "**完成**：改了 `a.go`"
	if got := ClipMarkdown(short, 100); got != short {
		t.Errorf("a short text is kept: %q", got)
	}
	for _, tc := range []struct {
		name, in string
		n        int
		want     string
	}{
		{"on a line break", "## 根因\n- **一**：甲乙丙丁\n- **二**：戊己庚辛壬癸", 22, "## 根因\n- **一**：甲乙丙丁…"},
		{"not inside bold", "前面的说明文字 **很长的加粗内容在这里**", 15, "前面的说明文字…"},
		{"not inside code", "前面的说明文字 `POST /api/v1/x` 后面", 15, "前面的说明文字…"},
		{"not inside a link", "前面的说明文字 [链接文字](https://example.com/a)", 25, "前面的说明文字…"},
		{"a closed span stays", "改了 `a.go` 和 **b** 还有很多很多很多", 18, "改了 `a.go` 和 **b**…"},
		{"not inside a double-backtick span", "前面的文字 ``abc def ghi`` 后面", 12, "前面的文字…"},
		{"not inside a three-backtick span", "前面的文字 ```abc def ghi``` 后面", 12, "前面的文字…"},
		{"a longer backtick run inside a span stays", "前面 ``a`b`` 后面的说明", 10, "前面 ``a`b``…"},
		{"bold marks inside code do not count", "运行 `make **target` 然后 **加粗的内容很多很多字", 24, "运行 `make **target` 然后…"},
		{"not inside single-star italic", "前面的说明 *斜体里的内容很多很多字*", 14, "前面的说明…"},
		{"not inside single-underscore italic", "前面的说明 _斜体里的内容很多很多字_", 14, "前面的说明…"},
		{"an intraword underscore stays", "见 foo_bar_baz 这个标识的说明还有很多很多字", 22, "见 foo_bar_baz 这个标识的说明还…"},
		{"cut between the bold marks", "前面 **加粗内容很多字", 4, "前面…"},
		{"not inside strike-through", "前面的说明 ~~删掉的内容还有很多字~~", 14, "前面的说明…"},
		{"a star-led list line stays", "* 要做的事情还有很多很多字", 8, "* 要做的事情还…"},
		{"stars flanked by spaces stay", "2 * 3 * 4 的说明还有很多字", 12, "2 * 3 * 4 的说…"},
		{"a closer-only star with no opener stays", "a (* b) 后面的说明还有很多字", 12, "a (* b) 后面的说…"},
		{"closer-only bold marks with no opener stay", "a** b 后面的说明还有很多字", 12, "a** b 后面的说明还…"},
		{"a bracket inside code is code", "见 `a[0` 的说明文字还有很多很多", 5, "见…"},
		{"an unclosed link trims", "改了 [a](x) 还有 [b 很长的文字", 16, "改了 [a](x) 还有…"},
		{"a bracket inside a closed link url stays", "见 [a](http://x/[?q=1) 后面的文字很多", 24, "见 [a](http://x/[?q=1) 后面…"},
		{"a code block is closed", "说明\n```go\nfunc a() {}\nfunc b() {}\nfunc c() {}", 34, "说明\n```go\nfunc a() {}\nfunc b() {}\n```\n…"},
	} {
		got := ClipMarkdown(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("%s: ClipMarkdown = %q, want %q", tc.name, got, tc.want)
		}
		if outside := marksOutsideCode(got); outside.boldStar != 0 || outside.boldUnder != 0 || outside.star != 0 || outside.under != 0 || outside.strike != 0 || outside.bracket != 0 || outside.endsInCode {
			t.Errorf("%s: unpaired marks outside code in %q: %+v", tc.name, got, outside)
		}
		if utf8.RuneCountInString(got) > tc.n+len("\n```\n…") {
			t.Errorf("%s: %q is longer than %d", tc.name, got, tc.n)
		}
	}
}

// outsideMarks are the inline marks left outside balanced backtick spans: the clipped text must leave none unpaired.
type outsideMarks struct {
	boldStar, boldUnder, star, under, strike, bracket int
	endsInCode                                        bool
}

func marksOutsideCode(md string) outsideMarks {
	var o outsideMarks
	inCode := false
	codeLen := 0
	var boldStarOpen, boldUnderOpen, starOpen, underOpen, strikeOpen, linkURL bool
	bracketDepth := 0
	for i := 0; i < len(md); {
		if md[i] == '`' {
			k := 1
			for i+k < len(md) && md[i+k] == '`' {
				k++
			}
			if inCode {
				if k == codeLen {
					inCode = false
				}
			} else {
				inCode, codeLen = true, k
			}
			i += k
			continue
		}
		if inCode {
			i++
			continue
		}
		switch {
		case strings.HasPrefix(md[i:], "**"):
			if opens, closes := emphasisFlanks(md, i, 2); opens || closes {
				if boldStarOpen {
					boldStarOpen = false
				} else if opens {
					boldStarOpen = true
				}
			}
			i += 2
		case md[i] == '*':
			if opens, closes := emphasisFlanks(md, i, 1); opens || closes {
				if starOpen {
					starOpen = false
				} else if opens {
					starOpen = true
				}
			}
		case strings.HasPrefix(md[i:], "__"):
			if opens, closes := emphasisFlanks(md, i, 2); (opens || closes) && !intraWordUnderscore(md, i, 2) {
				if boldUnderOpen {
					boldUnderOpen = false
				} else if opens {
					boldUnderOpen = true
				}
			}
			i += 2
		case md[i] == '_':
			if opens, closes := emphasisFlanks(md, i, 1); (opens || closes) && !intraWordUnderscore(md, i, 1) {
				if underOpen {
					underOpen = false
				} else if opens {
					underOpen = true
				}
			}
		case strings.HasPrefix(md[i:], "~~"):
			strikeOpen = !strikeOpen
			i += 2
		case md[i] == '[' && !linkURL:
			bracketDepth++
		case md[i] == ']' && bracketDepth > 0 && !linkURL:
			if strings.HasPrefix(md[i+1:], "(") {
				linkURL = true
			}
			bracketDepth--
		case md[i] == ')' && linkURL:
			linkURL = false
		}
		i++
	}
	if boldStarOpen {
		o.boldStar = 1
	}
	if boldUnderOpen {
		o.boldUnder = 1
	}
	if starOpen {
		o.star = 1
	}
	if underOpen {
		o.under = 1
	}
	if strikeOpen {
		o.strike = 1
	}
	o.bracket = bracketDepth
	o.endsInCode = inCode
	return o
}
