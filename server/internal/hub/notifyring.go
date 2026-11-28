package hub

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The notify events kept for a device with no push route when no ws_notify socket accepts them (#4043, #10394). APNs keeps an
// iPhone's alert until the phone is back, but an Android phone or a browser hears of a question or a finished turn only
// on its WebSocket, and notifyWSFrom sends to live sockets alone: a device that was away at that moment (a network
// change, Doze, a frozen app) never got the notification, only the card in its lists. So the hub keeps, per such device,
// the events notifyWSFrom selected for it, its settings (manager mutes, Phone Muted, quiet hours, "Finished") decided
// at that moment exactly as for a live socket, and sends them after the client_hello of its next connection, skipping
// the cards resolved meanwhile. Clients collapse them with their usual notification tags. In memory only: a hub restart
// loses them, as it loses the sockets (docs/protocol/protocol.md "Notifications over the persistent connection (ws_notify)").
const (
	notifyRingMax     = 50               // events kept per device; the oldest goes first
	notifyRingMaxAge  = 30 * time.Minute // an older one is news no more: its card is in the lists
	notifyRingDevices = 32               // devices remembered; the one whose last client_hello is oldest goes first
)

// notifyRings are the devices whose last client_hello declared ws_notify, by device credential, with what is kept for
// each; guarded by Hub.mu, the lock notifyWSFrom walks the sockets under, so an event is either accepted by a socket
// that declared ws_notify or kept for the hello that takes the ring.
type notifyRings struct {
	devices map[string]*notifyRing
	now     func() time.Time // tests; nil is time.Now
}

// notifyRing is one device: what its last client_hello said (the language its events are written in, its
// capabilities), and the events kept for it, oldest first.
type notifyRing struct {
	lang     i18n.Lang
	features map[string]bool
	seen     time.Time // its last client_hello
	events   []ringedNotify
}

type ringedNotify struct {
	at         time.Time
	card       string // the interaction it is about ("": none), skipped once that card is resolved
	occurrence string // its exact created_at; empty from an older payload keeps ID-only replay
	msg        []byte // the notify event as it would have gone to the socket, quiet hours applied
}

func (r *notifyRings) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// remember records device's client_hello and returns its ring, making room for it among notifyRingDevices.
func (r *notifyRings) remember(device string, lang i18n.Lang, features map[string]bool, now time.Time) *notifyRing {
	d := r.devices[device]
	if d == nil {
		if r.devices == nil {
			r.devices = map[string]*notifyRing{}
		}
		for len(r.devices) >= notifyRingDevices {
			delete(r.devices, r.oldest())
		}
		d = &notifyRing{}
		r.devices[device] = d
	}
	d.lang, d.features, d.seen = lang, features, now
	return d
}

// oldest is the device whose last client_hello is the oldest.
func (r *notifyRings) oldest() string {
	var out string
	for id, d := range r.devices {
		if out == "" || d.seen.Before(r.devices[out].seen) {
			out = id
		}
	}
	return out
}

// add keeps e, dropping what is past notifyRingMaxAge and the oldest beyond notifyRingMax.
func (d *notifyRing) add(e ringedNotify) {
	d.events = append(dropStale(d.events, e.at), e)
	if over := len(d.events) - notifyRingMax; over > 0 {
		d.events = slices.Delete(d.events, 0, over)
	}
}

// dropStale drops the events past notifyRingMaxAge at now (they are kept oldest first).
func dropStale(events []ringedNotify, now time.Time) []ringedNotify {
	i := 0
	for i < len(events) && now.Sub(events[i].at) >= notifyRingMaxAge {
		i++
	}
	return slices.Delete(events, 0, i)
}

// ringable: a connection whose credential names one device. The hub's main token names none, and a pairing token is
// traded for a device credential within seconds.
func ringable(device string) bool {
	return device != "" && !strings.HasPrefix(device, "pair_")
}

// replayNotifyRing runs after c's client_hello: it remembers what c declared and sends it what was kept for its device
// while it had no ws_notify socket, in order, except the cards resolved since and what is past notifyRingMaxAge. A
// hello without ws_notify forgets the device. The settings were applied when each event was selected; they are not
// applied again.
func (h *Hub) replayNotifyRing(c *client) {
	if !ringable(c.device) {
		return
	}
	c.mu.Lock()
	features := maps.Clone(c.meta.features)
	c.mu.Unlock()
	lang := c.lang()
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &h.rings
	if !features["ws_notify"] {
		delete(r.devices, c.device)
		return
	}
	now := r.clock()
	d := r.remember(c.device, lang, features, now)
	kept := dropStale(d.events, now)
	d.events = nil
	for _, e := range kept {
		if e.card != "" {
			ix, open := h.pendingInteraction(e.card)
			if !open {
				continue
			}
			if e.occurrence != "" {
				at, err := time.Parse(time.RFC3339Nano, e.occurrence)
				if err != nil || !ix.CreatedAt.Equal(at) {
					continue // a retained alert must never acquire a same-ID replacement's buttons
				}
			}
		}
		c.queue(e.msg)
	}
}

// ringNotify keeps a notify event for remembered devices with no accepting ws_notify socket (live), the required
// capability and no push route, whose settings take it (wantsOn, the same decision a socket gets). msgIn
// is the event in a language, encoded the event quiet or not. Called under h.mu by notifyWSFrom. It returns how many
// devices kept it.
func (h *Hub) ringNotify(live map[string]bool, required []string, wantsOn func(device string) (ok, quiet bool),
	msgIn func(i18n.Lang) map[string]any, encoded func(l i18n.Lang, quiet bool) []byte) (kept int) {
	r := &h.rings
	if len(r.devices) == 0 {
		return 0
	}
	now := r.clock()
	for device, d := range r.devices {
		if live[device] || len(required) > 0 && !d.features[required[0]] || h.push.routed(device) {
			continue
		}
		ok, quiet := wantsOn(device)
		if !ok {
			continue
		}
		msg := encoded(d.lang, quiet)
		if msg == nil {
			continue
		}
		payload := msgIn(d.lang)
		card, _ := payload["interaction_id"].(string)
		occurrence, _ := payload["interaction_created_at"].(string)
		d.add(ringedNotify{at: now, card: card, occurrence: occurrence, msg: msg})
		kept++
	}
	return kept
}
