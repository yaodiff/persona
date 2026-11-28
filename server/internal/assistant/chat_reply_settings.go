package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

func (s *Service) chatReplyMutedChatsSetting() ownSetting {
	return ownSetting{
		Setting: Setting{
			Key:     chatReplyMutedChatsKey,
			Title:   "Muted chats for reply relays",
			Actions: []string{"get", "add", "remove"},
			About: "Lark chats where chat_reply relays are stopped even when assistant.chat_reply_relay is on. " +
				"get lists muted chats and recent Lark chats where Persona wrote as you in the last 24 hours with their chat IDs",
			Value: `add: "<chat id>" or {"chat": "<chat id>", "title"?: "<name>"}; remove: "<chat id>"`,
		},
		get:    s.getChatReplyMutedChats,
		change: s.changeChatReplyMutedChats,
	}
}

func normalizeChatReplyFeed(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "chat:") {
		feed, ok := larkChatFeed(s)
		if !ok {
			return "", errors.New("invalid Lark chat ID")
		}
		s = strings.TrimSpace(feed)
	}
	if s == "" || len(s) > 200 {
		return "", errors.New("chat ID must be a non-empty string up to 200 bytes")
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return "", errors.New("chat ID must not contain whitespace or control characters")
		}
	}
	return s, nil
}

func chatReplyChatMuted(cfg Settings, feed string) bool {
	if feed == "" {
		return false
	}
	for _, m := range cfg.ChatReplyMutedChats {
		if m.Chat == feed {
			return true
		}
		if norm, err := normalizeChatReplyFeed(m.Chat); err == nil && norm == feed {
			return true
		}
	}
	return false
}

func (s *Service) chatReplyTitle(feed string) string {
	if feed == "" {
		return ""
	}
	b := &s.chatReplies
	b.mu.Lock()
	if m, ok := b.meta[feed]; ok && m.title != "" {
		b.mu.Unlock()
		return m.title
	}
	b.mu.Unlock()

	return s.inboxChatTitle(feed)
}

func (s *Service) chatReplyTitleWithSettings(cfg Settings, feed string) string {
	for _, m := range cfg.ChatReplyMutedChats {
		if m.Chat == feed || func() bool { n, err := normalizeChatReplyFeed(m.Chat); return err == nil && n == feed }() {
			if m.Title != "" {
				return m.Title
			}
		}
	}
	return s.chatReplyTitle(feed)
}

