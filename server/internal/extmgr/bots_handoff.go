package extmgr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Proxy replies handed off from a group (docs/extensions/bots.md "Proxy replies handed off from a group"): an
// authorized extension, or the owner, hands the hub a question a teammate asked in a group the bot is in. The bot
// answers it. The question never enters the owner's conversations: the assistant runs it as a guest with one tool,
// and the only thing the bot may write for it is that one reply (feishu.text with handoff set). botChatAllowed is
// unchanged, so the chat does not become one the bot sends into.

const (
	// botSourceProxyReply is BotAISource.Kind of a handoff.
	botSourceProxyReply = "proxy_reply"

	botHandoffChatsMax    = 200
	botHandoffQuestionMax = 2000
	botHandoffContextMax  = 6000
	botHandoffLabelMax    = 80
	botHandoffRefMax      = 120
	botHandoffLauncherMax = 64
	botHandoffBodyMax     = 64 << 10

	// botHandoffKeepFor is how long a handoff nobody finished is kept: the source, the draft and the pending decision.
	botHandoffKeepFor = 6 * time.Hour
	// botHandoffRunningFor is how long a handoff counts as still running.
	botHandoffRunningFor = 10 * time.Minute

	botHandoffPerChatHour = 6
	botHandoffPerDay      = 60
	botHandoffRunning     = 3

	botHandoffDeliveryConfirm = assistantwire.FeishuHandoffDeliveryConfirm
	botHandoffDeliveryDirect  = assistantwire.FeishuHandoffDeliveryDirect

	botHandoffStateAccepted = assistantwire.FeishuHandoffStateAccepted

	// botHandoffCapability is what this build can do with a handoff, whatever the owner's policy.
	botHandoffCapability = "proxy_reply"

	// botHandoffKeyPrefix starts the idempotency key of every reply a handoff posts, so two tries land once.
	botHandoffKeyPrefix = "ho:"
)

// botHandoffPolicy is who the bot answers without asking first, and who it never answers.
type botHandoffPolicy struct {
	Off          bool     `json:"off,omitempty"`           // refuse every handoff
	DirectChats  []string `json:"direct_chats,omitempty"`  // the bot answers without asking first
	BlockedChats []string `json:"blocked_chats,omitempty"` // never; wins over everything
	Launcher     string   `json:"launcher,omitempty"`      // launcher of the guest run ("" the assistant's default)
}

type botHandoffView struct {
	Off          bool     `json:"off,omitempty"`
	DirectChats  []string `json:"direct_chats,omitempty"`
	BlockedChats []string `json:"blocked_chats,omitempty"`
	Launcher     string   `json:"launcher,omitempty"`
}

func (s FeishuBotSettings) handoffView() *botHandoffView {
	if s.Handoff == nil {
		return nil
	}
	h := s.Handoff
	return &botHandoffView{Off: h.Off, DirectChats: slices.Clone(h.DirectChats), BlockedChats: slices.Clone(h.BlockedChats), Launcher: h.Launcher}
}

// handoffDelivery says how a handoff for chatID is answered: direct, or confirm (the owner approves first). A refusal
// is the error code (handoff_off, chat_blocked); delivery is then "".
func (s FeishuBotSettings) handoffDelivery(chatID string) (delivery assistantwire.FeishuHandoffDelivery, refusal string) {
	if s.groupChatBlocked(chatID) {
		return "", "chat_blocked"
	}
	h := s.Handoff
	if h != nil && slices.Contains(h.BlockedChats, chatID) {
		return "", "chat_blocked"
	}
	if h != nil && h.Off {
		return "", "handoff_off"
	}
	if h != nil && slices.Contains(h.DirectChats, chatID) {
		return botHandoffDeliveryDirect, ""
	}
	return botHandoffDeliveryConfirm, ""
}

// botHandoffPatch is the keys PUT /api/v1/bots/feishu may carry under "handoff". An absent key leaves it; a list of []
// clears it.
type botHandoffPatch struct {
	Off          *bool     `json:"off"`
	DirectChats  *[]string `json:"direct_chats"`
	BlockedChats *[]string `json:"blocked_chats"`
	Launcher     *string   `json:"launcher"`
}

func (p botHandoffPatch) empty() bool {
	return p.Off == nil && p.DirectChats == nil && p.BlockedChats == nil && p.Launcher == nil
}

