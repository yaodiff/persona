package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// updateOwnerWork writes only the private owner-work evidence. It must not hold todos.writing while waiting for the
// ledger: independent to-do writes should remain available while owner-work evidence waits.
func TestUpdateOwnerWorkWaitsForTheLedgerWithoutTheTodoWriter(t *testing.T) {
	s, _, todo, _ := ownerInstructionTask(t)
	s.supervise.writeMu.Lock()
	unlock := sync.OnceFunc(s.supervise.writeMu.Unlock)
	defer unlock()
	done := make(chan error, 1)
	go func() {
		done <- s.updateOwnerWork(context.Background(), todo.ID, func(v *Supervision) bool { return false })
	}()
	waitFor(t, "owner work at the ledger lock", func() bool {
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*Service).updateOwnerWork") {
				return true
			}
		}
		return false
	})
	if !s.todos.writing.TryLock() {
		t.Fatal("updateOwnerWork holds todos.writing while waiting for supervise.writeMu")
	}
	s.todos.writing.Unlock()
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("updateOwnerWork did not finish after releasing the ledger lock")
	}
}
