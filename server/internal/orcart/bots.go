package orcart

import (
	"context"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Orca's bots (docs/integrations/orca.md "Bots found on a computer", #16891): bots.roster lists them,
// bots.chats a bot's chats (its Home Chat among them), bots.items.list its items and bots.items.owner.comment
// answers one as the owner, all on this socket. Learned from Orca 1.4.274. An Orca without bots (older, or with Bots
// switched off) answers these with an error, which callers take as "no bots".

// BotsTimeout bounds a bots.* read: the runtime reads its own store, chats included.
const BotsTimeout = CallTimeout

// botsItemsPage is how many items one bots.items.list read asks for (Orca's own page is 50).
const botsItemsPage = 50

// Bots are bots.roster's bots on this computer (not a paired server's), without their chats.
func (c *Client) Bots(ctx context.Context) ([]proto.OrcaBot, error) {
	var res struct {
		Bots []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Agent     string `json:"agent"`
			Paused    bool   `json:"paused"`
			Specialty string `json:"specialty"`
		} `json:"bots"`
	}
	if err := c.Call(ctx, BotsTimeout, "bots.roster", map[string]any{"withoutChats": true}, &res); err != nil {
		return nil, err
	}
	out := make([]proto.OrcaBot, 0, len(res.Bots))
	for _, b := range res.Bots {
		if b.ID == "" {
			continue
		}
		out = append(out, proto.OrcaBot{BotID: proto.OrcaRef(b.ID), Name: firstWord(b.Name, b.ID), Agent: b.Agent,
			Paused: b.Paused, Specialty: b.Specialty})
	}
	return out, nil
}

// botChat is one chat of a bot (bots.chats): an Orca structured session.
type botChat struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	LastActivityAt int64  `json:"lastActivityAt"`
	Home           bool   `json:"home"`
	Closed         bool   `json:"closed"`
}

// BotHome is the bot's open Home Chat (bots.chats): its Orca session, title and last activity; an empty session while
// it has none. Closed chats (earlier Home Chats) are never it.
func (c *Client) BotHome(ctx context.Context, botID proto.OrcaRef) (session proto.OrcaRef, title string, activeMs int64, err error) {
	bot := proto.OrcaBot{BotID: botID}
	if _, err := c.BotChats(ctx, &bot); err != nil {
		return "", "", 0, err
	}
	return bot.HomeSessionID, bot.HomeTitle, bot.LastActiveMs, nil
}

// BotChats reads the bot's chats (bots.chats) into bot: its open Home Chat (HomeSessionID, HomeTitle, LastActiveMs,
// left empty while it has none), and answers the Orca sessions of all its open chats, the Home Chat and the side chats
// its owner started. Closed chats (earlier Home Chats) are neither.
func (c *Client) BotChats(ctx context.Context, bot *proto.OrcaBot) ([]proto.OrcaRef, error) {
	chats, err := c.botChats(ctx, bot.BotID)
	if err != nil {
		return nil, err
	}
	bot.HomeSessionID, bot.HomeTitle, bot.LastActiveMs = "", "", 0
	open := []proto.OrcaRef{}
	for _, ch := range chats {
		if ch.Closed || ch.ID == "" {
			continue
		}
		open = append(open, proto.OrcaRef(ch.ID))
		if ch.Home && bot.HomeSessionID == "" {
			bot.HomeSessionID, bot.HomeTitle, bot.LastActiveMs = proto.OrcaRef(ch.ID), ch.Title, ch.LastActivityAt
		}
	}
	return open, nil
}

func (c *Client) botChats(ctx context.Context, botID proto.OrcaRef) ([]botChat, error) {
	if botID == "" {
		return nil, fmt.Errorf("orca bots.chats: bot id is required")
	}
	var res struct {
		Chats []botChat `json:"chats"`
	}
	if err := c.Call(ctx, BotsTimeout, "bots.chats", map[string]any{"botId": string(botID)}, &res); err != nil {
		return nil, err
	}
	return res.Chats, nil
}

