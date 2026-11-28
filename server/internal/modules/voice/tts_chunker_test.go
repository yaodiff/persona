package voice

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func feedChunker(c *ttsChunker, deltas ...string) []string {
	var out []string
	for _, d := range deltas {
		out = append(out, c.Push(d)...)
	}
	out = append(out, c.Flush()...)
	return out
}

func TestTTSChunkerChineseEarlyFirstCut(t *testing.T) {
	c := newTTSChunker(defaultTTSChunkerConfig())

	// Too short (< 6 CJK chars) at the first comma: should hold.
	if got := c.Push("好的，"); len(got) != 0 {
		t.Fatalf("expected short opening clause to wait, got %v", got)
	}
	if got := c.Push("我马上帮你查"); len(got) != 0 {
		t.Fatalf("expected wait before weak pause, got %v", got)
	}
	// Weak pause once >= 6 CJK chars: cuts early for fast first audio.
	first := c.Push("一下，")
	wantFirst := []string{"好的，我马上帮你查一下，"}
	if !slices.Equal(first, wantFirst) {
		t.Fatalf("first cut = %v, want %v", first, wantFirst)
	}

	// Later pieces do not cut on weak pauses below upper bound; they wait for sentence ends.
	if got := c.Push("然后把结果，包括细节；"); len(got) != 0 {
		t.Fatalf("expected later piece not to cut on weak pause, got %v", got)
	}
	second := c.Push("都告诉你。")
	wantSecond := []string{"然后把结果，包括细节；都告诉你。"}
	if !slices.Equal(second, wantSecond) {
		t.Fatalf("second cut = %v, want %v", second, wantSecond)
	}

	third := c.Push("还有别的事吗？")
	wantThird := []string{"还有别的事吗？"}
	if !slices.Equal(third, wantThird) {
		t.Fatalf("third cut = %v, want %v", third, wantThird)
	}
	if rem := c.Flush(); len(rem) != 0 {
		t.Fatalf("expected empty flush, got %v", rem)
	}
}

func TestTTSChunkerChineseWeakPauseVariants(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantFirst string
		wantAll   []string
	}{
		{
			name:      "semicolon",
			in:        "这个功能已经上线了；你可以直接试试。",
			wantFirst: "这个功能已经上线了；",
			wantAll:   []string{"这个功能已经上线了；", "你可以直接试试。"},
		},
		{
			name:      "enumeration comma",
			in:        "苹果、香蕉、橘子、葡萄，都很新鲜。",
			wantFirst: "苹果、香蕉、橘子、",
			wantAll:   []string{"苹果、香蕉、橘子、", "葡萄，都很新鲜。"},
		},
		{
			name:      "colon after short word waits for next weak pause",
			in:        "注意：请先保存文件，然后再重启。",
			wantFirst: "注意：请先保存文件，",
			wantAll:   []string{"注意：请先保存文件，", "然后再重启。"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTTSChunker(defaultTTSChunkerConfig())
			got := feedChunker(c, tc.in)
			if len(got) == 0 || got[0] != tc.wantFirst {
				t.Fatalf("first piece = %v, want %q", got, tc.wantFirst)
			}
			if !slices.Equal(got, tc.wantAll) {
				t.Fatalf("all pieces = %v, want %v", got, tc.wantAll)
			}
		})
	}
}

func TestTTSChunkerEnglishEarlyFirstCut(t *testing.T) {
	c := newTTSChunker(defaultTTSChunkerConfig())

	// One-word opening before comma should NOT cut.
	if got := c.Push("Sure, "); len(got) != 0 {
		t.Fatalf("expected single word before comma to hold, got %v", got)
	}
	// Once a few words accumulate (>= 3 words = 6 units), weak pause cuts early.
	first := c.Push("let me check that for you, ")
	wantFirst := []string{"Sure, let me check that for you,"}
	if !slices.Equal(first, wantFirst) {
		t.Fatalf("first = %v, want %v", first, wantFirst)
	}
	// Later sentence with commas stays intact until period.
	rest := feedChunker(c, "and once I have the numbers, including totals, I will share them.")
	wantRest := []string{"and once I have the numbers, including totals, I will share them."}
	if !slices.Equal(rest, wantRest) {
		t.Fatalf("rest = %v, want %v", rest, wantRest)
	}
}

func TestTTSChunkerUpperLengthBoundWordAndCharBoundary(t *testing.T) {
	t.Run("english cuts at word boundary", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		in := "Let me tell you a very detailed story about the four changing seasons across the northern mountain valley."
		got := feedChunker(c, in)
		if len(got) < 2 {
			t.Fatalf("expected long punctuation-free opening to split, got %v", got)
		}
		// Rejoined text must preserve every word intact (no mid-word split).
		joined := strings.Join(got, " ")
		if joined != in {
			t.Fatalf("joined = %q, want %q (pieces=%v)", joined, in, got)
		}
	})

	t.Run("chinese cuts at rune boundary", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		in := strings.Repeat("测试语音分段效果", 15) // 120 CJK chars, no punctuation
		got := feedChunker(c, in)
		if len(got) < 2 {
			t.Fatalf("expected long CJK run to split, got %v", got)
		}
		for i, p := range got {
			if !utf8.ValidString(p) {
				t.Fatalf("piece %d is invalid UTF-8: %q", i, p)
			}
		}
		if strings.Join(got, "") != in {
			t.Fatalf("rejoined CJK text lost characters")
		}
	})
}

