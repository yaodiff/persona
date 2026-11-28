package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Sending a message from the phone (docs/protocol/protocol.md "Sending messages"). The app sends each message with its own
// client_msg_id and resends what got no answer with the same id; the node types it into the agent's terminal. A
// message must arrive once: the hub remembers every id — in memory while a send runs, in Postgres after — so a retry
// is answered from what happened instead of typed again, also after the node reconnected (a new node process knows
// nothing of earlier sends) or the hub restarted. A send whose connection broke while it was being typed is
// "unknown": the retry is not typed, and the app asks the user to look before sending again (with a new id).

const (
	sendDelivered = db.SendStateDelivered
	sendUnknown   = db.SendStateUnknown
	sendSending   = db.SendStateSending
	// sendLater: nothing was typed, and the conversation's manager keeps the message and sends it again by itself
	// (#10194, keepUndelivered): a retry of the id is answered as kept, never sent from here.
	sendLater = db.SendStateLater
)

type sendLog struct {
	mu      sync.Mutex
	flights map[string]*sendFlight
}

// sendFlight is one client_msg_id's send: retries arriving while it runs wait for it and get the same answer.
type sendFlight struct {
	done   chan struct{}
	status int
	body   []byte
	at     time.Time
}

// join returns the flight for id and whether this request is the one to send it.
func (l *sendLog) join(id string) (*sendFlight, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flights == nil {
		l.flights = map[string]*sendFlight{}
	}
	for k, f := range l.flights {
		if time.Since(f.at) > 10*time.Minute {
			delete(l.flights, k)
		}
	}
	if f := l.flights[id]; f != nil {
		return f, false
	}
	f := &sendFlight{done: make(chan struct{}), at: time.Now()}
	l.flights[id] = f
	return f, true
}

// forget drops an id whose send typed nothing, so a retry sends it.
func (l *sendLog) forget(id string, f *sendFlight) {
	l.mu.Lock()
	if l.flights[id] == f {
		delete(l.flights, id)
	}
	l.mu.Unlock()
}

// withoutControls drops what acts on a terminal rather than being read: line breaks and tabs stay.
func withoutControls(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return -1
		}
		return c
	}, s)
}

// switchSendWait is how long the hub waits for a message carrying out a launcher switch: the node's checks before the
// quit take at most a minute and the rest at most five (node/launcher_switch.go switchProbeWait, switchBudget); a
// switch to the other engine also builds its handoff before the quit and takes five and a half minutes after it
// (node/engine_switch.go engineSwitchBudget, #5054).
const switchSendWait = 8 * time.Minute

// sendWait is how long the hub waits for the node to take message p to session s (as listed; zero when not): 90
// seconds; 5 minutes for one that is starting or not running (a resume, node's claudeStartWait, #1396); switchSendWait
// for the user's message that carries out a switch to another launcher (apply_switch): it quits the agent and starts
// it again, and brings it back on its own launcher when that fails. Decided by the request, not the cached row, which
// may not show a switch picked a moment ago. A live terminal Claude send may resume on an updated provider too.
func sendWait(p map[string]any, s proto.Session) time.Duration {
	if apply, _ := p["apply_switch"].(bool); apply {
		return switchSendWait
	}
	if s.Starting || s.Status == proto.StatusOffline {
		return 5 * time.Minute
	}
	if s.CanRefreshTerminalLauncher() {
		return proto.LauncherRefreshSendWait
	}
	return 90 * time.Second
}

type sendTimingKey struct{}

type sendTiming struct {
	hub   *Hub
	id    string
	start time.Time
}

func (s *sendTiming) mark(phase string, ok bool) {
	if s != nil && s.hub.logger != nil {
		s.hub.logf("send_timing trace=%s phase=%s elapsed_ms=%d ok=%t", s.id, phase, time.Since(s.start).Milliseconds(), ok)
	}
}

func sendTimingOf(ctx context.Context) *sendTiming {
	s, _ := ctx.Value(sendTimingKey{}).(*sendTiming)
	return s
}

