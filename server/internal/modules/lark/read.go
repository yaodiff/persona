package lark

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Reading a group with /read (#11803, docs/extensions/bots.md "Reading a group with /read"). The owner writes
// "@bot /read <task>" in an alert or problem group; the main conversation reads the group as the owner with lark-cli
// on its own computer (the last readGroupDefault messages, or the range the task names), downloads its images and
// files into a directory of their own, checks the leads it found with the computer's read-only tools before it
// concludes (#16808), does the task, and the answer goes back as a reply under that message.
//
// The bot is given no way to read a group's history: the owner is in the group, and lark-cli reads as the owner, so no
// sensitive "read every group message" permission is needed. The hub let the message through (extmgr/bots_read.go);
// this is the second line of checks.
//
// The run is quiet in the group: the message gets OnIt while it works and DONE at the end, and no progress card, steps
// or web link go there; a held one that works past a minute tells the owner privately (readWorking). The answer goes to the owner's private chat first, and into the group only when the owner
// presses its button (read_held.go, #14437); with the read_group_direct switch on when the owner asked
// (readDirectKey), it goes straight under the message as before. Where it goes is kept in the run's own keys
// (readChatKey, readThreadKey), not a topic's ("chat", "thread"): a topic's progress card, agent message and question
// cards never apply to it, and a question or approval the run raises goes to the owner's private chat
// (interactionRuns).

const (
	// readGroupDefault is how many messages are read when the task names no range; readGroupMost the most for any
	// range ("read everything" included).
	readGroupDefault = 40
	readGroupMost    = 500
	// readFileMB and readTotalMB bound what is downloaded: the bot's own limit for a file it passes on (50 MB), and
	// four of them in all. readImages is how many pictures the model looks at.
	readFileMB  = 50
	readTotalMB = 200
	readImages  = 20
	// readDirRoot is where the downloads go on the conversation's computer, one directory per /read message.
	readDirRoot = "~/.persona/lark-read/"
	// readInvestigateMinutes and readInvestigateCalls bound the investigation after the read (#16808): the model
	// checks the group's leads (log IDs, errors, attachments) with the computer's read-only tools before it concludes,
	// and past these it stops and answers with what it has. Far inside the run's own limit (channel.RunMost), so the
	// answer is a staged conclusion, not a run that never ends.
	readInvestigateMinutes = 20
	readInvestigateCalls   = 40
	// readWorkingAfter is how long a held /read works before the owner's private chat is told it is on it
	// (readWorking): a quick answer needs no note, a long investigation must not look like no response.
	readWorkingAfter = time.Minute

	// readAskKey is the run's channel value of the owner's words, clipped to readAskRunes, for the line on the held
	// answer's card (read_held.go).
	readAskKey   = "read_ask"
	readAskRunes = 60
)

// readDirName keeps a message id usable as a directory name in a shell command unquoted.
var readDirName = lazyre.New(`[^A-Za-z0-9_-]`)

// feishuRead takes the owner's "@bot /read …" from a group chat (FeishuFromRead): the hub's checks again, then the
// request goes to the main conversation as a run that answers under the message. What keeps it from going is said to
// the owner where the answer would go: privately (readHeldNote) unless the answer goes straight into the group, and
// nothing else is said in the group.
func (s core) feishuRead(ev botcard.MessageEvent, st FeishuStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	msg := ev.Message
	if ev.Sender.SenderType != "user" || st.Owner == "" || ev.Sender.SenderID.OpenID != st.Owner {
		s.logf("bots: a /read that was not the owner's was dropped")
		return
	}
	if !st.Features.ReadGroup {
		s.logf("bots: a /read with the switch off was dropped")
		return
	}
	if msg.ChatType == "p2p" || msg.ChatID == "" || msg.MessageID == "" || msg.MessageType != "text" && msg.MessageType != "post" {
		s.logf("bots: a message marked as a /read was not one from a group: dropped")
		return
	}
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return
	}
	task, ok := botcard.ReadCommand(openingCommandWords(ev, content))
	if !ok {
		s.logf("bots: a message marked as a /read was not one: dropped")
		return
	}
	if s.botRecalled(msg.MessageID) {
		return
	}
	l := s.botLang(ctx)
	direct := st.Features.ReadGroupDirect
	note := func(text string) {
		if direct {
			s.botNoteTo(ctx, feishuTarget{ChatID: msg.ChatID, InThread: msg.ThreadID != "", NoDetour: true, Read: true}, msg.MessageID, text)
			return
		}
		s.readHeldNote(ctx, msg.ChatID, readAsk(task), text)
	}
	t := botTarget{Kind: targetMain}
	if err := s.botAllowed(l, st.Features, t); err != nil {
		note(err.Error())
		return
	}
	if err := s.readSend(ctx, ev, task, t, direct); err != nil {
		s.logf("bots: /read: %v", err)
		note(i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
}

// readAsk is the owner's words after /read as the held answer's card shows them: one line, clipped.
func readAsk(task string) string { return clip(oneLine(task), readAskRunes) }

// readSend sends the request into the main conversation (or starts it) and starts the run that answers under the
// message, straight into the group when direct, else held for the owner. The reaction says it was taken; nothing more
// is posted until the answer.
func (s core) readSend(ctx context.Context, ev botcard.MessageEvent, task string, t botTarget, direct bool) error {
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return err
	}
	msg := ev.Message
	reaction, err := s.botReact(ctx, msg.MessageID, "OnIt")
	if err != nil {
		s.logf("bots: /read: react: %v", err)
	}
	hello := s.hello(ctx)
	text := prompts.RenderBotReadGroup(readPrompt(strings.TrimSpace(hello.Owner), s.h.AnswerLanguage(hello.Locale), msg.ChatID, msg.MessageID, msg.ThreadID, task, direct))
	ext := map[string]string{readChatKey: msg.ChatID}
	if msg.ThreadID != "" {
		ext[readThreadKey] = msg.ThreadID
	}
	if direct {
		ext[readDirectKey] = "1"
	} else if ask := readAsk(task); ask != "" {
		ext[readAskKey] = ask
	}
	if err := s.h.StartRun(ctx, rt, t, msg.MessageID, text, nil, channelProbe(task, msg.MessageID), reaction, ext); err != nil {
		if reaction != "" {
			s.botUnreact(context.WithoutCancel(ctx), msg.MessageID, reaction)
		}
		return err
	}
	return nil
}

// readPrompt is what the main conversation is sent for a /read: where to read, the limits, the task, and where the
// answer goes (direct: straight into the group).
func readPrompt(owner, lang, chat, message, thread, task string, direct bool) prompts.BotReadGroup {
	return prompts.BotReadGroup{Owner: owner, Language: lang, ChatID: chat, MessageID: message, ThreadID: thread, Task: task,
		Dir: readDirRoot + readDirName.ReplaceAllString(message, "_"), Default: readGroupDefault, Most: readGroupMost,
		FileMB: readFileMB, TotalMB: readTotalMB, Images: readImages, Minutes: readInvestigateMinutes,
		Calls: readInvestigateCalls, Direct: direct}
}

// channelProbe is what of a /read to look for in the transcript: the task's first line, else the message id the
// prompt carries.
func channelProbe(task, message string) string {
	if strings.TrimSpace(task) == "" {
		return message
	}
	return botProbe(task, prompts.BotMessage{})
}
