package assistant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A conversation's last known good execution (docs/assistant/execution-spec.md "Last known good"): the four
// dimensions, the launcher alias and the computer of its last turn that ended without an error. A change of the
// configuration that leaves the conversation unable to run is undone for it in one step (POST …/model/restore),
// which makes that execution its pick, without touching the configuration anyone else runs on.

// lastGoodKey is assistant.last_good_specs: proto.LastGoodSpec by session id.
const lastGoodKey = "assistant.last_good_specs"

// maxLastGood bounds the book: the oldest go first.
const maxLastGood = 512

// lastGoodRefresh is how old a kept execution may get before a turn on the same one writes it again.
const lastGoodRefresh = time.Hour

// lastGoodBook is the book in memory, m replaced, never changed in place; write orders the changes and is held over
// the database write.
type lastGoodBook struct {
	mu     sync.Mutex
	write  sync.Mutex
	loaded bool
	m      map[string]proto.LastGoodSpec
}

// lastGoodLocked reads the book once; b.mu held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) lastGoodLocked(ctx context.Context) bool {
	b := &s.lastGood
	if b.loaded {
		return true
	}
	m := map[string]proto.LastGoodSpec{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, lastGoodKey, &m); err != nil {
			s.logf("execution: read %s: %v", lastGoodKey, err)
			return false
		}
	}
	if m == nil {
		m = map[string]proto.LastGoodSpec{}
	}
	b.m, b.loaded = m, true
	return true
}

// lastGoodOf is conversation sid's last known good execution; false when none is kept or the book can't be read.
func (s *Service) lastGoodOf(ctx context.Context, sid string) (proto.LastGoodSpec, bool) {
	b := &s.lastGood
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.lastGoodLocked(ctx) {
		return proto.LastGoodSpec{}, false
	}
	g, ok := b.m[sid]
	return g, ok
}

// sameLastGood: a and b are the same execution (when aside).
func sameLastGood(a, b proto.LastGoodSpec) bool {
	return a.Spec == b.Spec && a.Launcher == b.Launcher && a.Machine == b.Machine
}

// keepLastGood keeps g as conversation sid's last known good execution. The same one again is written only once it
// is lastGoodRefresh old, so a conversation's turns don't each write the book.
func (s *Service) keepLastGood(ctx context.Context, sid string, g proto.LastGoodSpec) {
	st := s.store()
	if st == nil {
		return
	}
	b := &s.lastGood
	b.write.Lock()
	defer b.write.Unlock()
	b.mu.Lock()
	if !s.lastGoodLocked(ctx) {
		b.mu.Unlock()
		return
	}
	cur := b.m
	b.mu.Unlock()
	if old, ok := cur[sid]; ok && sameLastGood(old, g) && g.At.Sub(old.At) < lastGoodRefresh {
		return
	}
	next := maps.Clone(cur)
	next[sid] = g
	for len(next) > maxLastGood {
		oldest := ""
		for id, v := range next {
			if id != sid && (oldest == "" || v.At.Before(next[oldest].At)) {
				oldest = id
			}
		}
		delete(next, oldest)
	}
	if err := st.PutSetting(ctx, lastGoodKey, next); err != nil {
		s.logf("execution: write %s: %v", lastGoodKey, err)
		return
	}
	b.mu.Lock()
	b.m = next
	b.mu.Unlock()
}

// lastGoodTurnSettled keeps the execution of machine's session sid as its last known good one when its turn ended
// without an error on a launcher.
func (s *Service) lastGoodTurnSettled(ctx context.Context, machine, sid string) {
	sess, ok := s.nodeSession(machine, sid)
	if !s.fleetOn() {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok || sess.Launcher == "" || sess.TurnError != "" || proto.IsExternalLauncher(sess.Launcher) {
		return
	}
	g := proto.LastGoodSpec{Spec: proto.ExecutionSpec{Model: sess.Model, Effort: sess.Effort}, Launcher: sess.Launcher,
		Machine: machine, At: time.Now().UTC()}
	if old, ok := s.lastGoodOf(ctx, sid); ok && old.Launcher == g.Launcher && old.Machine == g.Machine &&
		old.Spec.Model == g.Spec.Model && old.Spec.Effort == g.Spec.Effort && g.At.Sub(old.At) < lastGoodRefresh {
		return // the same as kept: no need to read the declarations again
	}
	if spec, ok := s.execDecl(ctx).specOfLauncher(sess.Launcher, sess.Model, sess.Effort); ok {
		g.Spec = spec
	}
	s.keepLastGood(ctx, sid, g)
}

// lastGoodRoutes: GET /api/v1/assistant/conversations/{id}/model/last-good ({last_good}: null when none is kept), and
// POST …/model/restore, which makes it the conversation's pick ({ok, model_pick, last_good}) with every check of a
// pick the user makes (pickModel). Like any pick, it stops nothing that runs: the next message runs on it.
func (s *Service) lastGoodRoutes() {
	s.handle("GET /api/v1/assistant/conversations/{id}/model/last-good", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-model_pick.err_invalid_id"))
			return
		}
		out := map[string]any{"last_good": nil}
		if g, ok := s.lastGoodOf(r.Context(), id); ok {
			out["last_good"] = g
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("POST /api/v1/assistant/conversations/{id}/model/restore", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-model_pick.err_invalid_id"))
			return
		}
		g, ok := s.lastGoodOf(r.Context(), id)
		if !ok {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-execution.err_no_last_good"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		in := proto.ModelPick{Launcher: g.Launcher, Model: g.Spec.Model, Effort: g.Spec.Effort, Runtime: g.Spec.Runtime,
			Channel: g.Spec.Channel}
		if !slices.Contains(pickEfforts, in.Effort) {
			in.Effort = "" // one a pick can't carry: the launcher's own
		}
		p, err := s.pickModel(ctx, id, g.Machine, in, s.requestLang(r))
		var pe *pickError
		switch {
		case errors.As(err, &pe):
			apierr.Write(w, pe.status, pe.err)
		case storedPickRefused(err):
			apierr.Call(w, err)
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
		default:
			apierr.WriteJSON(w, map[string]any{"ok": true, "model_pick": p.wire(), "last_good": g})
		}
	})
}
