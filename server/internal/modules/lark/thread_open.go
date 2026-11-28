package lark

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// openNotesMax bounds the first words kept as the to-do's notes, in characters.
const openNotesMax = 4000

// isTopicRoot says whether ev is the message that starts its topic: it is in a topic (it has a thread_id) and either
// replies to nothing (no root_id) or is its own root (root_id is its own message id). Checked with the message API on
// a real topic group, not with the event push (the two carry the same fields): the first message has an empty
// root_id and parent_id and a thread_id, a reply has both set. The second form is kept as a second accepted shape.
// A wrong guess either opens nothing (the owner sends "/ai") or opens a to-do for a reply.
func isTopicRoot(ev botcard.MessageEvent) bool {
	m := ev.Message
	return m.ThreadID != "" && (m.RootID == "" || m.RootID == m.MessageID)
}

// aiCommand is the line after "/ai" (or the full-width "／ai", whatever its case) when text starts with it as a word of
// its own ("/ai fix it", "/ai", but not "/aim"); ok is false for any other text.
func aiCommand(text string) (rest string, ok bool) { return botcard.AICommand(text) }

// openingWords are the owner's words in ev as an opening reads them: only a text or a rich-text message has any (a
// voice message, a shared group or card, a location, a card, a file and a message of a type not known come as a
// placeholder like "[语音消息]", which are no words); the bot's @ at the start is taken off when separated by whitespace
// (in a group where the bot only gets what @s it, every message starts with one; content has it as the name, the
// message's own text as @_user_N), and an @ further in is the person's name when the message's mentions give it, else
// left as it stands. A rich-text message of only pictures or videos has no words either. "" when there are none.
func openingWords(ev botcard.MessageEvent, content botcard.Content) string {
	return extractOpeningWords(ev, content, false)
}

// openingCommandWords extracts the owner's words for command parsing (/ai, /read, /noreply): for rich-text posts it
// strips leading structured at elements through their mention IDs so an adjacent command keeps its boundary (#12668),
// without removing mentions that touch punctuation in ordinary topic-opening notes and quotes (#12932).
func openingCommandWords(ev botcard.MessageEvent, content botcard.Content) string {
	return extractOpeningWords(ev, content, true)
}

func extractOpeningWords(ev botcard.MessageEvent, content botcard.Content, structuredPostMentions bool) string {
	m := ev.Message
	var text string
	switch m.MessageType {
	case "text":
		text = botcard.ParseContent(m.MessageType, m.Content, nil).Text // with the @_user_N keys still in it
		text = trimOpeningMentions(text, m.Mentions, unicode.IsSpace)
	case "post":
		if structuredPostMentions {
			text = content.StripLeadingMentions(m.Mentions, nil)
		} else {
			if stripped := content.StripLeadingMentions(m.Mentions, nil); !hasOpeningWords(stripped) {
				return ""
			}
			text = trimOpeningMentions(content.Text, m.Mentions, unicode.IsSpace)
		}
	default:
		return ""
	}
	for _, mn := range m.Mentions {
		if mn.Key != "" && mn.Name != "" {
			// A whole key: "@_user_1" is not the start of "@_user_10".
			text = regexp.MustCompile(regexp.QuoteMeta(mn.Key)+`\b`).ReplaceAllLiteralString(text, "@"+mn.Name)
		}
	}
	if !hasOpeningWords(text) {
		return ""
	}
	return text
}

func hasOpeningWords(text string) bool {
	// The placeholders botcard.ParseContent writes for a picture and a video in rich text.
	return strings.TrimSpace(strings.NewReplacer("[图片]", "", "[视频]", "").Replace(text)) != "" // i18n-ignore: matched against the text parsePost writes
}

