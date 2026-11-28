package hub

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

func newPeerLinkTestHub(remoteID, credID string) *Hub {
	h := &Hub{
		cfg:           &Config{Name: "Local"},
		logger:        log.New(io.Discard, "", 0),
		wsConnections: map[*client]struct{}{},
		clients:       map[*client]struct{}{},
	}
	h.public.id = "hub-local"
	ms := &h.mesh
	ms.running = true
	ms.epoch = 1
	ms.links = map[string]*peerLink{}
	ms.peers = map[string]db.Peer{
		remoteID: {HubID: remoteID, Name: "Remote", CredentialID: credID},
	}
	ms.records = map[string]*hubRecord{
		"hub-other": {HubID: "hub-other", Name: "Other", Epoch: 1, Rev: 1},
	}
	ms.seenAt = map[string]time.Time{
		"hub-other": time.Now(),
	}
	ms.changed = make(chan struct{})
	ms.kick = make(chan struct{}, 1)
	return h
}

// wsFrameRW adapts a *websocket.Conn to io.ReadWriter without swallowing 1001 GoingAway into io.EOF
// (unlike websocket.NetConn), so tests can assert the exact wire close status code.
type wsFrameRW struct {
	ctx  context.Context
	conn *websocket.Conn
	buf  []byte
}

func (w *wsFrameRW) Read(p []byte) (int, error) {
	if len(w.buf) == 0 {
		_, data, err := w.conn.Read(w.ctx)
		if err != nil {
			return 0, err
		}
		w.buf = data
	}
	n := copy(p, w.buf)
	w.buf = w.buf[n:]
	return n, nil
}

func (w *wsFrameRW) Write(p []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func runPeerLinkHelloScenario(t *testing.T, dialerID, tokenID string, onHello func(h *Hub)) (closeStatus websocket.StatusCode, runErr error, linkRegistered bool, recEvents int32) {
	t.Helper()
	const (
		remoteID = "hub-remote"
		credID   = "tok-peer-1"
	)
	h := newPeerLinkTestHub(remoteID, credID)
	runDone := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			runDone <- err
			return
		}
		runDone <- h.runPeerLink(r.Context(), conn, remoteID, dialerID, "127.0.0.1", tokenID)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial peer: %v", err)
	}
	defer clientConn.CloseNow()

	var recs atomic.Int32
	rw := &wsFrameRW{ctx: ctx, conn: clientConn}
	p := rpc.New("r", rw)
	p.OnEvent = func(event string, _ json.RawMessage) {
		if event == "rec" {
			recs.Add(1)
		}
	}
	p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "hello" {
			onHello(h)
			return peerHello{HubID: remoteID, Name: "Remote", Epoch: 1}, nil
		}
		return map[string]any{}, nil
	}
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx, rw) }()

	// Wait for the hub's runPeerLink to finish or detect that it stayed registered and sent records.
	select {
	case runErr = <-runDone:
	case <-time.After(2 * time.Second):
		// Unpatched code stayed connected instead of rejecting; tear down so the test finishes cleanly.
		linkRegistered = h.peerLinkTo(remoteID) != nil
		_ = clientConn.CloseNow()
		select {
		case runErr = <-runDone:
		case <-ctx.Done():
			t.Fatal("runPeerLink did not exit after client close")
		}
	}

	var serveErr error
	select {
	case serveErr = <-served:
	case <-ctx.Done():
		t.Fatal("remote rpc.Serve did not exit")
	}
	if !linkRegistered {
		linkRegistered = h.peerLinkTo(remoteID) != nil
	}
	return websocket.CloseStatus(serveErr), runErr, linkRegistered, recs.Load()
}