func (h *Hub) sendMessage(w http.ResponseWriter, r *http.Request, m *Machine) {
	started := time.Now()
	p, ok := optionalObject(w, r, proto.MessageBodyLimit)
	if !ok {
		return
	}
	if id, _ := p["client_msg_id"].(string); strings.HasPrefix(id, assistantwire.FirstMessageIDPrefix) || strings.HasPrefix(id, proto.A2ADeliveryPrefix) {
		// The manager's own, for a conversation's first message (assistant start.go humanStart): never an app's, so no
		// later message carries one (and passes for that first message). Refused before anything else is done with it.
		writeErrCode(w, http.StatusBadRequest, "client_msg_id_reserved", i18n.Err("hub-send.reserved_id"))
		return
	}
	sid := r.PathValue("sid")
	// The pictures this message carries, never those before it in the conversation (#12973).
	if images, _ := p["images"].([]any); len(images) > 0 {
		if row, _ := m.session(sid); apierr.ImagesOverLimit(w, proto.HubImageLimits.For(row.Agent), len(images)) {
			return
		}
	}
	if terminalAgentSend(r.Context()) {
		if _, ok := h.terminalAgentTarget(m, sid); !ok {
			h.refuseTerminalAgent(w, r)
			return
		}
		hold, present := p["hold_when_away"].(bool)
		if !present || hold != requestDeclares(r, featureAgentMessageHold) {
			writeCode(w, http.StatusBadRequest, "invalid_hold",
				i18n.T(h.requestLang(r), "agent-send.invalid_hold"), map[string]any{"delivered": "none"})
			return
		}
		text, _ := p["text"].(string)
		if len(p) != 3 || strings.TrimSpace(text) == "" || len(text) > devicewire.MaxTerminalAgentMessageBytes {
			writeCode(w, http.StatusBadRequest, "invalid_terminal_message",
				i18n.T(h.requestLang(r), "agent-send.invalid_text"), map[string]any{"delivered": "none"})
			return
		}
		delete(p, "hold_when_away")
	}

	if caller := principalOf(r.Context()); caller.Role == roleScoped {
		row, exists := m.session(sid)
		if !exists || spaceSession(&row) {
			writeErr(w, http.StatusNotFound, errors.New("no such session"))
			return
		}
		if !slices.Contains(caller.Scopes, extwire.ScopeAgentsPermissionMode) && scopedSendNeedsPermissionMode(row, p) {
			scopedPermissionDenied(w, extwire.ScopeAgentsPermissionMode)
			return
		}
	}
	if todoID, _ := p["todo_id"].(string); todoID != "" && principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
		return
	}
	if answerOnly(r) {
		// Validate this caller's target before joining another request's receipt.
		s, ok := m.session(sid)
		if !ok || len(h.shown(m.cfg.ID, []proto.Session{s})) == 0 {
			writeErr(w, http.StatusNotFound, errors.New("no such session"))
			return
		}
		text, _ := p["text"].(string)
		text = withoutControls(text)
		if strings.TrimSpace(text) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("text is required"))
			return
		}
		said := map[string]any{"session_id": sid, "text": text}
		if id, ok := p["client_msg_id"]; ok {
			said["client_msg_id"] = id
		}
		p = said
	}
	id, _ := p["client_msg_id"].(string)
	timing := &sendTiming{hub: h, id: proto.SendTraceID(id), start: started}
	r = r.WithContext(context.WithValue(r.Context(), sendTimingKey{}, timing))
	timing.mark("hub_received", true)
	if id == "" {
		h.sendMessageOnce(w, r, m, p)
		return
	}
	// Join before manager preparation, not just terminal delivery. A duplicate must
	// neither prepare another turn nor reset an already executing turn's state.
	f, first := h.sends.join(id)
	if !first {
		select {
		case <-f.done:
			// HTTP replays the receipt. Re-broadcasting this older terminal event
			// could clear a newer retry's Processing state for the same ID.
			writeRaw(w, f.status, f.body)
		case <-r.Context().Done():
		}
		return
	}
	h.toSubscribers(m.cfg.ID, sid, proto.MessageSendState{Type: "message_send_state", MachineID: m.cfg.ID,
		SessionID: sid, ClientMsgID: id, State: proto.MessageSendProcessing, At: time.Now().UTC()})
	// Receipt is not durable delivery. Keep the original flight alive even when
	// the HTTP caller disconnects during manager preparation.
	row, _ := m.session(sid)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), sendWait(p, row))
	defer cancel()
	rec := &bufferedResponse{header: http.Header{}, lang: h.requestLang(r)}
	h.sendMessageOnce(rec, r.WithContext(ctx), m, p)
	f.status, f.body = rec.status, rec.body.Bytes()
	state := h.messageSendState(m.cfg.ID, sid, id, f.status, f.body)
	close(f.done)
	if state == proto.MessageSendFailed {
		h.sends.forget(id, f) // preparation or delivery typed nothing: retry remains safe
	}
	for key, values := range rec.header {
		w.Header()[key] = values
	}
	writeRaw(w, f.status, f.body)
}

