package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// BotMux structured turn endpoint contract (deepcoldy/botmux#3597, #3599):
// GET /api/sessions/:sessionId/turns?since=<cursor>&limit=<n> returns BotmuxTurnsResponse. BotmuxCapOwnerUnionID: its
// /api/trigger also takes `actorUnionId` (a union_id) with `actor: "owner"`, and refuses one that is not the bot's owner
// with 403 owner_unresolved / owner_mismatch.
const (
	BotmuxTurnsSchema     = "botmux.session-turns.v1"
	BotmuxCapOwnerActor   = "ownerActor"
	BotmuxCapOwnerUnionID = "ownerUnionId"
)

// BotmuxTurnsResponse is the JSON payload of BotMux's read-only GET /api/sessions/:id/turns endpoint.
type BotmuxTurnsResponse struct {
	OK           bool         `json:"ok"`
	Schema       string       `json:"schema"`
	Capabilities []string     `json:"capabilities"`
	Next         string       `json:"next"`
	Turns        []BotmuxTurn `json:"turns"`
}

// BotmuxTurn is one structured turn record from BotMux.
type BotmuxTurn struct {
	TurnID  string              `json:"turnId"`
	Seq     int64               `json:"seq"`
	At      int64               `json:"at"`
	Inbound []BotmuxTurnInbound `json:"inbound"`
	Replies []BotmuxTurnReply   `json:"replies"`
	Anchor  *BotmuxTurnAnchor   `json:"anchor,omitempty"`
}

// BotmuxTurnInbound is one inbound message folded into a BotMux turn.
type BotmuxTurnInbound struct {
	MessageID       string                 `json:"messageId"`
	Text            string                 `json:"text"`
	Sender          *BotmuxTurnSender      `json:"sender,omitempty"`
	Via             string                 `json:"via"` // "lark" | "trigger" | "owner_actor"
	QuotedMessageID string                 `json:"quotedMessageId,omitempty"`
	Attachments     []BotmuxTurnAttachment `json:"attachments,omitempty"`
	Mentions        []BotmuxTurnMention    `json:"mentions,omitempty"`
	Placement       *BotmuxTurnPlacement   `json:"placement,omitempty"`
}

// BotmuxTurnSender identifies the sender in BotMux's record. OpenID is parsed only so it can be ignored;
// only display names leave the node.
type BotmuxTurnSender struct {
	Type   string `json:"type"`
	OpenID string `json:"openId,omitempty"`
	Name   string `json:"name,omitempty"`
}

// BotmuxTurnAttachment is safe attachment metadata in BotMux's record.
type BotmuxTurnAttachment struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// BotmuxTurnMention is a mentioned user in BotMux's record. OpenID is never forwarded.
type BotmuxTurnMention struct {
	OpenID string `json:"openId,omitempty"`
	Name   string `json:"name,omitempty"`
}

// BotmuxTurnPlacement describes whether the message landed in a topic ("thread") or directly in the chat ("chat").
type BotmuxTurnPlacement struct {
	Kind          string `json:"kind"` // "thread" | "chat"
	RootMessageID string `json:"rootMessageId,omitempty"`
}

// BotmuxTurnReply is one confirmed reply posted by BotMux during the turn.
type BotmuxTurnReply struct {
	MessageID    string `json:"messageId"`
	ResponseKind string `json:"responseKind"`
	At           int64  `json:"at"`
	Text         string `json:"text,omitempty"`
}

// BotmuxTurnAnchor links a BotMux turn to the CLI transcript entry it was delivered to.
type BotmuxTurnAnchor struct {
	CLIID        string `json:"cliId"`
	CLISessionID string `json:"cliSessionId,omitempty"`
	InputSha256  string `json:"inputSha256"`
	CLIEntryID   string `json:"cliEntryId,omitempty"`
}

// HasCapability reports whether the turns response advertises cap.
func (r BotmuxTurnsResponse) HasCapability(cap string) bool {
	return r.OK && r.Schema == BotmuxTurnsSchema && slices.Contains(r.Capabilities, cap)
}