func (s *Service) getChatReplyMutedChats(ctx context.Context) (string, error) {
	cfg := s.settings(ctx)
	var b strings.Builder
	b.WriteString("Muted chats:\n")
	if len(cfg.ChatReplyMutedChats) == 0 {
		b.WriteString("- (none)\n")
	} else {
		for _, m := range cfg.ChatReplyMutedChats {
			if m.Title != "" && m.Title != m.Chat {
				fmt.Fprintf(&b, "- %s (%s)\n", m.Chat, m.Title)
			} else {
				fmt.Fprintf(&b, "- %s\n", m.Chat)
			}
		}
	}

	ledger, ok := s.store().(chatSentStore)
	if !ok || cfg.MachineID == "" {
		return strings.TrimRight(b.String(), "\n"), nil
	}
	now := time.Now()
	rows, err := ledger.SentAsUsersForArchive(ctx, connector.Lark, now.Add(-chatReplyWindow), now.Add(time.Minute))
	if err != nil {
		return "", err
	}
	type recentChat struct {
		feed string
		at   time.Time
	}
	latestByFeed := map[string]time.Time{}
	for _, r := range rows {
		if r.RecalledAt != nil {
			continue
		}
		feed, ok := wroteFeed(r)
		if !ok {
			continue
		}
		var o struct {
			Machine string `json:"machine_id"`
			Session string `json:"session_id"`
		}
		if json.Unmarshal(r.Origin, &o) != nil || o.Session == "" || o.Machine != cfg.MachineID {
			continue
		}
		if prev, exists := latestByFeed[feed]; !exists || r.At.After(prev) {
			latestByFeed[feed] = r.At
		}
	}
	recent := make([]recentChat, 0, len(latestByFeed))
	for feed, at := range latestByFeed {
		if now.Sub(at) <= chatReplyWindow {
			recent = append(recent, recentChat{feed: feed, at: at})
		}
	}
	sort.Slice(recent, func(i, j int) bool {
		if !recent[i].at.Equal(recent[j].at) {
			return recent[i].at.After(recent[j].at)
		}
		return recent[i].feed < recent[j].feed
	})

	loc := s.location(ctx)
	b.WriteString("\nRecent Lark chats where Persona wrote as you (last 24h):\n")
	if len(recent) == 0 {
		b.WriteString("- (none)\n")
		return strings.TrimRight(b.String(), "\n"), nil
	}
	relayOn := chatReplyRelayOn(cfg)
	for _, c := range recent {
		status := "active"
		switch {
		case chatReplyChatMuted(cfg, c.feed):
			status = "muted"
		case s.chatReplyTakenOver(ctx, c.feed, c.at, now):
			status = "auto-stopped after owner replied"
		case !relayOn:
			status = "global relay off"
		}
		title := s.chatReplyTitleWithSettings(cfg, c.feed)
		ts := c.at.In(loc).Format("2006-01-02 15:04")
		if title != "" && title != c.feed {
			fmt.Fprintf(&b, "- %s (%s; last sent %s; status: %s)\n", c.feed, title, ts, status)
		} else {
			fmt.Fprintf(&b, "- %s (last sent %s; status: %s)\n", c.feed, ts, status)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func parseChatReplyMuteInput(v json.RawMessage) (chat, title string, err error) {
	if len(v) == 0 || string(v) == "null" {
		return "", "", errors.New(`value must be a chat ID string or {"chat", "title"?}`)
	}
	var str string
	if json.Unmarshal(v, &str) == nil {
		return str, "", nil
	}
	var obj struct {
		Chat  string `json:"chat"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(v, &obj); err != nil {
		return "", "", errors.New(`value must be a chat ID string or {"chat", "title"?}`)
	}
	return obj.Chat, obj.Title, nil
}

func (s *Service) changeChatReplyMutedChats(ctx context.Context, action string, v json.RawMessage) (*ownChange, error) {
	rawChat, rawTitle, err := parseChatReplyMuteInput(v)
	if err != nil {
		return nil, err
	}
	feed, err := normalizeChatReplyFeed(rawChat)
	if err != nil {
		return nil, err
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	cfg := s.settings(ctx)
	switch action {
	case "add":
		if chatReplyChatMuted(cfg, feed) {
			return &ownChange{summary: fmt.Sprintf("Chat %s is already muted for chat_reply relays.", feed)}, nil
		}
		if len(cfg.ChatReplyMutedChats) >= chatReplyMutedChatsMax {
			return nil, fmt.Errorf("at most %d muted chats are allowed", chatReplyMutedChatsMax)
		}
		title := clip(oneLine(strings.TrimSpace(rawTitle)), 60)
		if title == "" {
			title = s.chatReplyTitle(feed)
		}
		displayTitle := cmp.Or(title, feed)
		return &ownChange{
			summary: "Mute chat_reply relays for " + feed,
			cardText: func(l i18n.Lang) string {
				return i18n.T(l, "assistant-settings_tool.change_chat_reply_relay_muted_add", i18n.Args{"title": displayTitle, "chat": feed})
			},
			apply: func(ctx context.Context) (string, error) {
				if _, err := s.updateSettings(ctx, func(v *Settings) error {
					if chatReplyChatMuted(*v, feed) {
						return nil
					}
					if len(v.ChatReplyMutedChats) >= chatReplyMutedChatsMax {
						return fmt.Errorf("at most %d muted chats are allowed", chatReplyMutedChatsMax)
					}
					v.ChatReplyMutedChats = append(slices.Clone(v.ChatReplyMutedChats), chatReplyMutedChat{Chat: feed, Title: title})
					return nil
				}); err != nil {
					return "", err
				}
				s.settingsChanged(ctx)
				s.dropQueuedChatReplies(ctx, feed, time.Time{})
				return fmt.Sprintf("Muted chat_reply relays for chat %s. Use remove with %q to resume.", feed, feed), nil
			},
		}, nil
	case "remove":
		var found *chatReplyMutedChat
		for i := range cfg.ChatReplyMutedChats {
			m := &cfg.ChatReplyMutedChats[i]
			if m.Chat == feed {
				found = m
				break
			}
			if norm, err := normalizeChatReplyFeed(m.Chat); err == nil && norm == feed {
				found = m
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("chat %s is not muted in %s (get lists muted chats)", feed, chatReplyMutedChatsKey)
		}
		displayTitle := cmp.Or(found.Title, s.chatReplyTitle(feed), feed)
		return &ownChange{
			summary: "Unmute chat_reply relays for " + feed,
			cardText: func(l i18n.Lang) string {
				return i18n.T(l, "assistant-settings_tool.change_chat_reply_relay_muted_remove", i18n.Args{"title": displayTitle, "chat": feed})
			},
			apply: func(ctx context.Context) (string, error) {
				if _, err := s.updateSettings(ctx, func(v *Settings) error {
					v.ChatReplyMutedChats = slices.DeleteFunc(slices.Clone(v.ChatReplyMutedChats), func(m chatReplyMutedChat) bool {
						if m.Chat == feed {
							return true
						}
						norm, err := normalizeChatReplyFeed(m.Chat)
						return err == nil && norm == feed
					})
					return nil
				}); err != nil {
					return "", err
				}
				s.settingsChanged(ctx)
				return fmt.Sprintf("Resumed chat_reply relays for chat %s.", feed), nil
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}
