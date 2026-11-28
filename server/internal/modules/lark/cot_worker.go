package lark

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// cotWorker separates native I/O from the card's progress mutex. It owns the native
// cursor and one replaceable snapshot of the bounded item history, never a queue
// of ticks. A terminal snapshot supersedes progress and cancels its in-flight I/O.
type cotFallback struct {
	message string
	card    *botcard.Card
}

type cotWorker struct {
	answer                  chan *cotFallback
	ownerDone               <-chan struct{}
	mu                      sync.Mutex
	wake                    chan struct{}
	done                    chan struct{}
	pending                 []proto.Item
	queued, ending, aborted bool
	answerAbandoned         bool
	finished                bool
	usable                  bool
	cancel                  context.CancelFunc
	run                     channel.Run
	live                    runLive // worker-owned native cursor, never the card's runLive
	final                   cotFinal
	outcome                 cotOutcome
}

// cotQueue runs under the progress mutex, but never waits for native I/O. It owns the run's agent message for a
// topic run, and for a private run with dm_cot on. Its return value is the last confirmed native availability, for
// card fallback. fin, when set, is the turn's terminal snapshot and supersedes progress.
func (f feishuSink) cotQueue(r *channel.Run, fin *cotFinal) (bool, *cotWorker) {
	finish := fin != nil
	s := f.s
	st, ownerDone, ok := s.h.RunState(r.ID, false, func() any { return &runLive{} })
	owner, _ := st.(*runLive)
	if !ok || owner == nil {
		return false, nil
	}
	owner.mu.Lock()
	w := owner.native
	if w == nil {
		snap := *r
		snap.Ext = cloneExt(r.Ext)
		w = &cotWorker{answer: make(chan *cotFallback, 1), ownerDone: ownerDone, wake: make(chan struct{}, 1), done: make(chan struct{}), run: snap}
		w.live.cot = owner.cot
		w.usable = snap.Ext["cot"] != "" && snap.Ext["cot_msg"] != "" && snap.Ext["cot_off"] == ""
		owner.native = w
		w.live.cot.persist = func(snap *channel.Run, key, value string) {
			w.mu.Lock()
			if snap.Ext == nil {
				snap.Ext = map[string]string{}
			}
			snap.Ext[key] = value
			ext := map[string]string{key: value}
			if key == "cot" {
				snap.Ext["cot_msg"] = w.live.cot.msg
				ext["cot_msg"] = w.live.cot.msg
			}
			w.usable = snap.Ext["cot"] != "" && snap.Ext["cot_msg"] != "" && snap.Ext["cot_off"] == ""
			// The run's own values are kept (and the channels' state saved) only while this worker is still the run's,
			// under the channels' lock; removed durable membership never falls back to r.
			live, kept := false, false
			if !w.aborted {
				live, kept = s.h.PersistRunExt(r.ID, owner, func() bool { return owner.nativeIs(w) }, ext)
			}
			if live && !kept && !w.ending {
				w.aborted, w.pending, w.queued = true, nil, false
				if w.cancel != nil {
					w.cancel()
				}
			}
			// The channels' checkpoint is saved without holding the worker's lock over its I/O.
			w.mu.Unlock()
			if kept {
				s.h.SaveRuns()
			}
		}
		s.t.workers.Add(1)
		go func() { defer s.t.workers.Done(); w.work(f) }()
		go func() {
			select {
			case <-ownerDone:
				w.abort()
			case <-s.runCtx().Done():
				w.abort()
			case <-w.done:
			}
		}()
	}
	owner.mu.Unlock()
	items, _ := s.h.RunItems(r.ID)
	w.mu.Lock()
	defer w.mu.Unlock()
	usable := w.usable
	if w.ending || w.aborted {
		return usable, w
	}
	w.pending, w.queued = items, true
	if finish {
		w.ending = true
		w.final = *fin
		if w.cancel != nil {
			w.cancel()
		}
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return usable, w
}

// nativeIs: w is the run's worker.
func (p *runLive) nativeIs(w *cotWorker) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.native == w
}

