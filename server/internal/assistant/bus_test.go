package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The hub relays assistantwire.Routes (it does not run this package): they are exactly the ones the assistant serves.
func TestRoutesAreTheWireOnes(t *testing.T) {
	needsLarkLayer(t)
	s := newTestService(t, newFakeHub())
	got, want := s.Routes(), slices.Concat(assistantwire.Routes, assistantwire.InboxRoutes)
	if !s.briefingOn() {
		want = slices.DeleteFunc(want, func(r string) bool { return r == "POST /api/v1/later/{id}/handled" })
	}
	if !s.recordingOn() { // the recording module's routes are served only with it (recording.go)
		want = slices.DeleteFunc(want, func(r string) bool {
			// Stored photos' list and images are the core's own routes (kb_recording_media.go): they stay.
			return strings.Contains(r, " /api/v1/recordings") && r != "GET /api/v1/recordings/{id}/photos" &&
				r != "GET /api/v1/recordings/{id}/photos/{n}"
		})
	}
	if !s.healthOn() { // the Health module's routes likewise (health_layer.go); workouts stay in the core
		want = slices.DeleteFunc(want, func(r string) bool {
			return strings.Contains(r, " /api/v1/assistant/health/intake") ||
				strings.Contains(r, " /api/v1/assistant/health/items") || strings.Contains(r, " /api/v1/assistant/health/water/")
		})
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the assistant serves %v\nthe hub relays %v", got, want)
	}
}

// The hub's coded errors get their kind back over the bus (the assistant's code checks errors.Is), with the hub's
// message; others stay as they came.
func TestBusErrorKinds(t *testing.T) {
	for code, is := range map[string]func(error) bool{
		assistantwire.CodeNoDevice:       func(err error) bool { return errors.Is(err, ErrNoDevice) },
		assistantwire.CodeMachineOffline: func(err error) bool { return errors.Is(err, apierr.ErrOffline) },
		assistantwire.CodeNodeClosed:     func(err error) bool { return errors.Is(err, rpc.ErrClosed) },
		assistantwire.CodeStaleSnapshot:  func(err error) bool { return errors.Is(err, ErrStaleSnapshot) },
	} {
		err := busError(&rpc.Error{Code: code, Message: "说明"})
		if !is(err) || err.Error() != "说明" {
			t.Errorf("%s: %v (%T)", code, err, err)
		}
	}
	var re *rpc.Error
	if err := busError(&rpc.Error{Code: "permission_denied", Message: "不行"}); !errors.As(err, &re) || re.Code != "permission_denied" {
		t.Errorf("a node's own refusal: %v", err)
	}
	if err := busError(context.DeadlineExceeded); err != context.DeadlineExceeded {
		t.Errorf("not coded: %v", err)
	}
	if busError(nil) != nil {
		t.Error("nil")
	}
}

// The whole snapshot a new connection reports reaches the hub: it is sent once the connection counts as up, not
// while the hello's answer is still being kept (bus.Client calls OnHello before the connection is up, and a snapshot
// sent then was refused as offline, so the hub showed nothing of the assistant until its next change).
func TestBusSnapshotWaitsForTheConnection(t *testing.T) {
	snapshots := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodHello:
				return map[string]any{}, nil
			case assistantwire.MethodSnapshot:
				snapshots <- struct{}{}
				return map[string]any{}, nil
			}
			return nil, &rpc.Error{Code: "not_found", Message: method}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	b := NewBusHub(BusOptions{Hub: srv.URL, Token: "jb_x", Logf: testLogf(t)})
	// Widen the gap between the hello's answer and the connection counting as up, so a snapshot sent in it fails.
	onHello := b.client.OnHello
	b.client.OnHello = func(res json.RawMessage) error {
		err := onHello(res)
		time.Sleep(100 * time.Millisecond)
		return err
	}
	st := &memStore{}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()

	select {
	case <-snapshots:
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot reached the hub after connecting")
	}
}