// checkHandoffChats is checkTopicChats' rule for one of the policy's lists: trimmed, no whitespace, no repeat, bounded.
func checkHandoffChats(list []string) ([]string, error) {
	if len(list) > botHandoffChatsMax {
		return nil, i18n.Err("hub-bots.handoff_chats_many", i18n.Args{"n": botHandoffChatsMax})
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, raw := range list {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 100 || strings.ContainsFunc(id, unicode.IsSpace) || seen[id] {
			return nil, i18n.Err("hub-bots.handoff_chat_bad")
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// setHandoffPolicy applies a PUT's "handoff" object (bots.go): validated before anything is written, saved under the
// lock, rolled back when the save fails, audited after the lock when it changed.
func (h *Manager) setHandoffPolicy(ctx context.Context, raw json.RawMessage, appID *string) error {
	var patch botHandoffPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return err
	}
	if patch.empty() {
		return nil
	}
	var direct, blocked []string
	var err error
	if patch.DirectChats != nil {
		if direct, err = checkHandoffChats(*patch.DirectChats); err != nil {
			return err
		}
	}
	if patch.BlockedChats != nil {
		if blocked, err = checkHandoffChats(*patch.BlockedChats); err != nil {
			return err
		}
	}
	var launcher string
	if patch.Launcher != nil {
		launcher = strings.TrimSpace(*patch.Launcher)
		if len(launcher) > botHandoffLauncherMax || strings.ContainsFunc(launcher, unicode.IsSpace) {
			return i18n.Err("hub-bots.handoff_launcher_bad")
		}
	}
	needsOwner := len(direct) > 0 || len(blocked) > 0
	if needsOwner {
		if err := h.topicChatsPossible(ctx, appID); err != nil {
			if errors.Is(err, errNoTopicOwner) {
				return i18n.Err("hub-bots.handoff_needs_owner")
			}
			return err
		}
	}
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	before := b.set.Handoff
	next := botHandoffPolicy{}
	if before != nil {
		next = *before
		next.DirectChats, next.BlockedChats = slices.Clone(before.DirectChats), slices.Clone(before.BlockedChats)
	}
	if patch.Off != nil {
		next.Off = *patch.Off
	}
	if patch.DirectChats != nil {
		next.DirectChats = direct
	}
	if patch.BlockedChats != nil {
		next.BlockedChats = blocked
	}
	if patch.Launcher != nil {
		next.Launcher = launcher
	}
	if handoffPolicyEqual(before, &next) {
		b.mu.Unlock()
		return nil
	}
	b.set.Handoff = &next
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.Handoff = before
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()
	h.auditBot(ctx, botRecord{auditBotHandoffPolicy, map[string]any{
		"off": next.Off, "direct_chats": next.DirectChats, "blocked_chats": next.BlockedChats, "launcher": next.Launcher,
	}})
	return nil
}

func handoffPolicyEqual(a, b *botHandoffPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Off == b.Off && a.Launcher == b.Launcher && slices.Equal(a.DirectChats, b.DirectChats) && slices.Equal(a.BlockedChats, b.BlockedChats)
}

// ---- the public identity ----

// botIdentityView is the paired bot's public identity. A dedicated type, never feishuBotView with fields blanked, so a
// field added to the settings view cannot leak into it.
type botIdentityView struct {
	Enabled      bool     `json:"enabled"`
	Paired       bool     `json:"paired"`
	Ready        bool     `json:"ready"`
	AppID        string   `json:"app_id,omitempty"`
	BotOpenID    string   `json:"bot_open_id,omitempty"`
	BotName      string   `json:"bot_name,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// botIdentity is GET /api/v1/bots/feishu/identity. The open id is read once more, outside the lock, when the cache is
// empty and the bot is on.
func (h *Manager) botIdentity(ctx context.Context) botIdentityView {
	b := &h.bots
	b.mu.Lock()
	appID, _, domain, _, _ := h.botCredentialsLocked()
	v := botIdentityView{Enabled: b.set.Enabled, Paired: b.set.Owner != nil, Ready: b.api != nil,
		Capabilities: []string{botHandoffCapability}}
	if appID != "" {
		v.AppID, v.Domain = appID, botcard.ResolveDomain(domain)
	}
	v.BotName, v.BotOpenID = b.info.Name, b.info.OpenID
	api := b.api
	b.mu.Unlock()
	if v.BotOpenID == "" && api != nil && v.Enabled {
		v.BotOpenID = h.botOpenID(api)
	}
	return v
}

// ---- taking a handoff ----

type botHandoffRequest struct {
	ChatID     string `json:"chat_id"`
	MessageID  string `json:"message_id"`
	InThread   bool   `json:"in_thread"`
	Ref        string `json:"ref"`
	ChatName   string `json:"chat_name"`
	SenderName string `json:"sender_name"`
	Question   string `json:"question"`
	Context    string `json:"context"`
}

type botHandoffError struct {
	status int
	code   string
	err    error
	retry  int // Retry-After, seconds; 0 sends none
}

func (e *botHandoffError) Error() string { return e.err.Error() }

var botHandoffRefPattern = lazyre.New(`^[A-Za-z0-9:._-]+$`)

// handoffInvalid refuses one field of a handoff.
func handoffInvalid(field string) *botHandoffError {
	return &botHandoffError{http.StatusBadRequest, "invalid_handoff", i18n.Err("hub-bots.handoff_invalid", i18n.Args{"field": field}), 0}
}

// checkHandoffRequest trims and bounds the request. Labels are clipped; question and context over their limit are refused.
func checkHandoffRequest(in *botHandoffRequest) *botHandoffError {
	in.ChatID = strings.TrimSpace(in.ChatID)
	in.MessageID = strings.TrimSpace(in.MessageID)
	in.Ref = strings.TrimSpace(in.Ref)
	switch {
	case in.ChatID == "" || len(in.ChatID) > 100 || strings.ContainsFunc(in.ChatID, unicode.IsSpace):
		return handoffInvalid("chat_id")
	case in.MessageID != "" && (len(in.MessageID) > 100 || strings.ContainsFunc(in.MessageID, unicode.IsSpace)):
		return handoffInvalid("message_id")
	case in.InThread && in.MessageID == "":
		return handoffInvalid("in_thread")
	case in.Ref != "" && (len(in.Ref) > botHandoffRefMax || !botHandoffRefPattern.MatchString(in.Ref)):
		return handoffInvalid("ref")
	}
	in.ChatName = handoffLabel(in.ChatName)
	in.SenderName = handoffLabel(in.SenderName)
	in.Question = strings.TrimSpace(in.Question)
	in.Context = strings.TrimSpace(in.Context)
	switch {
	case in.Question == "" || utf8.RuneCountInString(in.Question) > botHandoffQuestionMax:
		return handoffInvalid("question")
	case utf8.RuneCountInString(in.Context) > botHandoffContextMax:
		return handoffInvalid("context")
	}
	return nil
}

// handoffLabel clips a caller-supplied label: one line, no terminal escapes, at most botHandoffLabelMax runes.
func handoffLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > botHandoffLabelMax {
		s = string([]rune(s)[:botHandoffLabelMax])
	}
	return s
}

// handoffCaller is who handed a question over, for the source and the audit: the extension, else the device.
func handoffCaller(ctx context.Context) string {
	p := principalOf(ctx)
	if p.Extension != "" {
		return "extension:" + p.Extension
	}
	return actorOf(ctx)
}

// handoffRefSum is the replay key: the caller, the ref and a fingerprint of the payload.
func handoffRefSum(caller, ref string, in botHandoffRequest) string {
	if ref == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{caller, ref, in.ChatID, in.MessageID, in.Question, in.Context,
		in.ChatName, in.SenderName, boolText(in.InThread)}, "\n")))
	return hex.EncodeToString(sum[:])
}

func boolText(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// handoffSource is the proxy reply of that id, nil when none was taken or it is past botHandoffKeepFor.
func (s FeishuBotSettings) handoffSource(id string, now time.Time) *BotAISource {
	if id == "" {
		return nil
	}
	for i := range s.AISources {
		src := &s.AISources[i]
		if src.Kind == botSourceProxyReply && src.HandoffID == id && now.Sub(src.At) <= botHandoffKeepFor {
			return src
		}
	}
	return nil
}

// handoffByRef is the proxy reply this caller already took under refSum, nil when none matches.
func (s FeishuBotSettings) handoffByRef(refSum string, now time.Time) *BotAISource {
	if refSum == "" {
		return nil
	}
	for i := range s.AISources {
		src := &s.AISources[i]
		if src.Kind == botSourceProxyReply && src.RefSum == refSum && now.Sub(src.At) <= botHandoffKeepFor {
			return src
		}
	}
	return nil
}

// handoffRefOther is a kept handoff of this caller and ref whose payload differs.
func (s FeishuBotSettings) handoffRefOther(caller, ref, sum string, now time.Time) bool {
	if ref == "" {
		return false
	}
	for i := range s.AISources {
		src := &s.AISources[i]
		if src.Kind == botSourceProxyReply && src.By == caller && src.Ref == ref && src.RefSum != sum && now.Sub(src.At) <= botHandoffKeepFor {
			return true
		}
	}
	return false
}

// handoffLimits says why chatID may not take another handoff now: "chat", "day" or "running"; "" when it may.
func (s FeishuBotSettings) handoffLimits(chatID string, now time.Time) string {
	hour, day, running := 0, 0, 0
	for i := range s.AISources {
		src := s.AISources[i]
		if src.Kind != botSourceProxyReply {
			continue
		}
		age := now.Sub(src.At)
		if age <= 24*time.Hour {
			day++
		}
		if src.ChatID == chatID && age <= time.Hour {
			hour++
		}
		if src.Ended == "" && age <= botHandoffRunningFor {
			running++
		}
	}
	switch {
	case hour >= botHandoffPerChatHour:
		return "chat"
	case day >= botHandoffPerDay:
		return "day"
	case running >= botHandoffRunning:
		return "running"
	}
	return ""
}

// takeHandoffSource keeps the handoff as a source the bot may answer (the caller's lock is not held). A failed save
// leaves the settings as they were.
func (h *Manager) takeHandoffSource(src BotAISource) error {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock().UTC()
	kept := slices.DeleteFunc(slices.Clone(b.set.AISources), func(s BotAISource) bool {
		keep := botAISourcesKeepFor
		if s.Kind == botSourceProxyReply {
			keep = botHandoffKeepFor
		}
		return now.Sub(s.At) > keep
	})
	kept = append(kept, src)
	if len(kept) > botAISourcesKeep {
		kept = kept[len(kept)-botAISourcesKeep:]
	}
	before := b.set.AISources
	b.set.AISources = kept
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.AISources = before
		return err
	}
	return nil
}

// dropHandoffSource forgets a handoff the assistant did not accept, so a refused one never counts.
func (h *Manager) dropHandoffSource(id string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	before := b.set.AISources
	next := slices.DeleteFunc(slices.Clone(before), func(s BotAISource) bool {
		return s.Kind == botSourceProxyReply && s.HandoffID == id
	})
	if len(next) == len(before) {
		return
	}
	b.set.AISources = next
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.AISources = before
	}
}

// handoffAnchorChat is the chat an anchor message is in, read from the open platform. A message that cannot be read,
// or that names no chat, refuses the handoff.
func (h *Manager) handoffAnchorChat(ctx context.Context, api botport.Client, messageID string) (string, error) {
	items, err := api.Message(ctx, messageID)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.MessageID == messageID && it.ChatID != "" {
			return it.ChatID, nil
		}
	}
	return "", errors.New("no chat")
}

// postHandoff is POST /api/v1/bots/feishu/handoffs, in the order the contract fixes.
func (h *Manager) postHandoff(w http.ResponseWriter, r *http.Request) {
	if answerOnly(r) {
		writeErrCode(w, http.StatusForbidden, "permission_denied", i18n.Err("hub-bots.handoff_invalid", i18n.Args{"field": "caller"}))
		return
	}
	var in botHandoffRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, botHandoffBodyMax)).Decode(&in); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid_handoff", i18n.Err("hub-bots.handoff_invalid", i18n.Args{"field": "body"}))
		return
	}
	if e := checkHandoffRequest(&in); e != nil {
		handoffWriteErr(w, e)
		return
	}
	api, set := h.botAPI()
	if api == nil {
		writeErrCode(w, http.StatusConflict, "bot_off", errBotOff)
		return
	}
	delivery, refusal := set.handoffDelivery(in.ChatID)
	if refusal != "" {
		h.auditBot(r.Context(), botRecord{auditBotHandoffRefused, map[string]any{"chat_id": in.ChatID, "reason": map[string]string{"chat_blocked": "blocked", "handoff_off": "off"}[refusal]}})
		status, key := http.StatusForbidden, "hub-bots.handoff_chat_blocked"
		if refusal == "handoff_off" {
			key = "hub-bots.handoff_off"
		}
		writeErrCode(w, status, refusal, i18n.Err(key))
		return
	}
	caller := handoffCaller(r.Context())
	now := h.bots.clock().UTC()
	if in.Ref != "" {
		sum := handoffRefSum(caller, in.Ref, in)
		if src := set.handoffByRef(sum, now); src != nil {
			writeJSON(w, assistantwire.FeishuHandoffResponse{Handoff: handoffAccepted(src.HandoffID, handoffDeliveryOf(src), src.At.Add(botHandoffKeepFor)), Replayed: true})
			return
		}
		if set.handoffRefOther(caller, in.Ref, sum, now) {
			handoffWriteErr(w, &botHandoffError{http.StatusConflict, "handoff_conflict", i18n.Err("hub-bots.handoff_conflict"), 0})
			return
		}
	}
	if why := set.handoffLimits(in.ChatID, now); why != "" {
		retry := 60
		if why == "day" {
			retry = 3600
		}
		handoffWriteErr(w, &botHandoffError{http.StatusTooManyRequests, "handoff_rate_limited", i18n.Err("hub-bots.handoff_rate_limited"), retry})
		return
	}
	if in.MessageID != "" {
		chat, err := h.handoffAnchorChat(r.Context(), api, in.MessageID)
		if err != nil || chat != in.ChatID {
			handoffWriteErr(w, handoffInvalid("message_id"))
			return
		}
	}
	id := "bh_" + handoffHex(8)
	src := BotAISource{ChatID: in.ChatID, MessageID: in.MessageID, At: now, Kind: botSourceProxyReply, HandoffID: id,
		Confirm: delivery == botHandoffDeliveryConfirm, InThread: in.InThread, By: caller, Ref: in.Ref, RefSum: handoffRefSum(caller, in.Ref, in)}
	if err := h.takeHandoffSource(src); err != nil {
		writeErrCode(w, http.StatusServiceUnavailable, "assistant_unavailable", errBotSettingsUnread)
		return
	}
	expires := now.Add(botHandoffKeepFor)
	payload := assistantwire.FeishuHandoff{ID: assistantwire.FeishuHandoffID(id), ChatID: assistantwire.FeishuChatID(in.ChatID), ChatName: in.ChatName, SenderName: in.SenderName,
		Question: in.Question, Context: in.Context, Confirm: src.Confirm, Launcher: set.handoffLauncher(), By: caller, ExpiresAt: expires}
	if err := h.callAssistantWaiting(assistantwire.MethodFeishuHandoff, payload, nil, time.Now().Add(inboundWait), 20*time.Second); err != nil {
		h.dropHandoffSource(id)
		writeErrCode(w, http.StatusServiceUnavailable, "assistant_unavailable", i18n.Err("hub-bots.handoff_assistant_away"))
		return
	}
	h.audit(r.Context(), channel.Feishu, auditBotHandoffTaken, caller, map[string]any{
		"chat_id": in.ChatID, "handoff_id": id, "delivery": delivery, "anchored": in.MessageID != ""})
	writeJSON(w, assistantwire.FeishuHandoffResponse{Handoff: handoffAccepted(id, delivery, expires)})
}

func (s FeishuBotSettings) handoffLauncher() string {
	if s.Handoff == nil {
		return ""
	}
	return s.Handoff.Launcher
}

func handoffDeliveryOf(src *BotAISource) assistantwire.FeishuHandoffDelivery {
	if src.Confirm {
		return botHandoffDeliveryConfirm
	}
	return botHandoffDeliveryDirect
}

func handoffAccepted(id string, delivery assistantwire.FeishuHandoffDelivery, expires time.Time) assistantwire.FeishuHandoffAccepted {
	return assistantwire.FeishuHandoffAccepted{ID: assistantwire.FeishuHandoffID(id), State: botHandoffStateAccepted, Delivery: delivery,
		ExpiresAt: expires.UTC().Format(time.RFC3339)}
}

func handoffWriteErr(w http.ResponseWriter, e *botHandoffError) {
	if e.retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.retry))
	}
	writeErrCode(w, e.status, e.code, e.err)
}

// handoffHex is n random bytes as hex (a handoff id's tail).
func handoffHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- delivering the reply ----

// handoffText delivers a feishu.text that names a handoff: the usual path is skipped entirely. The reply goes under the
// anchor when the handoff has one (never a detour into the chat) and into the chat when it has none, once.
func (h *Manager) handoffText(ctx context.Context, api botport.Client, set FeishuBotSettings, a struct {
	ChatID, ReplyTo, Text, Handoff string
	Confirmed, FullAnswer          bool
}) (any, error) {
	b := &h.bots
	b.mu.Lock()
	now := b.clock().UTC()
	set = b.set.copy()
	b.mu.Unlock()
	src := set.handoffSource(a.Handoff, now)
	switch {
	case src == nil || src.Ended != "":
		return nil, errBotSourceReply
	case a.ChatID != "" && a.ChatID != src.ChatID:
		return nil, errBotSourceReply
	case a.ReplyTo != "" && a.ReplyTo != src.MessageID:
		return nil, errBotSourceReply
	case a.FullAnswer || utf8.RuneCountInString(a.Text) > botSourceTextMax:
		return nil, errBotSourceReply
	case src.Confirm && !a.Confirmed:
		return nil, errBotSourceReply
	}
	if _, refusal := set.handoffDelivery(src.ChatID); refusal != "" {
		return nil, errBotSourceReply // blocked or switched off since it was taken
	}
	if !h.useHandoffReply(a.Handoff) {
		return nil, errBotSourceReply
	}
	key := botHandoffKeyPrefix + a.Handoff
	var id string
	var err error
	if src.MessageID != "" {
		// Never a detour into the chat: a reply whose anchor is gone is lost.
		id, err = api.Reply(ctx, src.MessageID, botcard.TypeText, botcard.TextContent(a.Text), key, src.InThread)
	} else {
		id, err = api.Send(ctx, "chat_id", src.ChatID, botcard.TypeText, botcard.TextContent(a.Text), key)
	}
	if err != nil {
		return nil, feishuCoded(err)
	}
	h.finishHandoff(a.Handoff, "posted")
	h.audit(ctx, channel.Feishu, auditBotHandoffPosted, src.By, map[string]any{"chat_id": src.ChatID, "handoff_id": a.Handoff, "confirmed": a.Confirmed})
	return map[string]any{"message_id": id}, nil
}

// useHandoffReply counts a try (at most botSourceReplies) before it is made. A failed send still counts.
func (h *Manager) useHandoffReply(id string) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.set.handoffSource(id, b.clock().UTC())
	if src == nil || src.Ended != "" || src.Replies >= src.repliesMax() {
		return false
	}
	src.Replies++
	h.saveBotSettingsLocked()
	return true
}

// finishHandoff marks a handoff ended and uses up its tries, so nothing more is posted for it.
func (h *Manager) finishHandoff(id, outcome string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.set.handoffSource(id, b.clock().UTC())
	if src == nil || src.Ended != "" {
		return
	}
	src.Ended = outcome
	src.Replies = src.repliesMax()
	h.saveBotSettingsLocked()
}

// endHandoff is feishu.handoff_end: the assistant says a handoff is over without a post.
func (h *Manager) endHandoff(ctx context.Context, id, outcome string) error {
	switch outcome {
	case "declined", "expired", "no_reply", "failed":
	default:
		return &rpc.Error{Code: "invalid_params", Message: "feishu.handoff_end: outcome must be declined, expired, no_reply or failed"}
	}
	now := h.bots.clock().UTC()
	b := &h.bots
	b.mu.Lock()
	src := b.set.handoffSource(id, now)
	if src == nil {
		b.mu.Unlock()
		return errBotSourceReply
	}
	chat, by, ended := src.ChatID, src.By, src.Ended
	b.mu.Unlock()
	if ended != "" {
		return nil // already over: saying so again changes nothing
	}
	h.finishHandoff(id, outcome)
	h.audit(ctx, channel.Feishu, auditBotHandoffEnded, by, map[string]any{"chat_id": chat, "handoff_id": id, "outcome": outcome})
	return nil
}
