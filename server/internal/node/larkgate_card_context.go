package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// larkCardContextAgent identifies the node's own read-only metadata lookup for approval card summaries,
	// parallel to portProbeAgent.
	larkCardContextAgent = "persona-card-context"
	// larkCardContextMaxBytes caps one metadata lookup response body.
	larkCardContextMaxBytes = 256 << 10
	// larkCardMaxPathIDLen caps accepted path IDs for clean rendering and metadata lookups.
	larkCardMaxPathIDLen = 128
)

// larkCardContextTimeout bounds one call's read-only metadata lookup. It is a variable so slow-environment tests
// can widen it for shell subprocess fixtures or tighten it for timeout verification.
var larkCardContextTimeout = 1500 * time.Millisecond

var larkContextHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// larkCardContext is the human context of one gated message write. OK is false when upstream metadata could
// not be read; larkMessageSummary can still render a clean summary for unambiguous message payloads using the
// target id, while ambiguous, dynamic, query-bearing, or extra-field calls fall back to the raw summary.
type larkCardContext struct {
	OK          bool
	Chat        string
	ReplySender string
	ReplyText   string
	InTopic     bool
}

type larkParsedMessage struct {
	Text     string
	Mentions []string
	InTopic  bool
	ReplyTo  string
}

// larkContextFromGate reads only the message's own conversation and, for a reply, the message it answers through
// the call's configured command gate (direct authenticated HTTP or hub gate.relay). It is a variable so tests can
// substitute a fake reader; no gate decision depends on it.
var larkContextFromGate = func(ctx context.Context, n *Node, cfg proto.AssistantLark, c larkCall) larkCardContext {
	if c.CLI || c.Gate < 0 || c.Gate >= len(cfg.Gates) {
		return larkCardContext{}
	}
	trimmed := strings.Trim(c.Path, "/")
	if !strings.HasPrefix(trimmed, "conversations/") && !strings.HasPrefix(trimmed, "messages/") {
		return larkCardContext{}
	}
	kind, chatID, messageID := larkMessageTarget(c)
	if kind == "" {
		return larkCardContext{}
	}
	if chatID != "" && !safeGatePathID(chatID) {
		return larkCardContext{}
	}
	if messageID != "" && !safeGatePathID(messageID) {
		return larkCardContext{}
	}
	g := cfg.Gates[c.Gate]
	if !g.Relay && g.Auth == nil {
		return larkCardContext{}
	}
	read := func(resource, id string) ([]byte, bool) {
		if !safeGatePathID(id) {
			return nil, false
		}
		logicalPath := "/" + resource + "/" + id
		call := larkCall{Gate: c.Gate, Method: http.MethodGet, Path: logicalPath}
		// This node-initiated lookup needs an explicit allowing rule; the unmatched
		// default for ordinary user-initiated reads does not authorize it.
		if action, ok := matchLark(g.Rules, call); !ok || action != "allow" {
			return nil, false
		}
		return gateContextRead(ctx, n, g, resource, id)
	}
	return resolveGateCardContext(kind, chatID, messageID, read)
}

