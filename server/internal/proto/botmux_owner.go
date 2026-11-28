package proto

import "strings"

// BotmuxOwnerUnionID is a Feishu / Lark user's union_id ("on_…"), the person a node's botmux sends speak for when
// botmux takes `actor: "owner"` (Settings › Agents, "BotMux Owner Union ID"; docs/integrations/engines.md "botmux"). The
// empty value is none: the node then sends the plain external-event trigger.
type BotmuxOwnerUnionID string

// botmuxOwnerUnionIDMax bounds a stored or scanned id: Feishu's union_ids are "on_" and 32 characters.
const botmuxOwnerUnionIDMax = 128

// ParseBotmuxOwnerUnionID trims s and says whether it is a union_id (`^on_[A-Za-z0-9_-]+$`, at most 128 bytes) or
// empty (none). ok is false for anything else, and the id is then empty.
func ParseBotmuxOwnerUnionID(s string) (BotmuxOwnerUnionID, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if len(s) > botmuxOwnerUnionIDMax || !strings.HasPrefix(s, "on_") || len(s) == len("on_") {
		return "", false
	}
	for _, r := range s[len("on_"):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", false
		}
	}
	return BotmuxOwnerUnionID(s), true
}

// MethodBotmuxOwnerCandidates asks a node for the union_ids its botmux's bots.json names (owners and allowedUsers
// entries), read-only; the answer is BotmuxOwnerCandidates. A node without it answers an unknown-method error.
const MethodBotmuxOwnerCandidates = "botmux.owner_candidates"

// BotmuxOwnerCandidates is a node's answer to MethodBotmuxOwnerCandidates: only union_ids, never the file's path,
// secrets, other ids or e-mail addresses. Empty when botmux or its bots.json is absent.
type BotmuxOwnerCandidates struct {
	Candidates []BotmuxOwnerCandidate `json:"candidates"`
}

// BotmuxOwnerCandidate is one union_id of bots.json: Bots is how many bots name it (as ownerUnionId or in
// allowedUsers), Sole how many of those name it as their only person (ownerUnionId, or an allowedUsers of just it):
// the bots botmux takes it as the owner of without more configuration.
type BotmuxOwnerCandidate struct {
	UnionID BotmuxOwnerUnionID `json:"union_id"`
	Bots    int                `json:"bots"`
	Sole    int                `json:"sole"`
}

// BotmuxExternalTriggerReason says why a botmux session's sends go as the plain external-event trigger
// (Session.BotmuxExternalTrigger), encoded as "botmux_external_trigger_reason". The zero value is unknown (an older
// node): clients show the upgrade notice.
type BotmuxExternalTriggerReason string

const (
	// BotmuxTriggerUpgrade: this botmux is older than 3.59.23 or does not advertise ownerActor.
	BotmuxTriggerUpgrade BotmuxExternalTriggerReason = "upgrade"
	// BotmuxTriggerOwnerUnset: botmux could take actor "owner", but no BotMux Owner Union ID is set in Settings.
	BotmuxTriggerOwnerUnset BotmuxExternalTriggerReason = "owner_unset"
)
