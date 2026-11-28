package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

func TestCloseStopsResumeWindowTimer(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, time.Hour, true, true, nil)
	l.cut()
	l.waitLost()

	l.h.mu.Lock()
	armed := l.h.resume != nil && l.h.resume.timer != nil
	l.h.mu.Unlock()
	if !armed {
		t.Fatal("expected resume window timer to be armed after transport loss")
	}

	l.h.Close(ErrClosed)

	l.h.mu.Lock()
	stillArmed := l.h.resume != nil && l.h.resume.timer != nil
	l.h.mu.Unlock()
	if stillArmed {
		t.Fatal("resume window timer stayed armed after Peer.Close")
	}
}

func TestResumeWindowTimerDoesNotKillResumedPeerRace(t *testing.T) {
	// Exercise concurrent timer expiration and resume handover with a short window:
	// whenever resume succeeds (returns nil), a stale window timer must never close the peer afterwards.
	for range 30 {
		l := newResumeLink(t, netshape.Config{}, 2*time.Millisecond, true, true, nil)
		l.cut()
		l.waitLost()
		if err := l.resume(); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := l.h.Call(ctx, "ping", map[string]any{}, nil)
		cancel()
		if err != nil {
			t.Fatalf("peer died after successful resume (h.Err=%v, n.Err=%v): %v", l.h.Err(), l.n.Err(), err)
		}
	}
}

func TestResumeWindowTimerFiredBeforeResumeDoesNotKillResumedPeer(t *testing.T) {
	fired := make(chan struct{})
	release := make(chan struct{})
	expiredDone := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	l := newResumeLink(t, netshape.Config{}, 5*time.Millisecond, true, true, func(h, n *Peer) {
		n.EnableResume(time.Hour)
		h.mu.Lock()
		var once sync.Once
		h.resume.testWindowExpire = func(expire func()) {
			once.Do(func() {
				close(fired)
				<-release
				expire()
				close(expiredDone)
			})
		}
		h.mu.Unlock()
	})

	l.cut()
	l.waitLost()

	// Wait deterministically for the window timer callback to have already fired (so timer.Stop() returns false).
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("resume window timer callback did not fire")
	}

	// Resume completes before the fired callback enters closeIf.
	if err := l.resume(); err != nil {
		t.Fatalf("resume failed: %v", err)
	}

	// Let the stale timer callback run to completion; it must see the updated generation/state and leave the peer open.
	unblock()
	select {
	case <-expiredDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale timer callback did not finish")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.h.Call(ctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("RPC call after stale timer callback failed (h.Err=%v): %v", l.h.Err(), err)
	}
	if err := l.h.Err(); err != nil {
		t.Fatalf("resumed peer closed by stale timer: %v", err)
	}
}

func TestResumeWindowTimerFromPriorLossDoesNotKillNextLossWindow(t *testing.T) {
	fired := make(chan struct{})
	release := make(chan struct{})
	expiredDone := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	l := newResumeLink(t, netshape.Config{}, 5*time.Millisecond, true, true, func(h, n *Peer) {
		n.EnableResume(time.Hour)
		h.mu.Lock()
		var once sync.Once
		h.resume.testWindowExpire = func(expire func()) {
			once.Do(func() {
				close(fired)
				<-release
				expire()
				close(expiredDone)
			})
		}
		h.mu.Unlock()
	})

	// Loss #1: timer fires and pauses before closeIf.
	l.cut()
	l.waitLost()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("first loss timer callback did not fire")
	}

	// Extend window for loss #2, resume #1, then trigger loss #2 while stale timer #1 is still parked.
	l.h.mu.Lock()
	l.h.resume.window = time.Hour
	l.h.resume.testWindowExpire = nil
	l.h.mu.Unlock()

	if err := l.resume(); err != nil {
		t.Fatalf("first resume failed: %v", err)
	}
	l.cut()
	l.waitLost()

	// Release stale timer #1 while the peer is lost again (on gen+1 / timerGen+1) and wait for it to finish.
	unblock()
	select {
	case <-expiredDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale first timer callback did not finish")
	}

	// Peer must remain open and resumable during loss #2's fresh 1h window.
	if err := l.resume(); err != nil {
		t.Fatalf("second resume failed after stale first timer ran (h.Err=%v): %v", l.h.Err(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.h.Call(ctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("RPC call after second resume failed: %v", err)
	}
}
