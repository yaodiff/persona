package larkcli

// The adapter's configuration, values and error kinds live in the contract internal/cliwire; these names keep the
// package's code and callers compiling unchanged (#13404).

import "github.com/nwverse/persona/server/internal/cliwire"

type (
	AuthStatus    = cliwire.AuthStatus
	Chat          = cliwire.Chat
	ChatList      = cliwire.ChatList
	Comment       = cliwire.Comment
	CommentPage   = cliwire.CommentPage
	Config        = cliwire.Config
	ConfigInfo    = cliwire.ConfigInfo
	Document      = cliwire.Document
	Error         = cliwire.Error
	ErrorKind     = cliwire.ErrorKind
	Mention       = cliwire.Mention
	Message       = cliwire.Message
	MessagePage   = cliwire.MessagePage
	Profile       = cliwire.Profile
	Reply         = cliwire.Reply
	ReplyList     = cliwire.ReplyList
	ReplyPage     = cliwire.ReplyPage
	SearchOptions = cliwire.SearchOptions
	Sender        = cliwire.Sender
	StateStore    = cliwire.StateStore
	WikiNode      = cliwire.WikiNode
	WikiNodeList  = cliwire.WikiNodeList
	WikiSpace     = cliwire.WikiSpace
	WikiSpaceList = cliwire.WikiSpaceList
)

const (
	ConnectorID            = cliwire.ConnectorID
	Expired                = cliwire.Expired
	KindExpired            = cliwire.KindExpired
	KindMissingPermission  = cliwire.KindMissingPermission
	KindNotInstalled       = cliwire.KindNotInstalled
	KindNotLoggedIn        = cliwire.KindNotLoggedIn
	KindBotNotInChat       = cliwire.KindBotNotInChat
	KindOther              = cliwire.KindOther
	KindRateLimited        = cliwire.KindRateLimited
	KindTimeout            = cliwire.KindTimeout
	KindUnsupportedVersion = cliwire.KindUnsupportedVersion
	MinimumVersion         = cliwire.MinimumVersion
	MissingPermission      = cliwire.MissingPermission
	NotInstalled           = cliwire.NotInstalled
	NotLoggedIn            = cliwire.NotLoggedIn
	Other                  = cliwire.Other
	RateLimited            = cliwire.RateLimited
	Timeout                = cliwire.Timeout
	UnsupportedVersion     = cliwire.UnsupportedVersion
)

var (
	KindOf         = cliwire.KindOf
	NewError       = cliwire.NewError
	ReadAsMeScopes = cliwire.ReadAsMeScopes
)

// cliError is how the runner makes its errors (cliwire.NewError).
var cliError = cliwire.NewError