func TestRunPeerLinkRejectsRevokedUnpairedAndStoppingDuringHello(t *testing.T) {
	t.Run("revoked_during_hello", func(t *testing.T) {
		code, runErr, registered, recs := runPeerLinkHelloScenario(t, "hub-remote", "tok-peer-1", func(h *Hub) {
			h.revoked("tok-peer-1")
		})
		if registered {
			t.Fatal("revoked peer link was registered into mesh.links")
		}
		if recs != 0 {
			t.Fatalf("revoked peer received %d mesh records, want 0", recs)
		}
		if code != peerCloseUnpaired {
			t.Fatalf("close status = %v (runErr=%v), want %v", code, runErr, peerCloseUnpaired)
		}
	})

	t.Run("outbound_revoked_during_async_unpair", func(t *testing.T) {
		// Outbound dialLoop links have tokenID == "". While unpairPeer is still running asynchronously
		// after credential revocation, peer.CredentialID is already marked revoked in h.devtok.
		code, runErr, registered, recs := runPeerLinkHelloScenario(t, "hub-local", "", func(h *Hub) {
			h.revoked("tok-peer-1")
		})
		if registered {
			t.Fatal("outbound peer link was registered while peer.CredentialID was revoked")
		}
		if recs != 0 {
			t.Fatalf("revoked outbound peer received %d mesh records, want 0", recs)
		}
		if code != peerCloseUnpaired {
			t.Fatalf("close status = %v (runErr=%v), want %v", code, runErr, peerCloseUnpaired)
		}
	})

	t.Run("unpaired_during_hello", func(t *testing.T) {
		code, runErr, registered, recs := runPeerLinkHelloScenario(t, "hub-remote", "tok-peer-1", func(h *Hub) {
			h.mesh.mu.Lock()
			delete(h.mesh.peers, "hub-remote")
			h.mesh.mu.Unlock()
		})
		if registered {
			t.Fatal("unpaired peer link was registered into mesh.links")
		}
		if recs != 0 {
			t.Fatalf("unpaired peer received %d mesh records, want 0", recs)
		}
		if code != peerCloseUnpaired {
			t.Fatalf("close status = %v (runErr=%v), want %v", code, runErr, peerCloseUnpaired)
		}
	})

	t.Run("credential_rotated_during_hello", func(t *testing.T) {
		code, runErr, registered, recs := runPeerLinkHelloScenario(t, "hub-remote", "tok-peer-1", func(h *Hub) {
			h.mesh.mu.Lock()
			row := h.mesh.peers["hub-remote"]
			row.CredentialID = "tok-peer-2"
			h.mesh.peers["hub-remote"] = row
			h.mesh.mu.Unlock()
		})
		if registered {
			t.Fatal("superseded peer credential was registered into mesh.links")
		}
		if recs != 0 {
			t.Fatalf("superseded peer received %d mesh records, want 0", recs)
		}
		if code != peerCloseUnpaired {
			t.Fatalf("close status = %v (runErr=%v), want %v", code, runErr, peerCloseUnpaired)
		}
	})

	t.Run("stopping_during_hello", func(t *testing.T) {
		code, runErr, registered, recs := runPeerLinkHelloScenario(t, "hub-remote", "tok-peer-1", func(h *Hub) {
			h.stopPeerLinks()
		})
		if registered {
			t.Fatal("peer link was registered after stopPeerLinks")
		}
		if recs != 0 {
			t.Fatalf("peer received %d mesh records after stopPeerLinks, want 0", recs)
		}
		if code != websocket.StatusGoingAway {
			t.Fatalf("close status after stopPeerLinks = %v (runErr=%v), want %v", code, runErr, websocket.StatusGoingAway)
		}
	})

	t.Run("concurrent_revoke_and_stop_race", func(t *testing.T) {
		for range 8 {
			const (
				remoteID = "hub-remote"
				credID   = "tok-peer-race"
			)
			h := newPeerLinkTestHub(remoteID, credID)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
				if err != nil {
					return
				}
				_ = h.runPeerLink(r.Context(), conn, remoteID, remoteID, "127.0.0.1", credID)
			}))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
			entered := make(chan struct{}, 4)

			var wg sync.WaitGroup
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c, _, err := websocket.Dial(ctx, wsURL, nil)
					if err != nil {
						return
					}
					defer c.CloseNow()
					nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
					p := rpc.New("r", nc)
					p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
						if method == "hello" {
							select {
							case entered <- struct{}{}:
							default:
							}
							return peerHello{HubID: remoteID, Name: "Remote", Epoch: 1}, nil
						}
						return map[string]any{}, nil
					}
					_ = p.Serve(ctx, nc)
				}()
			}

			select {
			case <-entered:
			case <-ctx.Done():
				cancel()
				wg.Wait()
				srv.Close()
				t.Fatal("timed out waiting for peer hello")
			}

			// Race revocation and shutdown concurrently against in-flight runPeerLink registration.
			var stopWG sync.WaitGroup
			stopWG.Add(2)
			go func() {
				defer stopWG.Done()
				h.revoked(credID)
			}()
			go func() {
				defer stopWG.Done()
				h.stopPeerLinks()
			}()
			stopWG.Wait()
			wg.Wait()
			cancel()
			srv.Close()

			if l := h.peerLinkTo(remoteID); l != nil {
				t.Fatal("revoked/stopped peer link remained registered in mesh.links")
			}
		}
	})
}

