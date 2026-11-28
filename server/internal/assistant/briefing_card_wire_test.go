package assistant

import (
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

// The enabled module must keep the card JSON, including button values and the UTC review round, byte-identical.
func TestBriefCardWire(t *testing.T) {
	needsBriefingModule(t)
	at := time.Date(2026, 1, 2, 8, 30, 0, 123000000, time.UTC)
	f := &modapi.BriefSnapshot{At: at, Lang: "en", Web: "https://example.com",
		Waiting:       []modapi.BriefBoardRow{{Summary: "Allow *edit*?", From: "Agent", Waited: 75 * time.Minute, Nudged: true}},
		Accept:        []modapi.BriefBoardRow{{ID: "review", Summary: "Check result", Asked: at, Link: "https://example.com/todos/review"}},
		ProgressTotal: 1, Progress: []modapi.BriefTaskProgress{{ID: "work", Title: "Ship report", Stage: "review"}},
	}
	s := newTestService(t, newFakeHub().withMachine())
	card, _, _ := s.briefCard(f, "Morning", "Today's **summary**.")
	got := card.JSON()
	want, err := os.ReadFile("testdata/briefing_card.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("card JSON changed:\ngot  %s\nwant %s", got, want)
	}
}
