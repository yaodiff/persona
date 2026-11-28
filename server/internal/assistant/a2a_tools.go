package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func a2aSessionCaller(ref a2aRef) a2aCaller {
	return a2aCaller{Ref: ref, Owner: "session:" + ref.Machine + "/" + ref.Session}
}

func (s *Service) a2aTool(ctx context.Context, ref a2aRef, name string, args json.RawMessage) proto.ToolResult {
	cfg := s.a2aConfig(ctx)
	if !s.a2aToolAllowed(ctx, cfg, ref) {
		return proto.TextResult(errA2AUnavailable.Error(), true)
	}
	st, err := s.a2aStore()
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	out, err := s.a2aToolCall(ctx, st, cfg, a2aSessionCaller(ref), name, args)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	if text, ok := out.(string); ok {
		// a2a_read: a conversation to read, as agent_read gives it; it changes no task, so nothing to deliver.
		return proto.TextResult(text, false)
	}
	s.a2aSignal()
	data, err := json.Marshal(out)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(string(data), false)
}

type a2aToolArgs struct {
	a2aRef
	Task           string `json:"task_id"`
	ID             string `json:"message_id"`
	Context        string `json:"context_id"`
	Text           string `json:"text"`
	State          string `json:"state"`
	Wake           *bool  `json:"wake"`
	Before         string `json:"before"`
	CWD            string `json:"cwd"`
	Launcher       string `json:"launcher"`
	Tier           string `json:"tier"`
	PermissionMode string `json:"permission_mode"`
	Presets        bool   `json:"presets"`
	Turns          int    `json:"turns"`
}

type a2aTaskView struct {
	ID             string     `json:"task_id"`
	Context        string     `json:"context_id"`
	Source         a2aRef     `json:"source"`
	Target         a2aRef     `json:"target"`
	State          string     `json:"state"`
	Queued         bool       `json:"queued,omitempty"`
	Events         []a2aEvent `json:"events"`
	Disconnected   bool       `json:"target_disconnected,omitempty"`
	WaitingSince   time.Time  `json:"waiting_since,omitzero"`
	WaitingSeconds int64      `json:"waiting_seconds,omitempty"`
	WaitingReason  string     `json:"waiting_reason,omitempty"`
}

func (t *a2aTask) view() a2aTaskView {
	out := a2aTaskView{ID: t.ID, Context: t.Context, Source: t.source(), Target: t.target(), State: t.State, Queued: t.State == a2aQueued, Events: t.Events, Disconnected: t.Disconnected}
	if !a2aTerminal(t.State) {
		for i := len(t.Events) - 1; i >= 0; i-- {
			e := t.Events[i]
			if e.Kind != "message" {
				continue
			}
			if e.Delivery == a2aQueued {
				out.WaitingSince, out.WaitingReason = e.At, a2aWaitingReason(e)
				if t.State == a2aQueued {
					out.WaitingReason = "Waiting behind an earlier task in the receiving session's FIFO."
				}
				out.WaitingSeconds = max(0, int64(time.Since(e.At).Seconds()))
			}
			break
		}
	}
	return out
}

