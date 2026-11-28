package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What comes from the nodes is the manager's (internal/assistant fleet_*.go; docs/server/machines.md "Who handles computer events"):
// the interactions, each Agent's state, which pushes go and when, the Live Activity's content, the nodes' MCP tools,
// the machines' monitoring. The hub shows and delivers what it reports:
//
//   - fleet.sessions: a machine's session list as the apps get it — kept on the machine (its sessions), sent to the
//     apps (broadcastSessions), answered on GET /api/v1/sessions;
//   - fleet.interaction / fleet.interaction_resolved / fleet.interactions: the pending interactions, kept here,
//     sent to the apps, answered on GET /api/v1/interactions and to a connecting app;
//   - fleet.live: the Live Activity's content and the island's numbers (liveactivity.go pushes it per device);
//   - notify.request: a push to deliver (push_send.go, notifyws.go): which devices take it is each device's settings.
//
// What is kept stays while the manager is away (the apps see the last of it); answers to the cards need it (the
// relayed routes answer 503 assistant_offline). The hub tells it what of the apps it needs (fleet.clients, fleet.seen,
// fleet.mutes), and still runs the phone's tools, the connectors', Cua's and ego's for it (app.call phone, hub).

// fleetCache is what the manager last told of the interactions and the fleet.
type fleetCache struct {
	mu           sync.Mutex
	interactions map[string]proto.Interaction
	muted        map[string][]string                 // machine → its sessions whose pushes are off, as the manager said
	live         *assistantwire.FleetLive            // the assistant's
	lives        map[string]*assistantwire.FleetLive // the other managers' (manager_state.go)
	clients      assistantwire.Clients               // what fleet.clients said last
	clientsSent  map[string]*busConn                 // the connections that received the current clients snapshot
	// clientsSend holds one fleet.clients from working out what is true to telling it (fleetClientsChanged).
	clientsSend sync.Mutex
}

// interactionResolvedEvent carries occurrence identity whenever the hub knows it.
// Older clients ignore the additive field; a zero-time legacy withdrawal remains
// ID-only. Clients use it to ignore an earlier resolution arriving after a same-ID
// replacement instead of guessing that every ID-only event names the latest card.
func interactionResolvedEvent(ix proto.Interaction) map[string]any {
	event := map[string]any{"type": "interaction_resolved", "id": ix.ID}
	if !ix.CreatedAt.IsZero() {
		event["created_at"] = ix.CreatedAt
	}
	return event
}

// Resolved mirror events carry no interaction object for the generic broadcast
// classifier to inspect. Pass their display-only identity explicitly so both
// live delivery and event-log replay retain the approval_mirrors capability fence.
func (h *Hub) broadcastInteractionResolved(ix proto.Interaction, event map[string]any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.broadcastToApprovalMirror("interaction_resolved", b, false, ix.MirrorOf != "")
}