// messageSendState only reports the transport outcome; session state belongs to
// the manager and node, and may already describe another running turn.
func (h *Hub) messageSendState(mid, sid, id string, status int, body []byte) string {
	state := proto.MessageSendFailed
	var out struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(body, &out)
	if status == http.StatusOK {
		state = proto.MessageSendDelivered
	} else if status == http.StatusAccepted && out.Delivered == sendLater {
		state = proto.MessageSendKept
	} else if out.Delivered == sendUnknown {
		state = proto.MessageSendUnknown
	}
	h.toSubscribers(mid, sid, proto.MessageSendState{Type: "message_send_state", MachineID: mid,
		SessionID: sid, ClientMsgID: id, State: state, At: time.Now().UTC()})
	return state
}

func (h *Hub) sendMessageOnce(w http.ResponseWriter, r *http.Request, m *Machine, p map[string]any) {
	sid := r.PathValue("sid")
	terminal := terminalAgentSend(r.Context())
	var s proto.Session
	var known bool
	if terminal {
		// The row used to route a terminal send must first pass the Agent
		// check. Do not reclassify it as a Persona chat after owner callbacks.
		s, known = h.terminalAgentTarget(m, sid)
		if !known {
			h.refuseTerminalAgent(w, r)
			return
		}
	}
	p["session_id"] = sid
	todoID, _ := p["todo_id"].(string)
	delete(p, "todo_id")
	if todoID != "" && principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
		return
	}
	text, isText := p["text"].(string)
	clientMsgID, _ := p["client_msg_id"].(string)
	if isText && strings.TrimSpace(text) != "" || p["images"] != nil {
		r = h.captureHumanSubmission(r, text)
	}
	indirect := viaMarked(p["via"]) // Siri or the share sheet: delivered, but not the owner's own input for project review
	for _, reserved := range []string{"creation_id", "incarnation", "caller", "submission", "share_authority", "skill_owner"} {
		delete(p, reserved)
	}
	var start freshStart
	fresh := false
	if isText && strings.TrimSpace(text) != "" && !answerOnly(r) {
		// The first message of an agent started for a todo with none: a new agent's first prompt, with its project's
		// background and the todo's decisions first (#2820; iOS and Android send it without todo_id).
		if f, ok := h.fresh.peek(m.cfg.ID, sid); ok && (todoID == "" || todoID == f.todoID) {
			start, fresh = f, true
		}
	}
	handoffAgent := ""
	if c, ok := m.session(sid); ok {
		handoffAgent = c.Agent
	}
	if todoID != "" {
		// The manager checks the todo (404, 409, 503 assistant_offline while it is away); handed to the agent for it,
		// the calls already made on it go first (#1618).
		hand := assistantwire.TodoHandoff{TodoID: todoID, Text: text, Agent: handoffAgent}
		if fresh {
			hand.New, hand.MachineID, hand.Cwd = true, m.cfg.ID, start.cwd
		}
		decided, images, ok := h.todoHandoff(r.Context(), w, hand)
		if !ok {
			return
		}
		if isText {
			p["text"] = decided
			if strings.TrimSpace(decided) != "" {
				mergeTodoImages(p, images)
			}
		}
	} else if fresh {
		decided, images := h.todoStartText(r.Context(), assistantwire.TodoHandoff{TodoID: start.todoID, Text: text, New: true,
			MachineID: m.cfg.ID, Cwd: start.cwd, Agent: handoffAgent})
		p["text"] = decided
		if strings.TrimSpace(decided) != "" {
			mergeTodoImages(p, images)
		}
	}
	spokeSession, spokeKnown := s, known
	if !terminal {
		spokeSession, spokeKnown = m.session(sid)
	}
	if spokeKnown && (!terminal && m.isAssistant(spokeSession) || isText && strings.TrimSpace(text) != "") {
		// The manager also observes owner messages to Agents before delivery, so a task's decision default can pause.
		spoke := assistantwire.UserSpokeParams{
			SessionRef: assistantwire.SessionRef{MachineID: m.cfg.ID, SessionID: sid},
			At:         time.Now().UTC(),
		}
		// A person's own app supplies the text for every conversation, including a supervisor / direction
		// conversation: its needs_user decision cards are answered there too, and this event reaches the manager
		// before the message is persisted, so the text cannot be recovered from transcript history at this point.
		// The text carries the send's client_msg_id, the same id its session.assistant_sent names: a choice that
		// must not settle before delivery waits for it (#13041).
		if isText && personSent(r.Context()) {
			spoke.Text = text
			spoke.ClientMsgID = wireids.ClientMsgID(clientMsgID)
			// Owner provenance (#15641) marks owner input to an Agent only, never the assistant's own conversations.
			if terminal {
				spoke.Source = assistantwire.OwnerInputTerminal
			} else if !m.isAssistant(spokeSession) {
				spoke.Source = assistantwire.OwnerInputApp
			}
		}
		h.asstOf(h.sessionOwner(m.cfg.ID, sid)).userSpoke(r.Context(), spoke)
	}
	if !terminal {
		s, known = m.session(sid)
	}
	picked := "" // a model picked in the chat's chip that the message runs on (#1644)
	assistantConv := false
	var asSent map[string]any // the app's message as it came, kept when it cannot go in (keepUndelivered)
	note := ""                // the main chat runs on another launcher than configured (#4659): told in a delivered send's answer
	if !terminal && (h.isMainOn(m.cfg.ID, sid) || known && (s.Assistant || m.isAssistant(s))) {
		assistantConv = true
		// One of the assistant's conversations: it says what the message runs on (and, the first one to a new main
		// conversation started empty, what the old one left before it).
		asked, _ := p["model"].(string)
		asSent = keepableMessage(p)
		var err error
		if note, err = h.assistantSend(r.Context(), m, sid, p); err != nil {
			// Older owner messages to it are kept undelivered: this one is kept behind them, never sent first.
			if id, _ := asSent["client_msg_id"].(string); id != "" && rpcCodeOf(err) == assistantwire.CodeUndeliveredAhead {
				if status, body, ok := h.keepUndelivered(r.Context(), m, sid, asSent, assistantwire.CodeUndeliveredAhead, ""); ok {
					h.recordAcceptedProjectReviewInput(r.Context(), r, m.cfg.ID, sid, id, nil, indirect)
					writeRaw(w, status, body)
					return
				}
			}
			writeSendAssistantErr(w, err)
			return
		}
		if target, _ := p["owner_approval_handled"].(string); target != "" {
			// The manager verified and resolved this exact owner-action reply. It is
			// not a conversation turn, so no node send or model execution follows.
			delete(p, "owner_approval_handled")
			body, _ := json.Marshal(map[string]any{"ok": true, "approval_reply": true, "interaction_id": target})
			writeRaw(w, http.StatusOK, append(body, '\n'))
			return
		}
		if kept, _ := p["model"].(string); strings.TrimSpace(asked) != "" && kept == strings.TrimSpace(asked) {
			picked = kept
		}
	} else if l, _ := p["launcher"].(string); l == "" && known && resumesWithoutLauncher(s) {
		h.applyResumeLauncher(r.Context(), m, sid, p) // the manager's (agents_link.go)
	}
	// The send carries on when the phone stops waiting (backgrounded, network gone): it may be typing already, and
	// its outcome is what the retry gets. Resuming an offline session can take a while (shell + claude startup).
	// One sent while the session still starts waits for claude to be ready (node/starting.go); one to a session that is
	// not running resumes it first. Either can wait minutes on a busy machine (node's claudeStartWait, #1396).
	row := s
	if !terminal {
		row, _ = m.session(sid)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), sendWait(p, row))
	defer cancel()
	id, _ := p["client_msg_id"].(string)
	if ch := channel.FromClientMsgID(id, []string{"voice"}); ch != "" {
		h.channelNoted(ch, m.cfg.ID, sid) // a voice conversation's errand (docs/extensions/channels.md "Source markers")
	}
	l := h.requestLang(r)
	external := s.SourceKind != ""
	if !terminal {
		row, found := m.session(sid)
		external = found && row.SourceKind != ""
	}
	if external && id == "" {
		callErr(w, &rpc.Error{Code: "codex_send_guard", Message: i18n.T(l, "hub-send.guard_need_id"), Data: json.RawMessage(`{"delivered":"none"}`)})
		return
	}
	if apply, _ := p["apply_switch"].(bool); id != "" && known && !assistantConv && todoID == "" && !fresh && !apply &&
		personSent(r.Context()) {
		// The owner's message to an Agent the hub lists (#10661), from a person's own full device: the row draws a held
		// one as the owner's bubble and only a full device can Retry or Delete it. Not a to-do's hand-off, an Agent's
		// first prompt or a launcher switch: those are decided now, not replayed later. It never overtakes messages held
		// for the Agent (NodeCall.Ordered: the manager refuses it, nothing typed, while they wait). From a client that
		// draws a held message it may be held itself while the computer is away, answered held instead of offline
		// (deliverOnce); one that does not would take 202 "later" for sent.
		holds := h.requestWants(r, featureAgentMessageHold)
		if terminal {
			holds = requestDeclares(r, featureAgentMessageHold)
		}
		if holds {
			ctx = withHold(ctx, l)
		} else {
			ctx = withOrdered(ctx)
		}
	}
	modelSent := func(status int) { // the picked model ran: the manager may remember it
		if status == http.StatusOK && picked != "" {
			launcher, _ := p["launcher"].(string)
			h.modelUsed(h.sessionOwner(m.cfg.ID, sid), m.cfg.ID, launcher, picked)
		}
	}
	if id == "" {
		status, body, outcome := h.deliverOnce(ctx, m, p)
		body = withLauncherNote(status, body, note)
		if assistantConv {
			h.conversationSent(m.cfg.ID, sid, "", status, body, outcome)
		}
		modelSent(status)
		if status == http.StatusOK && fresh {
			h.fresh.done(m.cfg.ID, sid)
		}
		if status == http.StatusOK && todoID != "" {
			h.linkSentTodo(ctx, m, todoID, sid, body)
		}
		writeRaw(w, status, body)
		return
	}
	f := &sendFlight{}
	if external && h.store == nil {
		f.status, f.body = callErrBytes(l, &rpc.Error{Code: "codex_send_guard", Message: i18n.T(l, "hub-send.guard_no_record"), Data: json.RawMessage(`{"delivered":"none"}`)})
		writeRaw(w, f.status, f.body)
		return
	}
	state := ""
	if h.store != nil {
		var err error
		if state, err = h.store.ClaimSend(ctx, id, m.cfg.ID, sid); err != nil {
			if external {
				f.status, f.body = callErrBytes(l, &rpc.Error{Code: "codex_send_guard", Message: i18n.T(l, "hub-send.guard_no_record"), Data: json.RawMessage(`{"delivered":"none"}`)})
				writeRaw(w, f.status, f.body)
				return
			}
			h.logf("send %s: %v", id, err) // no record: the send still goes, without the guard
		}
	}
	switch state {
	case sendDelivered:
		f.status, f.body = http.StatusOK, withLauncherNote(http.StatusOK, []byte("{}\n"), note)
	case sendSending, sendUnknown: // an earlier hub or connection was sending it and never learned the outcome
		f.status, f.body = callErrBytes(l, unknownDelivery(l, "hub-send.lost_before"))
	case sendLater: // its manager keeps it and sends it by itself
		f.status, f.body = laterBody(nil)
	default:
		var outcome string
		f.status, f.body, outcome = h.deliverOnce(ctx, m, p)
		f.body = withLauncherNote(f.status, f.body, note)
		if assistantConv {
			h.conversationSent(m.cfg.ID, sid, id, f.status, f.body, outcome)
		}
		keepCode := errorCodeOf(f.body)
		// A conversation at work whose task-follow-up role changed refuses the message (nothing typed): it waits for
		// the work to end instead of answering the owner "try again later" (assistantwire.CodeConversationBusy).
		busy := f.status == http.StatusConflict && keepCode == assistantwire.CodeConversationBusy
		if assistantConv && (outcome == sendUnknown || f.status >= http.StatusInternalServerError || busy) && asSent != nil {
			// Nothing was typed, or the connection ended before the outcome was known: the manager keeps the message,
			// sends it again in the first case and only looks for it in the second (the owner decides when it is not
			// found), and the app is told it is kept (#10194, #10503, #10669).
			if outcome == sendUnknown {
				keepCode = assistantwire.CodeDeliveryUnknown
			}
			if status, body, ok := h.keepUndelivered(context.WithoutCancel(ctx), m, sid, asSent, keepCode, apierr.Reason(f.body)); ok {
				f.status, f.body, outcome = status, body, sendLater
			}
		}
		if h.store != nil {
			commit := context.WithoutCancel(ctx)
			recorded, err := h.settleProjectReviewOwner(commit, r, m.cfg.ID, sid, id, outcome, indirect)
			if recorded && err != nil {
				// The receipt outranks the review notice: left in "sending", a retry of this id would be answered
				// "unknown" and a later drain could type the text again. Settle it alone; the notice is lost.
				h.logf("send %s: project review owner input not queued: %v", id, err)
			}
			if !recorded || err != nil {
				err = h.store.SettleSend(commit, id, outcome)
			}
			if err != nil {
				h.logf("send %s: %v", id, err)
			}
		}
	}
	modelSent(f.status)
	if f.status == http.StatusOK && fresh {
		h.fresh.done(m.cfg.ID, sid)
	}
	if f.status == http.StatusOK && todoID != "" {
		h.linkSentTodo(ctx, m, todoID, sid, f.body)
	}
	writeRaw(w, f.status, f.body)
}

