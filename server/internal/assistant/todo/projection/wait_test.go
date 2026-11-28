package projection

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/jsonsubset"
)

var waitSchema = sync.OnceValues(func() (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "schema", "wire", "todo-wait.schema.json"))
	if err != nil {
		return nil, err
	}
	return jsonsubset.Compile(data)
})

func holdsToWait(t *testing.T, raw []byte) string {
	t.Helper()
	root, err := waitSchema()
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonsubset.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jsonsubset.ValidateAt(root, root["$defs"].(map[string]any)["TodoWait"].(map[string]any), v, "$", 0)
}

func TestProjectionStructuredWaitKinds(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	until := at.Add(time.Hour)
	for _, tc := range []struct {
		on      assistantwire.WaitKind
		target  assistantwire.TaskWaitTarget
		kind    assistantwire.TodoWaitKind
		label   string
		handoff string
		url     string
		wake    time.Time
	}{
		{on: assistantwire.WaitDependency, target: assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetTodo, Ref: "todo_translation", Summary: "Translated copy"},
			kind: assistantwire.TodoWaitKindTodoHandoff, label: "Translated copy", handoff: "todo_translation"},
		{on: assistantwire.WaitDate, target: assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetTime, Ref: "Release day"},
			kind: assistantwire.TodoWaitKindDate, label: "Release day", wake: until},
		{on: assistantwire.WaitExternal, target: assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetIssue, Ref: "https://github.com/example/app/issues/6", Summary: "Provider response"},
			kind: assistantwire.TodoWaitKindExternalEvent, label: "Provider response", url: "https://github.com/example/app/issues/6"},
		{on: assistantwire.WaitUser, target: assistantwire.TaskWaitTarget{Ref: "Trip preference"},
			kind: assistantwire.TodoWaitKindPersonReply, label: "Trip preference"},
	} {
		t.Run(string(tc.on), func(t *testing.T) {
			f := Facts{Phase: assistantwire.TaskPhaseWaiting, RuntimeAt: at,
				WaitState: &assistantwire.TaskWaitState{WaitingOn: tc.on, Target: tc.target, Reason: "A decision is pending", Until: until}}
			if tc.on == assistantwire.WaitDependency {
				f.Delivery = &Delivery{State: deliveryLive} // A deployed PR does not resolve a separate dependency.
			}
			for _, enabled := range []bool{false, true} {
				f.TodosV2 = enabled
				views, waits := ViewsAndWaits([]Facts{f}, at)
				v, w := views[0], waits[0]
				if v.Phase != assistantwire.TodoPhaseWaiting || v.Section != assistantwire.TodoListSectionWaiting ||
					v.Line == nil || v.Line.Key != assistantwire.TodoLineKeyParked || len(v.Asks) != 0 {
					t.Fatalf("todos_v2=%t has unexpected view: %+v", enabled, v)
				}
				if w == nil || w.Kind != tc.kind || w.WaitingOn != tc.label || w.HandoffTodoID.String() != tc.handoff ||
					w.URL != tc.url || w.Reason != f.WaitState.Reason || !w.Since.Equal(at) || !w.Until.Equal(until) || !w.WakeAt.Equal(tc.wake) {
					t.Fatalf("todos_v2=%t has unexpected wait: %+v", enabled, w)
				}
				raw, err := json.Marshal(w)
				if err != nil {
					t.Fatal(err)
				}
				if problem := holdsToWait(t, raw); problem != "" {
					t.Fatalf("structured wait violates wire schema: %s", problem)
				}
			}
			f.DoneAt = &at
			if got := View(f, at); got.Phase != assistantwire.TodoPhaseDone || Wait(f, at) != nil {
				t.Fatalf("closed task was affected by its stale runtime wait: %+v", got)
			}
		})
	}
}

func TestProjectionKeepsExplicitPRWaitWithLandedMode(t *testing.T) {
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	f := Facts{
		Phase: assistantwire.TaskPhaseWaiting, CompletionModeNow: assistantwire.CompletionModeLanded,
		WaitState: &assistantwire.TaskWaitState{WaitingOn: assistantwire.WaitExternal,
			Target: assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetPR, Ref: "https://github.com/example/app/pull/12"}},
		Delivery: &Delivery{State: deliveryMerged},
	}
	if w := Wait(f, now); w == nil {
		t.Fatal("an explicit external wait was discarded")
	}
	f.ObservedWait = true
	if w := Wait(f, now); w != nil {
		t.Fatalf("a stale provider deploy wait held a merged landed task: %+v", w)
	}
	f.CompletionModeNow = assistantwire.CompletionModeVerified
	if w := Wait(f, now); w == nil {
		t.Fatal("deployment verification still holds the task")
	}
}