func init() {
	handleBusEvent(assistantwire.EventFleetSessions, func(h *Hub, c *busConn, params json.RawMessage) {
		rest, rows, scanned := proto.ScanSessionList(params)
		if !scanned || !proto.DecodeChangedSessions {
			var p assistantwire.FleetSessions
			if json.Unmarshal(params, &p) != nil || h.ownsMachine(c, p.MachineID) != nil {
				return // only its manager lists a machine's sessions (ownership.go)
			}
			if m := h.localMachine(p.MachineID); m != nil {
				m.archivedOnlyOf(c.managerID())
			}
			h.takeFleetSessions(p)
			return
		}
		// The list apart from the rest (proto.ScanSessionList): decoded the same, but only its sessions whose bytes
		// changed since the machine's last list (Machine.lists); what does not decode drops the whole event, as before.
		var p assistantwire.FleetSessions
		if json.Unmarshal(rest, &p) != nil || h.ownsMachine(c, p.MachineID) != nil {
			return
		}
		m := h.localMachine(p.MachineID)
		if m == nil {
			return
		}
		list, raws, err := m.lists.Decode(rows)
		if err != nil {
			return
		}
		m.archivedOnlyOf(c.managerID())
		p.Sessions = list
		h.takeFleetSessionsFrom(m, p, raws)
	})
	// A machine's archived rows told apart from its list (assistantwire.Hello.ArchivedApart, #6836): kept for lookups
	// by session only. They change nothing the apps get, so nothing is pushed.
	handleBusEvent(assistantwire.EventFleetArchived, func(h *Hub, c *busConn, params json.RawMessage) {
		var p assistantwire.FleetArchived
		if json.Unmarshal(params, &p) != nil || h.ownsMachine(c, p.MachineID) != nil {
			return
		}
		if m := h.localMachine(p.MachineID); m != nil {
			m.setArchived(c.managerID(), p.Sessions)
		}
	})
	handleBusEvent(assistantwire.EventFleetInteraction, func(h *Hub, c *busConn, params json.RawMessage) {
		var p assistantwire.FleetInteraction
		if json.Unmarshal(params, &p) != nil || p.Interaction.ID == "" || reservedDisplayOriginal(p.Interaction) {
			return
		}
		if !h.cardAllowed(c.managerID(), p.Interaction) {
			h.logf("fleet: [%s] card %s on another manager's machine %q: dropped", c.managerID(), clip(p.Interaction.ID, 64), clip(p.Interaction.MachineID, 64))
			return
		}
		p.Interaction.Manager = c.managerID() // its manager's (ownership.go)
		f := &h.reported
		f.mu.Lock()
		if o, ok := f.interactions[p.Interaction.ID]; ok {
			if o.Manager != p.Interaction.Manager {
				f.mu.Unlock()
				return // another manager's card: not replaced
			}
			if !p.Interaction.CreatedAt.IsZero() && !o.CreatedAt.IsZero() && p.Interaction.CreatedAt.Before(o.CreatedAt) {
				f.mu.Unlock()
				return // a delayed update for an earlier occurrence
			}
		}
		if p.Interaction.MirrorOf != "" {
			origin, ok := f.interactions[p.Interaction.MirrorOf]
			if !ok || !validDisplayMirror(origin, p.Interaction) {
				f.mu.Unlock()
				return // no display copy without its live authoritative permission
			}
			// Display content is authoritative-only. Even a malformed manager event
			// cannot show one operation while its action resolves another.
			p.Interaction = refreshedDisplayMirror(origin, p.Interaction)
		}
		if f.interactions == nil {
			f.interactions = map[string]proto.Interaction{}
		}
		f.interactions[p.Interaction.ID] = p.Interaction
		updated := []proto.Interaction{p.Interaction}
		var resolved []proto.Interaction
		if p.Interaction.MirrorOf == "" {
			for id, mirror := range f.interactions {
				if mirror.MirrorOf != p.Interaction.ID || mirror.Manager != p.Interaction.Manager {
					continue
				}
				mirror = refreshedDisplayMirror(p.Interaction, mirror)
				if !validDisplayMirror(p.Interaction, mirror) {
					delete(f.interactions, id)
					resolved = append(resolved, mirror)
					continue
				}
				f.interactions[id] = mirror
				updated = append(updated, mirror)
			}
		}
		f.mu.Unlock()
		for _, ix := range updated {
			h.broadcast(map[string]any{"type": "interaction", "interaction": ix})
		}
		for _, ix := range resolved {
			h.push.withdraw(ix.ID, ix.CreatedAt)
			h.broadcastInteractionResolved(ix, interactionResolvedEvent(ix))
		}
		if !h.cardAllowed(c.managerID(), p.Interaction) { // its machine moved meanwhile (dropCard)
			h.dropCard(c.managerID(), p.Interaction.ID)
		}
	})
	handleBusEvent(assistantwire.EventFleetResolved, func(h *Hub, c *busConn, params json.RawMessage) {
		var p assistantwire.FleetResolved
		if json.Unmarshal(params, &p) != nil || p.ID == "" {
			return
		}
		f := &h.reported
		f.mu.Lock()
		current, exists := f.interactions[p.ID]
		if exists && current.Manager != c.managerID() {
			f.mu.Unlock()
			return // another manager's
		}
		if exists && !p.CreatedAt.IsZero() && !current.CreatedAt.Equal(p.CreatedAt) {
			f.mu.Unlock()
			return // a delayed resolution for an earlier occurrence
		}
		delete(f.interactions, p.ID)
		resolved := proto.Interaction{ID: p.ID, CreatedAt: p.CreatedAt}
		if exists {
			resolved = current
		}
		ids := []proto.Interaction{resolved}
		for id, ix := range f.interactions {
			if ix.MirrorOf == p.ID && ix.Manager == c.managerID() &&
				(p.CreatedAt.IsZero() || ix.CreatedAt.Equal(p.CreatedAt)) {
				delete(f.interactions, id)
				ids = append(ids, ix)
			}
		}
		f.mu.Unlock()
		for _, ix := range ids {
			h.push.withdraw(ix.ID, ix.CreatedAt) // a suspended app hears it by push (#4349)
			h.push.resolvedPushOver(ix.ID)
			event := interactionResolvedEvent(ix)
			if p.AnsweredBy != "" {
				event["answered_by"] = p.AnsweredBy // the app marks a question the assistant answered (docs/assistant/assistant.md)
				if p.Reason != "" {
					event["reason"] = p.Reason // "Assistant answered: X, because Y"
				}
				if len(p.Answers) > 0 {
					event["answers"] = p.Answers // what "Change answer" (POST …/reanswer) starts from
				}
			}
			h.broadcastInteractionResolved(ix, event)
		}
	})
	handleBusEvent(assistantwire.EventFleetInteractions, func(h *Hub, c *busConn, params json.RawMessage) {
		var p assistantwire.FleetInteractions
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.takeFleetInteractionsFrom(c.managerID(), p.Interactions)
	})
	handleBusEvent(assistantwire.EventFleetLive, func(h *Hub, c *busConn, params json.RawMessage) {
		var p assistantwire.FleetLive
		if json.Unmarshal(params, &p) != nil {
			return
		}
		f := &h.reported
		f.mu.Lock()
		if id := c.managerID(); id == assistantwire.AssistantID {
			f.live = &p
		} else {
			if f.lives == nil {
				f.lives = map[string]*assistantwire.FleetLive{}
			}
			f.lives[id] = &p
		}
		f.mu.Unlock()
		h.live.changed()
	})
}

// takeFleetSessions keeps a machine's list as the manager reported it and sends it to the apps.
func (h *Hub) takeFleetSessions(p assistantwire.FleetSessions) {
	if m := h.localMachine(p.MachineID); m != nil {
		h.takeFleetSessionsFrom(m, p, nil)
	}
}

// takeFleetSessionsFrom is takeFleetSessions for machine m, with raws, what each session came in (nil: unknown).
func (h *Hub) takeFleetSessionsFrom(m *Machine, p assistantwire.FleetSessions, raws []string) {
	list := p.Sessions
	if list == nil {
		list = []proto.Session{}
	}
	var newlyDeleted []string
	var wasDeleted map[string]bool
	for _, row := range list {
		if !row.IsDeleted() {
			continue
		}
		if wasDeleted == nil {
			wasDeleted = map[string]bool{}
			for _, previous := range m.keptSessions() {
				if previous.IsDeleted() {
					wasDeleted[previous.SessionID] = true
				}
			}
		}
		if !wasDeleted[row.SessionID] {
			newlyDeleted = append(newlyDeleted, row.SessionID)
		}
	}
	m.setSessionListRaws(list, raws)
	f := &h.reported
	f.mu.Lock()
	if f.muted == nil {
		f.muted = map[string][]string{}
	}
	f.muted[m.cfg.ID] = p.Muted
	f.mu.Unlock()
	for _, id := range newlyDeleted {
		h.conversationDeletedPush(m, id)
	}
	h.broadcastSessions(m, m.keptSessions())
}

