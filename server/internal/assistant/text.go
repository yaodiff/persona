package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Formatting for tool results and the hub's logs, the same the hub writes with (one copy, internal/textfmt).
var (
	clipMarkdown  = textfmt.ClipMarkdown
	clip          = textfmt.Clip
	oneLine       = textfmt.OneLine
	firstLine     = textfmt.FirstLine
	firstNonEmpty = textfmt.FirstNonEmpty
	planTitle     = textfmt.PlanTitle
	ago           = textfmt.Ago
	short         = textfmt.Short
	agentLabel    = textfmt.AgentLabel
	stateNames    = textfmt.StateNames
	// ixToolName and toolDisplayName name a card's tool for a sentence; the fleet's rows name it the same way
	// (fleetview.InteractionDetail).
	ixToolName      = textfmt.ToolName
	toolDisplayName = textfmt.ToolDisplayName
)

// transcriptText is a conversation's recent items for a model to read (agent_read, conversation_read): the
// messages, tool calls in one line each, failed results, cut to at most the last 40 000 bytes at a rune boundary.
// link is the conversation's web page ("" for none): each message then carries its own link
// (<page>?at=<item id>, #2194), which opens the page scrolled to it.
func transcriptText(head, link string, items []proto.Item) string {
	var b strings.Builder
	b.WriteString(head + "\n\n")
	for _, it := range items {
		switch it.Kind {
		case proto.KindText:
			who := "Agent"
			if it.Role == "user" {
				who = "User"
				if it.From != "" {
					who = "Message from " + it.From
				}
			}
			if l := itemLink(link, it.ID); l != "" {
				who += " (" + l + ")"
			}
			text := it.Text
			if it.Role == "assistant" {
				text = wire.AssistantCardText(text)
			}
			fmt.Fprintf(&b, "%s: %s\n\n", who, clip(text, 4000))
		case proto.KindThinking:
			if t := strings.TrimSpace(it.Text); t != "" {
				fmt.Fprintf(&b, "(agent, progress summary) %s\n", clip(oneLine(t), 400))
			}
		case proto.KindToolUse:
			fmt.Fprintf(&b, "→ %s %s\n", it.ToolName, clip(oneLine(toolSummary(it.ToolName, it.ToolInput)), 200))
		case proto.KindToolResult:
			if it.IsError {
				fmt.Fprintf(&b, "  ✗ %s\n", clip(oneLine(it.Text), 300))
			}
		case proto.KindNotice, proto.KindRecap:
			if it.Text != "" {
				fmt.Fprintf(&b, "(%s)\n", clip(oneLine(it.Text), 300))
			}
		case proto.KindImage:
			b.WriteString("[image]\n")
		}
	}
	out := b.String()
	if len(out) > 40000 {
		start := len(out) - 40000
		for start < len(out) && !utf8.RuneStart(out[start]) {
			start++
		}
		out = "…(earlier part cut)\n" + out[start:]
	}
	return out
}

// toolSummary is the one-line gist of a tool call (command, file, url…).
func toolSummary(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	for _, k := range []string{"command", "file_path", "url", "query", "pattern", "description", "prompt", "path"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// itemLink is the link to one item of a conversation whose page is link, "" when either is.
func itemLink(link, id string) string {
	if link == "" || id == "" {
		return ""
	}
	return deeplink.At(link, id)
}

func clipRunesTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
