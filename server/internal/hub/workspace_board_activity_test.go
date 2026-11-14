package hub

import (
	"net/http"
	"testing"
)

func TestWorkspaceBoardActivityAnswerPermissionsAreReadOnlyAndNarrow(t *testing.T) {
	p := principal{Role: roleAnswer}
	for _, path := range []string{"/api/v1/workspace-board/activity", "/api/v1/workspace-board/activity/1", "/api/v1/workspace-board/activity/9223372036854775807"} {
		if !allowed(p, http.MethodGet, path) {
			t.Errorf("history read denied: %s", path)
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if allowed(p, method, path) {
				t.Errorf("history write granted: %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/api/v1/workspace-board/activity/0", "/api/v1/workspace-board/activity/-1", "/api/v1/workspace-board/activity/x", "/api/v1/workspace-board/activity/1/private", "/api/v1/workspace-board/anything", "/api/v1/workspace-board/activity-secret"} {
		if allowed(p, http.MethodGet, path) {
			t.Errorf("unexpected permission: %s", path)
		}
	}
}
