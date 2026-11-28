// Package botport is what the core (internal/extmgr) needs of the chat bot's client: the open platform's REST calls,
// the long connection that delivers its events, and the device-code registration that creates an app
// (docs/architecture/core-and-modules.md "Provider seams", #13404).
//
// The core declares it here in its own terms, with the pure types of internal/botcard; the composition root
// (internal/modules/all) provides it in the init of the lark module's file, under the module's build constraint. With
// the module left out of the build nothing is provided: Available reports false, the bot cannot be bound or started,
// and everything else of the hub runs as before.
//
// Everything is provided while the program initializes, before main runs, and only read afterwards.
package botport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/moduleswitch"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Client makes the app's REST calls with a tenant access token it keeps fresh.
type Client interface {
	AppID() string
	Domain() string
	BotInfo(ctx context.Context) (botcard.BotInfo, error)
	AutoConfigure(ctx context.Context) botcard.ConfigureReport

	Send(ctx context.Context, idType, id, msgType, content, uuid string) (string, error)
	SendUser(ctx context.Context, openID, msgType, content, uuid string) (messageID, chatID string, err error)
	Reply(ctx context.Context, messageID, msgType, content, uuid string, inThread bool) (string, error)
	AnswerContent(ctx context.Context, text string) (msgType, content string, err error)
	React(ctx context.Context, messageID, emoji string) (string, error)
	Unreact(ctx context.Context, messageID, reactionID string) error
	Pin(ctx context.Context, messageID string) error
	Unpin(ctx context.Context, messageID string) error
	Pins(ctx context.Context, chatID string) ([]botcard.Pin, error)
	Recall(ctx context.Context, messageID string) error
	UpdateCard(ctx context.Context, messageID, card string) error
	Message(ctx context.Context, messageID string) ([]botcard.Item, error)
	UserName(ctx context.Context, openID string) (string, error)
	Resource(ctx context.Context, messageID, key, kind string, limit int64) ([]byte, string, error)

	CardCreate(ctx context.Context, card string) (string, error)
	CardUpdate(ctx context.Context, id, card string, seq int64) error
	CardStream(ctx context.Context, id, element, text string, seq int64) error
	CardSettings(ctx context.Context, id string, streaming bool, seq int64) error

	CotCreate(ctx context.Context, chatID, replyTo string, inThread bool) (cotID, messageID string, err error)
	CotAppend(ctx context.Context, cotID, messageID string, events []botcard.CotEvent) error
	CotComplete(ctx context.Context, cotID, messageID, reason string) error
}

// Conn is the long connection that delivers the app's events to a handler.
type Conn interface {
	State() botcard.ConnState
	Run(ctx context.Context)
}

// Registrar creates an app in one authorization through a cloud's accounts site.
type Registrar interface {
	Begin(ctx context.Context, region, name string) (botcard.RegisterChallenge, error)
	Poll(ctx context.Context, region, deviceCode string) (botcard.RegisterTicket, error)
	// BeginWith is Begin that also asks for user scopes (the user sync, larksyncwire.RegisterOptions), and adds them to
	// an existing app instead of creating one when opts.AppID is set.
	BeginWith(ctx context.Context, region, name string, opts larksyncwire.RegisterOptions) (botcard.RegisterChallenge, error)
}

// SyncRegistrar is the app-registration subset the user sync needs from the manager's registrar (an unnamed interface
// alias so internal/modules/lark/larksync can declare the same Host without importing botport).
type SyncRegistrar = interface {
	BeginWith(ctx context.Context, region, name string, opts larksyncwire.RegisterOptions) (botcard.RegisterChallenge, error)
	Poll(ctx context.Context, region, deviceCode string) (botcard.RegisterTicket, error)
}

// SyncHost is what the Lark user sync service (internal/modules/lark/larksync) asks of the extension manager.
type SyncHost interface {
	HasSettings() bool
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
	SyncStore() larksyncwire.Store
	SealSecret(plain string) (string, error)
	OpenSecret(sealed string) (string, error)
	PrimaryApp(ctx context.Context) (appID, secret, domain string)
	Registrar() SyncRegistrar
	RegisteredDomain(region string) string
	CanWrite(r *http.Request) bool
	RunContext() context.Context
	Logf(format string, args ...any)
	BotLang(ctx context.Context) i18n.Lang
	UserLang() i18n.Lang
	Audit(ctx context.Context, action string, detail map[string]any)
	KBKick(connectorID string, scope connectorwire.Scope)
	EmitLarkMessages(binding string, msgs []larksyncwire.Message)
	PushLocalized(build func(i18n.Lang) map[string]any, collapseID string)
	IsMember() bool
}