// takeFleetInteractions is takeFleetInteractionsFrom the assistant the hub starts.
func (h *Hub) takeFleetInteractions(list []proto.Interaction) {
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, list)
}

// takeFleetInteractionsFrom replaces manager owner's pending interactions with what it has now that it
// (re)connected: those gone are over for the apps, the others are sent as they are. The other managers' stay.
func (h *Hub) takeFleetInteractionsFrom(owner string, list []proto.Interaction) {
	// Only cards on its own machines, or naming none (ownership.go cardAllowed).
	list = slices.DeleteFunc(slices.Clone(list), func(ix proto.Interaction) bool {
		return reservedDisplayOriginal(ix) || !h.cardAllowed(owner, ix)
	})
	f := &h.reported
	f.mu.Lock()
	// Another manager's card with the same id stays its (it is not this one's to replace).
	list = slices.DeleteFunc(list, func(ix proto.Interaction) bool {
		o, ok := f.interactions[ix.ID]
		return ok && o.Manager != owner && o.Manager != ""
	})
	// A malformed snapshot cannot make two entries with one display ID race for
	// which content is shown. The map below has always used the last entry; keep
	// that same entry's position and content in the ordered snapshot too.
	last := make(map[string]int, len(list))
	for i, ix := range list {
		last[ix.ID] = i
	}
	deduped := make([]proto.Interaction, 0, len(list))
	for i, ix := range list {
		if last[ix.ID] == i {
			deduped = append(deduped, ix)
		}
	}
	list = deduped
	now := make(map[string]proto.Interaction, len(list))
	for i := range list {
		list[i].Manager = owner
		now[list[i].ID] = list[i]
	}
	// A delayed reconnect snapshot has the same occurrence fence as a live
	// fleet.interaction event. Retain the newer card in both the authoritative map
	// and the ordered broadcast list; merely skipping the assignment would make
	// the withdrawal pass below remove the newer occurrence instead.
	for i, ix := range list {
		current, ok := f.interactions[ix.ID]
		if ok && !ix.CreatedAt.IsZero() && !current.CreatedAt.IsZero() && ix.CreatedAt.Before(current.CreatedAt) {
			list[i] = current
			now[ix.ID] = current
		}
	}
	// A display copy belongs to the hub's display state, not to the manager's
	// pending-operation list. Keep an already-published copy when a reconnect
	// snapshot contains its still-live original but omits the copy (for example
	// after approval_mirror_main was turned off for new cards). The original's
	// resolved event remains the only normal withdrawal boundary.
	for id, previous := range f.interactions {
		if previous.MirrorOf == "" || previous.Manager != owner {
			continue
		}
		if _, supplied := now[id]; supplied {
			continue
		}
		if origin, ok := now[previous.MirrorOf]; ok && origin.MirrorOf == "" {
			refreshed := refreshedDisplayMirror(origin, previous)
			now[id] = refreshed
			list = append(list, refreshed)
		}
	}
	for id, mirror := range now {
		if mirror.MirrorOf == "" {
			continue
		}
		origin, ok := now[mirror.MirrorOf]
		if !ok || !validDisplayMirror(origin, mirror) {
			delete(now, id)
			continue
		}
		// Snapshots get the same authoritative-only display content as live
		// events; identity validation alone must not admit forged text or deadlines.
		refreshed := refreshedDisplayMirror(origin, mirror)
		now[id] = refreshed
		for i := range list {
			if list[i].ID == id {
				list[i] = refreshed
			}
		}
	}
	list = slices.DeleteFunc(list, func(ix proto.Interaction) bool {
		_, ok := now[ix.ID]
		return !ok
	})
	old := map[string]proto.Interaction{}
	all := make(map[string]proto.Interaction, len(f.interactions)+len(now))
	for id, ix := range f.interactions {
		if ix.Manager == owner || ix.Manager == "" {
			old[id] = ix
		} else {
			all[id] = ix
		}
	}
	for id, ix := range now {
		all[id] = ix
	}
	f.interactions = all
	f.mu.Unlock()
	defer func() {
		for _, ix := range list { // after the apps were told: its machine moved meanwhile (dropCard)
			if !h.cardAllowed(owner, ix) {
				h.dropCard(owner, ix.ID)
			}
		}
	}()
	for id, ix := range old {
		if _, ok := now[id]; !ok {
			h.push.withdraw(id, ix.CreatedAt)
			h.broadcastInteractionResolved(ix, interactionResolvedEvent(ix))
		}
	}
	for _, ix := range list {
		if o, ok := old[ix.ID]; !ok || !sameJSON(o, ix) {
			h.broadcast(map[string]any{"type": "interaction", "interaction": ix})
		}
	}
}

