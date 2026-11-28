package transcript

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// ListTitle leaves a title of at most proto.TitleMax runes as it is (blocks and all, for its readers to strip), and cuts
// a longer one to proto.TitleMax runes on a rune boundary, after stripping the blocks in front of what the user wrote
// (#16343). All titles here are synthetic.
func TestListTitle(t *testing.T) {
	fits := strings.Repeat("界", proto.TitleMax) // exactly the cap, three bytes a rune
	if got := ListTitle(fits); got != fits {
		t.Errorf("a title of exactly %d runes changed: %d runes", proto.TitleMax, utf8.RuneCountInString(got))
	}
	short := `<system-reminder>kept for the readers</system-reminder> Fix the login page`
	if got := ListTitle(short); got != short {
		t.Errorf("a short title is not stripped here: %q", got)
	}
	if got := ListTitle(""); got != "" {
		t.Errorf("empty: %q", got)
	}

	over := fits + "x"
	if got := ListTitle(over); got != fits {
		t.Errorf("one rune over the cap: %d runes, want the first %d", utf8.RuneCountInString(got), proto.TitleMax)
	}

	prompt := strings.Repeat("Refactor the parser and add tests for each case. ", 2000) // ~100 KB
	got := ListTitle(prompt)
	if n := utf8.RuneCountInString(got); n != proto.TitleMax || !strings.HasPrefix(prompt, got) {
		t.Errorf("a whole prompt: %d runes, prefix %v", n, strings.HasPrefix(prompt, got))
	}

	mixed := strings.Repeat("ab😀", 1000) // a four-byte rune every third: a byte cut would split one
	got = ListTitle(mixed)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != proto.TitleMax || !strings.HasPrefix(mixed, got) {
		t.Errorf("multibyte: valid %v, %d runes", utf8.ValidString(got), utf8.RuneCountInString(got))
	}

	// Stripped first: the cut never lands inside an injected block, and the words after it are the title.
	words := "Rewrite this paragraph so it reads plainly. " + strings.Repeat("More detail on what to change. ", 200)
	blocked := `<persona-context kind="context" title="Selected text">` + strings.Repeat("quoted text ", 300) +
		`</persona-context> ` + words
	got = ListTitle(blocked)
	if !strings.HasPrefix(got, "Rewrite this paragraph") || utf8.RuneCountInString(got) != proto.TitleMax || strings.Contains(got, "<") {
		t.Errorf("a long title behind a context block: %q…", got[:min(60, len(got))])
	}

	// A leading block longer than what is stripped is dropped as a block a clip cut off: its title, else nothing.
	huge := `<persona-context kind="context" title="Selected text">` + strings.Repeat("quoted text ", 4000) +
		`</persona-context> ` + words
	if got := ListTitle(huge); got != "Selected text" {
		t.Errorf("a block past the window: %q, want the block's title", got)
	}
	untitledBlock := `<system-reminder>` + strings.Repeat("noise ", 4000) + `</system-reminder> ` + words
	if got := ListTitle(untitledBlock); got != "" {
		t.Errorf("an untitled block past the window: %q, want nothing (the readers fall back to the folder)", got[:min(60, len(got))])
	}

	// Invalid bytes are cut the way textfmt.Cut cuts them: whole runes, each replaced.
	invalid := strings.Repeat("a\xff", proto.TitleMax)
	got = ListTitle(invalid)
	if utf8.RuneCountInString(got) != proto.TitleMax || !utf8.ValidString(got) {
		t.Errorf("invalid bytes: %d runes, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}

// A title just over the cap reads only as far as the cap to tell; a long one strips a bounded window, not the whole.
func BenchmarkListTitle(b *testing.B) {
	plain := strings.Repeat("a b ", 64)                       // at the cap: no strip
	long := strings.Repeat("see <b>this</b> and that ", 4000) // ~100 KB with tags
	for _, c := range []struct{ name, s string }{{"fits", plain}, {"long", long}} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				_ = ListTitle(c.s)
			}
		})
	}
}
