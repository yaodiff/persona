package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

// Activity is an optional store capability: old/test stores never promise a journal they cannot persist.
type workspaceBoardActivityStore interface {
	WorkspaceBoardActivity(context.Context, int64, int, string) (workspaceboard.ActivityPage, error)
	WorkspaceBoardActivityDetail(context.Context, int64, int, int) (workspaceboard.ActivityDetail, error)
}

func (s *Service) boardActivityStore(w http.ResponseWriter) workspaceBoardActivityStore {
	store, ok := s.store().(workspaceBoardActivityStore)
	if !ok {
		apierr.WriteCode(w, http.StatusServiceUnavailable, "not_supported", i18n.Err("hub-workspace_board.err_activity_unavailable"))
		return nil
	}
	return store
}

func boardActivityNumber(q url.Values, key string, fallback, min, max int64) (int64, bool) {
	values, exists := q[key]
	if !exists {
		return fallback, true
	}
	if len(values) != 1 || values[0] == "" || strings.Trim(values[0], "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	return n, err == nil && n >= min && n <= max
}

func (s *Service) getWorkspaceBoardActivity(w http.ResponseWriter, r *http.Request) {
	store := s.boardActivityStore(w)
	if store == nil {
		return
	}
	q := r.URL.Query()
	before, validBefore := boardActivityNumber(q, "before", 0, 1, 1<<63-1)
	limit, validLimit := boardActivityNumber(q, "limit", 20, 1, 50)
	card := q.Get("card_id")
	if !validBefore || !validLimit || len(q["card_id"]) > 1 || len(card) > workspaceboard.MaxIDBytes || !utf8.ValidString(card) || strings.TrimSpace(card) != card || strings.ContainsFunc(card, unicode.IsControl) {
		apierr.WriteCode(w, http.StatusBadRequest, "invalid_request", i18n.Err("hub-workspace_board.err_bad_request"))
		return
	}
	page, err := store.WorkspaceBoardActivity(r.Context(), before, int(limit), card)
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	apierr.WriteJSON(w, page)
}

func (s *Service) getWorkspaceBoardActivityDetail(w http.ResponseWriter, r *http.Request) {
	store := s.boardActivityStore(w)
	if store == nil {
		return
	}
	q := r.URL.Query()
	revision, validRevision := boardActivityNumber(url.Values{"revision": {r.PathValue("revision")}}, "revision", 0, 1, 1<<63-1)
	offset, validOffset := boardActivityNumber(q, "offset", 0, 0, 2*workspaceboard.MaxCards+2*workspaceboard.MaxColumns)
	limit, validLimit := boardActivityNumber(q, "limit", 30, 1, 100)
	if !validRevision || !validOffset || !validLimit {
		apierr.WriteCode(w, http.StatusBadRequest, "invalid_request", i18n.Err("hub-workspace_board.err_bad_request"))
		return
	}
	detail, err := store.WorkspaceBoardActivityDetail(r.Context(), revision, int(offset), int(limit))
	if errors.Is(err, pgx.ErrNoRows) {
		apierr.WriteCode(w, http.StatusNotFound, "activity_not_found", i18n.Err("hub-workspace_board.err_activity_gone"))
		return
	}
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	apierr.WriteJSON(w, detail)
}
