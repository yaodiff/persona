package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The settings book contains dispatches created before A2A. New dispatches
// retain the same start method and task card, and are tracked by the A2A row.
func (s *Service) dispatchTasks(ctx context.Context, ref a2aRef) ([]dispatchTask, bool, error) {
	tasks := s.dispatched(ctx)
	st, err := s.a2aStore()
	if err != nil {
		return tasks, false, nil
	}
	cfg := s.a2aConfig(ctx)
	if !s.a2aToolAllowed(ctx, cfg, ref) {
		return tasks, false, nil
	}
	caller := a2aSessionCaller(ref)
	rows, err := st.A2ADispatches(ctx, caller.Owner, ref.Machine, ref.Session, time.Now().Add(-dispatchKeep))
	if err != nil {
		return nil, false, err
	}
	child := false
	for _, row := range rows {
		task, err := decodeA2A(&row, nil)
		if err != nil {
			return nil, false, err
		}
		if task.Launch == nil {
			continue
		}
		if task.target() == ref {
			child = true
		}
		if task.Owner == caller.Owner && s.a2aTaskVisible(ctx, cfg, caller, task) {
			tasks = append(tasks, dispatchTask{FromMachine: ref.Machine, FromSession: ref.Session, Machine: task.TargetMachine,
				Session: task.TargetSession, Cwd: cmp.Or(task.Launch.Worktree, task.Launch.CWD), Title: task.Launch.Title, At: task.Created, a2a: task})
		}
	}
	return tasks, child, nil
}

type dispatchA2AStart struct {
	store a2aStore
	task  *a2aTask
}

// admitDispatchA2A answers a retry by requestID with its task, or admits a new dispatch and records it: one within the
// caller's limits, to a computer that is online, which dispatchStartRefusal did not refuse (refused, worked out before
// the lock so that no launcher list is read under it).
func (s *Service) admitDispatchA2A(ctx context.Context, from a2aRef, target, requestID, prompt string, launch a2aLaunch, now time.Time, refused error) (*dispatchA2AStart, bool, error) {
	s.dispatch.admit.Lock()
	defer s.dispatch.admit.Unlock()
	digest := sha256.Sum256([]byte(prompt))
	launch.PromptHash = hex.EncodeToString(digest[:])
	if requestID != "" {
		if st, err := s.a2aStore(); err == nil {
			caller := a2aSessionCaller(from)
			row, err := decodeA2A(st.A2ARequest(ctx, caller.Owner, requestID))
			if row != nil || err != nil {
				cfg := s.a2aConfig(ctx)
				if !s.a2aToolAllowed(ctx, cfg, from) || row != nil && !s.a2aTaskEndpointAllowed(ctx, cfg, row.target(), row) {
					return nil, false, errA2AUnavailable
				}
				launch.Dispatch, launch.Subagent = true, true
				row, err := a2aCheckRetry(row, err, a2aRef{Machine: target}, "", prompt, true, &launch)
				return &dispatchA2AStart{store: st, task: row}, true, err
			}
		}
	}
	if err := s.checkDispatchCapacity(ctx, from, now); err != nil {
		return nil, false, err
	}
	if dev, ok := s.machineByName(ctx, target); !ok || !dev.Online && !dev.Reachable {
		return nil, false, fmt.Errorf("%s is offline; nothing was started", target)
	}
	if refused != nil {
		return nil, false, refused
	}
	return s.prepareDispatchA2A(ctx, from, target, requestID, prompt, launch)
}

func (s *Service) checkDispatchCapacity(ctx context.Context, from a2aRef, now time.Time) error {
	tasks, child, err := s.dispatchTasks(ctx, from)
	if err != nil {
		return err
	}
	limit, active := s.hello(ctx).Agents.Dispatch.Limit(), 0
	for _, task := range tasks {
		child = child || task.Machine == from.Machine && task.Session == from.Session
		if task.FromMachine != from.Machine || task.FromSession != from.Session {
			continue
		}
		if task.a2a != nil && !a2aTerminal(task.a2a.State) || task.a2a == nil && s.dispatchWorking(ctx, task, now) {
			active++
		}
	}
	if child {
		return errors.New("you are a task another agent handed to this computer: do the work here (your own in-session " +
			"subagents are fine), a dispatched task does not dispatch; nothing was started")
	}
	if active >= limit {
		return fmt.Errorf("%d tasks you dispatched are still working (the limit is %d): read them with dispatch_read and "+
			"wait for one to stop; nothing was started", active, limit)
	}
	return nil
}

