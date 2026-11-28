package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAssistantAskUserCancelReturnsNoAnswer(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := s.toolAskUser(ctx, "mac", "conversation", json.RawMessage(`{"why_owner":"priority","questions":[{"question":"Which color?"}]}`))
		done <- result{text, err}
	}()
	var ix proto.Interaction
	for ctx.Err() == nil {
		if qs := s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ix.CanCancel {
		t.Fatal("question cancellation unavailable")
	}
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: ix.ID, Answer: json.RawMessage(`{"cancel":true}`)}); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || !strings.Contains(r.text, "without answering") || strings.Contains(r.text, "The user answered:") {
		t.Fatalf("result: %+v", r)
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatal("closed question reappeared")
	}
}

func TestQuestionCancelReleasesWaiterAndReplaysWithoutAnswer(t *testing.T) {
	ft, ix, done := answerFleet(t)
	ft.s.fleet.mu.Lock()
	ft.s.fleet.interactions[ix.ID].ix.CanCancel = true
	ft.s.fleet.mu.Unlock()
	h := ft.s.fleetHandler(http.NotFoundHandler())
	for attempt := 0; attempt < 2; attempt++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(`{"cancel":true}`)))
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: %d %s", attempt, w.Code, w.Body.String())
		}
	}
	select {
	case r := <-done:
		if !r.IsQuestionCancel() {
			t.Fatalf("runtime received an answer: %+v", r)
		}
	default:
		t.Fatal("runtime waiter was not released")
	}
	select {
	case r := <-done:
		t.Fatalf("duplicate runtime resolution: %+v", r)
	default:
	}
	if _, pending := ft.s.pendingIx(ix.ID); pending {
		t.Fatal("cancelled question remains pending")
	}
	status, r, ended := ft.s.outcome(ix.ID)
	if status != "cancelled" || !ended || !r.IsQuestionCancel() {
		t.Fatalf("reconnect outcome: %s %+v %v", status, r, ended)
	}
	ix.Resumable = true
	res, err := ft.s.waitInteraction(context.Background(), ix)
	if err != nil || !res.IsQuestionCancel() {
		t.Fatalf("replayed question: %+v %v", res, err)
	}
	if _, pending := ft.s.pendingIx(ix.ID); pending {
		t.Fatal("old question reappeared")
	}
}

func TestQuestionCancelRejectsUnsupportedAndMixedActions(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		supported        bool
	}{
		{"old node", proto.IxQuestion, `{"cancel":true}`, false},
		{"approval", proto.IxPermission, `{"cancel":true}`, true},
		{"answer", proto.IxQuestion, `{"cancel":true,"answers":{"q":"yes"}}`, true},
		{"dismiss", proto.IxQuestion, `{"cancel":true,"dismiss":true}`, true},
		{"keys", proto.IxQuestion, `{"cancel":true,"keys":["esc"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, ix, done := answerFleet(t)
			ft.s.fleet.mu.Lock()
			p := ft.s.fleet.interactions[ix.ID]
			p.ix.Kind, p.ix.CanCancel = tc.kind, tc.supported
			ft.s.fleet.mu.Unlock()
			w := httptest.NewRecorder()
			ft.s.fleetHandler(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(tc.body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if _, pending := ft.s.pendingIx(ix.ID); !pending {
				t.Fatal("rejection consumed interaction")
			}
			select {
			case r := <-done:
				t.Fatalf("rejection reached runtime: %+v", r)
			default:
			}
		})
	}
}
