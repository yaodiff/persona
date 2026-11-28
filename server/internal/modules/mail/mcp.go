package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Tools returns the MCP tool definitions for mail (descriptions and schemas in prompts/mcp/mail).
func (s *Service) Tools() []proto.Tool {
	var out []proto.Tool
	for _, n := range []string{ToolAccounts, ToolSearch, ToolRead, ToolSaveAttachment, ToolSend, ToolMove} {
		t := prompts.Tool(n)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Call dispatches one MCP tool invocation. approve is called before sending a
// mail and must return false if the user declined.
func (s *Service) Call(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) proto.ToolResult {
	switch name {
	case ToolAccounts:
		accounts, err := s.ListAccounts()
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		if len(accounts) == 0 {
			return proto.TextResult(noAccounts, true)
		}
		var b strings.Builder
		for _, a := range accounts {
			fmt.Fprintf(&b, "- %s (%s), provider %s, address %s\n", a.Name, a.ID, a.Provider, a.Email)
		}
		return proto.TextResult(strings.TrimSpace(b.String()), false)

	case ToolSearch:
		res, err := s.search(ctx, args)
		if err != nil {
			return proto.TextResult(toolError(ctx, err), true)
		}
		var b strings.Builder
		switch {
		case len(res.Messages) > 0:
			for _, m := range res.Messages {
				fmt.Fprintf(&b, "- uid=%d account=%s mailbox=%s date=%s from=%s <%s> subject=%q\n  %s\n",
					m.UID, m.AccountID, m.Mailbox, m.Date, m.From.Name, m.From.Email, m.Subject, m.Snippet)
			}
			switch {
			case res.HasMore && res.NextBeforeUID > 0:
				fmt.Fprintf(&b, "(more results available; pass before_uid=%d to continue)\n", res.NextBeforeUID)
			case res.HasMore:
				// Every account's inbox at once: a UID only means something in its own account, so there is no
				// before_uid that continues them all.
				b.WriteString("(more results available; search one account with account_id to page further back)\n")
			}
		case res.HasMore && res.NextBeforeUID > 0:
			fmt.Fprintf(&b, "No messages matched among the ones looked at; the headers of older messages were not "+
				"matched yet (pass before_uid=%d to continue).\n", res.NextBeforeUID)
		case res.HasMore:
			b.WriteString("No messages matched among the ones looked at; the headers of older messages were not " +
				"matched yet (search one account with account_id to continue).\n")
		default:
			b.WriteString("No messages matched.\n")
		}
		if res.UIDValidity != 0 {
			fmt.Fprintf(&b, "uid_validity=%d\n", res.UIDValidity)
		}
		if res.HeadersOnly {
			b.WriteString("(The server's search found nothing for this query (some servers, such as NetEase 163, do not search " +
				"at all), so subjects, senders and recipients were matched here instead; that does not look into message bodies.)\n")
		}
		return proto.TextResult(b.String(), false)

	case ToolRead:
		var req struct {
			AccountID string `json:"account_id"`
			UID       uint32 `json:"uid"`
			Mailbox   string `json:"mailbox"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return proto.TextResult("invalid arguments: "+err.Error(), true)
		}
		msg, err := s.GetMessage(ctx, req.AccountID, req.UID, req.Mailbox)
		if err != nil {
			return proto.TextResult(toolError(ctx, err), true)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "From: %s <%s>\n", msg.From.Name, msg.From.Email)
		fmt.Fprintf(&b, "To: %s\n", formatAddresses(msg.To))
		if len(msg.Cc) > 0 {
			fmt.Fprintf(&b, "Cc: %s\n", formatAddresses(msg.Cc))
		}
		fmt.Fprintf(&b, "Date: %s\nSubject: %s\nMessage-ID: <%s>\n", msg.Date, msg.Subject, msg.MessageID)
		if msg.InReplyTo != "" {
			fmt.Fprintf(&b, "In-Reply-To: <%s>\n", msg.InReplyTo)
		}
		if len(msg.References) > 0 {
			fmt.Fprintf(&b, "References: %s\n", strings.Join(wrapIDs(msg.References), " "))
		}
		if len(msg.Attachments) > 0 {
			b.WriteString("Attachments:\n")
			for _, att := range msg.Attachments {
				fmt.Fprintf(&b, "- %s (%s, %d bytes) id=%s\n", att.Filename, att.Mime, att.Size, att.ID)
			}
		}
		b.WriteString("\n")
		body := msg.Text
		if strings.TrimSpace(body) == "" {
			body = stripHTML(msg.HTML)
		}
		b.WriteString(strings.TrimSpace(body))
		if msg.HTML != "" && msg.Text != "" {
			b.WriteString("\n\n[An HTML version of this message also exists.]")
		}
		return proto.TextResult(b.String(), false)

	case ToolSaveAttachment:
		// Only a caller with no node of its own gets here (the node on the agent's machine answers it otherwise).
		return proto.TextResult(ToolSaveAttachment+" saves files on the computer the agent runs on and is not available here.", true)

	case ToolMove:
		return s.callMove(ctx, args, approve)
	case ToolSend:
		res, _ := s.callSend(ctx, args, approve)
		return res
	}
	return proto.TextResult("unknown tool "+name, true)
}

// toolError is a failed search's or read's answer: the errors that say what happened in words (an incomplete
// search of every account, an account that could not be reached) in the caller's language, any other as it is.
func toolError(ctx context.Context, err error) string {
	var incomplete *IncompleteSearchError
	if errors.As(err, &incomplete) {
		return i18n.Localize(incomplete, i18n.From(ctx))
	}
	var unreachable *UnreachableError
	if errors.As(err, &unreachable) {
		return i18n.Localize(unreachable, i18n.From(ctx))
	}
	return err.Error()
}

// noAccounts is mail_accounts' answer without an account.
const noAccounts = "No mail accounts are configured. Ask the user to add one in the Persona app."

// search runs mail_search: its arguments, at most 20 messages unless they say otherwise.
func (s *Service) search(ctx context.Context, args json.RawMessage) (*ListResult, error) {
	var req struct {
		AccountID string `json:"account_id"`
		Mailbox   string `json:"mailbox"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
		BeforeUID uint32 `json:"before_uid"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	return s.ListMessages(ctx, ListOptions{
		AccountID: req.AccountID, Mailbox: req.Mailbox, Query: req.Query, Limit: req.Limit, BeforeUID: req.BeforeUID,
	})
}

// CallRef is Call that also says the Message-ID of the mail mail_send handed to the transport (SendMessage, without the
// angle brackets): empty for any other tool, and when nothing was sent.
func (s *Service) CallRef(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) (proto.ToolResult, string) {
	if name == ToolSend {
		return s.callSend(ctx, args, approve)
	}
	return s.Call(ctx, name, args, approve), ""
}

// callSend is mail_send.
func (s *Service) callSend(ctx context.Context, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) (proto.ToolResult, string) {
	var req SendRequest
	summary, err := SendSummary(args)
	if err == nil {
		err = json.Unmarshal(args, &req)
	}
	if err != nil {
		return proto.TextResult(err.Error(), true), ""
	}
	if approve != nil && !approve(ctx, summary, args) {
		return proto.TextResult("The user declined to send this email.", true), ""
	}
	messageID, err := s.SendMessage(ctx, req)
	if err != nil {
		return proto.TextResult(err.Error(), true), ""
	}
	return proto.TextResult("Mail sent to "+formatAddresses(req.To)+".", false), messageID
}

// SendSummary is mail_send's arguments as the approval card says them, or why they are not mail_send's.
func SendSummary(args json.RawMessage) (string, error) {
	var req SendRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", errors.New("invalid arguments: " + err.Error())
	}
	return fmt.Sprintf("Send mail to %s: %s", formatAddresses(req.To), req.Subject), nil
}

func formatAddresses(addrs []Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, a.Email))
		} else {
			parts = append(parts, a.Email)
		}
	}
	return strings.Join(parts, ", ")
}

func wrapIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "<" + id + ">"
	}
	return out
}
