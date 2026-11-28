package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Events extensions tell the main conversation (assistant.event, docs/extensions/extensions.md "Assistant events"). The
// manager checked the extension's assistant.events grant and its rate and says who sent it; this checks the event,
// writes it as a message that says it is an extension's observation and not the user's word (prompts/assistant/
// extension_event.md, source kind extension), and puts it into the main conversation that exists: now when that is
// idle, else queued behind its turn with the relay queue the conversations' messages use. It never starts, restarts or
// replaces a conversation: without a main conversation the answer is no_main_conversation.

const (
	// extensionEventTool marks a queued extension event (relayMsg.Tool); relayMsg.From is then the extension's id and
	// relayMsg.Key the event's key.
	extensionEventTool = "extension_event"

	// extEventQueueMost is how many events of one extension may wait in a conversation's queue; a conversation that
	// cannot take them (a computer that is off) would otherwise hold whatever a loop of keys sends.
	extEventQueueMost = 10

	// The most characters of each field (the schema says the same: schema/extension/assistant.schema.json).
	extEventKeyMost    = 128
	extEventTitleMost  = 200
	extEventBodyMost   = 4000
	extEventActionMost = 500
	extEventStateMost  = 64
	extEventNameMost   = 100
)

// extEventIDRe is an extension id (extension.idPattern).
var extEventIDRe = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// extEventBook serializes the queueing of events, so the replace-or-add decision and the cap of extEventQueueMost
// hold for calls that arrive together.
type extEventBook struct {
	mu sync.Mutex
}

// extensionEventRequest answers assistant.extension_event; false for any other method.
func (s *Service) extensionEventRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if method != assistantwire.MethodExtensionEvent {
		return nil, false, nil
	}
	var req assistantwire.ExtensionEvent
	if err := strictParams(method, params, &req); err != nil {
		return nil, true, err
	}
	out, err := s.extensionEvent(ctx, req)
	return out, true, err
}

// checkedEvent is an event whose fields are within their limits, trimmed, with its severity and replace filled in.
type checkedEvent struct {
	assistantwire.AssistantEventParams
	replace bool
}

// checkEvent holds raw, an extension's assistant.event params, to the schema's limits (invalid_params otherwise).
func checkEvent(raw json.RawMessage) (checkedEvent, error) {
	var p assistantwire.AssistantEventParams
	if err := strictParams(assistantwire.MethodAssistantEvent, raw, &p); err != nil {
		return checkedEvent{}, err
	}
	bad := func(msg string) error { return conversationsInvalid(assistantwire.MethodAssistantEvent, msg) }
	p.Key, p.Title = strings.TrimSpace(p.Key), strings.TrimSpace(p.Title)
	p.Body, p.ActionHint = strings.TrimSpace(p.Body), strings.TrimSpace(p.ActionHint)
	p.State = assistantwire.AssistantEventState(strings.TrimSpace(string(p.State)))
	for _, f := range []struct {
		name  string
		value string
		most  int
		need  bool
	}{
		{"key", p.Key, extEventKeyMost, true}, {"title", p.Title, extEventTitleMost, true}, {"body", p.Body, extEventBodyMost, false},
		{"action_hint", p.ActionHint, extEventActionMost, false}, {"state", string(p.State), extEventStateMost, false},
	} {
		if f.need && f.value == "" {
			return checkedEvent{}, bad(f.name + " is required")
		}
		if n := utf8.RuneCountInString(f.value); n > f.most {
			return checkedEvent{}, bad(f.name + " is longer than " + strconv.Itoa(f.most) + " characters")
		}
	}
	switch p.Severity {
	case "":
		p.Severity = assistantwire.EventSeverityInfo
	case assistantwire.EventSeverityInfo, assistantwire.EventSeverityWarning, assistantwire.EventSeverityCritical:
	default:
		return checkedEvent{}, bad("severity must be info, warning or critical")
	}
	return checkedEvent{AssistantEventParams: p, replace: p.Replace.Replaces()}, nil
}

