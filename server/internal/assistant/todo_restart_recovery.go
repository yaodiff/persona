package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// maxRestartRecoveryAttempts caps automatic restart self-healing retries per supervised task
// before escalating to supervision (#15733).
const maxRestartRecoveryAttempts = 2

// runtimeBootEpoch returns this Service's stable per-process boot epoch, initializing it lazily
// for zero-value Service{} instances in tests (#16099).
func (s *Service) runtimeBootEpoch() string {
	if s == nil {
		return ""
	}
	if p := s.todosV2BootEpoch.Load(); p != nil && *p != "" {
		return *p
	}
	gen := "boot_" + randomHex(8)
	if s.todosV2BootEpoch.CompareAndSwap(nil, &gen) {
		return gen
	}
	if p := s.todosV2BootEpoch.Load(); p != nil && *p != "" {
		return *p
	}
	return gen
}

// setBootEpoch overrides this Service's boot epoch (tests).
func (s *Service) setBootEpoch(epoch string) {
	if s == nil {
		return
	}
	v := strings.TrimSpace(epoch)
	s.todosV2BootEpoch.Store(&v)
}

// startupRecoveryProgress tracks which machines have already been swept for the current boot
// epoch so offline machines never stall online machines and already-swept machines are never
// double-processed within the same boot (#16227).
type startupRecoveryProgress struct {
	mu            sync.Mutex
	epoch         string
	sweptMachines map[string]bool
	inFlight      map[string]bool
	lastSweptFlag bool
}

func (p *startupRecoveryProgress) markSweptFlag(epoch string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.epoch = epoch
	p.lastSweptFlag = true
}

// startupRecoveryProgressRegistry attaches per-Service startup sweep state without modifying
// Service fields and cleans up automatically when Service is garbage-collected.
var startupRecoveryProgressRegistry = struct {
	mu    sync.Mutex
	bySvc map[weak.Pointer[Service]]*startupRecoveryProgress
}{
	bySvc: make(map[weak.Pointer[Service]]*startupRecoveryProgress),
}

func (s *Service) startupRecoveryProgress() *startupRecoveryProgress {
	wp := weak.Make(s)
	startupRecoveryProgressRegistry.mu.Lock()
	defer startupRecoveryProgressRegistry.mu.Unlock()
	if prog, ok := startupRecoveryProgressRegistry.bySvc[wp]; ok {
		return prog
	}
	prog := &startupRecoveryProgress{
		sweptMachines: make(map[string]bool),
		inFlight:      make(map[string]bool),
	}
	startupRecoveryProgressRegistry.bySvc[wp] = prog
	runtime.AddCleanup(s, func(key weak.Pointer[Service]) {
		startupRecoveryProgressRegistry.mu.Lock()
		delete(startupRecoveryProgressRegistry.bySvc, key)
		startupRecoveryProgressRegistry.mu.Unlock()
	}, wp)
	return prog
}