func (s *Service) a2aToolCall(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, name string, args json.RawMessage) (any, error) {
	var a a2aToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errA2AInvalid
	}
	a.a2aRef = s.a2aResolve(ctx, cfg, a.a2aRef) // an id the session's conversation went by: its row now (a2a_resolve.go)
	var t *a2aTask
	var err error
	switch name {
	case "a2a_sessions":
		if a.Presets { // and where a2a_send can start an Agent, with the Agent presets each computer can start
			return map[string]any{"sessions": s.a2aDiscover(ctx, cfg, c.Ref, a.Machine), "computers": s.a2aComputers(ctx, cfg, a.Machine)}, nil
		}
		return s.a2aDiscover(ctx, cfg, c.Ref, a.Machine), nil
	case "a2a_read":
		return s.a2aRead(ctx, cfg, c.Ref, a.a2aRef, a.Turns)
	case "a2a_get":
		if a.Task == "" {
			return s.a2aInbox(ctx, st, cfg, c, a.Before)
		}
		t, err = s.a2aGet(ctx, st, cfg, c, a.Task)
	case "a2a_send":
		if a.Task == "" {
			var launch *a2aLaunch
			if a.CWD != "" {
				if err := s.guardRefusal(ctx, c.Ref.Machine, c.Ref.Session, "agent_start"); err != nil {
					return nil, err
				}
				if err := s.dispatchToolGuard(ctx, c.Ref.Machine, c.Ref.Session, "agent_start"); err != nil {
					return nil, err
				}
				if err := s.workerRefusal(ctx, c.Ref.Machine, c.Ref.Session); err != nil {
					return nil, err
				}
				launch = &a2aLaunch{CWD: a.CWD, Launcher: a.Launcher, Tier: strings.TrimSpace(a.Tier), PermissionMode: a.PermissionMode, Pending: true, Subagent: true}
			} else if err := s.a2aCanSendMessage(ctx, c.Ref, a.a2aRef, a.Text); err != nil {
				return nil, err
			}
			if old, err := decodeA2A(st.A2ARequest(ctx, c.Owner, a.ID)); err == nil && old == nil && a2aValidID(a.ID) && a2aValidText(a.Text) &&
				a.a2aRef != c.Ref && (launch != nil || s.a2aAllowed(ctx, cfg, a.a2aRef)) {
				// A new hand-off, not a retry of one already made (a2aStart answers those). Its tier is resolved, or
				// refused, before anything is asked or recorded (agent_presets.go), and a launch that could not start
				// read-only where it would go is refused there too (start_host.go).
				where := a.CWD
				if launch != nil {
					if err := s.a2aLaunchTier(ctx, cfg, a.Machine, launch); err != nil {
						return nil, err
					}
					if err := s.a2aLaunchCheck(ctx, cfg, a.a2aRef, a.Context, launch); err != nil {
						return nil, err
					}
					where = s.a2aLaunchWhere(ctx, launch)
				}
				if err := s.a2aConfirm(ctx, c.Ref, a.a2aRef, where, a.Text, args); err != nil {
					return nil, err
				}
			}
			t, err = s.a2aStart(ctx, st, cfg, c, a.a2aRef, a.ID, a.Context, a.Text, a.Wake == nil || *a.Wake, launch)
			break
		}
		if t, err := s.a2aGet(ctx, st, cfg, c, a.Task); err == nil && t.Owner == c.Owner && t.State == a2aInput &&
			a2aValidID(a.ID) && a2aValidText(a.Text) && !slices.ContainsFunc(t.Events, func(e a2aEvent) bool { return e.ID == a.ID }) {
			// An answer that would be sent, not a retry of one already sent (a2aContinue).
			if err := s.a2aCanSendMessage(ctx, c.Ref, t.target(), a.Text); err != nil {
				return nil, err
			}
			if err := s.a2aConfirm(ctx, c.Ref, t.target(), "", a.Text, args); err != nil {
				return nil, err
			}
		}
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			if t.Owner != c.Owner {
				return false, errA2ANotFound
			}
			if err := s.a2aCanSendMessage(ctx, c.Ref, t.target(), a.Text); err != nil {
				return false, err
			}
			return a2aContinue(t, a.ID, a.Text)
		})
	case "a2a_report":
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			return a2aReport(t, c.Ref, a.ID, a.State, a.Text)
		})
	case "a2a_cancel":
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			if t.Owner != c.Owner {
				return false, errA2ANotFound
			}
			return a2aCancel(t)
		})
	default:
		return nil, errA2AInvalid
	}
	if err != nil {
		return nil, err
	}
	return t.view(), nil
}

// errA2ADeclined: the user themselves declined a hand-off on the phone. A card nobody answered is not that
// (a2aUnanswered).
var errA2ADeclined = errors.New("the user declined on the phone: nothing was sent")

// a2aUnanswered is a2aConfirm's error for a hand-off whose card ended without the user's own answer (outcome: it
// expired, was withdrawn or cleared, someone else answered it): nothing was sent, and the user did not decline it.
func a2aUnanswered(outcome string) error {
	return errors.New(outboundNotDone(outcome, errA2ADeclined.Error(), "nothing was sent"))
}

