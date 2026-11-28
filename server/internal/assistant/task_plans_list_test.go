package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
)

func listedTaskPlans(t *testing.T, w *httptest.ResponseRecorder) []*TaskPlan {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out struct {
		Plans []*TaskPlan `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Plans == nil {
		t.Fatalf("expected a plans array: %s (%v)", w.Body, err)
	}
	return out.Plans
}

func TestTaskPlansList(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		e.sessions("server", scopeListed()...)
		own := []*TaskPlan{
			{ID: "plan_main", Conversation: "conv1", State: planOpen},
			{ID: "plan_side", Conversation: "side", State: planOpen},
			{ID: "plan_health", Conversation: "h0", State: planOpen},
			{ID: "plan_started", Conversation: "conv1", State: planStarted},
			{ID: "plan_dismissed", Conversation: "conv1", State: planDismissed},
		}
		for _, p := range own {
			if err := e.s.savePlan(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			query string
			ids   []string
		}{
			{"", []string{"plan_main", "plan_side"}},
			{"?conversation=conv1", []string{"plan_main"}},
			{"?conversation=side", []string{"plan_side"}},
			{"?conversation=h0", []string{"plan_health"}},
			{"?conversation=unknown", nil},
			{"?conversation=", nil},
		} {
			plans := listedTaskPlans(t, e.do("GET", "/api/v1/task-plans"+tc.query, ""))
			var ids []string
			for _, p := range plans {
				ids = append(ids, p.ID)
				if p.State != planOpen || p.ID == "plan_health" && p.Space != SpaceHealth {
					t.Errorf("%s: plan not open or missing its space: %+v", tc.query, p)
				}
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.ids) {
				t.Errorf("%s: plans %v, want %v", tc.query, ids, tc.ids)
			}
		}
	})
}

func TestTaskPlansListExtensionScope(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	e.sessions("server", scopeListed()...)
	for _, p := range []*TaskPlan{
		{ID: "plan_visible", Conversation: "conv1", State: planOpen},
		{ID: "plan_health", Conversation: "h0", State: planOpen},
		{ID: "plan_unknown", Conversation: "unknown", State: planOpen},
		{ID: "plan_health_task", Conversation: "conv1", State: planOpen, Tasks: []PlanTask{
			{Cwd: identity.HealthDir("/home/owner")},
		}},
	} {
		if err := e.s.savePlan(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	for _, conv := range []string{"h0", "unknown"} {
		w := e.doAs(roleExtension, "GET", "/api/v1/task-plans?conversation="+conv, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, body %s", conv, w.Code, w.Body)
		}
	}
	plans := listedTaskPlans(t, e.doAs(roleExtension, "GET", "/api/v1/task-plans?conversation=conv1", ""))
	if len(plans) != 1 || plans[0].ID != "plan_visible" {
		t.Fatalf("extension received plans outside its feed scope: %+v", plans)
	}
	if plans := listedTaskPlans(t, e.doAs(roleExtension, "GET", "/api/v1/task-plans", "")); len(plans) != 0 {
		t.Fatalf("extension received unscoped plans: %+v", plans)
	}
}
