package hub

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// pushWants is which devices' settings take a push.
type pushWants struct {
	Kind string        // "" every device | input | done
	Took time.Duration // done: how long the turn took
	Long time.Duration // done: what counts as a long one
}

func (w pushWants) fn() func(NotifyPrefs) bool {
	switch w.Kind {
	case "input":
		return func(p NotifyPrefs) bool { return p.Input }
	case "done":
		return func(p NotifyPrefs) bool { return p.wantsDone(w.Took, w.Long) }
	}
	return nil
}

// notifyDone delivers the same alert and preference decision through both channels. Only the center has apps and
// devices: a member's computers are the center's machines. owner is the manager that asked for it: the devices that
// turned its pushes off get none (NotifyPrefs.MutedManagers).
func (h *Hub) notifyDone(owner, kind, machineID, machine, sessionID, title, text string, wants pushWants) {
	h.notifyDoneUnder(owner, kind, machineID, machine, sessionID, title, text, wants, "", "", pushPolicy{level: levelFor(kind, ""), done: kind == assistantwire.NotifyDone})
}

// pushOutcome is where one push went: the devices it was handed to now (a push route, a ws_notify connection or a
// browser's Web Push), the devices that could take this kind before their settings decided, the offline devices that
// keep it for their next connection (notifyring.go), and the ln-mode reason the push was held ("" when it was not).
// None of these is a delivery receipt.
type pushOutcome struct {
	devices, reachable, kept int
	held                     string
}

// reason is why nothing took the push now ("" when a device did): "no_device" when no device could take it,
// "filtered" when every one that could turned it off, as POST /api/v1/notify answers.
func (o pushOutcome) reason() string {
	switch {
	case o.devices > 0:
		return ""
	case o.reachable > 0:
		return "filtered"
	}
	return "no_device"
}

// notifyDoneUnder is notifyDone with the push's apns-collapse-id ("": one of its own) and the recording it is about
// ("": none; assistantwire.Notification.RecordingID), which the push and the notify carry as `recording_id`. It returns
// where the push went, with the ln-mode reason it was held, so the assistant can tell the model whether a notify_user
// notice reached a device, the briefing or nothing.
func (h *Hub) notifyDoneUnder(owner, kind, machineID, machine, sessionID, title, text string, wants pushWants, collapseID, recordingID string, policy pushPolicy) pushOutcome {
	todoPushID := ""
	if policy.cardID == "" {
		if id, _, ok := assistantwire.TodoPushKeyParts(policy.relatedKey); ok {
			todoPushID = id
		}
	}
	build := func(l i18n.Lang) map[string]any {
		p := donePayloadIn(l, machineID, machine, sessionID, title, text)
		if recordingID != "" {
			p["recording_id"] = recordingID
		}
		if policy.cardID != "" {
			// card_id: the app keeps the push until the hub's withdrawal names it (an app from before reads only
			// interaction_id, and clears it when its card list next comes without it).
			p["card_id"], p["interaction_id"] = policy.cardID, policy.cardID
		} else if todoPushID != "" {
			// Needs-user / to-do owner pushes carry card_id and todo_id (never interaction_id) so iOS recalls them on
			// todo-ask:<id> without NotificationWithdrawal.stale sweeping unanswered ones (#13916, #14388).
			p["card_id"], p["todo_id"] = assistantwire.TodoAskCardID(todoPushID), todoPushID
		}
		return p
	}
	var sent func(Device)
	if policy.cardID != "" {
		sent = func(d Device) {
			if hasFeature(d, featureWithdraw) {
				h.push.ownerCardPushedTo(policy.cardID, d.DeviceID)
			}
		}
	} else if todoPushID != "" {
		cardID := assistantwire.TodoAskCardID(todoPushID)
		sent = func(d Device) {
			if hasFeature(d, featureWithdraw) {
				h.push.ownerCardPushedTo(cardID, d.DeviceID)
			}
		}
	}
	if !h.sessionHiddenFromExtensions(machineID, sessionID) { // only of a session the extensions are shown (extension_private.go)
		h.emitToExtensions(owner, "agent.finished", map[string]any{"machine_id": machineID, "machine": machine, "session_id": sessionID, "title": title})
	}
	policy.manager, policy.title, policy.body = owner, title, text
	pushSelected, pushReachable, heldReason := h.push.sendFromPolicy(owner, policy, build, collapseID, wants.fn(), wants.quiet(), nil, sent)
	var ws wsReach
	if heldReason == "" {
		ws = h.notifyWSFrom(owner, kind, build, wants.fn(), wants.quiet())
	}
	return pushOutcome{devices: pushSelected + ws.selected, reachable: pushReachable + ws.reachable, kept: ws.kept, held: heldReason}
}