func normalizeGatePathPrefix(raw string) string {
	trimmed := strings.Trim(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

func gateContextRead(ctx context.Context, n *Node, g proto.CommandGate, resource, id string) ([]byte, bool) {
	prefix := normalizeGatePathPrefix(g.PathPrefix)
	escapedSeg := "/" + resource + "/" + url.PathEscape(id)
	if g.Relay {
		if n == nil || n.peer == nil {
			return nil, false
		}
		relayPath := (&url.URL{Path: prefix}).EscapedPath() + escapedSeg
		params := proto.GateRelayParams{
			Command: g.Command,
			Method:  http.MethodGet,
			Path:    relayPath,
			Header:  map[string][]string{"Accept": {"application/json"}},
		}
		var ans proto.GateRelayAnswer
		if err := n.peer.Call(ctx, proto.MethodGateRelay, params, &ans); err != nil {
			return nil, false
		}
		if ans.Status < 200 || ans.Status >= 300 || len(ans.Body) == 0 || len(ans.Body) > larkCardContextMaxBytes {
			return nil, false
		}
		return ans.Body, true
	}
	if g.BaseEnv == "" || g.Auth == nil {
		return nil, false
	}
	up, err := url.Parse(strings.TrimSpace(g.Env[g.BaseEnv]))
	if err != nil || up.Host == "" || (up.Scheme != "http" && up.Scheme != "https") {
		return nil, false
	}
	if strings.EqualFold(up.Hostname(), "host.persona.internal") {
		if p := up.Port(); p != "" {
			up.Host = net.JoinHostPort("127.0.0.1", p)
		} else {
			up.Host = "127.0.0.1"
		}
	}
	basePath := strings.TrimSuffix(up.Path, "/")
	escapedBase := strings.TrimSuffix(up.EscapedPath(), "/")
	if prefix != "" && !strings.HasSuffix(basePath, prefix) {
		basePath += prefix
		escapedBase += (&url.URL{Path: prefix}).EscapedPath()
	}
	up.Path = basePath + "/" + resource + "/" + id
	up.RawPath = escapedBase + escapedSeg
	up.RawQuery = ""
	up.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.String(), nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", larkCardContextAgent)
	if g.Auth.Header != "" {
		req.Header.Set(g.Auth.Header, gateAuthValue(g.Auth.Value, g.Env))
	}
	res, err := larkContextHTTPClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, larkCardContextMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > larkCardContextMaxBytes {
		return nil, false
	}
	return body, true
}

func resolveGateCardContext(kind, chatID, messageID string, read func(resource, id string) ([]byte, bool)) larkCardContext {
	if kind == "reply" {
		var out larkCardContext
		chat := chatID
		if safeGatePathID(messageID) {
			if data, ok := read("messages", messageID); ok {
				msgChatID, msgChatName, sender, text, inTopic := parseGateMessageContext(data)
				if chatID != "" && msgChatID != "" && msgChatID != chatID {
					// Cross-chat mismatch: fail closed and never adopt context from another conversation.
					return larkCardContext{}
				}
				if chat == "" && safeGatePathID(msgChatID) {
					chat = msgChatID
				}
				if msgChatName != "" && (chatID == "" || msgChatID == chatID) {
					out.Chat = msgChatName
				}
				out.ReplySender = sender
				if text != "" {
					out.ReplyText = larkExcerpt(text, 160)
				}
				out.InTopic = inTopic
			}
		}
		if out.Chat == "" && safeGatePathID(chat) {
			if data, ok := read("conversations", chat); ok {
				if name := parseGateConversationName(data); name != "" {
					out.Chat = name
				}
			}
		}
		if out.Chat == "" && chat != "" && (out.ReplySender != "" || out.ReplyText != "") {
			out.Chat = chat
		}
		out.OK = out.Chat != "" || out.ReplySender != "" || out.ReplyText != ""
		return out
	}
	if !safeGatePathID(chatID) {
		return larkCardContext{}
	}
	if data, ok := read("conversations", chatID); ok {
		if name := parseGateConversationName(data); name != "" {
			return larkCardContext{OK: true, Chat: name}
		}
	}
	return larkCardContext{}
}

func parseGateConversationName(data []byte) string {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return ""
	}
	for _, key := range []string{"item", "conversation", "chat", "data"} {
		if sub, ok := root[key].(map[string]any); ok {
			if name := firstNonEmptyField(sub, "name", "displayName", "display_name", "title"); name != "" {
				return larkExcerpt(name, 120)
			}
		}
	}
	if name := firstNonEmptyField(root, "name", "displayName", "display_name", "title"); name != "" {
		return larkExcerpt(name, 120)
	}
	return ""
}

func extractGatePartsText(raw any) string {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	var chunks []string
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s := firstNonEmptyField(part, "text"); s != "" {
			chunks = append(chunks, s)
		}
	}
	return strings.Join(chunks, " ")
}

