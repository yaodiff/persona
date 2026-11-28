package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestProjectReviewAndTodoBusInvalidParams(t *testing.T) {
	b := &BusHub{helloed: make(chan struct{})}
	close(b.helloed)
	for _, tc := range []struct{ method, params, message string }{
		{assistantwire.MethodProjectReviewOwnerInput, `[]`, "project_review.owner_input: json: cannot unmarshal array into Go value of type assistantwire.ProjectReviewOwnerInput"},
		{assistantwire.MethodTodoHandoff, `[]`, "assistant.todo_handoff: json: cannot unmarshal array into Go value of type assistantwire.TodoHandoff"},
		{assistantwire.MethodTodoLink, `[]`, "assistant.todo_link: json: cannot unmarshal array into Go value of type assistantwire.TodoLinkParams"},
	} {
		out, err := b.answer(t.Context(), tc.method, json.RawMessage(tc.params))
		var re *rpc.Error
		if out != nil || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams || re.Message != tc.message {
			t.Errorf("%s %s = %v, %v, want invalid_params with %q", tc.method, tc.params, out, err, tc.message)
		}
	}
}

func TestProjectReviewOwnerInputBusAcknowledgesWithEmptyObject(t *testing.T) {
	b := &BusHub{helloed: make(chan struct{}), svc: &Service{}}
	close(b.helloed)
	params := json.RawMessage(`{"machine_id":"box","session_id":"s1","message_id":"msg-1","future_field":true}`)
	out, err := b.answer(t.Context(), assistantwire.MethodProjectReviewOwnerInput, params)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(out); err != nil || string(raw) != `{}` {
		t.Fatalf("acknowledgment = %s, %v, want {}", raw, err)
	}
}

func TestHTTPBusInvalidParams(t *testing.T) {
	b := &BusHub{helloed: make(chan struct{})}
	close(b.helloed)
	for _, method := range []string{assistantwire.MethodHTTP, assistantwire.MethodHTTPStreamed} {
		out, err := b.answer(t.Context(), method, json.RawMessage(`[]`))
		var coded *rpc.Error
		if out != nil || !errors.As(err, &coded) || coded.Code != bus.CodeInvalidParams || !strings.HasPrefix(coded.Message, method+": ") {
			t.Errorf("%s = %v, %v, want invalid_params with the method prefix", method, out, err)
		}
	}
}

// The hub's http.request is served by one handler per service, built on the first request, not by a new pair of
// ServeMuxes per request; a service put in place since (Serve) gets one of its own and answers from then on.
func TestRelayedRequestsShareTheHandler(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "jb_x", Devices: &fakeDevices{}})
	b.helloOnce.Do(func() { close(b.helloed) })
	service := func(title string) *Service {
		s := New(Options{Hub: newFakeHub()})
		s.inbox.init()
		s.inbox.items["x"] = &InboxItem{ID: "x", Title: title, UpdatedAt: time.Now()}
		return s
	}
	get := func(s *Service) string {
		t.Helper()
		b.mu.Lock()
		b.svc = s
		b.mu.Unlock()
		params, _ := json.Marshal(assistantwire.HTTPRequest{Method: "GET", Path: "/api/v1/inbox"})
		out, err := b.request(context.Background(), assistantwire.MethodHTTP, params)
		res, ok := out.(assistantwire.HTTPResponse)
		if err != nil || !ok || res.Status != 200 {
			t.Fatalf("GET: %v %+v", err, out)
		}
		return string(res.Body)
	}
	first, second := service("first"), service("second")
	if body := get(first); !strings.Contains(body, `"title":"first"`) {
		t.Fatalf("first service: %s", body)
	}
	kept := b.httpH
	get(first)
	if b.httpH != kept {
		t.Fatal("the handler was built again for the same service")
	}
	if body := get(second); !strings.Contains(body, `"title":"second"`) || b.httpH == kept {
		t.Fatalf("the new service is not the one answering: %s", body)
	}
}
