package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const testBrokenModelRouteError = "There's an issue with the selected model (provider/missing-model). It may not exist or you may not have access to it. Run /model to pick a different model."

func brokenModelCardFixture(t *testing.T, supervised bool) (*Service, *fakeHub, Todo, proto.Interaction) {
	t.Helper()
	ft := newFleetTest(t)
	s, f := ft.s, ft.f
	on := true
	f.setHello(func(h *Hello) { h.Agents.Delegate.Enabled = &on })
	turn := time.Now().UTC().Truncate(time.Second)
	row := apiErrorTurn("s1", testBrokenModelRouteError)
	row.MachineID, row.UpdatedAt = "mac", turn
	ft.d.setSessions("mac", []proto.Session{row})
	f.setSessions("mac", row)

	var todo Todo
	if supervised {
		f.settings = &Settings{Main: "main-conv"}
		todo = Todo{
			ID:          "todo_broken_model",
			Title:       "Arbitrate report",
			Origin:      "main-conv",
			Supervision: &Supervision{Supervisor: "main-conv"},
			Agents: []TodoAgent{{
				MachineID: "mac",
				SessionID: row.SessionID,
			}},
		}
		putTodo(s, todo)
	}

	question := workT("en", "model_error_ask", map[string]any{"error": testBrokenModelRouteError})
	w := proto.WorkState{
		Phase:  proto.WorkBlocked,
		On:     proto.WaitUser,
		TurnAt: turn,
		Questions: []proto.Question{{
			Question: question,
			Options:  []proto.QuestionOption{{Label: answerLabel("en", answerHandled)}, {Label: answerLabel("en", answerSkip)}},
		}},
	}
	ix := workCard("mac", row, w, "work_broken_model")
	w.InteractionID = ix.ID
	entry := db.SessionWork{MachineID: "mac", SessionID: row.SessionID, TurnAt: turn, InteractionID: ix.ID, Work: w}
	s.work.mu.Lock()
	s.work.init()
	s.work.entries[[2]string{"mac", row.SessionID}] = &entry
	s.work.cards[ix.ID] = ix
	s.work.loaded = true
	s.work.mu.Unlock()
	putPending(s, &pending{ix: ix, local: func(ctx context.Context, r proto.Resolution) error {
		_, err := s.workAnswer(ctx, ix.ID, r)
		return err
	}})
	return s, f, todo, ix
}

func TestModelRouteCustomAnswerHandsOffToSupervisor(t *testing.T) {
	s, f, todo, ix := brokenModelCardFixture(t, true)
	question := ix.Questions[0].Question
	if err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: "Switch to Gemini 4"}}); err != nil {
		t.Fatalf("resolve custom answer on broken model card: %v", err)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("custom answer was sent to broken-model session: %v", got)
	}
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("broken-model card still pending: %+v", cards)
	}
	stored, ok := s.workEntry([2]string{"mac", "s1"})
	if !ok || !stored.Work.Deferred {
		t.Fatalf("expected deferred work entry after handoff, got ok=%v work=%+v", ok, stored.Work)
	}
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	s.supervise.mu.Unlock()
	if !pending || !g.Handoff || !strings.Contains(g.Detail, "Switch to Gemini 4") {
		t.Fatalf("supervisor signal missing replacement instruction: pending=%v signal=%+v", pending, g)
	}
}

func TestModelRouteReanswerHandsOffToSupervisor(t *testing.T) {
	s, f, todo, ix := brokenModelCardFixture(t, true)
	question := ix.Questions[0].Question
	skipLabel := answerLabel("en", answerSkip)
	res := proto.Resolution{
		AnsweredBy: proto.AnsweredByAssistant,
		Reason:     "skip first",
		Answers:    map[string]string{question: skipLabel},
	}
	if err := s.resolve(ix.ID, res); err != nil {
		t.Fatalf("assistant answer: %v", err)
	}
	// Persist the assistant-delegated resolution in the interaction store as resolveAnswer records it.
	if st, ok := s.store().(*fleetMem); ok {
		st.fmu.Lock()
		st.recs = append(st.recs, db.InteractionRecord{Interaction: ix, Status: "resolved", Resolution: &res})
		st.status[ix.ID] = "resolved"
		st.res[ix.ID] = &res
		st.fmu.Unlock()
	}

	code, err := s.reanswer(context.Background(), ix.ID, map[string]string{question: "Switch to Gemini 4"})
	if code != 0 || err != nil {
		t.Fatalf("reanswer returned (%d, %v), want (0, nil)", code, err)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("reanswer sent to broken-model session: %v", got)
	}
	if !strings.Contains(f.eventTypes(), "interaction_reanswered") {
		t.Fatalf("missing interaction_reanswered event: %s", f.eventTypes())
	}
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	s.supervise.mu.Unlock()
	if !pending || !g.Handoff || !strings.Contains(g.Detail, "Switch to Gemini 4") {
		t.Fatalf("supervisor signal missing reanswer handoff: pending=%v signal=%+v", pending, g)
	}
}

