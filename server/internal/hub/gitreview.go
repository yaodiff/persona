package hub

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// gitReview relays GET /api/v1/machines/{mid}/git/review?cwd=&scope=&base=&session=&target=&live=&path=… to the
// node's git.review: every file a scope changed, with its hunks, for the web's Review panel (docs/web/review.md,
// #151). Read like git/diff: full credentials only. The node's refusals (not_git, bad_base, no_default_branch…) are
// 409 with their code; a node from before it 409 node_outdated.
func (h *Hub) gitReview(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.GitReviewRequest{
		Cwd:     q.Get("cwd"),
		Scope:   q.Get("scope"),
		Base:    q.Get("base"),
		Session: q.Get("session"),
		Target:  q.Get("target"),
		Live:    q.Get("live") == "1" || q.Get("live") == "true",
		Paths:   q["path"],
	}
	if strings.TrimSpace(p.Cwd) == "" && !q.Has("backend_workspace_id") {
		writeErr(w, http.StatusBadRequest, errors.New("cwd is required"))
		return
	}
	out, ok := h.callNode(w, r, m, "git.review", p, 75*time.Second, "hub-gitreview.outdated", nil)
	if !ok {
		return
	}
	writeRawJSON(w, out)
}
