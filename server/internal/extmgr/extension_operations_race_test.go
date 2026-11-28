package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

type interceptHub struct {
	Host
	onNotify func(event string, params any)
}

func (i *interceptHub) Notify(event string, params any) error {
	if i.onNotify != nil {
		i.onNotify(event, params)
	}
	return i.Host.Notify(event, params)
}

func TestWaitStartedConcurrentOpUpdateNoDataRace(t *testing.T) {
	h, _, _ := marketHub(t)
	gate := make(chan struct{})
	p := host.Start(context.Background(), host.Spec{
		ID:      "demo",
		Name:    "Demo",
		Startup: true,
		Check: func(ctx context.Context) error {
			select {
			case <-gate:
				return errors.New("start boom")
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Logf: t.Logf,
	})
	defer p.Stop()

	h.extensions.mu.Lock()
	h.extensions.byID["demo"] = &hubExtension{proc: p}
	h.extensions.mu.Unlock()

	op := h.startOp(opUpgrade, "demo", "", i18n.Default, false)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			h.update(op, func(v *ExtensionOperation) bool {
				v.Name = "Demo"
				v.Version = "2.0.0"
				v.From = "1.0.0"
				return i == 0
			})
		}
		close(gate)
	}()

	err := h.waitStarted(context.Background(), "demo", true, op)
	wg.Wait()
	if !errors.Is(err, errRolledBack) {
		t.Fatalf("waitStarted = %v, want errRolledBack", err)
	}
}

func TestWaitStartedDoesNotMissStateTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 32 {
			gate := make(chan struct{})
			var attempts atomic.Int32
			p := host.Start(context.Background(), host.Spec{
				ID:      "demo",
				Name:    "Demo",
				Startup: true,
				Check: func(ctx context.Context) error {
					if attempts.Add(1) == 1 {
						select {
						case <-gate:
							return errors.New("boom")
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					<-ctx.Done()
					return ctx.Err()
				},
				Logf: t.Logf,
			})
			h := &Manager{extensions: &extensions{
				byID: map[string]*hubExtension{"demo": {proc: p}},
			}}
			op := &extOp{v: ExtensionOperation{ID: "op-1", Name: "Demo", Version: "2.0.0", From: "1.0.0"}}

			waitErr := make(chan error, 1)
			go func() {
				waitErr <- h.waitStarted(context.Background(), "demo", true, op)
			}()
			close(gate)

			err := <-waitErr
			p.Stop()
			if !errors.Is(err, errRolledBack) {
				t.Fatalf("waitStarted missed transition and returned %v, want errRolledBack", err)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("expected waitStarted to catch first failure, saw %d check attempts", got)
			}
		}
	})
}

func watchOpState(h *Manager, wantState string) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	prev := h.opts.Hub
	h.opts.Hub = &interceptHub{
		Host: prev,
		onNotify: func(event string, params any) {
			if event != assistantwire.EventBroadcast {
				return
			}
			b, ok := params.(assistantwire.Broadcast)
			if !ok {
				return
			}
			var env struct {
				Type      string `json:"type"`
				Operation struct {
					State string `json:"state"`
				} `json:"operation"`
			}
			if json.Unmarshal(b.Message, &env) == nil && env.Type == "extension_operation" && env.Operation.State == wantState {
				once.Do(func() { close(ch) })
			}
		},
	}
	return ch
}

