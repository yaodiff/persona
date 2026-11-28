package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The read tools' answers as JSON, for source subscriptions (connector.ReadJSON through the mail connector,
// docs/extensions/subscriptions.md "Existing connectors"): an object with one top-level list, whose items carry the
// names a recipe maps (id, title, sender, time, body). The models read the text Call writes. mail_read stays text:
// one message has no list to follow.

type accountJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Email    string `json:"email"`
}

type accountsJSON struct {
	Accounts []accountJSON `json:"accounts"`
}

type messageJSON struct {
	ID             string    `json:"id"` // the Message-ID, else account:mailbox:uid
	MessageID      string    `json:"message_id,omitempty"`
	AccountID      string    `json:"account_id"`
	Mailbox        string    `json:"mailbox"`
	UID            uint32    `json:"uid"` // for mail_read
	Title          string    `json:"title"`
	Sender         string    `json:"sender"` // the name, else the address
	SenderEmail    string    `json:"sender_email,omitempty"`
	To             []Address `json:"to,omitempty"`
	Time           string    `json:"time"`
	Body           string    `json:"body,omitempty"` // the start of the text
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged,omitempty"`
	HasAttachments bool      `json:"has_attachments,omitempty"`
}

type searchJSON struct {
	More bool `json:"more"`
	// NextBeforeUID continues a one-account search (before_uid); a search of every inbox has none.
	NextBeforeUID uint32 `json:"next_before_uid,omitempty"`
	// HeadersOnly: the server's search found nothing, so subjects, senders and recipients were matched instead.
	HeadersOnly bool          `json:"headers_only,omitempty"`
	UIDValidity uint32        `json:"uid_validity,omitempty"`
	Messages    []messageJSON `json:"messages"`
}

// CallJSON answers mail_accounts and mail_search as JSON; ok is false for the other tools.
func (s *Service) CallJSON(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	var data any
	switch name {
	case ToolAccounts:
		accounts, err := s.ListAccounts()
		if err != nil {
			return nil, true, err
		}
		if len(accounts) == 0 {
			return nil, true, errors.New(noAccounts)
		}
		out := accountsJSON{Accounts: make([]accountJSON, 0, len(accounts))}
		for _, a := range accounts {
			out.Accounts = append(out.Accounts, accountJSON{ID: a.ID, Name: a.Name, Provider: a.Provider, Email: a.Email})
		}
		data = out
	case ToolSearch:
		res, err := s.search(ctx, args)
		if err != nil {
			return nil, true, err
		}
		out := searchJSON{More: res.HasMore, NextBeforeUID: res.NextBeforeUID, HeadersOnly: res.HeadersOnly, UIDValidity: res.UIDValidity,
			Messages: make([]messageJSON, 0, len(res.Messages))}
		for _, m := range res.Messages {
			out.Messages = append(out.Messages, messageData(m))
		}
		data = out
	default:
		return nil, false, nil
	}
	out, err := json.Marshal(data)
	return out, true, err
}

func messageData(m MailSummary) messageJSON {
	out := messageJSON{ID: m.MessageID, MessageID: m.MessageID, AccountID: m.AccountID, Mailbox: m.Mailbox, UID: m.UID, Title: m.Subject,
		Sender: m.From.Name, SenderEmail: m.From.Email, To: m.To, Time: m.Date, Body: m.Snippet, Seen: m.Seen, Flagged: m.Flagged,
		HasAttachments: m.HasAttach}
	if out.ID == "" {
		out.ID = fmt.Sprintf("%s:%s:%d", m.AccountID, m.Mailbox, m.UID)
	}
	if out.Sender == "" {
		out.Sender = m.From.Email
	}
	return out
}
