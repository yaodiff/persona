package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/wireids"
)

// ---- the routes: the manager's ----

// ---- what the manager tells this code (in-process, not the hub) ----

func init() { onAssistantEvent(assistantwire.EventInboxChanged, (*Manager).busInboxChanged) }

// busInboxChanged is inbox.changed: the extensions hear of the items (inbox.changed), the apps get {"type":"inbox"}.
// It is told by one worker (tellInboxChanged), off the bus's read loop: an extension slow to take its event (its pipe
// full) must not hold up the manager's other calls and events. A backlog past inboxChangedQueue is not told to the
// extensions; the apps still hear of it.
func (h *Manager) busInboxChanged(params json.RawMessage) {
	var p assistantwire.InboxChanged
	if err := json.Unmarshal(params, &p); err != nil {
		h.logf("inbox.changed: %v", err)
		return
	}
	h.inbox.tellOnce.Do(func() {
		h.inbox.changed = make(chan assistantwire.InboxChanged, inboxChangedQueue)
		go h.tellInboxChanged()
	})
	select {
	case h.inbox.changed <- p:
	default:
		h.logf("inbox: the extensions are behind: %d changed items not told to them", len(p.Items))
		h.broadcast(map[string]any{"type": "inbox"})
	}
}

// inboxChangedQueue is how many inbox.changed wait for the worker at most.
const inboxChangedQueue = 256

// tellInboxChanged tells the extensions of the changed items (inbox.changed) and the apps {"type":"inbox"}.
func (h *Manager) tellInboxChanged() {
	for p := range h.inbox.changed {
		for _, it := range p.Items {
			h.extensions.emit("inbox.changed", map[string]any{"id": it.ID, "source": it.Source, "kind": it.Kind, "title": it.Title})
		}
		h.broadcast(map[string]any{"type": "inbox"})
	}
}

// ---- what this code asks the manager about the inbox ----

// inboxItemRaw is an item as the manager stores it (an extension's inbox_item slot); false when there is none, or
// the manager can't say.
func (h *Manager) inboxItemRaw(ctx context.Context, id string) ([]byte, bool) {
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxItemMethod,
		assistantwire.InboxItemParams{ID: wireids.InboxItemID(id)})
	if err != nil || !res.OK {
		return nil, false
	}
	return res.Item, true
}

// forgetInbox removes a source's inbox items (the manager's); it says how many went.
func (h *Manager) forgetInbox(ctx context.Context, source string) (int, error) {
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxForgetMethod,
		assistantwire.InboxForgetParams{Source: source})
	return res.Deleted, err
}

// ---- what the manager asks of the inbox's sources (app.call inbox.*) ----

var errCannotReply = i18n.ErrEnglish("inbox-errors.cannot_reply")

