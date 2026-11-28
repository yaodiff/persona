//go:build unix

package hubedge

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listenUnixSlot(t *testing.T, dir, name string) (string, net.Listener, chan []byte, *atomic.Int32) {
	t.Helper()
	path := filepath.Join(dir, name+".sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	headers := make(chan []byte, 32)
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 64)
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				n, _ := conn.Read(buf)
				if n > 0 {
					headers <- append([]byte(nil), buf[:n]...)
				}
			}(c)
		}
	}()
	return path, ln, headers, &accepted
}

// A connection held in dialActive during switchTo's hold -> release(old) -> setActive(green)
// window must re-read e.active after gate unblocks and connect directly to green rather than
// dialing the stale old slot captured before <-gate.
func TestDialActiveHeldDuringSwitchDialsNewActiveSlotDirectly(t *testing.T) {
	for _, clearBeforeActivate := range []bool{false, true} {
		name := "direct_switch"
		if clearBeforeActivate {
			name = "nil_then_activate"
		}
		t.Run(name, func(t *testing.T) {
			// Use a short directory under /tmp to stay well within Unix socket sun_path limits on macOS.
			dir, err := os.MkdirTemp("/tmp", "hubedge-dial-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })

			oldPath, _, _, oldAccepted := listenUnixSlot(t, dir, "old")
			newPath, _, newHeaders, newAccepted := listenUnixSlot(t, dir, "new")

			oldSlot := &proc{name: "a", sockets: map[string]string{"443": oldPath}}
			newSlot := &proc{name: "b", sockets: map[string]string{"443": newPath}}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gateWaiting := make(chan struct{})
			var waitOnce sync.Once
			e := &edge{
				o:          Options{HoldMax: 5 * time.Second, Logf: func(string, ...any) {}},
				ctx:        ctx,
				gate:       make(chan struct{}),
				held:       true,
				onGateWait: func() { waitOnce.Do(func() { close(gateWaiting) }) },
			}
			e.setActive(oldSlot)

			// Enter hold while e.active is still oldSlot, mirroring switchTo before release(old) exits.
			e.hold()

			type dialRes struct {
				p    *proc
				conn net.Conn
			}
			header := []byte("PROXY-V2-TEST")
			dialDone := make(chan dialRes, 1)
			go func() {
				p, c := e.dialActive("443", header)
				dialDone <- dialRes{p: p, conn: c}
			}()
			select {
			case <-gateWaiting:
			case <-time.After(3 * time.Second):
				t.Fatal("dialActive did not reach hold gate wait")
			}
			// Verify dialActive stays blocked while held.
			select {
			case res := <-dialDone:
				if res.conn != nil {
					_ = res.conn.Close()
				}
				t.Fatalf("dialActive returned %+v while edge was held", res)
			default:
			}

			if clearBeforeActivate {
				e.setActive(nil)
			}
			e.setActive(newSlot)

			var res dialRes
			select {
			case res = <-dialDone:
			case <-time.After(3 * time.Second):
				t.Fatal("dialActive did not unblock after setActive(newSlot)")
			}
			if res.conn == nil || res.p != newSlot {
				if res.conn != nil {
					_ = res.conn.Close()
				}
				t.Fatalf("dialActive returned (p=%v, conn=%v), want newSlot", res.p, res.conn != nil)
			}
			defer res.conn.Close()

			select {
			case got := <-newHeaders:
				if !bytes.Equal(got, header) {
					t.Fatalf("new slot received header %q, want %q", got, header)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("new slot did not receive PROXY header")
			}
			if got := oldAccepted.Load(); got != 0 {
				t.Fatalf("stale old slot accepted %d connection(s) during switch, want 0", got)
			}
			if got := newAccepted.Load(); got != 1 {
				t.Fatalf("new slot accepted %d connection(s), want 1", got)
			}
		})
	}
}

// setActive(nil) must atomically engage hold under e.mu so there is never a window where
// e.active == nil while e.gate is closed (which would cause dialActive to busy-spin).
func TestSetActiveNilHoldsAtomically(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hubedge-nil-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	oldPath, _, _, _ := listenUnixSlot(t, dir, "old")
	newPath, _, newHeaders, _ := listenUnixSlot(t, dir, "new")
	oldSlot := &proc{name: "a", sockets: map[string]string{"443": oldPath}}
	newSlot := &proc{name: "b", sockets: map[string]string{"443": newPath}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateWaiting := make(chan struct{})
	var waitOnce sync.Once
	e := &edge{
		o:          Options{HoldMax: 5 * time.Second, Logf: func(string, ...any) {}},
		ctx:        ctx,
		gate:       make(chan struct{}),
		held:       true,
		onGateWait: func() { waitOnce.Do(func() { close(gateWaiting) }) },
	}
	e.setActive(oldSlot)

	// Call setActive(nil) WITHOUT calling hold() separately.
	e.setActive(nil)

	e.mu.Lock()
	held, gate, active := e.held, e.gate, e.active
	e.mu.Unlock()
	if !held || active != nil {
		t.Fatalf("after setActive(nil): held=%v active=%v, want held=true active=nil", held, active)
	}
	select {
	case <-gate:
		t.Fatal("gate remained closed after setActive(nil)")
	default:
	}

	done := make(chan *proc, 1)
	go func() {
		p, c := e.dialActive("443", []byte("HDR"))
		if c != nil {
			_ = c.Close()
		}
		done <- p
	}()
	select {
	case <-gateWaiting:
	case <-time.After(3 * time.Second):
		t.Fatal("dialActive did not reach hold gate after setActive(nil)")
	}
	e.setActive(newSlot)
	select {
	case p := <-done:
		if p != newSlot {
			t.Fatalf("dialActive returned %v, want newSlot", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dialActive did not complete after setActive(newSlot)")
	}
	select {
	case got := <-newHeaders:
		if string(got) != "HDR" {
			t.Fatalf("header = %q, want HDR", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new slot did not receive header")
	}
}

func TestDialActiveConcurrentSlotSwitchesRace(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hubedge-race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	pathA := filepath.Join(dir, "a.sock")
	pathB := filepath.Join(dir, "b.sock")
	lnA, err := net.Listen("unix", pathA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lnA.Close() })
	lnB, err := net.Listen("unix", pathB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lnB.Close() })
	acceptAll := func(ln net.Listener) {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}(c)
		}
	}
	go acceptAll(lnA)
	go acceptAll(lnB)

	slotA := &proc{name: "a", sockets: map[string]string{"443": pathA}}
	slotB := &proc{name: "b", sockets: map[string]string{"443": pathB}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &edge{
		o:    Options{HoldMax: 5 * time.Second, Logf: func(string, ...any) {}},
		ctx:  ctx,
		gate: make(chan struct{}),
		held: true,
	}
	e.setActive(slotA)

	var wg sync.WaitGroup
	stopSwitch := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stopSwitch:
				e.setActive(slotA)
				return
			default:
			}
			e.hold()
			runtime.Gosched()
			e.setActive(nil)
			runtime.Gosched()
			if i%2 == 0 {
				e.setActive(slotB)
			} else {
				e.setActive(slotA)
			}
			// Yield while unheld so dialers can observe the active slot under GOMAXPROCS=1.
			runtime.Gosched()
		}
	}()

	var dialWG sync.WaitGroup
	for range 4 {
		dialWG.Add(1)
		go func() {
			defer dialWG.Done()
			for range 12 {
				p, c := e.dialActive("443", []byte("X"))
				if p == nil || c == nil {
					t.Errorf("dialActive failed unexpectedly: p=%v c=%v", p, c != nil)
					return
				}
				_ = c.Close()
			}
		}()
	}
	dialWG.Wait()
	close(stopSwitch)
	wg.Wait()
}
