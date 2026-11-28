package assistant

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// A scheduled run waits for the main conversation to be ready (docs/assistant/onboarding.md 4.4 "Scheduled tasks"). A new hub has the
// weekday morning briefing on, and with no computer yet (or one asleep, Claude Code missing or signed out, a key that is wrong)
// every run failed and pushed "Scheduled task failed". Now a due run first looks at the readiness (no probe: nothing is spent,
// the node's two questions are cached): when what the run needs is not there, it is skipped and recorded as such,
// and nothing is pushed. The first run once it is ready again goes as usual, and the main conversation gets one line
// saying the tasks run again (a feed item of kind notice, written before that run is sent).
//
// Only what the run needs counts: a follow-up going into a conversation on another computer does not wait on the
// assistant's computer, and a task on a launcher of its own does not wait on the main conversation's Claude Code or
// model. "Run now" in the app is not held back: the user asked for it and sees what goes wrong. Nor is a one-off (a
// reminder, a follow-up the assistant set for itself): it is turned off once due, so a skip would lose it; it goes as
// before, and a failure is pushed and retried once.
//
// The wait is marked (and the notice written) only for a step the user has to set up or fix: a computer that is just
// asleep or offline skips the run too, but its waking is not news, and the notice would say something untrue.

// errNotReady: the run was skipped because the computer or the model is not set up yet.
var errNotReady = i18n.Err("assistant-schedule.not_ready_skipped")

// feedNotice is a line the hub writes into a conversation (ref is the to-do ID for a task notice; body: {"text"}).
const feedNotice = "notice"

// noticeBody is a notice's body.
type noticeBody struct {
	Text string `json:"text"`
}

// taskTarget is where the run of t would go: the machine and launcher, and whether it starts or continues the task's
// own conversation on the assistant's machine (own) rather than going into the conversation it came from.
func (s *Service) taskTarget(ctx context.Context, t Task, mainLauncher string) (machine, launcher string, own bool) {
	if t.continuesSource() {
		if _, ok := s.device(ctx, t.MachineID); ok {
			launcher = mainLauncher // a conversation that does not say runs on the main conversation's (readinessModel)
			if c, ok := s.session(ctx, t.MachineID, t.SessionID); ok && c.Launcher != "" {
				launcher = c.Launcher
			}
			return t.MachineID, launcher, false
		}
	}
	launcher = mainLauncher
	if b, err := s.resolveModel(ctx, "task."+t.ID); err == nil {
		launcher = strings.TrimPrefix(b.Provider, "launcher.")
	}
	return s.taskMachine(ctx, t), launcher, true
}

// readyFor reports whether the run of t can go by the readiness r: false, with the step, when a step that keeps the
// main conversation from answering is one the run needs too. A step of Claude Code or the model is of its launcher:
// the main conversation's next turn's, which is its kept pick's when that is on another launcher than Main chat's
// (readinessTurnLauncher), and a run on another launcher does not wait on it.
func (s *Service) readyFor(ctx context.Context, t Task, r Readiness) (bool, ReadinessStep) {
	if r.Mode == ReadinessFull {
		return true, ReadinessStep{}
	}
	machine, launcher, own := s.taskTarget(ctx, t, r.Launcher)
	onMain := r.Machine != nil && machine == r.Machine.ID
	for _, st := range r.Steps {
		if st.ID == StepFirstMessage || !readinessBlocks(st) {
			continue
		}
		switch {
		case st.ID == StepComputer && (own && t.ExecutionMachineID == "" || onMain):
			return false, st // with no computer at all the task's own conversation has nowhere to start
		case st.ID != StepComputer && onMain && launcher == cmp.Or(st.Launcher, r.Launcher):
			return false, st
		}
	}
	return true, ReadinessStep{}
}

// transientStep: a step that blocks for now but is not something to set up or fix (the computer asleep or offline).
func transientStep(st ReadinessStep) bool {
	return st.ID == StepComputer && st.Code == codeNodeUnreachable
}

// gateRun is what a scheduled (not manual) run checks before it is sent: an error (errNotReady) when it is to be
// skipped. Ready again after a skip, it writes the one-line notice into the main conversation first.
func (s *Service) gateRun(ctx context.Context, t Task) error {
	r := s.readiness(ctx, false, readinessWaitGet)
	if ok, st := s.readyFor(ctx, t, r); !ok {
		if !transientStep(st) {
			s.routinesWaiting(ctx, true)
		}
		return errNotReady
	}
	if r.Mode == ReadinessFull && s.routinesWaiting(ctx, false) {
		s.noteReady(ctx, t)
	}
	return nil
}

// routinesWaiting marks (on) that a run was skipped for not being ready, or (off) takes the mark: true when it was
// there and this call cleared it, so that only one of the runs going together writes the notice. Nothing is written
// when nothing changes: a working hub never touches the record here.
func (s *Service) routinesWaiting(ctx context.Context, on bool) bool {
	cur := s.readinessRecordNow(ctx).RoutinesWaiting
	if on == (cur != nil) {
		return false
	}
	took := false
	if err := s.readinessUpdate(ctx, func(rec *readinessRecord) {
		switch {
		case on && rec.RoutinesWaiting == nil:
			now := time.Now().UTC()
			rec.RoutinesWaiting = &now
		case !on && rec.RoutinesWaiting != nil:
			rec.RoutinesWaiting, took = nil, true
		}
	}); err != nil {
		s.logf("assistant: readiness: %v", err)
		return false
	}
	return took
}

// noteReady writes the notice that the scheduled tasks run again into the main conversation (none: nothing).
func (s *Service) noteReady(ctx context.Context, t Task) {
	main := s.settings(ctx).Main
	if main == "" {
		return
	}
	l := s.lang(ctx)
	text := i18n.T(l, "assistant-schedule.ready_again", i18n.Args{"name": t.labelIn(l)})
	if s.trackPolicy(ctx, t.Space).Redact { // the main conversation does not name a task of a space that redacts either (health.go)
		text = i18n.T(l, "assistant-schedule.ready_again_unnamed")
	}
	if _, err := s.appendFeed(ctx, main, feedNotice, "", noticeBody{Text: text}); err != nil {
		s.logf("assistant: ready notice: %v", err)
	}
}
