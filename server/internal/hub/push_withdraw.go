package hub

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Taking a card's push back once the card is over (#4349): answered on the computer, the web or the watch, withdrawn
// by the agent or expired. A suspended app never gets `interaction_resolved`, so its approval and question pushes
// stayed on the lock screen until it was opened (#3513 is the app's side). The hub sends the devices the card was
// pushed to a silent background push naming the cards that are over, and the app removes their notifications.
//
// iOS gives background pushes a small budget: cards over close together go in one push, a device gets at most one a
// minute, and only devices that said they understand it (`features: ["withdraw"]` at registration) get any.

// featureWithdraw is what a device registers with when it removes the notifications a withdrawal push names.
const featureWithdraw = "withdraw"

const (
	// withdrawKept: how long the hub remembers where a card's push went. A card still pending after this is old
	// news on the lock screen, and the app clears it when it next lists the cards.
	withdrawKept = 30 * time.Minute
	// ownerCardKept: as withdrawKept for one of the assistant's own cards (a plan waiting for Start, a decision, a
	// needs-user ask, a failed start: Notification.CardID / TodoAskCardID). The app never lists those as pending
	// interactions, so nothing else clears their push: it stays on the lock screen until the card is over, however
	// late, and the hub remembers it as long.
	ownerCardKept = 7 * 24 * time.Hour
	// withdrawGather: cards over within this of the first go in one push ("always allow" settles several at once).
	withdrawGather = 5 * time.Second
	// withdrawEvery: at most one withdrawal push per device this often; what comes between waits for the next.
	withdrawEvery = time.Minute
	// withdrawMost bounds the ids in one push (APNs takes 4 KB); the rest go with the next.
	withdrawMost = 40
	// withdrawIDBytes bounds the encoded ids in one push too: runtime ids may be 128 characters, and forty of those
	// are more than a sealed push carries (relayMaxPlain, under APNs' 4 KB). The rest leaves room for aps, hub_id and iat.
	withdrawIDBytes = relayMaxPlain - 256
)

type withdrawals struct {
	mu sync.Mutex
	// pushed: per card id, the devices its push went to and when.
	pushed map[string]cardPush
	// queue: per device id, the cards over that it has not been told about yet.
	queue map[string]*withdrawQueue
	// gather and every are withdrawGather and withdrawEvery; tests shorten them.
	gather, every time.Duration
}

type cardPush struct {
	devices map[string]bool
	at      time.Time
	keep    time.Duration // withdrawKept, or ownerCardKept for an assistant card's push
}

// kept: whether the hub still remembers where it went at now.
func (c cardPush) kept(now time.Time) bool { return now.Sub(c.at) <= cmp.Or(c.keep, withdrawKept) }

type withdrawQueue struct {
	cards []withdrawCard
	timer *time.Timer
	last  time.Time // when the last withdrawal push went
}

// withdrawCard keeps the legacy id list and the occurrence-aware entry in lockstep.
type withdrawCard struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at,omitempty"`
}

func newWithdrawals() withdrawals {
	return withdrawals{pushed: map[string]cardPush{}, queue: map[string]*withdrawQueue{}, gather: withdrawGather, every: withdrawEvery}
}

// sendCard pushes manager's card id (sendLocalizedQuiet) under cardCollapseID and notes which devices that understand
// withdrawals it went to. only, when not nil, is the devices that can take it at all (an MCP form or link: those that
// registered for one).
func (p *Pusher) sendCard(manager string, policy pushPolicy, build localized, id string, wants func(NotifyPrefs) bool, rule quietRule, only func(Device) bool) string {
	if p == nil {
		return ""
	}
	policy.manager = manager
	if policy.level != assistantwire.LevelP0 || p.filteringFor(manager) {
		// Extract before applyPushGrading's mode check too: enabling push grading between checks must not hold a source-less card.
		// An immersive block, owner base config, or extension grading layer may inspect the session in focus, which it knows by the source.
		policy = pushPolicyWithSource(policy, build(i18n.Default))
	}
	if allow, reason := p.applyPushGrading(policy); !allow {
		return reason
	}
	rule = p.hushRule(rule) // quiet mode: without a sound on every device (push_attention.go)
	p.sendFromTo(manager, build, cardCollapseID(manager, id), wants, rule, only, func(d Device) {
		if hasFeature(d, featureWithdraw) {
			p.cardPushedTo(id, d.DeviceID)
		}
	})
	return ""
}

// cardCollapseID is the apns-collapse-id of manager's card id: the card id for the assistant the hub starts (and a card
// of no manager) when it fits APNs' 64 bytes. Another manager's card, and a longer id, gets card: and a hash of the
// manager and the id, so its push replaces only that manager's push of the same card, never another manager's card, a
// decision's (td_), a notify API alert, an extension's push or the hub's own, and APNs takes it. The apps find a card's
// notification by the payload's interaction_id (withdrawal, a tap), not by this id, and the withdrawal push names the
// card id.
func cardCollapseID(manager, id string) string {
	manager = cmp.Or(manager, assistantwire.AssistantID)
	if manager == assistantwire.AssistantID && len(id) <= apnsCollapseMax {
		return id
	}
	sum := sha256.New()
	sum.Write([]byte("card\x00"))
	sum.Write(binary.AppendUvarint(nil, uint64(len(manager))))
	sum.Write([]byte(manager))
	sum.Write([]byte(id))
	return "card:" + hex.EncodeToString(sum.Sum(nil)[:16])
}

