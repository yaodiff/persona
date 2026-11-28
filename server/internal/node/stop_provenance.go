package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func (s *stateFile) stopped(id string, cause proto.StopCause) {
	s.update(id, func(v *sessionState) bool {
		v.StopCause = cause
		v.StopCauseAt = max(time.Now().UnixNano(), v.StopCauseAt+1)
		return true
	})
}

// clearStopAfterAssistantInput retires the Stop or Exit from before an accepted input.
// An interrupt can be recorded before it takes the typing lock, so keep any newer mark.
func (s *stateFile) clearStopAfterAssistantInput(id string, previousStopAt int64) {
	s.update(id, func(st *sessionState) bool {
		if st.StopCauseAt != previousStopAt || (st.StopCause != proto.StopUser && st.StopCause != proto.StopExit) {
			return false
		}
		st.StopCause = proto.StopUnknown
		st.StopCauseAt = max(time.Now().UnixNano(), st.StopCauseAt+1)
		return true
	})
}

// A new running observation invalidates the previous stopped-session capability.
func (n *Node) observedRunning(id string) {
	release, ok := n.recovery.tryLock(id)
	if !ok {
		return // Never clear a user stop while its close/quit is still in progress.
	}
	defer release()
	n.recordRunning(id)
}

func (n *Node) recordRunning(id string) {
	now := time.Now()
	n.uncertainResumes.seen(id)
	n.state.update(id, func(v *sessionState) bool {
		changed := false
		if v.StopCause != proto.StopUnknown {
			v.StopCause = proto.StopUnknown
			v.StopCauseAt = max(now.UnixNano(), v.StopCauseAt+1)
			changed = true
		}
		if now.Unix()-v.RanAt >= int64(ranAtEvery/time.Second) {
			v.RanAt = now.Unix()
			changed = true
		}
		return changed
	})
}

// ranAtEvery: how often a running Agent's RanAt is written; restartWindow: how long before this node started an Agent
// may last have been seen running for its end to count as the restart's (markInterrupted). A quick restart or upgrade
// falls inside it; a machine that was off for longer leaves the cause unknown.
const (
	ranAtEvery    = 5 * time.Minute
	restartWindow = 2 * ranAtEvery
)

// state.put is one-way. Verify the cause before closing or consuming a stopped
// capability: a failed save must not revive old recovery permission after a restart.
func (n *Node) saveStopCause(ctx context.Context, id string, cause proto.StopCause) error {
	if cause == proto.StopUser {
		n.noteAgyStopped(id) // an agy pane's conversation is listed as stopped next (agystopped.go)
	}
	n.state.stopped(id, cause)
	want, known := n.state.get(id)
	if !known || n.peer == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var saved struct {
		Sessions map[string]sessionState `json:"sessions"`
	}
	err := n.peer.Call(cctx, "state.load", map[string]any{}, &saved)
	got, ok := saved.Sessions[id]
	if err != nil || !ok || got.StopCause != cause || got.StopCauseAt != want.StopCauseAt {
		return refusal("stop_not_saved", "The stop state could not be saved. Nothing was started, stopped or closed; try again.", nil)
	}
	return nil
}

func (n *Node) stopCause(id string) proto.StopCause {
	st, _ := n.state.get(id)
	return st.StopCause
}

// codexHistoryStopCause reports the stop provenance for a resumable Codex history row. Any saved state (including an
// explicit user stop or a previously consumed resume) is authoritative; only an idle, unowned Codex App thread Persona
// never owned before whose rollout ended its turn cleanly is eligible for supervised resume (#13772).
func (n *Node) codexHistoryStopCause(id string, src codexSource, m quickMeta, resumable bool) proto.StopCause {
	if st, ok := n.state.get(id); ok {
		return st.StopCause
	}
	if resumable && codexAppOrigin(src) && src.path != "" && m.turnClosed && !m.turnOpen {
		return proto.StopTaskCompleted
	}
	return proto.StopUnknown
}

