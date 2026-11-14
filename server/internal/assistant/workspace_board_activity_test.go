package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

type activityStore struct {
	*memoryBoardStore
	calls    int
	before   int64
	limit    int
	card     string
	revision int64
	offset   int
	err      error
}

func (s *activityStore) WorkspaceBoardActivity(_ context.Context, before int64, limit int, card string) (workspaceboard.ActivityPage, error) {
	s.calls++
	s.before, s.limit, s.card = before, limit, card
	return workspaceboard.ActivityPage{Entries: []workspaceboard.ActivitySummary{}, LatestRevision: "8", RetentionLimit: 500}, s.err
}
func (s *activityStore) WorkspaceBoardActivityDetail(_ context.Context, revision int64, offset, limit int) (workspaceboard.ActivityDetail, error) {
	s.calls++
	s.revision, s.offset, s.limit = revision, offset, limit
	return workspaceboard.ActivityDetail{Revision: "8", Changes: []workspaceboard.ActivityChange{}, Total: 0}, s.err
}
func TestWorkspaceBoardActivityReadContract(t *testing.T) {
	st := &activityStore{memoryBoardStore: newMemoryBoardStore()}
	s, _ := newBoardService(t, st, nil)
	for _, role := range []string{roleFull, roleAnswer} {
		capability := boardCall(s, "GET", "/api/v1/workspace-board", "", role)
		if capability.Code != 200 || !strings.Contains(capability.Body.String(), `"activity":true`) {
			t.Fatalf("capability: %d %s", capability.Code, capability.Body)
		}
		w := boardCall(s, "GET", "/api/v1/workspace-board/activity?before=8&limit=5&card_id=card-a", "", role)
		if w.Code != 200 || st.before != 8 || st.limit != 5 || st.card != "card-a" || !strings.Contains(w.Body.String(), `"entries":[]`) {
			t.Fatalf("list: %d %s %+v", w.Code, w.Body, st)
		}
		w = boardCall(s, "GET", "/api/v1/workspace-board/activity/8?offset=30&limit=30", "", role)
		if w.Code != 200 || st.revision != 8 || st.offset != 30 || st.limit != 30 {
			t.Fatalf("detail: %d %s", w.Code, w.Body)
		}
	}
	before := st.calls
	for _, path := range []string{
		"/api/v1/workspace-board/activity?before=0", "/api/v1/workspace-board/activity?before=-1", "/api/v1/workspace-board/activity?before=9223372036854775808",
		"/api/v1/workspace-board/activity?limit=0", "/api/v1/workspace-board/activity?limit=51", "/api/v1/workspace-board/activity?limit=2&limit=3",
		"/api/v1/workspace-board/activity?card_id=%00", "/api/v1/workspace-board/activity?card_id=a&card_id=b",
		"/api/v1/workspace-board/activity?card_id=" + strings.Repeat("x", 1025),
		"/api/v1/workspace-board/activity/0", "/api/v1/workspace-board/activity/nope", "/api/v1/workspace-board/activity/8?offset=-1",
		"/api/v1/workspace-board/activity/8?offset=999999999999", "/api/v1/workspace-board/activity/8?limit=101",
	} {
		if w := boardCall(s, "GET", path, "", roleFull); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if st.calls != before {
		t.Fatal("invalid input reached store")
	}
	st.err = pgx.ErrNoRows
	if w := boardCall(s, "GET", "/api/v1/workspace-board/activity/8", "", roleAnswer); w.Code != 404 || !strings.Contains(w.Body.String(), "activity_not_found") {
		t.Fatalf("expired: %d %s", w.Code, w.Body)
	}
	st.err = errors.New("database unavailable")
	if w := boardCall(s, "GET", "/api/v1/workspace-board/activity", "", roleFull); w.Code != 503 {
		t.Fatalf("failure: %d %s", w.Code, w.Body)
	}
}
func TestWorkspaceBoardActivityOlderStoreDoesNotAdvertiseHistory(t *testing.T) {
	s, _ := newBoardService(t, newMemoryBoardStore(), nil)
	w := boardCall(s, "GET", "/api/v1/workspace-board", "", roleFull)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"activity":false`) {
		t.Fatalf("capability: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/v1/workspace-board/activity", "/api/v1/workspace-board/activity/8"} {
		w := boardCall(s, "GET", path, "", roleAnswer)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "not_supported") {
			t.Fatalf("old store: %d %s", w.Code, w.Body)
		}
	}
}