func TestProjectionFollowsTheWaitCases(t *testing.T) {
	data, err := os.ReadFile(fixtures + "todo-wait/todo_wait.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Cases   []struct {
			Name     string    `json:"name"`
			Input    viewInput `json:"input"`
			Expected struct {
				Wait json.RawMessage `json:"wait"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != 1 || len(doc.Cases) == 0 {
		t.Fatalf("todo-wait/todo_wait.json: %v (version %d, %d cases)", err, doc.Version, len(doc.Cases))
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			list := viewList(t, c.Input)
			related := relate(list)
			got := Wait(related[0], c.Input.Now)
			views, waits := ViewsAndWaits(list, c.Input.Now)
			if !reflect.DeepEqual(waits[0], got) {
				t.Fatalf("ViewsAndWaits[0] = %+v, Wait = %+v", waits[0], got)
			}
			wantNull := len(c.Expected.Wait) == 0 || string(bytes.TrimSpace(c.Expected.Wait)) == "null"
			if wantNull {
				if got != nil {
					t.Fatalf("got wait %+v, want nil", got)
				}
				if views[0].Phase == assistantwire.TodoPhaseWaiting {
					t.Fatalf("phase is waiting but wait is nil")
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil wait, want %s", c.Expected.Wait)
			}
			if views[0].Phase != assistantwire.TodoPhaseWaiting {
				t.Fatalf("wait is non-nil but phase is %q", views[0].Phase)
			}
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if err := json.Unmarshal(out, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(c.Expected.Wait, &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("TodoWait:\n got  %s\n want %s", out, compact(t, c.Expected.Wait))
			}
			if p := holdsToWait(t, out); p != "" {
				t.Errorf("TodoWait does not hold to schema: %s", p)
			}
		})
	}
}

// Invariant: across every shared todo-view case, Wait is non-nil if and only if Phase is waiting, and every
// emitted Wait holds to schema/wire/todo-wait.schema.json.
func TestProjectionWaitMatchesWaitingPhaseAcrossAllViewCases(t *testing.T) {
	for _, c := range readCases[viewInput](t, "todo-view/todo_view.json") {
		t.Run(c.Name, func(t *testing.T) {
			list := viewList(t, c.Input)
			views, waits := ViewsAndWaits(list, c.Input.Now)
			v, w := views[0], waits[0]
			if v.Phase == assistantwire.TodoPhaseWaiting {
				if w == nil {
					t.Fatalf("phase is waiting (reason %q) but Wait is nil", v.Reason)
				}
				if !w.Kind.Valid() {
					t.Fatalf("invalid wait kind %q", w.Kind)
				}
				raw, err := json.Marshal(w)
				if err != nil {
					t.Fatal(err)
				}
				if p := holdsToWait(t, raw); p != "" {
					t.Fatalf("TodoWait schema violation: %s", p)
				}
			} else if w != nil {
				t.Fatalf("phase is %q but Wait is non-nil: %+v", v.Phase, w)
			}
		})
	}
}

func TestBuildPRWaitHandlesForgeReviewRequiredAndChecksFailure(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	f := Facts{
		ID:        "todo_forge_pr",
		Title:     "Real forge PR facts",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Minute),
		PRURL:     "https://github.com/org/repo/pull/99",
		PRStatus: &PRStatus{
			URL:       "https://github.com/org/repo/pull/99",
			State:     "open",
			Draft:     false,
			Review:    "required",
			Checks:    "failure",
			CheckedAt: now.Add(-time.Minute),
		},
	}
	w := Wait(f, now)
	if w == nil || w.Kind != assistantwire.TodoWaitKindPrMerge {
		t.Fatalf("Wait() = %+v, want pr_merge", w)
	}
	want := []assistantwire.TodoWaitPRBlocker{
		assistantwire.TodoWaitPRBlockerReviewRequired,
		assistantwire.TodoWaitPRBlockerChecksFailing,
	}
	if !reflect.DeepEqual(w.PrBlockers, want) {
		t.Fatalf("PrBlockers = %+v, want %+v", w.PrBlockers, want)
	}
}