// maybeRunStartupRestartRecovery executes startup restart self-healing per connected machine
// at most once per process boot when todos_v2 is enabled (#16099, #16227).
func (s *Service) maybeRunStartupRestartRecovery(ctx context.Context, lists machineSessions) {
	if s == nil || !s.settings(ctx).todosV2On() || s.todosV2RecoverySwept.Load() {
		return
	}
	st := s.todoRecoveryStore()
	if st == nil {
		return
	}
	states, err := st.ListTodoRecoveryStates(ctx)
	if err != nil {
		s.logf("todos_v2 startup restart recovery: list states: %v", err)
		return
	}
	bootEpoch := s.runtimeBootEpoch()
	prog := s.startupRecoveryProgress()

	needMachines := make(map[string]struct{})
	for _, rec := range states {
		if rec.LastActiveBootEpoch != "" && rec.LastActiveBootEpoch != bootEpoch && rec.MachineID != "" {
			needMachines[rec.MachineID] = struct{}{}
		}
	}
	if len(needMachines) == 0 {
		for _, l := range lists {
			if l.err == nil && len(l.list) > 0 {
				if s.todosV2RecoverySwept.CompareAndSwap(false, true) {
					prog.markSweptFlag(bootEpoch)
				}
				return
			}
		}
		if s.fleetUp.Load() {
			if s.todosV2RecoverySwept.CompareAndSwap(false, true) {
				prog.markSweptFlag(bootEpoch)
			}
		}
		return
	}

	var toCheck []string
	hasPending := false
	prog.mu.Lock()
	if prog.epoch != bootEpoch || prog.lastSweptFlag {
		prog.epoch = bootEpoch
		prog.sweptMachines = make(map[string]bool)
		prog.inFlight = make(map[string]bool)
		prog.lastSweptFlag = false
	}
	for mid := range needMachines {
		if prog.sweptMachines[mid] {
			continue
		}
		if prog.inFlight[mid] {
			hasPending = true
			continue
		}
		toCheck = append(toCheck, mid)
	}
	prog.mu.Unlock()
	slices.Sort(toCheck)

	if lists == nil {
		lists = machineSessions{}
	}
	readyCandidates := make(map[string]struct{})
	var sessions []proto.Session
	seen := make(map[[2]string]struct{})
	for _, mid := range toCheck {
		l, ok := lists[mid]
		if !ok {
			l.list, l.err = s.sessions(ctx, mid)
			lists[mid] = l
		}
		if l.err != nil {
			// Node not synced/reachable yet; skip only this machine and retry on a later tick.
			hasPending = true
			continue
		}
		readyCandidates[mid] = struct{}{}
		for _, sess := range l.list {
			if sess.MachineID == "" {
				sess.MachineID = mid
			}
			key := [2]string{sess.MachineID, sess.SessionID}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			sessions = append(sessions, sess)
		}
	}

	if len(readyCandidates) > 0 {
		readyMachines := make(map[string]struct{}, len(readyCandidates))
		prog.mu.Lock()
		if prog.epoch != bootEpoch {
			prog.mu.Unlock()
			return
		}
		for mid := range readyCandidates {
			if prog.sweptMachines[mid] || prog.inFlight[mid] {
				continue
			}
			prog.inFlight[mid] = true
			readyMachines[mid] = struct{}{}
		}
		prog.mu.Unlock()

		if len(readyMachines) > 0 {
			_, sweepErr := s.runRestartRecoverySweepForMachines(ctx, bootEpoch, sessions, readyMachines)
			prog.mu.Lock()
			for mid := range readyMachines {
				delete(prog.inFlight, mid)
				if sweepErr == nil && prog.epoch == bootEpoch {
					prog.sweptMachines[mid] = true
				} else {
					hasPending = true
				}
			}
			prog.mu.Unlock()
			if sweepErr != nil {
				s.logf("todos_v2 startup restart recovery sweep: %v", sweepErr)
			}
		}
	}

	if !hasPending {
		prog.mu.Lock()
		if prog.epoch == bootEpoch {
			allDone := true
			for mid := range needMachines {
				if !prog.sweptMachines[mid] || prog.inFlight[mid] {
					allDone = false
					break
				}
			}
			if allDone {
				s.todosV2RecoverySwept.Store(true)
				prog.lastSweptFlag = true
			}
		}
		prog.mu.Unlock()
	}
}

// restartRecoveryResult reports what a startup restart recovery sweep acted on.
type restartRecoveryResult struct {
	RecoveredTodoIDs []string
	EscalatedTodoIDs []string
	SkippedTodoIDs   []string
}

type todoRecoveryStore interface {
	RecordTodoActiveCheckpoint(ctx context.Context, row db.TodoRecoveryState) error
	GetTodoRecoveryState(ctx context.Context, todoID string) (db.TodoRecoveryState, bool, error)
	ListTodoRecoveryStates(ctx context.Context) ([]db.TodoRecoveryState, error)
	IncrementTodoRecoveryAttempts(ctx context.Context, todoID, newBootEpoch string, recoveredAt time.Time) (db.TodoRecoveryState, error)
	ResetTodoRecoveryState(ctx context.Context, todoID string, at time.Time) error
	DeleteTodoRecoveryState(ctx context.Context, todoID string) error
}

