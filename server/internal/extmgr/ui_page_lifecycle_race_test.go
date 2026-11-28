package extmgr

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

type blockingPageProcess struct {
	gen          atomic.Uint64
	mountEntered chan struct{}
	mountOnce    sync.Once
	mountCalls   atomic.Int32
	disposeCalls atomic.Int32
	suspendHook  func(ctx context.Context) error
}

func (p *blockingPageProcess) Call(ctx context.Context, method string, _ any, _ any) error {
	switch method {
	case "ui.page.mount":
		p.mountCalls.Add(1)
		if p.mountEntered != nil {
			p.mountOnce.Do(func() { close(p.mountEntered) })
		}
		<-ctx.Done()
		return ctx.Err()
	case "ui.page.dispose":
		p.disposeCalls.Add(1)
		return nil
	case "ui.page.suspend":
		if p.suspendHook != nil {
			return p.suspendHook(ctx)
		}
		return nil
	}
	return nil
}

func (p *blockingPageProcess) Hold() func()       { return func() {} }
func (p *blockingPageProcess) Generation() uint64 { return p.gen.Load() }

func TestPageDisposeDuringConcurrentMountNoDataRace(t *testing.T) {
	for range 20 {
		h := New(Options{Config: &Config{}})
		proc := &blockingPageProcess{}
		proc.gen.Store(1)

		req := retryOpenRequest()
		s, reused, err := h.pageReserveOpen(pageWho, req, proc, extension.UISlot{}, extension.UIParams{})
		if err != nil || reused {
			t.Fatalf("pageReserveOpen: reused=%v err=%v", reused, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = h.pageMount(t.Context(), s)
		}()
		go func() {
			defer wg.Done()
			<-start
			h.pageDropExtension("example-page")
		}()
		close(start)
		wg.Wait()

		if proc.mountCalls.Load() > 0 && proc.disposeCalls.Load() == 0 {
			t.Fatal("ui.page.mount ran without corresponding ui.page.dispose after pageDropExtension")
		}
	}
}

type signalPageHub struct {
	mu      sync.Mutex
	frames  []extwire.UITarget
	emitted chan struct{}
	once    sync.Once
}

func (p *signalPageHub) Call(context.Context, string, any, any) error { return nil }
func (p *signalPageHub) Notify(event string, params any) error {
	if event == extwire.EventUIStream {
		p.mu.Lock()
		p.frames = append(p.frames, params.(extwire.UITarget))
		p.mu.Unlock()
		if p.emitted != nil {
			p.once.Do(func() { close(p.emitted) })
		}
	}
	return nil
}

func TestPageEmitAfterStopDeliveryReturnsFalseWithoutLeakingBytes(t *testing.T) {
	t.Run("stop_before_first_emit", func(t *testing.T) {
		hub := &signalPageHub{}
		h := New(Options{Config: &Config{}, Hub: hub})
		h.pageStopDelivery()
		h.pageStopDelivery() // idempotent

		if h.pageEmit(pageWho, json.RawMessage(`{"kind":"delta"}`)) {
			t.Fatal("pageEmit returned true after pageStopDelivery")
		}
		if got := h.uiDelivery.bytes.Load(); got != 0 {
			t.Fatalf("uiDelivery.bytes = %d after rejected post-stop emit, want 0", got)
		}
	})

	t.Run("stop_after_emit", func(t *testing.T) {
		hub := &signalPageHub{emitted: make(chan struct{})}
		h := New(Options{Config: &Config{}, Hub: hub})
		if !h.pageEmit(pageWho, json.RawMessage(`{"kind":"snapshot"}`)) {
			t.Fatal("initial pageEmit returned false")
		}
		select {
		case <-hub.emitted:
		case <-time.After(2 * time.Second):
			t.Fatal("initial frame was not delivered")
		}
		h.pageStopDelivery()
		before := h.uiDelivery.bytes.Load()
		if h.pageEmit(pageWho, json.RawMessage(`{"kind":"delta","payload":"post-stop"}`)) {
			t.Fatal("pageEmit returned true after pageStopDelivery")
		}
		if got := h.uiDelivery.bytes.Load(); got > before {
			t.Fatalf("uiDelivery.bytes increased after rejected post-stop emit: before=%d after=%d", before, got)
		}
	})
}

func TestPageDisposeAllAndDisconnectNoWhoDataRace(t *testing.T) {
	for range 20 {
		hub := &signalPageHub{}
		h := New(Options{Config: &Config{}, Hub: hub})

		proc := &blockingPageProcess{}
		proc.gen.Store(1)
		s, err := h.pageReserve(pageWho, "example-page", "home", proc, extension.UISlot{}, extension.UIParams{})
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			h.pageDisconnect(pageWho)
		}()
		go func() {
			defer wg.Done()
			<-start
			h.pageDropExtension("example-page")
		}()
		close(start)
		wg.Wait()
		h.pageStopDelivery()

		_ = s
	}
}