// SyncService is the minimal interface the extension manager calls on the Lark user sync module (#13404).
type SyncService interface {
	Routes(api *http.ServeMux)
	Connector() connectorwire.Connector
	Loop(ctx context.Context)
	PrimaryTier(ctx context.Context) string
}

// Provider is what the lark module provides.
type Provider struct {
	// New is a client of the app on domain ("", "feishu", "lark" or a URL); httpClient replaces its HTTP client when
	// not nil (tests).
	New func(appID, appSecret, domain string, httpClient *http.Client) Client
	// NewConn is the long connection of a client New made, delivering events to handler.
	NewConn func(api Client, handler botcard.Handler, logf func(string, ...any)) Conn
	// NewRegistrar is a registrar of the clouds' accounts sites; host, when not "", replaces every region's (tests).
	NewRegistrar func(host string) Registrar
	// Sync is the user OAuth and the daily reader of the user sync (docs/extensions/bots.md "Reading as you: daily
	// sync").
	Sync larksyncwire.Provider
	// NewSyncService constructs the manager's user sync service (routes, connector, scheduler and state).
	NewSyncService func(host SyncHost) SyncService
}

var provided *Provider

// Provide provides the bot's client (init only).
func Provide(module string, p Provider) {
	if provided != nil {
		panic("botport: provided twice")
	}
	provided = &p
	switched.Provide(module, struct{}{})
}

// switched keeps the module's id for the run-time switch (moduleswitch): Available asks it.
var switched = moduleswitch.NewPort[struct{}]("botport")

// InBuild reports whether this build has the bot (the lark module), whatever the switch.
func InBuild() bool { return provided != nil }

// Available reports whether this build has the bot and s has the lark module on: the one question the extension
// manager asks before it makes anything of the port (the constructors below are called only after it). A switched-off
// module is absent exactly as one built out.
func Available(s moduleswitch.Set) bool { return provided != nil && switched.On(s) }

// ErrUnavailable: this build has no bot client (the lark module is left out).
var ErrUnavailable = errors.New("the Feishu / Lark bot is not part of this build")

// New is a client of the app (Provider.New with the default HTTP client); nil when the build has none.
func New(appID, appSecret, domain string) Client {
	if provided == nil {
		return nil
	}
	return provided.New(appID, appSecret, domain, nil)
}

// NewWithHTTP is New with its HTTP client replaced (tests); nil when the build has none.
func NewWithHTTP(appID, appSecret, domain string, httpClient *http.Client) Client {
	if provided == nil {
		return nil
	}
	return provided.New(appID, appSecret, domain, httpClient)
}

// NewConn is the long connection of api; nil when the build has none.
func NewConn(api Client, handler botcard.Handler, logf func(string, ...any)) Conn {
	if provided == nil {
		return nil
	}
	return provided.NewConn(api, handler, logf)
}

// NewRegistrar is a registrar; host, when not "", replaces every region's accounts site (tests). Nil when the build
// has none.
func NewRegistrar(host string) Registrar {
	if provided == nil {
		return nil
	}
	return provided.NewRegistrar(host)
}

// NewUserAuth is the user OAuth client (nil when the module is not provided); accounts and open, when not "", replace
// the accounts site and the open platform (tests).
func NewUserAuth(accounts, open string, httpClient *http.Client) larksyncwire.UserAuth {
	if provided == nil || provided.Sync.NewUserAuth == nil {
		return nil
	}
	return provided.Sync.NewUserAuth(accounts, open, httpClient)
}

// NewSyncer is one binding's reader of the user sync (nil when the module is not provided); now is the clock (nil:
// time.Now).
func NewSyncer(httpClient *http.Client, now func() time.Time) larksyncwire.Syncer {
	if provided == nil || provided.Sync.NewSyncer == nil {
		return nil
	}
	return provided.Sync.NewSyncer(httpClient, now)
}

// NewSyncService constructs the Lark user sync service for host (nil when the module is not provided).
func NewSyncService(host SyncHost) SyncService {
	if provided == nil || provided.NewSyncService == nil {
		return nil
	}
	return provided.NewSyncService(host)
}
