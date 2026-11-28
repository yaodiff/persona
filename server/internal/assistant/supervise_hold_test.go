package assistant

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

var silentSupervisionFinish = json.RawMessage(`{"visibility":"silent","summary":"Checked the task."}`)

func TestSupervisionHoldAcrossRestart(t *testing.T) {
	const refusal = "this is not an unpromoted supervision turn; normal user messages cannot be hidden"
	for _, tc := range []struct {
		name       string
		held       bool
		wantSilent bool
	}{
		{name: "active", held: true, wantSilent: true},
		{name: "promoted", held: true},
		{name: "user finish", held: true},
		{name: "released"},
		{name: "normal send"},
		{name: "owner send params"},
		{name: "turn ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := &memStore{}
			s, _ := newQuietServiceOn(t, store)
			const session = "conv1"
			if err := s.holdPushes(ctx, session); err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "promoted":
				if err := s.promoteSupervision(session); err != nil {
					t.Fatal(err)
				}
			case "user finish":
				if _, err := s.toolSupervisionFinish(ctx, session, json.RawMessage(`{"visibility":"user","summary":"Owner input needed."}`)); err != nil {
					t.Fatal(err)
				}
			case "released":
				if err := s.releasePushes(ctx, session); err != nil {
					t.Fatal(err)
				}
			case "normal send":
				if err := s.call(ctx, "server", DeviceAssistantSend, map[string]any{
					"session_id": session, "text": "Show the latest result.",
				}, nil); err != nil {
					t.Fatal(err)
				}
			case "owner send params":
				_, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: session,
					Params: map[string]any{"text": "Show the latest result."}})
				if err != nil {
					t.Fatal(err)
				}
			case "turn ended":
				s.remedyTurnEnded(ctx, session)
				if s.supervising(session) {
					t.Fatal("the ended turn still holds pushes")
				}
			}

			restarted, _ := newQuietServiceOn(t, store)
			if got := restarted.supervising(session); got != tc.held {
				t.Errorf("supervising after restart = %t, want %t", got, tc.held)
			}
			shown := slices.ContainsFunc(restarted.snapshot(ctx).Sessions, func(v SessionView) bool {
				return v.SessionID == session && v.Supervising
			})
			if shown != tc.held {
				t.Errorf("SessionView.Supervising after restart = %t, want %t", shown, tc.held)
			}
			result, err := restarted.toolSupervisionFinish(ctx, session, silentSupervisionFinish)
			if tc.wantSilent {
				if err != nil || !strings.Contains(result, `"visibility":"silent"`) {
					t.Fatalf("silent finish after restart = %q, %v", result, err)
				}
			} else if err == nil || err.Error() != refusal {
				t.Fatalf("silent finish after restart = %q, %v, want %q", result, err, refusal)
			}
		})
	}
}

func TestSupervisionHoldExpiry(t *testing.T) {
	ctx := t.Context()
	st := &memStore{}
	now := time.Now()
	holds := map[string]supervisionHold{
		"expired": {At: now.Add(-superviseHoldMax - time.Minute), Visible: true},
		"conv1":   {At: now},
	}
	if err := st.PutSetting(ctx, supervisionHoldsKey, holds); err != nil {
		t.Fatal(err)
	}
	s, _ := newQuietServiceOn(t, st)
	if s.supervising("expired") || !s.supervising("conv1") {
		t.Fatal("startup did not distinguish expired and active holds")
	}
	if _, err := s.toolSupervisionFinish(ctx, "expired", silentSupervisionFinish); err == nil {
		t.Fatal("an expired hold could hide an ordinary turn")
	}
	assertStored := func(want int) {
		t.Helper()
		var stored map[string]supervisionHold
		if _, err := st.Setting(ctx, supervisionHoldsKey, &stored); err != nil {
			t.Fatal(err)
		}
		if _, expired := stored["expired"]; expired || len(stored) != want {
			t.Fatalf("stored holds = %v, want %d active entries", stored, want)
		}
	}
	assertStored(1)
	if !s.expireSupervisionHolds(ctx, now.Add(superviseHoldMax+time.Minute)) {
		t.Fatal("the running manager did not expire its hold")
	}
	assertStored(0)
}

