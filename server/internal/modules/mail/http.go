package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Handler returns the HTTP handler for /api/v1/mail/... . Authentication is
// done by the hub before requests reach this handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/mail/accounts", s.handleListAccounts)
	mux.HandleFunc("POST /api/v1/mail/accounts", s.handleAddAccount)
	mux.HandleFunc("GET /api/v1/mail/oauth/google/config", s.handleGoogleConfig)
	mux.HandleFunc("POST /api/v1/mail/oauth/google/start", s.handleGoogleStart)
	mux.HandleFunc("POST /api/v1/mail/oauth/google/finish", s.handleGoogleFinish)
	mux.HandleFunc("GET /api/v1/mail/oauth/microsoft/config", s.handleMicrosoftConfig)
	mux.HandleFunc("PUT /api/v1/mail/oauth/microsoft/config", s.handleSetMicrosoftConfig)
	mux.HandleFunc("POST /api/v1/mail/oauth/microsoft/start", s.handleMicrosoftStart)
	mux.HandleFunc("POST /api/v1/mail/oauth/microsoft/finish", s.handleMicrosoftFinish)
	mux.HandleFunc("DELETE /api/v1/mail/accounts/{aid}", s.handleDeleteAccount)
	mux.HandleFunc("POST /api/v1/mail/accounts/{aid}/test", s.handleTestAccount)
	mux.HandleFunc("GET /api/v1/mail/accounts/{aid}/mailboxes", s.handleMailboxes)
	mux.HandleFunc("GET /api/v1/mail/messages", s.handleListMessages)
	mux.HandleFunc("GET /api/v1/mail/messages/{aid}/{uid}", s.handleGetMessage)
	mux.HandleFunc("GET /api/v1/mail/messages/{aid}/{uid}/attachments/{attachment}", s.handleAttachment)
	mux.HandleFunc("POST /api/v1/mail/messages/{aid}/{uid}/flags", s.handleSetFlags)
	mux.HandleFunc("POST /api/v1/mail/messages/{aid}/{uid}/move", s.handleMove)
	mux.HandleFunc("POST /api/v1/mail/send", s.handleSend)
	// ServeMux answers 404/405 in plain text; the API contract is JSON errors.
	// Binary downloads stream directly, retaining ServeContent's Range response headers.
	json := jsonErrors{mux}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Contains(r.URL.Path, "/attachments/") {
			mux.ServeHTTP(w, r)
			return
		}
		json.ServeHTTP(w, r)
	})
}

// jsonErrors rewrites ServeMux's plain-text 404/405 responses as
// {"error":"..."} so every failure looks the same to the app.
type jsonErrors struct {
	h http.Handler
}