// dropCard withdraws card id of manager owner's that was kept as its machine moved away. The move's withdrawal
// (withdrawCardsOn) may have run between the check and keeping it, or between keeping it and telling the apps, so a
// card is checked again once the apps were told: then it goes, and the apps are told it is over, even when the
// withdrawal took it already (it was shown again after). Another manager's card with the id stays.
func (h *Hub) dropCard(owner, id string) {
	f := &h.reported
	f.mu.Lock()
	ix, ok := f.interactions[id]
	other := ok && cmp.Or(ix.Manager, assistantwire.AssistantID) != owner
	ids := []proto.Interaction{{ID: id}}
	if ok && !other {
		ids[0] = ix
		delete(f.interactions, id)
		for copyID, mirror := range f.interactions {
			if mirror.MirrorOf == id && mirror.Manager == ix.Manager {
				delete(f.interactions, copyID)
				ids = append(ids, mirror)
			}
		}
	}
	f.mu.Unlock()
	if !other {
		for _, gone := range ids {
			h.push.withdraw(gone.ID, gone.CreatedAt)
			h.broadcastInteractionResolved(gone, interactionResolvedEvent(gone))
		}
	}
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// decorated is a machine's sessions as the manager listed them, stamped: its last list, with the archived rows it
// told apart (Machine.sessionList). The apps get it through Hub.shown, which leaves those out.
func (h *Hub) decorated(m *Machine) []proto.Session {
	list := m.sessionList()
	h.stampSessions(m, list)
	return list
}

// stampSessions puts on each of m's sessions the manager it belongs to: its machine's (ownership.go).
func (h *Hub) stampSessions(m *Machine, list []proto.Session) {
	owner := h.machineOwner(m.cfg.ID)
	for i := range list {
		list[i].Manager = owner
	}
}

// stamped is list with manager on each session, as stampSessions puts it: list itself when every session has it
// already, else a copy. list is never changed.
func stamped(list []proto.Session, manager string) []proto.Session {
	for i := range list {
		if list[i].Manager != manager {
			out := slices.Clone(list)
			for j := i; j < len(out); j++ {
				out[j].Manager = manager
			}
			return out
		}
	}
	return list
}

// decoratedSession is one of m's sessions as the apps get it: that one copied and stamped, not the whole list.
func (h *Hub) decoratedSession(m *Machine, sid string) (proto.Session, bool) {
	s, ok := m.session(sid)
	if ok {
		s.Manager = h.machineOwner(m.cfg.ID)
	}
	return s, ok
}

// listedWaitMax is how long awaitListed waits at most (Hub.listedWait shortens it in tests: their fake nodes start
// sessions that are never listed, so each start would wait all of it).
const listedWaitMax = 2 * time.Second

// awaitListed waits a little (listedWaitMax at most) for the manager's list of m to have session sid: a session just
// started is shown as the manager has it.
func (h *Hub) awaitListed(ctx context.Context, m *Machine, sid string) (proto.Session, bool) {
	wait := listedWaitMax
	if h.listedWait > 0 {
		wait = h.listedWait
	}
	for end := time.Now().Add(wait); ; {
		if s, ok := m.session(sid); ok {
			return s, true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return proto.Session{}, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// mutedSessions are those of the machine's sessions whose pushes are off, as the manager said.
func (h *Hub) mutedSessions(mid string, _ []proto.Session) []string {
	f := &h.reported
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.muted[mid])
}

// pendingInteractions are the pending interactions as the manager told them, oldest first.
func (h *Hub) pendingInteractions() []proto.Interaction {
	f := &h.reported
	f.mu.Lock()
	out := make([]proto.Interaction, 0, len(f.interactions))
	for _, ix := range f.interactions {
		out = append(out, ix)
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// pendingInteraction is one of them; false when it is not pending.
func (h *Hub) pendingInteraction(id string) (proto.Interaction, bool) {
	f := &h.reported
	f.mu.Lock()
	defer f.mu.Unlock()
	ix, ok := f.interactions[id]
	return ix, ok
}

// fleetLive is the Live Activity's content and numbers as the manager told them last.
// The Live Activity is one per phone: it shows the default manager's, else the assistant's (manager_state.go).
func (h *Hub) fleetLive() (FleetState, FleetStats) {
	st, stats, _ := h.fleetLiveOf()
	return st, stats
}

// fleetLiveOf is fleetLive with the manager whose it is: the activity counts that manager's computers' agents only.
func (h *Hub) fleetLiveOf() (FleetState, FleetStats, string) {
	def := h.defaultOwner(context.Background())
	f := &h.reported
	f.mu.Lock()
	defer f.mu.Unlock()
	live, from := f.live, assistantwire.AssistantID
	if l := f.lives[def]; l != nil && def != assistantwire.AssistantID {
		live, from = l, def
	}
	if live == nil {
		return FleetState{}, FleetStats{}, from
	}
	return live.State, live.Stats, from
}

// cardAnswerGrace is how much longer than a card's expiry the hub waits for the manager's answer about it. The manager
// ends an expired card itself and records how it ended; its answer should not lose to the hub's deadline over a slow
// write or two clocks a little apart.
const cardAnswerGrace = 30 * time.Second

// callAssistantAsIs uses the caller's deadline, if any, for a typed assistant call.
func callAssistantAsIs[P, R any](ctx context.Context, a assistantRemote, method buswire.Method[P, R], params P) (R, error) {
	caller := managerBusCaller{id: a.to(ctx), call: a.h.callBusAsIs}
	result, err := buswire.Call(ctx, caller, method, params)
	return result, assistantOffline(err)
}

// waitInteraction asks the user on the phone for one of the hub's own features (a mail send, Cua's apps, ego's
// scripts, persona_settings): the manager shows the card and waits for its answer (fleet.ask). Without the manager
// nothing can be asked: a dismiss.
//
// The hub waits as long as the card lives, not callBus's two minutes, which withdrew a card good for fifteen (#5907):
// until its expiry and cardAnswerGrace more, or, with no expiry given, for the manager's own wait for a card (its
// manager.json hook_timeout_minutes). When the hub stops first (its caller went away, the grace ran out), the manager
// withdraws the card (serveBus's Cancels).
func (h *Hub) waitInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	if h.askVia != nil { // tests play the manager
		return h.askVia(ctx, ix)
	}
	if !ix.ExpiresAt.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, ix.ExpiresAt.Add(cardAnswerGrace))
		defer cancel()
	}
	r, err := callAssistantAsIs(ctx, h.asstOf(h.machineOwner(ix.MachineID)), assistantwire.FleetAskMethod,
		assistantwire.FleetAskParams{Interaction: ix})
	if err != nil {
		h.logf("[%s] ask %s: %v", ix.MachineID, ix.ToolName, err)
		return proto.Resolution{Dismiss: true}, nil
	}
	return r, nil
}

// ---- what the hub tells the manager of the apps ----

// hasFullAppOnline reports whether at least one full user app (excluding runtime CLI simulators and
// restricted/extension clients) is currently connected to the hub.
func (h *Hub) hasFullAppOnline() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.role == roleFull && !c.runtimeCLI {
			return true
		}
	}
	return false
}

