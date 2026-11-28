// Package cliport is what the core (internal/extmgr, and internal/assistant for the knowledge base's Lark import,
// kb_lark.go) needs of the lark-cli adapter and its connector: a runner for the profile and identity checks the
// extension host makes and the commands the import runs (internal/assistant/kblark), and the connector the host
// registers (docs/architecture/core-and-modules.md "Provider seams", #13404). The values are the contract's
// (internal/cliwire).
//
// The composition root (internal/modules/all) provides it in the init of the lark module's file, under the module's
// build constraint. With the module left out nothing is provided: Available reports false, the host registers no
// lark-cli connector and every lark-cli check answers that it is not part of this build.
//
// Everything is provided while the program initializes, before main runs, and only read afterwards.
package cliport

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/moduleswitch"

	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Runner runs lark-cli for one profile.
type Runner interface {
	CheckVersion(ctx context.Context) error
	Version(ctx context.Context) (string, error)
	SearchLimit(ctx context.Context, kind, query string, limit int) (json.RawMessage, error)
	AuthStatus(ctx context.Context) (cliwire.AuthStatus, error)
	ConfigShow(ctx context.Context) (cliwire.ConfigInfo, error)
	InitProfile(ctx context.Context, name, appID, secret, brand string) error
	Profiles(ctx context.Context) ([]cliwire.Profile, error)
	ListChats(ctx context.Context, limit int) (cliwire.ChatList, error)
	ListMessages(ctx context.Context, chatID, thread, before string, limit int) (cliwire.MessagePage, error)
	// Run runs one read-only lark-cli command as the profile's user and returns its JSON output (the knowledge
	// base's Lark import: the minutes and document searches).
	Run(ctx context.Context, args ...string) ([]byte, error)
	// InspectDocument is drive +inspect of a document's link.
	InspectDocument(ctx context.Context, link string) (cliwire.Document, error)
}

// Connector is the lark-cli connector (cliwire.ConnectorID) with what the host sets up and reads on it.
type Connector interface {
	connectorwire.Connector
	connectorwire.ReadTools
	connectorwire.Options
	Profile() string
	SetStateStore(store cliwire.StateStore)
	SetInboxSource(prefer string, gatewayAvailable func(context.Context) bool)
	SetManagedInboxVisibility(chats []string, direct bool) error
	ObserveTopic(ctx context.Context, watch connectorwire.LarkTopicWatch) (connectorwire.LarkTopicObservation, error)
	ObserveReply(ctx context.Context, watch connectorwire.LarkReplyWatch) (connectorwire.LarkReplyObservation, error)
}

// Provider is what the lark module provides.
type Provider struct {
	NewRunner    func(cfg cliwire.Config) Runner
	NewConnector func(cfg cliwire.Config) Connector
}

var provided *Provider

// Provide provides the lark-cli adapter (init only).
func Provide(module string, p Provider) {
	if provided != nil {
		panic("cliport: provided twice")
	}
	provided = &p
	switched.Provide(module, struct{}{})
}

// switched keeps the module's id for the run-time switch (moduleswitch): Available asks it.
var switched = moduleswitch.NewPort[struct{}]("cliport")

// InBuild reports whether this build has the lark-cli adapter (the lark module), whatever the switch.
func InBuild() bool { return provided != nil }

// Available reports whether this build has the lark-cli adapter and s has the lark module on: the one question the extension
// manager asks before it makes anything of the port (the constructors below are called only after it). A switched-off
// module is absent exactly as one built out.
func Available(s moduleswitch.Set) bool { return provided != nil && switched.On(s) }

// ErrUnavailable: this build has no lark-cli adapter (the lark module is left out).
var ErrUnavailable = errors.New("lark-cli is not part of this build")

// NewRunner is a runner of cfg; nil when the build has none.
func NewRunner(cfg cliwire.Config) Runner {
	if provided == nil {
		return nil
	}
	return provided.NewRunner(cfg)
}

// NewConnector is the connector of cfg; nil when the build has none.
func NewConnector(cfg cliwire.Config) Connector {
	if provided == nil {
		return nil
	}
	return provided.NewConnector(cfg)
}
