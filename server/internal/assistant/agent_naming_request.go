package assistant

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A requested name (#7432): the user asks for one Agent session's automatic name now, from the Agent's menu
// (POST /api/v1/sessions/{mid}/{sid}/autoname, relayed by the hub). It is the background naming's own model call
// (agentNameFromModel: the same dialogue, prompt and validity check) without the background's guards. The user asked,
// so the per-session change limit, "placeholder names only", the automatic-naming switch and a custom title do not
// hold it back. The result is an automatic name in the same ledger: nothing is typed into the Agent, its tab keeps
// its name, and a later native or hand-written rename still wins. The assistant's own conversations are refused: they
// are named by their own rules (titles.go).

// agentNameRefusal is a requested name the manager did not give: the HTTP status, the stable code the apps read and
// the sentence in the request's language. retry says whether asking again as it is can work.
type agentNameRefusal struct {
	status int
	code   string
	retry  bool
	err    error
}

func (e *agentNameRefusal) Error() string { return e.err.Error() }
func (e *agentNameRefusal) Unwrap() error { return e.err }

// agentNameRefusals are the naming call's static reasons (agentNameFromModel) as the apps get them. A truncated
// answer is an invalid name to the user; a canceled call can only be the manager stopping.
var agentNameRefusals = map[string]agentNameRefusal{
	"model_not_configured":   {status: http.StatusConflict, code: "model_not_configured", err: i18n.Err("assistant-agent_naming.model_not_configured")},
	"no_dialogue":            {status: http.StatusConflict, code: "no_dialogue", err: i18n.Err("assistant-agent_naming.no_dialogue")},
	"invalid_name":           {status: http.StatusBadGateway, code: "invalid_name", retry: true, err: i18n.Err("assistant-agent_naming.invalid_name")},
	"model_output_truncated": {status: http.StatusBadGateway, code: "invalid_name", retry: true, err: i18n.Err("assistant-agent_naming.invalid_name")},
	"model_call_failed":      {status: http.StatusBadGateway, code: "model_failed", retry: true, err: i18n.Err("assistant-agent_naming.model_failed")},
	"dialogue_unavailable":   {status: http.StatusBadGateway, code: "dialogue_unavailable", retry: true, err: i18n.Err("assistant-agent_naming.dialogue_unavailable")},
	"naming_timeout":         {status: http.StatusGatewayTimeout, code: "timeout", retry: true, err: i18n.Err("assistant-agent_naming.timeout")},
	"naming_canceled":        {status: http.StatusServiceUnavailable, code: "canceled", retry: true, err: i18n.Err("assistant-agent_naming.timeout")},
}

var (
	errAgentNameNoMachine = &agentNameRefusal{status: http.StatusNotFound, err: i18n.Err("devices-api.unknown_machine")}
	errAgentNameNoSession = &agentNameRefusal{status: http.StatusNotFound, err: i18n.Err("hub-deleted_sessions.unknown")}
	errAgentNameAssistant = &agentNameRefusal{status: http.StatusBadRequest, code: "assistant_session", err: i18n.Err("assistant-agent_naming.assistant_session")}
	errAgentNameLedger    = &agentNameRefusal{status: http.StatusServiceUnavailable, code: "names_unavailable", retry: true, err: i18n.Err("assistant-profile.agent_names_unavailable")}
	errAgentNameSave      = &agentNameRefusal{status: http.StatusInternalServerError, code: "save_failed", retry: true, err: i18n.Err("assistant-agent_naming.save_failed")}
	errAgentNameRenamed   = &agentNameRefusal{status: http.StatusConflict, code: "renamed_meanwhile", err: i18n.Err("assistant-agent_naming.renamed_meanwhile")}
)

// agentNameResult is a requested name as given: the title the lists show now, and whether it differs from the one
// they showed before.
type agentNameResult struct {
	Title   string
	Changed bool
}

func (s *Service) agentNamingRoutes() {
	s.handle("POST /api/v1/sessions/{mid}/{sid}/autoname", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-agent_naming.forbidden"))
			return
		}
		res, err := s.requestAgentName(r.Context(), r.PathValue("mid"), r.PathValue("sid"))
		if err != nil {
			refusal := errAgentNameSave
			errors.As(err, &refusal)
			body := apierr.LocalizedBody(refusal.status, refusal.code, refusal.err, i18n.OfWriter(w))
			body["retryable"] = refusal.retry
			apierr.WriteBody(w, refusal.status, body)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "title": res.Title, "title_source": "auto", "changed": res.Changed})
	})
}

