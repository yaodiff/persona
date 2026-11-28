package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func setupSupervisedRecoveryTaskOnMachine(
	t *testing.T,
	s *Service,
	f *fakeHub,
	machineID, title, sessionID, source string,
	doneWhen []string,
	withSession bool,
) Todo {
	t.Helper()
	ctx := context.Background()
	task, err := s.createTodo(ctx, title, "Verify per-machine restart recovery behavior.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", doneWhen, 0)
	if err := s.linkTodoAgentWithSource(ctx, task.ID, machineID, sessionID, title, "/tmp/work", "claude", "", source, ""); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.sessions == nil {
		f.sessions = make(map[string][]proto.Session)
	}
	if _, exists := f.sessions[machineID]; !exists {
		f.sessions[machineID] = []proto.Session{}
	}
	if withSession {
		f.sessions[machineID] = append(f.sessions[machineID], proto.Session{
			MachineID: machineID,
			SessionID: sessionID,
			Title:     title,
			Cwd:       "/tmp/work",
			Agent:     "claude",
			Status:    proto.StatusIdle,
			UpdatedAt: time.Now().UTC(),
		})
	}
	f.mu.Unlock()
	return todoNow(s, task.ID)
}

func TestTodosV2RestartRecoveryZeroSessionsOnConnectedNode(t *testing.T) {
	s, f := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	f.mu.Lock()
	f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	f.mu.Unlock()

	// Connected machine reports 0 active sessions after reboot (pre-restart process exited).
	task := setupSupervisedRecoveryTaskOnMachine(t, s, f, "mac", "Rebooted node task", "s-gone-1", "assistant", []string{"Checks pass."}, false)
	if err := s.RecordSupervisedTaskActive(ctx, task.ID, "mac", "s-gone-1", "boot-prev"); err != nil {
		t.Fatal(err)
	}

	s.setBootEpoch("boot-2")
	s.superviseWatch(ctx, time.Now())

	if !s.todosV2RecoverySwept.Load() {
		t.Fatal("expected todosV2RecoverySwept=true after connected 0-session machine completed recovery")
	}
	rec, found, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, task.ID)
	if err != nil || !found {
		t.Fatalf("expected recovery checkpoint to remain tracked: found=%t err=%v", found, err)
	}
	if rec.LastActiveBootEpoch != "boot-2" {
		t.Fatalf("expected checkpoint boot epoch updated to boot-2, got %q", rec.LastActiveBootEpoch)
	}

	s.supervise.mu.Lock()
	sig, hasSig := s.supervise.pending[task.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if !hasSig || !strings.Contains(sig.Key, "restart_recovery_missing:") {
		t.Fatalf("expected missing-session sigRecovered signal for %s, got has=%t sig=%+v", task.ID, hasSig, sig)
	}

	// Repeating superviseWatch in the same boot must be a strict no-op.
	s.supervise.mu.Lock()
	delete(s.supervise.pending, task.ID+"|"+sigRecovered)
	s.supervise.mu.Unlock()
	s.superviseWatch(ctx, time.Now())
	s.supervise.mu.Lock()
	_, reemitted := s.supervise.pending[task.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if reemitted {
		t.Fatal("expected second superviseWatch in same boot not to re-process already swept machine")
	}
}

func TestTodosV2RestartRecoveryPerMachinePartialOfflineAndReconnect(t *testing.T) {
	s, f := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	f.mu.Lock()
	f.devices = []Device{
		{ID: "mac-a", Name: "Mac A", Online: true},
		{ID: "mac-b", Name: "Mac B", Online: true},
	}
	f.mu.Unlock()

	taskA := setupSupervisedRecoveryTaskOnMachine(t, s, f, "mac-a", "Online machine task", "s-a-1", "assistant", []string{"A done."}, true)
	taskB := setupSupervisedRecoveryTaskOnMachine(t, s, f, "mac-b", "Offline machine task", "s-b-1", "assistant", []string{"B done."}, true)
	taskBGone := setupSupervisedRecoveryTaskOnMachine(t, s, f, "mac-b", "Offline machine rebooted task", "s-b-gone", "assistant", []string{"B gone done."}, false)

	if err := s.RecordSupervisedTaskActive(ctx, taskA.ID, "mac-a", "s-a-1", "boot-prev"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSupervisedTaskActive(ctx, taskB.ID, "mac-b", "s-b-1", "boot-prev"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSupervisedTaskActive(ctx, taskBGone.ID, "mac-b", "s-b-gone", "boot-prev"); err != nil {
		t.Fatal(err)
	}

	// Simulate mac-b offline for the entire duration of tick 1.
	f.mu.Lock()
	f.sessionsFail["mac-b"] = 100
	f.mu.Unlock()

	s.setBootEpoch("boot-2")
	s.superviseWatch(ctx, time.Now())

	// Tick 1: mac-a recovers immediately; mac-b checkpoints stay untouched; todosV2RecoverySwept stays false.
	if s.todosV2RecoverySwept.Load() {
		t.Fatal("expected todosV2RecoverySwept=false while mac-b is still offline")
	}
	if sent := sentToAgent(f); len(sent) != 1 || !strings.Contains(sent[0], "attempt 1/2") {
		t.Fatalf("expected exactly 1 resume prompt for mac-a on tick 1, got %+v", sent)
	}
	recA, okA, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskA.ID)
	if err != nil || !okA || recA.LastActiveBootEpoch != "boot-2" || recA.RecoveryAttempts != 1 {
		t.Fatalf("expected mac-a checkpoint recovered on tick 1, got ok=%t rec=%+v err=%v", okA, recA, err)
	}
	recB, okB, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskB.ID)
	if err != nil || !okB || recB.LastActiveBootEpoch != "boot-prev" || recB.RecoveryAttempts != 0 {
		t.Fatalf("expected offline mac-b checkpoint untouched on tick 1, got ok=%t rec=%+v err=%v", okB, recB, err)
	}
	recBGone, okBGone, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskBGone.ID)
	if err != nil || !okBGone || recBGone.LastActiveBootEpoch != "boot-prev" || recBGone.RecoveryAttempts != 0 {
		t.Fatalf("expected offline mac-b missing-session checkpoint untouched on tick 1, got ok=%t rec=%+v err=%v", okBGone, recBGone, err)
	}
	s.supervise.mu.Lock()
	_, falseSigB := s.supervise.pending[taskB.ID+"|"+sigRecovered]
	_, falseSigBGone := s.supervise.pending[taskBGone.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if falseSigB || falseSigBGone {
		t.Fatalf("offline machine checkpoints must never be falsely flagged missing while offline: b=%t bgone=%t", falseSigB, falseSigBGone)
	}

	// Tick 2: mac-b reconnects. Recover mac-b without re-processing mac-a.
	f.mu.Lock()
	f.sessionsFail["mac-b"] = 0
	f.mu.Unlock()
	s.superviseWatch(ctx, time.Now())

	if !s.todosV2RecoverySwept.Load() {
		t.Fatal("expected todosV2RecoverySwept=true once mac-b reconnected and swept")
	}
	if sent := sentToAgent(f); len(sent) != 2 || !strings.Contains(sent[1], "attempt 1/2") {
		t.Fatalf("expected 2 total resume prompts (1 for mac-a, 1 for mac-b), got %+v", sent)
	}
	recA2, _, _ := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskA.ID)
	if recA2.RecoveryAttempts != 1 {
		t.Fatalf("expected mac-a not re-processed on tick 2, got attempts=%d", recA2.RecoveryAttempts)
	}
	recB2, _, _ := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskB.ID)
	if recB2.LastActiveBootEpoch != "boot-2" || recB2.RecoveryAttempts != 1 {
		t.Fatalf("expected mac-b idle checkpoint recovered on tick 2, got %+v", recB2)
	}
	recBGone2, _, _ := s.todoRecoveryStore().GetTodoRecoveryState(ctx, taskBGone.ID)
	if recBGone2.LastActiveBootEpoch != "boot-2" {
		t.Fatalf("expected mac-b missing-session checkpoint updated on tick 2, got %+v", recBGone2)
	}
	s.supervise.mu.Lock()
	sigBGone, hasSigBGone := s.supervise.pending[taskBGone.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if !hasSigBGone || !strings.Contains(sigBGone.Key, "restart_recovery_missing:") {
		t.Fatalf("expected missing-session sigRecovered for mac-b after reconnect, got has=%t sig=%+v", hasSigBGone, sigBGone)
	}
}

