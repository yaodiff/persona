package lark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// The chat's inbound path (chat_state.go, message.go, commands.go, card_actions.go, text_answer.go, read.go,
// models.go, elsewhere.go, thread.go, thread_open.go, nudge.go): the owner's messages and presses in the bot's chats,
// and the chat's state they read and write. What they ask of the rest of the assistant (a conversation to send to,
// an interaction to answer, a to-do to open, a decision to note) goes through the host; the names below are the ones
// the code had in the core.

// The hub's values, as the core names them (assistantwire).
type (
	FeishuStatus   = assistantwire.FeishuStatus
	FeishuFeatures = assistantwire.FeishuFeatures
	FeishuMessage  = assistantwire.FeishuMessage
	FeishuAction   = assistantwire.FeishuAction
	FeishuOwner    = assistantwire.FeishuOwner
	FeishuRecalled = assistantwire.FeishuRecalled
	FeishuView     = assistantwire.FeishuView
	FeishuAgent    = assistantwire.FeishuAgent
	Device         = assistantwire.Device

	// resolvedTarget is a target as it is now (Host.ResolveTarget).
	resolvedTarget = assistantwire.ChatResolved
)

const (
	FeishuText          = assistantwire.FeishuText
	FeishuReact         = assistantwire.FeishuReact
	FeishuUnreact       = assistantwire.FeishuUnreact
	FeishuMessages      = assistantwire.FeishuMessages
	FeishuResource      = assistantwire.FeishuResource
	FeishuSave          = assistantwire.FeishuSave
	FeishuUserName      = assistantwire.FeishuUserName
	FeishuFromElsewhere = assistantwire.FeishuFromElsewhere
	FeishuFromRead      = assistantwire.FeishuFromRead
	InteractionsGet     = assistantwire.InteractionsGet
	NotifyQuietGet      = assistantwire.NotifyQuietGet
	CodeTryAgain        = assistantwire.CodeTryAgain
)

// The core's text helpers (internal/textfmt).
var (
	clip      = textfmt.Clip
	oneLine   = textfmt.OneLine
	firstLine = textfmt.FirstLine
	short     = textfmt.Short
)

// endedAskFor is how long a question card that ended elsewhere is remembered for an option typed for it late: the
// channels' own (the core's channels_plugin.go).
const endedAskFor = 10 * time.Minute

// fbot is the chat's state.
func (s core) fbot() *feishuBook { return &s.t.book }

// badRequest is a call the chat cannot take as it came (the core's start.go).
func badRequest(err error) error {
	return &rpc.Error{Code: assistantwire.CodeBadRequest, Message: err.Error()}
}

// randomHex is n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// dotJoin joins the parts that are not empty with " · ".
func dotJoin(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// contextText is a quoted text as a prompt's context block shows it (the core's todo_decisions.go).
func contextText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(prompts.ContextBody(s)), "\n", "\n      ")
}

// botTargetOf is the target a run of the chat answers (the core's run_progress.go).
func botTargetOf(r *channel.Run) botTarget {
	kind := r.TargetKind
	if kind == "" || kind == targetMain {
		kind = targetPersona
	}
	return botTarget{Kind: kind, MachineID: r.MachineID, SessionID: r.SessionID}
}

// ---- the transport, over the host's ----

// botPost posts a card into the owner's chat, or as a reply to replyTo.
func (s core) botPost(ctx context.Context, replyTo string, card *botcard.Card, fallback string) (string, error) {
	return s.h.Post(ctx, feishuTarget{}, replyTo, card, fallback, "")
}

// botNote writes a line of text under a message of the owner's chat.
func (s core) botNote(ctx context.Context, messageID, text string) {
	s.h.Note(ctx, feishuTarget{}, messageID, text)
}

// botSay sends a line of text into the owner's chat.
func (s core) botSay(ctx context.Context, text string) {
	if err := s.appCall(ctx, FeishuText, map[string]any{"text": text}, nil); err != nil {
		s.logf("bots: say: %v", err)
	}
}

// botName is a user's name (the hub's contact lookup; "someone" when it can't be read).
func (s core) botName(ctx context.Context, openID string, lang i18n.Lang) string {
	var res struct {
		Name string `json:"name"`
	}
	if err := s.appCall(ctx, FeishuUserName, map[string]any{"open_id": openID}, &res); err != nil || res.Name == "" {
		return i18n.T(lang, "assistant-feishu.unknown_sender")
	}
	return res.Name
}

// appCallTo is a call of another app of the hub (interactions, notify).
func (s core) appCallTo(ctx context.Context, app, method string, params, out any) error {
	return s.h.CallApp(ctx, app, method, params, out)
}

func (s core) hello(ctx context.Context) assistantwire.Hello { return s.h.Hello(ctx) }

func (s core) resolveBotTarget(ctx context.Context, t botTarget) (resolvedTarget, error) {
	return s.h.ResolveTarget(ctx, t)
}

func (s core) botAllowed(l i18n.Lang, f FeishuFeatures, t botTarget) error {
	return s.h.Allowed(l, f, t)
}
