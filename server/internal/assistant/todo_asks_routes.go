package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A to-do's own asks answered through one route (#10465, docs/assistant/todos.md "Decisions the owner can answer"):
// POST /api/v1/todos/{id}/asks/{ask}/answer takes any answer to an ask of the to-do's row, named by the ask's id in
// its view (todo/projection: todo:decision@<asked_at>, todo:retry@<done_at>, and step:<step id>/<that id> for a step's
// ask on its parent's row), and maps it onto the paths each ask has had: a decision's option, fixed answer or words
// (decision.go), Don't handle and its Undo (task_review_dismissals.go), a failed start started again (tasks_start.go).
// The id carries the round each of them fences, so an answer from a screen that is behind is 409 ask_stale with the
// row as it is now, never an answer to a later question. A run's ask is answered at its card (POST
// /api/v1/interactions/{id}/resolve). Each callee takes the to-do's action lock itself, and that lock is not
// reentrant, so the route never holds it around one: the first answer anywhere wins. Unlike the conversation route,
// it names no conversation, so a to-do no conversation opened is answered too. The hub keeps an operation receipt for
// it (hub/todo_receipts.go): an answer asked again after its reply was lost gets the first reply back.

// maxTodoAnswerBody bounds an answer's body: words of decisionTextMax runes, each escaped as a surrogate pair at most.
const maxTodoAnswerBody = 64 << 10

// The ask ids of a to-do's own asks, as todo/projection forms them (spec/fixtures/todo-view/README.md "asks").
const (
	askDecisionPrefix = "todo:decision@"
	askRetryPrefix    = "todo:retry@"
	askStepPrefix     = "step:"
	askRunPrefix      = "run:"
)

// The outcomes TodoAskAnswer.outcome names.
const (
	answerLater   = assistantwire.TodoAskAnswerOutcomeLater
	answerDecline = assistantwire.TodoAskAnswerOutcomeDecline
	answerUndo    = assistantwire.TodoAskAnswerOutcomeUndo
)

var (
	errAskStale         = i18n.ErrEnglish("assistant-tasks.ask_stale")
	errAnswerInSession  = i18n.ErrEnglish("assistant-tasks.answer_in_session")
	errNotDeclinable    = i18n.ErrEnglish("assistant-tasks.not_declinable")
	errAskNotFound      = i18n.ErrEnglish("assistant-tasks.ask_not_found")
	errAnswerNeeded     = errors.New("this ask takes an option, a response, text or an outcome")
	errAnswerNotForThis = errors.New("this ask takes no option, response or text")
)

// startNotRetried is a failed start the asks route did not start again, refused before anything started: the retry
// route's 409s answer it, never a decision's words that found the to-do's computer offline (the accept route's 502).
type startNotRetried struct{ error }

func (e startNotRetried) Unwrap() error { return e.error }

// ownAsk is one of a to-do's own asks, as its id names it.
type ownAsk struct {
	todo  string // whose ask: the row's to-do, or the step's
	kind  string // assistantwire.OwnerAskKindDecision or OwnerAskKindRetry
	round string // the time after @: a decision's asked_at, a failed start's done_at
}

func (s *Service) todoAskRoutes() {
	// POST /api/v1/todos/{id}/asks/{ask}/answer: TodoAskAnswer → TodoAskAnswered; ask is one path segment, a / in it
	// encoded. 400 a body that is not one answer, or not one this ask takes; 403 a read-only device; 404 an unknown
	// to-do or an id no ask of its row can have; 409 ask_stale (with todo, its row now) when that ask no longer waits,
	// answer_in_session for a run's ask, not_declinable for Don't handle on an ask that has none, and the retry route's
	// 409s for a failed start it does not start; then the accept route's codes: 423 a node's refusal, 502
	// accept_not_taken, 503 the store, each delivered "none"; 502 send_in_doubt when the words may be with the Agent
	// (delivered "unknown") or are, with what followed failed ("yes").
	s.handle("POST /api/v1/todos/{id}/asks/{ask}/answer", s.postTodoAskAnswer)
}

