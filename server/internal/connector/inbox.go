package connector

import (
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Defined in the contract package connectorwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	GroupAppend         = connectorwire.GroupAppend
	GroupAssign         = connectorwire.GroupAssign
	GroupEvent          = connectorwire.GroupEvent
	GroupFallback       = connectorwire.GroupFallback
	GroupFallbackResult = connectorwire.GroupFallbackResult
	GroupNew            = connectorwire.GroupNew
	GroupToolCall       = connectorwire.GroupToolCall
	InboxMention        = connectorwire.InboxMention
	MentionUserID       = connectorwire.MentionUserID
	InboxReply          = connectorwire.InboxReply
	InboxThing          = connectorwire.InboxThing
	StatusChange        = connectorwire.StatusChange
	Tag                 = connectorwire.Tag
	ThingExtra          = connectorwire.ThingExtra
	ThingGroup          = connectorwire.ThingGroup
	ThingParent         = connectorwire.ThingParent
	Tier                = connectorwire.Tier
)

const (
	DoneRead            = connectorwire.DoneRead
	DoneReplied         = connectorwire.DoneReplied
	GroupMessagesAround = connectorwire.GroupMessagesAround
	GroupMessagesBefore = connectorwire.GroupMessagesBefore
	GroupSearch         = connectorwire.GroupSearch
	TagBranch           = connectorwire.TagBranch
	TagExternalObject   = connectorwire.TagExternalObject
	TagChat             = connectorwire.TagChat
	TagRepo             = connectorwire.TagRepo
	TagSender           = connectorwire.TagSender
	TierAction          = connectorwire.TierAction
	TierFYI             = connectorwire.TierFYI
	TierNoise           = connectorwire.TierNoise
)

var MigratableConnectors = connectorwire.MigratableConnectors