// linkSentTodo links a todo to the session its message went to: a Codex that waited for its first message answers
// with the thread that message began (node/codex_waiting.go), and that thread is the todo's from now on.
func (h *Hub) linkSentTodo(ctx context.Context, m *Machine, todoID, sid string, body []byte) {
	var reply struct {
		Session *proto.Session `json:"session"`
	}
	var title, cwd, agent string
	if json.Unmarshal(body, &reply) == nil && reply.Session != nil && reply.Session.SessionID != "" {
		sid = reply.Session.SessionID
		title, cwd, agent = reply.Session.Title, reply.Session.Cwd, reply.Session.Agent
	}
	if s, ok := m.session(sid); ok {
		title, cwd, agent = s.Title, s.Cwd, s.Agent
	}
	if _, err := h.asst().TodoLink(ctx, assistantwire.TodoLinkParams{TodoID: todoID, MachineID: m.cfg.ID, SessionID: sid,
		Title: title, Cwd: cwd, Agent: agent}); err != nil {
		h.logf("sendMessage: link todo %s for %s/%s failed: %v", todoID, m.cfg.ID, sid, err)
	}
}

// deliverOnce asks the node to send and says what became of it: delivered, unknown (it may have been typed), or ""
// (nothing typed: refused, offline, failed before typing).
func (h *Hub) deliverOnce(ctx context.Context, m *Machine, p map[string]any) (int, []byte, string) {
	var out json.RawMessage
	sendTimingOf(ctx).mark("dispatch_started", true)
	sid, _ := p["session_id"].(string)
	sentAt := time.Now()
	if sid != "" {
		// Arm before calling the node so fast streaming deltas emitted while session.send is still
		// in flight are captured; definitive non-delivery or queued responses disarm below.
		h.ttft.arm(m.cfg.ID, sid, sentAt)
	}
	err := m.call(ctx, "session.send", p, &out)
	sendTimingOf(ctx).mark("node_returned", err == nil)
	switch outcome := sendOutcome(err); {
	case err == nil:
		if sid != "" && sendReturnedQueued(out) {
			h.ttft.disarm(m.cfg.ID, sid, sentAt)
		}
		if len(out) == 0 {
			out = json.RawMessage("{}")
		}
		return http.StatusOK, append(out, '\n'), outcome
	case rpcCodeOf(err) == devicewire.CodeHeld && holdOf(ctx) != nil:
		if sid != "" {
			h.ttft.disarm(m.cfg.ID, sid, sentAt)
		}
		// The machine's manager holds it until the computer is back (withHold, #10661): nothing was typed, it goes by
		// itself, and a retry of the id is answered as kept (sendLater). Only for a send that asked to be held: any other
		// held answer is an ordinary refusal, never a kept message its client cannot draw.
		status, body := heldBody(err)
		return status, body, sendLater
	case outcome == sendUnknown && !hasDelivered(err):
		l := h.contextLang(ctx)
		status, body := callErrBytes(l, unknownDelivery(l, "hub-send.lost_during"))
		return status, body, outcome
	default:
		if sid != "" && outcome == "" {
			h.ttft.disarm(m.cfg.ID, sid, sentAt)
			err = m.withSendDialog(ctx, sid, err)
		}
		if outcome == "" && errors.Is(err, errManagerOffline) {
			rec := &bufferedResponse{header: http.Header{}, lang: h.contextLang(ctx)}
			writeSendAssistantErr(rec, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()})
			return rec.status, rec.body.Bytes(), outcome
		}
		status, body := callErrBytes(h.contextLang(ctx), apierr.OnMachine(err, m.name())) // claude_missing on "<name>'s MacBook"
		return status, body, outcome
	}
}

