package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type grantTestStore struct {
	*fleetMem
	fail bool
}

func grantHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func (m *grantTestStore) AlwaysGrants(_ context.Context, session string) ([]db.AlwaysGrant, error) {
	if m.fail {
		return nil, errors.New("database unavailable")
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	out := []db.AlwaysGrant{}
	for k := range m.always[session] {
		out = append(out, db.AlwaysGrant{KeyHash: grantHash(k), ToolName: "Bash"})
	}
	return out, nil
}

func (m *grantTestStore) RevokeAlways(_ context.Context, session, hash string) error {
	if m.fail {
		return errors.New("database unavailable")
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	for k := range m.always[session] {
		if grantHash(k) == hash {
			delete(m.always[session], k)
		}
	}
	return nil
}

type grantTestHub struct {
	*fakeHub
	reset func(DeviceCall) (json.RawMessage, error)
}

func (h *grantTestHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == "assistant.permissions.reset" {
		return h.reset(p)
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestAlwaysGrantRoutes(t *testing.T) {
	for _, result := range []string{`{"ok":true,"restart_required":false}`, `{"ok":true,"restart_required":true}`, `{}`, "offline"} {
		t.Run(result, func(t *testing.T) {
			ft := newFleetTest(t)
			st := &grantTestStore{fleetMem: ft.st}
			ft.s.setStore(func() Store { return st })
			resets := 0
			setHub(ft.s, &grantTestHub{fakeHub: ft.f, reset: func(p DeviceCall) (json.RawMessage, error) {
				resets++
				if ok, _ := st.AlwaysAllowed(context.Background(), "conv", []string{"tool:Bash"}); ok {
					t.Fatal("reset before deletion")
				}
				var in struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(p.Params, &in)
				if in.SessionID != "conv" {
					t.Fatalf("reset wrong conversation: %s", p.Params)
				}
				if result == "offline" {
					return nil, errors.New("offline")
				}
				return json.RawMessage(result), nil
			}})
			ctx := context.Background()
			_ = st.AllowAlways(ctx, "conv", "Bash", []string{"tool:Bash", "tool:Edit"})
			_ = st.AllowAlways(ctx, "other", "Bash", []string{"tool:Bash"})
			path := "/api/v1/assistant/conversations/conv/always"
			w := serve(ft.s, "GET", path, "")
			var list struct {
				Grants []db.AlwaysGrant `json:"grants"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Grants) != 2 {
				t.Fatalf("list: %d %s", w.Code, w.Body)
			}
			for range 2 { // Deleting again retries the process reset after a dropped response.
				w = serve(ft.s, "DELETE", path+"/"+grantHash("tool:Bash"), "")
				var out struct {
					OK      bool `json:"ok"`
					Restart bool `json:"restart_required"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				if w.Code != 200 || !out.OK || out.Restart != (result != `{"ok":true,"restart_required":false}`) {
					t.Fatalf("delete: %d %s", w.Code, w.Body)
				}
			}
			if resets != 2 {
				t.Fatalf("resets = %d", resets)
			}
			if _, ok := ft.s.alwaysAllowed(ctx, alwaysIx("next", "conv", "tool:Bash")); ok {
				t.Fatal("revoked tool still allowed")
			}
			for _, c := range []struct{ session, key string }{{"conv", "tool:Edit"}, {"other", "tool:Bash"}} {
				if ok, _ := st.AlwaysAllowed(ctx, c.session, []string{c.key}); !ok {
					t.Fatalf("unrelated grant removed: %+v", c)
				}
			}
		})
	}
}

func TestAlwaysGrantRouteErrors(t *testing.T) {
	ft := newFleetTest(t)
	st := &grantTestStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return st })
	base := "/api/v1/assistant/conversations/conv/always"
	for _, method := range []string{"GET", "DELETE"} {
		path := base
		if method == "DELETE" {
			path += "/" + grantHash("tool:Bash")
		}
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set(RoleHeader, "answer")
		w := httptest.NewRecorder()
		ft.s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("limited role: %d %s", w.Code, w.Body)
		}
	}
	for _, hash := range []string{"bad", strings.Repeat("A", 64)} {
		if w := serve(ft.s, "DELETE", base+"/"+hash, ""); w.Code != 400 {
			t.Fatalf("invalid hash: %d", w.Code)
		}
	}
	if w := serve(ft.s, "GET", "/api/v1/assistant/conversations/bad%20id/always", ""); w.Code != 400 {
		t.Fatalf("invalid conversation: %d", w.Code)
	}
	if w := serve(ft.s, "GET", base, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"grants":[]`) {
		t.Fatalf("empty list: %d %s", w.Code, w.Body)
	}
	st.fail = true
	for _, method := range []string{"GET", "DELETE"} {
		path := base
		if method == "DELETE" {
			path += "/" + grantHash("tool:Bash")
		}
		if w := serve(ft.s, method, path, ""); w.Code != 500 {
			t.Fatalf("store error: %d %s", w.Code, w.Body)
		}
	}
	ft.s.setStore(func() Store { return nil })
	if w := serve(ft.s, "GET", base, ""); w.Code != 503 {
		t.Fatalf("no store: %d", w.Code)
	}
}

func TestRememberedAnswerDoesNotRestoreRevokedGrant(t *testing.T) {
	ft := newFleetTest(t)
	st := &grantTestStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return st })
	ix := alwaysIx("waiting", "conv", "tool:Bash")
	done := make(chan proto.Resolution, 1)
	go func() { r, _ := ft.s.waitInteraction(context.Background(), ix); done <- r }()
	ft.waitPending(1)
	_ = st.AllowAlways(context.Background(), "conv", "Bash", ix.AlwaysKeys)
	res, ok := ft.s.alwaysAllowed(context.Background(), ix)
	if !ok {
		t.Fatal("grant not remembered")
	}
	_ = st.RevokeAlways(context.Background(), "conv", grantHash("tool:Bash"))
	if err := ft.s.resolveRemembered(ix.ID, res, false); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, ok := ft.s.alwaysAllowed(context.Background(), ix); ok {
		t.Fatal("in-flight automatic answer restored a revoked grant")
	}
}

type occurrenceSwapStore struct {
	*fleetMem
	alwaysStarted, alwaysRelease chan struct{}
	recordStarted, recordRelease chan struct{}
	alwaysOnce, recordOnce       sync.Once
}

func (s *occurrenceSwapStore) AlwaysAllowed(ctx context.Context, session string, keys []string) (bool, error) {
	if s.alwaysStarted != nil {
		s.alwaysOnce.Do(func() {
			close(s.alwaysStarted)
			<-s.alwaysRelease
		})
	}
	return s.fleetMem.AlwaysAllowed(ctx, session, keys)
}

func (s *occurrenceSwapStore) InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error) {
	if s.recordStarted != nil {
		s.recordOnce.Do(func() {
			close(s.recordStarted)
			<-s.recordRelease
		})
	}
	return s.fleetMem.InteractionRecord(ctx, id)
}

func TestKeepAlwaysDoesNotResolveReplacementOccurrence(t *testing.T) {
	ft := newFleetTest(t)
	st := &occurrenceSwapStore{fleetMem: ft.st, alwaysStarted: make(chan struct{}), alwaysRelease: make(chan struct{})}
	ft.s.setStore(func() Store { return st })
	first := resumable("ix_keep_always_occurrence")
	first.ToolName = "Bash"
	first.AlwaysKeys = []string{"lark:keep-always-occurrence"}
	first.CreatedAt = time.Now().UTC()
	putPending(ft.s, &pending{ix: first, done: make(chan proto.Resolution, 1)})
	source := first
	source.ID = "ix_keep_always_source"
	allow := true
	done := make(chan struct{})
	go func() {
		ft.s.keepAlways(source, proto.Resolution{Allow: &allow, Always: true})
		close(done)
	}()
	<-st.alwaysStarted
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	replacement.AlwaysKeys = []string{"lark:different-occurrence"}
	current := &pending{ix: replacement, done: make(chan proto.Resolution, 1)}
	putPending(ft.s, current)
	close(st.alwaysRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Always propagation did not finish")
	}
	ft.s.fleet.mu.Lock()
	kept := ft.s.fleet.interactions[replacement.ID] == current && !current.claimed
	ft.s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("an Always result for the old occurrence claimed its replacement")
	}
}

func TestRememberedReconnectDoesNotClaimReplacementAfterHistoryCheck(t *testing.T) {
	ft := newFleetTest(t)
	st := &occurrenceSwapStore{fleetMem: ft.st, recordStarted: make(chan struct{}), recordRelease: make(chan struct{})}
	ft.s.setStore(func() Store { return st })
	first := resumable("ix_remembered_reconnect_occurrence")
	first.ToolName = "Bash"
	first.AlwaysKeys = []string{"lark:remembered-reconnect-occurrence"}
	first.CreatedAt = time.Now().UTC()
	if err := st.AllowAlways(context.Background(), first.SessionID, first.ToolName, first.AlwaysKeys); err != nil {
		t.Fatal(err)
	}
	result := make(chan detachedTestResult, 1)
	go func() {
		r, err := ft.s.nodeInteraction(context.Background(), first)
		result <- detachedTestResult{res: r, err: err}
	}()
	<-st.recordStarted
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	current := &pending{ix: replacement, done: make(chan proto.Resolution, 1)}
	putPending(ft.s, current)
	close(st.recordRelease)
	got := awaitDetachedTest(t, result)
	if got.err != nil || got.res.Allow == nil || !*got.res.Allow || !got.res.Remembered {
		t.Fatalf("remembered reconnect result = %+v, %v", got.res, got.err)
	}
	ft.s.fleet.mu.Lock()
	kept := ft.s.fleet.interactions[replacement.ID] == current && !current.claimed
	ft.s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("a remembered answer claimed a replacement installed after its history check")
	}
}
