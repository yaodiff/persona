package extmgr

// A Cua Driver session that ended (#7590). Cua Driver ends a named session after cuaIdleTTL without a call, and when
// the user presses Stop on the bar it shows on the Mac's screen ("You're in control / Return to agent / Stop"). Either
// way it refuses every later call under that label ("session has ended; … Call start_session …") until start_session
// starts it again, and nothing it says tells the two apart: 0.38.0 refuses get_session on it too and keeps no reason.
// What the manager knows of its own calls does: a label it used less than cuaIdleTTL ago cannot have idled out, so it
// was ended on purpose, by the user's Stop as far as Persona can tell. That one is not started again behind the
// user's back: the model is told to ask, and once the owner speaks in the conversation again the next call starts it.
// One that idled out (or anything Persona cannot tell from it once it has run for cuaIdleTTL) is started again and the
// call retried once. Labels with no conversation (the manager's own reads, persona-apps) are no one's to stop.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// cuaIdleTTL is how long Cua Driver keeps a named session without a call (0.38.0, measured: gone after about 300 s).
const cuaIdleTTL = 5 * time.Minute

// cuaBookStart is when this process began keeping the book: before it, nothing is known of any label.
var cuaBookStart = time.Now()

// cuaBookKeep is how long a label's entry is kept after its last use, stop and the owner's last word (after that it
// is as unknown as one never seen).
const cuaBookKeep = 24 * time.Hour

// cuaSessionBook is what the manager knows of the Cua Driver session labels it calls under.
type cuaSessionBook struct {
	mu sync.Mutex
	// start is when the book began (zero: cuaBookStart); tests move it.
	start time.Time
	// labels by machine id and label (cuaKey).
	labels map[string]*cuaLabel
	// spoke: when the owner last spoke in the conversation whose label it is, by label (any machine).
	spoke map[string]time.Time
}

// cuaLabel is one label on one Mac.
type cuaLabel struct {
	used    time.Time // the last call Cua Driver took under it (any answer but "session has ended")
	stopped time.Time // when a call found it ended on purpose and the model was told; zero: not stopped
}

// cuaVerdict is what to do about a label found ended.
type cuaVerdict int

const (
	cuaRestart cuaVerdict = iota // start it again and retry the call once
	cuaStopped                   // the user's Stop: tell the model, do not start it again
)

func cuaKey(machine, label string) string { return machine + "\x00" + label }

func (b *cuaSessionBook) began() time.Time {
	if b.start.IsZero() {
		return cuaBookStart
	}
	return b.start
}

// labelLocked is machine's label, made when new; b.mu is held.
func (b *cuaSessionBook) labelLocked(machine, label string, now time.Time) *cuaLabel {
	if b.labels == nil {
		b.labels = map[string]*cuaLabel{}
	}
	k := cuaKey(machine, label)
	l := b.labels[k]
	if l == nil {
		b.pruneLocked(now)
		l = &cuaLabel{}
		b.labels[k] = l
	}
	return l
}

// pruneLocked drops what is older than cuaBookKeep; b.mu is held.
func (b *cuaSessionBook) pruneLocked(now time.Time) {
	for k, l := range b.labels {
		if now.Sub(l.used) > cuaBookKeep && now.Sub(l.stopped) > cuaBookKeep {
			delete(b.labels, k)
		}
	}
	for k, at := range b.spoke {
		if now.Sub(at) > cuaBookKeep {
			delete(b.spoke, k)
		}
	}
}

// used notes that Cua Driver took a call under machine's label at now.
func (b *cuaSessionBook) used(machine, label string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.labelLocked(machine, label, now)
	l.used, l.stopped = now, time.Time{}
}

// ownerSpoke notes that the owner spoke in the conversation whose label it is at now.
func (b *cuaSessionBook) ownerSpoke(label string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spoke == nil {
		b.spoke = map[string]time.Time{}
	}
	b.spoke[label] = now
}