func (s *Service) prepareDispatchA2A(ctx context.Context, from a2aRef, target, requestID, prompt string, launch a2aLaunch) (*dispatchA2AStart, bool, error) {
	st, err := s.a2aStore()
	if err != nil {
		// Managers without the A2A store retain the legacy dispatch path.
		return nil, false, nil
	}
	caller := a2aSessionCaller(from)
	to := a2aRef{target, newStartSessionID()}
	cfg := s.a2aConfig(ctx)
	if !s.a2aToolAllowed(ctx, cfg, from) || !s.a2aEndpointAllowed(ctx, cfg, to, true) {
		return nil, false, errA2AUnavailable
	}
	if requestID == "" {
		requestID = randomHex(16)
	}
	if !a2aValidID(requestID) {
		return nil, false, errA2AInvalid
	}
	launch.Dispatch, launch.Pending, launch.Subagent = true, true, true
	now := time.Now().UTC()
	task := &a2aTask{
		A2ARow: db.A2ARow{ID: randomHex(16), Owner: caller.Owner, Request: requestID, Context: randomHex(16),
			TargetMachine: to.Machine, TargetSession: to.Session, SourceMachine: from.Machine, SourceSession: from.Session},
		a2aBody: a2aBody{Wake: true, Launch: &launch},
	}
	task.append(a2aEvent{ID: requestID, Kind: "message", Text: a2aCleanText(prompt), State: a2aSubmitted, Delivery: a2aSending, Attempted: now})
	task.Body, err = json.Marshal(task.a2aBody)
	if err != nil {
		return nil, false, err
	}
	row, err := decodeA2A(st.A2ACreate(ctx, task.A2ARow))
	row, err = a2aCheckRetry(row, err, to, "", prompt, true, &launch)
	if err != nil {
		return nil, false, err
	}
	s.recordA2ASubagent(row)
	return &dispatchA2AStart{store: st, task: row}, row.ID != task.ID, nil
}

func (s *Service) finishDispatchA2A(ctx context.Context, start *dispatchA2AStart, session proto.Session, attempted bool, launchErr error) {
	if start == nil {
		return
	}
	defer s.a2aSignal()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if attempted && (startResultUnknown(launchErr) || sendUnknown(launchErr) || launchErr == nil && session.SessionID == "") {
		if err := s.a2aReceipt(ctx, start.store, start.task.ID, 1, a2aUncertain); err != nil {
			s.logf("dispatch A2A %s: %v", start.task.ID, err)
		}
		return
	}
	why := session.StartError // a definite failure's reason, on the A2A task as the caller was told it
	if launchErr != nil {
		why = launchErr.Error()
	}
	for range 5 {
		task, err := decodeA2A(start.store.A2AGet(ctx, start.task.ID))
		if err == nil && task != nil && task.Launch.Pending {
			err = s.a2aLaunched(ctx, start.store, task, session, launchErr != nil || session.StartError != "", why)
		}
		if errors.Is(err, errA2AConflict) {
			continue
		}
		if err != nil {
			s.logf("dispatch A2A %s: %v", start.task.ID, err)
		}
		return
	}
}

func dispatchA2AStatus(t *a2aTask) string {
	if t.Disconnected && !a2aTerminal(t.State) {
		return "computer offline"
	}
	switch t.State {
	case a2aSubmitted, a2aWorking:
		return "working"
	case a2aInput:
		return "waiting for your input"
	case a2aAuth:
		return "waiting for the user"
	case a2aComplete:
		return "stopped"
	default:
		return t.State
	}
}

func dispatchA2AResult(task *a2aTask, head string) string {
	if task.State == a2aSubmitted && task.Events[0].Delivery == a2aUncertain {
		head += " The start or first message is awaiting confirmation."
	}
	for i := len(task.Events) - 1; i >= 0; i-- {
		e := task.Events[i]
		if e.Kind == "status" && e.State == task.State && e.Text != "" {
			head += "\n\n" + e.Text
			break
		}
	}
	return fmt.Sprintf("%s\n\nA2A task %s.", head, task.ID)
}

func (s *Service) readDispatchA2A(ctx context.Context, from a2aRef, task *a2aTask, wait time.Duration) (string, error) {
	st, err := s.a2aStore()
	if err != nil {
		return "", err
	}
	c := a2aSessionCaller(from)
	cfg := s.a2aConfig(ctx)
	deadline := time.Now().Add(wait)
	for dispatchA2AStatus(task) == "working" && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(min(dispatchPoll, time.Until(deadline))):
		}
		task, err = s.a2aGet(ctx, st, cfg, c, task.ID)
		if err != nil {
			return "", err
		}
	}
	name := task.TargetMachine
	if m, ok := s.machineByName(ctx, name); ok {
		name = m.Name
	}
	return dispatchA2AResult(task, fmt.Sprintf("Task %s on %s (%s): %s.", task.TargetSession,
		name, cmp.Or(task.Launch.Worktree, task.Launch.CWD), dispatchA2AStatus(task))), nil
}
