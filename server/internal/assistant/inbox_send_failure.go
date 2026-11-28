package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// inboxSendFailure is how the apps get one reason a reply did not go out (#9125; assistantwire's CodeInbox* send
// codes): its status, whether sending the same reply again can work as it is, and its sentence (inbox-errors.json).
// Every one is delivered "none": the extension manager answers a send that may have gone out as assistant_in_doubt.
type inboxSendFailure struct {
	status    int
	retryable bool
	key       string
}

var inboxSendFailures = map[string]inboxSendFailure{
	assistantwire.CodeInboxCannotReply:       {http.StatusConflict, false, "inbox-errors.send_cannot_reply"},
	assistantwire.CodeInboxSourceUnavailable: {http.StatusServiceUnavailable, false, "inbox-errors.send_source_unavailable"},
	assistantwire.CodeInboxSourceSignedOut:   {http.StatusServiceUnavailable, false, "inbox-errors.send_signed_out"},
	assistantwire.CodeInboxSendDenied:        {http.StatusBadGateway, false, "inbox-errors.send_denied"},
	assistantwire.CodeInboxBotNotInChat:      {http.StatusBadGateway, false, "inbox-errors.bot_not_in_chat"},
	assistantwire.CodeInboxSendRateLimited:   {http.StatusTooManyRequests, true, "inbox-errors.send_rate_limited"},
	assistantwire.CodeInboxSendFailed:        {http.StatusBadGateway, true, "inbox-errors.send_failed"},
	assistantwire.CodeInboxSendUnrecorded:    {http.StatusServiceUnavailable, true, "inbox-errors.send_unrecorded"},
}

// writeInboxSendError writes err, the failure of a reply's send for item id (what: "reply", "suggestion"), when it
// is one this code words: in doubt (delivered unknown, never retryable), or a coded reason nothing went out. The
// log gets the item, the code and the request's id, never the error's words. False: err is none of them.
func (s *Service) writeInboxSendError(w http.ResponseWriter, r *http.Request, what, id string, err error, items ...InboxItem) bool {
	l := i18n.OfWriter(w)
	withItem := func(body map[string]any) map[string]any {
		if len(items) > 0 && items[0].ID != "" {
			body["item"] = items[0]
		}
		return body
	}
	if replyInDoubt(err) {
		// It may have gone out: the apps say so (delivered unknown); the same reply again is not sent twice. The
		// sentence is ours: an older manager's in-doubt message carried the source's own error.
		s.logf("inbox: %s of %s may have gone out: %s (request %s)", what, id, assistantwire.CodeAssistantInDoubt, inboxRequestID(w, r))
		apierr.WriteBody(w, http.StatusBadGateway, withItem(map[string]any{"code": assistantwire.CodeAssistantInDoubt,
			"error": i18n.T(l, "rpc-errors.in_doubt"), "delivered": "unknown", "retryable": false}))
		return true
	}
	code := ""
	var re *rpc.Error
	switch {
	case errors.Is(err, errCannotReply): // the item itself offers no way to answer it
		code = assistantwire.CodeInboxCannotReply
	case errors.As(err, &re):
		code = re.Code
	}
	f, ok := inboxSendFailures[code]
	if !ok {
		if err != nil {
			s.logf("inbox: %s of %s failed: %T (request %s)", what, id, err, inboxRequestID(w, r))
		}
		return false
	}
	s.logf("inbox: %s of %s not sent: %s (request %s)", what, id, code, inboxRequestID(w, r))
	body := map[string]any{"code": code, "error": i18n.T(l, f.key), "retryable": f.retryable, "delivered": "none"}
	if re != nil && len(re.Data) > 0 {
		var data struct {
			RetryAfter int `json:"retry_after"`
		}
		if json.Unmarshal(re.Data, &data) == nil && data.RetryAfter > 0 {
			body["retry_after"] = data.RetryAfter
			w.Header().Set("Retry-After", strconv.Itoa(data.RetryAfter))
		}
	}
	apierr.WriteBody(w, f.status, withItem(body))
	return true
}

// inboxRequestID is the request's id as the hub logs it (X-Request-Id, from the app's X-Client-Request-Id), for the
// log line that says why it failed; "-" when there is none. Only an id's characters are taken.
func inboxRequestID(w http.ResponseWriter, r *http.Request) string {
	id := w.Header().Get("X-Request-Id")
	if id == "" {
		id = r.Header.Get("X-Client-Request-Id")
	}
	if n := len(id); n < 4 || n > 64 || strings.Trim(id, "0123456789abcdefABCDEF-") != "" {
		return "-"
	}
	return id
}
