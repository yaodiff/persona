package assistant

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestOutsideDecisionStableOptionIDsAreSeparateFromLocalizedLabels(t *testing.T) {
	e := newTodoEnv(t, nil)
	in := outsideFixture()
	in.Reversible = outsideBool(false)
	in.AllowDefault = outsideBool(false)
	in.WhyOwner = "visible_to_others"
	in.Recommend = "Deny"
	in.Options = []assistantwire.OutsideDecisionOption{
		{ID: "Approve", Label: "批准", LeadsTo: "只执行这一次"},
		{ID: "Deny", Label: "拒绝", LeadsTo: "什么都不执行"},
	}
	d := raiseOutside(t, e, "user-cli", in)
	if d.Recommend != "拒绝" || len(d.Options) != 2 || d.Options[0].ID != "Deny" || d.Options[0].Label != "拒绝" {
		t.Fatalf("stable recommendation and localized labels = %+v", d)
	}
	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "批准"}); rec.Code != http.StatusOK {
		t.Fatalf("answer the localized Approve label: %d %s", rec.Code, rec.Body)
	}
	answered := pollOutside(t, e, "user-cli", d.ID)
	if answered.Answer == nil || answered.Answer.Option != "批准" || answered.Answer.OptionID != "Approve" {
		t.Fatalf("the answer did not preserve both label and stable id: %+v", answered.Answer)
	}
}

func TestOutsideDecisionRejectsDuplicateStableOptionIDs(t *testing.T) {
	e := newTodoEnv(t, nil)
	in := outsideFixture()
	in.Options[0].ID = "Approve"
	in.Options[1].ID = "Approve"
	if code, out := outsideCall(t, e, "user-cli", http.MethodPost, "/api/v1/decisions", in); code != http.StatusBadRequest || out.Decision != nil {
		t.Fatalf("duplicate stable option ids: %d %+v, want 400 without a decision", code, out)
	}
}
