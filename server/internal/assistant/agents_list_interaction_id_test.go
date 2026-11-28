package assistant

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func TestAgentsListScreenDialogSummary(t *testing.T) {
	const question = "Use this workspace?"
	const hidden = "private input value"
	for _, tc := range []struct {
		name   string
		change func(*proto.Interaction)
	}{
		{"safe", nil},
		{"private", func(ix *proto.Interaction) { ix.Private = true }},
		{"secret", func(ix *proto.Interaction) { ix.Secret = true }},
		{"form", func(ix *proto.Interaction) { ix.Fields = []proto.Field{{}} }},
		{"unparsed", func(ix *proto.Interaction) { ix.Dialog = nil }},
		{"blank question", func(ix *proto.Interaction) { ix.Dialog.Question = " \n " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub()
			f.setSessions("box", proto.Session{SessionID: "s1", Title: "Fixture", State: proto.StateNeedsYou})
			ix := proto.Interaction{ID: "screen-1", MachineID: "box", SessionID: "s1", Kind: proto.IxScreen, Screen: hidden, Summary: hidden,
				Dialog: &screen.Dialog{Question: question, Options: []screen.Option{
					{Key: "1", Label: "Continue"}, {Key: "2", Label: "Cancel"}, {Key: "3", Label: hidden, Kind: screen.KindTypeSomething},
				}}}
			want := "终端对话框：" + question + " · Continue / Cancel"
			if tc.change != nil {
				tc.change(&ix)
				want = "终端对话框等你处理"
			}
			f.hubCards = []proto.Interaction{ix}
			s := newStorelessService(t, f)

			got := s.describeAgents(t.Context(), proto.StateNeedsYou, "", "")
			if !strings.Contains(got, want+" (interaction_id=screen-1)") || strings.Contains(got, hidden) {
				t.Fatalf("screen summary = %s, want %q without raw or typed text", got, want)
			}
			if tc.change != nil && (strings.Contains(got, question) || strings.Contains(got, "Continue / Cancel")) {
				t.Fatalf("protected screen summary exposed the dialog: %s", got)
			}
		})
	}
}

// A pending card's text is clipped in agents_list, but never its interaction_id: without the id the assistant cannot
// answer the card with interaction_answer, so a long question (or approval, or plan) must not push it past the cut.
func TestAgentsListKeepsInteractionIDPastLongPendingText(t *testing.T) {
	long := strings.Repeat("Should I import the generated fixtures into the shared test database before the migration runs? ", 12)
	const id = "ix-7f3c2a9e-51b4-4d0e-9a61-2c8e5b1f0d37"
	cases := []struct {
		name string
		ix   proto.Interaction
	}{
		{"question", proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{
			Question: long, Options: []proto.QuestionOption{{Label: "Import"}, {Label: "Skip"}},
		}}}},
		{"permission", proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", Summary: long}},
		{"plan", proto.Interaction{Kind: proto.IxPlan, Plan: "# " + long + "\n\n1. Do it"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub()
			f.setSessions("box", proto.Session{SessionID: "s1", Title: "Fixture import", State: proto.StateNeedsYou})
			ix := tc.ix
			ix.ID, ix.MachineID, ix.SessionID = id, "box", "s1"
			f.hubCards = []proto.Interaction{ix}
			s := newStorelessService(t, f)

			got := s.describeAgents(context.Background(), proto.StateNeedsYou, "", "")
			const head = "    等你：" // i18n-ignore: agents_list output for the model
			var line string
			for _, l := range strings.Split(got, "\n") {
				if strings.HasPrefix(l, head) {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no pending line for the card:\n%s", got)
			}
			suffix := " (interaction_id=" + id + ")"
			if !strings.HasSuffix(line, suffix) {
				t.Errorf("the pending line lost its full interaction_id (want suffix %q):\n%s", suffix, line)
			}
			// The text before the id keeps its 400-rune clip (plus the ellipsis).
			if n := utf8.RuneCountInString(line); n > utf8.RuneCountInString(head)+400+1+utf8.RuneCountInString(suffix) {
				t.Errorf("the pending text must still be clipped; the line has %d runes:\n%s", n, line)
			}
		})
	}
}