func (s *Service) postTodoAskAnswer(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, i18n.ErrEnglish("http-status.forbidden"))
		return
	}
	var in assistantwire.TodoAskAnswer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTodoAnswerBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeTypedNothing(w, http.StatusBadRequest, "", err)
		return
	}
	if err := checkTodoAnswer(in); err != nil {
		writeTypedNothing(w, http.StatusBadRequest, "", err)
		return
	}
	ctx, id, askID := r.Context(), r.PathValue("id"), r.PathValue("ask")
	if err := s.ensureTodosLoaded(ctx); err != nil {
		writeTypedNothing(w, http.StatusServiceUnavailable, "", errTodosUnavailable)
		return
	}
	if _, ok := s.todoNow(id); !ok {
		writeTypedNothing(w, http.StatusNotFound, "todo_not_found", errors.New("todo not found"))
		return
	}
	ask, err := s.ownAskOf(id, askID)
	switch {
	case errors.Is(err, errAnswerInSession):
		writeTypedNothing(w, http.StatusConflict, "answer_in_session", err)
		return
	case err != nil:
		writeTypedNothing(w, http.StatusNotFound, "ask_not_found", err)
		return
	}
	out := assistantwire.TodoAskAnswered{Ask: askID, Outcome: assistantwire.TodoAskOutcomeAnswered}
	switch ask.kind {
	case assistantwire.OwnerAskKindRetry:
		err = s.answerRetry(ctx, ask, in, &out)
	default:
		err = s.answerDecision(ctx, ask, in, &out)
	}
	if s.writeAnswerError(w, r, id, err) {
		return
	}
	if row, ok := s.answeredRow(ctx, id); ok {
		out.Todo = row
	}
	apierr.WriteJSON(w, out)
}

// checkTodoAnswer refuses a body that is not one answer: more than one of option, response, text and outcome, words
// that are only space, a response or an outcome of no known kind, or a receipt that does not go with an undo.
func checkTodoAnswer(in assistantwire.TodoAskAnswer) error {
	given := 0
	for _, v := range []string{in.Option, in.Response, in.Text, in.Outcome} {
		if v != "" {
			given++
		}
	}
	switch {
	case given > 1:
		return errors.New("give one of option, response, text and outcome")
	case in.Text != "" && strings.TrimSpace(in.Text) == "":
		return errors.New("text is empty")
	case utf8.RuneCountInString(strings.TrimSpace(in.Text)) > decisionTextMax: // kept whole with the decisions, or refused
		return errors.New("text is longer than 4000 characters")
	case in.Response != "" && !slices.Contains(ownerResponses, in.Response):
		return errors.New("response must be decide_yourself, not_what_i_asked or explain_first")
	case in.Outcome != "" && !slices.Contains(assistantwire.TodoAskAnswerOutcomes, in.Outcome):
		return errors.New("outcome must be later, decline or undo")
	case (in.Outcome == answerUndo) != (in.Receipt != ""):
		return errors.New("undo takes the receipt the decline answered, and only undo takes one")
	}
	return nil
}

// ownAskOf is the ask of to-do row's row that id names: one of its own, or one of a step's (step:<step>/<id>, a to-do
// whose part_of is row). errAnswerInSession for a run's ask; errAskNotFound for an id no ask of the row can have.
func (s *Service) ownAskOf(row, id string) (ownAsk, error) {
	todo := row
	if rest, ok := strings.CutPrefix(id, askStepPrefix); ok {
		step, inner, ok := strings.Cut(rest, "/")
		if t, found := s.todoNow(step); !ok || !found || t.PartOf != row || strings.HasPrefix(inner, askStepPrefix) {
			return ownAsk{}, errAskNotFound
		}
		todo, id = step, inner
	}
	var a ownAsk
	switch {
	case strings.HasPrefix(id, askRunPrefix):
		return ownAsk{}, errAnswerInSession
	case strings.HasPrefix(id, askDecisionPrefix):
		a = ownAsk{todo: todo, kind: assistantwire.OwnerAskKindDecision, round: strings.TrimPrefix(id, askDecisionPrefix)}
	case strings.HasPrefix(id, askRetryPrefix):
		a = ownAsk{todo: todo, kind: assistantwire.OwnerAskKindRetry, round: strings.TrimPrefix(id, askRetryPrefix)}
	default:
		return ownAsk{}, errAskNotFound
	}
	if _, err := time.Parse(timeKeyLayout, a.round); err != nil {
		return ownAsk{}, errAskNotFound
	}
	return a, nil
}

