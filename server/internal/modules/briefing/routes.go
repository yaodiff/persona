package briefing

import (
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

const handleLaterRoute = "POST /api/v1/later/{id}/handled"

// POST /api/v1/later/{id}/handled: the Handled button of a question in a `brief` item's later list. It leaves the queue
// (an ask_user nobody answered ends with it: its to-do is closed, laterHandled); a question already handled, dropped (its
// to-do ended, or 7 days) or unknown is 409. A queue, or such a to-do, that can't be read or written is 503, and the
// question stays.
func (runner *runner) handleLater(w http.ResponseWriter, r *http.Request) {
	if role := r.Header.Get(assistantwire.RoleHeader); role != "" && role != "full" {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change the queue"))
		return
	}
	if runner.later == nil {
		apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
		return
	}
	ok, err := runner.later.Handle(r.Context(), r.PathValue("id"))
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
		return
	}
	if !ok {
		apierr.Write(w, http.StatusConflict, i18n.Err("assistant-feishu_cards.already_handled"))
		return
	}
	apierr.WriteJSON(w, map[string]any{"ok": true})
}
