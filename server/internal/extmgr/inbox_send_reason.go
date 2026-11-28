package extmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/nwverse/persona/server/internal/cliwire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Why a reply of the inbox did not go out (#9125). A send that may have gone out is never one of these: it is
// assistant_in_doubt (sendInboxOnce). Each is a coded *rpc.Error whose message is a fixed English sentence: the
// connector's own words (a CLI's output, an extension's message, a thread id) stay out of it and go to the log as an
// allowlisted kind only (inboxSendLogKind).

// inboxSendRefusal is err, a reply's failure that is known not to have gone out, as the apps may be told it.
func inboxSendRefusal(err error) *rpc.Error {
	code := assistantwire.CodeInboxSendFailed
	var retryAfter float64
	var re *rpc.Error
	var cli *cliwire.Error
	switch {
	case errors.As(err, &re) && isInboxSendCode(re.Code): // already said: kept, in this code's words
		code, retryAfter = re.Code, retryAfterOf(re)
	case errors.Is(err, errCannotReply):
		code = assistantwire.CodeInboxSourceUnavailable // its connector is not here (switched off), or mail is not set up
	case errors.Is(err, connector.ErrCannotReply):
		code = assistantwire.CodeInboxCannotReply
	case errors.As(err, &cli) && cli != nil:
		switch cli.Kind {
		case cliwire.KindNotInstalled, cliwire.KindUnsupportedVersion:
			code = assistantwire.CodeInboxSourceUnavailable
		case cliwire.KindNotLoggedIn, cliwire.KindExpired:
			code = assistantwire.CodeInboxSourceSignedOut
		case cliwire.KindMissingPermission:
			code = assistantwire.CodeInboxSendDenied
		case cliwire.KindBotNotInChat:
			code = assistantwire.CodeInboxBotNotInChat
		case cliwire.KindRateLimited:
			code, retryAfter = assistantwire.CodeInboxSendRateLimited, cli.RetryAfter.Seconds()
		}
	case errors.As(err, &re) && re.Code == proto.ToolErrorRateLimited: // an extension's connector (connector.Registry's convention)
		code, retryAfter = assistantwire.CodeInboxSendRateLimited, retryAfterOf(re)
	}
	return inboxSendError(code, retryAfter)
}

// retryAfterOf is an error's data.retry_after in seconds (0: it did not say).
func retryAfterOf(re *rpc.Error) float64 {
	var data struct {
		RetryAfter float64 `json:"retry_after"`
	}
	_ = json.Unmarshal(re.Data, &data)
	return data.RetryAfter
}

// inboxSendError is the refusal code, with how long a rate limit lasts when it is known (seconds, rounded up).
func inboxSendError(code string, retryAfter float64) *rpc.Error {
	e := &rpc.Error{Code: code, Message: i18n.T(i18n.En, inboxSendKeys[code])}
	if retryAfter > 0 {
		e.Data, _ = json.Marshal(map[string]int{"retry_after": int(math.Ceil(retryAfter))})
	}
	return e
}

// inboxSendKeys are the refusals' sentences (inbox-errors.json); the manager words them in the user's language by code.
var inboxSendKeys = map[string]string{
	assistantwire.CodeInboxCannotReply:       "inbox-errors.send_cannot_reply",
	assistantwire.CodeInboxSourceUnavailable: "inbox-errors.send_source_unavailable",
	assistantwire.CodeInboxSourceSignedOut:   "inbox-errors.send_signed_out",
	assistantwire.CodeInboxSendDenied:        "inbox-errors.send_denied",
	assistantwire.CodeInboxBotNotInChat:      "inbox-errors.bot_not_in_chat",
	assistantwire.CodeInboxSendRateLimited:   "inbox-errors.send_rate_limited",
	assistantwire.CodeInboxSendFailed:        "inbox-errors.send_failed",
	assistantwire.CodeInboxSendUnrecorded:    "inbox-errors.send_unrecorded",
}

func isInboxSendCode(code string) bool { _, ok := inboxSendKeys[code]; return ok }

// inboxSendLogKind is what the log may say of a failed send's cause: lark-cli's own safe words (they never carry its
// output), else only the error's code and type. An extension's or a mail server's message can quote what was sent.
func inboxSendLogKind(err error) string {
	var cli *cliwire.Error
	var re *rpc.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &cli) && cli != nil:
		return "lark-cli " + string(cli.Kind) + ": " + cli.Error()
	case errors.Is(err, errCannotReply), errors.Is(err, connector.ErrCannotReply):
		return err.Error() // fixed words, no identifiers
	case errors.Is(err, mailwire.ErrMaybeSent):
		return "mail: " + mailwire.ErrMaybeSent.Error() // its own fixed words, not what wraps them (the server's address)
	case errors.As(err, &re):
		return fmt.Sprintf("rpc code %q", re.Code)
	}
	return fmt.Sprintf("%T", err)
}
