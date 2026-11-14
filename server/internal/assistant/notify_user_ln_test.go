package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// When the hub's ln mode held a notify_user notice for the briefing, the tool answer tells the model it did not reach
// the phone, so the model never believes the user was alerted.
func TestNotifyUserHeldNoticeTellsModelItWentToBriefing(t *testing.T) {
	f := newFakeHub().withMachine()
	f.ownModels = true
	f.notifyResult = NotifyResult{OK: true, Held: true, Reason: "ln_p1_held"}
	s := newTestService(t, f)

	out, err := runTool(s, "routine-conv", "notify_user", map[string]any{
		"title": "Scheduled run finished", "body": "The nightly report is ready.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已转成简报") {
		t.Fatalf("answer = %q, want it to say the notice went to the briefing", out)
	}
	// The tool leaves the level empty for the hub; the hub (in real ln mode) levels a non-main conversation's notice
	// P1 and records the intercept that produced this held answer.
	if len(f.pushes) != 1 || f.pushes[0].Level != "" {
		t.Fatalf("pushed = %+v, want the one notify_user push with its level left empty", f.pushes)
	}
}

// A push that actually went out answers as before, with no briefing wording.
func TestNotifyUserDeliveredAnswersSent(t *testing.T) {
	f := newFakeHub().withMachine()
	f.ownModels = true
	s := newTestService(t, f)

	out, err := runTool(s, "main-conv", "notify_user", map[string]any{
		"title": "Done", "body": "PR merged", "push_level": "P0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Notification sent." {
		t.Fatalf("answer = %q, want the plain sent answer", out)
	}
	if len(f.pushes) != 1 || f.pushes[0].Level != assistantwire.LevelP0 {
		t.Fatalf("pushed = %+v, want the one explicit P0 push", f.pushes)
	}
}

// With no push_level named the tool must pass an empty level, not normalize it to P1: in ln mode the hub then levels
// it by conversation (the main conversation's ordinary notices still interrupt), and with ln mode off the hub derives
// P1 itself. Prefilling P1 here would make the hub's main-conversation rule unreachable (#7732).
func TestNotifyUserKeepsAnEmptyLevelForTheHub(t *testing.T) {
	f := newFakeHub().withMachine()
	f.ownModels = true
	s := newTestService(t, f)

	if _, err := runTool(s, "main-conv", "notify_user", map[string]any{
		"title": "Ready", "body": "The report is ready.",
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.pushes) != 1 || f.pushes[0].Level != "" {
		t.Fatalf("pushed = %+v, want one notification whose level is left empty for the hub", f.pushes)
	}
}