// withSendDialog adds the terminal addressed by this send without replacing a target reported by the node.
func (m *Machine) withSendDialog(ctx context.Context, sid string, err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != screen.DialogOpen {
		return err
	}
	var data map[string]any
	if len(re.Data) != 0 && json.Unmarshal(re.Data, &data) != nil {
		return err
	}
	if sidTarget, _ := data["session_id"].(string); sidTarget != "" && sidTarget != sid {
		return err
	}
	if midTarget, _ := data["machine_id"].(string); midTarget != "" && midTarget != m.cfg.ID {
		return err
	}
	if row, ok := m.session(sid); ok && row.Capabilities != nil && !row.Capabilities.Terminal {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["machine_id"], data["session_id"] = m.cfg.ID, sid
	// Some hosts refuse without a title. Only a full device can request a fresh look at the visible terminal.
	if title, _ := data["dialog"].(string); strings.TrimSpace(title) == "" && principalOf(ctx).Role == roleFull {
		if title = m.sendDialogHead(ctx, sid); title != "" {
			data["dialog"] = title
		}
	}
	raw, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return err
	}
	return &rpc.Error{Code: re.Code, Message: re.Message, Data: raw}
}

func (m *Machine) sendDialogHead(ctx context.Context, sid string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var out struct {
		Text string `json:"text"`
	}
	if err := m.call(ctx, "session.screen", map[string]any{"session_id": sid, "lines": 60}, &out); err != nil {
		return ""
	}
	var title string
	switch s := screen.ReadCodex(out.Text); s.View {
	case screen.CodexApproval:
		title = s.Approval.Title
	case screen.CodexQuestion:
		title = s.Question.Question
	default:
		title = cmp.Or(screen.CodexDialogHead(out.Text), screen.DialogHead(screen.Plain(out.Text)))
	}
	if runes := []rune(title); len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return title
}

