package assistant

import (
	"maps"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type reworkReviewerKey struct{}

// A supervisor's attempt to resume one of its task's Agents. A node update or closing the task can retire the
// attempt even while the hub is still reading an archived session's stop cause.
type resumeAttempt struct {
	todo, reviewer string
	agent          SessionRef
}

// The session list may change while a supervisor fetches it or checks an archived session's stop cause.
type resumeReview struct {
	reviewer string
	listedAt time.Time
}

func beginResumeReview(reviewer string) resumeReview {
	return resumeReview{reviewer: reviewer, listedAt: time.Now()}
}

func (b *todoBook) resumeBindingLocked(machine, session string) (*Todo, *TodoAgent) {
	t := b.todos[b.activeBySession[[2]string{machine, session}]]
	if t != nil && t.DoneAt == nil {
		for i := range t.Agents {
			a := &t.Agents[i]
			if a.MachineID == machine && a.SessionID == session && a.active() {
				return t, a
			}
		}
	}
	return nil, nil
}

// Drop retired evidence in one pass per node list, independently of how many live Agents it contains.
func (b *todoBook) pruneResumeChecksLocked(now time.Time) {
	maps.DeleteFunc(b.refusedResumes, func(r resumeAttempt, at time.Time) bool {
		t, a := b.resumeBindingLocked(r.agent.MachineID, r.agent.SessionID)
		return a == nil || t.ID != r.todo || at.Before(a.LinkedAt) || !at.After(a.resumeInvalidatedAt) || now.Sub(at) > decisionRecheckAfter
	})
}

func (b *todoBook) noteResumeCheck(sender string, review resumeReview, machine string, c proto.Session, ownerOnly bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, a := b.resumeBindingLocked(machine, c.SessionID)
	if a == nil || othersTask(c, *t, sender) != nil || review.listedAt.Before(a.LinkedAt) || !review.listedAt.After(a.resumeInvalidatedAt) {
		return
	}
	if !ownerOnly {
		a.resumeInvalidatedAt = review.listedAt
	}
	b.pruneResumeChecksLocked(time.Now())
	if !ownerOnly {
		return
	}
	if b.refusedResumes == nil {
		b.refusedResumes = make(map[resumeAttempt]time.Time)
	}
	attempt := resumeAttempt{todo: t.ID, reviewer: review.reviewer, agent: SessionRef{MachineID: machine, SessionID: c.SessionID}}
	if review.listedAt.After(b.refusedResumes[attempt]) {
		b.refusedResumes[attempt] = review.listedAt
	}
}

func (b *todoBook) observeResumedSessions(d DeviceSessions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for i := range d.Sessions {
		c := &d.Sessions[i]
		_, a := b.resumeBindingLocked(d.MachineID, c.SessionID)
		if a == nil || c.Archived || stoppedAgent(*c) {
			continue
		}
		switch c.Status {
		case proto.StatusWorking, proto.StatusIdle, proto.StatusDone, proto.StatusBlocked:
		default:
			if c.Capabilities == nil || !c.Capabilities.Send {
				continue
			}
		}
		a.resumeInvalidatedAt = now
	}
	b.pruneResumeChecksLocked(now)
}

// takeResumeTerminal consumes confirmed refusals for this review. Only one current binding can identify the target.
func (b *todoBook) takeResumeTerminal(id, reviewer string, now time.Time) *SessionRef {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.todos[id]
	if t == nil || t.DoneAt != nil {
		return nil
	}
	b.pruneResumeChecksLocked(now)
	var target *SessionRef
	var count int
	for attempt := range b.refusedResumes {
		if attempt.todo != id || attempt.reviewer != reviewer {
			continue
		}
		delete(b.refusedResumes, attempt)
		target = &attempt.agent
		count++
	}
	if count == 1 {
		return target
	}
	return nil
}