func (w *cotWorker) abort() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A terminal snapshot is already independent of the now-removed run.
	if w.ending {
		return
	}
	w.aborted, w.pending, w.queued = true, nil, false
	if w.cancel != nil {
		w.cancel()
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *cotWorker) work(f feishuSink) {
	defer close(w.done)
	defer func() {
		// A folded/removed run can receive a successful create after cancellation.
		// Close that known message without publishing state into its successor.
		w.mu.Lock()
		aborted := w.aborted
		w.mu.Unlock()
		if aborted && w.live.cot.cot != "" && !w.live.cot.done && !w.live.cot.off {
			_ = f.cotEnd(context.Background(), w.live.cot.cot, w.live.cot.msg, cotReasonDone)
		}
	}()
	for range w.wake {
		w.mu.Lock()
		if w.aborted {
			w.mu.Unlock()
			return
		}
		if !w.queued {
			w.mu.Unlock()
			continue
		}
		items, finish, final, abandoned := w.pending, w.ending, w.final, w.answerAbandoned
		if abandoned {
			final.answer = ""
		}
		hasOpenCot := w.live.cot.cot != "" || w.run.Ext["cot"] != ""
		w.pending, w.queued = nil, false
		if finish && abandoned && !hasOpenCot {
			w.outcome = cotOutcome{native: false, answered: false}
			w.finished = true
			w.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(f.s.runCtx(), 15*time.Second)
		w.cancel = cancel
		w.mu.Unlock()
		ctx = f.s.sendAs(ctx, "progress", w.run.MachineID, w.run.SessionID)
		native := true
		if finish {
			native = f.cotFinishNowItems(ctx, &w.run, &w.live, items, final)
		} else {
			f.cotProgressNowItems(ctx, &w.run, &w.live, items)
		}
		cancel()
		w.mu.Lock()
		w.cancel = nil
		w.usable = w.live.cot.cot != "" && !w.live.cot.off
		if finish {
			// Written before w.done closes, so a reader of it sees the outcome.
			w.outcome = cotOutcome{native: native, answered: w.live.cot.answered}
			w.finished = true
		}
		unified := w.final.answer != "" || w.answerAbandoned
		aborted := w.aborted
		w.mu.Unlock()
		// Unified mode already knows its outcome: the answer waits for it itself, so there is no card to repair.
		if finish && !native && !unified {
			// A failed answer attempt has no repair target. A successful retry can
			// supply one while native I/O is still blocked; removal still ends the wait.
			var repair *cotFallback
			select {
			case repair = <-w.answer:
			case <-w.ownerDone:
				select {
				case repair = <-w.answer:
				default:
				}
			case <-time.After(time.Minute):
			case <-f.s.runCtx().Done():
			}
			if repair != nil && repair.message != "" {
				rctx, done := context.WithTimeout(f.s.runCtx(), 15*time.Second)
				if err := f.s.botUpdateAnswerCard(rctx, repair.message, repair.card); err != nil {
					f.s.logf("bots: native process fallback: %v", err)
				}
				done()
			}
		}
		if finish || aborted {
			return
		}
	}
}

func (w *cotWorker) answerFallback(card *cotFallback) {
	select {
	case w.answer <- card:
	default:
	}
}

// state: the agent message is open; it is off. Read by the progress card's decision, never waits for I/O.
func (w *cotWorker) state() (open, off bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.run.Ext["cot"] != "", w.run.Ext["cot_off"] != ""
}

// result waits, at most d, for the terminal work. ok is false when it did not end in time; then its in-flight
// I/O is cancelled, any pending unified answer is abandoned so the worker does not duplicate the card fallback,
// and the worker closes the agent message itself.
func (w *cotWorker) result(ctx context.Context, d time.Duration) (out cotOutcome, ok bool) {
	select {
	case <-w.done:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.outcome, true
	default:
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-w.done:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.outcome, true
	case <-timer.C:
	case <-ctx.Done():
	}
	w.mu.Lock()
	if w.finished {
		out = w.outcome
		w.mu.Unlock()
		return out, true
	}
	if w.run.Ext["cot_answer"] != "" {
		w.mu.Unlock()
		return cotOutcome{native: true, answered: true}, true
	}
	w.answerAbandoned = true
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
	return cotOutcome{}, false
}