// answerDecision answers a decision ask (a decision, or a conclusion that says the work cannot be done) with in,
// filling out's outcome, delivery and receipt. errAskStale when its round no longer waits.
func (s *Service) answerDecision(ctx context.Context, ask ownAsk, in assistantwire.TodoAskAnswer, out *assistantwire.TodoAskAnswered) error {
	a, err := s.openOwnAsk(ctx, ask.todo, ask.round)
	if err != nil {
		return err
	}
	if a == nil {
		return errAskStale
	}
	decision := decideAsk(a)
	var result acceptResult
	switch {
	case in.Outcome == answerLater: // kept on the device: nothing to write, only the round to check
		out.Outcome = assistantwire.TodoAskOutcomeLater
		return nil
	case in.Outcome != "":
		review, err := s.dismissOwnAsk(ctx, ask, in.Receipt, in.Outcome == answerUndo)
		if err != nil {
			return err
		}
		s.askChanged(ctx)
		out.Outcome = assistantwire.TodoAskOutcomeUndone
		if in.Outcome == answerDecline {
			out.Outcome, out.Receipt = assistantwire.TodoAskOutcomeDeclined, review.DismissalID
		}
		return nil
	case !decision && (in.Option != "" || in.Response != "" || in.Text != ""):
		return errAnswerNotForThis // a conclusion that says it cannot be done is acknowledged, or declined
	case in.Option != "":
		result, err = s.chooseOption(ctx, ask.todo, ask.round, in.Option)
	case in.Response != "":
		result.taken, err = s.ownerCannotAnswer(ctx, ask.todo, ask.round, in.Response)
	case in.Text != "":
		result, err = s.answerDecisionText(ctx, ask.todo, ask.round, in.Text)
	case decision:
		return errAnswerNeeded // a decision has no one action: its recommendation is an option like the others
	default:
		result, err = s.acceptTryFromApp(ctx, ask.todo, ask.round)
	}
	if errors.Is(err, db.ErrTodoCompleted) && writeDelivered(err) == "none" {
		return errAskStale // the send's guard: the to-do closed, or the round ended, before anything went out
	}
	if err != nil {
		return err
	}
	if !result.taken {
		return errAskStale
	}
	if result.undelivered != nil {
		out.Delivered = "none"
	}
	return nil
}

// openOwnAsk is task id's own ask while its round asked waits for the owner, as the to-do's view lists it: a decision
// (askRound) or a conclusion that says the work cannot be done, asked then and still among the asks the view lists
// (awaitingAcceptance, so whatever leaves one out of the view leaves it out here too); nil otherwise. errAcceptStore
// when the asks cannot be read.
func (s *Service) openOwnAsk(ctx context.Context, id, asked string) (*acceptAsk, error) {
	if a, err := s.askRound(id, asked); err != nil || a != nil {
		return a, err
	}
	at, err := time.Parse(timeKeyLayout, asked)
	if err != nil {
		return nil, nil
	}
	for _, w := range s.awaitingAcceptance(ctx) {
		if w.todo == id && w.ask.Conclusion && w.ask.Outcome == outcomeCannot && w.ask.AskedAt.Equal(at) {
			return &w.ask, nil
		}
	}
	return nil, nil
}

// dismissOwnAsk is Don't handle on the decision ask names (undo: its Undo, with the receipt the decline answered),
// checked under the to-do's action lock, which changeDecisionDismissalLocked runs under too, so no answer slips in
// between. errAskStale when the round or the review moved on (a review written after the ask is a newer question,
// whose ask is still on its way); errNotDeclinable for an ask with no review waiting on the owner (a conclusion the
// supervisor already passed).
func (s *Service) dismissOwnAsk(ctx context.Context, ask ownAsk, receipt string, undo bool) (*decisionReview, error) {
	s.reviewDismissals.actions.acquire(ask.todo)
	defer s.reviewDismissals.actions.release(ask.todo)
	a, err := s.openOwnAsk(ctx, ask.todo, ask.round)
	if err != nil {
		return nil, err
	}
	t, ok := s.todoNow(ask.todo)
	if a == nil || !ok {
		return nil, errAskStale
	}
	r := currentDecisionReview(t)
	if r == nil {
		return nil, errNotDeclinable
	}
	if r.ReviewAt.After(a.AskedAt) {
		return nil, errAskStale
	}
	review, err := s.changeDecisionDismissalLocked(ctx, ask.todo, r.ReviewAt, receipt, undo)
	if errors.Is(err, errDecisionReviewConflict) {
		return nil, errAskStale
	}
	return review, err
}

