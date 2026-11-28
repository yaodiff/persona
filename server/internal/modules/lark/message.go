package lark

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- a message to the bot ----

// FeishuMessage is feishu.message: the owner's message in the bot's private chat (the hub checked who it is from),
// taken once and handled in the background, one at a time in the order they came (feishuTurn).
//
// Handled each on its own, a message slower to get through (its reaction, a picture to download, a quote to read)
// was overtaken by the next one. The later message went into the conversation first, and the one answer covering
// both went under the earlier message. A message right after "/new …" could also read the target before the new
// conversation was set, and start one more.
func (s core) FeishuMessage(ctx context.Context, p FeishuMessage) error {
	var ev botcard.MessageEvent
	if err := json.Unmarshal(p.Event, &ev); err != nil {
		return badRequest(err)
	}
	// It takes its place as it comes, before the bot's status is read (a call to the hub). One that is not handled
	// (refused, redelivered) still passes the turn on.
	b := s.fbot()
	turn := &feishuTurn{done: make(chan struct{}), wait: s.timing("turn_wait"), now: b.clock}
	b.mu.Lock()
	turn.since = turn.now()
	turn.prev, b.line = b.line, turn.done
	b.mu.Unlock()
	queued := false
	defer func() {
		if !queued {
			turn.pass()
		}
	}()
	// What it is taken with first: a message taken is always answered, never dropped for want of it.
	st, err := s.feishuStatus(ctx)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: "the bot's status: " + err.Error()}
	}
	// This is the internal, paired Feishu bot handover. Other extensions enter through channelReceive.
	// The bound chat check was made at the bot, before it called this method.
	// The core decides whether a project review takes it (Host.ProjectReviewOwnerInput).
	if p.From == "" && st.Enabled && st.Owner != "" && ev.Message.MessageID != "" &&
		ev.Sender.SenderType == "user" && ev.Sender.SenderID.OpenID == st.Owner && slices.Contains(st.Chats, ev.Message.ChatID) {
		target := s.botTargetFor(ev.Message.RootID, ev.Message.ParentID)
		if err := s.h.ProjectReviewOwnerInput(ctx, "feishu:"+ev.Message.MessageID, target.MachineID, target.SessionID); err != nil {
			return &rpc.Error{Code: CodeTryAgain, Message: "could not commit project review input"}
		}
	}
	first, err := s.claimBotMessage(ev.Message.MessageID)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: err.Error()}
	}
	if !first {
		return nil // redelivered, or handed over again after a call that was cut off
	}
	queued = true
	s.t.workers.Add(1) // the layer's background work (Layer.Wait)
	go func() {
		defer s.t.workers.Done()
		defer turn.pass()
		turn.waitTurn()
		if p.From == FeishuFromElsewhere {
			s.feishuElsewhere(ev, st) // the owner's "@bot /ai …" from a group that is not opted in (elsewhere.go)
			return
		}
		if p.From == FeishuFromRead {
			s.feishuRead(ev, st) // the owner's "@bot /read …" from a group (read.go)
			return
		}
		s.feishuMessage(ev, st, p.Action)
	}()
	return nil
}

