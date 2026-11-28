package mail

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/nwverse/persona/server/internal/i18n"
)

type moveError struct {
	code   string
	key    string
	status int
}

func (e moveError) Error() string { return e.Text(i18n.En) }
func (e moveError) Text(lang i18n.Lang) string {
	return i18n.T(lang, "mail-errors."+e.key)
}
func (e moveError) Code() string    { return e.code }
func (e moveError) HTTPStatus() int { return e.status }

var (
	moveApprovalRequired = moveError{"approval_required", "move_approval_required", http.StatusForbidden}
	invalidMove          = moveError{"invalid_params", "move_invalid", http.StatusBadRequest}
	sameMoveMailbox      = moveError{"invalid_params", "move_same_mailbox", http.StatusBadRequest}
	moveMailboxMissing   = moveError{"mailbox_not_found", "move_mailbox_missing", http.StatusNotFound}
	moveMessageMissing   = moveError{"message_not_found", "move_message_missing", http.StatusNotFound}
	moveValidityChanged  = moveError{"mail_uid_validity_changed", "move_validity_changed", http.StatusConflict}
	moveGmailArchive     = moveError{"unsupported", "move_gmail_archive", http.StatusUnprocessableEntity}
	moveUnsupported      = moveError{"unsupported", "move_unsupported", http.StatusUnprocessableEntity}
	moveFailed           = moveError{"mail_move_failed", "move_failed", http.StatusBadGateway}
	moveTimedOut         = moveError{"mail_move_timeout", "move_timeout", http.StatusGatewayTimeout}
	movePartial          = moveError{"mail_move_partial", "move_partial", http.StatusConflict}
	moveUnknown          = moveError{"mail_move_unknown", "move_unknown", http.StatusConflict}
)

func (s *Service) moveAccount(accountID string, uid uint32, req MoveRequest) (*MailAccount, error) {
	if accountID == "" || uid == 0 || req.UIDValidity == 0 || req.Mailbox == "" ||
		(req.To == "") == (req.ToMailbox == "") || strings.ContainsRune(req.Mailbox+req.ToMailbox, 0) {
		return nil, invalidMove
	}
	switch req.To {
	case "", RoleArchive, RoleJunk, RoleTrash, RoleInbox:
	default:
		return nil, invalidMove
	}
	if sameMailbox(req.Mailbox, req.ToMailbox) {
		return nil, sameMoveMailbox
	}
	a, err := s.account(accountID)
	if err != nil {
		return nil, err
	}
	if req.To == RoleArchive && (a.Provider == ProviderGmail || strings.EqualFold(a.IMAPHost, "imap.gmail.com")) {
		return nil, moveGmailArchive
	}
	return a, nil
}

// Only INBOX is case-insensitive in IMAP. Preserve the exact name for every other mailbox.
func sameMailbox(a, b string) bool {
	return a == b || strings.EqualFold(a, "INBOX") && strings.EqualFold(b, "INBOX")
}

func moveDestination(boxes []*imap.ListData, req MoveRequest) (string, error) {
	var attr imap.MailboxAttr
	switch req.To {
	case RoleArchive:
		attr = imap.MailboxAttrArchive
	case RoleJunk:
		attr = imap.MailboxAttrJunk
	case RoleTrash:
		attr = imap.MailboxAttrTrash
	}
	var fallback string
	for _, box := range boxes {
		if hasAttr(box.Attrs, imap.MailboxAttrNoSelect) || hasAttr(box.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		if req.ToMailbox != "" {
			if sameMailbox(req.ToMailbox, box.Mailbox) {
				return box.Mailbox, nil
			}
			continue
		}
		if attr != "" && hasAttr(box.Attrs, attr) {
			return box.Mailbox, nil
		}
		if fallback == "" && roleOf(box.Mailbox, box.Attrs) == req.To {
			fallback = box.Mailbox
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", moveMailboxMissing
}

func moveCapabilities(c *imapclient.Client) (imap.CapSet, error) {
	caps := c.Caps()
	if ended(c) {
		return nil, net.ErrClosed
	}
	if !caps.Has(imap.CapMove) && !caps.Has(imap.CapUIDPlus) {
		return nil, moveUnsupported
	}
	return caps, nil
}

// checkMove runs on the same selected connection as the write. For an approval preview it examines the mailbox
// read-only and only fetches the envelope needed to show the owner which message will move.
func checkMove(c *imapclient.Client, caps imap.CapSet, uid uint32, req MoveRequest, preview bool) (destination, subject string, err error) {
	boxes, err := c.List("", "*", listOptions(caps, nil)).Collect()
	if err != nil {
		return "", "", failedAt("list move destinations", err)
	}
	// Require a name from LIST so an alternate spelling accepted by SELECT cannot bypass the same-mailbox check.
	source, err := moveDestination(boxes, MoveRequest{ToMailbox: req.Mailbox})
	if err != nil {
		return "", "", err
	}
	destination, err = moveDestination(boxes, req)
	if err != nil {
		return "", "", err
	}
	if sameMailbox(source, destination) {
		return "", "", sameMoveMailbox
	}
	selected, err := c.Select(source, &imap.SelectOptions{ReadOnly: preview}).Wait()
	if err != nil {
		return "", "", failedAt("select move source", err)
	}
	if selected.UIDValidity != req.UIDValidity {
		return "", "", moveValidityChanged
	}
	messages, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, Envelope: preview}).Collect()
	if err != nil {
		return "", "", failedAt("find move source", err)
	}
	for _, message := range messages {
		if message.UID == imap.UID(uid) {
			if message.Envelope != nil {
				subject = message.Envelope.Subject
			}
			return destination, subject, nil
		}
	}
	return "", "", moveMessageMissing
}

func (s *Service) moveMessage(ctx context.Context, accountID string, uid uint32, req MoveRequest) (MoveResult, error) {
	a, err := s.moveAccount(accountID, uid, req)
	if err != nil {
		return MoveResult{}, err
	}
	var (
		result MoveResult
		phase  atomic.Uint32
	)
	err = s.withConnAtRetry(ctx, a, "move message", func(c *imapclient.Client, deadline time.Time) error {
		caps, err := moveCapabilities(c)
		if err != nil {
			return err
		}
		dest, _, err := checkMove(c, caps, uid, req, false)
		if err != nil {
			return err
		}
		// Check capabilities again after selecting the source. A server that closed while preparing the move
		// has not seen a write and can still be reconnected using the normal rules.
		caps, err = moveCapabilities(c)
		if err != nil {
			return err
		}
		beforeWrite := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !time.Now().Before(deadline) || phase.Load() == moveStopped {
				return ErrTimedOut
			}
			return nil
		}
		result, err = moveOnConn(c, caps, imap.UID(uid), dest, &phase, beforeWrite)
		return err
	}, func() bool { return phase.Load() == movePreparing })
	if err == nil {
		return result, nil
	}
	// Seal the operation even if the callback is still finishing after the caller cancelled or timed out.
	switch phase.Swap(moveStopped) {
	case movePreparing:
		return MoveResult{}, err
	case moveNative:
		// BAD rejects the command itself. NO may follow an applied move (RFC 6851 §3.3).
		var response *imap.Error
		if errors.As(err, &response) && response.Type == imap.StatusResponseTypeBad {
			return MoveResult{}, moveFailed
		}
	case moveCopying:
		var response *imap.Error
		if errors.As(err, &response) && (response.Type == imap.StatusResponseTypeNo || response.Type == imap.StatusResponseTypeBad) {
			return MoveResult{}, moveFailed
		}
	case moveCopied:
		// COPY succeeded. The final location of the source may need checking, but there is a known destination copy.
		return MoveResult{}, movePartial
	}
	return MoveResult{}, moveUnknown
}