// fleetClientsChanged tells the managers what the apps can take now, when it changed (fleet.clients): an app that
// takes MCP forms and links is connected, or a phone that registered for them would be pushed one (#2433), or whether
// a full user app is online. It runs after an app connects, says hello or goes, and after the devices or their settings
// change. One at a time, each with what is true then, so an older state never overtakes a newer one.
func (h *Hub) fleetClientsChanged() {
	f := &h.reported
	f.clientsSend.Lock()
	defer f.clientsSend.Unlock()
	c := assistantwire.Clients{
		Elicitation:     h.hasElicitationClient(),
		ElicitationPush: h.push.pushesElicitation(h.desktopOnline()),
		AppOnline:       h.hasFullAppOnline(),
		Viewing:         h.viewedSessions(),
	}
	f.mu.Lock()
	same := f.clients.Elicitation == c.Elicitation && f.clients.ElicitationPush == c.ElicitationPush &&
		f.clients.AppOnline == c.AppOnline && slices.Equal(f.clients.Viewing, c.Viewing)
	delivered := f.clientsSent
	f.clients = c
	f.mu.Unlock()
	next := map[string]*busConn{}
	for _, p := range h.busRoster() {
		if p.Role != bus.RoleManager {
			continue
		}
		conn := h.busConnOf(p.ID)
		if conn == nil {
			continue
		}
		if same && delivered[p.ID] == conn {
			next[p.ID] = conn
			continue
		}
		if conn.peer.Notify(assistantwire.EventClients, h.clientsForManager(c, p.ID)) == nil {
			next[p.ID] = conn
		}
	}
	f.mu.Lock()
	f.clientsSent = next
	f.mu.Unlock()
}

// viewedSessions reports owner presence from apps authenticated with a full credential.
func (h *Hub) viewedSessions() []assistantwire.SessionRef {
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		if c.role == roleFull {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()
	seen := map[[2]string]bool{}
	for _, c := range clients {
		c.mu.Lock()
		for key := range c.subs {
			seen[key] = true
		}
		for key := range c.termSubs {
			seen[[2]string{key[0], key[1]}] = true
		}
		c.mu.Unlock()
	}
	out := make([]assistantwire.SessionRef, 0, len(seen))
	for key := range seen {
		out = append(out, assistantwire.SessionRef{MachineID: key[0], SessionID: key[1]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].MachineID < out[j].MachineID ||
			out[i].MachineID == out[j].MachineID && out[i].SessionID < out[j].SessionID
	})
	return out
}

func (h *Hub) clientsForManager(c assistantwire.Clients, manager string) assistantwire.Clients {
	out := c
	out.Viewing = nil
	for _, ref := range c.Viewing {
		if h.sessionOwner(ref.MachineID, ref.SessionID) == manager {
			out.Viewing = append(out.Viewing, ref)
		}
	}
	return out
}

// clientsSettle runs on a manager connection's first word after its handshake, as mutesSettle does, and tells it what
// the apps can take now (fleet.clients). The hub says it again only when it changes, and at a handshake only to the
// assistant: a manager connected since would not know a phone takes MCP forms by push, which seldom changes (#2433).
// After the first word it lands after the hello's answer.
func (h *Hub) clientsSettle(c *busConn) {
	if c == nil || c.ident.Role != bus.RoleManager || !c.clientsSettled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		h.fleetClientsChanged() // a change not told yet goes to every manager
		f := &h.reported
		f.clientsSend.Lock()
		defer f.clientsSend.Unlock()
		f.mu.Lock()
		clients := f.clients
		f.mu.Unlock()
		if h.busCurrent(c) {
			err := c.peer.Notify(assistantwire.EventClients, h.clientsForManager(clients, c.ident.ID))
			f.mu.Lock()
			if err != nil {
				delete(f.clientsSent, c.ident.ID)
			} else {
				if f.clientsSent == nil {
					f.clientsSent = map[string]*busConn{}
				}
				f.clientsSent[c.ident.ID] = c
			}
			f.mu.Unlock()
		}
	}()
}

// fleetClientsRefresh also tells connected managers after an ownership change: their scoped viewers can change
// even when the global viewer set does not.
func (h *Hub) fleetClientsRefresh() {
	f := &h.reported
	f.clientsSend.Lock()
	f.mu.Lock()
	f.clientsSent = nil
	f.mu.Unlock()
	f.clientsSend.Unlock()
	h.fleetClientsChanged()
}

// markSeen tells the manager the user looked at a session (an app's WebSocket seen).
func (h *Hub) markSeen(machine, session string) {
	_ = h.notifyBus(h.sessionOwner(machine, session), assistantwire.EventSeen, assistantwire.FleetSeen{MachineID: machine, SessionID: session})
}

// markInteractionSeen tells the owning manager that a full user app displayed pending interaction id to the user.
func (h *Hub) markInteractionSeen(id string) {
	if id == "" {
		return
	}
	ix, ok := h.pendingInteraction(id)
	if !ok {
		return
	}
	_ = h.notifyBus(cmp.Or(ix.Manager, assistantwire.AssistantID), assistantwire.EventInteractionSeen, assistantwire.FleetInteractionSeen{InteractionID: id})
}