func (s core) feishuMessage(ev botcard.MessageEvent, st FeishuStatus, action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	msg := ev.Message
	if s.botRecalled(msg.MessageID) {
		s.logf("bots: a message recalled before its turn: not sent")
		return
	}
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Sticker {
		// A sticker carries no request: do not react, do not start an Agent turn.
		s.logf("bots: a sticker was accepted silently: no turn")
		return
	}
	if msg.ChatType != "p2p" {
		// Group messages route by topic, independently of private chat commands, cards and the current target.
		s.threadMessage(ctx, ev, st, content)
		return
	}
	if !content.Forward && len(content.Media) == 0 && s.h.NoReplyCommand(content.Text) && s.h.ChatNoReply(ctx, ev) {
		return // "don't report back" for a to-do taken in this chat with no topic to say it in (the core's todo_source.go)
	}
	var target botTarget
	if cmd, _, isCommand := parseBotCommand(content.Text); isCommand && !content.Forward && len(content.Media) == 0 {
		handled, rest := s.botCommand(ctx, st.Features, msg.MessageID, content.Text, msg.RootID, msg.ParentID)
		if handled {
			return
		}
		content.Text = rest
		if cmd == "/new" || cmd == "/新对话" { // i18n-ignore: command alias typed by the user
			// The first message explicitly starts a conversation, even when replying to an old card.
			// Keep the reply metadata for quoted context, but do not answer or route by that card.
			target = botTarget{Kind: targetPersona}
		}
	}
	if target.Kind == "" {
		if !content.Forward && s.h.ConclusionQuestion(ctx, &ev, &content, st.Features) {
			return
		}
		if !content.Forward && s.h.AcceptReply(ctx, msg.MessageID, content.Text, len(content.Media) > 0, msg.ParentID, msg.RootID) {
			return // about a "Please verify" card (the core's acceptance_reply.go)
		}
		if !content.Forward && len(content.Media) == 0 && s.h.ApproveOwnerAction(ctx, ev, content.Text, action) {
			return // an exact direct reply to an owner-action proposal (the core's owner_action_reply.go)
		}
		if !content.Forward && len(content.Media) == 0 && s.botAnswerQuestion(ctx, msg.MessageID, content.Text, action, msg.RootID, msg.ParentID) {
			return // the answer to the card waiting in this topic (text_answer.go)
		}
		target = s.botTargetFor(msg.RootID, msg.ParentID)
	}
	if err := s.botAllowed(s.botLang(ctx), st.Features, target); err != nil {
		s.botNote(ctx, msg.MessageID, err.Error())
		return
	}
	if err := s.botSend(ctx, &ev, content, target); err != nil {
		s.logf("bots: send: %v", err)
		s.botNote(ctx, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
}

const (
	botImagesMax = 4
)

// botSend passes a message on to its target and starts the run that answers it.
func (s core) botSend(ctx context.Context, ev *botcard.MessageEvent, content botcard.Content, t botTarget) error {
	return s.botSendIn(ctx, ev, content, t, nil)
}

// botSendIn is botSend for a message said in a topic. It imports no quotes or forwards from that group,
// and the run's progress, answer and notes go back into that thread (runTo).
func (s core) botSendIn(ctx context.Context, ev *botcard.MessageEvent, content botcard.Content, t botTarget, topic *feishuTopic) error {
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return err
	}
	msg := ev.Message
	reaction, err := s.botReact(ctx, msg.MessageID, "OnIt")
	if err != nil {
		s.logf("bots: react: %v", err)
	}
	started := false
	defer func() {
		if !started && reaction != "" {
			s.botUnreact(context.WithoutCancel(ctx), msg.MessageID, reaction)
		}
	}()
	hello := s.hello(ctx)
	lang := i18n.Lang(hello.Locale)
	in := prompts.BotMessage{Persona: !t.Agent(), Topic: topic != nil, Owner: strings.TrimSpace(hello.Owner), Language: s.h.AnswerLanguage(hello.Locale), Text: content.Text}
	if topic != nil {
		in.UnboundTopic = topic.UnboundMode != ""
		if topic.UnboundMode == assistantwire.TopicMainDecides {
			in.TopicMessageID = msg.MessageID
		}
	}
	if t.TodoID != "" && topic == nil {
		in.Todo = s.h.ReminderTodo(ctx, t.TodoID)
	}
	to := topic.target()
	// The media to pass on: this message's own, then the pictures and files of the message it quotes (the owner
	// replies to a picture to ask about it), each downloaded with the message it is in.
	type botMedia struct {
		of     string // the message it is in
		quoted bool   // not to be had is no reason to hold the message back
		botcard.Media
	}
	var media []botMedia
	for _, md := range content.Media {
		media = append(media, botMedia{of: msg.MessageID, Media: md})
	}
	if msg.ParentID != "" && topic == nil {
		var quoted []botcard.Media
		in.Quote, quoted = s.botQuote(ctx, msg.ParentID, lang)
		for _, md := range quoted {
			media = append(media, botMedia{of: msg.ParentID, quoted: true, Media: md})
		}
	}
	if content.Forward && topic == nil {
		in.Forward = s.botForward(ctx, msg.MessageID, lang)
	}
	var images []proto.Image
	var tooLarge []string // files too large to pass on
	for _, bm := range media {
		md := bm.Media
		if md.Kind == "image" {
			var res struct {
				Data     []byte `json:"data"`
				MIME     string `json:"mime"`
				Path     string `json:"path"`
				Name     string `json:"name"`
				TooLarge bool   `json:"too_large"`
			}
			if err := s.appCall(ctx, FeishuResource, map[string]any{"message_id": bm.of, "key": md.Key, "kind": md.Kind,
				"machine_id": rt.Machine, "as_file": len(images) >= botImagesMax}, &res); err != nil {
				if bm.quoted {
					s.logf("bots: the quoted message's image: %v", err)
					continue
				}
				var coded *rpc.Error
				if errors.As(err, &coded) && coded.Code == assistantwire.CodeFeishuSave {
					return fmt.Errorf("%s%w", i18n.T(s.botLang(ctx), "assistant-feishu.save_file", i18n.Args{"machine": rt.MachineName}), err)
				}
				return fmt.Errorf("%s%w", i18n.T(s.botLang(ctx), "assistant-feishu.download_image"), err)
			}
			if res.TooLarge || (res.Name != "" && res.Path == "") {
				tooLarge = append(tooLarge, res.Name)
				continue
			}
			if res.Path != "" {
				in.Files = append(in.Files, res.Path)
				continue
			}
			mime := res.MIME
			if !strings.HasPrefix(mime, "image/") {
				mime = "image/png"
			}
			images = append(images, proto.Image{Mime: mime, Data: base64.StdEncoding.EncodeToString(res.Data)})
			continue
		}
		var saved struct {
			Path     string `json:"path"`
			TooLarge bool   `json:"too_large"`
		}
		if err := s.appCall(ctx, FeishuSave, map[string]any{"message_id": bm.of, "key": md.Key, "kind": md.Kind, "name": md.Name,
			"machine_id": rt.Machine}, &saved); err != nil {
			if bm.quoted {
				s.logf("bots: the quoted message's file: %v", err)
				continue
			}
			return fmt.Errorf("%s%w", i18n.T(s.botLang(ctx), "assistant-feishu.save_file", i18n.Args{"machine": rt.MachineName}), err)
		}
		if saved.TooLarge || saved.Path == "" {
			tooLarge = append(tooLarge, md.Name) // left out: the rest of the message goes, and a line says so
			continue
		}
		in.Files = append(in.Files, saved.Path)
	}
	if s.botRecalled(msg.MessageID) { // recalled while its pictures and files came down
		s.logf("bots: a message recalled before it went in: not sent")
		return nil
	}
	text := prompts.RenderBotMessage(in)
	if strings.TrimSpace(content.Text) == "" && in.Quote == nil && in.Forward == "" && len(images) == 0 && len(in.Files) == 0 {
		if len(tooLarge) > 0 {
			return errors.New(i18n.T(s.botLang(ctx), "assistant-feishu.file_too_large_only", i18n.Args{"files": strings.Join(tooLarge, listSep(s.botLang(ctx)))}))
		}
		return errors.New(i18n.T(s.botLang(ctx), "assistant-feishu.cannot_read"))
	}
	if len(tooLarge) > 0 {
		defer func() {
			if started {
				s.botNoteTo(context.WithoutCancel(ctx), to, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-feishu.file_too_large", i18n.Args{"files": strings.Join(tooLarge, listSep(s.botLang(ctx)))}))
			}
		}()
	}

	ext := map[string]string{}
	if topic != nil {
		ext["chat"], ext["thread"] = topic.Chat, topic.Thread
		if in.TopicMessageID != "" {
			ext[assistantwire.FeishuTopicMayOpenTodo] = topic.Pairing
		}
	}
	if err := s.h.StartRun(ctx, rt, t, msg.MessageID, text, images, botProbe(content.Text, in), reaction, ext); err != nil {
		return err
	}
	started = true
	return nil
}

// botProbe picks what of a message to look for in the transcript: its first line, else something only it has.
func botProbe(text string, in prompts.BotMessage) string {
	var fallbacks []string
	if len(in.Files) > 0 {
		fallbacks = append(fallbacks, in.Files[0])
	}
	fallbacks = append(fallbacks, in.Forward)
	if in.Quote != nil {
		fallbacks = append(fallbacks, in.Quote.Text)
	}
	return channel.Probe(text, fallbacks...)
}

// botQuote is the message a message replies to, and the pictures and files in it.
func (s core) botQuote(ctx context.Context, id string, lang i18n.Lang) (*prompts.BotQuote, []botcard.Media) {
	items, err := s.botMessages(ctx, id)
	if err != nil || len(items) == 0 {
		return nil, nil
	}
	it := items[0]
	if it.Deleted {
		return nil, nil
	}
	c := botcard.ParseContent(it.MsgType, it.Body.Content, it.MentionList())
	who := "Persona"
	if it.Sender.SenderType == "user" {
		who = s.botName(ctx, it.Sender.ID, lang)
	}
	if c.Text == "" {
		c.Text = "[" + it.MsgType + "]"
	}
	return &prompts.BotQuote{ID: id, Who: who, Text: clip(c.Text, 4000)}, c.Media
}

// botForward reads a merged forward: one line per message, oldest first.
func (s core) botForward(ctx context.Context, id string, lang i18n.Lang) string {
	items, err := s.botMessages(ctx, id)
	if err != nil {
		return i18n.T(lang, "assistant-feishu.forward_error", i18n.Args{"error": i18n.Verbatim(err.Error())})
	}
	loc := s.h.Location(ctx)
	var lines []string
	n := 0
	for _, it := range items {
		if it.UpperMessageID != id {
			continue
		}
		c := botcard.ParseContent(it.MsgType, it.Body.Content, it.MentionList())
		text := c.Text
		if text == "" {
			text = "[" + it.MsgType + "]"
			if it.MsgType == "image" && len(c.Media) != 0 {
				// A standalone image has no text. Rich posts already carry their own media placeholders.
				text = i18n.T(lang, "assistant-feishu.forward_image")
			}
		}
		who := i18n.T(lang, "assistant-feishu.unknown_sender")
		if it.Sender.SenderType == "user" {
			who = s.botName(ctx, it.Sender.ID, lang)
		} else if it.Sender.SenderType == "app" {
			who = i18n.T(lang, "assistant-feishu.bot_sender")
		}
		line := i18n.T(lang, "assistant-feishu.forward_line", i18n.Args{
			"time": it.At().In(loc).Format("01-02 15:04"), "sender": i18n.Verbatim(who), "text": i18n.Verbatim(text),
		})
		n += len([]rune(line))
		if n > 30000 {
			lines = append(lines, i18n.T(lang, "assistant-feishu.forward_clipped"))
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return i18n.T(lang, "assistant-feishu.forward_empty")
	}
	return strings.Join(lines, "\n")
}