func TestSupervisionHoldFollowsBackgroundForkAcrossRestart(t *testing.T) {
	ctx := t.Context()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f := newQuietServiceOn(t, database)
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor = "background" })
	s.registerBackgroundRoundFork(ctx, "server", "fork", "background")
	if err := s.holdPushes(ctx, "background"); err != nil {
		t.Fatal(err)
	}

	f.settings = nil // subsequent managers load the configured supervisor from Postgres
	restarted := newServiceOn(t, f, database)
	if _, err := restarted.toolSupervisionFinish(ctx, "fork", silentSupervisionFinish); err != nil {
		t.Fatalf("background fork lost its base hold: %v", err)
	}
	if err := restarted.promoteSupervision("fork"); err != nil {
		t.Fatal(err)
	}
	promoted := newServiceOn(t, f, database)
	if _, err := promoted.toolSupervisionFinish(ctx, "fork", silentSupervisionFinish); err == nil {
		t.Fatal("background fork lost its base promotion")
	}
	if err := promoted.releasePushes(ctx, "fork"); err != nil {
		t.Fatal(err)
	}
	released := newServiceOn(t, f, database)
	if released.supervising("background") {
		t.Fatal("the released background hold came back")
	}
}

func TestSupervisionHoldSurvivesRoutineTools(t *testing.T) {
	t.Run("task_scope", func(t *testing.T) {
		s, _, todo := newSupervisedService(t)
		ctx := t.Context()
		if err := s.holdPushes(ctx, "conv1"); err != nil {
			t.Fatal(err)
		}
		if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.toolSupervisionFinish(ctx, "conv1", silentSupervisionFinish); err != nil {
			t.Fatalf("task_scope changed the hold: %v", err)
		}
	})
	t.Run("interaction_answer", func(t *testing.T) {
		s, _, _, ix := answerService(t)
		ctx := t.Context()
		const conversation = "conv-assistant"
		if err := s.holdPushes(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		q := ix.Questions[0]
		if _, err := runTool(s, conversation, "interaction_answer", answerArgs(ix.ID, map[string]string{q.Question: q.Options[1].Label})); err != nil {
			t.Fatal(err)
		}
		if _, err := s.toolSupervisionFinish(ctx, conversation, silentSupervisionFinish); err != nil {
			t.Fatalf("interaction_answer changed the hold: %v", err)
		}
	})
}

func TestSupervisionHoldClearsBeforeFinishedPush(t *testing.T) {
	ft := newFleetTestOn(t, &memStore{})
	fastDone(ft)
	ctx := t.Context()
	work := proto.Session{MachineID: "mac", SessionID: "conv1", Assistant: true, Agent: "codex", Status: proto.StatusWorking, UpdatedAt: time.Now()}
	done := work
	done.Status, done.LastText = proto.StatusDone, "Routine check complete."
	if err := ft.s.holdPushes(ctx, work.SessionID); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", work)
	ft.sessions("mac", done)
	if ft.s.supervising(work.SessionID) {
		t.Fatal("the completed supervision hold is still active")
	}
	ft.s.fleet.completionRuns.Wait()
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != 0 {
		t.Fatalf("quiet supervision completion was pushed: %v", got)
	}
	ft.sessions("mac", work)
	done.LastText = "The requested result."
	ft.sessions("mac", done)
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second); len(got) != 1 || got[0].Body != done.LastText {
		t.Fatalf("ordinary completion push = %v", got)
	}
}

func TestSupervisionHoldClearFailurePreventsOwnerSendUntilRetry(t *testing.T) {
	ctx := t.Context()
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	if err := s.holdPushes(ctx, "conv1"); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("synthetic hold clear failure")
	st.setFailPut(writeErr)
	send := func() error {
		_, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "conv1",
			Params: map[string]any{"text": "Show the latest result."}})
		return err
	}
	if err := send(); !errors.Is(err, writeErr) || s.supervising("conv1") {
		t.Fatalf("owner send with an uncleared durable hold = %v", err)
	}
	st.setFailPut(nil)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newQuietServiceOn(t, st)
	if restarted.supervising("conv1") {
		t.Fatal("the cleared hold returned after retry and restart")
	}
}