func trimOpeningMentions(text string, mentions []botcard.Mention, separator func(rune) bool) string {
	text = strings.TrimLeftFunc(strings.TrimSpace(text), separator)
	for cut := true; cut; {
		cut = false
		for _, mn := range mentions {
			for _, tok := range []string{mn.Key, "@" + mn.Name} {
				if tok == "" || tok == "@" {
					continue
				}
				// A whole word: "@_user_1" is not the start of "@_user_10".
				if rest, ok := strings.CutPrefix(text, tok); ok {
					r, _ := utf8.DecodeRuneInString(rest)
					if rest == "" || separator(r) {
						text, cut = strings.TrimLeftFunc(rest, separator), true
					}
				}
			}
		}
	}
	return text
}

var (
	// Media placeholders emitted by Lark, and common attachment/link icons in copied text.
	topicAttachment = lazyre.New(`(?i)^(?:\[(?:图片|视频|文件|附件|image|video|file|attachment)(?:[:：][^\]\r\n]*)?\]|[📎🔗]\x{FE0F}?)`) // i18n-ignore: incoming message text
	topicURL        = lazyre.New(`(?i)^<?(?:https?://|www\.)[^\s\p{Z}<>，。！？；、]+>?`)
	topicFilename   = lazyre.New(`^[^\s\p{Z}/\\]+\.[A-Za-z0-9]{1,8}[\s\p{Z}]+`)
)

// openingTitleWords chooses the temporary display name. The full parsed message still supplies
// the notes, the handoff and the model's later title.
func openingTitleWords(text string, content botcard.Content, mentions []botcard.Mention) (words string, linkLabel bool) {
	if stripped := content.StripLeadingMentions(mentions, nil); stripped != content.Text && (text == content.Text || text == stripped) {
		text = trimOpeningMentions(stripped, mentions, topicSeparator)
	} else if strings.HasPrefix(text, "@") {
		text = trimOpeningMentions(text, mentions, topicSeparator)
	}
	for text != "" {
		if label, rest, ok := content.CutPostLink(text); ok {
			label = strings.TrimSpace(label)
			if label != "" && !topicURL.MatchString(label) && topicAttachment.FindString(label) != label && !genericTopicRequest(label) {
				return label, true
			}
			text = trimOpeningMentions(rest, mentions, topicSeparator)
			continue
		}
		prefix := topicAttachment.FindString(text)
		if prefix == "" {
			prefix = topicURL.FindString(text)
		}
		if prefix == "" {
			// A filename can itself be the subject ("slides.pdf needs edits"). Skip it only beside a shared resource.
			if name := topicFilename.FindString(text); name != "" {
				rest := text[len(name):]
				if _, _, link := content.CutPostLink(rest); link || topicURL.MatchString(rest) || topicAttachment.MatchString(rest) {
					prefix = name
				}
			}
		}
		if prefix == "" {
			break
		}
		text = trimOpeningMentions(text[len(prefix):], mentions, topicSeparator)
	}
	if genericTopicRequest(text) {
		return "", false
	}
	return text, false
}

func topicSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(",;:，；：、", r) // i18n-ignore: punctuation in the owner's incoming words, matched as written
}

// Match a whole request, so "Have a look at the seed order" keeps its subject.
func genericTopicRequest(text string) bool {
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
		return true
	}
	switch strings.ToLower(strings.Trim(strings.TrimSpace(text), ".!?。！？")) { // i18n-ignore: punctuation in the owner's incoming words, matched as written
	case "have a look", "take a look", "please have a look", "please take a look",
		"看看", "看一下", "看下", "帮我看看", "请看一下", "見てください", "確認してください": // i18n-ignore: the owner's incoming words
		return true
	}
	return false
}

var topicTodoRequest = lazyre.New(`(?i)^(?:(?:please +)?(?:make|create|add) +(?:me +)?a +to-do(?: +(?:to|for) +| *[:：] *|$)|(?:请)?(?:建个待办|创建待办|新建待办)(?: *[:：] *| +|$)|ToDoを作って(?:ください)?(?: *[:：] *| +|$))`) // i18n-ignore: incoming request phrases

