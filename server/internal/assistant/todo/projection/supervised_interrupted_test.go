package projection

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSupervisedInterruptedStaysWithSupervisor(t *testing.T) {
	baseSession := proto.Session{
		MachineID:     "m1",
		SessionID:     "s-err",
		Title:         "Worker run",
		Status:        proto.StatusIdle,
		State:         proto.StateIdle,
		StateSince:    hours(-1),
		UpdatedAt:     hours(-1),
		TurnError:     "API Error: 503 Service Unavailable",
		TurnErrorKind: proto.TurnErrorTransient,
	}
	baseTodo := func(id string, sup *Supervision) Facts {
		return Facts{
			ID:          id,
			Title:       "Investigate error handling",
			CreatedAt:   hours(-5),
			UpdatedAt:   hours(-1),
			Origin:      "conv_lead",
			Agents:      []Agent{{MachineID: "m1", SessionID: "s-err", Title: "Worker run"}},
			Sessions:    []proto.Session{baseSession},
			Supervision: sup,
		}
	}

	t.Run("supervised unescalated 503 stays in working persona_working and out of needs_you", func(t *testing.T) {
		f := baseTodo("todo_sup_503", &Supervision{Supervisor: "conv_lead", UpdatedAt: hours(-2)})
		views := Views([]Facts{f}, testNow)
		v := views[0]
		if v.Phase != assistantwire.TodoPhaseWorking || v.Section != assistantwire.TodoListSectionWorking ||
			v.Reason != assistantwire.TodoReasonPersonaWorking {
			t.Fatalf("supervised 503 view = %+v, want working/working/persona_working", v)
		}
		if v.Line == nil || v.Line.Key != assistantwire.TodoLineKeyInterrupted || v.Line.Args["run"] != "Worker run" {
			t.Fatalf("supervised 503 line = %+v, want interrupted line for Worker run", v.Line)
		}
		if c := Counts([]Facts{f}, views); c.NeedsYou != 0 || c.InProgress != 1 {
			t.Fatalf("supervised 503 counts = %+v, want NeedsYou=0 InProgress=1", c)
		}
	})

	t.Run("supervised 503 escalated to needs_user surfaces in needs_you", func(t *testing.T) {
		f := baseTodo("todo_sup_503_esc", &Supervision{
			Supervisor: "conv_lead",
			State:      reviewNeedsUser,
			UpdatedAt:  hours(-1),
			Reviews:    []Review{{At: hours(-1), Verdict: reviewNeedsUser, Reason: "Switch provider?"}},
		})
		views := Views([]Facts{f}, testNow)
		if v := views[0]; v.Section != assistantwire.TodoListSectionNeedsYou || v.Reason != assistantwire.TodoReasonOwnerDecides {
			t.Fatalf("escalated 503 view = %+v, want needs_you/owner_decides", v)
		}
		if c := Counts([]Facts{f}, views); c.NeedsYou != 1 {
			t.Fatalf("escalated 503 counts = %+v, want NeedsYou=1", c)
		}
	})

	t.Run("supervised 503 parked stays parked rather than working or needs_you", func(t *testing.T) {
		f := baseTodo("todo_sup_503_parked", &Supervision{
			Supervisor: "conv_lead",
			State:      reviewParked,
			UpdatedAt:  hours(-1),
			Parked:     &Parked{Reason: "Upstream outage", Until: hours(2), WaitingOn: parkExternalEvent},
			Reviews:    []Review{{At: hours(-1), Verdict: reviewParked}},
		})
		v := View(f, testNow)
		if v.Section != assistantwire.TodoListSectionWaiting || v.Reason != assistantwire.TodoReasonParked {
			t.Fatalf("parked 503 view = %+v, want waiting/parked", v)
		}
	})

	t.Run("unsupervised 503 still alerts the owner in needs_you", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			sup  *Supervision
		}{
			{"nil supervision", nil},
			{"empty supervisor", &Supervision{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := baseTodo("todo_unsup_503", tc.sup)
				views := Views([]Facts{f}, testNow)
				if v := views[0]; v.Phase != assistantwire.TodoPhaseNeedsYou || v.Section != assistantwire.TodoListSectionNeedsYou ||
					v.Reason != assistantwire.TodoReasonInterrupted {
					t.Fatalf("unsupervised 503 view = %+v, want needs_you/needs_you/interrupted", v)
				}
				if c := Counts([]Facts{f}, views); c.NeedsYou != 1 {
					t.Fatalf("unsupervised 503 counts = %+v, want NeedsYou=1", c)
				}
			})
		}
	})

	t.Run("user_check accepted verification stays out of needs_you", func(t *testing.T) {
		f := Facts{
			ID:        "todo_uc",
			Title:     "Read-only research summary",
			CreatedAt: hours(-10),
			UpdatedAt: hours(-2),
			Origin:    "conv_lead",
			Supervision: &Supervision{
				Supervisor: "conv_lead",
				State:      "user_check",
				UpdatedAt:  hours(-2),
				Reviews:    []Review{{At: hours(-2), Verdict: reviewAccepted}},
			},
			Ask: &Ask{Outcome: outcomeDone, Verdict: "Summary ready for review", AskedAt: hours(-2)},
		}
		views := Views([]Facts{f}, testNow)
		if v := views[0]; v.Section == assistantwire.TodoListSectionNeedsYou {
			t.Fatalf("user_check entered needs_you: %+v", v)
		}
		if c := Counts([]Facts{f}, views); c.NeedsYou != 0 {
			t.Fatalf("user_check counts = %+v, want NeedsYou=0", c)
		}
	})

	t.Run("settled or user_check task with stale unreleased 503 worker does not regress to working", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			state string
		}{
			{"user_check", reviewUserCheck},
			{"accepted", reviewAccepted},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := baseTodo("todo_stale_503_"+tc.name, &Supervision{
					Supervisor: "conv_lead",
					State:      tc.state,
					UpdatedAt:  hours(-1),
					Reviews:    []Review{{At: hours(-1), Verdict: reviewAccepted}},
				})
				views := Views([]Facts{f}, testNow)
				if v := views[0]; v.Section == assistantwire.TodoListSectionWorking || v.Section == assistantwire.TodoListSectionNeedsYou {
					t.Fatalf("%s with stale 503 view = %+v, must not regress to working or needs_you", tc.name, v)
				}
			})
		}
	})
}