// managerPushCollapseID is the apns-collapse-id of manager's push with notification id (assistantwire.Notification.ID;
// "" without one): the same push asked for again once the hub no longer remembers handing it over (handedOverFor)
// takes the place of the one the phone shows instead of adding a second. A hash of the manager and the id, so it
// replaces only that manager's push of that id: never a card's, a decision's (td_), a notify API alert, an extension's
// push or the hub's own.
func managerPushCollapseID(manager, id string) string {
	if id == "" {
		return ""
	}
	manager = cmp.Or(manager, assistantwire.AssistantID)
	sum := sha256.New()
	sum.Write([]byte("push\x00"))
	sum.Write(binary.AppendUvarint(nil, uint64(len(manager))))
	sum.Write([]byte(manager))
	sum.Write([]byte(id))
	return "push:" + hex.EncodeToString(sum.Sum(nil)[:16])
}

// notifyReply reminds the devices that take "needs you" pushes of an agent still waiting on the user's answer (#5096).
// It is laid out as a finished turn's (answerable from the notification, opens the session), but nothing finished:
// the extensions hear no agent.finished. On the WebSocket it is a "reply" notify (not "done": Android puts away the
// finished notifications of seen sessions when it starts, and a reminder is about a seen one). owner is the manager
// that asked for it, as for notifyDone.
func (h *Hub) notifyReply(owner, machineID, machine, sessionID, title, text string) {
	h.notifyReplyKeyed(owner, machineID, machine, sessionID, title, text, "")
}

// notifyReplyKeyed is notifyReply with the manager's related key for the question it waits on (low-distraction mode keeps one key
// quiet for 12 hours after it rang): reminders of the same question merge, a new question of the same agent rings.
// "" (an older manager) keys by the session and what it asks. Returns the held reason when suppressed.
func (h *Hub) notifyReplyKeyed(owner, machineID, machine, sessionID, title, text, key string) string {
	build := func(l i18n.Lang) map[string]any { return donePayloadIn(l, machineID, machine, sessionID, title, text) }
	wants := pushWants{Kind: "input"}
	policy := pushPolicy{kind: assistantwire.NotifyReply, level: assistantwire.LevelP0, relatedKey: replyKey(sessionID, text, key), title: title, body: text}
	_, _, heldReason := h.push.sendFromPolicy(owner, policy, build, "", wants.fn(), wants.quiet(), nil, nil)
	if heldReason == "" {
		h.notifyWSFrom(owner, "reply", build, wants.fn(), wants.quiet())
	}
	return heldReason
}

// replyKey is the related key of a reply reminder: the manager's when it named one, else the session and a digest of
// the question, so one agent's second question is not taken for a repeat of its first.
func replyKey(sessionID, text, given string) string {
	if given = strings.TrimSpace(given); given != "" {
		return given
	}
	sum := sha256.Sum256([]byte(sessionID + "\x00" + strings.TrimSpace(text)))
	return "reply:" + sessionID + ":" + hex.EncodeToString(sum[:8])
}

// notifyDecide pushes a to-do whose conclusion waits for the owner's decision and recommends an option (#2056) to the
// devices that take "needs you" pushes: category TODO_DECIDE, whose OK takes the recommendation, or TODO_DECIDE_OPEN,
// with no action, when the manager says open_only (decidePayloadIn). On the WebSocket it is an "assistant" notify
// carrying the same `todo`: a client that does not know it shows a plain notification. Nothing finished: the
// extensions hear no agent.finished. One collapse id a to-do: a later round's push takes the place of the earlier one,
// whose OK could only answer "already handled".
func (h *Hub) notifyDecide(owner, machineID, machine, sessionID, title, text string, todo assistantwire.NotifyTodo) {
	h.notifyDecideKeyed(owner, machineID, machine, sessionID, title, text, todo, "")
}