const (
	movePreparing uint32 = iota
	moveNative
	moveCopying
	moveCopied
	moveStopped
)

func moveOnConn(c *imapclient.Client, caps imap.CapSet, uid imap.UID, dest string, phase *atomic.Uint32, beforeWrite func() error) (MoveResult, error) {
	if err := beforeWrite(); err != nil {
		return MoveResult{}, err
	}
	uids := imap.UIDSetNum(uid)
	if caps.Has(imap.CapMove) {
		if !phase.CompareAndSwap(movePreparing, moveNative) {
			return MoveResult{}, ErrTimedOut
		}
		data, err := c.Move(uids, dest).Wait()
		if err != nil {
			return MoveResult{}, failedAt("UID MOVE", err)
		}
		src, _ := data.SourceUIDs.(imap.UIDSet)
		dst, _ := data.DestUIDs.(imap.UIDSet)
		return movedTo(dest, uid, data.UIDValidity, src, dst), nil
	}
	// The library's fallback pipelines COPY, STORE and EXPUNGE. Wait after each step so a failed copy cannot
	// be followed by a STORE or an EXPUNGE, and only ever expunge the requested UID.
	if !phase.CompareAndSwap(movePreparing, moveCopying) {
		return MoveResult{}, ErrTimedOut
	}
	data, err := c.Copy(uids, dest).Wait()
	if err != nil {
		return MoveResult{}, failedAt("UID COPY", err)
	}
	if !phase.CompareAndSwap(moveCopying, moveCopied) {
		return MoveResult{}, ErrTimedOut
	}
	if err := beforeWrite(); err != nil {
		return MoveResult{}, err
	}
	if err := c.Store(uids, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return MoveResult{}, failedAt("UID STORE", err)
	}
	if err := beforeWrite(); err != nil {
		return MoveResult{}, err
	}
	if err := c.UIDExpunge(uids).Close(); err != nil {
		return MoveResult{}, failedAt("UID EXPUNGE", err)
	}
	return movedTo(dest, uid, data.UIDValidity, data.SourceUIDs, data.DestUIDs), nil
}

func movedTo(mailbox string, uid imap.UID, validity uint32, source, dest imap.UIDSet) MoveResult {
	out := MoveResult{Mailbox: mailbox}
	if validity != 0 && len(source) == 1 && source[0].Start == uid && source[0].Stop == uid &&
		len(dest) == 1 && dest[0].Start != 0 && dest[0].Start == dest[0].Stop {
		out.UID, out.UIDValidity = uint32(dest[0].Start), validity
	}
	return out
}

type moveFailure interface {
	error
	Code() string
	HTTPStatus() int
}

// moveResponseError keeps expected failures and hides raw provider replies from API and tool callers.
func moveResponseError(err error) moveFailure {
	var coded moveFailure
	switch {
	case errors.As(err, &coded):
		return coded
	case errors.Is(err, ErrTimedOut), errors.Is(err, context.DeadlineExceeded):
		return moveTimedOut
	default:
		return moveFailed
	}
}
