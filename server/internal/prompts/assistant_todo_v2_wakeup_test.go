package prompts

import (
	"os"
	"strings"
	"testing"
)

func TestSuperviseTodoV2WakeupGolden(t *testing.T) {
	const headPR = "https://github.com/example/app/pull/17"
	const nextPR = "https://github.com/example/app/pull/18"
	wakes := []SuperviseTodoV2Wakeup{
		{Kind: "blockers_unblocked", BlockerPRs: []string{headPR}},
		{Kind: "blockers_unblocked"},
		{Kind: "mergetrain_turn", PR: nextPR, PreviousPR: headPR, PreviousClosed: true, NeedsApproval: true},
		{Kind: "mergetrain_turn", PR: nextPR, PreviousPR: headPR},
		{Kind: "write_conflict", PeerID: "todo_peer", PeerTitle: "Update build settings", Overlaps: []string{"ci/build", "ci/test"}},
	}
	var rendered strings.Builder
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		rendered.WriteString("[" + lang + "]\n")
		for _, wake := range wakes {
			wake.Lang = lang
			rendered.WriteString(RenderSuperviseTodoV2Wakeup(wake) + "\n")
		}
	}
	const path = "testdata/todo_v2_wakeup.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(rendered.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		t.Fatalf("to-do v2 wakeup golden differs, run with UPDATE_GOLDEN=1 and review the diff")
	}
}