// Concurrent registerLink (h.mesh.mu -> h.devtok.mu), lookupToken touch (h.devtok.mu, then
// lock-free h.quiesce.begin), and meshPersist while paused (h.quiesce.mu -> h.mesh.mu) must not
// form a 3-lock deadlock cycle.
func TestRegisterLinkLookupTokenMeshPersistNoDeadlock(t *testing.T) {
	h := newTestHub(t)
	h.public.id = "hub-local"
	tok := "jd_fixture_peer_lock_order_test"
	const (
		remoteID = "hub-remote"
		credID   = "tok-peer-lock"
	)
	store := &tokenTouchStore{
		meshStore: newMeshStore(),
		token:     &db.DeviceToken{ID: "dev_lock_order", Name: "Phone", Kind: "phone", Role: roleFull},
		hash:      hashToken(tok),
	}
	h.store = store
	ms := &h.mesh
	ms.mu.Lock()
	ms.running = true
	ms.peers = map[string]db.Peer{
		remoteID: {HubID: remoteID, Name: "Remote", CredentialID: credID},
	}
	ms.links = map[string]*peerLink{}
	ms.records = map[string]*hubRecord{
		"hub-other": {HubID: "hub-other", Name: "Other", Epoch: 1, Rev: 1},
	}
	ms.seenAt = map[string]time.Time{"hub-other": time.Now()}
	if ms.changed == nil {
		ms.changed = make(chan struct{})
	}
	if ms.kick == nil {
		ms.kick = make(chan struct{}, 1)
	}
	ms.mu.Unlock()

	if err := h.quiesce.pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.quiesce.resume() })
	h.quiesce.mu.Lock()
	h.quiesce.testOnPauseDefer = runtime.Gosched
	h.quiesce.mu.Unlock()

	const iters = 200
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			for range iters {
				h.meshPersist(true)
			}
		}()
		go func() {
			defer wg.Done()
			for range iters {
				lctx, lcancel := context.WithCancel(ctx)
				l := &peerLink{
					hubID:   remoteID,
					dialer:  remoteID,
					tokenID: credID,
					ctx:     lctx,
					cancel:  func(error) { lcancel() },
				}
				_ = h.registerLink(l)
				h.unregisterLink(l)
			}
		}()
		go func() {
			defer wg.Done()
			for range iters {
				_, ok, err := h.lookupToken(ctx, tok)
				if err != nil || !ok {
					t.Errorf("lookupToken failed: ok=%v err=%v", ok, err)
					return
				}
			}
		}()
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock across registerLink, lookupToken, and paused meshPersist")
	}
}
