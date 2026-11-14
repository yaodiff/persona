package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A task start whose answer the link to its machine took with it (#7511): the hub's link to a computer can go
// half-open and come back a minute or two later (a proxy in the way, a network blink), and every start out on it
// fails with "rpc: connection closed" or "context deadline exceeded" although the computer is fine. The machine's own
// list is asked first (#6540); when it shows nothing, the start waits for the link to come back, asks the list again
// (until the node's own start would have run out of time, when the request may have reached it), and only then
// starts the agent once again. The wait follows the link (the machine back online), not a fixed
// delay. None of it counts against the launcher (recordStartFailure skips link failures and waits that ran out).

// taskReconnectWait bounds how long a task start waits for its machine's link to come back before its one retry
// (Service.taskRetry shortens it in tests); taskReconnectPoll is how often it looks.
var (
	taskReconnectWait = 3 * time.Minute
	taskReconnectPoll = time.Second
)

// reconnectWait is taskReconnectWait unless tests set taskRetry.
func (s *Service) reconnectWait() time.Duration {
	if s.taskRetry > 0 {
		return s.taskRetry
	}
	return taskReconnectWait
}

// startLinkLost is a task start whose answer was lost with the link to its machine and that the machine, when asked
// (after it came back: back), does not show: nothing says it started. runPlanTaskStart starts it once more when the
// machine is back.
type startLinkLost struct {
	err     error
	machine string // its name, for the failure's words
	back    bool   // the link came back within reconnectWait
}

func (e startLinkLost) Error() string { return e.err.Error() }
func (e startLinkLost) Unwrap() error { return e.err }

// startLinkDown: an unanswered start's link was the cause: it closed with the start out (or a hub on the way said
// so), or the wait ran out and the machine is not reachable from here now. A wait that ran out while the machine
// stayed connected is a node that did not answer in time (busy), not this.
func (s *Service) startLinkDown(ctx context.Context, machine string, err error) bool {
	if transientStartErr(err) {
		return true
	}
	return startWaitEnded(err) && !s.machineUp(context.WithoutCancel(ctx), machine)
}

// machineUp: the hub has machine connected now (online and reachable).
func (s *Service) machineUp(ctx context.Context, machine string) bool {
	ctx, cancel := context.WithTimeout(ctx, agentStartListTimeout)
	defer cancel()
	m, ok := s.machineByName(ctx, machine)
	return ok && m.Online && m.Reachable
}

// waitMachineBack waits until machine is connected again, at most reconnectWait; true when it is. ctx may be spent
// already (the start's own wait ran out on the dead link): the wait is bounded by reconnectWait alone.
func (s *Service) waitMachineBack(ctx context.Context, machine string) bool {
	limit := s.reconnectWait()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	tick := time.NewTicker(min(taskReconnectPoll, max(limit/8, time.Millisecond)))
	defer tick.Stop()
	for {
		if s.machineUp(ctx, machine) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// nodeStartBudget is how long a node may take over a session start before it gives up on it itself (the node's
// startWait: claude's start and ready waits and a margin, 4m30s by default), with some slack. A var for tests.
var nodeStartBudget = 5 * time.Minute

// startMayHaveArrived is how long after sentAt a start the link took may still be under way on its node: the node's
// own budget when the request was written (the link dropped with it out, or the wait ran out); zero for one that
// provably never left (closed before writing, no connection).
func startMayHaveArrived(err error, sentAt time.Time) time.Time {
	if errors.Is(err, rpc.ErrDropped) || startWaitEnded(err) {
		return sentAt.Add(nodeStartBudget)
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Message == rpc.ErrClosed.Error() {
		return sentAt.Add(nodeStartBudget) // a hub on the way: whether it was written there is not known
	}
	return time.Time{}
}

// linkFailureText is what a task's card says of a start that failed on the link or on a node that did not answer
// in time; "" for any other failure.
func (s *Service) linkFailureText(ctx context.Context, machine string, err error) string {
	lang := s.lang(ctx)
	var lost startLinkLost
	switch {
	case errors.As(err, &lost) && lost.back:
		return i18n.T(lang, "assistant-tasks.start_link_lost", i18n.Args{"machine": lost.machine, "reason": lost.err.Error()})
	case errors.As(err, &lost):
		return i18n.T(lang, "assistant-tasks.start_link_down", i18n.Args{"machine": lost.machine, "reason": lost.err.Error()})
	case startWaitEnded(err):
		return i18n.T(lang, "assistant-tasks.start_machine_busy", i18n.Args{"machine": s.machineName(machine), "reason": err.Error()})
	}
	return ""
}
