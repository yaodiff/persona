// Package connector is how a source of the user's information (GitHub, iMessage, later Feishu and mail) plugs into
// the hub: one connector per source, and each pipe the hub has — the assistant's tools, writes behind a gate, the
// settings page's status, realtime kicks — picks up what the connector implements, so adding a source does not
// touch those pipes (docs/extensions/connectors.md).
//
// A connector implements Connector, whose Manifest declares its capabilities; the hub finds each declared one's
// interface with As. Runner, Events and Machines are how it runs, found by type assertion.
package connector

import (
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/netproxy"
)

// The connector SPI is defined in the contract package connectorwire (docs/server/architecture.md "Contract
// packages"), so a connector built as a module (internal/modules/…) needs only it; the old names stay.
type (
	Media               = connectorwire.Media
	Connector           = connectorwire.Connector
	Status              = connectorwire.Status
	Accounts            = connectorwire.Accounts
	ReadTools           = connectorwire.ReadTools
	Action              = connectorwire.Action
	Actions             = connectorwire.Actions
	DoResult            = connectorwire.DoResult
	RefActions          = connectorwire.RefActions
	AutoConfirmGrant    = connectorwire.AutoConfirmGrant
	AutoConfirmApproval = connectorwire.AutoConfirmApproval
	AutoConfirmPolicy   = connectorwire.AutoConfirmPolicy
	ExtensionIdentity   = connectorwire.ExtensionIdentity
	AutoConfirmAudit    = connectorwire.AutoConfirmAudit
	ApprovalActions     = connectorwire.ApprovalActions
	IdentifiedConnector = connectorwire.IdentifiedConnector
	Options             = connectorwire.Options
	Events              = connectorwire.Events
	Scope               = connectorwire.Scope
	Runner              = connectorwire.Runner
	Machines            = connectorwire.Machines
	Chats               = connectorwire.Chats
	Items               = connectorwire.Items
	Threads             = connectorwire.Threads
	ThreadPage          = connectorwire.ThreadPage
	ThreadChat          = connectorwire.ThreadChat
	ThreadMessage       = connectorwire.ThreadMessage
	ThreadPerson        = connectorwire.ThreadPerson
	ThreadMedia         = connectorwire.ThreadMedia
	ThreadQuote         = connectorwire.ThreadQuote
	ThreadLabel         = connectorwire.ThreadLabel
	ThreadBranch        = connectorwire.ThreadBranch
	ThreadCode          = connectorwire.ThreadCode
	ThreadCheck         = connectorwire.ThreadCheck
	Field               = connectorwire.Field
	QRCode              = connectorwire.QRCode
	Prompt              = connectorwire.Prompt
	Login               = connectorwire.Login
	Secrets             = connectorwire.Secrets
	SentMessage         = connectorwire.SentMessage
	Recaller            = connectorwire.Recaller
	Archive             = connectorwire.Archive
	ArchiveMedia        = connectorwire.ArchiveMedia
	History             = connectorwire.History
	HistoryAnchors      = connectorwire.HistoryAnchorsReader
	Snapshot            = connectorwire.Snapshot
	Grouper             = connectorwire.Grouper
	EventWriter         = connectorwire.EventWriter
	Inbox               = connectorwire.Inbox
	InboxSettler        = connectorwire.InboxSettler
	Catalog             = connectorwire.Catalog
	CatalogSource       = connectorwire.CatalogSource
	CatalogTool         = connectorwire.CatalogTool
	CatalogAnnotations  = connectorwire.CatalogAnnotations
	SubscriptionRecipe  = connectorwire.SubscriptionRecipe
	SubscriptionFields  = connectorwire.SubscriptionFields
	CatalogResult       = connectorwire.CatalogResult
	ReadJSON            = connectorwire.ReadJSON
	CatalogQuery        = connectorwire.CatalogQuery
	Capability          = connectorwire.Capability
	Manifest            = connectorwire.Manifest
)

const (
	StateOK                              = connectorwire.StateOK
	StateNotConfigured                   = connectorwire.StateNotConfigured
	StateNotSignedIn                     = connectorwire.StateNotSignedIn
	StateNoPermission                    = connectorwire.StateNoPermission
	StateRateLimited                     = connectorwire.StateRateLimited
	StateError                           = connectorwire.StateError
	StateDisabled                        = connectorwire.StateDisabled
	ScopeChats                           = connectorwire.ScopeChats
	ScopeItems                           = connectorwire.ScopeItems
	ScopeInbox                           = connectorwire.ScopeInbox
	ThreadMost                           = connectorwire.ThreadMost
	MediaMost                            = connectorwire.MediaMost
	ThreadKindNone                       = connectorwire.ThreadKindNone
	PromptDeviceCode                     = connectorwire.PromptDeviceCode
	PromptQRCode                         = connectorwire.PromptQRCode
	CapStatus                            = connectorwire.CapStatus
	CapTools                             = connectorwire.CapTools
	CapCatalog                           = connectorwire.CapCatalog
	CapActions                           = connectorwire.CapActions
	CapEvents                            = connectorwire.CapEvents
	CapInbox                             = connectorwire.CapInbox
	CapSettle                            = connectorwire.CapSettle
	CapArchive                           = connectorwire.CapArchive
	CapChats                             = connectorwire.CapChats
	CapItems                             = connectorwire.CapItems
	CapLogin                             = connectorwire.CapLogin
	CapOptions                           = connectorwire.CapOptions
	CapThreads                           = connectorwire.CapThreads
	CapSnapshot                          = connectorwire.CapSnapshot
	CapGroup                             = connectorwire.CapGroup
	CapMeta                              = connectorwire.CapMeta
	CapFallback                          = connectorwire.CapFallback
	CapMachines                          = connectorwire.CapMachines
	CapRecall                            = connectorwire.CapRecall
	CapInboxVisibility                   = connectorwire.CapInboxVisibility
	CapHistory                           = connectorwire.CapHistory
	CapHistoryAnchors                    = connectorwire.CapHistoryAnchors
	ApprovalProtocolGuardedAutoConfirmV1 = connectorwire.ApprovalProtocolGuardedAutoConfirmV1
	ApprovalModeAutoConfirm              = connectorwire.ApprovalModeAutoConfirm
	AutoConfirmTokenMaxLen               = connectorwire.AutoConfirmTokenMaxLen
	AutoConfirmDefaultMaxTTLSeconds      = connectorwire.AutoConfirmDefaultMaxTTLSeconds
	AutoConfirmHardMaxTTLSeconds         = connectorwire.AutoConfirmHardMaxTTLSeconds
)

var (
	ErrRecallUnknown     = connectorwire.ErrRecallUnknown
	ErrCannotRecall      = connectorwire.ErrCannotRecall
	ErrRecallUnavailable = connectorwire.ErrRecallUnavailable
	ErrCannotReply       = connectorwire.ErrCannotReply
)

// Helpers of the SPI.
var (
	Write             = connectorwire.Write
	WriteWithApproval = connectorwire.WriteWithApproval
	RecallerOf        = connectorwire.RecallerOf
	CheckKinds        = connectorwire.CheckKinds
)

// Egress is defined in the leaf library netproxy, so a connector built as a module reaches it too; the old name stays.
type Egress = netproxy.Egress
