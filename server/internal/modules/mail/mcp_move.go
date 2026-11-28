package mail

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// PrepareMove describes the exact message and target using read-only IMAP commands. Keep the resolved mailbox in
// the operation so a change to the server's SPECIAL-USE roles while the card is open cannot redirect the move.
func (s *Service) PrepareMove(ctx context.Context, args json.RawMessage) (string, mailwire.PreparedMove, error) {
	var req struct {
		AccountID string `json:"account_id"`
		UID       uint32 `json:"uid"`
		MoveRequest
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return "", nil, invalidMove
	}
	a, err := s.moveAccount(req.AccountID, req.UID, req.MoveRequest)
	if err != nil {
		return "", nil, moveResponseError(err)
	}
	var destination, subject string
	err = s.withConn(ctx, a, "preview move", func(c *imapclient.Client) error {
		caps, err := moveCapabilities(c)
		if err != nil {
			return err
		}
		destination, subject, err = checkMove(c, caps, req.UID, req.MoveRequest, true)
		return err
	})
	if err != nil {
		return "", nil, moveResponseError(err)
	}
	l := i18n.From(ctx)
	if subject == "" {
		subject = i18n.T(l, "mail-errors.move_no_subject")
	}
	summary := i18n.T(l, "mail-errors.move_summary", i18n.Args{
		"account": strconv.QuoteToGraphic(a.Email), "source": strconv.QuoteToGraphic(req.Mailbox),
		"target": strconv.QuoteToGraphic(destination), "subject": strconv.QuoteToGraphic(subject),
	})
	req.To, req.ToMailbox = "", destination
	var consumed atomic.Bool
	return summary, func(ctx context.Context) (MoveResult, error) {
		if !consumed.CompareAndSwap(false, true) {
			return MoveResult{}, moveApprovalRequired
		}
		result, err := s.moveMessage(ctx, req.AccountID, req.UID, req.MoveRequest)
		if err != nil {
			return MoveResult{}, moveResponseError(err)
		}
		return result, nil
	}, nil
}

func (s *Service) callMove(ctx context.Context, args json.RawMessage, approve func(context.Context, string, json.RawMessage) bool) proto.ToolResult {
	if approve == nil {
		return proto.TextResult(i18n.Localize(moveApprovalRequired, i18n.From(ctx)), true)
	}
	summary, move, err := s.PrepareMove(ctx, args)
	if err != nil {
		return proto.TextResult(moveToolError(ctx, err), true)
	}
	if !approve(ctx, summary, args) || ctx.Err() != nil {
		return proto.TextResult(i18n.Localize(moveApprovalRequired, i18n.From(ctx)), true)
	}
	result, err := move(ctx)
	if err != nil {
		return proto.TextResult(moveToolError(ctx, err), true)
	}
	raw, _ := json.Marshal(result)
	return proto.TextResult(string(raw), false)
}

func moveToolError(ctx context.Context, err error) string {
	failure := moveResponseError(err)
	return failure.Code() + ": " + i18n.Localize(failure, i18n.From(ctx))
}