// ApplyBotmuxTurns overlays structured BotMux turn records onto transcript items.
// User items are matched by anchor.cliEntryId first, all of them, then the rest by an unambiguous anchor.inputSha256.
// Matched turns populate Item.Botmux for the user message and for the turn's `botmux send` replies
// (including file/card replies whose text was not on the command line). Unmatched turns are left
// untouched so wrapper parsing from markBotmux acts as a per-turn fallback.
// Item.Text is never modified, and no open_id or messageId ever leaves the node.
func ApplyBotmuxTurns(items []proto.Item, turns []BotmuxTurn, transcriptSessionID string) []proto.Item {
	if len(items) == 0 || len(turns) == 0 {
		return items
	}
	byEntry := map[string]int{}
	byHash := map[string][]int{}
	for i := range turns {
		a := turns[i].Anchor
		if a == nil {
			continue
		}
		if id := strings.TrimSpace(a.CLISessionID); id != "" && id != transcriptSessionID {
			continue
		}
		if id := strings.TrimSpace(a.CLIEntryID); id != "" {
			byEntry[id] = i
		}
		if h := strings.ToLower(strings.TrimSpace(a.InputSha256)); h != "" {
			byHash[h] = append(byHash[h], i)
		}
	}
	if len(byEntry) == 0 && len(byHash) == 0 {
		return items
	}

	used := make([]bool, len(turns))
	var out []proto.Item
	clone := func() []proto.Item {
		if out == nil {
			out = append([]proto.Item(nil), items...)
		}
		return out
	}

	// Index tool results by ToolUseID so reply matching only considers completed, successful calls.
	resultsByToolUse := map[string]*proto.Item{}
	for i := range items {
		if items[i].Kind == proto.KindToolResult && items[i].ToolUseID != "" {
			resultsByToolUse[items[i].ToolUseID] = &items[i]
		}
	}

	// Find all user text item indices (turn boundaries).
	var userIdx []int
	for i := range items {
		it := &items[i]
		if it.Role == "user" && it.Kind == proto.KindText && it.From == "" {
			userIdx = append(userIdx, i)
		}
	}

	entryKeys := func(id string) []string {
		if id == "" {
			return nil
		}
		if prefix, _, found := strings.Cut(id, "#"); found && prefix != "" {
			return []string{id, prefix}
		}
		return []string{id}
	}

	// Entry IDs are matched first for every user item, so a turn that names its own entry is never claimed through
	// an earlier item's identical input hash.
	matched := make([]int, len(userIdx))
	loadedEntries := map[string]bool{}
	for ui, start := range userIdx {
		matched[ui] = -1
		for _, key := range entryKeys(items[start].ID) {
			loadedEntries[key] = true
			if idx, ok := byEntry[key]; ok && !used[idx] && matched[ui] < 0 {
				matched[ui] = idx
				used[idx] = true
			}
		}
	}
	// The hash fallback only takes a turn whose entry is not loaded here, and only when both the turn and the
	// unmatched user item are the single ones with that hash; anything else keeps the wrapper decoding.
	unmatchedByHash := map[string]int{}
	for ui, start := range userIdx {
		if matched[ui] < 0 {
			for _, candidate := range itemInputHashCandidates(items[start]) {
				unmatchedByHash[candidate]++
			}
		}
	}
	for ui, start := range userIdx {
		if matched[ui] >= 0 {
			continue
		}
		for _, candidate := range itemInputHashCandidates(items[start]) {
			idxs := byHash[candidate]
			if len(idxs) != 1 || used[idxs[0]] || unmatchedByHash[candidate] != 1 {
				continue
			}
			if entry := strings.TrimSpace(turns[idxs[0]].Anchor.CLIEntryID); entry != "" && loadedEntries[entry] {
				continue
			}
			matched[ui] = idxs[0]
			used[idxs[0]] = true
			break
		}
	}

	for ui, start := range userIdx {
		tIdx := matched[ui]
		if tIdx < 0 {
			continue // per-turn fallback: keep wrapper-parsed Botmux as is
		}
		turn := turns[tIdx]

		end := len(items)
		if ui+1 < len(userIdx) {
			end = userIdx[ui+1]
		}

		if bm := botmuxFromInbound(turn.Inbound, items[start].Botmux); bm != nil {
			dst := clone()
			dst[start].Botmux = bm
		}

		if len(turn.Replies) == 0 {
			continue
		}
		repliesByID := make(map[string]BotmuxTurnReply, len(turn.Replies))
		for _, reply := range turn.Replies {
			if reply.MessageID != "" {
				repliesByID[reply.MessageID] = reply
			}
		}
		for j := start + 1; j < end; j++ {
			it := &items[j]
			if it.Kind != proto.KindToolUse {
				continue
			}
			result := resultsByToolUse[it.ToolUseID]
			if !botmuxToolResultSucceeded(result) {
				continue
			}
			sendCount := countBotmuxSendInvocations(botmuxCommand(it.ToolName, it.ToolInput))
			if sendCount == 0 {
				continue
			}
			messageIDs := botmuxSendReceiptIDs(result.Text)
			if len(messageIDs) > sendCount {
				continue
			}
			var replies []BotmuxTurnReply
			for _, messageID := range messageIDs {
				if reply, ok := repliesByID[messageID]; ok {
					replies = append(replies, reply)
					delete(repliesByID, messageID)
				}
			}
			if bm := botmuxFromReplies(replies, it.Botmux); bm != nil {
				dst := clone()
				dst[j].Botmux = bm
			}
		}
	}

	if out == nil {
		return items
	}
	return out
}

