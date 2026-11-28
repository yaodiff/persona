package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const maxSupervisionBatchMinutes = 1440

// syncBatchInterval releases optional deadlines when batching is switched off. The next routing pass applies the
// ordinary recipient cooldown instead; a disabled hour-long batch must not survive as a routine cooldown. b.mu is held.
func (b *superviseBook) syncBatchInterval(interval time.Duration) {
	if b.batchInterval > 0 && interval <= 0 {
		clear(b.batch)
		for id, g := range b.pending {
			g.batchTo, g.batchUntil = "", time.Time{}
			b.pending[id] = g
		}
	}
	b.batchInterval = interval
}

// superviseRoutineCooldown is how long routine signals for a conversation wait after a supervision message went to it,
// so that several tasks' idle, report and all_stopped signals arriving one after another make one wake, not one every
// superviseUrgentEvery (#11614). Every wake re-reads the supervisor's whole context, which is what costs. Signals that
// wait on a decision (supervisionBatchImmediate) never wait for it and take the held ones with them.
var superviseRoutineCooldown = 10 * time.Minute

// routineCooldown is p's cooldown, or superviseRoutineCooldown bounded by five times the shortest spacing: the hub's
// tests that shorten the spacing get a cooldown as short, production gets the full ten minutes.
func (p superviseTimings) routineCooldown() time.Duration {
	if p.cooldown > 0 {
		return p.cooldown
	}
	return min(superviseRoutineCooldown, 5*min(p.urgent, p.every))
}

// toldRecipient notes that a supervision message went in or queued for conversation to at now.
func (s *Service) toldRecipient(to string, now time.Time) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.told == nil {
		b.told = map[string]time.Time{}
	}
	if now.After(b.told[to]) {
		b.told[to] = now
	}
}

// supervisionBatchMinutesOf is the additional per-recipient routine batching interval. Zero keeps the legacy shared
// gather and spacing exactly as they were. Invalid saved values fail back to that behavior.
func supervisionBatchMinutesOf(v Settings) int {
	if v.SupervisionBatchMinutes == nil || *v.SupervisionBatchMinutes < 0 || *v.SupervisionBatchMinutes > maxSupervisionBatchMinutes {
		return 0
	}
	return *v.SupervisionBatchMinutes
}

func (s *Service) supervisionBatchInterval(ctx context.Context) time.Duration {
	return time.Duration(supervisionBatchMinutesOf(s.settings(ctx))) * time.Minute
}

// supervisionBatchImmediate bypasses the optional per-recipient batch and the routine cooldown.
// A recipient's whole digest goes together when any signal in it is immediate.
func supervisionBatchImmediate(g superviseSignal) bool {
	switch g.Kind {
	case sigIdle:
		return g.Handoff
	case sigNeedsYou, sigAsk, sigEscalated, sigDecide, sigDecisionReply, sigParkDecided, sigOwnerCannot, sigRecheck, sigUndecidable, sigDeliveryHold, sigFailed, sigCardlessWait,
		sigParkedDue, sigTodoReady, sigPRMerged, sigParentCloseout, sigNeedsUserExternalInfo, sigOutageRecovery, sigOwnerPresent: // a parked task whose time came (#11614), unblocked dependent (#12606), merge/parent closeout (#13100), external info during needs_user (#13112), outage recovery (#13158; still gated by NotBefore), or owner returned while waiting on owner action (#15087)
		return true
	case sigCheck: // the owner's answer to a check's decision; the check's own news can wait for the batch
		return g.Report == checkDecidedReport
	case sigReport:
		return g.Report == upReportQuestion || g.Report == upReportBlocked
	}
	return g.todoV2Wake()
}