func TestPageResumeAuthorizeFailureRestoresOwner(t *testing.T) {
	h := New(Options{Config: &Config{}})
	h.extensions = newExtensions(t.TempDir(), nil, func(string, ...any) {})
	proc := &blockingPageProcess{}
	proc.gen.Store(1)

	origWho := pageWho
	origWho.Connection = "conn-original"
	s, err := h.pageReserve(origWho, "example-page", "home", proc, extension.UISlot{}, extension.UIParams{})
	if err != nil {
		t.Fatal(err)
	}
	h.uiPages.mu.Lock()
	s.run = 1
	s.busy = false
	s.suspended = true
	h.uiPages.mu.Unlock()

	newWho := pageWho
	newWho.Connection = "conn-new"
	resume := wire.UIStreamRequest{
		Request:   "resume",
		Protocol:  1,
		RequestID: "res-1",
		Session:   s.id,
		Epoch:     s.epoch,
		Version:   1,
	}
	raw, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &resume})
	// Extension is not registered in h.extensions.reg, so pageAuthorize fails.
	if _, err := h.uiStream(t.Context(), extwire.UIStreamCall{Who: newWho, Envelope: raw}); err == nil {
		t.Fatal("expected resume authorization failure")
	}

	h.uiPages.mu.Lock()
	gotWho := s.who
	h.uiPages.mu.Unlock()
	if gotWho != origWho {
		t.Fatalf("s.who after failed resume = %+v, want original %+v", gotWho, origWho)
	}
}

type blockingWaitPageProcess struct {
	blockingPageProcess
	waitEntered chan struct{}
	waitOnce    sync.Once
}

func (p *blockingWaitPageProcess) Wait(ctx context.Context) error {
	if p.waitEntered != nil {
		p.waitOnce.Do(func() { close(p.waitEntered) })
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestPageDropExtensionCancelsBlockedWaitInPageMount(t *testing.T) {
	h := New(Options{Config: &Config{}})
	proc := &blockingWaitPageProcess{
		waitEntered: make(chan struct{}),
	}
	proc.gen.Store(1)

	req := retryOpenRequest()
	s, reused, err := h.pageReserveOpen(pageWho, req, proc, extension.UISlot{}, extension.UIParams{})
	if err != nil || reused {
		t.Fatalf("pageReserveOpen: reused=%v err=%v", reused, err)
	}

	mountDone := make(chan error, 1)
	go func() {
		_, err := h.pageMount(t.Context(), s)
		mountDone <- err
	}()

	select {
	case <-proc.waitEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("pageMount did not enter process Wait")
	}

	// Dropping the extension must immediately cancel the awaiting mount rather than leaving it blocked for uiTimeout.
	h.pageDropExtension("example-page")

	select {
	case err := <-mountDone:
		if err == nil {
			t.Fatal("expected canceled mount error after pageDropExtension")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pageMount remained blocked in Wait after pageDropExtension")
	}
}

func TestPagePauseOnlyClosesOwnedChannel(t *testing.T) {
	h := New(Options{Config: &Config{}})
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	proc := &blockingPageProcess{
		suspendHook: func(ctx context.Context) error {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		},
	}
	proc.gen.Store(1)
	s, err := h.pageReserve(pageWho, "example-page", "home", proc, extension.UISlot{}, extension.UIParams{})
	if err != nil {
		t.Fatal(err)
	}

	done1 := make(chan struct{})
	done2 := make(chan struct{})
	h.uiPages.mu.Lock()
	s.pauseDone = done1
	h.uiPages.mu.Unlock()

	pauseDone := make(chan struct{})
	go func() {
		defer close(pauseDone)
		_ = h.pagePause(s, done1)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("ui.page.suspend did not start")
	}

	// Simulate a newer pause channel installed before the earlier pagePause finishes.
	h.uiPages.mu.Lock()
	s.pauseDone = done2
	h.uiPages.mu.Unlock()
	unblock()

	select {
	case <-pauseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pagePause did not return")
	}
	select {
	case <-done1:
	default:
		t.Fatal("owned pause channel done1 was not closed")
	}
	select {
	case <-done2:
		t.Fatal("earlier pagePause closed newer pause channel done2")
	default:
	}
	h.uiPages.mu.Lock()
	cur := s.pauseDone
	h.uiPages.mu.Unlock()
	if cur != done2 {
		t.Fatal("earlier pagePause cleared newer s.pauseDone")
	}
}
