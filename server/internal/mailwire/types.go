// Package mailwire is the contract between the server core and the mail module (server/internal/modules/mail): the
// values both sides pass, and the Service the extension manager reaches mail through (docs/extensions/mail.md,
// docs/architecture/core-and-modules.md). Types, constants and sentinel errors only; the module aliases them.
//
// The JSON shapes mirror docs/protocol/protocol.md ("Mail API"); keep them in sync.
package mailwire

import (
	"encoding/json"
	"errors"
	"strings"
)

// Address is a mail address with an optional display name.
type Address struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Authentication methods.
const (
	AuthPassword = "password"
	// AuthOAuth2 (oauth.go) is Gmail's "Sign in with Google"; it cannot be added with POST /accounts.
)

// Providers.
const (
	ProviderGmail  = "gmail"
	ProviderIMAP   = "imap"
	ProviderFeishu = "feishu" // Feishu / Lark mail on a company domain: larkoffice.com or feishu.cn servers, whichever signs in first
)

// Mailbox roles. The values match the role field in docs/protocol/protocol.md.
const (
	RoleInbox   = "inbox"
	RoleSent    = "sent"
	RoleDrafts  = "drafts"
	RoleTrash   = "trash"
	RoleArchive = "archive"
	RoleJunk    = "junk"
)

// MailAccount is the public view of an account (no secrets).
type MailAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Provider string `json:"provider"` // gmail | imap
	Auth     string `json:"auth"`     // password | oauth2
	Username string `json:"username"`

	IMAPHost string `json:"imap_host"`
	IMAPPort int    `json:"imap_port"`
	// IMAPSecurity is an optional override: tls | starttls | none.
	// When empty it is derived from the port (993 = tls, 143 = starttls,
	// anything else = none, which is only meant for local test servers).
	IMAPSecurity string `json:"imap_security,omitempty"`

	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPSecurity string `json:"smtp_security"` // tls | starttls (| none for tests)

	Status string `json:"status"` // ok | error
	Error  string `json:"error"`

	// Secrets. Never marshalled into API responses.
	Password string      `json:"-"`
	OAuth    *OAuthCreds `json:"-"`
}

// OAuthCreds is the long-lived credential of an OAuth account, stored encrypted.
type OAuthCreds struct {
	// Provider is whose sign-in it is: "" Google, "microsoft" (the module's microsoft.go).
	Provider     string `json:"provider,omitempty"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

// AddAccountRequest is the POST /api/v1/mail/accounts body. Fields that the
// request leaves empty are filled from the preset for the email domain.
type AddAccountRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Provider string `json:"provider,omitempty"`
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`

	Password string `json:"password,omitempty"`

	IMAPHost     string `json:"imap_host,omitempty"`
	IMAPPort     int    `json:"imap_port,omitempty"`
	IMAPSecurity string `json:"imap_security,omitempty"`
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPSecurity string `json:"smtp_security,omitempty"`
}

// Mailbox is a mailbox with its role and counters.
type Mailbox struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Unseen int    `json:"unseen"`
	Total  int    `json:"total"`
}

// MailSummary is one row of a message list.
type MailSummary struct {
	AccountID string    `json:"account_id"`
	Mailbox   string    `json:"mailbox"`
	UID       uint32    `json:"uid"`
	MessageID string    `json:"message_id"`
	Subject   string    `json:"subject"`
	From      Address   `json:"from"`
	To        []Address `json:"to"`
	Date      string    `json:"date"` // RFC3339, UTC
	Snippet   string    `json:"snippet"`
	Seen      bool      `json:"seen"`
	Answered  bool      `json:"answered"`
	Flagged   bool      `json:"flagged"`
	HasAttach bool      `json:"has_attachments"`
}

// Attachment is attachment metadata; the body is not downloaded.
type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
}

// MailMessage is a full message.
type MailMessage struct {
	MailSummary
	Cc          []Address    `json:"cc"`
	Text        string       `json:"text"`
	HTML        string       `json:"html"`
	Attachments []Attachment `json:"attachments"`
	InReplyTo   string       `json:"in_reply_to"`
	References  []string     `json:"references"`
}

// ListOptions selects messages. AccountID == "" merges every account's inbox.
type ListOptions struct {
	AccountID string
	Mailbox   string
	Limit     int
	BeforeUID uint32
	Query     string
}

// ListResult is the message list response.
type ListResult struct {
	Messages []MailSummary `json:"messages"`
	HasMore  bool          `json:"has_more"`
	// NextBeforeUID is the before_uid of the next page of a one-account listing, set whenever HasMore is. A local
	// search (HeadersOnly) can stop before it has looked at the whole mailbox, so the next page may start below the
	// last message it looked at rather than below the last one it returned; Messages can then be empty.
	NextBeforeUID uint32 `json:"next_before_uid,omitempty"`
	// UIDValidity is the mailbox's UIDVALIDITY of a one-account listing (0 when merged): when it changes the server
	// numbered the mailbox anew, so UIDs kept from before (a cursor) no longer mean the same messages.
	UIDValidity uint32 `json:"uid_validity,omitempty"`
	// HeadersOnly means the server's search found nothing for the query (some, like NetEase 163, do not search at
	// all), so the results come from matching subjects, senders and recipients here, which does not look into bodies.
	HeadersOnly bool `json:"headers_only,omitempty"`
}

// SetFlagsRequest updates the seen/flagged flags of one message.
type SetFlagsRequest struct {
	Mailbox string `json:"mailbox"`
	Seen    *bool  `json:"seen,omitempty"`
	Flagged *bool  `json:"flagged,omitempty"`
}

// MoveRequest identifies the source mailbox generation and one destination in the same account.
type MoveRequest struct {
	Mailbox     string `json:"mailbox"`
	UIDValidity uint32 `json:"uid_validity"`
	To          string `json:"to,omitempty"`
	ToMailbox   string `json:"to_mailbox,omitempty"`
}

// MoveResult gives the destination and, when the server supplied COPYUID, the identity needed to move back.
type MoveResult struct {
	Mailbox     string `json:"mailbox"`
	UID         uint32 `json:"uid,omitempty"`
	UIDValidity uint32 `json:"uid_validity,omitempty"`
}

// SendRequest is a message to send.
type SendRequest struct {
	AccountID  string     `json:"account_id"`
	To         []Address  `json:"to"`
	Cc         []Address  `json:"cc,omitempty"`
	Bcc        []Address  `json:"bcc,omitempty"`
	Subject    string     `json:"subject"`
	Text       string     `json:"text"`
	HTML       string     `json:"html,omitempty"`
	InReplyTo  string     `json:"in_reply_to,omitempty"`
	References MessageIDs `json:"references,omitempty"`
}

// MessageIDs is a space-separated list of Message-IDs, such as a References header. Clients send it either as
// one string (the web app, the assistant) or as an array of ids (the iOS app); an array is joined with spaces.
type MessageIDs string

func (m *MessageIDs) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*m = MessageIDs(s)
		return nil
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return errors.New("references must be a string or an array of strings")
	}
	*m = MessageIDs(strings.Join(ids, " "))
	return nil
}
