package hub

import (
	"cmp"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// Desktop apps (the Mac app) on the hub, like WeChat's "WeChat for Mac is logged in": a desktop registers as one (register_desktop)
// instead of as the phone, so the phone keeps the phone_* tools and its wake pushes; every client hears which
// desktops are connected (the desktops event, docs/protocol/protocol.md), and a phone that turned on "Phone Muted"
// (NotifyPrefs.MuteWhileDesktop) gets no alert pushes while one is.
//
// A computer whose Persona Link says the user is at it (its manager's devices.desktops, docs/operations/link.md
// "Presence") counts as a desktop too, for the one alert Persona Link shows: "Phone Muted" holds back only the "Needs
// you" cards of that computer's manager while only such desktops are there (notifyInteraction): its Persona Link shows
// that manager's summary, never another's. The desktops event lists them only to clients that asked for
// featureDesktopsLink.

// featureDesktopsLink: a client that lists it (client_hello's features or ?features=) hears of Persona Link desktops in
// the desktops event too.
const featureDesktopsLink = "desktops_link"

// featureDesktopUnregister says this hub withdraws a connection's desktop presence without closing its app socket.
const featureDesktopUnregister = "desktop_unregister"

// desktopView is one connected desktop as the apps see it.
type desktopView struct {
	// Device is its device token's id (what "Sign out" revokes); empty when it connected with the pairing token, and
	// for a Persona Link desktop.
	Device string    `json:"device"`
	Name   string    `json:"name"`
	Since  time.Time `json:"since"`
	// Kind is "link" for a computer whose Persona Link says the user is at it: MachineID is that computer, Name what the
	// hub calls it and Since when the user was first there this time. Left out for the desktop app.
	Kind      string `json:"kind,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
}

// linkDesktop is a computer whose Persona Link says the user is at it until until, as its manager (owner) last
// reported.
type linkDesktop struct {
	owner, machine, name string
	since, until         time.Time
}

// linkDesktops are the Persona Link desktops (h.mu): each manager's as it last said, all of them by manager; the ones
// the clients that asked were last told of (shown); and the timer that takes the next one out when it lapses.
type linkDesktops struct {
	list   []linkDesktop
	shown  []linkDesktop
	expiry *time.Timer
}

func init() {
	handleBusEvent(devicewire.EventDesktops, func(h *Hub, c *busConn, params json.RawMessage) {
		var d devicewire.Desktops
		if err := json.Unmarshal(params, &d); err != nil {
			h.logf("bus: [%s] %s: %v", c.ident.ID, devicewire.EventDesktops, err)
			return
		}
		h.takeLinkDesktops(c.managerID(), d.Desktops)
	})
}

// desktops lists the connected desktops, one per device (its newest connection: a Mac reconnecting keeps the old
// socket for a while), earliest first; withLink, the Persona Link desktops too.
func (h *Hub) desktops(withLink bool) []desktopView {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.desktopsLocked(withLink)
}

func (h *Hub) desktopsLocked(withLink bool) []desktopView {
	byDevice := map[string]desktopView{}
	var loose []desktopView // the pairing token: no device to tell them apart
	for c := range h.clients {
		if !c.desktop {
			continue
		}
		v := desktopView{Device: c.device, Name: c.name, Since: c.since}
		if c.device == "" {
			loose = append(loose, v)
		} else if cur, ok := byDevice[c.device]; !ok || v.Since.After(cur.Since) {
			byDevice[c.device] = v
		}
	}
	out := loose
	for _, v := range byDevice {
		out = append(out, v)
	}
	if withLink {
		for _, d := range h.liveLinkDesktopsLocked(time.Now()) {
			out = append(out, desktopView{Name: d.name, Since: d.since, Kind: "link", MachineID: d.machine})
		}
	}
	if out == nil {
		out = []desktopView{}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].MachineID < out[j].MachineID
	})
	return out
}

// desktopsMessageLocked is the desktops event as it is now. Callers hold h.mu and queue it before letting go, so
// every client gets the lists in the order they changed (computed and queued apart, an older list could land last).
func (h *Hub) desktopsMessageLocked(withLink bool) []byte {
	b, _ := json.Marshal(map[string]any{"type": "desktops", "desktops": h.desktopsLocked(withLink)})
	return b
}

// sendDesktops queues the current list to one client (it has just connected, or asked for featureDesktopsLink).
func (h *Hub) sendDesktops(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	queue(c, h.desktopsMessageLocked(c.wants(featureDesktopsLink)))
}

// announceDesktopsLocked queues the current list to every client, with the Persona Link desktops to those that asked;
// linkOnly, to those only (only theirs changed). h.mu is held.
func (h *Hub) announceDesktopsLocked(linkOnly bool) {
	h.linkDesktops.shown = h.liveLinkDesktopsLocked(time.Now())
	var lists [2][]byte
	for c := range h.clients {
		withLink := c.wants(featureDesktopsLink)
		if linkOnly && !withLink {
			continue
		}
		i := 0
		if withLink {
			i = 1
		}
		if lists[i] == nil {
			lists[i] = h.desktopsMessageLocked(withLink)
		}
		queue(c, lists[i])
	}
}

// takeLinkDesktops is manager owner's devices.desktops: its computers whose Persona Link says the user is at them. Only
// its own computers count, as the hub has them (named as the hub calls them), with notifications showing; a mesh
// member's computer does not count yet. Each counts for the TTL it says from now, on the hub's own clock and at most
// DesktopTTLMax: a manager's clock, ahead or behind, never stretches it.
func (h *Hub) takeLinkDesktops(owner string, ds []devicewire.Desktop) {
	now := time.Now()
	var mine []linkDesktop
	for _, d := range ds {
		m := h.machineByID(d.MachineID) // a hub.json entry another one absorbed names that one's computer
		if m == nil || !d.Notifies || d.TTLMS <= 0 {
			continue
		}
		m.mu.Lock()
		ours := m.managed && m.owner == owner && !m.derived
		m.mu.Unlock()
		if ours {
			ttl := time.Duration(min(d.TTLMS, devicewire.DesktopTTLMax.Milliseconds())) * time.Millisecond
			mine = append(mine, linkDesktop{owner: owner, machine: m.cfg.ID, name: m.name(), since: d.Since, until: now.Add(ttl)})
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// The other managers' as they said them, but those that lapsed: those the timer has not taken out yet are no change.
	all := slices.DeleteFunc(slices.Clone(h.linkDesktops.list), func(d linkDesktop) bool {
		return d.owner == owner || !d.until.After(now)
	})
	all = append(all, mine...)
	slices.SortStableFunc(all, func(a, b linkDesktop) int { return cmp.Compare(a.owner, b.owner) })
	h.setLinkDesktopsLocked(all, now)
}

// setLinkDesktopsLocked makes list the Persona Link desktops. A change from what the clients were last told of is
// announced (one that lapsed meanwhile included, whether or not the timer came first); a later deadline alone is not.
// h.mu is held.
func (h *Hub) setLinkDesktopsLocked(list []linkDesktop, now time.Time) {
	h.linkDesktops.list = list
	h.announceLinkChangeLocked(now)
	h.armLinkExpiryLocked(now)
}

// announceLinkChangeLocked tells the clients that asked of the Persona Link desktops when they are not the ones they
// were last told of. h.mu is held.
func (h *Hub) announceLinkChangeLocked(now time.Time) {
	same := slices.EqualFunc(h.linkDesktops.shown, h.liveLinkDesktopsLocked(now), func(a, b linkDesktop) bool {
		return a.machine == b.machine && a.name == b.name && a.since.Equal(b.since)
	})
	if !same {
		h.announceDesktopsLocked(true)
	}
}

// liveLinkDesktopsLocked are the Persona Link desktops that have not lapsed at now; h.mu is held.
func (h *Hub) liveLinkDesktopsLocked(now time.Time) []linkDesktop {
	var live []linkDesktop
	for _, d := range h.linkDesktops.list {
		if d.until.After(now) {
			live = append(live, d)
		}
	}
	return live
}

// armLinkExpiryLocked sets the timer for the next Persona Link desktop to lapse: its manager may be gone and never say
// so, and the apps should hear it all the same. h.mu is held.
func (h *Hub) armLinkExpiryLocked(now time.Time) {
	if h.linkDesktops.expiry != nil {
		h.linkDesktops.expiry.Stop()
		h.linkDesktops.expiry = nil
	}
	var next time.Time
	for _, d := range h.linkDesktops.list {
		if next.IsZero() || d.until.Before(next) {
			next = d.until
		}
	}
	if !next.IsZero() {
		h.linkDesktops.expiry = time.AfterFunc(next.Sub(now), h.expireLinkDesktops)
	}
}

// expireLinkDesktops drops the Persona Link desktops that lapsed, and says so.
func (h *Hub) expireLinkDesktops() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.linkDesktops.list = h.liveLinkDesktopsLocked(now)
	h.announceLinkChangeLocked(now)
	h.armLinkExpiryLocked(now)
}

// queue drops the message for a client too slow to take it, like broadcast's events: it gets the list again with the
// next change or when it reconnects.
func queue(c *client, b []byte) {
	select {
	case c.out <- b:
	default:
	}
}

// linkDesktopOnline: some computer of manager owner's has its Persona Link say the user is at it ("Phone Muted" holds
// back that manager's "Needs you" cards, the ones that Persona Link shows). Read when asked, so one whose manager went
// quiet stops counting when it lapses.
func (h *Hub) linkDesktopOnline(owner string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	return slices.ContainsFunc(h.linkDesktops.list, func(d linkDesktop) bool { return d.owner == owner && d.until.After(now) })
}

// desktopOnline: some desktop app is connected ("Phone Muted" holds the phone's pushes back).
func (h *Hub) desktopOnline() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.desktop {
			return true
		}
	}
	return false
}

// desktopOpen: the desktop app with this push id is connected. It posts its own notifications for agents that turn
// needs-you or finish (the Mac's MacAttentionWatcher, on the same Settings › Notifications), so those pushes would show twice.
func (h *Hub) desktopOpen(device string) bool {
	if device == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.desktop && c.pushDevice == device {
			return true
		}
	}
	return false
}

// registerDesktop marks c as a desktop app and tells every client. Not for an answer-only device (a watch): it would
// mute the phone. pushDevice is its id for pushes (PushRegistrar.deviceID; empty from an older app): while it is
// connected the hub leaves the agents' alerts to it (desktopOpen).
func (h *Hub) registerDesktop(c *client, name, pushDevice string) {
	if c.role != roleFull {
		return
	}
	h.mu.Lock()
	c.desktop, c.name, c.since, c.pushDevice = true, name, time.Now(), pushDevice
	h.announceDesktopsLocked(false)
	h.mu.Unlock()
	go h.fleetClientsChanged() // Phone Muted holds back a phone's MCP form pushes now (fleet_link.go)
	h.logf("desktop %q connected (device %s)", name, c.device)
}

// unregisterDesktop withdraws only this connection's desktop presence. The web app keeps its socket and other work.
// Older hubs ignore the unknown message; the existing socket-close path remains the compatibility fallback.
func (h *Hub) unregisterDesktop(c *client) {
	if c.role != roleFull {
		return
	}
	h.mu.Lock()
	if !c.desktop && c.pushDevice == "" {
		h.mu.Unlock()
		return
	}
	name := c.name
	c.desktop, c.pushDevice = false, ""
	h.announceDesktopsLocked(false)
	h.mu.Unlock()
	go h.fleetClientsChanged()
	h.logf("desktop %q unregistered (device %s)", name, c.device)
}

// dropClient forgets a client whose connection ended (also as one the shutdown has to close); a desktop's going is
// announced.
func (h *Hub) dropClient(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	delete(h.wsConnections, c)
	desktop := c.desktop
	if desktop {
		h.announceDesktopsLocked(false)
	}
	phone, device := c.phone, c.pushDevice
	h.mu.Unlock()
	h.fsWatchRelease(c) // the folders its panels had open are no longer watched (fswatch.go)
	go h.fleetClientsChanged()
	if desktop {
		h.logf("desktop %q disconnected (device %s)", c.name, c.device)
	}
	if phone && !h.phoneOpen(device) {
		h.live.phoneLeft(device, c.clean)
	}
}

// phoneOpen: the app on the phone with this push id is open and connected. An app closes its connection when it goes
// to the background, so it is open while connected (a suspended one with the socket left open is dropped by the
// pings within ~35 s).
func (h *Hub) phoneOpen(device string) bool {
	if device == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.phone && c.pushDevice == device {
			return true
		}
	}
	return false
}

// alertWanted is which devices an alert push goes to: what their settings pick (wants; nil: every device, for what
// was asked for explicitly), less those muted while a desktop is online.
func alertWanted(wants func(NotifyPrefs) bool, desktopOnline bool) func(NotifyPrefs) bool {
	return func(p NotifyPrefs) bool {
		if wants == nil {
			return true
		}
		return wants(p) && !(desktopOnline && p.MuteWhileDesktop)
	}
}