func topicTodoCommand(text string) (string, bool) {
	if rest, ok := aiCommand(text); ok {
		return rest, true
	}
	text = strings.TrimSpace(text)
	if len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == '/' || r == '／' { // i18n-ignore: command syntax
			name := text[size:]
			if at := strings.IndexFunc(name, unicode.IsSpace); at >= 0 {
				if strings.EqualFold(name[:at], "todo") {
					return strings.TrimSpace(name[at:]), true
				}
			} else if strings.EqualFold(name, "todo") {
				return "", true
			}
		}
	}
	return "", false
}

func topicOpening(ev botcard.MessageEvent, content botcard.Content, mode assistantwire.TopicTodoMode) (string, bool) {
	words := openingCommandWords(ev, content)
	if text, ok := topicTodoCommand(words); ok {
		return text, true
	}
	if prefix := topicTodoRequest.FindString(words); prefix != "" {
		return strings.TrimSpace(words[len(prefix):]), true
	}
	if mode == assistantwire.TopicAlwaysTodo && isTopicRoot(ev) {
		return openingWords(ev, content), true
	}
	return "", false
}

// openTopicTodo makes the to-do for the topic ev is in, with text (the first words, the command taken off) as its
// notes, and hands the words to the main conversation (openTodoIn); what keeps it from doing so is said in the topic.
func (s core) openTopicTodo(ctx context.Context, ev botcard.MessageEvent, st FeishuStatus, content botcard.Content, text string, topic *feishuTopic) {
	if _, fail := s.openTodoIn(ctx, ev, st, content, text, "", topic, nil, nil); fail != "" {
		s.botNoteTo(ctx, topic.target(), ev.Message.MessageID, fail)
	}
}

type topicTodoDecision struct {
	title, origin string
}

// DecideTopicTodo opens a to-do for a main-conversation run that is already answering in this topic.
func (l *Layer) DecideTopicTodo(ctx context.Context, run channel.Run, title, notes string) (string, error) {
	s := l.chat()
	topic := &feishuTopic{Chat: run.Ext["chat"], Thread: run.Ext["thread"]}
	st, err := s.feishuStatus(ctx)
	if err != nil {
		return "", err
	}
	if st.TopicTodoModes[topic.Chat] != assistantwire.TopicMainDecides || s.botRecalled(run.ReplyTo) ||
		run.Ext[assistantwire.FeishuTopicMayOpenTodo] != pairingKey(st) {
		return "", errors.New("this Lark topic no longer allows the main conversation to open a to-do")
	}
	var ev botcard.MessageEvent
	ev.Message.MessageID = run.ReplyTo
	if notes == "" {
		notes = title
	}
	todo, fail := s.openTodoIn(ctx, ev, st, botcard.Content{}, notes, "", topic, nil, &topicTodoDecision{title: title, origin: run.SessionID})
	if fail != "" {
		return "", errors.New(fail)
	}
	return todo, nil
}

