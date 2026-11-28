package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/rpc"
)

func newRelayStopTestHub(t *testing.T) (*Hub, *atomic.Int32, func()) {
	t.Helper()
	var acceptCalls atomic.Int32
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()

	hubPeer := rpc.New("h", c2sW)
	mgrPeer := rpc.New("m", s2cW)
	mgrPeer.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == devicewire.MethodAccept {
			acceptCalls.Add(1)
			return nil, errors.New("test manager declines accept")
		}
		return nil, errors.New("unexpected method: " + method)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()
			_ = c2sR.Close()
			_ = c2sW.Close()
			_ = s2cR.Close()
			_ = s2cW.Close()
		})
	}
	t.Cleanup(cleanup)
	go func() { _ = hubPeer.Serve(ctx, s2cR) }()
	go func() { _ = mgrPeer.Serve(ctx, c2sR) }()

	h := &Hub{
		cfg:           &Config{},
		logger:        log.New(io.Discard, "", 0),
		wsConnections: map[*client]struct{}{},
		clients:       map[*client]struct{}{},
	}
	h.devices.kept = map[string]string{"m1": "mgr-1"}
	h.bus.current = map[string]*busConn{
		"mgr-1": {peer: hubPeer},
	}
	return h, &acceptCalls, cleanup
}

// Concurrent stopWebSockets and relayLinkConn must be race-free under -race:
// relayLinkConn must not read h.wsStopping (guarded by h.mu) while holding rs.mu.
func TestRelayLinkConnStopWebSocketsRace(t *testing.T) {
	for range 15 {
		h, _, cleanup := newRelayStopTestHub(t)
		entered := make(chan struct{}, 16)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case entered <- struct{}{}:
			default:
			}
			h.relayLinkConn(w, r, devicewire.Accept{
				Kind:      devicewire.AcceptLink,
				MachineID: "m1",
				TokenID:   "tok-1",
			}, &websocket.AcceptOptions{InsecureSkipVerify: true}, 1<<20)
		}))

		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		var wg sync.WaitGroup
		stopDials := make(chan struct{})
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stopDials:
						return
					default:
					}
					conn, _, err := websocket.Dial(ctx, wsURL, nil)
					if err != nil {
						continue
					}
					_, _, _ = conn.Read(ctx)
					_ = conn.CloseNow()
				}
			}()
		}

		select {
		case <-entered:
		case <-ctx.Done():
			close(stopDials)
			wg.Wait()
			cancel()
			srv.Close()
			cleanup()
			t.Fatal("timed out waiting for relayLinkConn to start")
		}

		if err := h.stopWebSockets(ctx); err != nil {
			close(stopDials)
			wg.Wait()
			cancel()
			srv.Close()
			cleanup()
			t.Fatalf("stopWebSockets: %v", err)
		}
		close(stopDials)
		wg.Wait()
		cancel()
		srv.Close()
		cleanup()
	}
}

// Once stopLinks has snapshotted active links for shutdown, any newly accepted link
// must see rs.stopping under rs.mu, close immediately with 1001 GoingAway, and never
// enter handToManager (which could otherwise miss shutdown close and stall wsHandlers.Wait).
func TestRelayLinkConnAfterStopLinksClosesGoingAway(t *testing.T) {
	h, acceptCalls, _ := newRelayStopTestHub(t)
	_ = h.stopLinks()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.relayLinkConn(w, r, devicewire.Accept{
			Kind:      devicewire.AcceptLink,
			MachineID: "m1",
			TokenID:   "tok-1",
		}, &websocket.AcceptOptions{InsecureSkipVerify: true}, 1<<20)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	_, _, err = conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
		t.Fatalf("close status after stopLinks = %v (err=%v), want %v", got, err, websocket.StatusGoingAway)
	}
	if calls := acceptCalls.Load(); calls != 0 {
		t.Fatalf("handToManager called %d times after stopLinks, want 0", calls)
	}
}

// If a link credential is revoked in the window between linkCaller authentication and
// registration in rs.links, relayLinkConn must reject it with CloseRevoked (4002)
// under rs.mu instead of admitting and handing the revoked link to the manager.
func TestRelayLinkConnRevokedBetweenAuthAndRegistrationClosesRevoked(t *testing.T) {
	h, acceptCalls, _ := newRelayStopTestHub(t)
	h.revoked("tok-revoked")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.relayLinkConn(w, r, devicewire.Accept{
			Kind:      devicewire.AcceptLink,
			MachineID: "m1",
			TokenID:   "tok-revoked",
		}, &websocket.AcceptOptions{InsecureSkipVerify: true}, 1<<20)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	_, _, err = conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != link.CloseRevoked {
		t.Fatalf("close status for revoked token = %v (err=%v), want %v", got, err, link.CloseRevoked)
	}
	if calls := acceptCalls.Load(); calls != 0 {
		t.Fatalf("handToManager called %d times for revoked token, want 0", calls)
	}
}
