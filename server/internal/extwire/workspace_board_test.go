package extwire

import "testing"

func TestWorkspaceBoardActivityScopeDoesNotGrantWrites(t *testing.T) {
	for _, path := range []string{"/api/v1/workspace-board/activity", "/api/v1/workspace-board/activity/{revision}"} {
		if RouteScopes["GET "+path] != ScopeAgentsRead {
			t.Errorf("missing read scope for %s", path)
		}
		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			if RouteScopes[method+" "+path] != "" {
				t.Errorf("write scope granted: %s %s", method, path)
			}
		}
	}
}