// memTodoRecoveryStore is the concurrency-safe fallback store used when Service runs against
// an in-memory test Store instead of Postgres *db.DB.
type memTodoRecoveryStore struct {
	mu   sync.Mutex
	rows map[string]db.TodoRecoveryState
}

func newMemTodoRecoveryStore() *memTodoRecoveryStore {
	return &memTodoRecoveryStore{rows: make(map[string]db.TodoRecoveryState)}
}

func (m *memTodoRecoveryStore) RecordTodoActiveCheckpoint(_ context.Context, row db.TodoRecoveryState) error {
	row.TodoID = strings.TrimSpace(row.TodoID)
	row.MachineID = strings.TrimSpace(row.MachineID)
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.LastActiveBootEpoch = strings.TrimSpace(row.LastActiveBootEpoch)
	if row.TodoID == "" || row.MachineID == "" || row.SessionID == "" || row.LastActiveBootEpoch == "" {
		return errors.New("todo recovery checkpoint requires todo_id, machine_id, session_id and last_active_boot_epoch")
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.rows[row.TodoID]; ok {
		cur.MachineID = row.MachineID
		cur.SessionID = row.SessionID
		cur.LastActiveBootEpoch = row.LastActiveBootEpoch
		cur.UpdatedAt = row.UpdatedAt.UTC()
		m.rows[row.TodoID] = cur
		return nil
	}
	if row.RecoveryAttempts < 0 {
		row.RecoveryAttempts = 0
	}
	row.UpdatedAt = row.UpdatedAt.UTC()
	m.rows[row.TodoID] = row
	return nil
}

func (m *memTodoRecoveryStore) GetTodoRecoveryState(_ context.Context, todoID string) (db.TodoRecoveryState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[strings.TrimSpace(todoID)]
	return row, ok, nil
}

func (m *memTodoRecoveryStore) ListTodoRecoveryStates(_ context.Context) ([]db.TodoRecoveryState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]db.TodoRecoveryState, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b db.TodoRecoveryState) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.TodoID, b.TodoID)
	})
	return out, nil
}

func (m *memTodoRecoveryStore) IncrementTodoRecoveryAttempts(_ context.Context, todoID, newBootEpoch string, recoveredAt time.Time) (db.TodoRecoveryState, error) {
	todoID = strings.TrimSpace(todoID)
	newBootEpoch = strings.TrimSpace(newBootEpoch)
	if todoID == "" || newBootEpoch == "" {
		return db.TodoRecoveryState{}, errors.New("increment todo recovery attempts requires todo_id and new_boot_epoch")
	}
	if recoveredAt.IsZero() {
		recoveredAt = time.Now().UTC()
	}
	at := recoveredAt.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[todoID]
	if !ok {
		return db.TodoRecoveryState{}, db.ErrTodoNotFound
	}
	cur.RecoveryAttempts++
	cur.LastActiveBootEpoch = newBootEpoch
	cur.LastRecoveredAt = &at
	cur.UpdatedAt = at
	m.rows[todoID] = cur
	return cur, nil
}

func (m *memTodoRecoveryStore) ResetTodoRecoveryState(_ context.Context, todoID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.rows[strings.TrimSpace(todoID)]; ok {
		cur.RecoveryAttempts = 0
		cur.LastRecoveredAt = nil
		cur.UpdatedAt = at.UTC()
		m.rows[cur.TodoID] = cur
	}
	return nil
}

func (m *memTodoRecoveryStore) DeleteTodoRecoveryState(_ context.Context, todoID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, strings.TrimSpace(todoID))
	return nil
}