func (j jsonErrors) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w}
	j.h.ServeHTTP(rec, r)
	if rec.status < 400 || rec.jsonWritten || rec.body.Len() == 0 {
		if rec.headerWritten {
			w.WriteHeader(rec.status)
		}
		w.Write(rec.body.Bytes())
		return
	}
	msg := strings.TrimSpace(rec.body.String())
	if msg == "" {
		msg = http.StatusText(rec.status)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(rec.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// statusRecorder buffers a handler's response so it can be rewritten.
type statusRecorder struct {
	http.ResponseWriter
	status        int
	headerWritten bool
	jsonWritten   bool
	body          bytes.Buffer
}

// Lang passes on the request's language (i18n.Carrier) without exposing the real writer: Unwrap would let
// http.ResponseController flush past the buffer.
func (r *statusRecorder) Lang() i18n.Lang { return i18n.OfWriter(r.ResponseWriter) }

func (r *statusRecorder) WriteHeader(status int) {
	if r.headerWritten {
		return
	}
	r.headerWritten = true
	r.status = status
	if ct := r.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
		r.jsonWritten = true
	}
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.headerWritten {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	var unknown *UnknownAccountError
	var incomplete *IncompleteSearchError
	var unreachable *UnreachableError
	switch {
	case errors.As(err, &unknown):
		apierr.WriteCode(w, status, unknown.Code(), unknown)
		return
	case errors.As(err, &incomplete):
		apierr.WriteCode(w, status, incomplete.Code(), incomplete)
		return
	case errors.As(err, &unreachable):
		apierr.WriteCode(w, status, unreachable.Code(), unreachable)
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// statusFor maps service errors onto HTTP status codes.
func statusFor(err error) int {
	msg := err.Error()
	var ue userError
	var unknown *UnknownAccountError
	var incomplete *IncompleteSearchError
	var unreachable *UnreachableError
	switch {
	case errors.As(err, &unknown):
		return unknown.HTTPStatus()
	case errors.As(err, &incomplete):
		return incomplete.HTTPStatus()
	case errors.As(err, &unreachable):
		return unreachable.HTTPStatus()
	case errors.As(err, &ue):
		return http.StatusBadRequest
	case strings.Contains(msg, "not found"),
		strings.Contains(msg, "unknown mailbox"):
		return http.StatusNotFound
	case errors.Is(err, ErrAccountExists):
		return http.StatusConflict
	case strings.Contains(msg, "is required"),
		strings.Contains(msg, "invalid"),
		strings.Contains(msg, "unknown mail provider"),
		strings.Contains(msg, "not configured"),
		strings.Contains(msg, "start again"),
		strings.Contains(msg, "not granted"),
		strings.Contains(msg, "no recipients"):
		return http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	writeError(w, statusFor(err), err)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON body: "+err.Error()))
		return false
	}
	return true
}

func (s *Service) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.ListAccounts()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Service) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var req AddAccountRequest
	if !decodeBody(w, r, &req) {
		return
	}
	account, err := s.AddAccount(req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Service) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoginHint string `json:"login_hint"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	start, err := s.StartGoogleOAuth(req.LoginHint)
	if errors.Is(err, errGoogleNotConfigured) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": googleNotConfiguredText(r), "code": "google_not_configured"})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

func (s *Service) handleGoogleFinish(w http.ResponseWriter, r *http.Request) {
	var req OAuthFinishRequest
	if !decodeBody(w, r, &req) {
		return
	}
	account, err := s.FinishGoogleOAuth(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Service) handleGoogleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.GoogleConfig())
}

func (s *Service) handleMicrosoftConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.MicrosoftConfig(r.Context()))
}

// handleSetMicrosoftConfig keeps the client ID the user registered in Entra ({"client_id": ""} forgets it).
func (s *Service) handleSetMicrosoftConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID string `json:"client_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.SetMicrosoftClientID(r.Context(), req.ClientID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.MicrosoftConfig(r.Context()))
}

func (s *Service) handleMicrosoftStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoginHint string `json:"login_hint"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	start, err := s.StartMicrosoftOAuth(r.Context(), req.LoginHint)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

func (s *Service) handleMicrosoftFinish(w http.ResponseWriter, r *http.Request) {
	var req OAuthFinishRequest
	if !decodeBody(w, r, &req) {
		return
	}
	account, err := s.FinishMicrosoftOAuth(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Service) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteAccount(r.PathValue("aid")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.TestAccount(r.PathValue("aid")); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleMailboxes(w http.ResponseWriter, r *http.Request) {
	boxes, err := s.Mailboxes(r.Context(), r.PathValue("aid"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if boxes == nil {
		boxes = []Mailbox{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": boxes})
}

func (s *Service) handleListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseUint(q.Get("before_uid"), 10, 32)
	res, err := s.ListMessages(r.Context(), ListOptions{
		AccountID: q.Get("account_id"),
		Mailbox:   q.Get("mailbox"),
		Limit:     limit,
		BeforeUID: uint32(before),
		Query:     q.Get("q"),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.Messages == nil {
		res.Messages = []MailSummary{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Service) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid uid"))
		return
	}
	msg, err := s.GetMessage(r.Context(), r.PathValue("aid"), uint32(uid), r.URL.Query().Get("mailbox"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg})
}

func (s *Service) handleSetFlags(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid uid"))
		return
	}
	var req SetFlagsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.SetFlags(r.Context(), r.PathValue("aid"), uint32(uid), req); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleMove(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil || uid == 0 {
		apierr.WriteCode(w, http.StatusBadRequest, "invalid_params", invalidMove)
		return
	}
	var req MoveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	result, err := s.moveMessage(r.Context(), r.PathValue("aid"), uint32(uid), req)
	if err != nil {
		failure := moveResponseError(err)
		apierr.WriteCode(w, failure.HTTPStatus(), failure.Code(), failure)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Send(r.Context(), req); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