func TestTodosV2RestartRecoveryEdgeBypassWhenFlagOff(t *testing.T) {
	s, f := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()

	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default to be false")
	}
	f.mu.Lock()
	f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	f.mu.Unlock()

	task := setupSupervisedRecoveryTaskOnMachine(t, s, f, "mac", "Flag-off empty session task", "s-off-empty", "assistant", []string{"Done."}, false)
	if err := s.todoRecoveryStore().RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
		TodoID:              task.ID,
		MachineID:           "mac",
		SessionID:           "s-off-empty",
		LastActiveBootEpoch: "boot-prev",
	}); err != nil {
		t.Fatal(err)
	}

	s.setBootEpoch("boot-now")
	s.superviseWatch(ctx, time.Now())

	if s.todosV2RecoverySwept.Load() {
		t.Fatal("expected todosV2RecoverySwept to stay false when todos_v2=false")
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("expected 0 messages when todos_v2=false, got %+v", sent)
	}
	rec, found, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, task.ID)
	if err != nil || !found || rec.LastActiveBootEpoch != "boot-prev" {
		t.Fatalf("expected checkpoint untouched when todos_v2=false, got found=%t rec=%+v err=%v", found, rec, err)
	}
	s.supervise.mu.Lock()
	_, hasSig := s.supervise.pending[task.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if hasSig {
		t.Fatal("expected no recovery signal when todos_v2=false")
	}
}
