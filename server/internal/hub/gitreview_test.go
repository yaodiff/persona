package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// git/review (#151) reaches the node's git.review with the scope, the base, a turn's session/target/live and every
// path; the node's refusal comes back as 409 with its code; a node from before it is 409 node_outdated; a watch's
// token is refused.
func TestGitReviewRoute(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	hubSide, nodeSide := net.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubSide), rpc.New("n", nodeSide)
	t.Cleanup(func() { hubSide.Close(); nodeSide.Close() })
	var mu sync.Mutex
	var got proto.GitReviewRequest
	old := false
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "git.backend.review" && !old {
			var in map[string]any
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, err
			}
			if in["backend_workspace_id"] != "ws_example" || in["repo_id"] != "api" || in["scope"] != "commit" {
				return nil, fmt.Errorf("wrong repository selection: %v", in)
			}
			return proto.GitReview{Root: "/work/api"}, nil
		}
		if method != "git.review" || old {
			return nil, fmt.Errorf("unknown method %s", method)
		}
		var p proto.GitReviewRequest
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		got = p
		mu.Unlock()
		if p.Scope == proto.ReviewBranch {
			return nil, &rpc.Error{Code: proto.CodeNoDefaultBranch, Message: "no default branch"}
		}
		return proto.GitReview{Root: "/work/web", Scope: p.Scope, Files: []proto.GitReviewFile{
			{Path: "a.go", Status: "modified", Added: 1, Hunks: []proto.GitHunk{{OldStart: 1, NewStart: 1, Lines: []string{"+x"}}}},
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = hubPeer.Serve(ctx, hubSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()
	m := newMachine(h, MachineConfig{ID: "mac", Name: "Sample computer"})
	m.peer = hubPeer
	h.byID["mac"] = m
	handler := h.routes()
	base := "/api/v1/machines/mac/git/review"

	code, res := call(t, handler, "GET", base+"?cwd=/work/web&scope=turn&base=abc123&session=s1&target=def456&live=1&path=a.go&path=b%20c.go", h.cfg.Token, "")
	files, _ := res["files"].([]any)
	mu.Lock()
	asked := got
	mu.Unlock()
	if code != http.StatusOK || res["root"] != "/work/web" || len(files) != 1 {
		t.Fatalf("review: %d %v", code, res)
	}
	if asked.Cwd != "/work/web" || asked.Scope != "turn" || asked.Base != "abc123" || asked.Session != "s1" || asked.Target != "def456" || !asked.Live || len(asked.Paths) != 2 || asked.Paths[1] != "b c.go" {
		t.Fatalf("the node was asked %+v", asked)
	}
	if code, res := call(t, handler, "GET", base+"?cwd=/work/web&scope=branch", h.cfg.Token, ""); code != http.StatusConflict || res["code"] != proto.CodeNoDefaultBranch {
		t.Fatalf("refusal: %d %v", code, res)
	}
	if code, _ := call(t, handler, "GET", base+"?scope=uncommitted", h.cfg.Token, ""); code != http.StatusBadRequest {
		t.Fatalf("no cwd: %d", code)
	}
	_, watch := call(t, handler, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Watch","kind":"watch"}`)
	if code, _ := call(t, handler, "GET", base+"?cwd=/work/web&scope=uncommitted", watch["token"].(string), ""); code != http.StatusForbidden {
		t.Fatalf("a watch's token: %d", code)
	}
	if code, res := call(t, handler, "GET", base+"?backend_workspace_id=ws_example&repo_id=api&scope=commit", h.cfg.Token, ""); code != 200 || res["root"] != "/work/api" {
		t.Fatalf("selected repository: %d %v", code, res)
	}
	if code, _ := call(t, handler, "GET", base+"?backend_workspace_id=ws_example&cwd=/work/web", h.cfg.Token, ""); code != 400 {
		t.Fatalf("missing repository: %d", code)
	}
	old = true
	if code, res := call(t, handler, "GET", base+"?cwd=/work/web&scope=uncommitted", h.cfg.Token, ""); code != http.StatusConflict || res["code"] != proto.CodeNodeOutdated || res["error"] == "" {
		t.Fatalf("an old node: %d %v", code, res)
	}
}