func TestTTSChunkerDecimalsAbbreviationsAndURLs(t *testing.T) {
	t.Run("decimals and numeric punctuation", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		got := feedChunker(c, "Pi is 3.14159 and v1.2.3 costs 1,234.56 at 14:30.")
		want := []string{"Pi is 3.14159 and v1.2.3 costs 1,234.56 at 14:30."}
		if !slices.Equal(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("streamed decimal across delta boundary", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		if got := c.Push("Pi is 3."); len(got) != 0 {
			t.Fatalf("should not split streamed decimal at trailing dot, got %#v", got)
		}
		got := feedChunker(c, "14 today. Next sentence.")
		want := []string{"Pi is 3.14 today.", "Next sentence."}
		if !slices.Equal(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("common abbreviations not split", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		got := feedChunker(c,
			"Ask Dr.", " Smith about Fig. 2 at 3 p.m. today. ",
			"Meet Mr. Jones at St. Mary's Ave. in the U.S. for e.g. audio, etc. tomorrow. Done.",
		)
		want := []string{
			"Ask Dr. Smith about Fig. 2 at 3 p.m. today.",
			"Meet Mr. Jones at St. Mary's Ave. in the U.S. for e.g. audio, etc. tomorrow.",
			"Done.",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("bare and streamed URLs not split", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		got := feedChunker(c,
			"See https://", "tts.example.com/v1/guide.html?", "lang=en&x=1", " for details. ", "All done.",
		)
		want := []string{
			"See https://tts.example.com/v1/guide.html?lang=en&x=1 for details.",
			"All done.",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("sentence ending dot after URL splits next sentence", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		got := feedChunker(c, "Visit https://tts.example.com/docs. Next check the logs.")
		want := []string{"Visit https://tts.example.com/docs.", "Next check the logs."}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestTTSChunkerMarkdownCleanup(t *testing.T) {
	c := newTTSChunker(defaultTTSChunkerConfig())
	in := strings.Join([]string{
		"## Summary",
		"- Check **important** *details* and `config.json`.",
		"1. Open [the guide](https://tts.example.com/a.b?c=1) right now.",
		"2) Keep snake_case_name and 2 * 3 intact.",
		"```",
		"go test",
		"```",
		"---",
		"> Final note here.",
	}, "\n")
	got := feedChunker(c, in)
	want := []string{
		"Summary",
		"Check important details and config.json.",
		"Open the guide right now.",
		"Keep snake_case_name and 2 * 3 intact.",
		"go test",
		"Final note here.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestTTSChunkerStreamedMarkdownLink(t *testing.T) {
	c := newTTSChunker(defaultTTSChunkerConfig())
	got := feedChunker(c, "Read [our ", "guide](https://", "tts.example.com/a?", "x=1) today.", " Thanks!")
	want := []string{"Read our guide today.", "Thanks!"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTTSChunkerFlushAndWhitespaceOnly(t *testing.T) {
	t.Run("whitespace and symbol only pieces dropped without consuming first cut", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		if got := c.Push("   \n\t\n---\n***\n"); len(got) != 0 {
			t.Fatalf("expected non-speakable input to be dropped, got %v", got)
		}
		// Early first cut must still trigger on the first real sentence.
		got := c.Push("好的，我马上帮你查一下，")
		want := []string{"好的，我马上帮你查一下，"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("flush loses nothing", func(t *testing.T) {
		for _, in := range []string{"Trailing fragment without period", "Value is 3.", "Ask Dr."} {
			c := newTTSChunker(defaultTTSChunkerConfig())
			got := feedChunker(c, in)
			want := []string{in}
			if !slices.Equal(got, want) {
				t.Fatalf("input %q: got %v, want %v", in, got, want)
			}
		}
	})

	t.Run("empty and whitespace flush returns nil", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		if got := feedChunker(c, "", "   ", "\n\t  "); len(got) != 0 {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("reset restarts first-clause mode", func(t *testing.T) {
		c := newTTSChunker(defaultTTSChunkerConfig())
		_ = feedChunker(c, "First sentence.")
		c.Reset()
		got := c.Push("好的，我马上帮你查一下，")
		want := []string{"好的，我马上帮你查一下，"}
		if !slices.Equal(got, want) {
			t.Fatalf("after Reset: got %v, want %v", got, want)
		}
	})
}