// answerRetry starts a failed start again (no answer) or leaves it for later, filling out's outcome; errAskStale once it
// is not the failure the id names (started again, failed anew, closed) or another start of it is on its way, so of two
// retries at once one starts and the other is stale.
func (s *Service) answerRetry(ctx context.Context, ask ownAsk, in assistantwire.TodoAskAnswer, out *assistantwire.TodoAskAnswered) error {
	switch {
	case in.Outcome == answerDecline || in.Outcome == answerUndo:
		return errNotDeclinable
	case in.Option != "" || in.Response != "" || in.Text != "":
		return errAnswerNotForThis
	}
	at, _ := time.Parse(timeKeyLayout, ask.round) // ownAskOf checked it
	t, ok := s.todoNow(ask.todo)
	if !ok || !t.failedStart() || !t.DoneAt.Equal(at) {
		return errAskStale
	}
	if in.Outcome == answerLater {
		out.Outcome = assistantwire.TodoAskOutcomeLater
		return nil
	}
	if f := s.askRetryHook; f != nil {
		f(ask.todo)
	}
	_, started, err := s.retryTaskStart(ctx, ask.todo, retryWith{failedAt: at})
	switch {
	case err != nil:
		return startNotRetried{err}
	case !started:
		return errAskStale
	}
	return nil
}

// writeAnswerError writes err as the answer route answers it (true), the accept and retry routes' codes for theirs;
// false when err is nil. Every refusal says delivered (todo_write_outcome.go): words that may be with the Agent
// already are 502 send_in_doubt, before any other reading of err, and anything else typed nothing. A failed start
// started again starts after the answer (startPlanTaskAsync), so none of its failures went out.
func (s *Service) writeAnswerError(w http.ResponseWriter, r *http.Request, id string, err error) bool {
	var refused retryRefused
	var notRetried startNotRetried
	if err == nil {
		return false
	}
	if delivered := writeDelivered(err); delivered != "none" && !errors.As(err, &notRetried) {
		writeInDoubt(w, http.StatusBadGateway, err, delivered, answerInDoubtKeys)
		return true
	}
	switch {
	case errors.Is(err, errAskStale):
		body := apierr.LocalizedBody(http.StatusConflict, "ask_stale", err, i18n.OfWriter(w))
		if row, ok := s.answeredRow(r.Context(), id); ok {
			body["todo"] = row
		}
		body["delivered"] = "none"
		apierr.WriteBody(w, http.StatusConflict, body)
	case errors.Is(err, errCloseNotConfirmed):
		writeTypedNothing(w, http.StatusUnprocessableEntity, "todo_close_not_confirmed", err)
	case errors.Is(err, errNotDeclinable):
		writeTypedNothing(w, http.StatusConflict, "not_declinable", err)
	case errors.Is(err, errDecisionOption), errors.Is(err, errAnswerNeeded), errors.Is(err, errAnswerNotForThis):
		writeTypedNothing(w, http.StatusBadRequest, "", err)
	case errors.Is(err, errAcceptStore):
		writeTypedNothing(w, http.StatusServiceUnavailable, "", i18n.Err("http-status.unavailable"))
	case errors.Is(err, db.ErrTodoNotFound):
		writeTypedNothing(w, http.StatusNotFound, "todo_not_found", err)
	case errors.As(err, &notRetried) && (errors.Is(err, errTaskNotRetryable) || errors.Is(err, errTaskNotInPlan) ||
		errors.Is(err, errMachineOffline) || errors.As(err, &refused) || errors.Is(err, errPlanNotFound) || errors.Is(err, errPlanNotOpen)):
		writeTypedNothing(w, http.StatusConflict, "", err) // a start the retry route refuses too: nothing started
	case nodeRefusal(err): // nothing was typed: 423 with the node's code, as the accept route answers (#7343)
		writeNodeRefusal(w, err)
	default: // nothing taken; the owner can answer again
		writeTypedNothing(w, http.StatusBadGateway, "accept_not_taken", i18n.Err("assistant-reports.accept_not_taken",
			i18n.Args{"error": clip(oneLine(i18n.Localize(err, s.requestCtxLang(r.Context()))), 120)}))
	}
	return true
}

// answeredRow is to-do id's row with its view as GET /api/v1/todos/{id} answers it, for an answer to carry; false
// when it cannot be read.
func (s *Service) answeredRow(ctx context.Context, id string) (json.RawMessage, bool) {
	t, found, err := s.viewedTodo(ctx, id)
	if err != nil || !found {
		return nil, false
	}
	raw, err := json.Marshal(t)
	return raw, err == nil
}