// The hub's own manager offers the hub no compression (bus.Client.Uncompressed): its child, and the built-in manager's
// service dialing the hub's local address. A manager elsewhere, or one made in the app, compresses as before (a
// manager's own service is DeployService too, manager/service.go).
func TestBusHubUncompressedOnTheHubsMachine(t *testing.T) {
	for _, c := range []struct {
		deploy, id, hub string
		want            bool
	}{
		{"child", "", "http://127.0.0.1:18660", true},
		{"child", "", "http://198.51.100.5:18660", true},
		{DeployService, "", "http://127.0.0.1:18660", true},
		{DeployService, assistantwire.AssistantID, "http://localhost:18660", true},
		{DeployService, assistantwire.AssistantID, "http://[::1]:18660", true},
		{DeployService, assistantwire.AssistantID, "https://203.0.113.7:18661", false},
		{DeployService, "m_1", "http://127.0.0.1:18660", false},
		{"external", "", "http://127.0.0.1:18660", false},
		{"docker", "", "http://127.0.0.1:18660", false},
	} {
		b := NewBusHub(BusOptions{ID: c.id, Deploy: c.deploy, Hub: c.hub, Token: "t"})
		if b.client.Uncompressed != c.want {
			t.Errorf("%+v: Uncompressed %v", c, b.client.Uncompressed)
		}
	}
}

// BusHub sends the host's machine_uid in manager.hello, omits pending_restart when false, sends manager.status live on
// SetPendingRestart changes, and carries pending_restart: true in manager.hello across reconnects.
func TestBusHubHelloMachineUIDAndPendingRestart(t *testing.T) {
	hostDir := t.TempDir()
	const uid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := os.WriteFile(filepath.Join(hostDir, "machine-uid"), []byte(uid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hellos := make(chan map[string]any, 4)
	statuses := make(chan assistantwire.ManagerStatus, 8)
	var mu sync.Mutex
	var currentWS *websocket.Conn
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		currentWS = ws
		mu.Unlock()
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodHello:
				var m map[string]any
				_ = json.Unmarshal(params, &m)
				hellos <- m
				return map[string]any{}, nil
			case assistantwire.MethodSnapshot:
				return map[string]any{}, nil
			}
			return nil, &rpc.Error{Code: "not_found", Message: method}
		}
		p.OnEvent = func(event string, params json.RawMessage) {
			if event == assistantwire.EventManagerStatus {
				var st assistantwire.ManagerStatus
				if json.Unmarshal(params, &st) == nil {
					statuses <- st
				}
			}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	b := NewBusHub(BusOptions{ID: "m-123456", Hub: srv.URL, Token: "jb_x", HostDataDir: hostDir, Backoff: 10 * time.Millisecond, Logf: testLogf(t)})
	st := &memStore{}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()

	select {
	case h1 := <-hellos:
		if h1["machine_uid"] != uid {
			t.Fatalf("first hello machine_uid = %v, want %q", h1["machine_uid"], uid)
		}
		if _, has := h1["pending_restart"]; has {
			t.Fatalf("first hello should omit pending_restart: %v", h1)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first manager.hello")
	}
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	b.SetPendingRestart(true)
	select {
	case got := <-statuses:
		if !got.PendingRestart {
			t.Fatalf("status = %+v, want pending_restart=true", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for manager.status true")
	}

	// Force a disconnect: the reconnecting manager.hello must carry pending_restart: true and machine_uid.
	mu.Lock()
	ws := currentWS
	mu.Unlock()
	_ = ws.Close(websocket.StatusGoingAway, "drop")
	select {
	case h2 := <-hellos:
		if h2["machine_uid"] != uid || h2["pending_restart"] != true {
			t.Fatalf("reconnect hello = %v, want machine_uid=%q pending_restart=true", h2, uid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect manager.hello")
	}
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	b.SetPendingRestart(false)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-statuses:
			if !got.PendingRestart {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for manager.status false")
		}
	}
}

// A switch to Persona Link that stops waiting for the hub's begin (its time ran out, devices link_switch.go) tells the
// hub so (rpc.cancel), which ends the begin's handler: the hub gives it up instead of switching the computer for a
// manager that no longer waits.
func TestBusCancelsALinkSwitchBeginGivenUp(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodHello, assistantwire.MethodSnapshot:
				return map[string]any{}, nil
			case devicewire.MethodLinkSwitchBegin:
				select {
				case <-ctx.Done():
					cancelled <- struct{}{}
				case <-time.After(10 * time.Second):
				}
				return nil, ctx.Err()
			}
			return nil, &rpc.Error{Code: "not_found", Message: method}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	b := NewBusHub(BusOptions{Hub: srv.URL, Token: "jb_x", Logf: testLogf(t)})
	st := &memStore{}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := b.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer ccancel()
	if err := b.Client().Call(cctx, devicewire.MethodLinkSwitchBegin, devicewire.LinkSwitchBegin{MachineID: "box", Operation: "ls_1"}, nil); err == nil {
		t.Fatal("begin answered")
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub's begin was not told the manager gave it up")
	}
}
