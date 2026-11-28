package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Group access (docs/extensions/bots.md "Group access for teammates"): who besides the owner may @ the bot in a group.
// The hub owns the policy and every gate. A non-owner group message is judged by guestAccess, then handed to the
// assistant over feishu.guest (bots_guest.go); it never goes through handToAssistant or feishu.message.

const (
	botGroupListMax            = 200 // each of the four lists (botHandoffChatsMax)
	botGuestToolsMax           = 8   // assistant.maxGuestTools
	botGuestPerChatHourDefault = 20
	botGuestPerChatHourMin     = 1
	botGuestPerChatHourMax     = 120
)

// botGroupMember is a user or a chat on one of the four lists.
type botGroupMember struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // the owner's label, clipped with handoffLabel
}

type botGroupAccess struct {
	Mode          assistantwire.FeishuGroupAccessMode `json:"mode,omitempty"` // "" is owner_only
	AllowedUsers  []botGroupMember                    `json:"allowed_users,omitempty"`
	AllowedChats  []botGroupMember                    `json:"allowed_chats,omitempty"`
	BlockedUsers  []botGroupMember                    `json:"blocked_users,omitempty"`
	BlockedChats  []botGroupMember                    `json:"blocked_chats,omitempty"`
	GuestTools    []string                            `json:"guest_tools,omitempty"`
	PerChatHourly int                                 `json:"per_chat_hourly,omitempty"` // 0 is botGuestPerChatHourDefault
	Revision      int64                               `json:"revision,omitempty"`
}

// The reasons guestAccess gives why a non-owner may not trigger the bot, in the order it checks them.
const (
	botGuestBlockedUser    = "blocked_user"
	botGuestBlockedChat    = "blocked_chat"
	botGuestOwnerOnly      = "owner_only"
	botGuestChatNotAllowed = "chat_not_allowed"
	botGuestUserNotAllowed = "user_not_allowed"
)

// groupChatBlocked: chatID is on group_access.blocked_chats. It silences every inbound group message, the owner's too.
func (s FeishuBotSettings) groupChatBlocked(chatID string) bool {
	if chatID == "" || s.GroupAccess == nil {
		return false
	}
	return groupListed(s.GroupAccess.BlockedChats, chatID)
}

// guestAccess says why a non-owner may not trigger the bot in chatID; "" when they may. The order is the precedence.
func (s FeishuBotSettings) guestAccess(sender, chatID string) string {
	g := s.GroupAccess
	if g != nil && groupListed(g.BlockedUsers, sender) {
		return botGuestBlockedUser
	}
	if s.groupChatBlocked(chatID) {
		return botGuestBlockedChat
	}
	if s.Owner == nil || g == nil || g.Mode.Effective() == assistantwire.FeishuGroupOwnerOnly {
		return botGuestOwnerOnly
	}
	if !groupListed(g.AllowedChats, chatID) {
		return botGuestChatNotAllowed
	}
	if g.Mode == assistantwire.FeishuGroupAllowlist && !groupListed(g.AllowedUsers, sender) {
		return botGuestUserNotAllowed
	}
	return ""
}

// perChatHourly is the policy's cap, the default when it was never set.
func (g *botGroupAccess) perChatHourly() int {
	if g == nil || g.PerChatHourly == 0 {
		return botGuestPerChatHourDefault
	}
	return g.PerChatHourly
}

func groupListed(list []botGroupMember, id string) bool {
	return id != "" && slices.ContainsFunc(list, func(m botGroupMember) bool { return m.ID == id })
}

func (g *botGroupAccess) copy() *botGroupAccess {
	if g == nil {
		return nil
	}
	c := *g
	c.AllowedUsers = slices.Clone(g.AllowedUsers)
	c.AllowedChats = slices.Clone(g.AllowedChats)
	c.BlockedUsers = slices.Clone(g.BlockedUsers)
	c.BlockedChats = slices.Clone(g.BlockedChats)
	c.GuestTools = slices.Clone(g.GuestTools)
	return &c
}

