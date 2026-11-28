package assistant

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func nativeLoopTestPage(t *testing.T, i int, fire, end time.Time, reply string) []proto.Item {
	t.Helper()
	page := turnPage(fmt.Sprintf("loop-%d", i), reply, true)
	start := transcript.ParseLine(i+1, fmt.Appendf(nil, `{"type":"system","subtype":"scheduled_task_fire","uuid":"fire-%d","taskId":"job-1","timestamp":%q}`, i, fire.Format(time.RFC3339Nano)), &transcript.Meta{})
	if len(start) != 1 || start[0].NativeLoopID != "job-1" {
		t.Fatalf("native wake missing: %+v", start)
	}
	page[0] = start[0]
	page[1].TS, page[2].TS = end, end
	return page
}

func TestNativeLoopNoopSignalRequiresBothThresholds(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	ctx := t.Context()
	row := turnRow(proto.StatusIdle, 0, "noop")
	at := todo.CreatedAt.Add(time.Minute)
	for i, offset := range []time.Duration{0, 30, 60, 90, 179, 179, 180, 210} {
		fire := at.Add(offset*time.Minute + time.Duration(i)*2*time.Millisecond)
		end := fire.Add(time.Millisecond)
		items := nativeLoopTestPage(t, i, fire, end, "noop")
		closed, named := turnEndOf(items)
		row.Rev++
		turn, again := s.takeTurnAs(ctx, todo, row, items, closed, named, fire, turnLoopNoop, "", "same evidence")
		if again || turn.LoopNoop == nil || turn.LoopNoop.Count != i+1 {
			t.Fatalf("round %d was miscounted: %+v", i+1, turn.LoopNoop)
		}
		if replay, again := s.takeTurnAs(ctx, todo, row, items, closed, named, fire, turnLoopNoop, "", "same evidence"); !again || replay.LoopNoop.Count != i+1 {
			t.Fatalf("round %d replay was counted: %+v", i+1, replay.LoopNoop)
		}
		if signal, ok := s.nativeLoopNoopSignal(ctx, todo); ok != (i >= 6) || ok && signal.Kind != sigLoopNoop {
			t.Fatalf("wrong threshold at round %d and %v: %+v, %v", i+1, end.Sub(at), signal, ok)
		}
	}
	fire := at.Add(4 * time.Hour)
	items := nativeLoopTestPage(t, 8, fire, fire.Add(time.Second), "The deployment completed.")
	closed, named := turnEndOf(items)
	row.Rev++
	s.takeTurn(ctx, todo, row, items, closed, named, fire, turnDone)
	if signal, ok := s.nativeLoopNoopSignal(ctx, todo); ok {
		t.Fatalf("progress did not retire the streak: %+v", signal)
	}
}

func TestNativeLoopNoopKeepsParkingAndDeadline(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := t.Context()
	at := time.Now().UTC().Add(-time.Minute)
	until := at.Add(2 * time.Hour)
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewParked
		v.Reviews = append(v.Reviews, TaskReview{At: at, Until: until, Verdict: reviewParked, Parks: maxReparks + 1})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	working := turnRow(proto.StatusWorking, 1, "")
	working.Loop = &proto.LoopInfo{Kind: "cron"}
	f.setSessions("box", working)
	s.taskWorking(ctx, "box", "s1", startRow{Rev: working.Rev, At: working.UpdatedAt, NativeLoop: true})
	checkPark := func() {
		t.Helper()
		current := todoNow(s, todo.ID)
		park := current.Supervision.parking()
		if park == nil || !park.ParkEndedAt.IsZero() || park.Parks != maxReparks+1 || !park.Until.Equal(until) {
			t.Fatalf("native wake changed the park: %+v", current.Supervision)
		}
		if g, ok := parkedDueSignal(current, until); !ok || g.Kind != sigParkedDue {
			t.Fatalf("original deadline is no longer actionable: %+v", g)
		}
	}
	checkPark()
	fire := time.Now().UTC()
	f.setItems(nativeLoopTestPage(t, 0, fire, fire.Add(time.Second), "**noop — the build is still pending.**\n\n### Persona 工作报告\n- Status: done\n- Merged: n/a\n- Deployed: n/a\n- Waiting: The pending build\n- Artifact: file /tmp/checks.txt\n\n---")...)
	done := turnRow(proto.StatusIdle, 2, "noop")
	done.Loop = working.Loop
	f.setSessions("box", done)
	if got := s.settleTurn(ctx, "box", "s1", ""); got != settleTaken {
		t.Fatalf("native turn not handled: %v", got)
	}
	if turn, ok := s.turnOf(ctx, todo.ID); !ok || turn.State != turnLoopNoop {
		t.Fatalf("native turn not recorded as no-change: %+v", turn)
	}
	checkPark()
}

func TestNativeLoopNoopSortsWithRoutineSignals(t *testing.T) {
	entries := sortEntries([]prompts.SuperviseEntry{
		{Kind: sigLoopNoop},
		{Kind: sigDecide},
		{Kind: sigReview},
		{Kind: sigNoProgress},
	})
	var kinds []string
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	want := []string{sigDecide, sigReview, sigLoopNoop, sigNoProgress}
	if !slices.Equal(kinds, want) {
		t.Fatalf("digest order = %v, want %v", kinds, want)
	}
}