// effectiveStopCause returns stopCause, or StopTaskCompleted for an idle, unowned Codex App thread that Persona has
// never owned before and that is eligible for terminal recovery right now.
func (n *Node) effectiveStopCause(id string) proto.StopCause {
	if st, ok := n.state.get(id); ok {
		return st.StopCause
	}
	src, ok := n.codexSource(id)
	if !ok || src.path == "" || !codexAppOrigin(src) {
		return proto.StopUnknown
	}
	st, err := os.Stat(src.path)
	if err != nil {
		return proto.StopUnknown
	}
	m := n.meta(src.path, st)
	return n.codexHistoryStopCause(id, src, m, n.codexResumable(context.Background(), id))
}

// noteGatewayRunning keeps a live observation after a gateway error even inside recordRunning's five-minute
// write interval. A transcript found only after its terminal disappeared is not evidence of a non-user stop.
func (n *Node) noteGatewayRunning(s *proto.Session) {
	kind, _ := transcript.GatewayErrorKind(s.TurnError, s.UpdatedAt)
	if kind == "" || (s.TurnErrorKind != "" && s.TurnErrorKind != kind) || s.UpdatedAt.IsZero() || s.UpdatedAt.After(time.Now()) {
		return
	}
	release, ok := n.recovery.tryLock(s.SessionID)
	if !ok {
		return
	}
	defer release()
	n.state.update(s.SessionID, func(st *sessionState) bool {
		if st.StopCause != proto.StopUnknown || st.Pane == "" || st.Pane != s.PaneID ||
			s.UpdatedAt.UnixNano() <= st.StopCauseAt || st.RanAt >= s.UpdatedAt.Unix() {
			return false
		}
		st.RanAt = time.Now().Unix()
		return true
	})
}

// noteGatewayStop requires both a live observation after the recognized error and the original, retained Persona
// terminal. An explicit stop, a closed pane, unobserved history or a consumed resume cannot grant recovery.
func (n *Node) noteGatewayStop(s *proto.Session) {
	kind, _ := transcript.GatewayErrorKind(s.TurnError, s.UpdatedAt)
	if s.TurnErrorKind != "" && s.TurnErrorKind != kind {
		return // Structured authentication/model failures are never gateway recovery permission.
	}
	var cause proto.StopCause
	switch kind {
	case proto.TurnErrorUsageLimit:
		cause = proto.StopUsageLimit
	case proto.TurnErrorTransient:
		cause = proto.StopTransientError
	default:
		return
	}
	release, ok := n.recovery.tryLock(s.SessionID)
	if !ok {
		return
	}
	defer release()
	if _, live := n.agent(s.SessionID); live {
		return
	}
	pane, kept := n.keptPane(s.SessionID)
	if !kept {
		return
	}
	n.state.update(s.SessionID, func(st *sessionState) bool {
		if st.StopCause != proto.StopUnknown || s.UpdatedAt.IsZero() || s.UpdatedAt.UnixNano() <= st.StopCauseAt ||
			st.Pane != pane.PaneID || st.RanAt <= 0 || st.RanAt < s.UpdatedAt.Unix() {
			return false
		}
		st.StopCause, st.StopCauseAt = cause, max(time.Now().UnixNano(), st.StopCauseAt+1)
		return true
	})
}

// Called again under the session's recovery lock: a cached assistant row must not
// undo a user close that happened after that row was listed.
func (n *Node) supervisorResumeAllowed(id, mode, launcher string) error {
	cause := n.effectiveStopCause(id)
	if !validID(id) || !cause.SupervisorMayResume() {
		return refusal("resume_not_allowed", fmt.Sprintf("Supervisor resume is refused: stop cause is %s. Use the owner's Resume.", cause), nil)
	}
	if mode != "" || launcher != "" {
		return refusal("resume_not_allowed", "Supervisor resume must keep the session's original launcher and permission mode.", nil)
	}
	if _, live := n.agent(id); live {
		return refusal("resume_not_allowed", "The session is running again; refresh it before sending.", nil)
	}
	if cause == proto.StopUnknown {
		// A Codex thread archived in Codex itself was put away there, likely by the user: as an Agent archived in
		// Persona (the hub's resumeAllowed), only a recorded non-user stop lets the assistant restore and resume it.
		if home, _, ok := n.codexThread(id); ok && codexArchived(home, n.codexPath(id)) {
			return refusal("resume_not_allowed", "Supervisor resume of an archived Codex session needs a recorded "+
				"non-user stop; stop cause is unknown. Use the owner's Resume.", nil)
		}
	}
	if cause == proto.StopUnknown && n.uncertainResumes.settling(id, time.Now()) {
		// An earlier start of it ended uncertain (a line typed into a shell, a stalled Agent, an unanswered call): it may
		// still come up, and a second resume must not start it twice (#8214).
		return refusal("resume_not_allowed", fmt.Sprintf("An earlier resume of the session may still be starting; "+
			"look again in %s, or check its terminal.", resumeSettle), nil)
	}
	if cause == proto.StopUsageLimit || cause == proto.StopTransientError {
		st, _ := n.state.get(id)
		pane, kept := n.keptPane(id)
		if !kept || st.Pane != pane.PaneID {
			return refusal("resume_not_allowed", "The original terminal was closed or changed; use the owner's Resume.", nil)
		}
	}
	return nil
}

