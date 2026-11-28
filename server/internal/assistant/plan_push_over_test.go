package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestPlanWaitingReceiptWhenOwnerIsPresent(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "side1"})
	res, err := s.toolTaskPlan(ctx, "side1", planArgs(false))
	if err != nil || !strings.Contains(res, "修登录崩溃: waiting for the user to tap Start") {
		t.Fatalf("waiting plan receipt: %q, %v", res, err)
	}
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("the owner's conversation received a duplicate push: %+v", got)
	}
}

// A real owner-step plan card's push stands for the card: it carries the plan as its card, and once the card is decided anywhere
// (Don't start here) the manager tells the hub it is over, so the push leaves the phones it went to. Start on a card
// already decided answers with the plan as it is, as Don't start does.
func TestPlanPushIsTakenBackOnceDecided(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	if _, err := s.toolTaskPlan(ctx, "side1", planArgsWithStep(t, ownerStepMoney)); err != nil {
		t.Fatal(err)
	}
	pushed := f.pushed()
	if len(pushed) != 1 || pushed[0].CardID == "" || pushed[0].ID != "card:"+pushed[0].CardID {
		t.Fatalf("the plan's push does not stand for its card: %+v", pushed)
	}
	id := pushed[0].CardID
	over := func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, e := range f.told {
			var r assistantwire.FleetResolved
			if e.event == assistantwire.EventFleetResolved && json.Unmarshal(e.params, &r) == nil && r.ID == id {
				return true
			}
		}
		return false
	}
	if over() {
		t.Fatal("the push was taken back while the card is open")
	}
	if w := request(s, http.MethodPost, "/api/v1/task-plans/"+id+"/dismiss", ""); w.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the plan's push to be taken back", over)

	// Start on a card decided elsewhere stays a conflict (the phone's copy is gone by now anyway).
	if w := request(s, http.MethodPost, "/api/v1/task-plans/"+id+"/start", ""); w.Code != http.StatusConflict {
		t.Fatalf("Start on a dismissed card: %d %s", w.Code, w.Body)
	}
}