// extensionEvent delivers one event of extension req.Extension into the main conversation.
func (s *Service) extensionEvent(ctx context.Context, req assistantwire.ExtensionEvent) (assistantwire.AssistantEventResult, error) {
	if !extEventIDRe.MatchString(req.Extension) { // the manager's word, but it ends up in the assistant's prompt as it is
		return assistantwire.AssistantEventResult{}, conversationsInvalid(assistantwire.MethodExtensionEvent, "extension is not an extension id")
	}
	ev, err := checkEvent(json.RawMessage(req.Params))
	if err != nil {
		return assistantwire.AssistantEventResult{}, err
	}
	here := s.settings(ctx)
	machine, to := here.mainRouteMachine(), here.Main // the main conversation lives where settings bind it, not always on the default machine
	if machine == "" || to == "" {
		return assistantwire.AssistantEventResult{}, &rpc.Error{Code: assistantwire.CodeNoMainConversation, Message: assistantwire.MethodAssistantEvent + ": there is no main conversation to tell"}
	}
	name := strings.Join(strings.Fields(req.Name), " ")
	if name == "" {
		name = req.Extension
	}
	name = cutRunes(name, extEventNameMost)
	msg := relayMsg{
		From: req.Extension, FromTitle: name, Tool: extensionEventTool, Key: ev.Key, Title: ev.Title, State: ev.Severity, At: time.Now().UTC(),
		Text: prompts.RenderExtensionEvent(prompts.ExtensionEvent{ID: req.Extension, Severity: ev.Severity, Name: name, Key: ev.Key,
			Title: ev.Title, Body: ev.Body, ActionHint: ev.ActionHint, State: string(ev.State)}),
	}
	c, ok := s.session(ctx, machine, to)
	idle := ok && c.Status != proto.StatusWorking && !c.Starting
	directFailed, directErr := false, error(nil)
	if idle && s.queuedFor(to) == 0 {
		dctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err := s.tryDirectRelay(dctx, machine, to, &msg)
		cancel()
		if err == nil {
			return assistantwire.AssistantEventResult{Delivery: assistantwire.EventDelivered}, nil
		}
		if s.refusedDirect(ctx, machine, to, msg, err) {
			return assistantwire.AssistantEventResult{}, err
		}
		if relayFailureOf(err) == relayFailedGone {
			s.logf("extension %s: event %q: %v; dropped", req.Extension, ev.Key, err)
			return assistantwire.AssistantEventResult{}, &rpc.Error{
				Code:    assistantwire.CodeNoMainConversation,
				Message: assistantwire.MethodAssistantEvent + ": " + err.Error(),
			}
		}
		directFailed, directErr = true, err
		s.logf("extension %s: event %q: %v; queued", req.Extension, ev.Key, err)
	}
	bg := backgroundRunning(c)
	if directFailed {
		if fresh, ok := s.session(ctx, machine, to); ok {
			c = fresh
			idle = fresh.Status != proto.StatusWorking && !fresh.Starting
			bg = backgroundRunning(fresh)
		} else {
			idle = false
			bg = false
		}
	}
	delivery, shouldDrain, err := s.queueExtensionEvent(machine, to, msg, ev.replace, directFailed, directErr, bg)
	if err != nil {
		return assistantwire.AssistantEventResult{}, err
	}
	s.publish(ctx) // shown waiting above the composer
	if shouldDrain && idle {
		s.drainRelays(machine, []proto.Session{c})
	}
	return assistantwire.AssistantEventResult{Delivery: delivery}, nil
}

// queueExtensionEvent queues msg behind conversation to's turn: in the place of the same extension's queued event with
// the same key when replace (assistant.event's replace; the one going in right now is left), else after the others,
// unless the extension already has extEventQueueMost waiting. When directFailed is true and the queued message lands at
// the head of a non-draining queue, failure backoff (failedAt, stuck, roleChange) and drainAfterLocked are applied
// atomically under b.mu before s.publish runs; if another message is already ahead at q[0], shouldDrain requests a
// drain so a healthy head is not stalled.
func (s *Service) queueExtensionEvent(machine, to string, msg relayMsg, replace, directFailed bool, directErr error, background bool) (delivery string, shouldDrain bool, err error) {
	book := &s.extEvents
	book.mu.Lock()
	defer book.mu.Unlock()

	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return "", false, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: assistantwire.MethodAssistantEvent + ": the relay queue could not be read, try again"}
	}

	var queuedCopy relayMsg
	if replace {
		q := b.st.Queue[to]
		for i := range q {
			if q[i].Tool == extensionEventTool && q[i].From == msg.From && q[i].Key == msg.Key && (i > 0 || !b.draining[to]) {
				q[i] = msg
				_ = s.saveRelayLocked()
				queuedCopy = q[i]
				delivery = assistantwire.EventReplaced
				break
			}
		}
	}
	if delivery == "" {
		if queuedEventsLocked(b.st.Queue[to], msg.From) >= extEventQueueMost {
			b.mu.Unlock()
			return "", false, &rpc.Error{Code: assistantwire.CodeQueueFull, Message: assistantwire.MethodAssistantEvent + ": " + msg.From + " already has " + strconv.Itoa(extEventQueueMost) + " events waiting for the main conversation"}
		}
		msg.QueuedAt, msg.Depth = time.Now().UTC(), len(b.st.Queue[to])+1
		b.st.Queue[to] = append(b.st.Queue[to], msg)
		_ = s.saveRelayLocked()
		queuedCopy = msg
		delivery = assistantwire.EventQueued
	}

	if directFailed {
		q := b.st.Queue[to]
		if !b.draining[to] && len(q) > 0 && q[0] == queuedCopy {
			wait := relayRetryGap
			if relayInDoubt(directErr) {
				wait = relayLookGap
				delete(b.stuck, to)
				delete(b.failedAt, to)
				delete(b.msgFailedAt, to)
				delete(b.roleChange, to)
			} else {
				now := time.Now()
				if b.failedAt != nil {
					b.failedAt[to] = now
				}
				b.recordMsgFailedLocked(to, queuedCopy, now)
				if b.stuck != nil {
					b.stuck[to] = queuedCopy
				}
				if b.roleChange != nil {
					if roleChangeBusy(directErr) {
						b.roleChange[to] = background
					} else {
						delete(b.roleChange, to)
					}
				}
			}
			s.drainAfterLocked(machine, to, wait)
		} else {
			shouldDrain = !b.draining[to]
		}
	} else {
		shouldDrain = true
	}
	b.mu.Unlock()

	if delivery == assistantwire.EventQueued {
		go s.releaseHeldAsks(to)
	}
	return delivery, shouldDrain, nil
}

func queuedEventsLocked(q []relayMsg, id string) int {
	n := 0
	for _, m := range q {
		if m.Tool == extensionEventTool && m.From == id {
			n++
		}
	}
	return n
}

// queuedEvents is how many events of extension id wait in conversation to's queue.
func (s *Service) queuedEvents(to, id string) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0
	}
	return queuedEventsLocked(b.st.Queue[to], id)
}