// apnsCollapseMax is the longest apns-collapse-id APNs takes, in bytes.
const apnsCollapseMax = 64

func (p *Pusher) cardPushedTo(id, device string) { p.cardPushedFor(id, device, withdrawKept) }

// ownerCardPushedTo is cardPushedTo for one of the assistant's own cards (Notification.CardID / TodoAskCardID):
// remembered for ownerCardKept.
func (p *Pusher) ownerCardPushedTo(id, device string) { p.cardPushedFor(id, device, ownerCardKept) }

func (p *Pusher) cardPushedFor(id, device string, keep time.Duration) {
	w := &p.withdrawals
	now := p.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for old, c := range w.pushed {
		if !c.kept(now) {
			delete(w.pushed, old)
		}
	}
	if q := w.queue[device]; q != nil && len(q.cards) > 0 {
		// A fresh push for the same id (e.g. a superseded to-do decision's new round) supersedes any queued withdrawal
		// from the earlier round so the delayed background push cannot wipe the new alert.
		q.cards = slices.DeleteFunc(q.cards, func(queued withdrawCard) bool { return queued.ID == id })
	}
	c, ok := w.pushed[id]
	if !ok {
		c = cardPush{devices: map[string]bool{}}
	}
	c.devices[device] = true
	c.at, c.keep = now, max(c.keep, keep)
	w.pushed[id] = c
}

// withdraw: the card is over. The devices its push went to are told, unless it went to none (a muted session, a
// quiet task, "Needs you" off) or so long ago that the hub forgot. A known occurrence is carried separately from the
// legacy ID list so a delayed withdrawal cannot take back a same-ID replacement's alert.
func (p *Pusher) withdraw(id string, occurrence ...time.Time) {
	if p == nil {
		return
	}
	w := &p.withdrawals
	now := p.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.pushed[id]
	delete(w.pushed, id)
	if !ok || !c.kept(now) {
		return
	}
	for device := range c.devices {
		q := w.queue[device]
		if q == nil {
			q = &withdrawQueue{}
			w.queue[device] = q
		}
		card := withdrawCard{ID: id}
		if len(occurrence) > 0 && !occurrence[0].IsZero() {
			card.CreatedAt = occurrence[0].UTC().Format(time.RFC3339Nano)
		}
		q.cards = append(q.cards, card)
		p.scheduleWithdrawLocked(device, q, now)
	}
}

// scheduleWithdrawLocked sends q after the gathering wait, or once the device's minute is up if that is later.
func (p *Pusher) scheduleWithdrawLocked(device string, q *withdrawQueue, now time.Time) {
	if q.timer != nil || len(q.cards) == 0 {
		return
	}
	w := &p.withdrawals
	wait := w.gather
	if next := q.last.Add(w.every).Sub(now); next > wait {
		wait = next
	}
	q.timer = time.AfterFunc(wait, func() { p.flushWithdraw(device) })
}

func (p *Pusher) flushWithdraw(device string) {
	w := &p.withdrawals
	w.mu.Lock()
	q := w.queue[device]
	if q == nil {
		w.mu.Unlock()
		return
	}
	q.timer = nil
	cards := q.cards
	if n := withdrawBatch(cards); n < len(cards) {
		cards, q.cards = cards[:n:n], cards[n:]
	} else {
		q.cards = nil
	}
	q.last = p.now()
	p.scheduleWithdrawLocked(device, q, q.last) // what did not fit
	w.mu.Unlock()
	if len(cards) == 0 {
		return
	}
	ids := make([]string, len(cards))
	for i, card := range cards {
		ids[i] = card.ID
	}
	p.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}, "resolved_interaction_ids": ids,
		"resolved_interactions": cards}, "", "background", "5",
		func(d Device) bool { return d.DeviceID == device && hasFeature(d, featureWithdraw) })
}

// withdrawBatch is how many cards, from the first, go in one push: at most withdrawMost, and only as many as fit
// withdrawIDBytes with both the legacy IDs and occurrence entries encoded. Always at least one, so the queue moves.
func withdrawBatch(cards []withdrawCard) int {
	size := 0
	for i, card := range cards {
		id, _ := json.Marshal(card.ID)
		entry, _ := json.Marshal(card)
		size += len(id) + len(entry) + 2 // and their commas
		if i == withdrawMost || i > 0 && size > withdrawIDBytes {
			return i
		}
	}
	return len(cards)
}

func hasFeature(d Device, feature string) bool { return slices.Contains(d.Features, feature) }

// knownFeatures are the features a device may register with; anything else is dropped.
var knownFeatures = map[string]bool{featureWithdraw: true, featureElicitation: true, compat.Feature: true}

func cleanFeatures(in []string) []string {
	var out []string
	for _, f := range in {
		if knownFeatures[f] && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}