func sendReturnedQueued(out json.RawMessage) bool {
	if len(out) == 0 {
		return false
	}
	var flag struct {
		Queued bool `json:"queued"`
	}
	return json.Unmarshal(out, &flag) == nil && flag.Queued
}

// conversationSent tells the conversation's manager how an app's message to it went, delivered or not (the north
// star's availability, docs/assistant/north-star.md): only a send that ran, not a retry answered from its record. It
// names the message by its client_msg_id id ("" none), which its manager settles a direction's notes block by (#11392).
func (h *Hub) conversationSent(mid, sid, id string, status int, body []byte, outcome string) {
	r := assistantwire.ConversationSent{MachineID: mid, SessionID: sid, Outcome: assistantwire.ConversationFailed, ClientMsgID: id}
	switch {
	case status == http.StatusOK:
		r.Outcome = assistantwire.ConversationDelivered
	case outcome == sendUnknown:
		r.Outcome = assistantwire.ConversationUnknown
	default:
		var e struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		r.Code, r.Error = e.Code, cmp.Or(e.Detail, e.Error)
	}
	h.asstOf(h.sessionOwner(mid, sid)).notify(assistantwire.EventConversationSent, r)
}

// sendOutcome reads a node call's error: what may have reached the terminal.
func sendOutcome(err error) string {
	if err == nil {
		return sendDelivered
	}
	if errors.Is(err, errOffline) {
		return ""
	}
	var re *rpc.Error
	if errors.As(err, &re) { // the node answered: it marks failures after typing began
		var d struct {
			Delivered string `json:"delivered"`
		}
		_ = json.Unmarshal(re.Data, &d)
		if d.Delivered == sendUnknown {
			return sendUnknown
		}
		return ""
	}
	// No answer (the connection closed, the deadline passed): the node had the request and may have typed it.
	return sendUnknown
}