// notifyDecideKeyed is notifyDecide with the manager's related key (assistantwire.Notification). A key of the to-do
// and a reason ("todo:<id>:decide", assistantwire.TodoPushKey) rings once while the to-do still waits on the owner for
// it (push_todo_dedup.go); without one (an older manager) the key is the round, and each round rings as before. The
// payload carries todo_id (never interaction_id: the iOS app clears a delivered push whose interaction_id is not in its
// card list as stale, and a decision is never in that list) and records withdraw-capable devices under TodoAskCardID so
// EventFleetResolved(todo-ask:<id>) recalls it (#13916).
func (h *Hub) notifyDecideKeyed(owner, machineID, machine, sessionID, title, text string, todo assistantwire.NotifyTodo, relatedKey string) string {
	key := strings.TrimSpace(relatedKey)
	if _, id, _, ok := structuredPushKeyParts(key); !ok || id != todo.ID {
		// One key a round: the same decision does not ring twice in 12 hours, but a new round of it (asked again) does.
		key = "todo:" + todo.ID + "@" + todo.AskedAt.UTC().Format(time.RFC3339Nano)
	}
	if !h.push.claimTodoPush(key) {
		return interceptReasonDedup // the same to-do's decision rang and still waits on the owner
	}
	// The decision's card (TodoAskCardID): once the to-do no longer waits on the owner, the manager names it
	// (EventFleetResolved) and the push leaves the devices it went to. card_id and todo_id only, never interaction_id:
	// an app from before would clear a delivered push whose interaction_id its card list lacks, and a decision is never
	// in it (#13916, #14388).
	card := ""
	if todo.ID != "" {
		card = assistantwire.TodoAskCardID(todo.ID)
	}
	build := func(i18n.Lang) map[string]any {
		p := decidePayloadIn(owner, machineID, machine, sessionID, title, text, todo)
		if card != "" {
			p["card_id"] = card
		}
		return p
	}
	var sent func(Device)
	if card != "" {
		sent = func(d Device) {
			if hasFeature(d, featureWithdraw) {
				h.push.ownerCardPushedTo(card, d.DeviceID)
			}
		}
	}
	wants := pushWants{Kind: "input"}
	sum := sha256.Sum256([]byte("todo_decide\x00" + todo.ID))
	policy := pushPolicy{kind: assistantwire.NotifyDecide, level: assistantwire.LevelP0, relatedKey: key, title: title, body: text}
	_, _, heldReason := h.push.sendFromPolicy(owner, policy, build, "td_"+hex.EncodeToString(sum[:12]), wants.fn(), wants.quiet(), nil, sent)
	if heldReason == "" {
		h.notifyWSFrom(owner, "assistant", build, wants.fn(), wants.quiet())
	}
	return heldReason
}

// notifyInteraction pushes a new card. linkCovered: owner says its Persona Link summary rings for this card
// (assistantwire.Notification.LinkCovered).
func (h *Hub) notifyInteraction(owner string, ix proto.Interaction, machine string, linkCovered bool) {
	h.notifyInteractionAt(owner, ix, machine, linkCovered, "", "")
}

// notifyInteractionAt is notifyInteraction with the manager's push level and related key for low-distraction mode (push_quiet.go):
// "" is a card's own, P0 keyed by the card. A supervisor's escalation names P1 when what it asks is not the owner's
// alone, and low-distraction mode then holds it for the "Needs you" list instead of alerting.
func (h *Hub) notifyInteractionAt(owner string, ix proto.Interaction, machine string, linkCovered bool, level, key string) {
	h.notifyInteractionHinted(owner, ix, machine, linkCovered, level, key, "", "")
}