// startFailedForSure: a start's error says nothing of it ran (a refusal with delivered none) and no tab was kept for
// it (withKeptTab). Any other failure (a line typed into a shell, a stalled agent, an unanswered call) may still bring
// the Agent up, and a second resume must not start it twice: those stay unknown (#8214).
func startFailedForSure(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || deliveredOf(re) != deliveredNone {
		return false
	}
	if len(re.Data) > 0 {
		var d struct {
			Pane      string `json:"pane"`
			KeptUntil string `json:"kept_until"`
		}
		if json.Unmarshal(re.Data, &d) == nil && (d.Pane != "" || d.KeptUntil != "") {
			return false
		}
	}
	return true
}

// noteResumeFailed records the assistant's resume of session id that ended in err with the Agent not running as
// resume_failed: stopped by Persona's own step, which the assistant may try again, never unknown (#11441). Nothing
// when the resume went (err nil) or the Agent runs after all.
func (n *Node) noteResumeFailed(id string, err error) {
	if err == nil || !startFailedForSure(err) {
		return // an uncertain start may still bring it up: a second resume must not start it twice (unknown, #8214)
	}
	if _, live := n.agent(id); live {
		return
	}
	n.state.stopped(id, proto.StopResumeFailed)
	n.logf("session %s: the assistant's resume failed: %v", short(id), err)
}

// resumeSettle: how long after a resume of a session whose outcome was uncertain the assistant's next resume of it is
// refused. The supervisor may resume an Agent whose stop cause is unknown, so the old guard (a resume consumes its
// stop to unknown, which refused the next one) no longer keeps two starts apart; this does, for as long as a slow
// start may take to show. After it an Agent that is not running is stopped, whatever that start did.
const resumeSettle = 5 * time.Minute

// resumeStarts remembers, in memory, the resumes whose outcome was uncertain. A node that starts again forgets them:
// by then its listing shows whether the Agent came up.
type resumeStarts struct {
	mu sync.Mutex
	at map[string]time.Time
}

// begin notes a resume of id starting at now. done ends it: a start that ran nothing for sure lets the next resume go
// at once. Any other is kept until the Agent is seen running (seen), a stop cause is recorded after it (the node's
// guard reads only an unknown cause as unsettled), or resumeSettle passes.
func (r *resumeStarts) begin(id string, now time.Time) (done func(err error)) {
	r.mu.Lock()
	if r.at == nil {
		r.at = map[string]time.Time{}
	}
	for k, t := range r.at {
		if now.Sub(t) >= resumeSettle {
			delete(r.at, k)
		}
	}
	r.at[id] = now
	r.mu.Unlock()
	return func(err error) {
		if err == nil || !startFailedForSure(err) {
			return
		}
		r.mu.Lock()
		if r.at[id].Equal(now) {
			delete(r.at, id)
		}
		r.mu.Unlock()
	}
}

// seen: id's Agent was seen running, so whatever start brought it up has settled.
func (r *resumeStarts) seen(id string) {
	r.mu.Lock()
	delete(r.at, id)
	r.mu.Unlock()
}

// settling: a resume of id started less than resumeSettle before now and its outcome was uncertain.
func (r *resumeStarts) settling(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.at[id]
	return ok && now.Sub(t) < resumeSettle
}
