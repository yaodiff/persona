package briefing

import (
	"cmp"
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

// The `brief` item of a routine's conversation (docs/protocol/protocol.md "Tasks and the chat feed"): the facts of a
// run with facts on, written when the run is sent whether or not the Lark bot is on, so the apps show the same fixed
// part as the Lark card.

// Body is the facts as a `brief` item's JSON body (byte-identical to historical briefBody).
func (r *runner) Body(f *modapi.BriefSnapshot, title string) modapi.BriefBody {
	if f == nil {
		return modapi.BriefBody{}
	}
	b := modapi.BriefBody{
		Contributions:    f.Contributions,
		Confirmations:    f.Confirmations,
		Title:            title,
		At:               f.At,
		Hours:            int(Window / time.Hour),
		WaitingUnknown:   f.WaitingUnknown,
		DoneTotal:        f.DoneTotal,
		Failed:           f.Failed,
		FailedTotal:      f.FailedTotal,
		Waiting:          []modapi.BriefBodyRow{},
		Done:             []modapi.BriefBodyDone{},
		Accept:           []modapi.BriefBodyRow{},
		ProgressTotal:    f.ProgressTotal,
		Progress:         []modapi.BriefBodyProgress{},
		Directions:       f.Directions,
		DisabledRoutines: f.DisabledRoutines,
	}
	b.DeferredSupervision = f.DeferredSupervision
	if b.Failed == nil {
		b.Failed = []modapi.BriefFailure{}
	}
	if b.Confirmations.Sources == nil {
		b.Confirmations.Sources = []modapi.BriefConfirmationCount{}
	}
	lines := f.NorthStarLines
	if lines == nil {
		lines = f.NorthStar.Lines
	}
	if lines == nil {
		lines = []string{}
	}
	b.NorthStar = modapi.BriefBodyNorthStar{
		Lines:                  lines,
		Interruptions:          f.NorthStar.Interruptions,
		InterruptionsYesterday: f.NorthStar.InterruptionsYesterday,
		Partial:                f.NorthStar.Partial,
		Availability:           f.NorthStar.Availability,
		AvailabilityYesterday:  f.NorthStar.AvailabilityYesterday,
		Attempts:               f.NorthStar.Attempts,
		OK:                     f.NorthStar.OK,
	}
	row := func(x modapi.BriefBoardRow) modapi.BriefBodyRow {
		return modapi.BriefBodyRow{
			ID:      x.ID,
			Title:   x.Summary,
			From:    x.From,
			Link:    x.Link,
			WaitedS: int64(x.Waited / time.Second),
			Nudged:  x.Nudged,
		}
	}
	// A row of what waits in a chat: the chat it came from, for the apps to open (the tasks to verify open their to-do).
	chatRow := func(x modapi.BriefBoardRow) modapi.BriefBodyRow {
		out := row(x)
		if x.Session != "" {
			out.MachineID, out.SessionID = x.Machine, x.Session
		}
		return out
	}
	for _, x := range f.Waiting {
		b.Waiting = append(b.Waiting, chatRow(x))
	}
	for _, x := range f.Accept {
		out := row(x)
		out.Title, out.Detail, out.AskedAt = x.ID, cmp.Or(x.Detail, f.Changed[x.ID]), x.Asked
		out.EvidenceLink = x.EvidenceLink
		if a, ok := f.Asks[x.ID]; ok {
			out.Verdict, out.Outcome = a.Verdict, a.Outcome
			if a.Decide {
				out.Recommend = a.Recommend
			}
		}
		if r.todos != nil {
			if title, ok := r.todos.TodoTitle(x.ID); ok {
				out.Title = title
			}
		}
		if out.Detail == out.Verdict || out.Detail == out.Title {
			out.Detail = ""
		}
		b.Accept = append(b.Accept, out)
	}
	for _, x := range f.Later {
		b.Later = append(b.Later, chatRow(x))
	}
	b.LaterTotal = f.LaterTotal
	if f.Setup != nil {
		b.Setup = f.Setup.Suggestions
	}
	for _, d := range f.Done {
		b.Done = append(b.Done, modapi.BriefBodyDone{ID: d.ID, Title: d.Title, At: d.At})
	}
	for _, p := range f.Progress {
		b.Progress = append(b.Progress, modapi.BriefBodyProgress{ID: p.ID, Title: p.Title, Stage: p.Stage, StageKind: p.StageKind})
	}
	if r.todos != nil {
		briefCtx, briefCancel := context.WithTimeout(context.Background(), 10*time.Second)
		for _, fup := range r.todos.StalePRFollowups(briefCtx, f.At, f.Lang) {
			if fup.Title == "" {
				fup.Title = fup.TodoID
				if t, ok := r.todos.TodoTitle(fup.TodoID); ok {
					fup.Title = t
				}
			}
			b.PRFollowups = append(b.PRFollowups, fup)
		}
		briefCancel()
	}
	return b
}

// writeFeed writes a run's facts into its conversation, with the time it was sent as its time: after the run's ⏰ message.
func (r *runner) writeFeed(run modapi.BriefScheduleRun, title string, f *modapi.BriefSnapshot) {
	if r.feed == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Use the scheduled occurrence rather than the process-local start time: a
	// redelivery after restart must resolve to the same chat entry.
	id := "feed_brief_run_" + FallbackID(run.RefID, run.Due)
	if err := r.feed.AppendOnce(ctx, id, run.SessionID, modapi.FeedKindBrief, run.RefID, r.Body(f, title)); err != nil {
		r.logf("brief: the feed item of %s: %v", run.RefID, err)
		return
	}
	r.supervisionBriefed(ctx, f)
}

func (r *runner) supervisionBriefed(ctx context.Context, f *modapi.BriefSnapshot) {
	if r.todos == nil || f == nil || len(f.DeferredSupervision) == 0 {
		return
	}
	ids := make([]string, 0, len(f.DeferredSupervision))
	for _, row := range f.DeferredSupervision {
		ids = append(ids, string(row.ID))
	}
	r.todos.SupervisionBriefed(ctx, ids, f.At)
}
