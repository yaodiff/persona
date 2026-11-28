package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

func TestDevelopmentPreviewAndLatestTarget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		u := h.upd
		u.loaded, u.settings.Source, u.settings.DevelopmentAuto = true, "web-dev", true
		h.build.Commit = strings.Repeat("a", 40)
		h.cfg.Update.Development = &update.DevelopmentConfig{}
		mux := http.NewServeMux()
		u.routes(mux)
		post := func(path, body string, want int) {
			t.Helper()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/hub/update"+path, strings.NewReader(body)))
			if w.Code != want {
				t.Fatalf("POST %s: %d %s", path, w.Code, w.Body)
			}
			synctest.Wait()
		}
		remote := update.DevelopmentCandidate{Commit: strings.Repeat("b", 40), Branch: "main", UpdatedAt: "2026-11-01T12:00:00Z"}
		var built []update.DevelopmentCandidate
		var resolveErr error
		var resume chan struct{}
		resolves := 0
		u.devRun = func(_ context.Context, _ *update.DevelopmentConfig, _, op string, input map[string]any, result any, _ func(), _ func(update.DevelopmentProgress)) error {
			switch op {
			case "resolve":
				resolves++
				if resume != nil {
					<-resume
				}
				*result.(*update.DevelopmentCandidate) = remote
				return resolveErr
			case "preflight":
				return nil
			case "build":
				built = append(built, *input["candidate"].(*update.DevelopmentCandidate))
			default:
				t.Errorf("unexpected helper call %s", op)
			}
			return errors.New("fixture stopped the build")
		}

		_, raw := getUpdate(t, u)
		if raw["supports_preview"] != true {
			t.Fatal("preview capability missing")
		}
		post("/check", `{"preview":true}`, 200)
		cached := remote
		before := u.view()
		if !u.wantsAuto() || before.Latest.Commit != cached.Commit || before.LastCheck == nil || len(built) != 0 {
			t.Fatalf("preview on an automatic hub: %+v, builds %v", before, built)
		}
		u.lastCheck = before.LastCheck.Add(-time.Hour)
		before = u.view()
		resolveErr = errors.New("remote unreachable")
		post("/check", `{"preview":true}`, 502)
		if v := u.view(); !v.LastCheck.Equal(*before.LastCheck) || v.Latest.Commit != cached.Commit {
			t.Fatalf("failed preview changed the cached result: %+v", v)
		}
		post("", `{"target":"latest"}`, 200)
		failed := u.view()
		if failed.State != "failed" || failed.Error == "" || u.running || !failed.LastCheck.Equal(*before.LastCheck) || failed.Latest.Commit != cached.Commit || len(built) != 0 {
			t.Fatalf("failed resolution: %+v, builds %v", failed, built)
		}

		resolveErr = nil
		remote.Commit = strings.Repeat("c", 40)
		remote.UpdatedAt = "2026-11-02T12:00:00Z"
		count := resolves
		post("", "{}", 200)
		if resolves != count || len(built) != 1 || built[0] != cached {
			t.Fatalf("legacy build changed target: %v", built)
		}

		resume = make(chan struct{})
		post("", `{"target":"latest"}`, 200)
		if v := u.view(); v.State != "resolving" || !u.running {
			t.Fatalf("no reservation: %+v", v)
		}
		if w := nodePolicyRequest(t, u, http.MethodPut, `{"source":"release"}`); w.Code != 409 {
			t.Fatal("source changed during resolution")
		}
		post("", `{"target":"latest"}`, 200)
		close(resume)
		synctest.Wait()
		if resolves != count+1 || len(built) != 2 || built[1] != remote {
			t.Fatalf("reserved build: %d resolutions, %v", resolves-count, built)
		}

		remote.Commit = h.build.Commit
		post("", `{"target":"latest"}`, 200)
		if v := u.view(); v.State != "idle" || v.Latest.Newer || v.Latest.Commit != remote.Commit || v.Error != "" || u.running || len(built) != 2 {
			t.Fatalf("current revision: %+v, builds %v", v, built)
		}
	})
}