// openTodoIn makes the to-do for the topic, with text (the first words, the command taken off) as its title and notes,
// and hands the words to the main conversation. Who takes it is worked out before anything is made, and a topic that
// turns out to be taken (the core's db.ErrThreadTaken) leaves no to-do behind. The first words are not a decision: the to-do is made
// of them. ev is the message the run answers under in the topic: the owner's own, or the one the bot opened it with
// (openFromElsewhere). quote, when not empty, is the text of a message the words reply to: it follows them in the notes
// and in the hand-over, as context. src, when given, is where the request came from, recorded on the to-do. A nil topic
// is a to-do bound to no topic (a request taken in the owner's private chat, openInOwnerChat): ev is then a message of that
// chat, and what is said goes there. todo is the to-do's id; fail says why no to-do was made, in the bot's language, ""
// when one was; what goes wrong after the to-do exists is said in the topic (or the private chat). A decision from
// the main conversation is already in a run, so it receives the to-do ID without another handoff.
func (s core) openTodoIn(ctx context.Context, ev botcard.MessageEvent, st FeishuStatus, content botcard.Content, text, quote string,
	topic *feishuTopic, src *assistantwire.ChatTodoSource, decision *topicTodoDecision) (todo, fail string) {
	msg := ev.Message
	l := s.botLang(ctx)
	to := topic.target()
	where := "private chat" // for the log
	if topic != nil {
		where = "topic " + topic.Thread
	}
	say := func(key string, args ...i18n.Args) {
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu."+key, args...))
	}
	failed := func(key string, err error) string {
		return i18n.T(l, "assistant-feishu."+key, i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	if text == "" {
		// A to-do is named after what was said: a picture, a merged forward, a voice message or the like alone has no
		// words to name it by.
		return "", i18n.T(l, "assistant-feishu.thread_open_needs_text")
	}
	target, origin, why := s.openTarget(ctx, st)
	if why != "" {
		return "", why
	}
	// What the owner said, and apart from it what someone else said in the message it replied to: the notes carry both, the
	// second under a label that says whose it is and with nothing in it that closes a context block.
	words := text
	if quote != "" {
		words += "\n\n" + i18n.T(l, "assistant-feishu.elsewhere_quote_label") + "\n" + prompts.ContextBody(quote)
	}
	var title string
	var retitle bool
	if decision == nil {
		titleWords, linkLabel := text, false
		if topic != nil && src == nil {
			titleWords, linkLabel = openingTitleWords(text, content, msg.Mentions)
			if titleWords == "" {
				titleWords = i18n.T(l, "assistant-tool_titles.todo_create")
			}
		}
		title, retitle = s.h.TodoTitle(ctx, titleWords, l, linkLabel)
	} else {
		if origin != decision.origin {
			return "", i18n.T(l, "assistant-feishu.thread_open_no_main")
		}
		if existing, ok := s.h.TodoOfThread(topic.Chat, topic.Thread); ok && !existing.Done {
			return existing.ID, "" // this topic already has an open to-do
		}
		title, _ = s.h.TodoTitle(ctx, decision.title, l, true)
	}
	if src != nil && src.Anchor == "" {
		src.Handoff = msg.MessageID
	}
	// The to-do is made, bound to the topic, given its source, supervised by the main conversation and shown in its
	// feed by the core; anything that fails there takes the to-do back.
	todo, err := s.h.OpenTodo(ctx, assistantwire.ChatOpenTodo{Title: title, Notes: clip(words, openNotesMax), Origin: origin,
		Topic: topic != nil, ChatID: topicChat(topic), ThreadID: topicThread(topic), Source: src, Retitle: retitle, Text: text, Where: where})
	if err != nil {
		return "", failed("thread_open_failed", err)
	}
	s.logf("bots: %s opened todo %s", where, todo)

	if decision != nil {
		say("thread_decided", i18n.Args{"title": title})
		return todo, ""
	}

	line := i18n.T(l, "assistant-feishu.thread_open_done", i18n.Args{"title": title})
	if link := s.h.TodoLink(strings.TrimRight(st.PublicURL, "/"), todo); isWebLink(link) {
		line += "\n" + link
	}
	s.botNoteTo(ctx, to, msg.MessageID, line)

	content.Text = prompts.RenderTodoThreadOpen(prompts.TodoThreadOpen{Language: s.h.AnswerLanguage(s.hello(ctx).Locale), Title: contextText(clip(oneLine(title), 80)), TodoID: todo, Text: text, Quote: quote, NoTopic: topic == nil})
	if err := s.botSendIn(ctx, &ev, content, target, topic); err != nil {
		s.logf("bots: %s: todo %s to the main conversation: %v", where, todo, err)
		say("thread_open_not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	return todo, ""
}

// topicChat and topicThread are a topic's chat and thread, "" for none.
func topicChat(t *feishuTopic) string {
	if t == nil {
		return ""
	}
	return t.Chat
}

func topicThread(t *feishuTopic) string {
	if t == nil {
		return ""
	}
	return t.Thread
}

// isWebLink: link is an http(s) address with a host (what a message can carry as a link; "" is none).
func isWebLink(link string) bool {
	u, err := url.Parse(link)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
