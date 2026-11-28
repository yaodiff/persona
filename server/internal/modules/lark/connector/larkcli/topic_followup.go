package larkcli

import (
	"context"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// ObserveTopic shares the named-reply reader's exact scope, precision and bounded pagination checks.
// It returns human source receipts, never message bodies or an interpretation of what was said.
func (c *Connector) ObserveTopic(ctx context.Context, watch connectorwire.LarkTopicWatch) (connectorwire.LarkTopicObservation, error) {
	var out connectorwire.LarkTopicObservation
	chatID, threadID := string(watch.ChatID), string(watch.ThreadID)
	if c == nil || c.runner == nil || !validOpaqueID(chatID) || !validOpaqueID(threadID) {
		return out, errors.New("invalid or unavailable Lark topic reader")
	}
	id := watch.Identity
	if id == (connectorwire.LarkTopicIdentity{}) {
		auth, err := c.runner.AuthStatus(ctx)
		if err != nil {
			return out, fmt.Errorf("read linked Lark topic identity: %w", err)
		}
		if !auth.LoggedIn || auth.Expired {
			return out, errors.New("linked Lark topic account is unavailable")
		}
		id = connectorwire.LarkTopicIdentity{
			Brand: auth.Brand, AppID: connectorwire.LarkAppID(auth.AppID), AccountOpenID: connectorwire.LarkOpenID(auth.OpenID),
		}
	}
	scope := connectorwire.LarkReplyWatch{Brand: id.Brand, AppID: string(id.AppID), AccountOpenID: string(id.AccountOpenID),
		ChatID: chatID, ThreadID: threadID}
	if (scope.Brand != "feishu" && scope.Brand != "lark") || !validOpaqueID(scope.AppID) || !validOpaqueID(scope.AccountOpenID) {
		return out, errors.New("incomplete linked Lark topic identity")
	}
	before, err := c.replyAuth(ctx, scope)
	if err != nil {
		return out, err
	}
	var messages []connectorwire.LarkReplyMessage
	seen, tokens := map[string]bool{}, map[string]bool{}
	token := ""
	for pageNo := 0; pageNo < parkReplyPages; pageNo++ {
		page, err := c.runner.ListMessages(ctx, scope.ChatID, scope.ThreadID, token, 50)
		if err != nil {
			return out, fmt.Errorf("read linked Lark topic: %w", err)
		}
		reachedCheckpoint := false
		for _, msg := range page.Messages {
			if seen[msg.MessageID] {
				continue
			}
			verified, ok := verifyReplyScope(msg, scope)
			if !ok {
				continue
			}
			stamp, stamped := parseReplyTime(msg.CreateTime, c.currentTime())
			if stamped && !watch.Since.IsZero() && stamp.before.Before(watch.Since) {
				reachedCheckpoint = true
			}
			if msg.Deleted || (msg.MsgType != "text" && msg.MsgType != "post") || forwardedContent(msg.Content) ||
				msg.Sender.SenderType != "user" || msg.Sender.IDType != "open_id" || msg.Sender.AppID != "" ||
				msg.Sender.ID == scope.AccountOpenID || !validOpaqueID(msg.Sender.ID) || !validOpaqueID(msg.MessageID) {
				continue
			}
			if !stamped {
				return out, errors.New("linked Lark topic message time is unknown or in the future")
			}
			seen[msg.MessageID] = true
			messages = append(messages, connectorwire.LarkReplyMessage{ID: msg.MessageID, SenderID: msg.Sender.ID,
				SenderType: "user", IDType: "open_id", ChatID: verified.chat, ThreadID: verified.thread,
				At: stamp.at, Before: stamp.before})
		}
		// ListMessages explicitly asks for newest-first pages. Once this page reaches the saved
		// checkpoint, older pages cannot contain new activity. A legacy first look only establishes a baseline.
		if !page.HasMore || reachedCheckpoint || watch.Since.IsZero() {
			break
		}
		if page.PageToken == "" || tokens[page.PageToken] || pageNo == parkReplyPages-1 {
			return out, errors.New("linked Lark topic exceeds bounded or advancing pagination")
		}
		tokens[page.PageToken], token = true, page.PageToken
	}
	after, err := c.replyAuth(ctx, scope)
	if err != nil {
		return out, err
	}
	if before != after {
		return out, errors.New("linked Lark topic identity changed during read")
	}
	checked := c.currentTime().UTC()
	for _, msg := range messages {
		if msg.Before.After(checked) {
			return out, errors.New("linked Lark topic message time is not safely in the past")
		}
	}
	watch.Identity = id
	return connectorwire.LarkTopicObservation{Watch: watch, Messages: messages, CheckedAt: checked}, nil
}