// groupAccessEqual says whether two policies are the same, revision aside: an identical document changes nothing.
func groupAccessEqual(a, b *botGroupAccess) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Mode == b.Mode && a.PerChatHourly == b.PerChatHourly && slices.Equal(a.GuestTools, b.GuestTools) &&
		slices.Equal(a.AllowedUsers, b.AllowedUsers) && slices.Equal(a.AllowedChats, b.AllowedChats) &&
		slices.Equal(a.BlockedUsers, b.BlockedUsers) && slices.Equal(a.BlockedChats, b.BlockedChats)
}

// checkGroupMembers is checkHandoffChats' rule for one of the four lists: trimmed ids, no whitespace, no repeats, bounded.
func checkGroupMembers(list []botGroupMember, field string) ([]botGroupMember, error) {
	if len(list) > botGroupListMax {
		return nil, groupAccessInvalid(field)
	}
	out := make([]botGroupMember, 0, len(list))
	seen := map[string]bool{}
	for _, m := range list {
		id := strings.TrimSpace(m.ID)
		if id == "" || len(id) > 100 || strings.ContainsFunc(id, unicode.IsSpace) || seen[id] {
			return nil, groupAccessInvalid(field)
		}
		seen[id] = true
		out = append(out, botGroupMember{ID: id, Name: handoffLabel(m.Name)})
	}
	return out, nil
}

// groupAccessInvalid refuses one field of the policy.
func groupAccessInvalid(field string) error {
	return i18n.Err("hub-bots.group_access_invalid", i18n.Args{"field": field})
}

// setGroupAccess replaces the whole policy (PUT /api/v1/bots/feishu/group-access): validated before anything is written,
// saved under the lock, rolled back when the save fails, audited after the lock only when it changed. An identical
// document changes nothing and keeps the revision.
func (h *Manager) setGroupAccess(ctx context.Context, in assistantwire.FeishuGroupAccessUpdate) (botGroupAccess, error) {
	if !in.Policy.Mode.Valid() {
		return botGroupAccess{}, groupAccessInvalid("mode")
	}
	users, err := checkGroupMembers(membersOf(in.Policy.AllowedUsers), "allowed_users")
	if err != nil {
		return botGroupAccess{}, err
	}
	chats, err := checkGroupMembers(chatsOf(in.Policy.AllowedChats), "allowed_chats")
	if err != nil {
		return botGroupAccess{}, err
	}
	blockedUsers, err := checkGroupMembers(membersOf(in.Policy.BlockedUsers), "blocked_users")
	if err != nil {
		return botGroupAccess{}, err
	}
	blockedChats, err := checkGroupMembers(chatsOf(in.Policy.BlockedChats), "blocked_chats")
	if err != nil {
		return botGroupAccess{}, err
	}
	hourly := in.Policy.PerChatHourly
	switch {
	case hourly == 0:
		hourly = botGuestPerChatHourDefault
	case hourly < botGuestPerChatHourMin || hourly > botGuestPerChatHourMax:
		return botGroupAccess{}, groupAccessInvalid("per_chat_hourly")
	}
	tools, err := h.checkGuestTools(in.Policy.GuestTools)
	if err != nil {
		return botGroupAccess{}, err
	}
	next := botGroupAccess{Mode: in.Policy.Mode, AllowedUsers: users, AllowedChats: chats, BlockedUsers: blockedUsers,
		BlockedChats: blockedChats, GuestTools: tools, PerChatHourly: hourly}
	needsOwner := next.Mode != assistantwire.FeishuGroupOwnerOnly || len(users)+len(chats)+len(blockedUsers)+len(blockedChats)+len(tools) > 0
	if needsOwner {
		if err := h.topicChatsPossible(ctx, nil); err != nil {
			if errors.Is(err, errNoTopicOwner) {
				return botGroupAccess{}, i18n.Err("hub-bots.group_access_needs_owner")
			}
			return botGroupAccess{}, err
		}
	}
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return botGroupAccess{}, err
	}
	if b.set.Owner != nil && groupListed(blockedUsers, b.set.Owner.OpenID) {
		b.mu.Unlock()
		return botGroupAccess{}, groupAccessInvalid("blocked_users")
	}
	before := b.set.GroupAccess
	if before != nil && before.Revision != in.Revision || before == nil && in.Revision != 0 {
		b.mu.Unlock()
		return botGroupAccess{}, &groupAccessConflict{}
	}
	next.Revision = in.Revision + 1
	if groupAccessEqual(before, &next) {
		next.Revision = before.Revision
		b.mu.Unlock()
		return next, nil
	}
	b.set.GroupAccess = &next
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.GroupAccess = before
		b.mu.Unlock()
		return botGroupAccess{}, err
	}
	b.mu.Unlock()
	h.auditBot(ctx, botRecord{auditBotGroupPolicy, map[string]any{
		"mode": string(next.Mode), "allowed_users": memberIDs(next.AllowedUsers), "allowed_chats": memberIDs(next.AllowedChats),
		"blocked_users": memberIDs(next.BlockedUsers), "blocked_chats": memberIDs(next.BlockedChats),
		"guest_tools": next.GuestTools, "per_chat_hourly": next.PerChatHourly,
	}})
	return next, nil
}