// inboxAppCall answers the manager's calls about the inbox's sources; ok false for one that is not theirs.
func (h *Manager) inboxAppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var out any
	var err error
	switch p.AppID + " " + p.Method {
	case assistantwire.AppInbox + " " + assistantwire.InboxCollect:
		var a struct {
			Round string `json:"round"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			round := roundFull
			switch a.Round {
			case assistantwire.RoundPoll:
				round = roundPoll
			case assistantwire.RoundChats:
				round = roundChats
			}
			err = h.collectInbox(ctx, round)
			out = map[string]any{}
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxKick:
		h.kickInbox()
		out = map[string]any{}
	case assistantwire.AppInbox + " " + assistantwire.InboxConfigMethod:
		out = h.inboxConfigView(ctx)
	case assistantwire.AppInbox + " " + assistantwire.InboxMailBody:
		var ref assistantwire.InboxMailRef
		if err = json.Unmarshal(p.Params, &ref); err == nil {
			out, err = h.inboxMailBody(ctx, ref)
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxMailFlagsMethod:
		var ask assistantwire.InboxMailFlagsAsk
		if err = json.Unmarshal(p.Params, &ask); err == nil {
			out, err = h.inboxMailFlags(ctx, ask)
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxMessagePolicy:
		out = map[string]bool{"sender_identity": true}
	case assistantwire.AppInbox + " " + assistantwire.InboxReplyMail:
		var a struct {
			assistantwire.InboxMailRef
			Text   string `json:"text"`
			SendID string `json:"send_id"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			var sent assistantwire.InboxSent
			sent.Already, err = h.sendInboxOnce(ctx, a.SendID, "mail", func(ctx context.Context) error { return h.replyMail(ctx, a.InboxMailRef, a.Text) })
			out = sent
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxReply:
		var a struct {
			Source         string               `json:"source"`
			Reply          connector.InboxReply `json:"reply"`
			SendID         string               `json:"send_id"`
			SenderIdentity string               `json:"sender_identity"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			in, ok := connector.As[connector.Inbox](h.connectorOf(a.Source))
			if !ok && h.subs != nil {
				// A subscription's row (#6198): its connector answers, by the thread it knows the row's item by. A row it
				// cannot answer is refused as such (inbox_cannot_reply), not as a source that is not here (#9194).
				target, thread, err := h.subs.ReplyTarget(ctx, a.Source, a.Reply.Thread, a.Reply.Last)
				switch {
				case err == nil:
					in, ok, a.Reply.Thread = target, true, thread
				case errors.Is(err, connector.ErrCannotReply):
					return nil, true, h.inboxSendFailure(a.SendID, a.Source, "", err)
				}
			}
			if !ok {
				h.logf("inbox: reply %s through %s not sent: %s (no connector)", a.SendID, a.Source, assistantwire.CodeInboxSourceUnavailable)
				return nil, true, inboxSendRefusal(errCannotReply) // its connector is not here (the plugin switched off)
			}
			var sent assistantwire.InboxSent
			if a.SenderIdentity != "" && (a.Source != "larkcli" || a.SenderIdentity != "bot" && a.SenderIdentity != "user") {
				return nil, true, errors.New("this source does not support the selected sender identity")
			}
			sent.Already, err = h.sendInboxOnce(ctx, a.SendID, a.Source, func(ctx context.Context) error {
				return in.Reply(connectorwire.WithSendIdentity(ctx, a.SenderIdentity), a.Reply)
			})
			out = sent
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxSettle:
		var a struct {
			Source  string   `json:"source"`
			Threads []string `json:"threads"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			over := map[string]string{}
			if s, ok := connector.As[connector.InboxSettler](h.connectorOf(a.Source)); ok {
				over = s.Settle(ctx, a.Threads)
			}
			out = map[string]any{"over": over}
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxGroupTool:
		var a struct {
			Source string                  `json:"source"`
			Call   connector.GroupToolCall `json:"call"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			g, ok := connector.As[connector.Grouper](h.connectorOf(a.Source))
			if !ok {
				return nil, true, fmt.Errorf("%s does not relate batches", a.Source)
			}
			var raw json.RawMessage
			if raw, err = g.GroupTool(ctx, a.Call); err == nil {
				out = map[string]any{"result": compactJSON(raw, groupToolMost)}
			}
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxGroupFallback:
		var a struct {
			Source   string                  `json:"source"`
			Fallback connector.GroupFallback `json:"fallback"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			g, ok := connector.As[connector.Grouper](h.connectorOf(a.Source))
			if !ok {
				return nil, true, fmt.Errorf("%s does not relate batches", a.Source)
			}
			out, err = g.GroupFallback(ctx, a.Fallback)
		}
	case assistantwire.AppInbox + " " + assistantwire.InboxGroupAppend:
		var a struct {
			Source   string                `json:"source"`
			Append   connector.GroupAppend `json:"append"`
			AppendID string                `json:"append_id"`
		}
		if err = json.Unmarshal(p.Params, &a); err == nil {
			w, ok := connector.As[connector.EventWriter](h.connectorOf(a.Source))
			if !ok {
				return nil, true, fmt.Errorf("%s does not write events", a.Source)
			}
			if string(a.Append.Meta) == "null" { // a new event: no meta yet
				a.Append.Meta = nil
			}
			var thing connector.InboxThing
			if thing, err = h.groupAppendOnce(ctx, w, a.AppendID, a.Append); err == nil {
				out = map[string]any{"thing": thing}
			}
		}
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

// groupAppendWait bounds one group.append at the plugin. It runs to the end however long the manager waits: what it
// wrote is kept for the manager's retry (groupAppendOnce).
const groupAppendWait = time.Minute

// groupAppendsKept is how many plugins' answers to group.append are kept for a retry at most.
const groupAppendsKept = 1000

// groupAppendOnce has the plugin write one event once per append id: the same id again (the answer was lost on the
// way to the manager, or it stopped waiting) is answered with what the plugin said, not written twice. Without an id
// it is just written.
func (h *Manager) groupAppendOnce(ctx context.Context, w connector.EventWriter, appendID string, a connector.GroupAppend) (connector.InboxThing, error) {
	if appendID == "" {
		return w.GroupAppend(ctx, a)
	}
	b := &h.inbox
	l, _ := b.appendLocks.LoadOrStore(appendID, &sync.Mutex{})
	mu := l.(*sync.Mutex)
	defer b.appendLocks.Delete(appendID) // after the unlock: one who comes later finds the answer kept
	mu.Lock()
	defer mu.Unlock()
	b.appendMu.Lock()
	thing, ok := b.appended[appendID]
	b.appendMu.Unlock()
	if ok {
		return thing, nil
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), groupAppendWait)
	defer cancel()
	thing, err := w.GroupAppend(actx, a)
	if err != nil {
		return thing, err
	}
	b.appendMu.Lock()
	if b.appended == nil || len(b.appended) >= groupAppendsKept {
		b.appended = map[string]connector.InboxThing{} // crude, but retries come within minutes
	}
	b.appended[appendID] = thing
	b.appendMu.Unlock()
	return thing, nil
}

// groupToolMost is the bytes of a connector tool's answer the model reads.
const groupToolMost = 8 << 10

// connectorOf is the connector registered as id; nil when there is none.
func (h *Manager) connectorOf(id string) connector.Connector {
	if h.connectors == nil || id == "" {
		return nil
	}
	return h.connectors.Get(id)
}

// inboxConfigView is what the inbox goes by of this code's: the triage rules and the AI twin's (settings over
// manager.json), manager.json's inbox.agent, the default Feishu account (the Feishu extension's, inboxLarkAccount), the
// plugins' connectors, and the languages the devices want mail translated into.
func (h *Manager) inboxConfigView(ctx context.Context) assistantwire.InboxConfig {
	out := assistantwire.InboxConfig{Rules: h.inboxRules(ctx), AgentRules: h.inboxAgentRules(ctx)}
	extensionConnectors.Range(func(id, _ any) bool { // the plugins' connectors, before a round lists them
		out.Extensions = append(out.Extensions, id.(string))
		return true
	})
	slices.Sort(out.Extensions)
	if h.cfg != nil {
		out.Agent = h.cfg.Inbox.Agent
		out.ReplyPolicy = cloneReplyPolicyConfig(h.cfg.Inbox.ReplyPolicy)
		out.Visibility = h.cfg.Inbox.Visibility
	}
	out.LarkAccount = h.inboxLarkAccount(ctx)
	if h.mail != nil {
		out.Translate = h.mailTargets(ctx)
	}
	return out
}

func cloneReplyPolicyConfig(in *assistantwire.InboxReplyPolicyConfig) *assistantwire.InboxReplyPolicyConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.Sources = slices.Clone(in.Sources)
	if len(in.Categories) > 0 {
		out.Categories = make(map[string]string, len(in.Categories))
		for k, v := range in.Categories {
			out.Categories[k] = v
		}
	}
	if len(in.Routes) > 0 {
		out.Routes = make(map[string]string, len(in.Routes))
		for k, v := range in.Routes {
			out.Routes[k] = v
		}
	}
	return &out
}

// inboxMailFlags is which of the asked mails are still in their mailbox, read or not (at most
// InboxMailFlagsMost of them; more is refused rather than read in part, which would look like mail gone).
func (h *Manager) inboxMailFlags(ctx context.Context, ask assistantwire.InboxMailFlagsAsk) (assistantwire.InboxMailFlags, error) {
	out := assistantwire.InboxMailFlags{Seen: []uint32{}, Unseen: []uint32{}}
	if h.mail == nil {
		return out, i18n.ErrEnglish("inbox-errors.mail_unconfigured")
	}
	if len(ask.UIDs) > assistantwire.InboxMailFlagsMost {
		return out, fmt.Errorf("inbox.mail_flags: %d UIDs, at most %d", len(ask.UIDs), assistantwire.InboxMailFlagsMost)
	}
	flags, err := h.mail.SeenFlags(ctx, ask.AccountID, ask.Mailbox, ask.UIDs)
	if err != nil {
		return out, err
	}
	for _, uid := range ask.UIDs {
		seen, there := flags[uid]
		switch {
		case !there:
		case seen:
			out.Seen = append(out.Seen, uid)
		default:
			out.Unseen = append(out.Unseen, uid)
		}
	}
	return out, nil
}

// inboxMailBody is one mail read in full, peeking (it stays unread): its text part, else its HTML as plain text
// (readable text, paragraphs and links kept, when the ref asks: translation).
func (h *Manager) inboxMailBody(ctx context.Context, ref assistantwire.InboxMailRef) (assistantwire.InboxMailMessage, error) {
	if h.mail == nil {
		return assistantwire.InboxMailMessage{}, i18n.ErrEnglish("inbox-errors.mail_unconfigured")
	}
	m, err := h.mail.GetMessage(ctx, ref.AccountID, ref.UID, ref.Mailbox)
	if err != nil {
		return assistantwire.InboxMailMessage{}, err
	}
	text := strings.TrimSpace(m.Text)
	switch {
	case text != "":
	case ref.Readable:
		text = strings.TrimSpace(h.mail.ReadableText(m.HTML))
	default:
		text = strings.TrimSpace(h.mail.PlainText(m.HTML))
	}
	return assistantwire.InboxMailMessage{From: mailAddress(m.From), To: mailAddresses(m.To), Cc: mailAddresses(m.Cc), Date: m.Date,
		Subject: m.Subject, Text: text}, nil
}