func TestDeleteOperationCancelRacesWithLaunchAndPreview(t *testing.T) {
	t.Run("cancel while preview finishes prevents needs_consent and discards preview", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		canceledPushed := watchOpState(h, opCanceled)
		mux := h.routes()
		pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+notesPackage(t, "1.0.0"), "")
		if err != nil {
			t.Fatal(err)
		}

		inRun := make(chan struct{})
		releaseRun := make(chan struct{})
		op, _, err := h.launch(context.Background(), previewJob{
			kind:     opInstall,
			id:       pv.Manifest.ID,
			ofMarket: true,
			lang:     i18n.Default,
			run: func(ctx context.Context) (*extension.Preview, error) {
				close(inRun)
				<-releaseRun
				// Simulate preview completing right as cancellation arrived.
				return pv, nil
			},
		}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		<-inRun

		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+op.v.ID, phone, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE while previewing = %d %v, want 204", code, out)
		}
		close(releaseRun)
		<-canceledPushed

		v := h.snapshot(op)
		if v.State != opCanceled || v.PreviewID != "" || v.EndedAt == nil {
			t.Fatalf("operation after cancel+preview finish = %+v, want canceled with empty preview_id and ended_at", v)
		}
		if h.extensions.reg.PreviewOf(pv.ID) != nil {
			t.Fatal("canceled preview was not discarded from registry")
		}
	})

	t.Run("cancel while auto-consented retry preview finishes does not install", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		canceledPushed := watchOpState(h, opCanceled)
		mux := h.routes()
		pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+notesPackage(t, "1.0.0"), "")
		if err != nil {
			t.Fatal(err)
		}

		from := &extOp{consented: pv.Digest, consentedBundle: map[string]string{}}
		inRun := make(chan struct{})
		releaseRun := make(chan struct{})
		op, _, err := h.launch(context.Background(), previewJob{
			kind:     opInstall,
			id:       pv.Manifest.ID,
			ofMarket: true,
			lang:     i18n.Default,
			run: func(ctx context.Context) (*extension.Preview, error) {
				close(inRun)
				<-releaseRun
				return pv, nil
			},
		}, from, true)
		if err != nil {
			t.Fatal(err)
		}
		<-inRun

		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+op.v.ID, phone, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE while auto-retry previewing = %d %v, want 204", code, out)
		}
		close(releaseRun)
		<-canceledPushed

		v := h.snapshot(op)
		if v.State != opCanceled || v.PreviewID != "" || v.EndedAt == nil {
			t.Fatalf("auto-retry op after cancel+preview finish = %+v, want canceled", v)
		}
		if h.extensions.reg.PreviewOf(pv.ID) != nil {
			t.Fatal("canceled auto-retry preview was not discarded")
		}
		if h.extensions.reg.Get(pv.Manifest.ID) != nil {
			t.Fatal("canceled auto-retry operation installed the extension")
		}
	})

	t.Run("immediate DELETE on initial launch broadcast cancels instead of 409", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		mux := h.routes()
		var deleteCode atomic.Int32
		var cancelOnce sync.Once
		h.opts.Hub = &interceptHub{
			Host: h.opts.Hub,
			onNotify: func(event string, params any) {
				if event != assistantwire.EventBroadcast {
					return
				}
				b, ok := params.(assistantwire.Broadcast)
				if !ok {
					return
				}
				var env struct {
					Type      string `json:"type"`
					Operation struct {
						ID string `json:"id"`
					} `json:"operation"`
				}
				if json.Unmarshal(b.Message, &env) != nil || env.Type != "extension_operation" || env.Operation.ID == "" {
					return
				}
				cancelOnce.Do(func() {
					code, _ := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+env.Operation.ID, phone, "")
					deleteCode.Store(int32(code))
				})
			},
		}

		sawCancel := make(chan error, 1)
		runDone := make(chan struct{})
		op, _, err := h.launch(context.Background(), previewJob{
			kind:     opInstall,
			id:       "notes",
			ofMarket: true,
			lang:     i18n.Default,
			run: func(ctx context.Context) (*extension.Preview, error) {
				defer close(runDone)
				select {
				case <-ctx.Done():
					sawCancel <- context.Cause(ctx)
					return nil, ctx.Err()
				default:
					if deleteCode.Load() != http.StatusNoContent {
						sawCancel <- errors.New("not canceled during launch")
						return nil, errors.New("not canceled")
					}
					<-ctx.Done()
					sawCancel <- context.Cause(ctx)
					return nil, ctx.Err()
				}
			},
		}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		<-runDone

		if got := deleteCode.Load(); got != http.StatusNoContent {
			t.Fatalf("immediate DELETE on startOp broadcast = %d, want 204", got)
		}
		cause := <-sawCancel
		if !errors.Is(cause, extension.ErrUserCanceled) {
			t.Fatalf("cancel cause = %v, want ErrUserCanceled", cause)
		}
		if v := h.snapshot(op); v.State != opCanceled {
			t.Fatalf("state after immediate cancel = %s, want canceled", v.State)
		}
	})

	t.Run("concurrent DELETE and claimOpOfPreview never discard a claimed preview", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		mux := h.routes()
		pkgDir := notesPackage(t, "1.0.0")

		for i := range 24 {
			pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+pkgDir, "")
			if err != nil {
				t.Fatal(err)
			}
			op := h.startOp(opInstall, pv.Manifest.ID, "", i18n.Default, true)
			h.opPreviewed(op, pv, nil)

			start := make(chan struct{})
			var wg sync.WaitGroup
			var delCode int
			var claimed *extOp
			var claimErr error

			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				delCode, _ = call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+op.v.ID, phone, "")
			}()
			go func() {
				defer wg.Done()
				<-start
				claimed, claimErr = h.claimOpOfPreview(pv.ID)
			}()
			close(start)
			wg.Wait()

			if claimErr != nil {
				t.Fatalf("iter %d: unexpected claim error: %v", i, claimErr)
			}
			v := h.snapshot(op)
			if claimed != nil {
				if delCode != http.StatusConflict {
					t.Fatalf("iter %d: claimed op got DELETE %d, want 409", i, delCode)
				}
				if h.extensions.reg.PreviewOf(pv.ID) == nil {
					t.Fatalf("iter %d: racing DELETE discarded preview of claimed operation", i)
				}
				if v.State != opRunning {
					t.Fatalf("iter %d: claimed op state = %s, want running", i, v.State)
				}
				h.extensions.reg.Discard(pv.ID)
			} else {
				if delCode != http.StatusNoContent {
					t.Fatalf("iter %d: unclaimed op got DELETE %d, want 204", i, delCode)
				}
				if h.extensions.reg.PreviewOf(pv.ID) != nil {
					t.Fatalf("iter %d: canceled preview still in registry", i)
				}
				if v.State != opCanceled {
					t.Fatalf("iter %d: canceled op state = %s, want canceled", i, v.State)
				}
			}
		}
	})

	t.Run("concurrent DELETE and POST /install never both succeed on a canceled preview", func(t *testing.T) {
		pkgDir := uiPackage(t)
		v1 := tarDir(t, pkgDir)
		gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{"example-ui-1.0.0.tar.gz": v1}}
		srv := httptest.NewTLSServer(gh)
		defer srv.Close()
		storage := httptest.NewTLSServer(gh)
		defer storage.Close()
		gh.storage = "https://storage.example.com"
		gh.files[extension.IndexFile] = marketIndex(t, "Example plugins", marketEntry(t, pkgDir, "release:v1/example-ui-1.0.0.tar.gz", v1))

		newMarketHub := func(t *testing.T) (*Manager, http.Handler, string, string) {
			t.Helper()
			h, phone, _ := marketHub(t)
			mux := h.routes()
			tr := srv.Client().Transport.(*http.Transport).Clone()
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr == "storage.example.com:443" {
					addr = storage.Listener.Addr().String()
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}
			h.extensions.markets.client, h.extensions.markets.githubAPI = &http.Client{Transport: tr}, srv.URL
			_, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, `{"spec": "github:example/plugins"}`)
			return h, mux, phone, mk["id"].(string)
		}

		previewMarket := func(t *testing.T, h *Manager, mux http.Handler, phone, marketID string) (opID, previewID, digest string) {
			t.Helper()
			code, out := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "extension": "example-ui"}`, marketID))
			if code != http.StatusOK {
				t.Fatalf("catalog/preview = %d %v, want 200", code, out)
			}
			opID, _ = out["operation_id"].(string)
			previewID, _ = out["preview_id"].(string)
			digest, _ = out["permissions_digest"].(string)
			if opID == "" || previewID == "" || digest == "" {
				t.Fatalf("missing fields from catalog/preview: %v", out)
			}
			return opID, previewID, digest
		}

		for i := range 12 {
			h, mux, phone, marketID := newMarketHub(t)
			opID, previewID, digest := previewMarket(t, h, mux, phone, marketID)
			op := h.opByID(opID)
			body := fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, previewID, digest)

			start := make(chan struct{})
			var wg sync.WaitGroup
			var delCode, installCode int
			var installOut map[string]any

			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				delCode, _ = call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+opID, phone, "")
			}()
			go func() {
				defer wg.Done()
				<-start
				installCode, installOut = call(t, mux, "POST", "/api/v1/extensions/-/install", phone, body)
			}()
			close(start)
			wg.Wait()

			v := h.snapshot(op)
			if installCode == http.StatusOK {
				if v.State == opCanceled {
					t.Fatalf("iter %d: split-brain! op is canceled AND POST /install returned 200 (%v)", i, installOut)
				}
				if delCode != http.StatusConflict && !(delCode == http.StatusNoContent && h.opByID(opID) == nil) {
					t.Fatalf("iter %d: install succeeded (200) but DELETE returned %d (opByID=%v, state=%s)", i, delCode, h.opByID(opID) != nil, v.State)
				}
				if h.extensions.reg.Get("example-ui") == nil {
					t.Fatalf("iter %d: install returned 200 but extension is missing", i)
				}
			} else {
				if delCode != http.StatusNoContent {
					t.Fatalf("iter %d: install failed (%d %v) and DELETE returned %d, want 204", i, installCode, installOut, delCode)
				}
				if h.extensions.reg.PreviewOf(previewID) != nil {
					t.Fatalf("iter %d: canceled preview still in registry", i)
				}
				if h.extensions.reg.Get("example-ui") != nil {
					t.Fatalf("iter %d: canceled preview was installed despite DELETE 204", i)
				}
				if v.State != opCanceled {
					t.Fatalf("iter %d: canceled op state = %s, want canceled", i, v.State)
				}
			}
		}

		// Deterministic check of the fallback path: POST /install after DELETE must never install.
		h, mux, phone, marketID := newMarketHub(t)
		opID, previewID, digest := previewMarket(t, h, mux, phone, marketID)
		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+opID, phone, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %v, want 204", code, out)
		}
		body := fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, previewID, digest)
		if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, body); code == http.StatusOK {
			t.Fatalf("POST /install after DELETE succeeded: %v", out)
		}
		if h.extensions.reg.Get("example-ui") != nil {
			t.Fatal("extension was installed after DELETE")
		}
	})

	t.Run("DELETE in needs_consent discards bundle member previews too", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		mux := h.routes()
		primary, err := h.extensions.reg.Preview(context.Background(), "dir:"+notesPackage(t, "1.0.0"), "")
		if err != nil {
			t.Fatal(err)
		}
		member, err := h.extensions.reg.Preview(context.Background(), "dir:"+uiPackage(t), "")
		if err != nil {
			t.Fatal(err)
		}
		primary.Bundle = []*extension.Preview{member}

		op := h.startOp(opInstall, primary.Manifest.ID, "", i18n.Default, true)
		h.opPreviewed(op, primary, nil)
		if h.extensions.reg.PreviewOf(primary.ID) == nil || h.extensions.reg.PreviewOf(member.ID) == nil {
			t.Fatal("expected primary and bundle member previews to be staged before DELETE")
		}

		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+op.v.ID, phone, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE in needs_consent = %d %v, want 204", code, out)
		}
		if h.extensions.reg.PreviewOf(primary.ID) != nil {
			t.Fatal("primary preview was not discarded on DELETE")
		}
		if h.extensions.reg.PreviewOf(member.ID) != nil {
			t.Fatal("bundle member preview leaked in registry after DELETE")
		}
		if v := h.snapshot(op); v.State != opCanceled {
			t.Fatalf("op state = %s, want canceled", v.State)
		}
	})

	t.Run("synchronous launch returns ErrUserCanceled if DELETE races with preview finish", func(t *testing.T) {
		h, phone, _ := marketHub(t)
		mux := h.routes()
		pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+notesPackage(t, "1.0.0"), "")
		if err != nil {
			t.Fatal(err)
		}

		inRun := make(chan string, 1)
		releaseRun := make(chan struct{})
		type launchRes struct {
			op  *extOp
			pv  *extension.Preview
			err error
		}
		done := make(chan launchRes, 1)

		var idOnce sync.Once
		prevHub := h.opts.Hub
		h.opts.Hub = &interceptHub{
			Host: prevHub,
			onNotify: func(event string, params any) {
				if event != assistantwire.EventBroadcast {
					return
				}
				b, ok := params.(assistantwire.Broadcast)
				if !ok {
					return
				}
				var env struct {
					Type      string `json:"type"`
					Operation struct {
						ID string `json:"id"`
					} `json:"operation"`
				}
				if json.Unmarshal(b.Message, &env) == nil && env.Type == "extension_operation" && env.Operation.ID != "" {
					idOnce.Do(func() { inRun <- env.Operation.ID })
				}
			},
		}

		go func() {
			op, gotPV, gotErr := h.launch(context.Background(), previewJob{
				kind:     opInstall,
				id:       pv.Manifest.ID,
				ofMarket: true,
				lang:     i18n.Default,
				run: func(ctx context.Context) (*extension.Preview, error) {
					<-releaseRun
					return pv, nil
				},
			}, nil, false)
			done <- launchRes{op: op, pv: gotPV, err: gotErr}
		}()

		id := <-inRun
		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+id, phone, ""); code != http.StatusNoContent {
			close(releaseRun)
			t.Fatalf("DELETE during sync launch = %d %v, want 204", code, out)
		}
		close(releaseRun)

		res := <-done
		if res.pv != nil {
			t.Fatalf("sync launch returned non-nil preview after cancel: %+v", res.pv)
		}
		if !errors.Is(res.err, extension.ErrUserCanceled) {
			t.Fatalf("sync launch err = %v, want ErrUserCanceled", res.err)
		}
		if h.extensions.reg.PreviewOf(pv.ID) != nil {
			t.Fatal("canceled sync preview was not discarded")
		}
		if v := h.snapshot(res.op); v.State != opCanceled {
			t.Fatalf("sync op state = %s, want canceled", v.State)
		}
	})
}