// requestAgentName names machine's session sid with the model now, as the user asked. Requests for one session that
// arrive while its call is out share that call and its answer: one model call, one ledger write, one list update.
func (s *Service) requestAgentName(ctx context.Context, machine, sid string) (agentNameResult, error) {
	if m, ok := s.fleetMachine(machine); ok {
		machine = m.ID // an id another one absorbed names that one
	} else if s.fleetOn() {
		return agentNameResult{}, errAgentNameNoMachine
	}
	if hook := s.agentNames.beforeRequest; hook != nil {
		hook()
	}
	v, err, _ := s.agentNames.requests.Do(agentNameKey(machine, sid), func() (any, error) {
		// The request that started the call may go away; the ones that joined it still get its answer.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentNamingTimeout+15*time.Second)
		defer cancel()
		return s.nameAgentNow(ctx, machine, sid)
	})
	if err != nil {
		return agentNameResult{}, err
	}
	return v.(agentNameResult), nil
}

func (s *Service) nameAgentNow(ctx context.Context, machine, sid string) (agentNameResult, error) {
	// The raw node row, as the background pass reads it: the name is kept against the native title.
	c, found := s.rawAgentForNaming(ctx, machine, sid)
	if !found {
		return agentNameResult{}, errAgentNameNoSession
	}
	if c.Assistant || s.isMain(ctx, sid) {
		return agentNameResult{}, errAgentNameAssistant
	}
	key := agentNameKey(machine, sid)
	b := &s.agentNames
	b.mu.Lock()
	loaded := s.agentNamesLocked(ctx)
	before := b.records[key]
	b.mu.Unlock()
	if !loaded {
		return agentNameResult{}, errAgentNameLedger // a write over an unread ledger would drop every other name
	}
	shown := s.agentDisplayName(machine, c)
	if shown == "" {
		shown = c.Title
	}
	callCtx, cancel := context.WithTimeout(ctx, agentNamingTimeout)
	title, reason := s.agentNameFromModel(callCtx, machine, sid)
	cancel()
	if reason != "" {
		s.logf("agent naming: requested session=%q/%q reason=%s", machine, sid, reason)
		refusal, known := agentNameRefusals[reason]
		if !known {
			refusal = agentNameRefusals["model_call_failed"]
		}
		return agentNameResult{}, &refusal
	}
	// The name stands over the native title as it is now: one that changed during the call is the one it covers.
	current, found := s.rawAgentForNaming(ctx, machine, sid)
	if !found {
		return agentNameResult{}, errAgentNameNoSession
	}
	if current.Assistant {
		return agentNameResult{}, errAgentNameAssistant
	}
	// A name written by hand while the model was asked is the later wish: in the app (the ledger's manual record
	// is new or changed) or in the Agent itself (a custom title that was not there when the call started).
	nativeRename := (current.TitleManual || current.TitleSource.String() == "manual") && !sameAgentRawTitle(c.Title, current.Title)
	b.mu.Lock()
	r := b.records[key]
	if nativeRename || r.Manual && (!before.Manual || r.Title != before.Title) {
		b.mu.Unlock()
		s.logf("agent naming: requested session=%q/%q reason=renamed_meanwhile", machine, sid)
		return agentNameResult{}, errAgentNameRenamed
	}
	r.Machine, r.Session = machine, sid
	r.Title, r.RawTitle = title, current.Title
	// An automatic name from now on, whatever it was. The background's change count is its own: this neither spends
	// nor resets it. Its failures and their cooldown are over, since the model just named this session.
	r.Manual, r.Requested = false, true
	r.NamedAt, r.NamedOutput = time.Now(), current.OutputTokens
	r.Failures, r.LastFailure, r.NextAttempt = 0, "", time.Time{}
	err := s.putAgentNameLocked(ctx, key, r)
	b.mu.Unlock()
	if err != nil {
		s.logf("agent naming: requested session=%q/%q reason=result_write_failed", machine, sid)
		return agentNameResult{}, errAgentNameSave
	}
	s.publishSessions(machine) // the apps' lists follow before the request is answered
	s.logf("agent naming: requested session=%q/%q result=named", machine, sid)
	return agentNameResult{Title: title, Changed: title != shown}, nil
}
