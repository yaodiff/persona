package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
	"github.com/nwverse/persona/server/internal/db"
)

type mergeTrainOutcome struct {
	Tracked  bool
	RepoKey  string
	Decision mergetrain.Decision

	turnWakeup superviseSignal
}

type mergeTrainTurnWake struct {
	RepoKey       string `json:"repo_key"`
	PreviousPRURL string `json:"previous_pr_url"`
	PreviousState string `json:"previous_state"`
}

type mergeTrainBlockerCondition struct {
	RepoKey     string `json:"repo_key,omitempty"`
	PRURL       string `json:"pr_url,omitempty"`
	LastHeadOID string `json:"last_head_oid,omitempty"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
}

func mergeTrainBlockerID(prURL string) string {
	sum := sha256.Sum256([]byte(todoBlockerKindMergeTrain + "\x00" + prURL))
	return "mt_" + hex.EncodeToString(sum[:8])
}

type mergeTrainCoordinator struct {
	mu        sync.Mutex
	train     *mergetrain.Train
	checkedAt map[string]time.Time // by canonical PR URL
}

func (s *Service) resetMergeTrain() {
	c := &s.mergeTrain
	c.mu.Lock()
	defer c.mu.Unlock()
	c.train = nil
	clear(c.checkedAt)
}

func mergeTrainBlockerPR(targetRef, conditionJSON string) (string, mergeTrainBlockerCondition) {
	var condition mergeTrainBlockerCondition
	_ = json.Unmarshal([]byte(conditionJSON), &condition)
	for _, ref := range []string{condition.PRURL, targetRef} {
		if _, _, prURL, ok := mergetrain.ParsePRURL(ref); ok {
			return prURL, condition
		}
	}
	return "", condition
}

func (s *Service) clearMergeTrainBlocker(todoID, targetRef, conditionJSON string) {
	c := &s.mergeTrain
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.train == nil {
		return
	}
	prURL, condition := mergeTrainBlockerPR(targetRef, conditionJSON)
	if prURL == "" {
		// A legacy row with only a repo and to-do can be paired with a single known PR.
		// Leave the queue alone if that identity is ambiguous.
		repoKey := mergetrain.NormalizeRepoKey(firstNonEmpty(condition.RepoKey, targetRef))
		for _, repo := range c.train.Snapshot().Repos {
			if repo.RepoKey != repoKey {
				continue
			}
			entries := slices.Concat(repo.Queue, repo.Escalated)
			if repo.Head != nil {
				entries = append(entries, *repo.Head)
			}
			for _, entry := range entries {
				if entry.TodoID == todoID {
					if prURL != "" {
						return
					}
					prURL = entry.PRURL
				}
			}
		}
	}
	c.train.Forget(prURL)
}

func (s *Service) mergeTrainSnapshot(ctx context.Context) mergetrain.Snapshot {
	if !s.settings(ctx).todosV2On() {
		s.resetMergeTrain()
		return mergetrain.Snapshot{}
	}
	c := &s.mergeTrain
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.train == nil {
		return mergetrain.Snapshot{}
	}
	return c.train.Snapshot()
}

func mergeTrainHeadState(entry mergetrain.Entry) bool {
	return entry.State == mergetrain.StateActive || entry.State == mergetrain.StateAwaitingApproval
}

func (s *Service) mergeTrainHeadForWake(g superviseSignal) (mergetrain.Entry, bool) {
	c := &s.mergeTrain
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.train == nil || g.MergeTrainTurn == nil {
		return mergetrain.Entry{}, false
	}
	head, ok := c.train.Head(g.MergeTrainTurn.RepoKey)
	_, _, prURL, _ := mergetrain.ParsePRURL(g.PRURL)
	return head, ok && mergeTrainHeadState(head) && head.PRURL == prURL && head.TodoID == g.Todo
}

func mergeTrainTurnSignal(repoKey, previousPR string, status TodoPRStatus, head mergetrain.Entry) superviseSignal {
	at := status.terminalAt
	if at.IsZero() {
		at = status.CheckedAt
	}
	if at.IsZero() {
		at = time.Now()
	}
	return superviseSignal{
		Todo: head.TodoID, Kind: sigMergeTrainTurn, PRURL: head.PRURL,
		Key:            previousPR + "@" + status.State + "@" + at.UTC().Format(time.RFC3339Nano),
		MergeTrainTurn: &mergeTrainTurnWake{RepoKey: repoKey, PreviousPRURL: previousPR, PreviousState: status.State},
	}
}

// onPRStatusMergeTrain follows an observed PR through the repository queue and stores conflict
// evidence. Forge observations never count as rebase attempts. The existing PR feedback path
// already wakes supervisors about conflicts.
func (s *Service) onPRStatusMergeTrain(ctx context.Context, todo Todo, status TodoPRStatus) mergeTrainOutcome {
	if !s.settings(ctx).todosV2On() {
		s.resetMergeTrain()
		return mergeTrainOutcome{}
	}
	repoKey, _, prURL, ok := mergetrain.ParsePRURL(status.URL)
	if todo.ID == "" || !ok {
		return mergeTrainOutcome{}
	}
	status.State = strings.ToLower(strings.TrimSpace(status.State))
	if status.State != "open" && status.State != "merged" && status.State != "closed" {
		return mergeTrainOutcome{}
	}

	out, changed, resolved, err := s.applyMergeTrainObservation(ctx, todo.ID, repoKey, prURL, status)
	if err != nil {
		s.logf("merge train: persist observation of %s: %v", prURL, err)
	}
	if changed {
		s.todosChanged(ctx)
	}
	for _, row := range resolved {
		ready, err := s.blockerUnblockStillHolds(ctx, superviseSignal{Todo: row.TodoID, Key: blockerResolutionKey(row)})
		if ready || err != nil {
			s.emitTodoBlockerWake(ctx, todoBlockerWake{last: row})
		}
	}
	if len(resolved) > 0 {
		s.sweepAdmissionQueue(ctx)
	}
	if wake := out.turnWakeup; wake.Todo != "" {
		if next, ok := s.todoNow(wake.Todo); ok && next.DoneAt == nil && !next.Cancelled() {
			wake.To = s.todoReadySupervisor(ctx, s.settings(ctx), next)
			s.signal(wake)
		}
	}
	return out
}

func (s *Service) applyMergeTrainObservation(ctx context.Context, todoID, repoKey, prURL string, status TodoPRStatus) (mergeTrainOutcome, bool, []db.TodoBlockerRow, error) {
	// Keep the observed row and queue transition ordered with other PR observations and blocker reconciliation.
	s.blockerReconcile.mu.Lock()
	defer s.blockerReconcile.mu.Unlock()
	c := &s.mergeTrain
	c.mu.Lock()
	defer c.mu.Unlock()

	out := mergeTrainOutcome{RepoKey: repoKey}
	if !s.settings(ctx).todosV2On() || !status.CheckedAt.IsZero() && status.CheckedAt.Before(c.checkedAt[prURL]) {
		return out, false, nil, nil
	}
	if c.train == nil {
		c.train = mergetrain.New()
		c.checkedAt = make(map[string]time.Time)
	}
	old, seen := c.train.Entry(prURL)
	if seen {
		if old.State == mergetrain.StateMerged && status.State != "merged" {
			return out, false, nil, nil
		}
		todoID = old.TodoID
	}
	write, err := s.storeMergeTrainObservation(ctx, todoID, repoKey, prURL, status)
	changed, resolved := write.changed, write.resolved
	if err != nil {
		return out, changed, resolved, err
	}
	if !seen && write.owner != "" {
		todoID = write.owner
	}
	if !status.CheckedAt.IsZero() {
		c.checkedAt[prURL] = status.CheckedAt
	}

	if status.State != "open" {
		before, _ := c.train.Head(repoKey)
		if status.State == "merged" {
			out.Decision, err = c.train.RecordMerged(prURL)
		} else {
			out.Decision, err = c.train.Cancel(prURL)
		}
		if err != nil {
			return out, changed, resolved, nil
		}
		out.Tracked = true
		after, _ := c.train.Head(repoKey)
		if after.PRURL != before.PRURL && mergeTrainHeadState(after) {
			out.turnWakeup = mergeTrainTurnSignal(repoKey, prURL, status, after)
		}
		return out, changed || old.State != mergetrain.StateMerged && old.State != mergetrain.StateCancelled, resolved, nil
	}

	approved := strings.EqualFold(strings.TrimSpace(status.Review), "approved") && !status.Draft
	policy := mergetrain.PolicyHumanReviewRequired
	if approved {
		policy = mergetrain.PolicyAutoMerge
	}
	entry, err := c.train.Enqueue(mergetrain.Entry{TodoID: todoID, PRURL: prURL, Policy: policy, Approved: approved})
	if err != nil {
		return out, changed, resolved, err
	}
	if approved && !entry.Approved {
		if _, err := c.train.Approve(prURL); err == nil {
			entry, _ = c.train.Entry(prURL)
		}
	}
	out.Tracked = true
	out.Decision = mergetrain.Decision{RepoKey: repoKey, TodoID: entry.TodoID, State: entry.State, Action: mergetrain.ActionWaitQueue}
	switch {
	case entry.State == mergetrain.StateEscalated:
		out.Decision.Action = mergetrain.ActionEscalateNeedsUser
		out.Decision.Reason = entry.LastError
	case status.Conflicts:
		out.Decision.Reason = "waiting on merge conflict resolution"
	case mergeTrainHeadState(entry):
		green := strings.EqualFold(strings.TrimSpace(status.Checks), "success") && !status.Draft
		if !approved && (entry.Policy == mergetrain.PolicyAutoMerge || entry.Approved) {
			green = false
		}
		out.Decision = c.train.EvaluateHead(repoKey, green)
	}
	return out, changed || !seen || old.State != out.Decision.State || old.Approved != entry.Approved, resolved, nil
}

type mergeTrainObservationWrite struct {
	owner    string
	changed  bool
	resolved []db.TodoBlockerRow
}

func (s *Service) storeMergeTrainObservation(ctx context.Context, todoID, repoKey, prURL string, status TodoPRStatus) (mergeTrainObservationWrite, error) {
	st := s.todoBlockerDB()
	if st == nil {
		return mergeTrainObservationWrite{}, nil
	}
	rows, err := st.ListMergeTrainPRBlockers(ctx, prURL, repoKey)
	if err != nil {
		return mergeTrainObservationWrite{}, err
	}
	conflict := status.State == "open" && status.Conflicts
	now := time.Now().UTC()
	hasActive, dismissed := false, false
	var result mergeTrainObservationWrite
	var writeErr error
	for _, row := range rows {
		storedPR, condition := mergeTrainBlockerPR(row.TargetRef, row.ConditionJSON)
		if storedPR != prURL {
			continue
		}
		if row.State != db.TodoBlockerStateActive {
			dismissed = dismissed || row.State == db.TodoBlockerStateCancelled && condition.LastHeadOID == status.HeadOID
			continue
		}
		if result.owner == "" {
			result.owner = row.TodoID
		}
		if !conflict {
			saved, err := st.ResolveTodoBlocker(ctx, row.ID, now)
			writeErr = errors.Join(writeErr, err)
			if saved != nil {
				result.changed = true
				result.resolved = append(result.resolved, *saved)
			}
			continue
		}
		hasActive = true
		if condition.LastHeadOID != status.HeadOID {
			condition.LastHeadOID = status.HeadOID
			data, err := json.Marshal(condition)
			if err != nil {
				return result, err
			}
			saved, err := st.UpdateActiveTodoBlockerCondition(ctx, row.ID, string(data), now)
			writeErr = errors.Join(writeErr, err)
			result.changed = result.changed || saved != nil
		}
	}
	if conflict && !hasActive && !dismissed {
		data, err := json.Marshal(mergeTrainBlockerCondition{RepoKey: repoKey, PRURL: prURL, LastHeadOID: status.HeadOID})
		if err != nil {
			return result, err
		}
		saved, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
			ID: mergeTrainBlockerID(prURL), TodoID: todoID, Kind: todoBlockerKindMergeTrain,
			TargetRef: prURL, ConditionJSON: string(data), State: db.TodoBlockerStateActive,
			CreatedAt: now, UpdatedAt: now,
		})
		writeErr = errors.Join(writeErr, err)
		result.changed = result.changed || saved != nil
	}
	return result, writeErr
}

type mergeTrainQueueItemView struct {
	TodoID         string `json:"todo_id"`
	PRNumber       int    `json:"pr_number,omitempty"`
	PRURL          string `json:"pr_url,omitempty"`
	State          string `json:"state"`
	Policy         string `json:"policy"`
	Approved       bool   `json:"approved"`
	Position       int    `json:"position"`
	RebaseFailures int    `json:"rebase_failures,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

type mergeTrainRepoQueueView struct {
	RepoKey   string                    `json:"repo_key"`
	Head      *mergeTrainQueueItemView  `json:"head,omitempty"`
	Queue     []mergeTrainQueueItemView `json:"queue"`
	Escalated []mergeTrainQueueItemView `json:"escalated,omitempty"`
}

type mergeTrainTodoBadgeView struct {
	RepoKey    string `json:"repo_key"`
	Position   int    `json:"position"`
	AheadPRURL string `json:"ahead_pr_url,omitempty"`
	State      string `json:"state"`
}

type mergeTrainSnapshotResponse struct {
	Repos  []mergeTrainRepoQueueView          `json:"repos"`
	ByTodo map[string]mergeTrainTodoBadgeView `json:"by_todo"`
}

func mergeTrainItemView(e mergetrain.Entry, pos int) mergeTrainQueueItemView {
	return mergeTrainQueueItemView{
		TodoID:         e.TodoID,
		PRNumber:       e.PRNumber,
		PRURL:          e.PRURL,
		State:          string(e.State),
		Policy:         string(e.Policy),
		Approved:       e.Approved,
		Position:       pos,
		RebaseFailures: e.RebaseFailures,
		LastError:      e.LastError,
	}
}

func buildMergeTrainSnapshotResponse(snap mergetrain.Snapshot, hidden map[string]bool) mergeTrainSnapshotResponse {
	resp := mergeTrainSnapshotResponse{
		Repos:  make([]mergeTrainRepoQueueView, 0, len(snap.Repos)),
		ByTodo: make(map[string]mergeTrainTodoBadgeView),
	}
	addBadge := func(todoID string, next mergeTrainTodoBadgeView) {
		current, exists := resp.ByTodo[todoID]
		if !exists || current.State != string(mergetrain.StateEscalated) &&
			(next.State == string(mergetrain.StateEscalated) || next.Position < current.Position) {
			resp.ByTodo[todoID] = next
		}
	}
	for _, rs := range snap.Repos {
		rv := mergeTrainRepoQueueView{
			RepoKey: rs.RepoKey,
			Queue:   make([]mergeTrainQueueItemView, 0, len(rs.Queue)),
		}
		if rs.Head != nil && !hidden[rs.Head.TodoID] {
			item := mergeTrainItemView(*rs.Head, 1)
			rv.Head = &item
			addBadge(rs.Head.TodoID, mergeTrainTodoBadgeView{
				RepoKey:  rs.RepoKey,
				Position: 1,
				State:    string(rs.Head.State),
			})
		}
		basePos := 1
		if rs.Head != nil {
			basePos = 2
		}
		for i, e := range rs.Queue {
			if hidden[e.TodoID] {
				continue
			}
			pos := basePos + i
			aheadURL := ""
			if i == 0 {
				if rs.Head != nil && !hidden[rs.Head.TodoID] {
					aheadURL = rs.Head.PRURL
				}
			} else if !hidden[rs.Queue[i-1].TodoID] {
				aheadURL = rs.Queue[i-1].PRURL
			}
			rv.Queue = append(rv.Queue, mergeTrainItemView(e, pos))
			addBadge(e.TodoID, mergeTrainTodoBadgeView{
				RepoKey:    rs.RepoKey,
				Position:   pos,
				AheadPRURL: aheadURL,
				State:      string(e.State),
			})
		}
		for _, e := range rs.Escalated {
			if hidden[e.TodoID] {
				continue
			}
			rv.Escalated = append(rv.Escalated, mergeTrainItemView(e, 0))
			addBadge(e.TodoID, mergeTrainTodoBadgeView{
				RepoKey:  rs.RepoKey,
				Position: 0,
				State:    string(e.State),
			})
		}
		if rv.Head != nil || len(rv.Queue) > 0 || len(rv.Escalated) > 0 {
			resp.Repos = append(resp.Repos, rv)
		}
	}
	return resp
}

// todoMergeTrainRoutes registers read-only merge-train queue visibility endpoints strictly gated under todos_v2=true (#16432).
func (s *Service) todoMergeTrainRoutes() {
	s.handle("GET /api/v1/todos/mergetrain", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("merge train queue requires todos_v2 to be enabled"))
			return
		}
		snap := s.mergeTrainSnapshot(ctx)
		var hidden map[string]bool
		if r.Header.Get(RoleHeader) == roleExtension {
			if err := s.ensureTodosLoaded(ctx); err != nil {
				apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
				return
			}
			var ids []string
			for _, rs := range snap.Repos {
				if rs.Head != nil {
					ids = append(ids, rs.Head.TodoID)
				}
				for _, e := range rs.Queue {
					ids = append(ids, e.TodoID)
				}
				for _, e := range rs.Escalated {
					ids = append(ids, e.TodoID)
				}
			}
			if len(ids) > 0 {
				hidden = s.todosHidden(ctx, ids, nil)
			}
		}
		apierr.WriteJSON(w, buildMergeTrainSnapshotResponse(snap, hidden))
	})
}
