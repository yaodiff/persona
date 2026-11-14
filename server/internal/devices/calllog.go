package devices

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The hub's calls to a node are its hot path (a task's session.start, the apps' reads): until #7509 none of them
// left a line in the log, and a start that failed with "context deadline exceeded" or "rpc: connection closed" could
// not be told apart from a busy node afterwards. Every call that fails is logged with its machine, method, time and
// the kind of failure; a call that worked only when it is slow, a session start, or one in callLogSample.

// Result kinds of a call, as the log line's result= field says them.
const (
	callOK       = "ok"
	callDeadline = "deadline" // the caller's wait ran out (or the node answered with those words)
	callCanceled = "canceled" // the caller stopped waiting
	callClosed   = "closed"   // the connection closed with the call out, or before it could be written
	callOffline  = "offline"  // no connection to the machine to send it on
	callError    = "error"    // the node answered with an error, or the hub refused before sending (an outdated node)
)

// callSlow: a call that worked but took at least this long is logged; a canceled one only after this long (a caller
// giving up on a quick call — a watcher ending — is routine).
const callSlow = 2 * time.Second

// callLogSample: one in this many calls to a machine that worked quickly is logged anyway, so the log shows the
// traffic too.
const callLogSample = 200

// callResult classifies err for the log.
func callResult(err error) string {
	var re *rpc.Error
	switch {
	case err == nil:
		return callOK
	case errors.Is(err, context.DeadlineExceeded):
		return callDeadline
	case errors.Is(err, context.Canceled):
		return callCanceled
	case errors.Is(err, errOffline):
		return callOffline
	case errors.Is(err, rpc.ErrClosed), errors.Is(err, rpc.ErrSuspended):
		return callClosed
	case errors.As(err, &re) && re.Code == "" && re.Message == context.DeadlineExceeded.Error():
		return callDeadline // a hub or connector further on relayed its own wait running out
	case errors.As(err, &re) && re.Code == "" && re.Message == rpc.ErrClosed.Error():
		return callClosed
	}
	return callError
}

// callOfflineEvery: calls to a machine with no connection fail at once, and the hub's own rounds (keep sessions, usage,
// watches) make them all the time while it is away: one line per machine this often, saying how many were not logged.
// A session start is always logged.
const callOfflineEvery = time.Minute

// shouldLogCall: failures always (a quick cancel excepted, offline ones rate-limited); successes when slow, for a
// session start, or sampled. suppressed is how many offline lines were not logged before this one.
func (m *Machine) shouldLogCall(method, result string, took time.Duration) (ok bool, suppressed int) {
	switch result {
	case callOK:
		return took >= callSlow || devicewire.IsSessionStart(method) || m.callCount.Add(1)%callLogSample == 0, 0
	case callCanceled:
		return took >= callSlow, 0
	case callOffline:
		if devicewire.IsSessionStart(method) {
			return true, 0
		}
		m.callLogMu.Lock()
		defer m.callLogMu.Unlock()
		if now := time.Now(); now.Sub(m.offlineLogged) >= callOfflineEvery {
			n := m.offlineSkipped
			m.offlineLogged, m.offlineSkipped = now, 0
			return true, n
		}
		m.offlineSkipped++
		return false, 0
	}
	return true, 0
}

// logCall writes one line for a finished call: key=value fields, the error last and quoted.
//
//	[m1] rpc call method=session.start result=closed took=1m2.3s via=ssh sent=true tries=1 err="rpc: connection closed"
func (m *Machine) logCall(method string, err error, took time.Duration, tries int) {
	result := callResult(err)
	ok, suppressed := m.shouldLogCall(method, result, took)
	if !ok {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] rpc call method=%s result=%s took=%s", m.cfg.ID, method, result, took.Round(time.Millisecond))
	if via := m.transportNow(); via != "" {
		fmt.Fprintf(&b, " via=%s", via) // the connection now: a call cut off by a reconnect shows the new one's
	}
	switch {
	case errors.Is(err, rpc.ErrDropped): // out on the connection when it closed: the node may have done it
		b.WriteString(" sent=true")
	case errors.Is(err, rpc.ErrClosed), errors.Is(err, rpc.ErrSuspended): // never written
		b.WriteString(" sent=false")
	}
	if suppressed > 0 {
		fmt.Fprintf(&b, " offline_not_logged=%d", suppressed) // since the last offline line of this machine
	}
	if tries > 1 {
		fmt.Fprintf(&b, " tries=%d", tries)
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Code != "" {
		fmt.Fprintf(&b, " code=%s", re.Code)
	}
	if err != nil {
		fmt.Fprintf(&b, " err=%q", oneLineErr(err))
	}
	m.d.logf("%s", b.String())
}

// logCutOff is the peer's OnClose: the calls a closing connection took with it, which fail with only "connection
// closed" to say.
//
//	[m1] connection over ssh closed with 2 call(s) out: ping, session.start (reason: "node exited")
func (m *Machine) logCutOff(via string, err error, inflight []string) {
	if len(inflight) == 0 {
		return
	}
	why := "closed"
	if err != nil {
		why = oneLineErr(err)
	}
	m.d.logf("[%s] connection over %s closed with %d call(s) out: %s (reason: %q)", m.cfg.ID, via, len(inflight), strings.Join(inflight, ", "), why)
}

// oneLineErr is err's text on one line, at most 300 bytes: an ssh error can carry a banner.
func oneLineErr(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