func hasDelivered(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && bytes.Contains(re.Data, []byte(`"delivered"`))
}

// unknownDelivery says, in l, why (a hub-send key) the outcome is unknown.
func unknownDelivery(l i18n.Lang, why string) error {
	return &rpc.Error{Message: i18n.T(l, "hub-send.unknown", i18n.Args{"why": i18n.T(l, why)}), Data: json.RawMessage(`{"delivered":"unknown"}`)}
}

// callErrBytes is callErr's response as bytes, so waiting retries can get the same one.
// callErrBytes is what callErr answers for err, in language l (coded refusals are explained in it, apierr.Call).
func callErrBytes(l i18n.Lang, err error) (int, []byte) {
	rec := &bufferedResponse{header: http.Header{}, lang: l}
	callErr(rec, err)
	return rec.status, rec.body.Bytes()
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	lang   i18n.Lang
}

func (b *bufferedResponse) Lang() i18n.Lang { return b.lang } // i18n.Carrier

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }

// writeRaw writes a send's answer, which the hub built itself (callErrBytes, laterBody) or a node returned (a 200): a
// server error's is the hub's own error, noted for its request line (apierr.NoteError) as WriteBody would.
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	var e map[string]any
	if status >= 500 && json.Unmarshal(body, &e) == nil {
		apierr.NoteError(w, status, e)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// resumesWithoutLauncher: a message to this session resumes it (it is not running) and nothing on its machine says
// which launcher it ran with (started outside Persona, no launcher matches its base URL and model). The node would
// fall back to plain claude, which may not be signed in there (node/sendsafe.go resumeLauncher).
func resumesWithoutLauncher(s proto.Session) bool {
	return s.Status == proto.StatusOffline && s.Launcher == "" && !s.Assistant && s.SourceKind == "" &&
		(s.Agent == "" || s.Agent == "claude")
}

// keepableMessage is the app's message as it came, for keepUndelivered: what a later send runs SendParams on again.
func keepableMessage(p map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"text", "images", "launcher", "model", "effort", "client_msg_id"} {
		if v, ok := p[k]; ok {
			out[k] = v
		}
	}
	return out
}

