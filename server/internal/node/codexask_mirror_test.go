package node

import (
	"testing"

	"github.com/nwverse/persona/server/internal/screen"
)

func TestCodexApprovalOccurrenceTimestampSurvivesScans(t *testing.T) {
	cards := &codexCards{}
	agent := codexAgent{kind: "codex", cwd: "/work/example"}
	dialog := &screen.CodexApprovalDialog{Rev: "rev-1", Title: "Run command", Command: "printf ok"}
	first := cards.approvalOccurrenceCard("s1", agent, "Conversation", dialog)
	second := cards.approvalOccurrenceCard("s1", agent, "Conversation", dialog)
	if first.ID == "" || second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("unchanged dialog occurrence: first=%+v second=%+v", first, second)
	}
	third := first
	waitFor(t, "a later Codex approval occurrence", func() bool {
		cards.approvalGone("s1")
		third = cards.approvalOccurrenceCard("s1", agent, "Conversation", dialog)
		return !third.CreatedAt.Equal(first.CreatedAt)
	})
	if third.ID != first.ID || third.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("later dialog must be a new occurrence: first=%+v third=%+v", first, third)
	}
}
