package prompts

import (
	"strings"
	"testing"
)

// The merge title prompt asks for one JSON title and shows every merged row, in the order given (#7229).
func TestInboxMergeTitle(t *testing.T) {
	sys := RenderInboxMergeTitle(InboxMergeTitle{Owner: "Alex", Language: "English"})
	for _, want := range []string{"Alex", "用English写", `{"title": "…"}`} {
		if !strings.Contains(sys, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, sys)
		}
	}
	if plain := RenderInboxMergeTitle(InboxMergeTitle{}); !strings.Contains(plain, "沿用这些标题的语言") {
		t.Errorf("without a language the titles' own is kept:\n%s", plain)
	}
	in := RenderInboxMergeTitleInput(InboxMergeTitleInput{Items: []InboxFolderItem{
		{Kind: "dm", Source: "Lark", Title: "Budget deadline", Sender: "Ana", Summary: "Ana asks for the numbers"},
		{Kind: "mail", Title: "Budget numbers", Preview: "Hi, attached"},
	}})
	i, j := strings.Index(in, "Budget deadline"), strings.Index(in, "Budget numbers")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("the rows are not shown in order:\n%s", in)
	}
	for _, want := range []string{"来自：Ana", "摘要：Ana asks for the numbers", "内容：Hi, attached", "类型：dm（Lark）"} {
		if !strings.Contains(in, want) {
			t.Errorf("the input lacks %q:\n%s", want, in)
		}
	}
}
