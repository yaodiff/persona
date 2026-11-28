package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Idempotent PR-merge review wake for open supervised tasks (#13100, #14049).
//
// When applyPRStatus observes an open supervised task's delivery PR transition to merged (and no other delivery PR of
// that task remains open), a park waiting on that PR continues using the existing sigReview path, while other tasks,
// including ones parked on something else (a date, a person), receive a durable, idempotent sigPRMerged wake without
// mutating Supervision.State, without ending an unrelated park, and without blindly marking the task done.

const prMergeWakesKey = "assistant.pr_merge_wakes"

// prMergeWakeMarker records that open task TodoID was already woken for delivery PRURL reaching "merged".
type prMergeWakeMarker struct {
	TodoID string    `json:"todo_id"`
	PRURL  string    `json:"pr_url"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
}

// prMergeWakeMarkerKey returns the canonical persistence key for (todo_id, pr_url, merged).
func prMergeWakeMarkerKey(todoID, prURL string) string {
	todoID = strings.TrimSpace(todoID)
	norm := normalizePRKey(prURL)
	if todoID == "" || norm == "" {
		return ""
	}
	raw, _ := json.Marshal([]string{todoID, norm, "merged"})
	return string(raw)
}

// prMergeSignalKey returns the canonical signal key for a merged delivery PR.
func prMergeSignalKey(prURL string) string {
	norm := normalizePRKey(prURL)
	if norm == "" {
		return ""
	}
	return norm + "@merged"
}

// checkUnparkedPRMerged verifies that t is an unparked open supervised task whose delivery PR prURL is observed as
// merged and that no other bound delivery PR of t remains open or unobserved.
// A non-nil error indicates a transient store read failure (caller should retry rather than drop permanently).
func (s *Service) checkUnparkedPRMerged(ctx context.Context, t Todo, prURL string) ([]string, bool, error) {
	if t.DoneAt != nil || t.Cancelled() || t.StartError != "" || t.isConcern() || isAskTodo(t.ID) {
		return nil, false, nil
	}
	if t.Supervision == nil || t.Supervision.externalPRWake() != nil {
		return nil, false, nil
	}
	// A park that waits on this PR is woken by its own sigReview path (applyPRStatus). Any other park (a date, someone
	// else, a handoff to another task) stays parked, but its supervisor still hears that the delivery is complete (#14049).
	held := t.Supervision.parking()
	if parkWaitsOnPR(held, prURL) {
		return nil, false, nil
	}
	switch t.Supervision.state(t) {
	case reviewAccepted, reviewCancelled, reviewClosing, reviewUserCheck:
		return nil, false, nil
	}
	prURL = strings.TrimSpace(prURL)
	if prURL == "" || (t.PRURL != prURL && !slices.Contains(t.PRs, prURL)) || !s.stageFollows(t.ID, prURL) {
		return nil, false, nil
	}
	delivery, err := s.todoDeliveryPRs(ctx, t)
	if err != nil {
		return nil, false, err
	}
	targetKey := normalizePRKey(prURL)
	if targetKey == "" {
		return nil, false, nil
	}
	inDelivery := false
	for _, u := range delivery {
		if normalizePRKey(u) == targetKey {
			inDelivery = true
			break
		}
	}
	if !inDelivery || parkWaitsOnPR(held, delivery...) {
		return nil, false, nil
	}
	targetFacts, ok := todoObservedPRState(t, prURL)
	if !ok || !strings.EqualFold(targetFacts.State, "merged") {
		return nil, false, nil
	}
	var otherMerged []string
	seen := map[string]bool{targetKey: true}
	for _, u := range delivery {
		k := normalizePRKey(u)
		if k == "" || k == targetKey {
			continue
		}
		f, ok := todoObservedPRState(t, u)
		if !ok || (!strings.EqualFold(f.State, "merged") && !strings.EqualFold(f.State, "closed")) {
			return nil, false, nil
		}
		if strings.EqualFold(f.State, "merged") && !seen[k] {
			seen[k] = true
			otherMerged = append(otherMerged, u)
		}
	}
	return otherMerged, true, nil
}

// loadPRMergeWakesLocked loads durable PR-merge wake markers once per Service lifetime.
// s.todos.prMergeWakeMu must be held.
func (s *Service) loadPRMergeWakesLocked(ctx context.Context) error {
	b := &s.todos
	if b.prMergeWakesLoaded {
		if b.prMergeWakes == nil {
			b.prMergeWakes = map[string]prMergeWakeMarker{}
		}
		return nil
	}
	if st := s.store(); st != nil {
		loaded := map[string]prMergeWakeMarker{}
		if _, err := st.Setting(ctx, prMergeWakesKey, &loaded); err != nil {
			return err
		}
		b.prMergeWakes = loaded
	} else if b.prMergeWakes == nil {
		b.prMergeWakes = map[string]prMergeWakeMarker{}
	}
	b.prMergeWakesLoaded = true
	return nil
}

// prMergeWakeRecorded reports whether (todoID, prURL, merged) already has a persisted wake marker.
func (s *Service) prMergeWakeRecorded(ctx context.Context, todoID, prURL string) (bool, error) {
	key := prMergeWakeMarkerKey(todoID, prURL)
	if key == "" {
		return false, nil
	}
	b := &s.todos
	b.prMergeWakeMu.Lock()
	defer b.prMergeWakeMu.Unlock()
	if err := s.loadPRMergeWakesLocked(ctx); err != nil {
		return false, err
	}
	_, ok := b.prMergeWakes[key]
	return ok, nil
}

// prMergeWakeTold persists idempotent (todo_id, pr_url, merged) wake markers for delivered or durably queued
// sigPRMerged signals so repeated sweeps or restarts never re-emit duplicate wakes.
func (s *Service) prMergeWakeTold(ctx context.Context, list []superviseSignal) {
	var merged []superviseSignal
	for _, g := range list {
		if g.Kind == sigPRMerged && g.Todo != "" && g.PRURL != "" {
			merged = append(merged, g)
		}
	}
	if len(merged) == 0 {
		return
	}
	b := &s.todos
	b.prMergeWakeMu.Lock()
	defer b.prMergeWakeMu.Unlock()
	if err := s.loadPRMergeWakesLocked(ctx); err != nil {
		s.logf("pr_merged: load wake markers: %v", err)
		return
	}
	next := maps.Clone(b.prMergeWakes)
	if next == nil {
		next = map[string]prMergeWakeMarker{}
	}
	now := time.Now().UTC()
	changed := false
	mark := func(todoID, prURL string) {
		k := prMergeWakeMarkerKey(todoID, prURL)
		if k == "" {
			return
		}
		if _, ok := next[k]; ok {
			return
		}
		next[k] = prMergeWakeMarker{
			TodoID: todoID,
			PRURL:  normalizePRKey(prURL),
			State:  "merged",
			At:     now,
		}
		changed = true
	}
	for _, g := range merged {
		mark(g.Todo, g.PRURL)
		// Also mark any already-merged sibling delivery PRs covered by this same multi-PR closeout wake,
		// so re-polling the current PR on a later sweep never emits a duplicate wake.
		if t, ok := s.todoNow(g.Todo); ok {
			if delivery, err := s.todoDeliveryPRs(ctx, t); err == nil {
				for _, u := range delivery {
					if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "merged") {
						mark(g.Todo, u)
					}
				}
			}
		}
	}
	if !changed {
		return
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, prMergeWakesKey, next); err != nil {
			s.logf("pr_merged: persist wake markers: %v", err)
			return
		}
	}
	b.prMergeWakes = next
}

// prunePRMergeWakes removes wake markers for closed or deleted to-dos so the stored map stays bounded by open tasks.
func (s *Service) prunePRMergeWakes(ctx context.Context) {
	b := &s.todos
	b.prMergeWakeMu.Lock()
	defer b.prMergeWakeMu.Unlock()
	if err := s.loadPRMergeWakesLocked(ctx); err != nil {
		s.logf("pr_merged: load wake markers for prune: %v", err)
		return
	}
	if len(b.prMergeWakes) == 0 {
		return
	}
	var remove []string
	b.mu.RLock()
	if b.loaded {
		for k, m := range b.prMergeWakes {
			if t := b.todos[m.TodoID]; t == nil || t.DoneAt != nil {
				remove = append(remove, k)
			}
		}
	}
	b.mu.RUnlock()
	if len(remove) == 0 {
		return
	}
	next := maps.Clone(b.prMergeWakes)
	for _, k := range remove {
		delete(next, k)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, prMergeWakesKey, next); err != nil {
			s.logf("pr_merged: prune wake markers: %v", err)
			return
		}
	}
	b.prMergeWakes = next
}

// clearSentPRMerged removes in-memory sent markers for sigPRMerged signals dropped as moot before delivery.
func (s *Service) clearSentPRMerged(list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range list {
		if g.Kind == sigPRMerged && b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
	}
}

// hasPendingReviewSignal reports whether a sigReview wake is already queued for todoID.
func (s *Service) hasPendingReviewSignal(todoID string) bool {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.pending[(superviseSignal{Todo: todoID, Kind: sigReview}).id()]
	return ok
}

// maybeEmitUnparkedPRMerged emits one durable, idempotent sigPRMerged wake when an unparked open supervised task's
// delivery PR transitions to merged (or is observed merged while sitting in stage: merge / waiting supervision) and no
// other delivery PR of the task remains open. Parked tasks (including unrelated active parks) and tasks already
// carrying a PR-park or pending sigReview wake are left untouched.
func (s *Service) maybeEmitUnparkedPRMerged(ctx context.Context, todoID, prURL string, held *TaskReview, parkWoke, wasOpen bool, preStage string) {
	if parkWoke || parkWaitsOnPR(held, prURL) {
		return
	}
	cur, ok := s.todoNow(todoID)
	if !ok || cur.Supervision == nil || parkWaitsOnPR(cur.Supervision.parking(), prURL) || cur.Supervision.externalPRWake() != nil {
		return
	}
	if s.hasPendingReviewSignal(cur.ID) {
		return
	}
	st := cur.Supervision.state(cur)
	if st == reviewPending && !wasOpen {
		return
	}
	// A parked task's supervisor is not watching its PRs: a delivery PR first read merged while it waits (one only in
	// its repository links, polled since #14049) is news too, once per PR (the marker below).
	if !wasOpen && preStage != "merge" && st != reviewNeedsUser && st != reviewTimedOut && st != reviewRework && st != reviewParked {
		return
	}
	if _, ready, err := s.checkUnparkedPRMerged(ctx, cur, prURL); err != nil || !ready {
		return
	}
	recorded, err := s.prMergeWakeRecorded(ctx, cur.ID, prURL)
	if err != nil {
		s.logf("pr_merged: check wake marker for %s (%s): %v", cur.ID, prURL, err)
		return
	}
	if recorded {
		return
	}
	s.signal(superviseSignal{
		Todo:  cur.ID,
		Kind:  sigPRMerged,
		PRURL: prURL,
		Key:   prMergeSignalKey(prURL),
	})
}

// prMergedDigestEntry builds and revalidates a sigPRMerged supervision entry at delivery time, returning retry=true on
// transient repository-store read failures.
func (s *Service) prMergedDigestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil || t.Supervision == nil {
		return prompts.SuperviseEntry{}, false, false
	}
	otherMerged, ready, err := s.checkUnparkedPRMerged(ctx, t, g.PRURL)
	if err != nil {
		return prompts.SuperviseEntry{}, false, true
	}
	if !ready || prMergeSignalKey(g.PRURL) != g.Key {
		return prompts.SuperviseEntry{}, false, false
	}
	v := t.Supervision
	lang := answerLanguage(string(s.botLang(ctx)))
	if g.To == "" && s.isLeaderRef(v.Supervisor) {
		lang = "English"
	}
	e := prompts.SuperviseEntry{
		ID:       t.ID,
		Title:    t.Title,
		Kind:     sigPRMerged,
		Detail:   prompts.RenderPRMergedWake(prompts.PRMergedWake{Language: lang, PRURL: g.PRURL, OtherPRs: otherMerged, Parked: parkedReason(v)}),
		DoneWhen: v.DoneWhen,
		Round:    v.Round,
		Link:     todoLink(web, t.ID),
		Quiet:    t.Quiet,
	}
	e.LastReview = s.lastReviewEntry(ctx, v)
	s.setEntryOrigin(ctx, t, web, &e)
	s.briefEntry(ctx, t.ID, &e)
	return e, true, false
}

// parkedReason is what task supervision v is parked for, "" when it is not parked.
func parkedReason(v *Supervision) string {
	if r := v.parking(); r != nil {
		return r.Reason
	}
	return ""
}