// mutesChanged tells every manager the muted spaces / agents (the hub's settings mutes) after a change and after a
// machine moved; each also gets them in its hello's answer (assistant_link.go). They are the hub's: a manager never
// reads them from a database. Each manager's fleet.mutes has its own machines' only (mutesFor). Mutes that could not
// be read are not sent: the managers keep the last ones (an empty set would push every muted agent). Sends are one at
// a time, each with the mutes as they are then, so an older set never overtakes a newer one.
func (h *Hub) mutesChanged() {
	h.mutesSend.Lock()
	defer h.mutesSend.Unlock()
	m, ok := h.mutesEvent()
	if !ok {
		return
	}
	for _, peer := range h.busRoster() {
		if peer.Role == bus.RoleManager {
			_ = h.notifyBus(peer.ID, assistantwire.EventMutes, h.mutesFor(peer.ID, m))
		}
	}
}

// mutesFor is m as manager id gets it: the mutes of its own machines only ("<machine>/<space or session>",
// ownership.go), never another manager's.
func (h *Hub) mutesFor(id string, m assistantwire.FleetMutes) assistantwire.FleetMutes {
	if m.Mutes == nil {
		return m
	}
	mine := func(keys []string) []string {
		if keys == nil {
			return nil
		}
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			if mid, _, _ := strings.Cut(k, "/"); h.machineOwner(mid) == id {
				out = append(out, k)
			}
		}
		return out
	}
	return assistantwire.FleetMutes{Mutes: &assistantwire.Mutes{Spaces: mine(m.Mutes.Spaces), Sessions: mine(m.Mutes.Sessions),
		Allowed: mine(m.Mutes.Allowed)}}
}

// mutesRetry hands manager id the mutes once they can be read (they were not when it connected), while it is
// connected; it gives up after mutesRetries tries (the manager keeps the ones it had; the next change sends them).
func (h *Hub) mutesRetry(id string) {
	for range mutesRetries {
		time.Sleep(mutesRetryEvery)
		if h.busConnOf(id) == nil {
			return
		}
		h.mutesSend.Lock()
		m, ok := h.mutesEvent()
		if ok {
			_ = h.notifyBus(id, assistantwire.EventMutes, h.mutesFor(id, m))
		}
		h.mutesSend.Unlock()
		if ok {
			return
		}
	}
	h.logf("mutes: still unreadable: [%s] keeps the ones it had", id)
}

// mutesRetryEvery and mutesRetries: how often and how many times mutesRetry tries (tests shorten them).
var (
	mutesRetryEvery = 5 * time.Second
	mutesRetries    = 60
)

func (h *Hub) mutesEvent() (assistantwire.FleetMutes, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, ok := h.readMutes(ctx)
	return assistantwire.FleetMutes{Mutes: &assistantwire.Mutes{Spaces: p.Spaces, Sessions: p.Sessions, Allowed: p.Allowed}}, ok
}

// mutesSettle runs on a manager connection's first word after its handshake and sends it the mutes once more. The
// hello's answer and a fleet.mutes sent while it was on its way race at the manager (the answer is taken on another
// goroutine than the events, and events of its last connection may still be queued): either could be taken last.
// The manager says nothing before it took the answer (bus.Client), so these land after it. A manager from before the
// answer carried the mutes has them from here.
func (h *Hub) mutesSettle(c *busConn) {
	if c == nil || c.ident.Role != bus.RoleManager || !c.mutesSettled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		h.mutesSend.Lock()
		defer h.mutesSend.Unlock()
		if m, ok := h.mutesEvent(); ok && h.busCurrent(c) { // unreadable: mutesRetry's
			_ = c.peer.Notify(assistantwire.EventMutes, h.mutesFor(c.managerID(), m))
		}
	}()
}