func botmuxSendReceiptIDs(text string) []string {
	var messageIDs []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var receipt struct {
			Success   bool   `json:"success"`
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal([]byte(line), &receipt) == nil && receipt.Success && receipt.MessageID != "" {
			messageIDs = append(messageIDs, receipt.MessageID)
		}
	}
	return messageIDs
}

func itemInputHashCandidates(it proto.Item) []string {
	if it.RawTextSHA256 != "" {
		if it.RawTextTrimmedSHA256 != "" && it.RawTextTrimmedSHA256 != it.RawTextSHA256 {
			return []string{it.RawTextSHA256, it.RawTextTrimmedSHA256}
		}
		return []string{it.RawTextSHA256}
	}
	return inputHashCandidates(it.Text)
}

func inputHashCandidates(text string) []string {
	if text == "" {
		return nil
	}
	h1 := sha256Hex(text)
	trimmed := strings.TrimRight(text, "\r\n")
	if trimmed == text {
		return []string{h1}
	}
	return []string{h1, sha256Hex(trimmed)}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func botmuxFromInbound(inbound []BotmuxTurnInbound, existing *proto.Botmux) *proto.Botmux {
	if len(inbound) == 0 {
		return nil
	}
	b := &proto.Botmux{Kind: proto.BotmuxMessage}
	allTrigger := true
	var parts []string
	var mentions []string
	var meta []proto.BotmuxAttachment
	for _, m := range inbound {
		if m.Via != "trigger" {
			allTrigger = false
		}
		if t := strings.TrimSpace(m.Text); t != "" {
			parts = append(parts, t)
		}
		if b.Sender == "" && m.Sender != nil {
			// Display name only: never fall back to openId.
			if name := strings.TrimSpace(m.Sender.Name); name != "" {
				b.Sender = name
				b.SenderType = proto.BotmuxSenderType(strings.TrimSpace(m.Sender.Type))
			}
		}
		if strings.TrimSpace(m.QuotedMessageID) != "" {
			b.Quoted = true
		}
		if m.Placement != nil && m.Placement.Kind == "thread" {
			b.Thread = true
		}
		for _, a := range m.Attachments {
			name := strings.TrimSpace(a.Name)
			if name != "" {
				name = path.Base(strings.ReplaceAll(name, "\\", "/"))
				if name == "." || name == "/" {
					name = ""
				}
			}
			meta = append(meta, proto.BotmuxAttachment{
				Kind:     proto.BotmuxAttachmentKind(strings.TrimSpace(a.Kind)),
				Name:     name,
				MimeType: proto.MIMEType(strings.TrimSpace(a.MimeType)),
			})
		}
		for _, mn := range m.Mentions {
			// Display name only: never include openId.
			if name := strings.TrimSpace(mn.Name); name != "" && !slices.Contains(mentions, name) {
				mentions = append(mentions, name)
			}
		}
	}
	if allTrigger {
		b.Kind = proto.BotmuxTrigger
		if existing != nil && existing.Source != "" {
			b.Source = existing.Source
		}
	}
	if existing != nil && existing.TopicContext {
		b.TopicContext = true
	}
	b.Text = strings.Join(parts, "\n\n")
	if len(meta) > 0 {
		b.Attachments = len(meta)
		b.AttachmentMeta = meta
	}
	if len(mentions) > 0 {
		b.Mentions = mentions
	}
	if b.Text == "" && b.Attachments == 0 && !b.Quoted {
		return nil
	}
	return b
}

func botmuxFromReply(r BotmuxTurnReply, existing *proto.Botmux) *proto.Botmux {
	text := strings.TrimSpace(r.Text)
	if text == "" && existing != nil {
		text = existing.Text
	}
	final := r.ResponseKind == "final"
	if r.ResponseKind == "" && existing != nil {
		final = existing.Final
	}
	var voice bool
	var attachments int
	if existing != nil {
		voice = existing.Voice
		attachments = existing.Attachments
	}
	if text == "" && !voice && attachments == 0 {
		return nil
	}
	return &proto.Botmux{
		Kind:        proto.BotmuxReply,
		Text:        text,
		Final:       final,
		Voice:       voice,
		Attachments: attachments,
	}
}

func botmuxFromReplies(replies []BotmuxTurnReply, existing *proto.Botmux) *proto.Botmux {
	if len(replies) == 0 {
		return nil
	}
	if len(replies) == 1 {
		return botmuxFromReply(replies[0], existing)
	}
	var parts []string
	final := false
	for _, r := range replies {
		if t := strings.TrimSpace(r.Text); t != "" {
			parts = append(parts, t)
		}
		if r.ResponseKind == "final" {
			final = true
		}
	}
	text := strings.Join(parts, "\n\n")
	if text == "" && existing != nil {
		text = existing.Text
	}
	var voice bool
	var attachments int
	if existing != nil {
		voice = existing.Voice
		attachments = existing.Attachments
	}
	if text == "" && !voice && attachments == 0 {
		return nil
	}
	return &proto.Botmux{
		Kind:        proto.BotmuxReply,
		Text:        text,
		Final:       final,
		Voice:       voice,
		Attachments: attachments,
	}
}

// countBotmuxSendInvocations counts the `botmux send` invocations in cmd BotMux would carry out (not --help, -h,
// --dry-run, an unknown flag or a flag missing its value), including forms whose body is not on the command line
// (--content-file, --card-file, --card-json, heredocs, pipes). It reads cmd through botmuxSendCalls like BotmuxSend
// does, so leading `cd … &&` and NAME=value prefixes, chains, blocks and comments are taken apart the same way, and a
// quoted word, a comment or a here-document's body is never counted as a command.
func countBotmuxSendInvocations(cmd string) int {
	if !strings.Contains(cmd, "botmux") || !strings.Contains(cmd, "send") {
		return 0
	}
	calls, ok := botmuxSendCalls(cmd)
	if !ok {
		return 0
	}
	count := 0
	for _, words := range calls {
		if isValidBotmuxSendArgs(words[2:]) {
			count++
		}
	}
	return count
}

func isValidBotmuxSendArgs(words []string) bool {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "-h" || w == "--" {
			return false
		}
		if !strings.HasPrefix(w, "--") {
			continue
		}
		name, value, eq := strings.Cut(w, "=")
		switch botmuxSendFlags[name] {
		case sendFlagBool:
			if eq {
				return false
			}
		case sendFlagOptional:
		case sendFlagValue, sendFlagFileValue:
			if !eq {
				if i+1 >= len(words) || words[i+1] == "-h" || strings.HasPrefix(words[i+1], "--") {
					return false
				}
				i++
				value = words[i]
			}
			if value == "" {
				return false
			}
		default: // sendFlagGiveUp (--help, --dry-run) or unknown flag
			return false
		}
	}
	return true
}