// digestWaitWithBatch is digestWait while optional batching is off. With it on, recipient batch deadlines replace the
// legacy shared last-message gap, so one recipient cannot hold up another. New routine signals make one scheduling pass
// after the ordinary gather, where gateDigestBatches assigns their recipient's deadline.
func digestWaitWithBatch(now, last time.Time, pending []superviseSignal, p superviseTimings, batch time.Duration) (time.Duration, bool) {
	if batch <= 0 {
		return digestWait(now, last, pending, p)
	}
	var due time.Time
	for _, g := range pending {
		at := superviseGatherAt(g, p.gather)
		if g.NotBefore.After(at) {
			at = g.NotBefore
		}
		if batch > 0 && !supervisionBatchImmediate(g) && g.batchUntil.After(at) {
			at = g.batchUntil
		}
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	if due.IsZero() {
		return 0, false
	}
	return max(0, due.Sub(now)), true
}

// supervisionBatchDeferred reports whether g stays pending while another recipient or an immediate signal is delivered.
func supervisionBatchDeferred(g superviseSignal, now time.Time) bool {
	return !supervisionBatchImmediate(g) && g.batchUntil.After(now.Add(time.Second))
}

// gateDigestBatches holds routine groups until their own recipient's deadline: with a batch interval, that interval
// after the first routine signal; without one, the routine cooldown after the last supervision message that went to
// the recipient (none recently: no wait). An immediate group also releases that recipient's previously held routine
// signals, so a mixed digest is delivered whole. Returned groups still use the existing deliverDigest path, including
// its busy-recipient relay queue.
func (s *Service) gateDigestBatches(order []string, groups map[string][]superviseSignal, now time.Time, interval, cooldown time.Duration) []string {
	if interval <= 0 && cooldown <= 0 {
		return order
	}
	out := make([]string, 0, len(order))
	for _, to := range order {
		list := groups[to]
		immediate := slices.ContainsFunc(list, supervisionBatchImmediate)
		b := &s.supervise
		b.mu.Lock()
		deadline, told := b.batch[to], b.told[to]
		b.mu.Unlock()
		if !immediate {
			if deadline.IsZero() && interval <= 0 && !told.IsZero() {
				deadline = told.Add(cooldown)
			}
			if deadline.IsZero() && interval > 0 {
				anchor := now
				for _, g := range list {
					if !g.At.IsZero() && g.At.Before(anchor) {
						anchor = g.At
					}
				}
				deadline = anchor.Add(interval)
			}
			if deadline.After(now.Add(time.Second)) {
				s.holdDigestBatch(to, deadline, list)
				delete(groups, to)
				continue
			}
		}
		groups[to] = s.releaseDigestBatch(to, list)
		out = append(out, to)
	}
	return out
}

// holdDigestBatch returns an unaccepted group to pending with one recipient deadline. A newer copy queued meanwhile
// stays newer and inherits the same deadline; neither copy is marked sent.
func (s *Service) holdDigestBatch(to string, deadline time.Time, list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if b.batch == nil {
		b.batch = map[string]time.Time{}
	}
	b.batch[to] = deadline
	for _, g := range list {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if cur, ok := b.pending[g.id()]; ok {
			g = cur
		}
		g.batchTo, g.batchUntil = to, deadline
		b.pending[g.id()] = g
	}
}

// releaseDigestBatch takes every held signal for to, merges newer copies queued while this digest was being built,
// clears its batch clock and reserves the released signals in the existing early sent cache.
func (s *Service) releaseDigestBatch(to string, current []superviseSignal) []superviseSignal {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	var held []superviseSignal
	for id, g := range b.pending {
		if g.batchTo != to {
			continue
		}
		held = append(held, g)
		delete(b.pending, id)
	}
	delete(b.batch, to)
	out := mergeSignals(current, held)
	for i := range out {
		out[i].batchTo, out[i].batchUntil = "", time.Time{}
		b.sent[out[i].id()] = out[i].Key
	}
	return out
}

// rescheduleSupervision makes a settings write take effect without waiting for an old timer. Existing nonzero batch
// deadlines stay anchored; setting zero releases them through the legacy scheduler on the newly scheduled pass.
func (s *Service) rescheduleSupervision() {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
}

// schedulePendingSupervision arms a batch stranded by an early screening return. Delivery failures already set a
// retry timer: preserve that backoff instead of making an old pending signal immediately due again.
func (s *Service) schedulePendingSupervision() {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer == nil && len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
}

func (s *Service) setSupervisionBatchMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var input *int
	if err := json.Unmarshal(raw, &input); err != nil || input == nil || *input < 0 || *input > maxSupervisionBatchMinutes {
		return nil, i18n.Err("assistant-settings_tool.bad_supervision_batch_minutes")
	}
	value := *input
	before := supervisionBatchMinutesOf(s.settings(ctx))
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.supervision_batch_minutes_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_supervision_batch_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.SupervisionBatchMinutes = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		s.rescheduleSupervision()
		return i18n.T(s.lang(ctx), "assistant-settings_tool.supervision_batch_minutes_saved", i18n.Args{"value": value}), nil
	}}, nil
}