// notifyInteractionHinted is notifyInteractionAt with what an attention mode reads of the card (push_attention.go): its
// depth (assistantwire.DepthDeep for a plan or a long review) and the track it comes from ("" for none). Returns the
// held reason when suppressed.
func (h *Hub) notifyInteractionHinted(owner string, ix proto.Interaction, machine string, linkCovered bool, level, key, depth, track string) string {
	switch level {
	case assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2:
	default:
		level = assistantwire.LevelP0 // none, or one this hub does not know: a card interrupts
	}
	if key = strings.TrimSpace(key); key == "" {
		key = "ix:" + ix.ID
	}
	if !h.push.claimTodoPushCard(key, ix.ID) {
		return interceptReasonDedup
	}
	build := func(l i18n.Lang) map[string]any { return interactionPayloadIn(l, ix, machine) }
	summary := ix.Summary
	if ix.Private { // a Just me turn's card: what it asks stays in the app
		summary = ""
	}
	if !h.hiddenFromExtensions(ix) { // only of a card the extensions are shown (extension_private.go)
		h.emitToExtensions(owner, "agent.needs_you", map[string]any{"machine_id": ix.MachineID, "machine": machine, "session_id": ix.SessionID,
			"interaction_id": ix.ID, "kind": ix.Kind, "summary": summary})
	}
	wants := pushWants{Kind: "input"}
	var heldReason string
	if ix.Kind == proto.IxElicitation {
		// Only what can show an MCP form or link: the phones that registered for one (#2433; a tap opens the card,
		// the push has no buttons) and the connections that declared it.
		policy := pushPolicy{kind: assistantwire.NotifyInput, level: level, relatedKey: key, title: summary, depth: depth, track: track}
		heldReason = h.push.sendCard(owner, policy, build, ix.ID, wants.fn(), wants.quiet(), takesElicitation)
		if heldReason == "" {
			h.notifyWSFrom(owner, "interaction", build, wants.fn(), wants.quiet(), "elicitation")
		}
	} else {
		// A card owner's Persona Link summary rings for: "Phone Muted" holds it back while one of owner's Persona Link
		// desktops is there too (desktops.go); which cards those are is owner's to say. The desktop app's online check
		// follows in sendCard and notifyWSFrom.
		fn := alertWanted(wants.fn(), linkCovered && h.linkDesktopOnline(owner))
		policy := pushPolicy{kind: assistantwire.NotifyInput, level: level, relatedKey: key, title: summary, depth: depth, track: track}
		heldReason = h.push.sendCard(owner, policy, build, ix.ID, fn, wants.quiet(), nil)
		if heldReason == "" {
			h.notifyWSFrom(owner, "interaction", build, fn, wants.quiet())
		}
	}
	return heldReason
}