// ended is what to do about machine's label, found ended at now. own: a label of no conversation's (the manager's own
// reads), which no one stops from the screen.
func (b *cuaSessionBook) ended(machine, label string, own bool, now time.Time) cuaVerdict {
	if own {
		return cuaRestart
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.labelLocked(machine, label, now)
	spoke, heard := b.spoke[label]
	switch {
	case !l.stopped.IsZero():
		// Told already: the owner's word since is the go-ahead, nothing else is (the model retrying in the same turn).
		if heard && spoke.After(l.stopped) {
			l.stopped = time.Time{}
			return cuaRestart
		}
		return cuaStopped
	case l.used.IsZero() && heard:
		// Last used before this process began, and the owner spoke since it did: this call is for their new word.
		return cuaRestart
	case !l.used.IsZero() && now.Sub(l.used) < cuaIdleTTL, l.used.IsZero() && now.Sub(b.began()) < cuaIdleTTL:
		// Too recent to have idled out, or the process too new to know: ended on purpose, as far as can be told.
		l.stopped = now
		return cuaStopped
	}
	return cuaRestart
}

// cuaSessionEnded: Cua Driver refused the call because its session had ended.
func cuaSessionEnded(err error) bool {
	return err != nil && strings.Contains(err.Error(), "session has ended")
}

// cuaAnswered: the error is Cua Driver's own answer (the node's "cua-driver <tool>: …"), so the call reached it and its
// session counted it as a call; not the RPC's or the node's.
func cuaAnswered(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cua-driver ")
}

// OwnerSpoke: the owner spoke in assistant conversation session (a message, or an answer to its question). A Cua
// Driver session the user stopped on a Mac for it may start again on its next computer call.
func (h *Manager) OwnerSpoke(session string) {
	if session == "" {
		return
	}
	h.cuaSessions.ownerSpoke(cuaConversationLabel(session), time.Now())
}

// cuaConversationLabel is the Cua Driver session label of conversation session's calls.
func cuaConversationLabel(session string) string { return "persona-" + convID(session) }

// computerCall runs one computer-use tool on m's node under session (a Cua Driver session label). When Cua Driver
// says that session ended, it is started again and the call retried once, unless the user stopped it (the book
// above): then nothing runs and the error tells the model to ask the user first. conversation "" is the manager's own
// call.
func (h *Manager) computerCall(ctx context.Context, m *Machine, session, conversation, tool string, args map[string]any) (*proto.ComputerResult, error) {
	res, err := h.computerCallOnce(ctx, m, session, conversation, tool, args)
	if session == "" || h.callBackend(m) == "lcu" {
		return res, err
	}
	if !cuaSessionEnded(err) {
		if err == nil || cuaAnswered(err) {
			h.cuaSessions.used(m.cfg.ID, session, time.Now())
		}
		return res, err
	}
	if h.cuaSessions.ended(m.cfg.ID, session, conversation == "", time.Now()) == cuaStopped {
		h.logf("[%s] Cua Driver session %s was ended on the Mac while in use: not started again until the owner speaks", m.cfg.ID, session)
		return nil, h.cuaStoppedErr(m)
	}
	if _, serr := h.computerCallOnce(ctx, m, session, conversation, "start_session", map[string]any{}); serr != nil {
		h.logf("[%s] Cua Driver session %s had ended; starting it again failed: %v", m.cfg.ID, session, serr)
		return nil, fmt.Errorf("%w (Persona could not start it again: %v)", err, serr)
	}
	h.logf("[%s] Cua Driver session %s had ended: started again", m.cfg.ID, session)
	res, err = h.computerCallOnce(ctx, m, session, conversation, tool, args)
	if err == nil || cuaAnswered(err) && !cuaSessionEnded(err) {
		h.cuaSessions.used(m.cfg.ID, session, time.Now())
	}
	return res, err
}

// cuaStoppedErr is what the model is told when the user stopped operating m on its screen: nothing was done, ask the
// user (a sentence to say, in their language) and call again only after they answer.
func (h *Manager) cuaStoppedErr(m *Machine) error {
	say := i18n.T(h.userLang(), "hub-computer.session_stopped", i18n.Args{"machine": m.name()})
	return errors.New("Nothing was done: operating " + m.name() + " was stopped on its screen (most likely the user pressed " +
		"Stop on the bar Cua Driver shows there). Do not call computer_state or computer_run again in this turn. Tell the " +
		"user and ask whether to go on, in your reply, in these words or close to them: \"" + say + "\" Once they answer " +
		"that you should go on, call again: the first call after their answer starts it again.")
}