func parseGateMessageContext(data []byte) (chatID, chatName, sender, text string, inTopic bool) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return "", "", "", "", false
	}
	var obj map[string]any
	for _, key := range []string{"item", "message", "data"} {
		if sub, ok := root[key].(map[string]any); ok {
			obj = sub
			break
		}
	}
	if obj == nil {
		obj = root
	}
	chatID = firstNonEmptyField(obj, "feedId", "feed_id", "chatId", "chat_id", "conversationId", "conversation_id")
	chatName = firstNonEmptyField(obj, "chatName", "chat_name", "conversationName", "conversation_name", "feedName", "feed_name")
	if chatName == "" {
		for _, key := range []string{"conversation", "chat"} {
			if sub, ok := obj[key].(map[string]any); ok {
				if name := firstNonEmptyField(sub, "name", "displayName", "display_name", "title"); name != "" {
					chatName = name
					break
				}
			}
		}
	}
	chatName = larkExcerpt(chatName, 120)

	for _, key := range []string{"sender", "author"} {
		if sub, ok := obj[key].(map[string]any); ok {
			sender = firstNonEmptyField(sub, "name", "displayName", "display_name")
			if sender != "" {
				break
			}
		}
	}
	if sender == "" {
		sender = firstNonEmptyField(obj, "senderName", "sender_name", "authorName", "author_name", "fromName", "from_name")
	}
	if sender == "" {
		for _, key := range []string{"sender", "author"} {
			if sub, ok := obj[key].(map[string]any); ok {
				sender = firstNonEmptyField(sub, "id", "userId", "user_id")
				if sender != "" {
					break
				}
			}
		}
	}
	if sender == "" {
		sender = firstNonEmptyField(obj, "senderId", "sender_id")
	}
	sender = larkExcerpt(sender, 80)

	deletedOrRecalled := false
	for _, key := range []string{"isDeleted", "is_deleted", "deleted", "isRecalled", "is_recalled", "recalled"} {
		if b, ok := obj[key].(bool); ok && b {
			deletedOrRecalled = true
			break
		}
	}
	if !deletedOrRecalled {
		if s := firstNonEmptyField(obj, "text", "textPreview", "text_preview"); s != "" {
			text = s
		} else if s := extractGatePartsText(obj["parts"]); s != "" {
			text = s
		} else if body, ok := obj["body"].(map[string]any); ok {
			if s := firstNonEmptyField(body, "text"); s != "" {
				text = s
			} else if c, ok := body["content"].(string); ok {
				text = larkNestedText(c)
			}
		} else if c, ok := obj["content"].(string); ok {
			text = larkNestedText(c)
		} else if cobj, ok := obj["content"].(map[string]any); ok {
			text = firstNonEmptyField(cobj, "text")
		}
		if text == "" {
			text = firstNonEmptyField(obj, "summary", "preview")
		}
	}

	if firstNonEmptyField(obj, "threadId", "thread_id", "rootId", "root_id", "topicId", "topic_id") != "" {
		inTopic = true
	}
	if tmt := strings.ToLower(firstNonEmptyField(obj, "threadMessageType", "thread_message_type")); tmt == "root" || tmt == "reply" {
		inTopic = true
	}
	for _, key := range []string{"inTopic", "in_topic", "isTopic", "is_topic"} {
		if b, ok := obj[key].(bool); ok && b {
			inTopic = true
			break
		}
	}
	if _, ok := obj["thread"].(map[string]any); ok {
		inTopic = true
	}
	return chatID, chatName, sender, text, inTopic
}

func firstNonEmptyField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

// safeGatePathID accepts only bounded printable ASCII single-segment IDs with no path traversal,
// control characters, whitespace, or URL/shell metacharacters.
func safeGatePathID(id string) bool {
	if id == "" || len(id) > larkCardMaxPathIDLen || id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		if r <= 0x20 || r >= 0x7f {
			return false
		}
		switch r {
		case '/', '\\', '?', '#', '$', '%', '`', ';', '"', '\'', '<', '>':
			return false
		}
	}
	return true
}

func isLarkGateTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t == "" || t == "飞书" || strings.EqualFold(t, "lark") || strings.EqualFold(t, "feishu")
}

func isFeishuOpenAPIID(kind, chatID, messageID string) bool {
	if kind == "reply" {
		if !safeGatePathID(messageID) || !strings.HasPrefix(messageID, "om_") {
			return false
		}
		return chatID == "" || safeGatePathID(chatID)
	}
	return safeGatePathID(chatID) && strings.HasPrefix(chatID, "oc_")
}

// larkContextFromCLI reads only the message's own chat and, for a reply, the message it answers. It is a variable
// so tests can substitute a fake binary or reader; no gate decision depends on it.
var larkContextFromCLI = func(ctx context.Context, n *Node, cfg proto.AssistantLark, c larkCall) larkCardContext {
	if c.Gate >= 0 && c.Gate < len(cfg.Gates) && !isLarkGateTitle(cfg.Gates[c.Gate].Title) {
		return larkCardContext{}
	}
	kind, chatID, messageID := larkMessageTarget(c)
	if kind == "" || !isFeishuOpenAPIID(kind, chatID, messageID) {
		return larkCardContext{}
	}
	bin := "lark-cli"
	if cfg.CLI != nil && cfg.CLI.Bin != "" {
		bin = cfg.CLI.Bin
	}
	if !filepath.IsAbs(bin) {
		p, err := exec.LookPath(bin)
		if err != nil {
			return larkCardContext{}
		}
		bin = p
	}
	read := func(path string, out any) bool {
		cli := cfg.CLI
		if cli == nil {
			cli = &proto.LarkCLI{}
		}
		args := append(larkCLIProfile(cli), "api", "GET", path)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = larkCLIEnv(bin)
		data, err := cmd.Output()
		if err != nil || json.Unmarshal(data, out) != nil {
			return false
		}
		return true
	}
	chat := chatID
	if kind == "reply" {
		var answer struct {
			Message struct {
				ChatID   string `json:"chat_id"`
				ThreadID string `json:"thread_id"`
				Sender   struct {
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"message"`
		}
		if !read("/open-apis/im/v1/messages/"+messageID, &answer) {
			return larkCardContext{}
		}
		text := larkNestedText(answer.Message.Body.Content)
		if text == "" {
			return larkCardContext{}
		}
		if answer.Message.ChatID != "" {
			if !safeGatePathID(answer.Message.ChatID) {
				return larkCardContext{}
			}
			if chatID != "" && answer.Message.ChatID != chatID {
				return larkCardContext{}
			}
			chat = answer.Message.ChatID
		}
		sender := answer.Message.Sender.Name
		if sender == "" {
			sender = answer.Message.Sender.ID
		}
		out := larkCardContext{Chat: chat, ReplySender: sender, ReplyText: larkExcerpt(text, 160), InTopic: answer.Message.ThreadID != ""}
		var info struct {
			Chat struct {
				Name string `json:"name"`
			} `json:"chat"`
		}
		if safeGatePathID(chat) && read("/open-apis/im/v1/chats/"+chat, &info) && info.Chat.Name != "" {
			out.Chat = info.Chat.Name
		}
		out.OK = out.Chat != ""
		return out
	}
	var info struct {
		Chat struct {
			Name string `json:"name"`
		} `json:"chat"`
	}
	if !safeGatePathID(chat) || !read("/open-apis/im/v1/chats/"+chat, &info) {
		return larkCardContext{}
	}
	if info.Chat.Name != "" {
		chat = info.Chat.Name
	}
	return larkCardContext{OK: true, Chat: chat}
}

// larkCardContexts enriches each message write independently, with a short total budget per call. Any other gated
// call, or a failed read, never delays or blocks the card; nothing here grants or denies a call.
func (n *Node) larkCardContexts(ctx context.Context, cfg proto.AssistantLark, calls []larkCall) []larkCardContext {
	out := make([]larkCardContext, len(calls))
	for i, c := range calls {
		if c.CLI {
			continue
		}
		parsed, ok := parseCleanLarkMessage(c)
		if !ok {
			continue
		}
		if _, _, _, ok := larkEffectiveTarget(c, parsed); !ok {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, larkCardContextTimeout)
		meta := larkContextFromGate(callCtx, n, cfg, c)
		if !meta.OK {
			meta = larkContextFromCLI(callCtx, n, cfg, c)
		}
		cancel()
		out[i] = meta
	}
	return out
}

// larkMessageTarget recognizes only the message-send and message-reply routes whose body is user-visible text.
func larkMessageTarget(c larkCall) (kind, chatID, messageID string) {
	if c.Method != "POST" {
		return "", "", ""
	}
	parts := strings.Split(strings.Trim(c.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "conversations" && parts[2] == "messages" {
		if len(parts) == 3 {
			return "send", parts[1], ""
		}
		if len(parts) == 5 && parts[4] == "replies" {
			return "reply", parts[1], parts[3]
		}
	}
	if len(parts) == 3 && parts[0] == "messages" && parts[2] == "replies" {
		return "reply", "", parts[1]
	}
	for i, part := range parts {
		if part == "chats" && i+2 < len(parts) && parts[i+2] == "messages" && i+2 == len(parts)-1 {
			return "send", parts[i+1], ""
		}
		if part == "messages" && i+2 < len(parts) && parts[i+2] == "replies" && i+2 == len(parts)-1 {
			return "reply", "", parts[i+1]
		}
	}
	return "", "", ""
}

func larkNestedText(content string) string {
	var v struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(content), &v) == nil && v.Text != "" {
		return strings.TrimSpace(v.Text)
	}
	return strings.TrimSpace(content)
}

func isUnsafeBidiRune(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F ||
		(r >= 0x202A && r <= 0x202E) ||
		(r >= 0x2066 && r <= 0x2069)
}

// stripUnsafeDisplay removes ANSI escape sequences, C0/C1 control characters, and Unicode BiDi controls
// from untrusted read-only remote metadata; whitespace controls and line/paragraph separators become spaces
// for subsequent single-line collapsing. Never use this on outgoing user-approved text or mentions.
func stripUnsafeDisplay(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI sequence: ESC [ <params> <intermediates> <final>
				i++
				for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
					i++
				}
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
					i++
				}
				if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
					i++
				}
			case ']': // OSC sequence: ESC ] ... (BEL or ESC \)
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				if s[i] >= 0x40 && s[i] <= 0x5f {
					i++
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' || r == 0x2028 || r == 0x2029 {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isUnsafeBidiRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hasUnsafeDisplay reports whether s contains ESC/ANSI, disallowed C0/C1 control characters, Unicode BiDi
// controls, Unicode line/paragraph separators, or invalid UTF-8 bytes. Outgoing payloads matching this fall
// back to the raw JSON summary instead of silently stripping bytes that will actually be transmitted.
func hasUnsafeDisplay(s string, allowNewlines bool) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return true
		}
		i += size
		if r == 0x1b || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isUnsafeBidiRune(r) || r == 0x2028 || r == 0x2029 {
			return true
		}
		if r < 0x20 {
			if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
				continue
			}
			return true
		}
	}
	return false
}

func larkExcerpt(text string, max int) string {
	text = strings.Join(strings.Fields(stripUnsafeDisplay(text)), " ")
	r := []rune(text)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return text
}

// larkClip normalizes line endings, trims outer whitespace, and caps outgoing text length without stripping bytes.
func larkClip(text string, max int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return text
}

// decodeStrictJSONObject decodes one top-level JSON object without duplicate keys (exact or case-folded) or
// trailing tokens.
func decodeStrictJSONObject(raw string) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	seenLower := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		lower := strings.ToLower(name)
		if seenLower[lower] {
			return nil, false
		}
		seenLower[lower] = true
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false
		}
		fields[name] = val
	}
	endTok, err := dec.Token()
	if err != nil || endTok != json.Delim('}') {
		return nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, false
	}
	return fields, true
}

func decodeJSONNullOrString(raw json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func decodeJSONNullOrBool(raw json.RawMessage) (bool, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return false, true
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func parseCleanContentText(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return "", true
	}
	if strings.HasPrefix(trimmed, "{") {
		obj, ok := decodeStrictJSONObject(trimmed)
		if !ok || len(obj) != 1 {
			return "", false
		}
		val, exists := obj["text"]
		if !exists {
			return "", false
		}
		s, ok := decodeJSONNullOrString(val)
		if !ok {
			return "", false
		}
		return strings.TrimSpace(s), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	inner := strings.TrimSpace(s)
	if strings.HasPrefix(inner, "{") {
		obj, ok := decodeStrictJSONObject(inner)
		if !ok || len(obj) != 1 {
			return "", false
		}
		val, exists := obj["text"]
		if !exists {
			return "", false
		}
		text, ok := decodeJSONNullOrString(val)
		if !ok {
			return "", false
		}
		return strings.TrimSpace(text), true
	}
	if strings.HasPrefix(inner, "[") {
		return "", false
	}
	return inner, true
}

func parseCleanMentions(raw json.RawMessage) ([]string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	if len(items) == 0 {
		return nil, true
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		fields, ok := decodeStrictJSONObject(string(item))
		if !ok {
			return nil, false
		}
		vals := map[string]string{}
		isAll := false
		idCount, nameCount, allFlagCount := 0, 0, 0
		for k, v := range fields {
			switch k {
			case "all", "isAll", "is_all":
				allFlagCount++
				if allFlagCount > 1 {
					return nil, false
				}
				b, ok := decodeJSONNullOrBool(v)
				if !ok {
					return nil, false
				}
				if b {
					isAll = true
				}
			case "userId", "user_id", "id", "openId", "open_id":
				idCount++
				if idCount > 1 {
					return nil, false
				}
				s, ok := decodeJSONNullOrString(v)
				if !ok || hasUnsafeDisplay(s, false) {
					return nil, false
				}
				vals[k] = strings.TrimSpace(s)
			case "name", "displayName", "display_name":
				nameCount++
				if nameCount > 1 {
					return nil, false
				}
				s, ok := decodeJSONNullOrString(v)
				if !ok || hasUnsafeDisplay(s, false) {
					return nil, false
				}
				vals[k] = strings.TrimSpace(s)
			case "key", "type":
				s, ok := decodeJSONNullOrString(v)
				if !ok || hasUnsafeDisplay(s, false) {
					return nil, false
				}
				vals[k] = strings.TrimSpace(s)
			default:
				return nil, false
			}
		}
		if vals["type"] != "" && !strings.EqualFold(vals["type"], "user") {
			// Non-user mention types (e.g. "all", "usergroup", "department") keep full raw visibility.
			return nil, false
		}
		for _, k := range []string{"userId", "user_id", "id", "openId", "open_id", "key"} {
			v := vals[k]
			if v == "" {
				continue
			}
			clean := strings.TrimPrefix(v, "@")
			if strings.HasPrefix(strings.ToLower(clean), "usergroup:") {
				return nil, false
			}
			if strings.EqualFold(clean, "all") {
				isAll = true
			}
		}
		var label string
		for _, k := range []string{"name", "displayName", "display_name"} {
			if vals[k] != "" {
				label = vals[k]
				break
			}
		}
		if label == "" && isAll {
			label = "all"
		}
		if label == "" {
			for _, k := range []string{"key", "userId", "user_id", "openId", "open_id", "id"} {
				if vals[k] != "" {
					label = vals[k]
					break
				}
			}
		}
		label = strings.TrimSpace(strings.TrimLeft(label, "@"))
		if label == "" || utf8.RuneCountInString(label) > 80 {
			return nil, false
		}
		if isAll && !isAllMentionLabel(label) {
			label += " (all)"
		}
		out = append(out, "@"+label)
	}
	return out, true
}

func isAllMentionLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "all", "everyone", "所有人", "全体成员", "全員":
		return true
	}
	return false
}

// parseCleanLarkMessage validates that c is an unambiguous, query-free, static JSON plain-text send/reply whose
// fields are all accounted for and valid for its route kind. Any dynamic ($), query-bearing, malformed,
// duplicate-key, synonym-conflicting, non-text, or extra-field payload returns false so the caller keeps the
// full transparent method/path/body summary.
func parseCleanLarkMessage(c larkCall) (larkParsedMessage, bool) {
	kind, _, _ := larkMessageTarget(c)
	if kind == "" || c.Query != "" || strings.Contains(c.Body, "$") {
		return larkParsedMessage{}, false
	}
	fields, ok := decodeStrictJSONObject(c.Body)
	if !ok {
		return larkParsedMessage{}, false
	}
	var out larkParsedMessage
	hasText, hasContent := false, false
	hasMsgType, hasReplyThreadMode, hasReplyTo, hasClientID, hasExpectedSender, hasEditorType := false, false, false, false, false, false
	for k, raw := range fields {
		switch k {
		case "text":
			if hasContent {
				return larkParsedMessage{}, false
			}
			s, ok := decodeJSONNullOrString(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			out.Text = strings.TrimSpace(s)
			hasText = true
		case "content":
			if hasText {
				return larkParsedMessage{}, false
			}
			s, ok := parseCleanContentText(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			out.Text = s
			hasContent = true
		case "msg_type", "msgType":
			if hasMsgType {
				return larkParsedMessage{}, false
			}
			hasMsgType = true
			s, ok := decodeJSONNullOrString(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			if mt := strings.ToLower(strings.TrimSpace(s)); mt != "" && mt != "text" {
				return larkParsedMessage{}, false
			}
		case "mentions":
			mentions, ok := parseCleanMentions(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			out.Mentions = mentions
		case "replyMode", "reply_mode":
			if kind != "reply" || hasReplyThreadMode {
				return larkParsedMessage{}, false
			}
			hasReplyThreadMode = true
			s, ok := decodeJSONNullOrString(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "", "direct", "reply", "quote", "chat", "normal":
			case "thread":
				out.InTopic = true
			default:
				return larkParsedMessage{}, false
			}
		case "thread", "reply_in_thread", "replyInThread":
			if kind != "reply" || hasReplyThreadMode {
				return larkParsedMessage{}, false
			}
			hasReplyThreadMode = true
			b, ok := decodeJSONNullOrBool(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			if b {
				out.InTopic = true
			}
		case "replyToMessageId", "reply_to_message_id", "replyTo", "reply_to", "replyId", "reply_id":
			if kind != "reply" || hasReplyTo {
				return larkParsedMessage{}, false
			}
			hasReplyTo = true
			s, ok := decodeJSONNullOrString(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			s = strings.TrimSpace(s)
			if s != "" {
				if !safeGatePathID(s) {
					return larkParsedMessage{}, false
				}
				out.ReplyTo = s
			}
		case "clientId", "client_id", "uuid":
			if hasClientID {
				return larkParsedMessage{}, false
			}
			hasClientID = true
			if _, ok := decodeJSONNullOrString(raw); !ok {
				return larkParsedMessage{}, false
			}
		case "expectedSenderId", "expected_sender_id", "expect_user", "expectUser":
			if hasExpectedSender {
				return larkParsedMessage{}, false
			}
			hasExpectedSender = true
			if _, ok := decodeJSONNullOrString(raw); !ok {
				return larkParsedMessage{}, false
			}
		case "editorType", "editor_type":
			if hasEditorType {
				return larkParsedMessage{}, false
			}
			hasEditorType = true
			s, ok := decodeJSONNullOrString(raw)
			if !ok {
				return larkParsedMessage{}, false
			}
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "", "simple", "plain", "text", "markdown", "md":
			default:
				return larkParsedMessage{}, false
			}
		default:
			return larkParsedMessage{}, false
		}
	}
	if out.Text == "" || hasUnsafeDisplay(out.Text, true) {
		return larkParsedMessage{}, false
	}
	return out, true
}

func larkEffectiveTarget(c larkCall, parsed larkParsedMessage) (kind, chatID, messageID string, ok bool) {
	kind, chatID, messageID = larkMessageTarget(c)
	if kind == "" {
		return "", "", "", false
	}
	if chatID != "" && !safeGatePathID(chatID) {
		return "", "", "", false
	}
	if messageID != "" && !safeGatePathID(messageID) {
		return "", "", "", false
	}
	if parsed.ReplyTo != "" {
		if kind != "reply" || (messageID != "" && messageID != parsed.ReplyTo) {
			return "", "", "", false
		}
	}
	if kind == "send" && chatID == "" {
		return "", "", "", false
	}
	if kind == "reply" && chatID == "" && messageID == "" {
		return "", "", "", false
	}
	return kind, chatID, messageID, true
}

func larkSummaryServiceName(gates []proto.CommandGate, c larkCall, lang i18n.Lang) string {
	title := ""
	if c.Gate >= 0 && c.Gate < len(gates) {
		title = larkExcerpt(gates[c.Gate].Title, 60)
	}
	if isLarkGateTitle(title) {
		return i18n.T(lang, "node-larkgate.default_service")
	}
	return title
}

// larkMessageSummary renders a clean send or reply without its HTTP route or internal JSON fields.
// Ambiguous, dynamic ($), query-bearing, non-JSON, or extra-field calls return ("", false) so the
// caller falls back to the full transparent method/path/body summary.
func larkMessageSummary(gates []proto.CommandGate, c larkCall, meta larkCardContext, lang i18n.Lang) (string, bool) {
	parsed, ok := parseCleanLarkMessage(c)
	if !ok {
		return "", false
	}
	kind, chatID, messageID, ok := larkEffectiveTarget(c, parsed)
	if !ok {
		return "", false
	}
	service := larkSummaryServiceName(gates, c, lang)
	chat := larkExcerpt(meta.Chat, 120)
	if chat == "" {
		chat = larkExcerpt(chatID, 120)
	}
	msgID := larkExcerpt(messageID, 120)
	replySender := larkExcerpt(meta.ReplySender, 80)
	replyText := larkExcerpt(meta.ReplyText, 160)
	inTopic := meta.InTopic || parsed.InTopic

	var b strings.Builder
	if kind == "send" {
		b.WriteString(i18n.T(lang, "node-larkgate.send_to", i18n.Args{"service": service, "chat": chat}))
	} else if chat != "" && replySender != "" {
		b.WriteString(i18n.T(lang, "node-larkgate.reply_to", i18n.Args{"service": service, "chat": chat, "sender": replySender}))
	} else if chat != "" {
		b.WriteString(i18n.T(lang, "node-larkgate.reply_in_chat", i18n.Args{"service": service, "chat": chat}))
	} else if replySender != "" {
		b.WriteString(i18n.T(lang, "node-larkgate.reply_to_sender", i18n.Args{"service": service, "sender": replySender}))
	} else {
		b.WriteString(i18n.T(lang, "node-larkgate.reply_message", i18n.Args{"service": service, "message": msgID}))
	}
	if inTopic {
		b.WriteString(i18n.T(lang, "node-larkgate.topic_suffix"))
	}
	if kind == "reply" && replyText != "" {
		b.WriteString("\n" + i18n.T(lang, "node-larkgate.original", i18n.Args{"text": replyText}))
	}
	if len(parsed.Mentions) > 0 {
		b.WriteString("\n" + i18n.T(lang, "node-larkgate.mentions", i18n.Args{"mentions": strings.Join(parsed.Mentions, ", ")}))
	}
	b.WriteString("\n" + i18n.T(lang, "node-larkgate.outgoing", i18n.Args{"text": larkClip(parsed.Text, 2000)}))
	return b.String(), true
}
