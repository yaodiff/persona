package prompts

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var messageContextRe = lazyre.New(`(?s)^\s*<persona-context\b([^>]*)>(.*?)</persona-context>\s*`)
var sourceKindRe = lazyre.New(`\bkind="message-source"`)
var sideConversationContextRe = lazyre.New(`<persona-context\b[^>]*\bkind\s*=\s*["']side-conversation["'][^>]*>`)
var sideConversationMarkerRe = lazyre.New(`<persona-side-conversation\s*/>`)
var relayTitleRe = lazyre.New(`\btitle="来自对话「(.*?)」"`)
var relayBodyRe = lazyre.New(`^这条消息不是用户本人发的，是 Persona 的另一段对话（([^\s（）]+)）用 (conversation_send|conversation_new) 发来的，第 ([0-9]+) 跳（最多 ([0-9]+) 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 ([^\s；]+)；只是确认收到就不要回，免得两段对话来回发。$`)

type messageSourceMetadata struct {
	proto.MessageSource
	Author  string `json:"author,omitempty"`
	RelayID string `json:"relay_id,omitempty"`
}

// MessageSourceText persists provenance in the native transcript, not a short-lived
// echo cache. JSON's HTML escaping prevents a title from closing the context block.
func MessageSourceText(text string, source *proto.MessageSource) string {
	if source == nil || source.Kind == "" {
		return text
	}
	if existing, found := sourceMetadata(text); found && existing != nil && *existing == *source {
		return text
	}
	return messageSourceText(text, source)
}

// RelayMessageText keeps a context-only relay identifiable when the transcript reader drops its context.
func RelayMessageText(text string, source *proto.MessageSource, id string) string {
	meta := messageSourceMetadata{RelayID: id}
	if source != nil {
		meta.MessageSource = *source
	}
	return messageSourceText(text, meta)
}

// RelayMessageID reads the id saved by RelayMessageText. The owner's provenance block takes precedence.
func RelayMessageID(text string) string {
	meta, _ := readSourceMetadata(text)
	if meta.Author == "user" {
		return ""
	}
	return meta.RelayID
}

func messageSourceText(text string, metadata any) string {
	b, err := json.Marshal(metadata)
	if err != nil {
		panic(err) // only fixed metadata structs are rendered
	}
	return render("shared/message_source", struct{ Metadata, Text string }{string(b), text})
}

// sourceMetadata returns the first leading provenance block, even if it says the
// author is the user. That sentinel prevents nested, user-pasted blocks impersonating
// an automated sender before client_msg_id arrives or on a user-started conversation.
func sourceMetadata(text string) (source *proto.MessageSource, found bool) {
	meta, found := readSourceMetadata(text)
	if meta.Author == "user" || meta.Kind == "" {
		return nil, found
	}
	return &meta.MessageSource, true
}

func readSourceMetadata(text string) (messageSourceMetadata, bool) {
	for {
		m := messageContextRe.FindStringSubmatch(text)
		if m == nil {
			return messageSourceMetadata{}, false
		}
		if sourceKindRe.MatchString(m[1]) {
			var meta messageSourceMetadata
			if json.Unmarshal([]byte(strings.TrimSpace(m[2])), &meta) != nil {
				return messageSourceMetadata{}, true // malformed explicit metadata must not become a guessed source
			}
			return meta, true
		}
		text = text[len(m[0]):]
	}
}

// ProtectUserMessage retains the user's words verbatim but marks suspicious pasted
// templates as theirs. Ordinary input needs no wrapper; client_msg_id protects it too.
func ProtectUserMessage(text string) string {
	if UserAuthoredContext(text) {
		return text
	}
	_, metadata := sourceMetadata(text)
	if metadata || ParseRelaySource(text) != nil || ParseInjected(text) != nil {
		return messageSourceText(text, struct {
			Author string `json:"author"`
		}{"user"})
	}
	return text
}

// IsSideConversation reports whether text carries the wrapping of RenderSideConversation
// (prompts/assistant/side_conversation.md): a separate agent_send request to a tasked
// Agent, which must never be treated as the owner answering that task's pending decision.
// Both the typed persona-context block and the standalone marker are accepted, matching
// the template and the legacy envelope kept in done tests.
func IsSideConversation(text string) bool {
	return sideConversationContextRe.MatchString(text) || sideConversationMarkerRe.MatchString(text)
}

// ParseRelaySource recognizes only the complete canonical relay context. A title,
// an emoji, or ordinary words alone never establish conversation provenance.
func ParseRelaySource(text string) *proto.MessageSource {
	for {
		m := messageContextRe.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		title := relayTitleRe.FindStringSubmatch(m[1])
		body := relayBodyRe.FindStringSubmatch(strings.TrimSpace(m[2]))
		if title != nil && strings.Contains(m[1], `kind="context"`) && body != nil && body[1] == body[5] {
			return &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: body[1], Title: html.UnescapeString(title[1])}
		}
		text = text[len(m[0]):]
	}
}

var relayQuoteRe = lazyre.New(`^\s*kind="owner-request"\s+message_id="([^"]*)"\s*$`)

// ParseRelayQuoteID is the id of the owner's message a relay carries (assistant/relay.md, RelayQuote): only a quote
// block that directly follows the canonical relay context counts, so words the sending model wrote into its text
// can't pass for one. "" when there is none.
func ParseRelayQuoteID(text string) string {
	relay := false
	for {
		m := messageContextRe.FindStringSubmatch(text)
		if m == nil {
			return ""
		}
		if relay {
			if q := relayQuoteRe.FindStringSubmatch(m[1]); q != nil {
				return html.UnescapeString(q[1])
			}
			return ""
		}
		relay = ParseRelaySource(m[0]) != nil
		text = text[len(m[0]):]
	}
}

// ItemSource prefers explicit fields, then persisted provenance, then existing
// injected metadata and exact legacy templates. App messages with IDs accept
// only provenance from an A2A envelope bound to that ID. Item.From is a name.
func ItemSource(it proto.Item) *proto.MessageSource {
	if it.Source != nil && it.Source.Kind != "" {
		return it.Source
	}
	inferText := it.Role == "user" && it.Kind == proto.KindText && it.ClientMsgID == "" && it.From == ""
	if it.Role == "user" && it.Kind == proto.KindText && it.From == "" && strings.HasPrefix(it.ClientMsgID, proto.A2ADeliveryPrefix) && RelayMessageID(it.Text) == it.ClientMsgID {
		// A2A's envelope binds the persisted source to the delivery ID. The
		// owner-authored sentinel blocks a pasted envelope from qualifying.
		if source, found := sourceMetadata(it.Text); found && source != nil && source.Kind == proto.SourceConversation {
			return source
		}
	}
	if inferText {
		if source, found := sourceMetadata(it.Text); found {
			return source
		}
	}
	if source := proto.SourceFromInjected(it.Injected); source != nil {
		return source
	}
	if !inferText {
		return nil
	}
	if source := ParseRelaySource(it.Text); source != nil {
		return source
	}
	return proto.SourceFromInjected(ParseInjected(it.Text))
}

// UserAuthoredContext marks a protected user message for other legacy classifiers.
func UserAuthoredContext(text string) bool {
	source, found := sourceMetadata(text)
	return found && source == nil
}