// keepUndelivered hands the owner's message to conversation sid that could not go in to the conversation's manager,
// which keeps it and sends it again by itself (assistant.keep_undelivered, #10194), and makes the app's answer: 202
// {"delivered":"later","undelivered":{…}}. False when the manager did not keep it (away, an older one, refused): the
// app then gets the failure as before.
func (h *Hub) keepUndelivered(ctx context.Context, m *Machine, sid string, msg map[string]any, code, why string) (int, []byte, bool) {
	kctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tried := time.Time{}
	if code == assistantwire.CodeDeliveryUnknown {
		tried = sendTimingOf(ctx).start
	}
	p := principalOf(ctx)
	credential := ""
	if p.Role == roleFull || p.Role == roleAnswer {
		credential = p.ID
	}
	res, err := callAssistant(kctx, h.asstOf(h.sessionOwner(m.cfg.ID, sid)), assistantwire.KeepUndeliveredMethod, assistantwire.KeepUndeliveredParams{
		MachineID: m.cfg.ID, SessionID: sid, Params: msg, Human: personSent(ctx), Lang: string(h.contextLang(ctx)), Code: code, Error: why, TriedAt: tried,
		CredentialID: credential, OriginDeviceID: wireids.DeviceID(originDeviceID(ctx)),
	})
	if err != nil || res.Undelivered == nil {
		h.logf("send to %s/%s: not kept for later: %v", m.cfg.ID, sid, err)
		return 0, nil, false
	}
	status, body := laterBody(res.Undelivered)
	return status, body, true
}

// laterBody is the answer to a message its manager keeps to send later; kept is nil for a retry answered from the
// send record (the row's undelivered list says the rest).
func laterBody(kept *proto.UndeliveredMessage) (int, []byte) {
	out := map[string]any{"delivered": sendLater}
	if kept != nil {
		out["undelivered"] = kept
	}
	b, _ := json.Marshal(out)
	return http.StatusAccepted, append(b, '\n')
}

// featureAgentMessageHold is what a client declares (client_hello's features, or X-Persona-Features on the request) when
// it draws a message held for an Agent on its row (proto.UndeliveredMessage.Computer); the hub lists the same name in
// proto.Features.
const featureAgentMessageHold = "agent_message_hold"

// holdKey marks the owner's message to an Agent (#10661): a heldSend, which busNode.Call puts on its session.send.
type holdKey struct{}

// heldSend is holdKey's mark: the message goes after any held for its Agent (NodeCall.Ordered), and, when hold, its
// manager may hold it while the computer is away, its answer worded in lang (NodeCall.Hold).
type heldSend struct {
	hold bool
	lang i18n.Lang
}

// withOrdered marks ctx's session.send as one that never overtakes messages held for its Agent.
func withOrdered(ctx context.Context) context.Context {
	return context.WithValue(ctx, holdKey{}, heldSend{})
}

// withHold is withOrdered for a message its manager may hold itself, its answer worded in l.
func withHold(ctx context.Context, l i18n.Lang) context.Context {
	return context.WithValue(ctx, holdKey{}, heldSend{hold: true, lang: l})
}

// orderedOf is the NodeCall.Ordered of ctx's session.send: whether withOrdered or withHold marked it.
func orderedOf(ctx context.Context) bool {
	_, ok := ctx.Value(holdKey{}).(heldSend)
	return ok
}

// holdOf is the NodeCall.Hold of ctx's session.send: nil unless withHold marked it.
func holdOf(ctx context.Context) *devicewire.Hold {
	m, ok := ctx.Value(holdKey{}).(heldSend)
	if !ok || !m.hold {
		return nil
	}
	return &devicewire.Hold{Lang: string(m.lang)}
}

// heldBody is the answer to a message the machine's manager holds (devicewire.CodeHeld): laterBody with the held
// message as its row shows it.
func heldBody(err error) (int, []byte) {
	var held devicewire.HeldAnswer
	var re *rpc.Error
	if errors.As(err, &re) {
		_ = json.Unmarshal(re.Data, &held)
	}
	return laterBody(held.Undelivered)
}

// writeSendAssistantErr answers an assistant error during sendMessageOnce / todoHandoff (before ClaimSend and
// deliverOnce touch a node): 503 assistant_offline explicitly states delivered:"none" and retryable:true so clients
// know nothing was sent and can safely retry once the assistant is back.
func writeSendAssistantErr(w http.ResponseWriter, err error) {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == assistantwire.CodeAssistantOffline {
		body := apierr.CodeBody(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
		body["delivered"] = "none"
		body["retryable"] = true
		writeErrorBody(w, http.StatusServiceUnavailable, body)
		return
	}
	writeAssistantErr(w, err)
}

func rpcCodeOf(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// errorCodeOf is an error answer's code ("" for none).
func errorCodeOf(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Code
}
