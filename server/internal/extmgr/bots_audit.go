package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/channel"
)

// What happened to the Feishu bot, as plugin_audit records it (#5353): the plugin column holds the channel's id
// (channel.Feishu) and the actions a bot_ prefix. Who did it is the device (actorOf) for what the app changed, and the
// Feishu account (botChatActor) for what happened in the chat.
const (
	auditBotCredentials = "bot_credentials"  // {app_id, domain?, cleared?, owner_reset?}: never the secret
	auditBotEnabled     = "bot_enabled"      // {enabled}: only when it changes
	auditBotFeatures    = "bot_features"     // {features}: the switches, only when they change
	auditBotNudgeAfter  = "bot_nudge_after"  // {minutes}: nudge_after, only when it changes
	auditBotPairCode    = "bot_pair_code"    // a new pairing code was issued (never the code)
	auditBotPaired      = "bot_paired"       // {open_id}: whoever sent the code is the owner now
	auditBotPairGuessed = "bot_pair_guessed" // {fails}: the pairing was dropped after too many wrong codes
	auditBotUnpaired    = "bot_unpaired"     // the owner was removed

	auditBotSourceReportDelay     = "bot_source_report_delay"      // {minutes}: source-group preview window
	auditBotSourceReportFullReply = "bot_source_report_full_reply" // {enabled}: forward the final answer

	// {chat_id}: a group chat the owner opted in, or took out again (the /topics command, or the app's list).
	auditBotTopicChatAdded   = "bot_topic_chat_added"
	auditBotTopicChatRemoved = "bot_topic_chat_removed"
	// {chat_id}: the group chat that is the home topic group now (/topics home, or the app's list).
	auditBotTopicHome = "bot_topic_home"
	// {chat_id, space}: the domain space bound to a topic group (/topics space, or the app's list).
	auditBotTopicSpace = "bot_topic_space"
	// {chat_id}: the owner's "@bot /ai …" was taken from a group chat that is not opted in (#6146). Never the words.
	auditBotAITaken = "bot_ai_taken"
	// {chat_id}: the owner's "@bot /read …" was taken from a group chat (#11803). Never the words.
	auditBotReadTaken = "bot_read_taken"

	// {off, direct_chats, blocked_chats, launcher}: the proxy-reply policy, only when it changes. Never a message.
	auditBotHandoffPolicy = "bot_handoff_policy"
	// {chat_id, handoff_id, delivery, anchored}: a handoff was taken. Never the question or the names.
	auditBotHandoffTaken = "bot_handoff_taken"
	// {chat_id, reason}: a handoff was refused, blocked or switched off.
	auditBotHandoffRefused = "bot_handoff_refused"
	// {chat_id, handoff_id, confirmed}: the bot posted the reply.
	auditBotHandoffPosted = "bot_handoff_posted"
	// {chat_id, handoff_id, outcome}: a handoff ended without a post.
	auditBotHandoffEnded = "bot_handoff_ended"

	// {mode, allowed_users, allowed_chats, blocked_users, blocked_chats, guest_tools, per_chat_hourly}: ids and tool
	// names only, when the policy changes. Never a label, never a message.
	auditBotGroupPolicy = "bot_group_policy_changed"
	// {chat_id, guest_id, mode, in_thread}: a non-owner's @bot was taken. Never the words or the name.
	auditBotGuestTriggered = "bot_guest_triggered"
	// {chat_id, guest_id}: the bot posted the guest's reply.
	auditBotGuestPosted = "bot_guest_posted"
	// {chat_id, guest_id, outcome}: a guest turn ended without a post.
	auditBotGuestEnded = "bot_guest_ended"
	// {chat_id, reason}: a trigger refused by a limit; at most one per chat per ten minutes.
	auditBotGuestRefused = "bot_guest_refused"
)

// botRecord is an audit record of the bot's, written once its lock is let go.
type botRecord struct {
	action string
	detail map[string]any
}

// auditBot records actions on the Feishu bot by whoever made the request.
func (h *Manager) auditBot(ctx context.Context, records ...botRecord) {
	for _, r := range records {
		h.audit(ctx, channel.Feishu, r.action, actorOf(ctx), r.detail)
	}
}

// botChatActor is the Feishu account that did something in the bot's chat.
func botChatActor(openID string) string { return "feishu:" + openID }