// groupAccessConflict is a stale revision: the client reloads and reapplies.
type groupAccessConflict struct{}

func (*groupAccessConflict) Error() string { return "group access changed elsewhere" }

// checkGuestTools keeps only read-only connector tools, at most botGuestToolsMax, with no repeats.
func (h *Manager) checkGuestTools(list []string) ([]string, error) {
	if len(list) > botGuestToolsMax {
		return nil, groupAccessInvalid("guest_tools")
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, raw := range list {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			return nil, groupAccessInvalid("guest_tools")
		}
		if h.connectors == nil || !h.connectors.Offers(name) || !h.connectors.ReadOnly(name) {
			return nil, i18n.Err("hub-bots.group_access_tool_bad", i18n.Args{"tool": name})
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func membersOf(list []assistantwire.FeishuGroupUser) []botGroupMember {
	out := make([]botGroupMember, 0, len(list))
	for _, u := range list {
		out = append(out, botGroupMember{ID: string(u.OpenID), Name: u.Name})
	}
	return out
}

func chatsOf(list []assistantwire.FeishuGroupChat) []botGroupMember {
	out := make([]botGroupMember, 0, len(list))
	for _, c := range list {
		out = append(out, botGroupMember{ID: string(c.ChatID), Name: c.Name})
	}
	return out
}

func memberIDs(list []botGroupMember) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out
}

// groupAccessView is the response of GET and PUT, built under the lock; names of recent requests are filled afterwards.
func (h *Manager) groupAccessView(ctx context.Context) assistantwire.FeishuGroupAccessResponse {
	b := &h.bots
	b.mu.Lock()
	_ = h.loadBotsLocked(ctx)
	g := b.set.GroupAccess
	v := assistantwire.FeishuGroupAccessResponse{
		Paired: b.set.Owner != nil,
		Policy: assistantwire.FeishuGroupAccessPolicy{
			Mode: assistantwire.FeishuGroupOwnerOnly, PerChatHourly: botGuestPerChatHourDefault,
			AllowedUsers: []assistantwire.FeishuGroupUser{}, AllowedChats: []assistantwire.FeishuGroupChat{},
			BlockedUsers: []assistantwire.FeishuGroupUser{}, BlockedChats: []assistantwire.FeishuGroupChat{},
			GuestTools: []string{},
		},
		AvailableTools: []assistantwire.FeishuGuestToolOption{},
		RecentRequests: []assistantwire.FeishuGroupRequest{},
		Limits: assistantwire.FeishuGroupAccessLimits{ListMax: botGroupListMax, GuestToolsMax: botGuestToolsMax,
			PerChatHourlyMin: botGuestPerChatHourMin, PerChatHourlyMax: botGuestPerChatHourMax,
			PerChatHourlyDefault: botGuestPerChatHourDefault},
	}
	if g != nil {
		v.Revision = g.Revision
		v.Policy.Mode = g.Mode.Effective()
		v.Policy.PerChatHourly = g.perChatHourly()
		v.Policy.AllowedUsers = usersView(g.AllowedUsers)
		v.Policy.AllowedChats = chatsView(g.AllowedChats)
		v.Policy.BlockedUsers = usersView(g.BlockedUsers)
		v.Policy.BlockedChats = chatsView(g.BlockedChats)
		if g.GuestTools != nil {
			v.Policy.GuestTools = slices.Clone(g.GuestTools)
		}
	}
	for _, ask := range b.guestAsks {
		req := assistantwire.FeishuGroupRequest{OpenID: assistantwire.FeishuOpenID(ask.OpenID),
			ChatID: assistantwire.FeishuChatID(ask.ChatID), At: ask.At.Unix()}
		if name := groupLabel(b.set.TopicChats, ask.ChatID); name != "" {
			req.ChatName = name
		}
		v.RecentRequests = append(v.RecentRequests, req)
	}
	missing := slices.Clone(b.guestAsks) // read after the lock is released, while noteGuestAsk may shift the original
	b.mu.Unlock()
	if h.connectors != nil {
		for _, c := range h.connectors.All() {
			if !h.connectors.Enabled(c.ID()) {
				continue
			}
			label := connector.DisplayName(ctx, c)
			for _, name := range connector.ToolNames(c) {
				if h.connectors.ReadOnly(name) {
					v.AvailableTools = append(v.AvailableTools, assistantwire.FeishuGuestToolOption{Name: name, Connector: label})
				}
			}
		}
	}
	h.fillRequestNames(ctx, v.RecentRequests, missing)
	return v
}

func usersView(list []botGroupMember) []assistantwire.FeishuGroupUser {
	out := make([]assistantwire.FeishuGroupUser, 0, len(list))
	for _, m := range list {
		out = append(out, assistantwire.FeishuGroupUser{OpenID: assistantwire.FeishuOpenID(m.ID), Name: m.Name})
	}
	return out
}

func chatsView(list []botGroupMember) []assistantwire.FeishuGroupChat {
	out := make([]assistantwire.FeishuGroupChat, 0, len(list))
	for _, m := range list {
		out = append(out, assistantwire.FeishuGroupChat{ChatID: assistantwire.FeishuChatID(m.ID), Name: m.Name})
	}
	return out
}

func groupLabel(chats []BotChat, id string) string {
	for _, c := range chats {
		if c.ChatID == id {
			return c.Name
		}
	}
	return ""
}

// fillRequestNames looks up at most five names the recent requests do not have yet. The owner asked for the page, so a
// stranger cannot make the hub call the open platform for them.
func (h *Manager) fillRequestNames(ctx context.Context, reqs []assistantwire.FeishuGroupRequest, asks []botGuestAsk) {
	api, _ := h.botAPI()
	if api == nil {
		return
	}
	looked := 0
	for i := range reqs {
		if looked >= 5 {
			return
		}
		name := ""
		if i < len(asks) {
			name = asks[i].Name
		}
		if name == "" {
			looked++
			nctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			got, err := api.UserName(nctx, string(reqs[i].OpenID))
			cancel()
			if err == nil {
				name = got
			}
		}
		reqs[i].Name = name
	}
}

// getGroupAccess is GET /api/v1/bots/feishu/group-access.
func (h *Manager) getGroupAccess(w http.ResponseWriter, r *http.Request) {
	if answerOnly(r) {
		writeErrCode(w, http.StatusForbidden, "permission_denied", i18n.Err("hub-bots.group_access_invalid", i18n.Args{"field": "caller"}))
		return
	}
	if err := h.loadBotSettings(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, h.groupAccessView(r.Context()))
}

// putGroupAccess is PUT /api/v1/bots/feishu/group-access: the whole policy, read-modify-written by the client.
func (h *Manager) putGroupAccess(w http.ResponseWriter, r *http.Request) {
	if answerOnly(r) {
		writeErrCode(w, http.StatusForbidden, "permission_denied", i18n.Err("hub-bots.group_access_invalid", i18n.Args{"field": "caller"}))
		return
	}
	if err := h.loadBotSettings(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	var in assistantwire.FeishuGroupAccessUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid_group_access", groupAccessInvalid("body"))
		return
	}
	saved, err := h.setGroupAccess(r.Context(), in)
	var told *i18n.Message
	switch {
	case errors.As(err, new(*groupAccessConflict)):
		writeErrCode(w, http.StatusConflict, "group_access_conflict", i18n.Err("hub-bots.group_access_conflict"))
	case errors.As(err, &told):
		writeErrCode(w, http.StatusBadRequest, "invalid_group_access", err)
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err) // unreadable, or the save did not land
	default:
		_ = saved
		writeJSON(w, h.groupAccessView(r.Context()))
	}
}
