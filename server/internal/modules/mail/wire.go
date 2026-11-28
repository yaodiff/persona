package mail

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"
)

// The values the core and this module pass are mailwire's (the contract); the module names them as its own.
type (
	Address           = mailwire.Address
	MailAccount       = mailwire.MailAccount
	OAuthCreds        = mailwire.OAuthCreds
	AddAccountRequest = mailwire.AddAccountRequest
	Mailbox           = mailwire.Mailbox
	MailSummary       = mailwire.MailSummary
	Attachment        = mailwire.Attachment
	MailMessage       = mailwire.MailMessage
	ListOptions       = mailwire.ListOptions
	ListResult        = mailwire.ListResult
	SetFlagsRequest   = mailwire.SetFlagsRequest
	MoveRequest       = mailwire.MoveRequest
	MoveResult        = mailwire.MoveResult
	SendRequest       = mailwire.SendRequest
	MessageIDs        = mailwire.MessageIDs
	AccountRow        = mailwire.AccountRow
	Records           = mailwire.Records
)

const (
	AuthPassword = mailwire.AuthPassword

	ProviderGmail  = mailwire.ProviderGmail
	ProviderIMAP   = mailwire.ProviderIMAP
	ProviderFeishu = mailwire.ProviderFeishu

	RoleInbox   = mailwire.RoleInbox
	RoleSent    = mailwire.RoleSent
	RoleDrafts  = mailwire.RoleDrafts
	RoleTrash   = mailwire.RoleTrash
	RoleArchive = mailwire.RoleArchive
	RoleJunk    = mailwire.RoleJunk

	ToolAccounts       = mailwire.ToolAccounts
	ToolSearch         = mailwire.ToolSearch
	ToolRead           = mailwire.ToolRead
	ToolSend           = mailwire.ToolSend
	ToolMove           = mailwire.ToolMove
	ToolSaveAttachment = mailwire.ToolSaveAttachment

	MaxSavedAttachment = mailwire.MaxSavedAttachment
)

var (
	ErrAccountExists = mailwire.ErrAccountExists
	ErrNoAccount     = mailwire.ErrNoAccount
	ErrMaybeSent     = mailwire.ErrMaybeSent
	ErrTimedOut      = mailwire.ErrTimedOut
)

// Open is the module's mailwire.Factory: the service with hub.json's Google and Microsoft settings.
func Open(cfg mailwire.Config) (mailwire.Service, error) {
	s, err := NewWithKey(cfg.DataDir, cfg.SecretKey, cfg.Store)
	if err != nil {
		return nil, err
	}
	s.SetGoogleClientID(cfg.GoogleClientID)
	s.SetMicrosoft(cfg.MicrosoftClientID, cfg.MicrosoftRedirect)
	if err := s.SetGoogleProxy(cfg.GoogleProxy); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

var _ mailwire.Service = (*Service)(nil)

// PasswordGuide is the package's PasswordGuide, for the core (mailwire.Service).
func (*Service) PasswordGuide(lang, email string) string {
	return PasswordGuide(i18n.Lang(lang), email)
}

// SendSummary is the package's SendSummary, for the core (mailwire.Service).
func (*Service) SendSummary(args json.RawMessage) (string, error) { return SendSummary(args) }

// ReadableText is the package's ReadableText, for the core (mailwire.Service).
func (*Service) ReadableText(html string) string { return ReadableText(html) }

// PlainText is the package's PlainText, for the core (mailwire.Service).
func (*Service) PlainText(html string) string { return PlainText(html) }
