package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// What the core says of an agent's question, permission, plan or terminal dialog to the chat that shows it as a card
// (the lark module's asks.go and ask_cards.go, #13404): who asks and for which task, the conversation's page and the
// core's wording of it as one value (ixView), where its card goes as the channels' runs say (ixRoute), and whether
// its words are sensitive (ixSensitive). The cards themselves, their state and their answers are the module's.

// ixView is what ix's card says of the core (assistantwire.AskView).
func (s *Service) ixView(ctx context.Context, ix proto.Interaction) assistantwire.AskView {
	var v assistantwire.AskView
	v.Agent, v.Machine, v.Secondary = s.feishuAgentPlaceParts(ctx, ix)
	v.Task, v.TaskSame = s.ixTask(ctx, ix)
	v.OpenLabel, v.OpenURL = s.openLink(ctx, ix.MachineID, ix.SessionID)
	v.OpenInline = boardLinkOK(v.OpenURL)
	v.Tool = ixToolName(ix)
	if v.Terminal = terminalQuestion(ix); v.Terminal {
		v.TerminalText = terminalQuestionText(ix)
	}
	v.ExactProposal = ownerActionProposal(ix)
	return v
}

// ixRoute is where ix's card goes (assistantwire.AskRoute): whether a run of the chat is on its conversation, the
// runs it answers to (interactionRuns), and whether its conversation is the main one.
func (s *Service) ixRoute(ctx context.Context, ix proto.Interaction) assistantwire.AskRoute {
	var out assistantwire.AskRoute
	for _, r := range s.channelRuns(channel.Feishu) {
		if r.MachineID == ix.MachineID && r.SessionID == ix.SessionID {
			out.Driven = true
			break
		}
		// The hub's own approvals (mail_send) do not name the conversation: one on the assistant's machine while a
		// Persona run goes on is most likely that run's.
		if ix.SessionID == "" && r.TargetKind != targetAgent && r.MachineID == ix.MachineID {
			out.Driven = true
			break
		}
	}
	private, topic := s.interactionRuns(ix)
	if private != nil {
		out.Private = &assistantwire.AskRun{ReplyTo: private.ReplyTo}
	}
	if topic != nil {
		out.Topic = &assistantwire.AskRun{ReplyTo: topic.ReplyTo}
		if ext := s.runExts(topic, "chat", "thread"); ext["chat"] != "" && ext["thread"] != "" {
			out.Topic.To = assistantwire.ChannelTarget{ChatID: ext["chat"], InThread: true}
		}
	}
	out.Main = ix.SessionID != "" && s.isMainOn(ctx, ix.MachineID, ix.SessionID)
	return out
}

// ixSensitive: ix's card says something sensitive (its words and its track's default categories): "all cards" does
// not take it, only a chat that drives its conversation (health.go, info_use.go).
func (s *Service) ixSensitive(ctx context.Context, ix proto.Interaction) bool {
	return s.exitPolicy(ctx, interactionText(ix), ix.SessionID, s.ixSpace(ctx, ix)).Redact
}

// ixState is the core's live record of interaction id: ended (interaction.ended came, maybe while its card was
// being sent) and pending (it is up now).
func (s *Service) ixState(id string) (ended, pending bool) {
	_, pending = s.pendingIx(id)
	return s.interactionEnded(id), pending
}

// interactionRuns are the runs that decide where ix's card goes: the latest asked in the owner's DM and the latest
// asked in a topic, of the conversation it belongs to (either may be nil). While a /read of the conversation goes on,
// the card may be that run's, and a /read's group gets its answer and nothing else: the card goes to the owner's DM,
// so no topic is returned either (another topic's run of the same conversation would take the /read's question into
// its group, and a line in that topic would point at a card it has nothing to do with).
func (s *Service) interactionRuns(ix proto.Interaction) (private, topic *channel.Run) {
	read := false
	for _, r := range s.channelRuns(channel.Feishu) {
		if r.MachineID != ix.MachineID || r.SessionID != ix.SessionID {
			continue
		}
		if s.runExt(&r, assistantwire.FeishuReadChatKey) != "" {
			read = true
			continue
		}
		if ext := s.runExts(&r, "chat", "thread"); ext["chat"] == "" || ext["thread"] == "" {
			if private == nil || !r.Started.Before(private.Started) {
				private = &r
			}
		} else if topic == nil || !r.Started.Before(topic.Started) {
			topic = &r
		}
	}
	if read {
		topic = nil
	}
	return private, topic
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

// ixTask is the task ix's agent works on as the cards name it: its direction and title; "" when it works on none. same:
// the agent's own title is the task's, so the label is the direction alone, to go before the agent. A card closing after
// its task ended or its session moved on keeps the task it was posted for when that name is available.
func (s *Service) ixTask(ctx context.Context, ix proto.Interaction) (label string, same bool) {
	machine := s.canonical(ix.MachineID)
	t, ok := s.taskOf(ctx, machine, ix.SessionID)
	if ix.TaskTitle != "" && (!ok || t.Title != ix.TaskTitle) {
		t, ok = s.closedTaskOf(ctx, machine, ix.SessionID)
		if !ok || t.Title != ix.TaskTitle {
			return clip(oneLine(ix.TaskTitle), 70), false
		}
	}
	task := clip(oneLine(t.Title), 70)
	if !ok || task == "" {
		return "", false
	}
	direction := s.feishuDirection(ctx, t, s.feishuDirections(ctx))
	if transcript.Title(ix.SessionTitle) == t.Title {
		return direction, true
	}
	return direction + task, false
}

// jsonStrings are the string values in raw (a tool's input, its secrets already redacted), not its keys or braces.
func jsonStrings(raw string) []string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			keys := slices.Sorted(maps.Keys(v))
			for _, k := range keys {
				walk(v[k])
			}
		}
	}
	walk(v)
	return out
}

// pendingQuestion is one line saying what a pending interaction waits for; "" when it says nothing.
func pendingQuestion(ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxQuestion:
		return strings.TrimSpace(questionSummary(ix))
	case proto.IxPlan:
		return strings.TrimSpace(planTitle(ix.Plan))
	case proto.IxPermission:
		return strings.TrimSpace(ixToolName(ix) + " " + oneLine(ix.Summary))
	}
	return strings.TrimSpace(oneLine(ix.Summary))
}