// a2aConfirm asks the phone before an assistant conversation hands text to another session, or to a new one in
// folder where, through A2A (health.go confirmOutbound: at once when the text needs no confirming). Other senders are
// not asked.
func (s *Service) a2aConfirm(ctx context.Context, from, to a2aRef, where, text string, args json.RawMessage) error {
	if !s.isAssistantConversation(ctx, from.Machine, from.Session) {
		return nil
	}
	label := to.Machine
	if m, ok := s.device(ctx, to.Machine); ok && m.Name != "" {
		label = m.Name
	}
	if where != "" {
		label += " · " + where
	}
	ok, outcome, err := s.confirmOutbound(ctx, from.Machine, from.Session, "a2a_send", text, label+"\n\n"+text, args)
	if err != nil {
		return err
	}
	if !ok && outcome == proto.OutcomeDeclined {
		return errA2ADeclined
	}
	if !ok {
		return a2aUnanswered(outcome)
	}
	return nil
}

func (s *Service) a2aCanSendMessage(ctx context.Context, from, to a2aRef, text string) error {
	if err := s.guardRefusal(ctx, from.Machine, from.Session, "conversation_send"); err != nil {
		return err
	}
	return s.dispatchSendGuard(withToolCaller(ctx, from.Machine, from.Session), from.Session, to.Machine, to.Session, text)
}

func a2aReport(t *a2aTask, sender a2aRef, id, state, text string) (bool, error) {
	if sender != t.target() {
		return false, errA2ANotFound
	}
	if !a2aValidID(id) || !a2aValidText(text) {
		return false, errA2AInvalid
	}
	kind := "status"
	switch state {
	case "progress":
		kind, state = "progress", ""
	case a2aInput, a2aComplete, a2aFailed:
	default:
		return false, fmt.Errorf("%w: state must be progress, input_required, completed or failed", errA2AInvalid)
	}
	ev := a2aReturn(t, a2aEvent{ID: id, Kind: kind, State: state, Text: text})
	if !slices.ContainsFunc(t.Events, func(e a2aEvent) bool { return e.ID == id }) {
		for i := len(t.Events) - 1; i >= 0; i-- {
			e := &t.Events[i]
			if e.Kind != "message" {
				continue
			}
			// A report from the verified recipient can arrive ahead of the send's acknowledgment.
			if e.Delivery == a2aSending || e.Delivery == a2aUncertain {
				e.Delivery = a2aDelivered
			}
			if e.Delivery == a2aQueued && a2aTerminal(state) {
				// A verified recipient may clear undelivered work. Do not
				// fabricate a receipt or leave its input eligible for delivery.
				e.Delivery = a2aDiscarded
			} else if e.Delivery != a2aDelivered {
				return false, fmt.Errorf("%w: request is still queued; report failed or completed to clear it", errA2AInvalid)
			}
			if t.State == a2aSubmitted && kind == "progress" {
				t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aWorking})
			}
			break
		}
	}
	return a2aAppendOnce(t, ev)
}

func (s *Service) a2aInbox(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, before string) (any, error) {
	out := []a2aTaskView{}
	all, err := st.A2AList(ctx, c.Owner, c.Ref.Machine, c.Ref.Session, "", "", before, true, nil, 21)
	if err != nil {
		return nil, err
	}
	next := ""
	for i, row := range all {
		if i == 20 {
			next = all[i-1].ID
			break
		}
		t, err := decodeA2A(&row, nil)
		if err != nil {
			return nil, err
		}
		if s.a2aTaskVisible(ctx, cfg, c, t) {
			v := t.view()
			// The full history is available when the caller selects one task.
			v.Events = v.Events[len(v.Events)-1:]
			out = append(out, v)
		}
	}
	return map[string]any{"tasks": out, "next": next}, nil
}

func (s *Service) a2aDiscover(ctx context.Context, cfg assistantwire.A2AConfig, self a2aRef, machine string) any {
	type peer struct {
		a2aRef
		Title  string `json:"title"`
		Agent  string `json:"agent,omitempty"`
		Status string `json:"status"`
	}
	out := []peer{}
	machines := cfg.Machines
	if machines == nil {
		for _, d := range s.devices(ctx) {
			machines = append(machines, d.ID)
		}
	}
	for _, mid := range machines {
		if machine != "" && mid != machine {
			continue
		}
		for _, sess := range s.a2aSessionsOn(ctx, mid) {
			ref := a2aRef{mid, sess.SessionID}
			if ref != self && a2aSendable(sess) && s.a2aAllowed(ctx, cfg, ref) {
				out = append(out, peer{ref, sess.Title, sess.Agent, sess.Status})
			}
		}
	}
	return out
}
