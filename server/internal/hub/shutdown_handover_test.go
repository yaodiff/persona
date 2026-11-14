package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type handoverTestStore struct {
	*meshStore
	warmRelease   chan struct{}
	warmStarted   chan struct{}
	warmReturned  chan struct{}
	machineWrites atomic.Int32
}

func newHandoverTestStore() *handoverTestStore {
	return &handoverTestStore{
		meshStore:    newMeshStore(),
		warmStarted:  make(chan struct{}),
		warmReturned: make(chan struct{}),
	}
}

func (s *handoverTestStore) PutSetting(ctx context.Context, key string, v any) error {
	switch key {
	case devicesWarmKey:
		close(s.warmStarted)
		if s.warmRelease != nil {
			<-s.warmRelease
		}
		err := ctx.Err()
		if err == nil {
			err = s.meshStore.PutSetting(ctx, key, v)
		}
		close(s.warmReturned)
		return err
	case machineEntriesKey:
		s.machineWrites.Add(1)
	}
	return s.meshStore.PutSetting(ctx, key, v)
}

func newHandoverTestHub(t *testing.T, store Store) *Hub {
	t.Helper()
	h := newTestHub(t)
	h.store = store
	h.shutdown = newShutdownBudget(h.cfg)
	h.shutdown.timeout = 40 * time.Millisecond
	t.Cleanup(h.shutdown.close)
	return h
}

func waitHandover(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestBeginStopMachineHandoverBounded(t *testing.T) {
	t.Run("stalled store", func(t *testing.T) {
		st := newHandoverTestStore()
		st.warmRelease = make(chan struct{})
		release := sync.OnceFunc(func() { close(st.warmRelease) })
		t.Cleanup(release)
		h := newHandoverTestHub(t, st)
		returned := make(chan struct{})
		go func() {
			h.beginStop()
			close(returned)
		}()
		waitHandover(t, st.warmStarted, "the warm handover write")
		select {
		case <-returned:
		case <-time.After(500 * time.Millisecond):
			t.Error("beginStop still waits for a Store call after the shutdown budget")
		}
		release()
		waitHandover(t, st.warmReturned, "the stalled Store call to return")
		waitHandover(t, returned, "beginStop to return")
	})

	t.Run("handover lock contention", func(t *testing.T) {
		st := newHandoverTestStore()
		h := newHandoverTestHub(t, st)
		h.devices.reportedSave.Lock()
		unlock := sync.OnceFunc(h.devices.reportedSave.Unlock)
		t.Cleanup(unlock)
		returned := make(chan struct{})
		go func() {
			h.beginStop()
			close(returned)
		}()
		waitHandover(t, st.warmReturned, "the warm handover write")
		select {
		case <-returned:
		case <-time.After(500 * time.Millisecond):
			t.Error("beginStop still waits for reportedSave after the shutdown budget")
		}
		unlock()
		waitHandover(t, returned, "beginStop to return")
	})
}

func TestSaveMachineEntriesAfterLockWait(t *testing.T) {
	for _, expired := range []bool{true, false} {
		name := "live context still writes"
		if expired {
			name = "expired context does not write"
		}
		t.Run(name, func(t *testing.T) {
			st := newHandoverTestStore()
			h := newHandoverTestHub(t, st)
			h.devices.reportedRead = true
			h.devices.reportedSave.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
			} else {
				t.Cleanup(cancel)
			}
			done := make(chan struct{})
			go func() {
				h.saveMachineEntries(ctx)
				close(done)
			}()
			h.devices.reportedSave.Unlock()
			waitHandover(t, done, "saveMachineEntries to finish")
			want := int32(1)
			if expired {
				want = 0
			}
			if got := st.machineWrites.Load(); got != want {
				t.Fatalf("machine-entry writes = %d, want %d", got, want)
			}
		})
	}
}