// BotItems are the bot's items with one of statuses (bots.items.list; none: Orca's open ones), newest activity first,
// at most one page.
func (c *Client) BotItems(ctx context.Context, botID proto.OrcaRef, statuses ...proto.OrcaItemStatus) (*proto.OrcaBotItems, error) {
	if botID == "" {
		return nil, fmt.Errorf("orca bots.items.list: bot id is required")
	}
	params := map[string]any{"botId": string(botID), "limit": botsItemsPage}
	if len(statuses) > 0 {
		s := make([]string, len(statuses))
		for i, st := range statuses {
			s[i] = string(st)
		}
		params["statuses"] = s
	}
	var res struct {
		BotID   string `json:"botId"`
		BotName string `json:"botName"`
		Items   []struct {
			UID       string `json:"uid"`
			Number    int    `json:"number"`
			Title     string `json:"title"`
			DoneMeans string `json:"doneMeans"`
			Status    string `json:"status"`
			Chat      *struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"chat"`
			LastActivityAt int64 `json:"lastActivityAt"`
			Question       *struct {
				EventID   string `json:"eventId"`
				Text      string `json:"text"`
				Gist      string `json:"gist"`
				Decisions []struct {
					Question string `json:"question"`
					Multi    bool   `json:"multi"`
					Replies  []struct {
						Label       string `json:"label"`
						Detail      string `json:"detail"`
						Recommended bool   `json:"recommended"`
					} `json:"replies"`
				} `json:"decisions"`
			} `json:"question"`
		} `json:"items"`
	}
	if err := c.Call(ctx, BotsTimeout, "bots.items.list", params, &res); err != nil {
		return nil, err
	}
	out := &proto.OrcaBotItems{BotID: proto.OrcaRef(firstWord(res.BotID, string(botID))), Name: res.BotName,
		Items: make([]proto.OrcaBotItem, 0, len(res.Items))}
	for _, it := range res.Items {
		if it.UID == "" {
			continue
		}
		item := proto.OrcaBotItem{ItemID: proto.OrcaRef(it.UID), Number: it.Number, Title: it.Title, DoneMeans: it.DoneMeans,
			Status: proto.OrcaItemStatus(it.Status), LastActiveMs: it.LastActivityAt}
		if it.Chat != nil {
			item.ChatSessionID, item.ChatTitle = proto.OrcaRef(it.Chat.ID), it.Chat.Title
		}
		if q := it.Question; q != nil && q.EventID != "" {
			question := &proto.OrcaItemQuestion{EventID: proto.OrcaRef(q.EventID), Gist: q.Gist, Text: q.Text}
			for _, d := range q.Decisions {
				if strings.TrimSpace(d.Question) == "" {
					continue
				}
				decision := proto.OrcaItemDecision{Question: d.Question, Multi: d.Multi, Replies: []proto.OrcaItemReply{}}
				for _, r := range d.Replies {
					if r.Label != "" {
						decision.Replies = append(decision.Replies, proto.OrcaItemReply{Label: r.Label, Detail: r.Detail,
							Recommended: r.Recommended})
					}
				}
				question.Decisions = append(question.Decisions, decision)
			}
			item.Question = question
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// AnswerBotItem answers the item's open question as its owner (bots.items.owner.comment): text names the question by
// its event, and requestID makes a retry of the same answer land once. Orca wakes the bot with it.
func (c *Client) AnswerBotItem(ctx context.Context, item, question proto.OrcaRef, text, requestID string) error {
	if item == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("orca bots.items.owner.comment: item and text are required")
	}
	params := map[string]any{"item": string(item), "text": text}
	if question != "" {
		params["questionId"] = string(question)
	}
	if requestID != "" {
		params["requestId"] = requestID
	}
	return c.Call(ctx, WriteTimeout, "bots.items.owner.comment", params, nil)
}

// firstWord is the first of values with words in it, trimmed.
func firstWord(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