// restartRecoveryFallbackRegistry attaches a per-Service in-memory recovery store without
// modifying Service fields and cleans up automatically when Service is garbage-collected.
var restartRecoveryFallbackRegistry = struct {
	mu    sync.Mutex
	bySvc map[weak.Pointer[Service]]*memTodoRecoveryStore
}{
	bySvc: make(map[weak.Pointer[Service]]*memTodoRecoveryStore),
}

func (s *Service) todoRecoveryStore() todoRecoveryStore {
	if s == nil {
		return nil
	}
	if st, ok := s.store().(todoRecoveryStore); ok && st != nil {
		return st
	}
	wp := weak.Make(s)
	restartRecoveryFallbackRegistry.mu.Lock()
	defer restartRecoveryFallbackRegistry.mu.Unlock()
	if mem, ok := restartRecoveryFallbackRegistry.bySvc[wp]; ok {
		return mem
	}
	mem := newMemTodoRecoveryStore()
	restartRecoveryFallbackRegistry.bySvc[wp] = mem
	runtime.AddCleanup(s, func(key weak.Pointer[Service]) {
		restartRecoveryFallbackRegistry.mu.Lock()
		delete(restartRecoveryFallbackRegistry.bySvc, key)
		restartRecoveryFallbackRegistry.mu.Unlock()
	}, wp)
	return mem
}

// RecordSupervisedTaskActive persists an active execution checkpoint for a supervised task
// under todos_v2. When todos_v2 is disabled, it is a 100% no-op. If the task is already
// completed/cancelled, any existing recovery attempt counter is reset.
func (s *Service) RecordSupervisedTaskActive(ctx context.Context, todoID, machineID, sessionID, bootEpoch string) error {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil
	}
	todoID = strings.TrimSpace(todoID)
	machineID = strings.TrimSpace(machineID)
	sessionID = strings.TrimSpace(sessionID)
	bootEpoch = strings.TrimSpace(bootEpoch)
	if todoID == "" || machineID == "" || sessionID == "" || bootEpoch == "" {
		return nil
	}
	st := s.todoRecoveryStore()
	if st == nil {
		return nil
	}
	t, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if t.DoneAt != nil || t.CancelledAt != nil {
		return st.ResetTodoRecoveryState(ctx, todoID, time.Now().UTC())
	}
	if !isSupervisedRecoverableTodo(t) {
		return nil
	}
	agent, linked := findActiveLinkedAgent(t, machineID, sessionID)
	if !linked || isManualTodoAgent(agent) {
		return nil
	}
	return st.RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
		TodoID:              todoID,
		MachineID:           machineID,
		SessionID:           sessionID,
		LastActiveBootEpoch: bootEpoch,
		UpdatedAt:           time.Now().UTC(),
	})
}

// ResetSupervisedTaskRecovery resets the recovery attempt counter when a task resumes
// normal progress or completes. Completely bypassed when todos_v2 is off.
func (s *Service) ResetSupervisedTaskRecovery(ctx context.Context, todoID string) error {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil
	}
	st := s.todoRecoveryStore()
	if st == nil {
		return nil
	}
	return st.ResetTodoRecoveryState(ctx, todoID, time.Now().UTC())
}

// RunRestartRecoverySweep scans persisted active checkpoints from earlier boot epochs and
// automatically resumes restart-interrupted supervised tasks up to maxRestartRecoveryAttempts,
// escalating cleanly once capped. Strictly bypassed (100% no-op) when todos_v2 is off.
func (s *Service) RunRestartRecoverySweep(ctx context.Context, currentBootEpoch string, sessions []proto.Session) (restartRecoveryResult, error) {
	return s.runRestartRecoverySweepForMachines(ctx, currentBootEpoch, sessions, nil)
}