func TestModelRouteStandaloneCustomAnswerRefusedWithoutSend(t *testing.T) {
	s, f, _, ix := brokenModelCardFixture(t, false)
	question := ix.Questions[0].Question

	err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: "Switch to Gemini 4"}})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != errModelRouteUnavailable.Code {
		t.Fatalf("expected %s error, got %v", errModelRouteUnavailable.Code, err)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("refused answer still sent to session: %v", got)
	}
	if cards := s.workCards(); len(cards) != 1 {
		t.Fatalf("expected card to remain pending after refusal, got %+v", cards)
	}

	// Standalone reanswer on an assistant-resolved card returns 409 Conflict without sending.
	delegatedIx := ix
	delegatedIx.ID = "work_broken_reanswer"
	delegatedRes := proto.Resolution{
		AnsweredBy: proto.AnsweredByAssistant,
		Reason:     "skip first",
		Answers:    map[string]string{question: answerLabel("en", answerSkip)},
	}
	if st, ok := s.store().(*fleetMem); ok {
		st.fmu.Lock()
		st.recs = append(st.recs, db.InteractionRecord{Interaction: delegatedIx, Status: "resolved", Resolution: &delegatedRes})
		st.status[delegatedIx.ID] = "resolved"
		st.res[delegatedIx.ID] = &delegatedRes
		st.fmu.Unlock()
	}
	code, reErr := s.reanswer(context.Background(), delegatedIx.ID, map[string]string{question: "Switch to Gemini 4"})
	if code != http.StatusConflict || !errors.Is(reErr, errModelRouteUnavailable) {
		t.Fatalf("expected 409 errModelRouteUnavailable from reanswer, got (%d, %v)", code, reErr)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("reanswer refusal still sent to session: %v", got)
	}

	// Explicit "Continue" option stays allowed when the user fixed the provider externally.
	if err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: answerLabel("en", answerHandled)}}); err != nil {
		t.Fatalf("continue option unexpectedly refused: %v", err)
	}
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], continueText) {
		t.Fatalf("expected explicit Continue to send %q, got %v", continueText, got)
	}
}

func TestModelRouteCustomAnswerRefusedWhenSupervisionOff(t *testing.T) {
	s, f, _, ix := brokenModelCardFixture(t, true)
	f.settings = &Settings{Main: "main-conv", AutoSupervisionMode: supervisionModeOff}
	s.prefs.mu.Lock()
	s.prefs.cached = &Settings{Main: "main-conv", AutoSupervisionMode: supervisionModeOff}
	s.prefs.mu.Unlock()
	question := ix.Questions[0].Question

	err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: "Switch to Gemini 4"}})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != errModelRouteUnavailable.Code {
		t.Fatalf("expected %s error with supervision off, got %v", errModelRouteUnavailable.Code, err)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("custom answer sent while supervision off: %v", got)
	}
	if cards := s.workCards(); len(cards) != 1 {
		t.Fatalf("card should stay pending when supervision cannot accept handoff, got %+v", cards)
	}
}

func TestModelRouteFallbackUnavailableRefusesTypedContinue(t *testing.T) {
	ft := newFleetTest(t)
	s, f := ft.s, ft.f
	turn := time.Now().UTC().Truncate(time.Second)
	row := apiErrorTurn("s1", fallbackRouteError)
	row.MachineID, row.UpdatedAt = "mac", turn
	row.ModelFallback = refusalFallback()
	ft.d.setSessions("mac", []proto.Session{row})
	f.setSessions("mac", row)

	question := workT("en", "model_error_ask", map[string]any{"error": fallbackRouteError})
	w := proto.WorkState{
		Phase:  proto.WorkBlocked,
		On:     proto.WaitUser,
		TurnAt: turn,
		Questions: []proto.Question{{
			Question: question,
			Options:  []proto.QuestionOption{{Label: answerLabel("en", answerSkip)}},
		}},
	}
	ix := workCard("mac", row, w, "work_fallback_unavailable")
	w.InteractionID = ix.ID
	s.work.mu.Lock()
	s.work.init()
	s.work.entries[[2]string{"mac", row.SessionID}] = &db.SessionWork{MachineID: "mac", SessionID: row.SessionID, TurnAt: turn, InteractionID: ix.ID, Work: w}
	s.work.cards[ix.ID] = ix
	s.work.loaded = true
	s.work.mu.Unlock()
	putPending(s, &pending{ix: ix, local: func(ctx context.Context, r proto.Resolution) error {
		_, err := s.workAnswer(ctx, ix.ID, r)
		return err
	}})

	err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: answerLabel("en", answerHandled)}})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != errModelRouteUnavailable.Code {
		t.Fatalf("expected %s error on typed continue for fallback-unavailable card, got %v", errModelRouteUnavailable.Code, err)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("typed continue sent on fallback-unavailable card: %v", got)
	}
}
