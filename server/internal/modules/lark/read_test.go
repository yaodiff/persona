package lark

import (
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
)

// A /read's prompt carries where to read and the limits (the assistant's TestGoldenBotReadGroup renders these as its
// goldens); a message id that is not fit for a directory name unquoted is made so.
func TestReadPromptFillsTheLimits(t *testing.T) {
	got := readPrompt("示例用户", "", "oc_example", "om_example", "omt_example", "看下这几条告警", false)
	want := prompts.BotReadGroup{Owner: "示例用户", ChatID: "oc_example", MessageID: "om_example", ThreadID: "omt_example",
		Task: "看下这几条告警", Dir: "~/.persona/lark-read/om_example", Default: 40, Most: 500, FileMB: 50, TotalMB: 200, Images: 20,
		Minutes: 20, Calls: 40}
	if got != want {
		t.Fatalf("readPrompt = %+v, want %+v", got, want)
	}
	if got := readPrompt("", "", "oc_example", "om_a/b c$", "", "", false).Dir; got != "~/.persona/lark-read/om_a_b_c_" {
		t.Fatalf("dir %q", got)
	}
	// With the read_group_direct switch the prompt says the answer goes straight into the group (#14437).
	if !readPrompt("", "", "oc_example", "om_example", "", "", true).Direct {
		t.Fatal("the direct prompt does not say so")
	}
}