func (s *Service) runRestartRecoverySweepForMachines(
	ctx context.Context,
	currentBootEpoch string,
	sessions []proto.Session,
	readyMachines map[string]struct{},
) (restartRecoveryResult, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return restartRecoveryResult{}, nil
	}
	currentBootEpoch = strings.TrimSpace(currentBootEpoch)
	if currentBootEpoch == "" {
		return restartRecoveryResult{}, nil
	}
	st := s.todoRecoveryStore()
	if st == nil {
		return restartRecoveryResult{}, nil
	}
	states, err := st.ListTodoRecoveryStates(ctx)
	if err != nil {
		return restartRecoveryResult{}, err
	}
	if len(states) == 0 {
		return restartRecoveryResult{}, nil
	}

	sessByKey := make(map[[2]string]proto.Session, len(sessions))
	wantCards := make(map[string]map[string]bool)
	for _, sess := range sessions {
		sessByKey[[2]string{sess.MachineID, sess.SessionID}] = sess
	}
	for _, rec := range states {
		if rec.LastActiveBootEpoch == currentBootEpoch {
			continue
		}
		if readyMachines != nil {
			if _, ok := readyMachines[rec.MachineID]; !ok {
				continue
			}
		}
		if wantCards[rec.MachineID] == nil {
			wantCards[rec.MachineID] = make(map[string]bool)
		}
		wantCards[rec.MachineID][rec.SessionID] = true
	}
	pendingCards := s.pendingOf(wantCards)
	asks := s.todoAsks(ctx)
	now := time.Now().UTC()

	var result restartRecoveryResult
	for _, rec := range states {
		if readyMachines != nil {
			if _, ok := readyMachines[rec.MachineID]; !ok {
				continue
			}
		}
		if rec.LastActiveBootEpoch == currentBootEpoch {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 1: open supervised task with explicit done_when.
		t, ok, err := s.todo(ctx, rec.TodoID)
		if err != nil {
			return result, err
		}
		if !ok || t.DoneAt != nil || t.CancelledAt != nil || !isSupervisedRecoverableTodo(t) {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 2: no pending owner card / needs_user / unanswered interaction.
		if hasPendingOwnerWait(t, asks, pendingCards[[2]string{rec.MachineID, rec.SessionID}]) {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 3: active linked non-manual Agent (never touch user-opened manual terminals).
		agent, linked := findActiveLinkedAgent(t, rec.MachineID, rec.SessionID)
		if !linked || isManualTodoAgent(agent) {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 4: session dropped to idle/done without a completion conclusion (skip still-working),
		// or vanished completely on a connected ready machine after restart.
		sess, found := sessByKey[[2]string{rec.MachineID, rec.SessionID}]
		if !found {
			if readyMachines == nil || isRestartConcludedTodo(t) {
				result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
				continue
			}
			if err := st.RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
				TodoID:              rec.TodoID,
				MachineID:           rec.MachineID,
				SessionID:           rec.SessionID,
				LastActiveBootEpoch: currentBootEpoch,
				RecoveryAttempts:    rec.RecoveryAttempts,
				LastRecoveredAt:     rec.LastRecoveredAt,
				UpdatedAt:           now,
			}); err != nil {
				return result, err
			}
			s.signal(superviseSignal{
				Todo:    t.ID,
				Kind:    sigRecovered,
				Key:     fmt.Sprintf("restart_recovery_missing:%s@%s", t.ID, currentBootEpoch),
				At:      now,
				Machine: rec.MachineID,
				Session: rec.SessionID,
				Detail: fmt.Sprintf(
					"Pre-restart session %s/%s is no longer active after restart (boot epoch %s -> %s); supervisor review required.",
					rec.MachineID, rec.SessionID, rec.LastActiveBootEpoch, currentBootEpoch,
				),
			})
			result.EscalatedTodoIDs = append(result.EscalatedTodoIDs, rec.TodoID)
			continue
		}
		if !isRestartInterruptedSession(t, sess) {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 6: cap reached -> escalate cleanly to supervision, never loop infinitely.
		if rec.RecoveryAttempts >= maxRestartRecoveryAttempts {
			if err := st.RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
				TodoID:              rec.TodoID,
				MachineID:           rec.MachineID,
				SessionID:           rec.SessionID,
				LastActiveBootEpoch: currentBootEpoch,
				RecoveryAttempts:    rec.RecoveryAttempts,
				LastRecoveredAt:     rec.LastRecoveredAt,
				UpdatedAt:           now,
			}); err != nil {
				return result, err
			}
			s.signal(superviseSignal{
				Todo:    t.ID,
				Kind:    sigRecovered,
				Key:     fmt.Sprintf("restart_recovery_cap:%s@%s", t.ID, currentBootEpoch),
				At:      now,
				Machine: rec.MachineID,
				Session: rec.SessionID,
				Detail: fmt.Sprintf(
					"Automatic restart recovery reached the %d-attempt cap for session %s/%s across boot epoch %s -> %s; supervisor review required.",
					maxRestartRecoveryAttempts, rec.MachineID, rec.SessionID, rec.LastActiveBootEpoch, currentBootEpoch,
				),
			})
			result.EscalatedTodoIDs = append(result.EscalatedTodoIDs, rec.TodoID)
			continue
		}

		// Guard 5: under cap -> send bounded resume instruction and persist attempt increment.
		prompt := formatRestartRecoveryPrompt(rec.RecoveryAttempts+1, maxRestartRecoveryAttempts)
		if _, err := s.sendToTask(ctx, t, prompt); err != nil {
			result.SkippedTodoIDs = append(result.SkippedTodoIDs, rec.TodoID)
			continue
		}
		if _, err := st.IncrementTodoRecoveryAttempts(ctx, rec.TodoID, currentBootEpoch, now); err != nil {
			return result, err
		}
		result.RecoveredTodoIDs = append(result.RecoveredTodoIDs, rec.TodoID)
	}
	return result, nil
}