// fleetRoutesRelayed relays the apps' requests on the manager's routes on the interactions and the sessions' states
// (it answers them as the hub did); while it is not connected they are answered 503 assistant_offline. A device
// that may only answer is held to a card's own choices first, here.
func (h *Hub) fleetRoutesRelayed(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/interactions/{id}/resolve", h.withoutTerminalControls(h.withNormalizedDisplayMirror(h.withClientOp("resolve", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeBodyErr(w, err, err)
			return
		}
		var res proto.Resolution
		if err := json.Unmarshal(body, &res); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		r, ok := h.handoffGate(w, r, r.PathValue("id"), res) // who may approve a hand-off card (secret_handoff.go)
		if !ok {
			return
		}
		if answerOnly(r) && (!answerMayResolve(res) || h.picksFreeText(r.PathValue("id"), res)) {
			writeErr(w, http.StatusForbidden, errors.New("this device can only answer with a card's own choices"))
			return
		}
		pr := principalOf(r.Context())
		if pr.Role == roleScoped {
			ix, pending := h.pendingInteraction(r.PathValue("id"))
			if !pending || !h.scopedQuestion(ix) || res.Approve != nil || res.Allow != nil || res.Mode != "" || res.Always || len(res.Keys) > 0 || len(res.PreKeys) > 0 {
				scopedPermissionDenied(w, "")
				return
			}
		}
		if pr.Role == roleExtension {
			// A pending card the token is never shown (health space, private) is answered as if it did not exist, the
			// same 404 its read routes give, before any answer is forwarded (#12216).
			if _, hidden := h.pendingCardHiddenFromExtensions(r.PathValue("id")); hidden {
				writeErr(w, http.StatusNotFound, errDeleteUnknown)
				return
			}
			if why, mode, mid, sid := h.extensionResolveRefused(pr, r.PathValue("id"), res); why != "" { // extension_modes.go
				h.refuseExtensionMode(w, r, pr, mid, sid, mode, why)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.forwardOwned(w, h.captureOwnerAnswer(r), h.byInteraction)
	}))))
	// A clarification is an owner's free-text message to the originating
	// conversation, not a card answer. Restricted answer-only devices and
	// extension principals cannot speak as the owner here. The operation receipt
	// and the manager's client_msg_id together make an unchanged retry one message.
	api.HandleFunc("POST /api/v1/interactions/{id}/clarify", h.withoutTerminalControls(h.withNormalizedDisplayMirror(h.withClientOp("clarify", func(w http.ResponseWriter, r *http.Request) {
		if answerOnly(r) || principalOf(r.Context()).Role == roleExtension {
			writeErr(w, http.StatusForbidden, errors.New("only the owner can ask an approval question"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeBodyErr(w, err, err)
			return
		}
		var in struct {
			Text        string `json:"text"`
			ClientMsgID string `json:"client_msg_id"`
		}
		if json.Unmarshal(body, &in) == nil {
			clientID := strings.TrimSpace(in.ClientMsgID)
			ix, pending := h.pendingInteraction(r.PathValue("id"))
			question := assistantwire.ClarificationProof{InteractionID: r.PathValue("id"), MachineID: ix.MachineID,
				SessionID: ix.SessionID, Text: in.Text, ClientMsgID: clientID}
			if pending && ix.CanClarify && utf8.RuneCountInString(strings.TrimSpace(in.Text)) <= 1000 {
				admitted := h.captureHumanSubmission(r, in.Text)
				if _, verified := admitted.Context().Value(trustedSubmissionKey{}).(devicewire.Submission); verified {
					if clientID == "" {
						clientID = "approval-question-" + ix.ID + "-" + randomHex(6)
						question.ClientMsgID = clientID
						var fields map[string]json.RawMessage
						if json.Unmarshal(body, &fields) == nil {
							fields["client_msg_id"], _ = json.Marshal(clientID)
							body, _ = json.Marshal(fields)
						}
					}
					if validHubClarification(question) {
						manager := cmp.Or(ix.Manager, assistantwire.AssistantID)
						token := h.clarificationProofs.issue(manager, question)
						r = withClarificationRequestProof(admitted, token)
					}
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.forwardOwned(w, r, h.byInteraction)
	}))))
	// Body client_id uses the same durable operation receipts as resolve.
	api.HandleFunc("POST /api/v1/interactions/{id}/choose", h.withReplyReceipt("choose", h.serveChoose))
	// "Change answer": a question the assistant answered gets the user's answer (assistant/fleet_delegate.go); a device that may
	// only answer cards does not send agents messages.
	api.HandleFunc("POST /api/v1/interactions/{id}/reanswer", h.withoutTerminalControls(func(w http.ResponseWriter, r *http.Request) {
		if answerOnly(r) {
			writeErr(w, http.StatusForbidden, errors.New("this device can only answer with a card's own choices"))
			return
		}
		h.forwardOwned(w, r, h.byRequest) // answered already: no longer pending, the request names its manager
	}))
	// Card view receipt: starts the short countdown on an unseen owner-only timed card (e.g. secret_run / secret_handoff).
	// Restricted devices and extension tokens cannot claim the owner saw the card.
	api.HandleFunc("POST /api/v1/interactions/{id}/seen", h.withoutTerminalControls(func(w http.ResponseWriter, r *http.Request) {
		if answerOnly(r) || principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("only a full user device can mark a card seen"))
			return
		}
		h.forwardOwned(w, r, h.byInteraction)
	}))
	api.HandleFunc("GET /api/v1/interactions/history", func(w http.ResponseWriter, r *http.Request) {
		if h.requestHasElicitation(r) {
			r.Header.Set(assistantwire.ElicitationHeader, "1")
		} else {
			r.Header.Del(assistantwire.ElicitationHeader)
		}
		h.forwardOwned(w, r, h.byRequest)
	})
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/seen", h.extensionControllable(h.withMachine(func(w http.ResponseWriter, r *http.Request, _ *Machine) {
		h.forwardOwned(w, r, h.bySession)
	})))
}

// ---- what the manager asks of the hub for the nodes ----

// fleetAppCall answers the manager's app.call for what is still the hub's to run for the nodes: the phone's tools;
// false for the others. (The connectors', Cua's and ego's tools, the gated commands' text, lark-cli's settings and the
// channels' owner actions are the manager's extensions': internal/extmgr, in its process.)
func (h *Hub) fleetAppCall(ctx context.Context, from string, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var a struct {
		Assistant     bool            `json:"assistant"`
		MachineID     string          `json:"machine_id"`
		SessionID     string          `json:"session_id"`
		Name          string          `json:"name"`
		Args          json.RawMessage `json:"args"`
		ToolName      string          `json:"tool_name"`
		Summary       string          `json:"summary"`
		InteractionID string          `json:"interaction_id"`
		Action        string          `json:"action"`
		CallID        json.RawMessage `json:"call_id"`
	}
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
	}
	var callID string
	_ = json.Unmarshal(a.CallID, &callID)
	var out any
	switch p.AppID + " " + p.Method {
	case assistantwire.AppPhone + " " + assistantwire.PhoneTools:
		out = map[string]any{"tools": h.phoneToolList()}
	case assistantwire.AppPhone + " " + assistantwire.PhoneCall:
		if a.Name == runtimeCLITool || !strings.HasPrefix(a.Name, "phone_") {
			out = proto.TextResult("unknown tool "+a.Name, true)
		} else {
			out = h.callPhoneWithWait(ctx, a.Name, a.Args, h.mcpWaitReporter(from, a.MachineID, callID))
		}
	default:
		return nil, false, nil
	}
	b, err := json.Marshal(out)
	return b, true, err
}

// ---- the pushes the manager decided on ----

