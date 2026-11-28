package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Outside decisions (#8235): an authorized script or extension raises the same
// rich owner decision as a supervisor, without holding a model turn or an Agent
// session open. Every request gets a backing to-do row because the existing web
// and native cards answer a to-do ask. Naming todo_id makes that row a piece of
// the named request; it never replaces an ask already on the request itself.
//
// The acceptance book holds the open card. outsideDecision holds the caller's
// durable view beside it, atomically saved by saveAcceptErrLocked. Answers are
// returned to this caller, never relayed to an Agent or answered by the
// assistant. In particular decide_yourself is a fixed answer for the caller to
// act on, not permission for Persona to choose on the owner's behalf.

type outsideAnswer = assistantwire.OutsideDecisionAnswer

type outsideDecision struct {
	ID          string    `json:"id"`
	Caller      string    `json:"caller"`
	Ref         string    `json:"ref,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	TodoID      string    `json:"todo_id,omitempty"`
	AskTodoID   string    `json:"ask_todo_id"`
	SourceKind  string    `json:"source_kind"`
	SourceName  string    `json:"source_name"`
	Ask         acceptAsk `json:"ask"`
}

type outsideCaller struct {
	key, kind, name string
}

type outsideFailure struct {
	status int
	code   string
	err    error
}

func (e *outsideFailure) Error() string { return e.err.Error() }
func (e *outsideFailure) Unwrap() error { return e.err }

func outsideErr(status int, code string, err error) error {
	return &outsideFailure{status: status, code: code, err: err}
}

var errOutsideNotFound = errors.New("decision not found")
var errOutsideClosed = errors.New("the decision is already answered")
var errOutsideStore = errors.New("the decision could not be saved")
var errOutsideDecisionPending = errors.New("answer or withdraw the pending outside decision first")

func copyAcceptAsk(a *acceptAsk) acceptAsk {
	if a == nil {
		return acceptAsk{}
	}
	cp := *a
	cp.prepared = nil
	cp.Messages = append([]string(nil), a.Messages...)
	cp.Earlier = append([]string(nil), a.Earlier...)
	cp.Options = append([]DecisionOption(nil), a.Options...)
	if a.From != nil {
		from := *a.From
		cp.From = &from
	}
	if a.OutsideAnswer != nil {
		answer := *a.OutsideAnswer
		cp.OutsideAnswer = &answer
	}
	return cp
}

func cloneOutsideDecisions(in map[string]*outsideDecision) map[string]*outsideDecision {
	out := make(map[string]*outsideDecision, len(in))
	for id, rec := range in {
		if rec == nil {
			continue
		}
		cp := *rec
		cp.Ask = copyAcceptAsk(&rec.Ask)
		out[id] = &cp
	}
	return out
}

func outsideView(rec *outsideDecision) assistantwire.OutsideDecision {
	a := &rec.Ask
	out := assistantwire.OutsideDecision{
		ID: rec.ID, TodoID: rec.TodoID, AskTodoID: rec.AskTodoID, State: assistantwire.OutsideDecisionPending,
		Source: assistantwire.OutsideDecisionSource{Kind: rec.SourceKind, Name: rec.SourceName},
		Title:  a.Reason, Background: a.OutsideBackground, WhyOwner: a.WhyOwner, Recommend: a.Recommend,
		CostOfWaiting: a.CostOfWaiting, Reversible: a.Reversible, Default: defaultNever, CreatedAt: a.AskedAt,
	}
	for _, o := range a.Options {
		out.Options = append(out.Options, assistantwire.OutsideDecisionOption{ID: assistantwire.OutsideDecisionOptionID(o.ID), Label: o.Label, LeadsTo: o.LeadsTo})
	}
	if a.DefaultAllowed && a.Reversible && !neverDefault(a.WhyOwner) && !a.Deadline.IsZero() && (a.DueAt.IsZero() || a.ByDefault) {
		out.Default = defaultTaken
		deadline := a.Deadline
		out.Deadline = &deadline
	}
	if !a.WithdrawnAt.IsZero() {
		out.State = assistantwire.OutsideDecisionWithdrawn
		at := a.WithdrawnAt
		out.WithdrawnAt = &at
	} else if !a.AcceptedAt.IsZero() {
		out.State = assistantwire.OutsideDecisionAnswered
	}
	if a.OutsideAnswer != nil {
		answer := *a.OutsideAnswer
		out.Answer = &answer
	}
	return out
}

func outsideReply(rec *outsideDecision, inserted bool) assistantwire.OutsideDecisionReply {
	view := outsideView(rec)
	return assistantwire.OutsideDecisionReply{Decision: &view, Inserted: inserted}
}

func outsideErrorReply(err error) assistantwire.OutsideDecisionReply {
	var failure *outsideFailure
	if errors.As(err, &failure) {
		return assistantwire.OutsideDecisionReply{Code: failure.code, Error: failure.err.Error()}
	}
	return assistantwire.OutsideDecisionReply{Code: "store_unavailable", Error: err.Error()}
}

// normalizeOutsideDecision applies the supervisor's complete-decision rules,
// including moving the recommended option first. The caller's background and
// reversibility are additional required facts; allow_default only controls the
// automatic deadline, never whether a reversible answer is one tap.
func normalizeOutsideDecision(in assistantwire.OutsideDecisionRequest) (reviewDecision, string, bool, string, error) {
	if in.Reversible == nil {
		return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision", errors.New("reversible is required"))
	}
	reversible := *in.Reversible
	in.Title = textfmt.StripTerminalEscapes(in.Title)
	in.Background = textfmt.StripTerminalEscapes(in.Background)
	in.WhyOwner = textfmt.StripTerminalEscapes(in.WhyOwner)
	in.Recommend = textfmt.StripTerminalEscapes(in.Recommend)
	in.CostOfWaiting = textfmt.StripTerminalEscapes(in.CostOfWaiting)
	seenIDs := make(map[string]bool, len(in.Options))
	for i := range in.Options {
		in.Options[i].Label = textfmt.StripTerminalEscapes(in.Options[i].Label)
		in.Options[i].LeadsTo = textfmt.StripTerminalEscapes(in.Options[i].LeadsTo)
		id := string(in.Options[i].ID)
		if id == "" {
			continue
		}
		if strings.TrimSpace(id) != id || len(id) > 64 || seenIDs[id] {
			return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision", errors.New("option ids must be unique, trimmed and at most 64 characters"))
		}
		for _, r := range id {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
				continue
			}
			return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision", errors.New("option ids use only ASCII letters, digits, '.', '_' and '-'"))
		}
		seenIDs[id] = true
	}
	// An existing exact label keeps its legacy meaning. Otherwise recommend may
	// name the stable id while the owner sees and answers the localized label.
	labelRecommended := false
	for _, option := range in.Options {
		labelRecommended = labelRecommended || option.Label == in.Recommend
	}
	if !labelRecommended {
		for _, option := range in.Options {
			if option.ID != "" && string(option.ID) == in.Recommend {
				in.Recommend = option.Label
				break
			}
		}
	}
	options := make([]DecisionOption, 0, len(in.Options))
	for _, o := range in.Options {
		options = append(options, DecisionOption{ID: string(o.ID), Label: o.Label, LeadsTo: o.LeadsTo})
	}
	d, err := readDecision(in.WhyOwner, in.Title, in.Recommend, in.CostOfWaiting, options)
	if err != nil {
		return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision", err)
	}
	seen := make(map[string]bool, len(d.Options))
	for _, option := range d.Options {
		// Owner replies and polling identify an option by label. Use the same
		// whitespace/punctuation-insensitive comparison as exactDecisionOption.
		key := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, option.Label)
		if seen[key] {
			return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision",
				errors.New("option labels must be distinct ignoring whitespace and punctuation"))
		}
		seen[key] = true
	}
	background := clip(oneLine(in.Background), 500)
	if background == "" {
		return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision", errors.New("background is required"))
	}
	if strings.Contains(in.Ref, "#") || utf8.RuneCountInString(in.Ref) > decisionRefMax {
		return reviewDecision{}, "", false, "", outsideErr(http.StatusBadRequest, "invalid_decision",
			fmt.Errorf("ref must be at most %d characters without '#'", decisionRefMax))
	}
	allowDefault := in.AllowDefault == nil || *in.AllowDefault
	in.TodoID = strings.TrimSpace(in.TodoID)
	in.Ref = strings.TrimSpace(in.Ref)
	in.Title, in.Background, in.WhyOwner = d.Reason, background, d.WhyOwner
	in.Recommend, in.CostOfWaiting, in.Options = d.Recommend, d.CostOfWaiting, nil
	for _, o := range d.Options {
		in.Options = append(in.Options, assistantwire.OutsideDecisionOption{ID: assistantwire.OutsideDecisionOptionID(o.ID), Label: o.Label, LeadsTo: o.LeadsTo})
	}
	in.AllowDefault = &allowDefault
	in.Reversible = &reversible
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return d, background, allowDefault, hex.EncodeToString(sum[:]), nil
}

func (s *Service) outsideByRefLocked(caller *outsideCaller, ref, fingerprint string) (*outsideDecision, error) {
	if ref == "" {
		return nil, nil
	}
	for _, rec := range s.accept.outside {
		if rec != nil && rec.Caller == caller.key && rec.Ref == ref {
			if rec.Fingerprint != fingerprint {
				return nil, outsideErr(http.StatusConflict, "decision_ref_conflict", errors.New("ref already names a different decision"))
			}
			cp := *rec
			cp.Ask = copyAcceptAsk(&rec.Ask)
			return &cp, nil
		}
	}
	return nil, nil
}

// raiseOutsideDecision persists the complete owner-facing ask before sending
// its push. The HTTP request or local CLI returns after that work, never after
// an owner answer. A caller ref makes a lost create response safe to retry.
func (s *Service) raiseOutsideDecision(ctx context.Context, caller *outsideCaller, in assistantwire.OutsideDecisionRequest) (*outsideDecision, bool, error) {
	d, background, allowDefault, fingerprint, err := normalizeOutsideDecision(in)
	if err != nil {
		return nil, false, err
	}
	callerCopy := *caller
	callerCopy.name = clip(oneLine(textfmt.StripTerminalEscapes(callerCopy.name)), 80)
	if callerCopy.name == "" {
		callerCopy.name = "Outside caller"
	}
	caller = &callerCopy
	b := &s.accept
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return nil, false, outsideErr(http.StatusServiceUnavailable, "store_unavailable", errAcceptStore)
	}
	// A durable replay does not depend on the parent's later state: the caller
	// must be able to recover its id even if that to-do was closed or deleted
	// after the first response was lost. A different payload still conflicts.
	if rec, err := s.outsideByRefLocked(caller, strings.TrimSpace(in.Ref), fingerprint); err != nil || rec != nil {
		b.mu.Unlock()
		return rec, false, err
	}
	b.mu.Unlock()

	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, false, outsideErr(http.StatusServiceUnavailable, "store_unavailable", err)
	}
	todoID := strings.TrimSpace(in.TodoID)
	origin := ""
	if todoID != "" {
		parent, ok := s.todoNow(todoID)
		switch {
		case !ok:
			return nil, false, outsideErr(http.StatusNotFound, "todo_not_found", db.ErrTodoNotFound)
		case parent.DoneAt != nil:
			return nil, false, outsideErr(http.StatusConflict, "todo_closed", errors.New("the to-do is closed"))
		default:
			origin = parent.Origin
		}
	}

	now := time.Now().UTC()
	id := "odc_" + randomHex(8)
	askTodoID := "todo_od_" + randomHex(8)
	// This row is the decision's one owner-facing home, not a quiet background
	// round: it must be in Needs-you lists and badges. NoCard below only keeps
	// the Lark retry from adding another card; the explicit decide push follows.
	row := db.TodoRow{ID: askTodoID, Title: d.Reason, Notes: background, CreatedAt: now, UpdatedAt: now,
		Origin: origin, PartOf: todoID}
	if _, err := s.insertTodo(ctx, row); err != nil {
		var piece *pieceError
		if errors.As(err, &piece) || errors.Is(err, db.ErrTodoNotFound) {
			return nil, false, outsideErr(http.StatusBadRequest, "invalid_todo", err)
		}
		return nil, false, outsideErr(http.StatusServiceUnavailable, "store_unavailable", err)
	}
	a := acceptAsk{Conclusion: true, Outcome: outcomeDecide, Reason: d.Reason, Recommend: d.Recommend,
		AskedAt: now, NoCard: true, WhyOwner: d.WhyOwner, Options: d.Options, CostOfWaiting: d.CostOfWaiting,
		Reversible: *in.Reversible, OutsideID: id, OutsideSource: caller.name, OutsideKind: caller.kind,
		OutsideBackground: background, DefaultAllowed: allowDefault, From: &askFrom{At: now, ByAssistant: true}}
	if allowDefault && a.Reversible && !neverDefault(a.WhyOwner) {
		a.Deadline = now.Add(decisionDefaultAfter)
	}
	rec := &outsideDecision{ID: id, Caller: caller.key, Ref: strings.TrimSpace(in.Ref), Fingerprint: fingerprint,
		TodoID: todoID, AskTodoID: askTodoID, SourceKind: caller.kind, SourceName: caller.name, Ask: copyAcceptAsk(&a)}

	b.mu.Lock()
	b.asks[askTodoID], b.outside[id] = &a, rec
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		delete(b.asks, askTodoID)
		delete(b.outside, id)
		b.mu.Unlock()
		if cleanupErr := s.removeTodo(ctx, askTodoID); cleanupErr != nil {
			s.logf("outside decision %s: clean up an unsaved backing to-do: %v", id, cleanupErr)
		}
		return nil, false, outsideErr(http.StatusServiceUnavailable, "store_unavailable", errors.Join(errOutsideStore, err))
	}
	stored := *b.outside[id]
	stored.Ask = copyAcceptAsk(&b.outside[id].Ask)
	b.mu.Unlock()
	s.askChanged(ctx)
	if _, err := s.pushDecisionErr(ctx, askTodoID); err != nil {
		// The durable card is already raised. A push transport failure must not
		// turn a successful create into a retry that asks the owner twice.
		s.logf("outside decision %s: initial push: %v", id, err)
	}
	return &stored, true, nil
}

func (s *Service) outsideForCaller(ctx context.Context, caller *outsideCaller, id string) (*outsideDecision, error) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAcceptLocked(ctx) {
		return nil, outsideErr(http.StatusServiceUnavailable, "store_unavailable", errAcceptStore)
	}
	rec := b.outside[id]
	if rec == nil || rec.Caller != caller.key {
		return nil, outsideErr(http.StatusNotFound, "decision_not_found", errOutsideNotFound)
	}
	cp := *rec
	cp.Ask = copyAcceptAsk(&rec.Ask)
	return &cp, nil
}

// withdrawOutsideDecision fences the owner answer with the backing row's action
// lock. Repeating a withdrawal is idempotent; withdrawing an answered decision
// is a conflict, so a lost response cannot erase the answer the caller polls.
func (s *Service) withdrawOutsideDecision(ctx context.Context, caller *outsideCaller, id string) (*outsideDecision, error) {
	b := &s.accept
	b.writing.Lock()
	defer b.writing.Unlock()
	rec, err := s.outsideForCaller(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	s.reviewDismissals.actions.acquire(rec.AskTodoID)
	defer s.reviewDismissals.actions.release(rec.AskTodoID)
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return nil, outsideErr(http.StatusServiceUnavailable, "store_unavailable", errAcceptStore)
	}
	cur := b.outside[id]
	if cur == nil || cur.Caller != caller.key {
		b.mu.Unlock()
		return nil, outsideErr(http.StatusNotFound, "decision_not_found", errOutsideNotFound)
	}
	if !cur.Ask.WithdrawnAt.IsZero() {
		cp := *cur
		cp.Ask = copyAcceptAsk(&cur.Ask)
		b.mu.Unlock()
		return &cp, nil
	}
	if !cur.Ask.AcceptedAt.IsZero() {
		b.mu.Unlock()
		return nil, outsideErr(http.StatusConflict, "decision_closed", errOutsideClosed)
	}
	now := time.Now().UTC()
	a := b.asks[cur.AskTodoID]
	var previous acceptAsk
	if a != nil && a.OutsideID == id && a.AskedAt.Equal(cur.Ask.AskedAt) {
		previous = copyAcceptAsk(a)
		a.AcceptedAt, a.WithdrawnAt = now, now
	} else {
		previous = copyAcceptAsk(&cur.Ask)
		cur.Ask.AcceptedAt, cur.Ask.WithdrawnAt = now, now
	}
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		if a != nil && a.OutsideID == id {
			*a = previous
		} else {
			cur.Ask = previous
		}
		b.mu.Unlock()
		return nil, outsideErr(http.StatusServiceUnavailable, "store_unavailable", errors.Join(errOutsideStore, err))
	}
	out := *b.outside[id]
	out.Ask = copyAcceptAsk(&b.outside[id].Ask)
	b.mu.Unlock()
	s.askChanged(ctx)
	s.todoPushOver(cur.AskTodoID)
	s.closeOutsideBacking(ctx, cur.AskTodoID)
	return &out, nil
}

// answerOutside records one owner/default answer without any Agent or
// supervisor delivery. Its caller holds the backing row's action lock.
func (s *Service) answerOutside(ctx context.Context, id, asked string, answer outsideAnswer, byDefault bool) (acceptResult, error) {
	a, err := s.askRound(id, asked)
	if err != nil || a == nil || a.OutsideID == "" {
		return acceptResult{}, err
	}
	if byDefault && !(a.DefaultAllowed && a.Reversible && !neverDefault(a.WhyOwner) && !a.Deadline.IsZero() && a.DueAt.IsZero() && !time.Now().Before(a.Deadline)) {
		return acceptResult{}, nil
	}
	now := time.Now().UTC()
	answer.AnsweredAt = now
	answer.AnsweredBy = assistantwire.OutsideAnswerOwner
	if byDefault {
		answer.AnsweredBy = assistantwire.OutsideAnswerDefault
	}
	b := &s.accept
	b.mu.Lock()
	cur := b.asks[id]
	if cur == nil || cur.OutsideID != a.OutsideID || !cur.AskedAt.Equal(a.AskedAt) || !cur.AcceptedAt.IsZero() {
		b.mu.Unlock()
		return acceptResult{}, nil
	}
	previous := copyAcceptAsk(cur)
	for _, option := range cur.Options {
		if option.Label == answer.Option {
			answer.OptionID = assistantwire.OutsideDecisionOptionID(option.ID)
			break
		}
	}
	cur.OutsideAnswer = &answer
	stays := answer.Response == respondExplainFirst
	if stays {
		cur.Response = answer.Response
		if !cur.Deadline.IsZero() && cur.DueAt.IsZero() {
			cur.DueAt = now // an explanation was asked for: no default meanwhile
		}
	} else {
		cur.AcceptedAt = now
		cur.Response, cur.Chosen = answer.Response, ""
		if answer.Option != "" && answer.Option != cur.Recommend {
			cur.Chosen = answer.Option
		}
		if byDefault {
			cur.ByDefault, cur.DueAt = true, now
		}
	}
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		*cur = previous
		b.mu.Unlock()
		return acceptResult{}, fmt.Errorf("%w: %w", errAcceptStore, err)
	}
	cp := copyAcceptAsk(cur)
	caller, ref := "", ""
	if rec := b.outside[cp.OutsideID]; rec != nil {
		caller, ref = rec.Caller, rec.Ref
	}
	b.mu.Unlock()
	s.askChanged(ctx)
	s.updateAskCards(ctx, id, cp)
	if !stays {
		s.todoPushOver(id)
		s.recordOutsideDecision(ctx, id, &cp)
		s.closeOutsideBacking(ctx, id)
		option := ""
		if cp.OutsideAnswer != nil {
			option = cp.OutsideAnswer.Option
		}
		s.handoffDecided(ctx, caller, ref, option)
	}
	return acceptSucceeded(), nil
}

// recordOutsideDecision keeps the ordinary to-do decision history too. The
// caller's polling answer is already durable; a failure here is logged rather
// than rolling the owner's answered card back.
func (s *Service) recordOutsideDecision(ctx context.Context, id string, a *acceptAsk) {
	if a.OutsideAnswer == nil || a.OutsideAnswer.Response != "" {
		return
	}
	choice := a.OutsideAnswer.Option
	if a.OutsideAnswer.Text != "" {
		choice = a.OutsideAnswer.Text
	}
	if choice == "" {
		return
	}
	st := s.todoDB()
	if st == nil {
		return
	}
	at := decisionNow()
	row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id, Question: a.Reason,
		Choice: decisionText(choice), Source: db.DecisionManual, CreatedAt: at}
	if a.ByDefault {
		row.Source, row.Reason = db.DecisionAgent, defaultReason(s.botLang(ctx), s.location(ctx), a.Deadline)
	}
	if ok, err := s.insertDecision(ctx, st, row); err != nil {
		s.logf("outside decision %s: record answer: %v", a.OutsideID, err)
	} else if ok {
		s.decisionsChanged(ctx, id, at)
	}
}

// outsideBackingPending keeps an ordinary to-do close/delete from making the
// owner card unanswerable while its caller still sees a pending decision. The
// answer and withdrawal paths close the backing row only after recording state.
func (s *Service) outsideBackingPending(id string) bool {
	a, ok := s.askCopy(id)
	return ok && a.OutsideID != "" && a.AcceptedAt.IsZero() && a.WithdrawnAt.IsZero()
}

func (s *Service) closeOutsideBacking(ctx context.Context, id string) {
	t, ok := s.todoNow(id)
	if !ok || t.DoneAt != nil {
		return
	}
	now := time.Now().UTC()
	done := true
	_, err := s.markTodo(ctx, id, func(st todoStore) (*db.TodoRow, error) {
		return st.UpdateTodo(ctx, id, nil, nil, &done, now)
	}, func(row *db.TodoRow) error {
		row.DoneAt, row.UpdatedAt = &now, now
		return nil
	})
	if err != nil && !errors.Is(err, db.ErrTodoNotFound) {
		s.logf("outside decision backing to-do %s: close: %v", id, err)
	}
}

func outsideHTTPCaller(r *http.Request) (*outsideCaller, error) {
	if fullRole(r) {
		return &outsideCaller{key: "full", kind: "app", name: "Persona"}, nil
	}
	if r.Header.Get(RoleHeader) == roleExtension {
		ext := strings.TrimSpace(r.Header.Get(ExtensionHeader))
		if ext == "" {
			return nil, outsideErr(http.StatusBadRequest, "extension_unknown", errors.New("extension unknown"))
		}
		return &outsideCaller{key: "extension:" + ext, kind: "extension", name: ext}, nil
	}
	return nil, outsideErr(http.StatusForbidden, "forbidden", i18n.ErrEnglish("http-status.forbidden"))
}

func writeOutsideError(w http.ResponseWriter, err error) {
	var failure *outsideFailure
	if errors.As(err, &failure) {
		writeTypedNothing(w, failure.status, failure.code, failure.err)
		return
	}
	if errors.Is(err, errAcceptStore) {
		writeTypedNothing(w, http.StatusServiceUnavailable, "store_unavailable", err)
		return
	}
	apierr.Write(w, http.StatusInternalServerError, err)
}

func (s *Service) outsideDecisionRoutes() {
	s.handle("POST /api/v1/decisions", func(w http.ResponseWriter, r *http.Request) {
		caller, err := outsideHTTPCaller(r)
		if err != nil {
			writeOutsideError(w, err)
			return
		}
		var in assistantwire.OutsideDecisionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			writeTypedNothing(w, http.StatusBadRequest, "invalid_decision", err)
			return
		}
		rec, inserted, err := s.raiseOutsideDecision(r.Context(), caller, in)
		if err != nil {
			writeOutsideError(w, err)
			return
		}
		apierr.WriteJSON(w, outsideReply(rec, inserted))
	})
	s.handle("GET /api/v1/decisions/{id}", func(w http.ResponseWriter, r *http.Request) {
		caller, err := outsideHTTPCaller(r)
		if err == nil {
			var rec *outsideDecision
			rec, err = s.outsideForCaller(r.Context(), caller, r.PathValue("id"))
			if err == nil {
				apierr.WriteJSON(w, outsideReply(rec, false))
				return
			}
		}
		writeOutsideError(w, err)
	})
	s.handle("POST /api/v1/decisions/{id}/withdraw", func(w http.ResponseWriter, r *http.Request) {
		caller, err := outsideHTTPCaller(r)
		if err == nil {
			var rec *outsideDecision
			rec, err = s.withdrawOutsideDecision(r.Context(), caller, r.PathValue("id"))
			if err == nil {
				apierr.WriteJSON(w, outsideReply(rec, false))
				return
			}
		}
		writeOutsideError(w, err)
	})
}
