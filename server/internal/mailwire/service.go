package mailwire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/proto"
)

// Tool names exposed to Claude through MCP. The hub intercepts anything with a
// "mail_" prefix and routes it to Call.
const (
	ToolAccounts = "mail_accounts"
	ToolSearch   = "mail_search"
	ToolRead     = "mail_read"
	ToolSend     = "mail_send"
	ToolMove     = "mail_move"
	// ToolSaveAttachment writes a file on the machine the agent runs on, so that machine's node answers it
	// (node/mailattachment.go) with the bytes FetchAttachment hands over its mail.attachment request; it is listed
	// here with the others so that it comes and goes with mail.
	ToolSaveAttachment = "mail_save_attachment"
)

// MaxSavedAttachment bounds an attachment FetchAttachment hands over for mail_save_attachment: it crosses the node's
// connection in one message (base64 in JSON, about 4/3 of it, under the connection's 64 MiB line), and 25 MB is the
// most most providers let a mail carry.
const MaxSavedAttachment = 25 << 20

// ErrAccountExists: the address already has an account (HTTP 409). Changing its servers or password means deleting
// it and adding it again, and the message says so.
var ErrAccountExists = errors.New("已经添加过了")

// ErrNoAccount: there is no account with that ID.
var ErrNoAccount = errors.New("no such mail account")

// ErrMaybeSent is in the error of a send that failed after the SMTP server got the whole message but before it answered
// it: the server may have taken it, so a caller that sends at most once treats the send as in doubt, not as not sent
// (#9166).
var ErrMaybeSent = errors.New("the server got the message but did not answer it, so it may have gone")

// ErrTimedOut is an operation on an account that ran past networkTimeout (the inbox counts it as a timeout, not a
// failure).
var ErrTimedOut = errors.New("imap: operation timed out")

// MicrosoftClientSetting is the settings row holding the Outlook client ID the user entered, sealed in its
// MicrosoftClientSealedField; the key rotation re-seals it (db.SealedSettings names it through these).
const (
	MicrosoftClientSetting     = "mail.microsoft_client"
	MicrosoftClientSealedField = "enc"
)

// AccountRow is one account as the mail module persists it: non-secret JSON plus ciphertext (mail_accounts).
type AccountRow struct {
	ID          string
	Email       string
	Data        json.RawMessage
	PasswordEnc string
	OAuthEnc    string
}

// Records is where accounts are persisted: Postgres (internal/db) in production, memory in tests.
// The mail module encrypts secrets before they reach it, with a key that never leaves dataDir.
type Records interface {
	MailAccounts(ctx context.Context) ([]AccountRow, error)
	ReplaceMailAccounts(ctx context.Context, rows []AccountRow) error
}

// Store is what the manager hands the mail module: the accounts' rows and the settings it keeps (the Outlook client
// ID).
type Store interface {
	Records
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

// Config opens the mail module (Factory).
type Config struct {
	// DataDir is the manager's directory; SecretKey the key file the passwords are sealed with.
	DataDir, SecretKey string
	Store              Store
	// GoogleClientID and GoogleProxy are hub.json's google section (docs/extensions/mail.md).
	GoogleClientID, GoogleProxy string
	// MicrosoftClientID and MicrosoftRedirect are hub.json's microsoft section.
	MicrosoftClientID, MicrosoftRedirect string
}

// Factory opens the mail module. The composition root provides it (internal/extmgr/mailport); a build without the
// module (persona_without_mail, persona_core_only) has none, and the manager runs without mail.
type Factory func(Config) (Service, error)

// PreparedMove executes the single move described by its approval summary.
type PreparedMove func(context.Context) (MoveResult, error)

// Service is the mail module as the extension manager uses it.
type Service interface {
	Handler() http.Handler
	Tools() []proto.Tool
	Call(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) proto.ToolResult
	// CallRef is Call that also says the Message-ID of the mail mail_send handed to the transport ("" for anything else).
	CallRef(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) (proto.ToolResult, string)
	// CallJSON is a read tool's answer as JSON for subscriptions (connector.ReadJSON); ok is false for a tool without one.
	CallJSON(ctx context.Context, name string, args json.RawMessage) (out json.RawMessage, ok bool, err error)
	ListAccounts() ([]MailAccount, error)
	IntakeMailboxes(ctx context.Context, accountID string) ([]string, error)
	ListMessages(ctx context.Context, opts ListOptions) (*ListResult, error)
	GetMessage(ctx context.Context, accountID string, uid uint32, mailbox string) (*MailMessage, error)
	// SeenFlags is, for each of uids still in mailbox, whether it is seen; one it no longer has is not in the map.
	SeenFlags(ctx context.Context, accountID, mailbox string, uids []uint32) (map[uint32]bool, error)
	Send(ctx context.Context, req SendRequest) error
	// FetchAttachment is one attachment's bytes for mail_save_attachment (the node's mail.attachment), at most max.
	FetchAttachment(ctx context.Context, accountID string, uid uint32, mailbox, id string, max int64) (Attachment, []byte, error)
	AddAccount(req AddAccountRequest) (*MailAccount, error)
	DeleteAccount(id string) error
	// ExportAccount and ImportAccount move an account to another manager (extmgr's sources.go); the JSON holds its
	// secrets.
	ExportAccount(id string) ([]byte, error)
	ImportAccount(data []byte) (*MailAccount, error)

	// PasswordGuide is, in lang, where the provider of email makes the authorization code its mail clients sign in
	// with; "" when the domain has no such steps.
	PasswordGuide(lang, email string) string
	// SendSummary is mail_send's arguments as the approval card says them, or why they are not mail_send's.
	SendSummary(args json.RawMessage) (string, error)
	// PrepareMove reads the message and resolves the destination for the approval card. Run the returned move only
	// after the owner approves that card.
	PrepareMove(ctx context.Context, args json.RawMessage) (summary string, move PreparedMove, err error)
	// ReadableText is a message's HTML body as readable text (paragraphs kept); PlainText the same on one line.
	ReadableText(html string) string
	PlainText(html string) string
}