// notifyWSFrom projects a push onto the opt-in WebSocket notification event, written in each connection's language
// (client.lang), by the authenticated device's preferences, even without an APNs token or key, and with what its quiet
// hours do to it (quiethours.go): skipped, or marked "quiet" for the client to post without a sound. manager is the
// one that asked for it: a device that turned that manager's pushes off (NotifyPrefs.MutedManagers) gets none, as with
// its push (Pusher.sendFrom); "" is the hub's own, for every device wants takes. Returns the selected count and the
// count of capable connections before preferences are applied, each with the browsers its Web Push went to (or could
// go to); neither is a client delivery receipt. A device without a push route keeps what its settings take for its
// next connection when no ws_notify socket accepts it (notifyring.go). kept counts those devices; a rejected socket
// still counts as selected.
func (h *Hub) notifyWSFrom(manager, kind string, build localized, wants func(NotifyPrefs) bool, rule quietRule, required ...string) wsReach {
	msgs := map[i18n.Lang]map[string]any{}
	msgIn := func(l i18n.Lang) map[string]any {
		if m, ok := msgs[l]; ok {
			return m
		}
		m := notifyMessage(kind, build(l))
		msgs[l] = m
		return m
	}
	type form struct {
		lang  i18n.Lang
		quiet bool
	}
	encoded := map[form][]byte{}
	msgFor := func(l i18n.Lang, quiet bool) []byte {
		if b, ok := encoded[form{l, quiet}]; ok {
			return b
		}
		msg := msgIn(l)
		if quiet {
			msg = maps.Clone(msg)
			msg["quiet"] = true
		}
		b, err := json.Marshal(msg)
		if err != nil {
			b = nil // sent nowhere, as sendJSON did
		}
		encoded[form{l, quiet}] = b
		return b
	}
	// Mute phone holds these back while a desktop is online, with the same rule as the APNs pushes (alertWanted): an
	// Android phone alerts from them. desktopOnline takes h.mu, so it is read before.
	wants = alertWanted(wants, h.desktopOnline())
	// Read before h.mu as well: the user's time zone asks the assistant's profile.
	now, loc := time.Now(), (*time.Location)(nil)
	if rule != quietNone && h.push != nil {
		now, loc = h.push.now(), h.userLocation()
	}
	hushAll := rule == quietHushAll || h.push.attentionHushes() // the stricter of quiet mode and quiet hours
	// wantsOn is whether device's settings take it, and whether inside its quiet hours (posted without a sound).
	wantsOn := func(device string) (ok, quiet bool) {
		prefs := defaultPrefs()
		if h.push != nil {
			prefs = h.push.prefs(device)
		}
		if prefs.mutes(manager) || wants != nil && !wants(prefs) {
			return false, false
		}
		quiet = rule != quietNone && prefs.Quiet.activeAt(now, loc)
		if hushAll {
			// Quiet attention mode, or a push it already hushed: posted without a sound, never skipped for it.
			return !(quiet && rule == quietSkip), true
		}
		return !(quiet && rule == quietSkip), quiet
	}
	var reach wsReach
	h.mu.Lock()
	defer h.mu.Unlock()
	live := map[string]bool{} // devices whose ws_notify socket accepted this event
	for c := range h.clients {
		if c.role == roleReadOnly || c.role == roleScoped || !c.has("ws_notify") {
			continue
		}
		if len(required) > 0 && !c.has(required[0]) {
			continue
		}
		reach.reachable++
		ok, quiet := wantsOn(c.device)
		if !ok {
			continue
		}
		if b := msgFor(c.lang(), quiet); b != nil && c.queue(b) {
			live[c.device] = true
		}
		reach.selected++
	}
	reach.kept = h.ringNotify(live, required, wantsOn, msgIn, msgFor)
	sent, capable := h.webPushNotify(kind, required, wantsOn, msgIn) // the browsers with no page open (webpush.go)
	reach.selected += sent
	reach.reachable += capable
	return reach
}

// wsReach is where notifyWSFrom put one notify event: selected and reachable as it says, and kept, the devices
// that keep it for their next connection (offline or rejected by every eligible socket).
type wsReach struct {
	selected, reachable, kept int
}

func notifyMessage(kind string, payload map[string]any) map[string]any {
	payload = cleanAlert(payload)
	aps := payload["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	msg := map[string]any{
		"type": "notify", "kind": kind,
		"machine_id": payload["machine_id"], "session_id": payload["session_id"],
		"title": alert["title"], "subtitle": alert["subtitle"], "body": alert["body"],
	}
	if id, ok := payload["interaction_id"]; ok {
		msg["interaction_id"] = id
	}
	if at, ok := payload["interaction_created_at"]; ok {
		msg["interaction_created_at"] = at
	}
	if id, ok := payload["id"]; ok { // alert: the caller's id (POST /api/v1/notify)
		msg["id"] = id
	}
	if en, ok := payload["extension_notification"]; ok {
		msg["extension_notification"] = en
	}
	if td, ok := payload["todo"]; ok { // a decision's push (decidePayloadIn)
		msg["todo"] = td
	}
	if id, ok := payload["todo_id"]; ok { // a to-do decision or needs-user push (#13916)
		msg["todo_id"] = id
	}
	if health, ok := payload["health"]; ok {
		msg["health"] = health
	}
	if id, ok := payload["recording_id"]; ok { // a meeting's minutes (notifyDoneUnder)
		msg["recording_id"] = id
	}
	return msg
}
