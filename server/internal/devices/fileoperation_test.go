package devices

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFileOperationRouteReadsTheNode(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines/box/files/operations/op-1", nil)
	d.Handler(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operation status: %d %s", rec.Code, rec.Body)
	}
	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer["method"] != "file.operation" {
		t.Fatalf("operation answer: %v, %v", answer, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.calls) == 0 || n.calls[len(n.calls)-1] != `file.operation {"operation_id":"op-1"}` {
		t.Fatalf("node calls = %v", n.calls)
	}
}