// pushRecordingID is the recording id a manager's push names (assistantwire.Notification.RecordingID), lowercased, or
// "" when it is not a recording id (a UUID): the push carries it as it is and an app opens it.
func pushRecordingID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if !clientOpUUID.MatchString(id) {
		return ""
	}
	return id
}

// deliverPush is deliverPushFrom the assistant the hub starts.
func (h *Hub) deliverPush(n assistantwire.Notification) error {
	_, err := h.deliverPushFrom(assistantwire.AssistantID, n)
	return err
}

// relatedTodoID maps only source forms with a defined meaning. A quiet supervisor's ask:<id> is a real to-do, but a
// model-controlled notify_user key (Asked=true) is not promoted from that prefix. todo:<id> is already a form every
// held-push client follows, so carrying it into todo_id preserves that destination's precedence over the session.
func relatedTodoID(key string, allowAsk bool) string {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "todo:") { // todo:<id>, the legacy todo:<id>@<round> and todo:<id>:<reason>
		return assistantwire.RelatedTodoID(key)
	}
	if allowAsk {
		if id, ok := strings.CutPrefix(key, "ask:"); ok {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// deliverPushFrom delivers what manager from asks (notify.request): an assistant's push, an agent's finished turn, a
// card. Only the center has apps and devices. Only about its own (ownership.go): a card's push is for a card it
// reported that is pending, and the hub pushes its own copy of it; any other push names a machine and session of its
// own, or none. What the extensions are told of it (agent.finished, agent.needs_you) goes to from's only, and the
// devices that muted from (NotifyPrefs.MutedManagers) get none of it.
//
// It returns where an assistant's push or a finished turn went (nil for a card, a reply reminder or a decision when it
// was not held; when held, held carries the intercept reason so notify.request reports held/reason consistently without
// breaking existing checks for nil devices): the devices it was handed to, the offline ones that keep it, and the
// ln-mode reason it was held. The assistant's notify_user tool tells the model from it whether the notice reached a
// device, the briefing or nothing, and counts it as an interruption only when a device took it.
func (h *Hub) deliverPushFrom(from string, n assistantwire.Notification) (out *pushOutcome, err error) {
	switch n.Kind {
	case assistantwire.NotifyInput:
		if n.Interaction == nil {
			return nil, errors.New("an input push needs its interaction")
		}
		ix, ok := h.pendingInteraction(n.Interaction.ID)
		if !ok || cmp.Or(ix.Manager, assistantwire.AssistantID) != from {
			return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: "an input push is for a pending card the manager reported"}
		}
		if held := h.notifyInteractionHinted(from, ix, n.Subtitle, n.LinkCovered, n.Level, n.RelatedKey, n.Depth, n.Track); held != "" {
			return &pushOutcome{held: held}, nil
		}
		return nil, nil
	case assistantwire.NotifyPush, assistantwire.NotifyDone, assistantwire.NotifyReply, assistantwire.NotifyDecide:
		if !h.ownsRef(from, n.DeviceID, n.SessionID) {
			return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("a push about machine %q, session %q: another manager's",
				clip(n.DeviceID, 64), clip(n.SessionID, 64))}
		}
	}
	switch n.Kind {
	case assistantwire.NotifyPush: // one with an id shows once, sent again however late (a day recap's push)
		if !h.push.claimTodoPushCard(n.RelatedKey, n.CardID) {
			// The same to-do's or concern's push about the same reason/state rang and still waits on the owner.
			return &pushOutcome{held: interceptReasonDedup}, nil
		}
		policy := pushPolicy{kind: assistantwire.NotifyPush, level: levelFor(n.Kind, n.Level), relatedKey: n.RelatedKey, todoID: relatedTodoID(n.RelatedKey, !n.Asked),
			depth: n.Depth, track: n.Track, cardID: n.CardID, derivedLevel: n.Level == "", asked: n.Asked, mainConv: h.isMainForManager(from, n.DeviceID, n.SessionID)}
		o := h.notifyDoneUnder(from, "assistant", n.DeviceID, n.Subtitle, n.SessionID, n.Title, n.Body, pushWants{}, managerPushCollapseID(from, n.ID),
			pushRecordingID(n.RecordingID), policy)
		out = &o
	case assistantwire.NotifyDone:
		o := h.notifyDoneUnder(from, "done", n.DeviceID, n.Subtitle, n.SessionID, n.Title, n.Body,
			pushWants{Kind: "done", Took: time.Duration(n.TookMS) * time.Millisecond, Long: time.Duration(n.LongMS) * time.Millisecond},
			"", "", pushPolicy{kind: assistantwire.NotifyDone, level: levelFor(n.Kind, n.Level), relatedKey: n.RelatedKey, todoID: relatedTodoID(n.RelatedKey, false),
				done: true, track: n.Track})
		out = &o
	case assistantwire.NotifyReply:
		if held := h.notifyReplyKeyed(from, n.DeviceID, n.Subtitle, n.SessionID, n.Title, n.Body, n.RelatedKey); held != "" {
			out = &pushOutcome{held: held}
		}
	case assistantwire.NotifyDecide:
		if n.Todo == nil || n.Todo.ID == "" || n.Todo.AskedAt.IsZero() || n.Todo.Recommend == "" {
			return nil, errors.New("a decision's push needs its to-do, when it was asked and the option its OK takes")
		}
		if held := h.notifyDecideKeyed(from, n.DeviceID, n.Subtitle, n.SessionID, n.Title, n.Body, *n.Todo, n.RelatedKey); held != "" {
			out = &pushOutcome{held: held}
		}
	case assistantwire.NotifyHealthWater:
		return h.notifyHealthWater(from, n)
	default:
		return nil, fmt.Errorf("unknown notification kind %q", n.Kind)
	}
	return out, nil
}
