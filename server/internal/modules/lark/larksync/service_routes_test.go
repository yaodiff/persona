package larksync

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

func TestLarkSyncRoutesNeedFullAccess(t *testing.T) {
	w := newServiceWorld(t)
	key, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc-secret-token", "ref-secret-token", time.Hour)
	writes := []struct{ method, path, body string }{
		{"PUT", "/api/v1/bots/feishu/sync", `{"schedule_hour":4}`},
		{"POST", "/api/v1/bots/feishu/sync/bots", `{"label":"x"}`},
		{"POST", "/api/v1/bots/feishu/sync/bots/b1/register", `{}`},
		{"POST", "/api/v1/bots/feishu/sync/bots/primary/scopes", `{}`},
		{"DELETE", "/api/v1/bots/feishu/sync/bots/b1", ""},
		{"POST", "/api/v1/bots/feishu/sync/bots/primary/authorize", `{"redirect_uri":"` + testRedirect + `"}`},
		{"POST", "/api/v1/bots/feishu/sync/bots/primary/authorize/poll", ""},
		{"POST", "/api/v1/bots/feishu/sync/oauth", `{"state":"s","code":"c"}`},
		{"PUT", "/api/v1/bots/feishu/sync/bindings/" + key, `{"enabled":false}`},
		{"DELETE", "/api/v1/bots/feishu/sync/bindings/" + key, ""},
		{"POST", "/api/v1/bots/feishu/sync/bindings/" + key + "/run", ""},
	}
	for _, who := range []testPrincipal{{ID: "watch", Role: testRoleAnswer}, {ID: "ext", Role: testRoleFull, Extension: "someext"}} {
		if code, view := w.doWith(t, who, "GET", "/api/v1/bots/feishu/sync", ""); code != 200 || view["available"] != true {
			t.Fatalf("%+v reads: %d %v", who, code, view)
		}
		for _, r := range writes {
			if code, _ := w.doWith(t, who, r.method, r.path, r.body); code != http.StatusForbidden {
				t.Errorf("%+v %s %s: %d, want 403", who, r.method, r.path, code)
			}
		}
	}
	if b := w.binding(t, key); !b.Enabled || len(w.reg.begins) != 0 || len(w.ua.configured) != 0 {
		t.Fatalf("a refused request changed something: %+v", b)
	}
	_, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	noSecretIn(t, "the view", marshalJSON(t, view), "acc-secret-token", "ref-secret-token", "primary-secret", "_enc")
}

func TestLarkSyncRoutesChangeTheBindings(t *testing.T) {
	w := newServiceWorld(t)
	key, acc := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc", "ref", time.Hour)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu/sync", `{"schedule_hour":24}`); code != 400 {
		t.Fatalf("hour 24: %d", code)
	}
	_ = w.svc.runSync(context.Background(), key)
	code, view := w.do(t, "PUT", "/api/v1/bots/feishu/sync", `{"schedule_hour":5}`)
	if code != 200 || view["schedule_hour"] != float64(5) {
		t.Fatalf("schedule: %d %v", code, view)
	}
	if b := w.binding(t, key); b.NextRunAt.Hour() != 5 || !b.NextRunAt.After(w.now()) {
		t.Fatalf("next run: %v", b.NextRunAt)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu/sync/bindings/"+key, `{"tier":"everything"}`); code != 400 {
		t.Fatalf("bad tier: %d", code)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu/sync/bindings/nobody~0", `{"enabled":false}`); code != 404 {
		t.Fatalf("unknown binding: %d", code)
	}
	code, view = w.do(t, "PUT", "/api/v1/bots/feishu/sync/bindings/"+key, `{"enabled":false,"label":" Work ","tier":"full_with_im"}`)
	b := view["bindings"].([]any)[0].(map[string]any)
	if code != 200 || b["enabled"] != false || b["label"] != "Work" || b["tier"] != larksyncwire.TierFull {
		t.Fatalf("change: %d %v", code, view)
	}
	if isDue(w.binding(t, key), w.now().Add(72*time.Hour)) {
		t.Fatal("a switched-off binding is due")
	}

	ran := make(chan struct{})
	w.script(larksyncwire.PrimaryBot, acc, func(ctx context.Context, _ larksyncwire.Binding, token func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		defer close(ran)
		if _, err := token(ctx); err != nil {
			return larksyncwire.Result{}, err
		}
		return larksyncwire.Result{Cursors: map[string]string{"drive": "9"}}, nil
	})
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/bindings/"+key+"/run", ""); code != 200 {
		t.Fatalf("run: %d", code)
	}
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("sync now did not run")
	}
	for deadline := time.Now().Add(10 * time.Second); w.svc.runtime(key).running.Load(); runtime.Gosched() {
		if time.Now().After(deadline) {
			t.Fatal("the background run did not finish")
		}
	}
	if w.store.cursorsOf(key)["drive"] != "9" {
		t.Fatalf("cursors: %v", w.store.cursorsOf(key))
	}

	code, view = w.do(t, "DELETE", "/api/v1/bots/feishu/sync/bindings/"+key, "")
	if code != 200 || len(view["bindings"].([]any)) != 0 || len(w.store.cursorsOf(key)) != 0 {
		t.Fatalf("remove: %d %v %v", code, view, w.store.cursorsOf(key))
	}
	if raw := w.store.raw(bindingsKey); strings.Contains(raw, "refresh_token_enc") {
		t.Fatalf("the tokens stayed: %s", raw)
	}
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/sync/bindings/"+key, ""); code != 404 {
		t.Fatalf("removed twice: %d", code)
	}
}

func TestLarkSyncUnavailableWithoutItsStore(t *testing.T) {
	w := newServiceWorld(t)
	w.host.syncStore = nil
	code, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	if code != 200 || view["available"] != false {
		t.Fatalf("view: %d %v", code, view)
	}
	if code, _ := w.do(t, "POST", "/api/v1/bots/feishu/sync/bots", `{"label":"x"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("add bot: %d", code)
	}
	w.svc.Loop(context.Background()) // returns at once: nothing to schedule
}
