package larkcli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

const (
	maxOwnerPromiseGroups = 5
	ownerPromiseGrace     = 15 * time.Minute
	ownerPromiseLookback  = 2 * time.Hour
	ownerPromiseMaxReads  = 8
	ownerPromiseMaxStored = 100
)

var ownerPromiseWords = lazyre.New(`(?i)^(?:i(?:['’]ll| will)\s+(?:look into|look at|check|investigate|fix|follow up|report back|take care of)\b|(?:我(?:会|来|去|晚点|稍后|回头)|晚点|稍后|回头)(?:再|去|来)?\s*(?:看|查|修|排查|跟进|处理|同步))`) // i18n-ignore: English and Chinese message data.
var delegatedPromiseWords = lazyre.New(`(?i)^(?:i(?:['’]ve| have)? asked (?:my|the) (?:ai|assistant) to (?:look|check|investigate)\b|我(?:已(?:经)?)?(?:让|叫|请)我的?\s*(?:ai|助手)\s*(?:一起)?(?:看|查|跟进|排查))`)                   // i18n-ignore: English and Chinese message data.

// pollOwnerPromises uses only the explicitly selected group reads. Starting
// a pilot or changing login establishes a fresh baseline.
func (c *Connector) pollOwnerPromises(ctx context.Context, auth adapter.AuthStatus, now time.Time) (bool, error) {
	c.inboxMu.Lock()
	changed := false
	var groups []string
	modelAllowed := c.ownerPromiseModel
	identity := auth.Brand + "/" + auth.AppID + "/" + auth.OpenID
	if len(c.ownerPromiseGroups) > 0 && c.inboxState.PromiseOwner != identity {
		c.inboxState.PromiseOwner = identity
		c.inboxState.PromiseSince = nil
		for key, thing := range c.inboxThings {
			if thing.OwnerPromise != nil {
				thing.Done = connectorwire.DoneRead
				c.inboxThings[key] = thing
				changed = true
			}
		}
	}
	for group := range c.ownerPromiseGroups {
		if !c.replyChatExcludedLocked(group, "group", "") {
			groups = append(groups, group)
		}
	}
	for key, thing := range c.inboxThings {
		if thing.OwnerPromise == nil {
			continue
		}
		if now.Sub(thing.At) > ownerPromiseLookback {
			delete(c.inboxThings, key)
			continue
		}
		if !slices.Contains(groups, string(thing.OwnerPromise.ChatID)) {
			thing.Done = connectorwire.DoneRead
			c.inboxThings[key] = thing
			changed = true
		}
	}
	if c.inboxState.PromiseSince == nil && len(groups) > 0 {
		c.inboxState.PromiseSince = make(map[string]time.Time, len(groups))
	}
	var scan []string
	slices.Sort(groups)
	for _, group := range groups {
		if c.inboxState.PromiseSince[group].IsZero() {
			c.inboxState.PromiseSince[group] = now
			changed = true
		} else {
			scan = append(scan, group)
		}
	}
	c.inboxMu.Unlock()

	reads := 0
	var lastError error
	for _, group := range scan {
		c.inboxMu.Lock()
		since := c.inboxState.PromiseSince[group]
		c.inboxMu.Unlock()
		if cutoff := now.Add(-ownerPromiseLookback); cutoff.After(since) {
			since = cutoff
		}
		page, err := c.runner.SearchMessages(ctx, "", adapter.SearchOptions{
			ChatID: group, Sender: auth.OpenID, Start: since.UTC().Format(time.RFC3339), Limit: 50,
		})
		if err != nil {
			lastError = err
			continue
		}
		slices.SortStableFunc(page.Messages, func(a, b adapter.Message) int {
			return messageTime(b).Compare(messageTime(a))
		})
		for _, msg := range page.Messages {
			if msg.ChatID != group || !ownsPromiseMessage(msg, auth.OpenID) {
				continue
			}
			key := ownerPromiseKey(identity, group, msg.MessageID)
			c.inboxMu.Lock()
			old, seen := c.inboxThings[key]
			c.inboxMu.Unlock()
			if seen && !msg.Deleted && !msg.Updated {
				changed = changed || !now.Before(old.At.Add(ownerPromiseGrace))
				continue
			}
			at := messageTime(msg)
			text := strings.TrimSpace(messageText(msg))
			strong, delegated := ownerPromiseWords.MatchString(text), modelAllowed && delegatedPromiseWords.MatchString(text)
			if msg.Deleted || (!strong && !delegated) || len(msg.Mentions) != 0 ||
				strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") { // i18n-ignore: source punctuation.
				if seen && old.Done == "" {
					old.Done = connectorwire.DoneRead
					c.inboxMu.Lock()
					c.inboxThings[key] = old
					c.inboxMu.Unlock()
					changed = true
				}
				continue
			}
			if at.IsZero() || at.Before(since) || at.After(now) || !strings.HasPrefix(msg.ThreadID, "omt_") ||
				!validOpaqueID(msg.ThreadID) || reads >= ownerPromiseMaxReads || seen && (old.Done != "" || old.Preview == text) {
				continue
			}
			reads++
			thread, err := c.runner.ListMessages(ctx, group, msg.ThreadID, "", 12)
			if err != nil {
				lastError = err
				continue
			}
			request := promiseRequest(msg, thread.Messages, auth.OpenID)
			// A contextual hand-off needs the preceding request. Explicit
			// promises may stand on their own in a verified topic.
			if !strong && request.MessageID == "" {
				continue
			}
			host := "applink.feishu.cn"
			if strings.EqualFold(auth.Brand, "lark") {
				host = "applink.larksuite.com"
			}
			source := "https://" + host + "/client/thread/open?" + url.Values{
				"open_chat_id": {group}, "open_thread_id": {msg.ThreadID}, "message_id": {msg.MessageID},
			}.Encode()
			c.inboxMu.Lock()
			_, stillSelected := c.ownerPromiseGroups[group]
			current := c.inboxState.PromiseSince[group]
			if stillSelected && (strong || c.ownerPromiseModel) && !current.IsZero() && !at.Before(current) &&
				!c.replyChatExcludedLocked(group, "group", "") {
				// Bound pending observations independently of ordinary Inbox rows.
				if seen || c.ownerPromiseCountLocked() < ownerPromiseMaxStored {
					c.inboxThings[key] = connectorwire.InboxThing{
						Thread: key, Kind: connectorwire.OwnerPromiseKind, Title: msg.ChatName, Preview: promiseExcerpt(text),
						Last: msg.MessageID, At: at, OpenURL: source,
						Context: promiseExcerpt(messageText(request)),
						Tags:    []connectorwire.Tag{{Key: connectorwire.TagChat, Value: group, Name: msg.ChatName}},
						OwnerPromise: &connectorwire.OwnerPromise{ChatID: connectorwire.LarkChatID(group), TopicID: connectorwire.LarkTopicID(msg.ThreadID),
							RequestID: connectorwire.LarkMessageID(request.MessageID), Model: !strong},
					}
					changed = true
				}
			}
			c.inboxMu.Unlock()
		}
	}
	return changed, lastError
}