func isSupervisedRecoverableTodo(t Todo) bool {
	if t.Supervision == nil || strings.TrimSpace(t.Origin) == "" {
		return false
	}
	if t.Supervision.State == reviewAccepted || t.Supervision.State == reviewParked {
		return false
	}
	for _, criterion := range t.Supervision.DoneWhen {
		if strings.TrimSpace(criterion) != "" {
			return true
		}
	}
	return false
}

func hasPendingOwnerWait(t Todo, asks map[string]todoAsk, cards []proto.Interaction) bool {
	if t.Supervision != nil {
		if t.Supervision.State == reviewNeedsUser || t.Supervision.Waits != nil {
			return true
		}
	}
	if t.planPending {
		return true
	}
	if t.Check != nil && t.Check.Verdict == verdictNeedsUser {
		return true
	}
	if _, hasAsk := asks[t.ID]; hasAsk {
		return true
	}
	return len(cards) > 0
}

func findActiveLinkedAgent(t Todo, machineID, sessionID string) (TodoAgent, bool) {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if a.MachineID == machineID && a.SessionID == sessionID {
			return a, true
		}
	}
	return TodoAgent{}, false
}

func isManualTodoAgent(a TodoAgent) bool {
	return strings.EqualFold(strings.TrimSpace(a.Source), "manual")
}

func isRestartConcludedTodo(t Todo) bool {
	if t.Check != nil && t.Check.Verdict == verdictDone {
		return true
	}
	if t.Supervision != nil && t.Supervision.Progress != nil && t.Supervision.Progress.Kind == "done" {
		return true
	}
	return false
}

func isRestartInterruptedSession(t Todo, sess proto.Session) bool {
	status := strings.ToLower(strings.TrimSpace(sess.Status))
	if status == proto.StatusWorking {
		return false
	}
	if status != proto.StatusIdle && status != proto.StatusDone {
		return false
	}
	return !isRestartConcludedTodo(t)
}

func formatRestartRecoveryPrompt(attempt, maxAttempts int) string {
	return fmt.Sprintf(
		"Service restart interrupted your background command (auto-recovery attempt %d/%d). Please clean up any stale temporary artifacts, verify current repository state, and continue completing the remaining verification and delivery.",
		attempt, maxAttempts,
	)
}
