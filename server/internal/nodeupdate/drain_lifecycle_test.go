package nodeupdate

import (
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func spinUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// Once ArmHandover or PreflightFailed has ended pre-exec draining and armed rollback, a late or
// duplicate BeginDrain for the same upload ID must not flip Draining back to true or start a
// keepalive goroutine that would hold off the rollback watchdog.
func TestBeginDrainRejectedAfterHandoverOrPreflightFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(m *Manager, id string) error
	}{
		{"arm-handover", func(m *Manager, id string) error { return m.ArmHandover(id) }},
		{"preflight-failed", func(m *Manager, id string) error {
			m.PreflightFailed(id, errors.New("exit status 23"))
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, data, _ := testManager(t)
			m.SetTimeoutForTest(time.Minute)
			id := receive(t, m, data, spec(data, "next"))
			if _, err := m.Commit(t.Context(), id, nil); err != nil {
				t.Fatal(err)
			}
			m.ResponseWritten(id)
			if err := m.BeginDrain(id); err != nil {
				t.Fatal(err)
			}
			if err := tc.end(m, id); err != nil {
				t.Fatal(err)
			}
			// A late or retried BeginDrain must be a no-op after handover/preflight failure.
			if err := m.BeginDrain(id); err != nil {
				t.Fatalf("late BeginDrain error: %v", err)
			}
			p, err := loadPending(markerPath(m.exe))
			if err != nil {
				t.Fatal(err)
			}
			if p.Draining {
				t.Fatalf("late BeginDrain after %s resurrected Draining=true on marker", tc.name)
			}
			m.mu.Lock()
			hasDrain := m.drainCancel != nil || m.drainDone != nil
			m.mu.Unlock()
			if hasDrain || m.activeDrains.Load() != 0 {
				t.Fatalf("late BeginDrain after %s started a zombie drain keepalive (active=%d)", tc.name, m.activeDrains.Load())
			}
		})
	}
}

// Close must wait for any in-flight BeginDrain to finish and prevent it from mutating the marker
// or leaving a drain keepalive goroutine running after Close returns.
func TestCloseSynchronizesWithInFlightBeginDrain(t *testing.T) {
	m, data, _ := testManager(t)
	refuseWatch(t, m)
	m.SetTimeoutForTest(time.Hour)
	target := spec(data, "next")
	id := receive(t, m, data, target)
	installExternally(t, m, data)

	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)

	// Rewind AdoptionDeadline so we can deterministically observe BeginDrain passing its first lock
	// check and blocking on the directory flock, with zero sleeps.
	sentinel := time.Now().Add(time.Minute)
	m.mu.Lock()
	m.upload.AdoptionDeadline = sentinel
	m.mu.Unlock()

	unlock, err := lockDir(filepath.Dir(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()

	beginDone := make(chan error, 1)
	go func() {
		beginDone <- m.BeginDrain(id)
	}()

	// Wait until BeginDrain has updated AdoptionDeadline under m.mu and is now heading into locked(exe).
	spinUntil(t, "BeginDrain to update AdoptionDeadline before lockDir", func() bool {
		m.mu.Lock()
		bumped := m.upload.AdoptionDeadline.After(sentinel)
		m.mu.Unlock()
		return bumped
	})

	// Start Close concurrently while BeginDrain is held at the directory lock.
	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()

	spinUntil(t, "Close to mark manager closed", m.closed.Load)

	beforeMarker, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned while BeginDrain was still in flight")
	default:
	}

	unlock()
	released = true
	if err := <-beginDone; err != nil {
		t.Fatal(err)
	}
	<-closed

	afterMarker, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if !afterMarker.Deadline.Equal(beforeMarker.Deadline) {
		t.Fatalf("BeginDrain mutated marker deadline after Close began: before=%v after=%v",
			beforeMarker.Deadline, afterMarker.Deadline)
	}
	m.mu.Lock()
	hasDrain := m.drainCancel != nil || m.drainDone != nil
	m.mu.Unlock()
	if hasDrain || m.activeDrains.Load() != 0 {
		t.Fatalf("drain keepalive remained active after Close (active=%d)", m.activeDrains.Load())
	}
}

// Concurrent BeginDrain calls for the same upload must serialize cleanly, leaving at most one
// active keepalive goroutine while draining and zero leaked goroutines after Close.
func TestConcurrentBeginDrainDoesNotLeakKeepalive(t *testing.T) {
	m, data, _ := testManager(t)
	refuseWatch(t, m)
	m.SetTimeoutForTest(time.Hour)
	target := spec(data, "next")
	id := receive(t, m, data, target)
	installExternally(t, m, data)

	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)

	const callers = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = m.BeginDrain(id)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d BeginDrain: %v", i, err)
		}
	}
	if got := m.activeDrains.Load(); got != 1 {
		t.Fatalf("active drain keepalive goroutines after concurrent BeginDrain = %d, want 1", got)
	}

	// A stale upload ID must not stop the active upload's keepalive goroutine.
	if err := m.BeginDrain("stale-upload-id"); err != nil {
		t.Fatalf("stale BeginDrain: %v", err)
	}
	if err := m.ArmHandover("stale-upload-id"); err != nil {
		t.Fatalf("stale ArmHandover: %v", err)
	}
	if got := m.activeDrains.Load(); got != 1 {
		t.Fatalf("stale upload ID disturbed active drain keepalive: active=%d, want 1", got)
	}

	m.Close()

	if got := m.activeDrains.Load(); got != 0 {
		t.Fatalf("leaked %d drain keepalive goroutine(s) after Close", got)
	}
}

// Racing BeginDrain against ArmHandover for the same upload must always converge to Draining=false
// with zero active keepalive goroutines regardless of which call wins drainMu first.
func TestConcurrentBeginDrainAndArmHandoverEndsUndraining(t *testing.T) {
	m, data, _ := testManager(t)
	m.SetTimeoutForTest(time.Minute)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var errDrain, errArm error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errDrain = m.BeginDrain(id)
	}()
	go func() {
		defer wg.Done()
		<-start
		errArm = m.ArmHandover(id)
	}()
	close(start)
	wg.Wait()
	if errDrain != nil || errArm != nil {
		t.Fatalf("BeginDrain=%v ArmHandover=%v", errDrain, errArm)
	}

	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if p.Draining {
		t.Fatal("marker left Draining=true after concurrent BeginDrain and ArmHandover")
	}
	if got := m.activeDrains.Load(); got != 0 {
		t.Fatalf("active drain keepalive goroutines after ArmHandover = %d, want 0", got)
	}
}