func ownsPromiseMessage(msg adapter.Message, owner string) bool {
	direct, known := directChatType(msg.ChatType, msg.ChatMode)
	return known && !direct && owner != "" && msg.Sender.ID == owner && msg.Sender.SenderType == "user" &&
		msg.Sender.AppID == "" && (msg.Sender.IDType == "" || msg.Sender.IDType == "open_id") &&
		validOpaqueID(msg.MessageID) && msg.MsgType == "text"
}

func ownerPromiseKey(account, group, message string) string {
	digest := sha256.Sum256([]byte(account + "\n" + group + "\n" + message))
	return fmt.Sprintf("owner-promise:%x", digest[:16])
}

func promiseRequest(owner adapter.Message, history []adapter.Message, ownerID string) adapter.Message {
	var request adapter.Message
	for _, other := range history {
		// A named topic read proves the topic of a row that omits it.
		if other.ChatID == "" {
			other.ChatID = owner.ChatID
		}
		if other.ChatID != owner.ChatID || other.ThreadID != "" && other.ThreadID != owner.ThreadID ||
			other.Deleted || other.Sender.ID == "" || other.Sender.ID == ownerID || other.Sender.SenderType != "user" ||
			other.MsgType != "text" || !validOpaqueID(other.MessageID) || !after(owner, other) ||
			(other.MessageID != owner.ParentID && !messageMentions(other, ownerID)) ||
			strings.TrimSpace(messageText(other)) == "" {
			continue
		}
		if other.MessageID == owner.ParentID {
			return other
		}
		if request.MessageID == "" || after(other, request) {
			request = other
		}
	}
	return request
}

func promiseExcerpt(text string) string {
	words := []rune(strings.Join(strings.Fields(text), " "))
	return string(words[:min(len(words), 300)])
}

// ownerPromiseThingLocked releases the observation after the grace period.
// Removing a group or its model consent retires old cards. Keep the closure
// marker for the rest of the replay window.
func (c *Connector) ownerPromiseThingLocked(thing connectorwire.InboxThing, now time.Time) connectorwire.InboxThing {
	p := thing.OwnerPromise
	if p == nil || now.Sub(thing.At) > ownerPromiseLookback || now.Before(thing.At.Add(ownerPromiseGrace)) {
		return connectorwire.InboxThing{}
	}
	if _, selected := c.ownerPromiseGroups[string(p.ChatID)]; !selected || p.Model && !c.ownerPromiseModel {
		thing.Done = connectorwire.DoneRead
	}
	if thing.Done != "" {
		thing.Preview, thing.Context = "", ""
	}
	return thing
}

func (c *Connector) ownerPromiseCountLocked() int {
	n := 0
	for _, t := range c.inboxThings {
		if t.OwnerPromise != nil {
			n++
		}
	}
	return n
}
