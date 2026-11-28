package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The "Fleet" Live Activity (docs/apple/live-activity.md): one activity per phone, on the lock screen and in the Dynamic
// Island, summing up every agent that needs you, is working, or finished unseen. The hub computes the state
// (FleetState, the activity's ContentState) from what it already knows and pushes it over APNs (push type
// liveactivity) to each device's activity token; push-to-start opens one when the app is not running.
//
// Budget: Apple throttles activity updates (numbers unpublished), and each one redraws the island and the lock screen
// on the phone. So what needs the user (a request arriving, or answered) goes out at once; anything else is merged
// for fleetCoalesce and reaches a device at most every fleetMinGap; the timer (working since…) runs on the phone; a
// phone whose app is open draws the activity itself from the fleet events and gets no push; and nothing is sent
// while nothing changes except a refresh before the content goes stale (docs/apple/live-activity.md "Push cadence").

const (
	fleetCoalesce = 2500 * time.Millisecond
	// fleetMinGap: an ordinary change (counts, titles, the assistant, an agent done) reaches a device at most this
	// often, so a device's activity is at most fleetMinGap + fleetCoalesce behind the hub (TestFleetPacingBound).
	// On the user's hub (2026-11-02) the fleet changed ~180 times an hour, each one a push.
	fleetMinGap  = 30 * time.Second
	fleetStaleIn = 15 * time.Minute // stale-date: buttons disappear if the hub goes quiet this long
	fleetRefresh = 10 * time.Minute // resend unchanged content to a device this long after its last push
	// fleetOpenFresh: how long after the last fleet event an app that went to the background is sure its activity is
	// not stale. An open app only updates its activity when what it shows changes or it is within 5 minutes of its
	// stale-date (LiveActivityController.worthUpdating), so 5 minutes is all it is sure of; a minute less for the
	// tick and the push to arrive.
	fleetOpenFresh  = 4 * time.Minute
	fleetEndAfter   = time.Minute     // empty this long: end the activity
	fleetDoneLinger = 5 * time.Minute // only finished agents left: keep showing them this long
	fleetMaxItems   = 3
	fleetMaxAge     = 8 * time.Hour // the system ends an activity after about this long; past it, start a new one
	// fleetRetireBefore: an activity this close to fleetMaxAge is ended by the hub, while its token still reaches it, so the
	// one that replaces it never sits beside a card iOS ended but left on the Lock Screen (#12982).
	fleetRetireBefore = 20 * time.Minute
)

// forDevice is what one device gets: its settings and persona, and of the assistant and the numbers only what it
// shows (so a change it does not show costs no push).
func forDevice(s FleetState, p NotifyPrefs, stats FleetStats) FleetState {
	d := s
	d.Details = p.LockDetails
	d.Look = p.Look
	d.Left, d.Right = p.Island.sides()
	if !p.LockDetails {
		// Without names on the Lock Screen a request reads "A permission request is waiting": nobody can tell who asks
		// or what approving does, so it gets no Allow / Deny there, only a tap into the app (#12982).
		d.Items = slices.Clone(s.Items)
		for i := range d.Items {
			if k := d.Items[i].Kind; k == "permission" || k == "plan" { // the kinds with buttons; others keep naming their card
				d.Items[i].Interaction = ""
				d.Items[i].InteractionCreatedAt = ""
			}
		}
	}
	shows := func(slot string) bool { return d.Left == slot || d.Right == slot }
	if !shows(SlotAssistant) {
		d.Assistant = false
	}
	var ds FleetStats
	if shows(SlotTPM) {
		ds.TPM = stats.TPM
	}
	if shows(SlotTokens) {
		ds.Tokens = stats.Tokens
	}
	if shows(SlotRequests) {
		ds.Requests = stats.Requests
	}
	if shows(SlotLoad) {
		ds.Loads = pickLoad(stats.Loads, p.Island.Machine)
	}
	if ds.TPM != 0 || ds.Tokens != 0 || ds.Requests != 0 || ds.Loads != nil {
		d.Stats = &ds
	} else {
		d.Stats = nil
	}
	return d
}

// pickLoad: the chosen machine, or else the busiest.
func pickLoad(loads []FleetLoad, machine string) []FleetLoad {
	var best *FleetLoad
	for i := range loads {
		if loads[i].ID == machine {
			return []FleetLoad{loads[i]}
		}
		if best == nil || loads[i].Percent > best.Percent {
			best = &loads[i]
		}
	}
	if best == nil {
		return nil
	}
	return []FleetLoad{*best}
}

func fleetEmpty(s FleetState) bool { return s.Phase == "" }

// showsNothing: a device's version of the fleet (forDevice) has nothing to draw: no agent, and the assistant not
// answering or not on its island. Only the assistant answering is something to show where the island shows it
// ("Thinking", #4398).
func showsNothing(dst FleetState) bool { return fleetEmpty(dst) && !dst.Assistant }

// showsAssistant: the device has the activity on, with a side of its island for the assistant answering.
func showsAssistant(p NotifyPrefs) bool {
	left, right := p.Island.sides()
	return p.LiveActivity && (left == SlotAssistant || right == SlotAssistant)
}

// needsYouIDs: the items waiting for the user, to tell a new one (push now) from anything else (merge). Since (the
// card's creation) tells two cards of one agent apart when neither names its interaction (a card with fields).
func needsYouIDs(s FleetState) map[string]bool {
	out := map[string]bool{}
	for _, it := range s.Items {
		if it.Kind != "working" && it.Kind != "unread" {
			out[it.ID+"#"+it.Interaction+"#"+strconv.FormatFloat(it.Since, 'f', -1, 64)] = true
		}
	}
	return out
}

func fleetRelevance(s FleetState) float64 {
	switch s.Phase {
	case "needsYou":
		return 1
	case "working":
		return 0.5
	}
	return 0.2
}

// coalescer decides when a new fleet state goes out: a change in what needs the user (a request arriving, or answered
// or gone) at once, anything else after fleetCoalesce (more changes in the meantime ride along).
type coalescer struct {
	sent     []byte          // last state pushed
	needs    map[string]bool // its needs-you items
	needsYou int             // its NeedsYou: past fleetMaxItems the items stay the same three while the count moves
	pending  bool
}

// offer returns whether to push now, or else whether a delayed flush should be scheduled.
func (c *coalescer) offer(st FleetState) (now, schedule bool) {
	b, _ := json.Marshal(st)
	if bytes.Equal(b, c.sent) {
		c.pending = false
		return false, false
	}
	if needs := needsYouIDs(st); !maps.Equal(needs, c.needs) && (len(needs) > 0 || len(c.needs) > 0) {
		return true, false
	}
	if st.NeedsYou != c.needsYou { // a request beyond the items arrived or was answered (#4604)
		return true, false
	}
	if c.pending {
		return false, false
	}
	c.pending = true
	return false, true
}

func (c *coalescer) sentState(st FleetState) {
	c.sent, _ = json.Marshal(st)
	c.needs = needsYouIDs(st)
	c.needsYou = st.NeedsYou
	c.pending = false
}

// ---- the manager ----

type liveActivities struct {
	h      *Hub
	signal chan struct{}

	mu        sync.Mutex
	rows      map[string]db.LiveActivity // device -> its activity
	coal      coalescer
	shown     []byte          // last state broadcast to the open apps
	resend    map[string]bool // only devices whose activity settings changed
	last      FleetState
	emptyAt   time.Time // since when the fleet has been over (empty, or only finished agents for a while)
	doneSince time.Time
	// answeredAt: when the assistant stopped answering last (zero: not since the fleet started over). An activity that
	// shows it stays fleetEndAfter after that, as one does after the fleet empties (#4398).
	answeredAt time.Time
	episode    int                  // bumps each time the fleet becomes active after being over
	started    map[string]int       // device -> episode it was push-started in (once per episode)
	sent       map[string][]byte    // device -> the content-state it last got
	sentAt     map[string]time.Time // device -> when
	stats      []byte               // last numbers broadcast to the open apps
	// deferred: device -> the due time of the deferred start it last logged (claimStart), so a start held for hours is
	// one line, not one a round. Only that: when a start may be claimed is the store's to say, every round, since the
	// other slot of a blue-green switch moves it too (TestFleetHoldLiftedOnTheOtherSlot).
	deferred map[string]time.Time
	// The last fleet broadcast to the open apps, and when: what an app that closed its connection cleanly (gone to
	// the background) left its activity showing (phoneLeft).
	shownState    FleetState
	shownStats    FleetStats
	shownAt       time.Time
	shownIntake   uint64        // the last intake revision the open apps were sent (intakeSent)
	repair        bool          // a device's activity shows something unknown: flush without waiting for a change
	widgets       widgetWaker   // which phones' Home Screen widgets to wake, and when (widget_wake.go)
	widgetChanges chan struct{} // intake revisions wake widgets without offering Live Activity content
	// wake: when the run loop's timer goes off next (zero: not set), for a merge or a device's turn after fleetMinGap.
	wake         time.Time
	now          func() time.Time
	minGap       time.Duration // fleetMinGap (0 in the replay of how it was before, pushreplay_test.go)
	storeTimeout time.Duration // bounds each token write; tests shorten it
	// answers: the phones whose apps registered (register_phone), waiting for their holds to be lifted off the app's
	// read loop (phoneConnectedLater); answering while a goroutine drains them.
	answerMu  sync.Mutex
	answers   []phoneAnswer
	answering bool
	// quiescedStart, deferredHolds and deferredSaves hold writes refused while
	// a development snapshot pauses hub writers (quiesce.go). holdReplayMu
	// serializes deferred hold replay ahead of newer holds/lifts and start
	// claims, and saveMu serializes SaveLiveActivity per device so resumed
	// saves cannot overwrite newer state.
	holdReplayMu  sync.Mutex
	holdReplaying bool
	quiescedStart bool
	deferredHolds []deferredLiveHold
	deferredSaves map[string]bool
	rowGen        map[string]uint64
	saveMu        map[string]*sync.Mutex
	saveWaiters   map[string][]*liveSaveWaiter
}

type liveSaveWaiter struct {
	wantGen uint64
	done    bool
	err     error
}

type deferredLiveHold struct {
	device string
	asOf   time.Time
	hold   time.Duration
}

// phoneAnswer is one register_phone: the push id it named, its connection's credential, and when it arrived.
type phoneAnswer struct {
	pushID, credential string
	at                 time.Time
}

func newLiveActivities(h *Hub) *liveActivities {
	return &liveActivities{h: h, signal: make(chan struct{}, 1), widgetChanges: make(chan struct{}, 1), rows: map[string]db.LiveActivity{}, started: map[string]int{},
		deferred: map[string]time.Time{}, sent: map[string][]byte{}, sentAt: map[string]time.Time{}, now: time.Now, minGap: fleetMinGap, storeTimeout: deviceStoreTimeout}
}

func (la *liveActivities) load(ctx context.Context) {
	if la.h.store == nil {
		return
	}
	list, err := la.h.store.LiveActivities(ctx)
	if err != nil {
		la.h.logf("load live activities: %v", err)
		return
	}
	la.mu.Lock()
	for _, v := range list {
		la.rows[v.DeviceID] = v
	}
	la.mu.Unlock()
}

// changed is called from broadcast (with the hub lock held): never block.
func (la *liveActivities) changed() {
	if la == nil {
		return
	}
	select {
	case la.signal <- struct{}{}:
	default:
	}
}

// settingsChanged refreshes only what this device's activity actually displays.
// Notification and translation choices, and identical retries, need no activity push.
func (la *liveActivities) settingsChanged(device string, before, after NotifyPrefs) {
	st, stats := la.h.fleetLive()
	old, _ := json.Marshal(forDevice(st, before, stats))
	next, _ := json.Marshal(forDevice(st, after, stats))
	if before.LiveActivity == after.LiveActivity && bytes.Equal(old, next) {
		return
	}
	la.mu.Lock()
	if la.resend == nil {
		la.resend = map[string]bool{}
	}
	la.resend[device] = true
	if !before.LiveActivity && after.LiveActivity {
		// Re-enabled: its previous activity may have ended while pushes were off.
		delete(la.sent, device)
		delete(la.sentAt, device)
		delete(la.started, device)
	}
	la.mu.Unlock()
	la.changed()
}

// snapshot is the fleet as the manager worked it out last (fleet.live, fleet_link.go): it stays while the manager is
// away.
func (la *liveActivities) snapshot() FleetState {
	st, _ := la.h.fleetLive()
	return st
}

// The activity's content and numbers are the manager's (assistant/fleet_live.go): the hub gives each device its part
// and pushes it.
type (
	FleetState    = assistantwire.FleetState
	FleetLook     = assistantwire.FleetLook
	FleetStats    = assistantwire.FleetStats
	FleetLoad     = assistantwire.FleetLoad
	FleetItem     = assistantwire.FleetItem
	FleetProgress = assistantwire.FleetProgress
)

// fleetStats are the island's numbers as the manager told them last.
func (h *Hub) fleetStats() FleetStats {
	_, stats := h.fleetLive()
	return stats
}

func (la *liveActivities) run(ctx context.Context) {
	la.load(ctx)
	la.changed() // restart reconstructs any deferred start from the current fleet, not an old payload
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	timer.Stop()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-la.widgetChanges:
			la.wakeWidgets(la.snapshot())
		case <-la.signal:
			la.mu.Lock()
			resend, repair := la.resend, la.repair
			la.resend, la.repair = nil, false
			la.mu.Unlock()
			st := la.snapshot()
			if b, _ := json.Marshal(st); !bytes.Equal(b, la.shown) { // the open app updates its activity itself
				la.shown = b
				la.broadcast(st)
			}
			la.wakeWidgets(st)
			now, schedule := la.coal.offer(st)
			switch {
			case now:
				la.wakeAt(timer, la.flush(st, true, false))
			case schedule:
				la.wakeAt(timer, la.now().Add(fleetCoalesce))
			case repair:
				la.wakeAt(timer, la.flush(st, false, false))
			}
			if len(resend) > 0 {
				// Ordinary and urgent fleet changes keep their own delivery above.
				// A settings refresh bypasses the gap only for these devices; the
				// existing timer still flushes anything other devices have pending.
				la.wakeAt(timer, la.flush(st, false, false, slices.Sorted(maps.Keys(resend))...))
			}
		case <-timer.C:
			la.wake = time.Time{}
			la.wakeAt(timer, la.flush(la.snapshot(), false, false))
		case <-tick.C:
			la.tick()
		}
	}
}

// wakeAt sets the run loop's timer for t, unless it goes off sooner already (zero t: nothing to do).
func (la *liveActivities) wakeAt(timer *time.Timer, t time.Time) {
	if t.IsZero() || (!la.wake.IsZero() && !t.Before(la.wake)) {
		return
	}
	la.wake = t
	timer.Reset(max(t.Sub(la.now()), time.Millisecond))
}

// broadcast sends the open apps the fleet with every number (each app picks what its own settings show).
func (la *liveActivities) broadcast(st FleetState) {
	stats := la.h.fleetStats()
	la.mu.Lock()
	la.shownState, la.shownStats, la.shownAt = st, stats, la.now()
	la.mu.Unlock()
	st.Stats = &stats
	la.stats, _ = json.Marshal(stats)
	la.h.broadcast(map[string]any{"type": "fleet", "fleet": st})
}

// tick: end an activity that has been over for a while, refresh one about to go stale, update the numbers.
func (la *liveActivities) tick() {
	if la.h.isMember() { // the center's Live Activity is the group's (mesh_group.go)
		return
	}
	la.wakeWidgets(la.snapshot()) // a change held back for widgetGap, the heartbeat
	now := la.now()
	la.retire(now)
	la.mu.Lock()
	over := !la.emptyAt.IsZero() && now.Sub(la.emptyAt) >= fleetEndAfter
	doneLong := la.last.Phase == "done" && !la.doneSince.IsZero() && now.Sub(la.doneSince) >= fleetDoneLinger
	// The assistant answering, or done less than fleetEndAfter ago, holds the activities that show it (#4398).
	answering := la.last.Assistant || (!la.answeredAt.IsZero() && now.Sub(la.answeredAt) < fleetEndAfter)
	last := la.last
	la.mu.Unlock()
	switch {
	case (over || doneLong) && !answering:
		la.endAll()
		return
	case over || doneLong:
		la.end(showsAssistant) // the others have nothing left to show
	}
	if !fleetEmpty(last) || last.Assistant {
		if !la.coal.pending { // a change is being merged: its flush refreshes too, next tick otherwise
			la.refresh(last, now)
		}
		la.metrics(last, now)
	}
}

// refresh resends last (the state the last flush went with) to each device whose activity has had no push for
// fleetRefresh, before it goes stale (fleetStaleIn) and shows "waiting for the hub" without its buttons. Counted per
// device: a flush that pushed nothing to it (the change was one its island does not show, or went to another device)
// does not put it off (#4154). Not the snapshot: a request arriving that the run loop has not flushed yet goes out
// with its own urgent push, which a low priority refresh carrying it first would make look already sent.
func (la *liveActivities) refresh(last FleetState, now time.Time) {
	la.mu.Lock()
	rows := make([]db.LiveActivity, 0, len(la.rows))
	for _, r := range la.rows {
		if r.PushToken != "" && now.Sub(r.StartedAt) < fleetMaxAge && now.Sub(la.sentAt[r.DeviceID]) >= fleetRefresh {
			rows = append(rows, r)
		}
	}
	la.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	stats := la.h.fleetStats()
	for _, r := range rows {
		d, ok := la.h.push.device(r.DeviceID)
		if !ok || !devicePrefs(d.Prefs).LiveActivity || la.h.phoneOpen(r.DeviceID) {
			continue // an open app keeps its activity fresh itself
		}
		dst := forDevice(last, devicePrefs(d.Prefs), stats)
		if showsNothing(dst) {
			continue // only the assistant answering, not on this island: the activity ends with the fleet
		}
		la.markSent(r.DeviceID, dst, now, true)
		la.send(d, r, liveActivityPayload("update", dst, now), false)
	}
}

// metrics pushes the numbers alone: low priority, at most every live_metrics_seconds per device, and only to devices
// whose shown (rounded) numbers changed.
func (la *liveActivities) metrics(last FleetState, now time.Time) {
	stats := la.h.fleetStats()
	if b, _ := json.Marshal(stats); !bytes.Equal(b, la.stats) {
		// The open app already saw the latest snapshot, even while its APNs
		// update is being coalesced. Metrics must not broadcast the older last.
		la.broadcast(la.snapshot())
	}
	every := la.h.cfg.Notify.liveMetrics()
	la.mu.Lock()
	rows := make([]db.LiveActivity, 0, len(la.rows))
	for _, r := range la.rows {
		if r.PushToken != "" && now.Sub(r.StartedAt) < fleetMaxAge && now.Sub(la.sentAt[r.DeviceID]) >= every {
			rows = append(rows, r)
		}
	}
	la.mu.Unlock()
	for _, r := range rows {
		d, ok := la.h.push.device(r.DeviceID)
		if !ok || !devicePrefs(d.Prefs).LiveActivity || la.h.phoneOpen(r.DeviceID) {
			continue // an open app has every number from the fleet event
		}
		dst := forDevice(last, devicePrefs(d.Prefs), stats)
		if showsNothing(dst) {
			continue
		}
		if la.markSent(r.DeviceID, dst, now, false) {
			la.send(d, r, liveActivityPayload("update", dst, now), false)
		}
	}
}

// notYet says whether dst, a change for device, waits for its turn (fleetMinGap after its last push), and until when.
// What needs the user, a forced resend, and a device whose activity shows something unknown go now.
func (la *liveActivities) notYet(device string, dst FleetState, now time.Time, urgent bool) (due time.Time, wait bool) {
	if urgent {
		return time.Time{}, false
	}
	b, _ := json.Marshal(dst)
	la.mu.Lock()
	defer la.mu.Unlock()
	at, ok := la.sentAt[device]
	if !ok || bytes.Equal(b, la.sent[device]) {
		return time.Time{}, false
	}
	due = at.Add(la.minGap)
	return due, now.Before(due)
}

// phoneLeft: the app on device closed its connection. Gone to the background (a clean close), its activity shows the
// last fleet it was sent, which is what the device has now. Not with a new stale-date, though: a fleet that changed
// nothing it shows, a settings or look change, left the one an earlier update set, and the hub cannot tell which. It
// is only sure of fleetOpenFresh after that fleet, so the refresh is due then: its last push is put fleetRefresh
// before that (#4294). Otherwise (the network dropped, the app was killed or suspended with the socket open) what it
// shows is unknown, and so is it when that fleet is old enough to be near its stale-date: the current state goes out
// at once, without waiting for a change or fleetMinGap.
func (la *liveActivities) phoneLeft(device string, clean bool) {
	if la == nil || device == "" {
		return
	}
	d, ok := la.h.push.device(device)
	now := la.now()
	// The app wrote its widgets as it went: they show what it showed (widget_wake.go), the intake revision it was
	// last sent included, not one that moved after.
	la.mu.Lock()
	shown := widgetCountsOf(la.shownState)
	shown.intake = la.shownIntake
	la.mu.Unlock()
	la.widgets.wrote(device, shown, now)
	la.mu.Lock()
	r := la.rows[device]
	needsStart := r.PushToStartToken != "" && (r.PushToken == "" || now.Sub(r.StartedAt) >= fleetMaxAge)
	if clean && ok && !needsStart && !la.shownAt.IsZero() && now.Sub(la.shownAt) < fleetOpenFresh {
		b, _ := json.Marshal(forDevice(la.shownState, devicePrefs(d.Prefs), la.shownStats))
		la.sent[device], la.sentAt[device] = b, la.shownAt.Add(fleetOpenFresh-fleetRefresh)
	} else {
		delete(la.sent, device)
		delete(la.sentAt, device)
		la.repair = true
	}
	la.mu.Unlock()
	la.changed()
}

// markSent records dst as the device's content-state and says whether it is new (always, when forced).
func (la *liveActivities) markSent(device string, dst FleetState, now time.Time, force bool) bool {
	b, _ := json.Marshal(dst)
	la.mu.Lock()
	defer la.mu.Unlock()
	if !force && bytes.Equal(b, la.sent[device]) {
		return false
	}
	la.sent[device], la.sentAt[device] = b, now
	return true
}

// flush pushes st to every device that wants the activity: update a running one, or start one (once per episode,
// and at most once per fleetRefresh rolling window, atomically persisted across hub restarts and overlapping slots).
// A start the phone has not answered holds the next one for fleetMaxAge (holdStart): the hub has no token to update
// or end that activity with, so another start would only put a second card beside it (#7641). A start APNs or the
// relay refused holds nothing (startRefused); one with no answer holds like an accepted one. A hold and what lifts
// it are about one start, named by the time it was claimed at: they leave a start claimed later alone (holdStart).
// Whether a start is held is asked of the store each round: this hub keeps no deadline of its own, since the phone
// may answer the other slot of a blue-green switch, or a start that slot sent may be refused.
// A device whose own version did not change (the change was something it does not show) is skipped unless forced; one
// whose app is open is not pushed (it draws the activity itself); one pushed less than fleetMinGap ago gets an ordinary
// change later: next is when the first of those is due (zero: none). A settings refresh
// names only its devices and bypasses their gap, not their content deduplication.
func (la *liveActivities) flush(st FleetState, urgent, force bool, settingsDevices ...string) (next time.Time) {
	if la.h.isMember() { // the center pushes the group's Live Activity; a member's devices are the center's now
		return time.Time{}
	}
	la.coal.sentState(st)
	now := la.now()
	la.mu.Lock()
	wasOver := fleetEmpty(la.last) || la.last.Phase == "done"
	if fleetEmpty(st) {
		if la.emptyAt.IsZero() {
			la.emptyAt = now
		}
	} else {
		la.emptyAt = time.Time{}
		if wasOver && st.Phase != "done" {
			la.episode++
		}
	}
	if st.Phase == "done" {
		if la.doneSince.IsZero() {
			la.doneSince = now
		}
	} else {
		la.doneSince = time.Time{}
	}
	if la.last.Assistant && !st.Assistant {
		la.answeredAt = now
	}
	la.last = st
	episode := la.episode
	rows := make([]db.LiveActivity, 0, len(la.rows))
	for _, r := range la.rows {
		rows = append(rows, r)
	}
	la.mu.Unlock()
	// Nothing left to show ends by tick after fleetEndAfter, unless something comes back first. When what went was the
	// last thing needing the user (answered, here or on a computer, or gone: urgent), the running activities are cleared
	// at once all the same, or their request and its buttons stay up for that minute. Not ended early: a push-start is
	// rationed (fleetRefresh), and the next request arriving soon after would find no activity to open.
	if fleetEmpty(st) && !st.Assistant && !urgent {
		return time.Time{}
	}
	// whose agents the activity counts (one manager's: fleetLiveOf): a device that muted it gets no start alert
	_, stats, from := la.h.fleetLiveOf()
	for _, r := range rows {
		if len(settingsDevices) > 0 && !slices.Contains(settingsDevices, r.DeviceID) {
			continue
		}
		d, ok := la.h.push.device(r.DeviceID)
		if !ok {
			continue
		}
		prefs := devicePrefs(d.Prefs)
		if !prefs.LiveActivity {
			continue
		}
		dst := forDevice(st, prefs, stats)
		// Nothing for this device to show (an empty fleet, or only the assistant answering where its island does not
		// show it): ended by tick like an empty fleet, and only an activity already running is cleared, when the last
		// request went.
		cleared := showsNothing(dst)
		if cleared && !urgent {
			continue
		}
		if la.h.phoneOpen(r.DeviceID) {
			if r.PushToken != "" && now.Sub(r.StartedAt) < fleetMaxAge {
				la.markSent(r.DeviceID, dst, now, false) // what its app just drew from the fleet event
			}
			// No update token yet: the foreground app starts its own activity.
			// Do not consume this episode's push-start opportunity until it leaves.
			continue
		}
		switch {
		case r.PushToken != "" && now.Sub(r.StartedAt) < fleetMaxAge:
			if due, wait := la.notYet(r.DeviceID, dst, now, urgent || force || len(settingsDevices) > 0); wait {
				if next.IsZero() || due.Before(next) {
					next = due
				}
				continue
			}
			if la.markSent(r.DeviceID, dst, now, force) {
				la.send(d, r, liveActivityPayload("update", dst, now), urgent)
			}
		// Only the assistant answering starts none: whether it should is a product decision not made yet (#4398).
		case r.PushToStartToken != "" && !fleetEmpty(st) && st.Phase != "done":
			la.mu.Lock()
			already := la.started[r.DeviceID] == episode
			la.mu.Unlock()
			if already {
				continue
			}
			observed, _ := json.Marshal(la.snapshot())
			due, claimed := la.claimStart(r, now)
			if !claimed {
				if !due.IsZero() && (next.IsZero() || due.Before(next)) {
					next = due
				}
				continue
			}
			// Token registration or foreground entry during the database call wins.
			// The spent budget is deliberately not refunded: a crash or ambiguous
			// delivery must not let the other blue-green process start again.
			unchanged := func() bool {
				la.mu.Lock()
				current := la.rows[r.DeviceID]
				la.mu.Unlock()
				latest, _ := json.Marshal(la.snapshot())
				currentDevice, present := la.h.push.device(r.DeviceID)
				return current == r && bytes.Equal(observed, latest) && present && bytes.Equal(currentDevice.Prefs, d.Prefs) &&
					!la.h.phoneOpen(r.DeviceID)
			}
			suppressed := func() {
				la.h.logf("live activity start suppressed: reason=state_changed device=%s due=%s", trafficIdentity(r.DeviceID), due.UTC().Format(time.RFC3339))
				if next.IsZero() || due.Before(next) {
					next = due
				}
			}
			if !unchanged() {
				suppressed()
				continue
			}
			// Held before it is sent, so the phone's answer, which may follow at once, is what lifts it. So does a
			// start APNs or the relay refused, or that never left the hub (startRefused); one nobody answered at all
			// may be on the phone, and stays held.
			la.holdStart(context.Background(), r.DeviceID, now, fleetMaxAge)
			// The hold write can take up to storeTimeout: the app may have come to the foreground or reported an
			// update token meanwhile, and its answer's lift may have reached the store before this hold and been
			// overwritten by it. Nothing is sent then, and the hold goes back to the claim's cooldown (#7641 review).
			if !unchanged() {
				la.liftHold(context.Background(), r.DeviceID, now)
				suppressed()
				continue
			}
			la.mu.Lock()
			la.started[r.DeviceID] = episode
			la.mu.Unlock()
			la.markSent(r.DeviceID, dst, now, true)
			// "Phone Muted" while a desktop is online, the device's quiet hours (quiethours.go), or the device turned off
			// the pushes of the manager whose agents it counts (NotifyPrefs.MutedManagers): the activity still starts,
			// without its alert (desktops.go).
			quiet := prefs.MuteWhileDesktop && la.h.push.desktopOnline != nil && la.h.push.desktopOnline() ||
				la.h.push.inQuiet(prefs) || prefs.mutes(from)
			la.sendStart(d, r, now, startPayloadIn(la.h.userLang(), dst, now, quiet))
		}
	}
	return next
}

// claimStart authorizes only push-to-start, never ordinary alerts or activity updates.
// Persist before sending; without storage (or on any error) fail closed and retry
// with the latest snapshot. A timer carries no payload or token across episodes.
func (la *liveActivities) claimStart(r db.LiveActivity, now time.Time) (time.Time, bool) {
	retry := now.Add(fleetMinGap)
	if la.h.store == nil {
		la.h.logf("live activity start deferred: reason=store_unavailable device=%s due=%s", trafficIdentity(r.DeviceID), retry.UTC().Format(time.RFC3339))
		return retry, false
	}
	for !la.drainDeferredHolds() {
		if !la.h.quiesce.beginOrDefer(func() {
			la.mu.Lock()
			la.quiescedStart = true
			la.mu.Unlock()
		}) {
			return retry, false
		}
		la.h.quiesce.end()
	}
	if !la.h.quiesce.beginOrDefer(func() {
		la.mu.Lock()
		la.quiescedStart = true
		la.mu.Unlock()
	}) {
		return retry, false
	}
	defer la.h.quiesce.end()
	ctx, cancel := context.WithTimeout(context.Background(), la.storeTimeout)
	defer cancel()
	due, claimed, err := la.h.store.ClaimLiveActivityStart(ctx, r.DeviceID, r.PushToStartToken, now, fleetRefresh, fleetMaxAge)
	if err != nil {
		la.h.logf("live activity start deferred: reason=claim_failed device=%s due=%s error=%v", trafficIdentity(r.DeviceID), retry.UTC().Format(time.RFC3339), err)
		return retry, false
	}
	la.mu.Lock()
	logged, repeat := la.deferred[r.DeviceID]
	repeat = repeat && !claimed && logged.Equal(due)
	if claimed {
		delete(la.deferred, r.DeviceID)
	} else {
		la.deferred[r.DeviceID] = due
	}
	la.mu.Unlock()
	if !claimed && !repeat { // the same answer as last round is not said again: a hold lasts hours of rounds
		reason := "token_changed"
		switch {
		case due.Sub(now) > fleetRefresh:
			reason = "unanswered_start"
		case !due.IsZero():
			reason = "rolling_cooldown"
		}
		la.h.logf("live activity start deferred: reason=%s device=%s due=%s", reason, trafficIdentity(r.DeviceID), due.UTC().Format(time.RFC3339))
	}
	return due, claimed
}

// holdStart keeps the device's next push-start back until hold after its last one (HoldLiveActivityStart, in the
// start budget so a restart or the other slot holds it too): fleetMaxAge once a start goes out, since until the phone
// answers its activity may be up where the hub can neither update nor end it; fleetRefresh again when the phone
// answers or the start is refused (liftHold). Best effort: when the write fails the rolling cooldown alone paces the
// starts, as before #7641.
//
// asOf is the start the write is about: the time it was claimed at (the now its flush claimed with), or the time the
// phone answered. The store leaves a start claimed after asOf alone, so a write that comes late (a refusal after the
// send's retries, a slow store, the slot that is leaving) cannot hold or lift a newer start.
//
// The deadline lives in the store alone. A hub that remembered it would hold on after the other slot of a blue-green
// switch lifted it (the phone's app connected there, a start sent from there was refused), for up to fleetMaxAge:
// flush asks the store each round instead (claimStart).
func (la *liveActivities) holdStart(ctx context.Context, device string, asOf time.Time, hold time.Duration) {
	if la.h.store == nil {
		return
	}
	held := quiesceHeld(ctx)
	if !la.drainDeferredHoldsFor(held, deferredLiveHold{device: device, asOf: asOf, hold: hold}) {
		return
	}
	if !held {
		if !la.h.quiesce.beginOrDefer(func() {
			la.mu.Lock()
			la.deferredHolds = append(la.deferredHolds, deferredLiveHold{device: device, asOf: asOf, hold: hold})
			la.mu.Unlock()
		}) {
			return
		}
		defer la.h.quiesce.end()
	}
	ctx, cancel := context.WithTimeout(ctx, la.storeTimeout)
	defer cancel()
	if _, err := la.h.store.HoldLiveActivityStart(ctx, device, asOf, hold); err != nil {
		la.h.logf("live activity start hold: device=%s hold=%s error=%v", trafficIdentity(device), hold, err)
	}
}

// drainDeferredHoldsFor drains any holds queued during a pause before a new
// holdStart/liftHold writes to the store, without holding holdReplayMu across
// the caller's own HoldLiveActivityStart call. Returns false if writes paused
// again and incoming was appended behind the remaining deferred holds.
func (la *liveActivities) drainDeferredHoldsFor(held bool, incoming deferredLiveHold) bool {
	la.mu.Lock()
	if len(la.deferredHolds) == 0 && !la.holdReplaying {
		la.mu.Unlock()
		return true
	}
	la.mu.Unlock()

	la.holdReplayMu.Lock()
	defer la.holdReplayMu.Unlock()
	if !la.drainDeferredHoldsLocked(held) {
		la.mu.Lock()
		la.deferredHolds = append(la.deferredHolds, incoming)
		la.mu.Unlock()
		return false
	}
	return true
}

func (la *liveActivities) replayHold(h deferredLiveHold, held bool) bool {
	if la.h.store == nil {
		return true
	}
	if !held {
		if !la.h.quiesce.beginOrDefer(func() {
			la.mu.Lock()
			la.deferredHolds = append([]deferredLiveHold{h}, la.deferredHolds...)
			la.mu.Unlock()
		}) {
			return false
		}
		defer la.h.quiesce.end()
	}
	ctx, cancel := context.WithTimeout(context.Background(), la.storeTimeout)
	defer cancel()
	if _, err := la.h.store.HoldLiveActivityStart(ctx, h.device, h.asOf, h.hold); err != nil {
		la.h.logf("live activity start hold: device=%s hold=%s error=%v", trafficIdentity(h.device), h.hold, err)
	}
	return true
}

// drainDeferredHolds persists holds deferred during a quiesce pause in FIFO
// order before any newer hold/lift or start claim can touch the store budget.
// Returns false if writes were paused again before all deferred holds drained.
func (la *liveActivities) drainDeferredHolds() bool {
	if la.h.store == nil {
		return true
	}
	la.holdReplayMu.Lock()
	defer la.holdReplayMu.Unlock()
	return la.drainDeferredHoldsLocked(false)
}

func (la *liveActivities) drainDeferredHoldsLocked(held bool) bool {
	la.mu.Lock()
	if len(la.deferredHolds) == 0 {
		la.mu.Unlock()
		return true
	}
	la.holdReplaying = true
	la.mu.Unlock()
	defer func() {
		la.mu.Lock()
		la.holdReplaying = false
		la.mu.Unlock()
	}()
	for {
		la.mu.Lock()
		if len(la.deferredHolds) == 0 {
			la.mu.Unlock()
			return true
		}
		h := la.deferredHolds[0]
		la.deferredHolds = la.deferredHolds[1:]
		la.mu.Unlock()
		if !la.replayHold(h, held) {
			return false
		}
	}
}

// phoneAnswered: the phone reached the hub (it reported an activity's update token; its app connecting is
// phoneConnected), so the hub hears of what it starts there again: the next start waits only for the rolling cooldown.
// The answer is stamped now: it lifts the hold of a start claimed up to now, never of one claimed while its write was
// on its way.
// device is the phone's push id, which the start was claimed under (the activity's row, the start budget).
func (la *liveActivities) phoneAnswered(ctx context.Context, device string) {
	if la == nil || device == "" {
		return
	}
	la.liftHold(ctx, device, la.now())
}

// phoneConnectedLater is phoneConnected off the app's read loop, which never waits for the store (#6889): one
// goroutine at a time lifts the queued answers in order, each as of when it arrived, and ends when none is left. A
// repeated registration replaces its queued one; past helloPendingLimit a new one is dropped (logged): the hold then
// lasts until the phone reports a token, registers again or fleetMaxAge passes.
func (la *liveActivities) phoneConnectedLater(pushID, credential string) {
	if la == nil {
		return
	}
	a := phoneAnswer{pushID: pushID, credential: credential, at: la.now()}
	la.answerMu.Lock()
	defer la.answerMu.Unlock()
	if i := slices.IndexFunc(la.answers, func(q phoneAnswer) bool { return q.pushID == pushID && q.credential == credential }); i >= 0 {
		la.answers[i].at = a.at
	} else if len(la.answers) >= helloPendingLimit {
		la.h.logf("live activity start hold: answer dropped, %d waiting", len(la.answers))
		return
	} else {
		la.answers = append(la.answers, a)
	}
	if !la.answering {
		la.answering = true
		go la.liftAnswers()
	}
}

func (la *liveActivities) liftAnswers() {
	for {
		la.answerMu.Lock()
		if len(la.answers) == 0 {
			la.answering = false
			la.answerMu.Unlock()
			return
		}
		a := la.answers[0]
		la.answers = la.answers[1:]
		la.answerMu.Unlock()
		la.phoneConnected(context.Background(), a.pushID, a.credential, a.at)
	}
}

// phoneConnected: a phone's app registered over its connection (register_phone), which answers the starts claimed
// for that phone. They are claimed under its push id, so that is what names it: pushID when the app gave one. An
// older app gives none, and what its registration is kept under instead (the connection's credential, or "legacy")
// is no push id: lifting that would lift nothing. Such a phone is the devices its credential registered (or took
// since, Device.Credential) or, on a connection without a credential, the devices that have none, as wakeDevices
// finds the phone of such a registration. Never a Mac or a watch: they register as devices too and run no activity
// of the phone's. at is when the app registered: its answer lifts the holds of starts claimed up to then.
func (la *liveActivities) phoneConnected(ctx context.Context, pushID, credential string, at time.Time) {
	if la == nil {
		return
	}
	if pushID != "" {
		la.liftHold(ctx, pushID, at)
		return
	}
	if la.h.push == nil {
		return
	}
	for _, d := range la.h.push.list() {
		its := d.CredentialID == credential || d.Credential == credential
		if credential == "" {
			its = d.CredentialID == "" && d.Credential == ""
		}
		if its && d.Kind != kindMac && d.Kind != "watch" {
			la.liftHold(ctx, d.DeviceID, at)
		}
	}
}

// startRefused: the start claimed at claimed did not reach the phone for certain (pushRefused: never sent, or APNs or
// the relay answered with a status other than 200), so there is no card to hold for: the next start waits only for the
// rolling cooldown, as before #7641. A start with no answer at all (pushUnknown) is not lifted: APNs may have taken it,
// and another start would put a second card beside it, so it waits for the phone or fleetMaxAge like an accepted one.
// The refusal lifts its own start's hold only: if another start was claimed before it came, that one's hold stays.
func (la *liveActivities) startRefused(device string, claimed time.Time) {
	la.h.logf("live activity start refused: device=%s, next start after the rolling cooldown", trafficIdentity(device))
	la.liftHold(context.Background(), device, claimed)
}

// liftHold puts the device's next start back on the rolling cooldown, unless its last start was claimed after asOf
// (holdStart). In the store only, where every hub's next round reads it.
func (la *liveActivities) liftHold(ctx context.Context, device string, asOf time.Time) {
	la.holdStart(ctx, device, asOf, fleetRefresh)
}

func (la *liveActivities) endAll() { la.end(nil) }

// endCredential withdraws only this credential's activities before signedOut forgets their push routes.
func (la *liveActivities) endCredential(credential string) {
	if la == nil || la.h.push == nil || credential == "" {
		return
	}
	var pending []<-chan struct{}
	for _, d := range la.h.push.list() {
		if d.Credential != credential {
			continue
		}
		la.mu.Lock()
		r := la.rows[d.DeviceID]
		// Stop future fleet flushes while the end is in flight. Revocation already removes persisted rows.
		delete(la.rows, d.DeviceID)
		delete(la.sent, d.DeviceID)
		delete(la.sentAt, d.DeviceID)
		delete(la.started, d.DeviceID)
		delete(la.deferred, d.DeviceID)
		delete(la.resend, d.DeviceID)
		delete(la.deferredSaves, d.DeviceID)
		if la.rowGen != nil {
			la.rowGen[d.DeviceID]++
		}
		la.mu.Unlock()
		if r.PushToken != "" {
			// No agent details in the final state, even if dismissal is delayed by the phone.
			pending = append(pending, la.h.push.sendLiveActivity(d, r.PushToken,
				liveActivityPayload("end", FleetState{}, time.Now()), "10", func() {}))
		}
	}
	// Dispatch every device concurrently, but keep registrations until the transports finish checking them.
	for _, done := range pending {
		<-done
	}
}

// end ends the running activities: all of them (keep nil, the fleet has been over for a while), or those of the
// devices whose settings keep does not hold on to.
func (la *liveActivities) end(keep func(NotifyPrefs) bool) {
	now := time.Now()
	la.mu.Lock()
	rows := make([]db.LiveActivity, 0, len(la.rows))
	for _, r := range la.rows {
		if r.PushToken != "" {
			rows = append(rows, r)
		}
	}
	last := la.last
	if keep == nil {
		la.emptyAt, la.doneSince, la.answeredAt = time.Time{}, time.Time{}, time.Time{}
		la.last = FleetState{}
		la.sent, la.sentAt = map[string][]byte{}, map[string]time.Time{}
	}
	la.mu.Unlock()
	for _, r := range rows {
		d, ok := la.h.push.device(r.DeviceID)
		if ok && keep != nil && keep(devicePrefs(d.Prefs)) {
			continue
		}
		if ok {
			la.send(d, r, liveActivityPayload("end", last, now), false)
		}
		la.mu.Lock()
		delete(la.sent, r.DeviceID)
		delete(la.sentAt, r.DeviceID)
		la.mu.Unlock()
		la.update(r.DeviceID, func(v *db.LiveActivity) { v.PushToken, v.ActivityID = "", "" })
	}
}

// liveActivityPayload is the APNs body for one event. No `category`, no sound: the interaction push already rang.
func liveActivityPayload(event string, st FleetState, now time.Time) map[string]any {
	return liveActivityPayloadIn(i18n.Default, event, st, now)
}

// liveActivityPayloadIn is liveActivityPayload with a start's alert in l.
func liveActivityPayloadIn(l i18n.Lang, event string, st FleetState, now time.Time) map[string]any {
	aps := map[string]any{"timestamp": now.Unix(), "event": event, "content-state": st}
	switch event {
	case "end":
		aps["dismissal-date"] = now.Unix()
	default:
		aps["stale-date"] = now.Add(fleetStaleIn).Unix()
		aps["relevance-score"] = fleetRelevance(st)
	}
	if event == "start" {
		aps["attributes-type"] = "PersonaActivityAttributes"
		aps["attributes"] = map[string]any{"kind": "fleet"}
		aps["alert"] = map[string]any{"title": "Persona", "body": fleetSentence(l, st)}
	}
	return map[string]any{"aps": aps}
}

// startPayloadIn is a push-to-start written in l; quiet drops its alert (the phone is muted while a desktop is online,
// or in its quiet hours).
func startPayloadIn(l i18n.Lang, st FleetState, now time.Time, quiet bool) map[string]any {
	p := liveActivityPayloadIn(l, "start", st, now)
	if quiet {
		delete(p["aps"].(map[string]any), "alert")
	}
	return p
}

// fleetSentence: "2 need you · 3 working" (in Chinese), the alert of a push-started activity.
func fleetSentence(l i18n.Lang, st FleetState) string {
	var parts []string
	if st.NeedsYou > 0 {
		parts = append(parts, i18n.T(l, "hub-liveactivity.needs_you", i18n.Args{"count": st.NeedsYou}))
	}
	if st.Working > 0 {
		parts = append(parts, i18n.T(l, "hub-liveactivity.working", i18n.Args{"count": st.Working}))
	}
	if st.Unread > 0 {
		parts = append(parts, i18n.T(l, "hub-liveactivity.unread", i18n.Args{"count": st.Unread}))
	}
	return strings.Join(parts, " · ")
}

// retire ends each activity about to reach fleetMaxAge while its token still reaches it, and forgets that it was
// started this episode, so the next flush may start a fresh one instead of leaving a card iOS ended (and keeps on the
// Lock Screen for hours, stale) beside the new one (#12982). An open app's activity is the app's to replace.
func (la *liveActivities) retire(now time.Time) {
	la.mu.Lock()
	var old []db.LiveActivity
	for _, r := range la.rows {
		if r.PushToken != "" && !r.StartedAt.IsZero() && now.Sub(r.StartedAt) >= fleetMaxAge-fleetRetireBefore {
			old = append(old, r)
		}
	}
	la.mu.Unlock()
	for _, r := range old {
		if la.h.phoneOpen(r.DeviceID) {
			continue
		}
		la.endToken(r.DeviceID, r.PushToken, now)
		la.mu.Lock()
		delete(la.sent, r.DeviceID)
		delete(la.sentAt, r.DeviceID)
		delete(la.started, r.DeviceID)
		la.mu.Unlock()
		la.update(r.DeviceID, func(v *db.LiveActivity) {
			if v.PushToken == r.PushToken {
				v.PushToken, v.ActivityID = "", ""
			}
		})
	}
}

// endToken ends (and takes off the Lock Screen at once) the activity behind token on device: one being retired, or one
// another activity of the same phone replaced (#12982). Best effort: an activity iOS already ended stays until iOS
// removes it.
func (la *liveActivities) endToken(device, token string, now time.Time) {
	if token == "" || la.h.push == nil {
		return
	}
	d, ok := la.h.push.device(device)
	if !ok {
		return
	}
	la.mu.Lock()
	last := la.last
	la.mu.Unlock()
	la.h.logf("live activity: ending a replaced or retiring activity on %s", d.Name)
	la.h.push.sendLiveActivity(d, token, liveActivityPayload("end", last, now), "10", func() {})
}

func (la *liveActivities) send(d Device, r db.LiveActivity, payload map[string]any, urgent bool) {
	prio := "5"
	if urgent {
		prio = "10"
	}
	la.h.push.sendLiveActivity(d, r.PushToken, payload, prio, func() {
		la.update(r.DeviceID, func(v *db.LiveActivity) {
			if v.PushToken == r.PushToken {
				v.PushToken, v.ActivityID = "", ""
			}
		})
	})
}

// sendStart sends the start claimed at claimed; a refusal lifts that start's hold (startRefused).
func (la *liveActivities) sendStart(d Device, r db.LiveActivity, claimed time.Time, payload map[string]any) {
	la.h.logf("live activity: push-start on %s", d.Name)
	la.h.push.sendLiveActivityThen(d, r.PushToStartToken, compatibleStartPayload(d, payload), "10", func() {
		la.update(r.DeviceID, func(v *db.LiveActivity) {
			if v.PushToStartToken == r.PushToStartToken {
				v.PushToStartToken = ""
			}
		})
	}, func(o pushResult) {
		if o == pushRefused {
			la.startRefused(r.DeviceID, claimed)
		}
	})
}

// update keeps background token cleanup running even if persistence fails; updateContext logs the error.
func (la *liveActivities) update(device string, fn func(*db.LiveActivity)) {
	_ = la.updateContext(context.Background(), device, fn)
}

func (la *liveActivities) deviceSaveMuLocked(device string) *sync.Mutex {
	if la.saveMu == nil {
		la.saveMu = map[string]*sync.Mutex{}
	}
	m := la.saveMu[device]
	if m == nil {
		m = &sync.Mutex{}
		la.saveMu[device] = m
	}
	return m
}

// updateContext changes one device's row and bounds persistence by the caller's context and the store timeout.
func (la *liveActivities) updateContext(ctx context.Context, device string, fn func(*db.LiveActivity)) error {
	la.mu.Lock()
	v := la.rows[device]
	was := v.PushToken
	v.DeviceID = device
	fn(&v)
	la.rows[device] = v
	var devMu *sync.Mutex
	var waiter *liveSaveWaiter
	if la.h.store != nil {
		if la.rowGen == nil {
			la.rowGen = map[string]uint64{}
		}
		la.rowGen[device]++
		wantGen := la.rowGen[device]
		if la.deferredSaves == nil {
			la.deferredSaves = map[string]bool{}
		}
		la.deferredSaves[device] = true
		waiter = &liveSaveWaiter{wantGen: wantGen}
		if la.saveWaiters == nil {
			la.saveWaiters = map[string][]*liveSaveWaiter{}
		}
		la.saveWaiters[device] = append(la.saveWaiters[device], waiter)
		devMu = la.deviceSaveMuLocked(device)
	}
	la.mu.Unlock()
	defer la.changed() // a new token gets the current state even if persistence needs a retry
	if v.PushToken != "" && v.PushToken != was {
		la.phoneAnswered(ctx, device) // only the phone reports an update token
	}
	if waiter != nil {
		return la.flushDeviceSave(ctx, device, devMu, waiter)
	}
	return nil
}

func (la *liveActivities) removeSaveWaiter(device string, waiter *liveSaveWaiter) {
	la.mu.Lock()
	list := la.saveWaiters[device]
	if i := slices.Index(list, waiter); i >= 0 {
		list = slices.Delete(list, i, i+1)
		if len(list) == 0 {
			delete(la.saveWaiters, device)
		} else {
			la.saveWaiters[device] = list
		}
	}
	la.mu.Unlock()
}

func (la *liveActivities) waiterResultLocked(waiter *liveSaveWaiter) error {
	if waiter != nil && waiter.done {
		return waiter.err
	}
	return nil
}

func (la *liveActivities) flushDeviceSave(ctx context.Context, device string, devMu *sync.Mutex, waiter *liveSaveWaiter) error {
	if waiter != nil {
		defer la.removeSaveWaiter(device, waiter)
	}
	if la.h.store == nil {
		return nil
	}
	devMu.Lock()
	defer devMu.Unlock()
	held := quiesceHeld(ctx)
	first := true
	for {
		la.mu.Lock()
		if !la.deferredSaves[device] {
			err := la.waiterResultLocked(waiter)
			la.mu.Unlock()
			return err
		}
		v, ok := la.rows[device]
		gen := la.rowGen[device]
		delete(la.deferredSaves, device)
		la.mu.Unlock()
		if !ok {
			la.mu.Lock()
			err := la.waiterResultLocked(waiter)
			la.mu.Unlock()
			return err
		}
		if !held {
			if !la.h.quiesce.beginOrDefer(func() {
				la.mu.Lock()
				if la.rowGen[device] == gen {
					if la.deferredSaves == nil {
						la.deferredSaves = map[string]bool{}
					}
					la.deferredSaves[device] = true
				}
				la.mu.Unlock()
			}) {
				la.mu.Lock()
				err := la.waiterResultLocked(waiter)
				la.mu.Unlock()
				return err
			}
		}
		baseCtx := context.Background()
		if first && ctx != nil {
			baseCtx = ctx
		}
		first = false
		sctx, cancel := context.WithTimeout(baseCtx, la.storeTimeout)
		err := la.h.store.SaveLiveActivity(sctx, v)
		cancel()
		if !held {
			la.h.quiesce.end()
		}
		if err != nil {
			la.h.logf("save live activity: %v", err)
		}
		la.mu.Lock()
		for _, w := range la.saveWaiters[device] {
			if !w.done && gen >= w.wantGen {
				w.done = true
				w.err = err
			}
		}
		la.mu.Unlock()
	}
}

// resumeWrites flushes Live Activity holds, saves and start claims deferred
// while a development snapshot paused hub writers (quiesce.go). Deferred holds
// are persisted before the repair flush is signaled so a resumed start claim
// cannot read a stale start budget.
func (la *liveActivities) resumeWrites() {
	if la == nil {
		return
	}
	type saveItem struct {
		device string
		mu     *sync.Mutex
	}
	la.mu.Lock()
	quiescedStart := la.quiescedStart
	la.quiescedStart = false
	hasHolds := len(la.deferredHolds) > 0
	var saves []saveItem
	for device := range la.deferredSaves {
		saves = append(saves, saveItem{device: device, mu: la.deviceSaveMuLocked(device)})
	}
	if quiescedStart && !hasHolds {
		la.repair = true
	}
	la.mu.Unlock()
	signalRepair := func() {
		la.mu.Lock()
		la.repair = true
		la.mu.Unlock()
		select {
		case la.signal <- struct{}{}:
		default:
		}
	}
	if quiescedStart && !hasHolds {
		select {
		case la.signal <- struct{}{}:
		default:
		}
	}
	if !hasHolds && len(saves) == 0 {
		return
	}
	la.h.nodeWorkers.Add(1)
	go func() {
		defer la.h.nodeWorkers.Done()
		if hasHolds {
			replayed := la.drainDeferredHolds()
			if quiescedStart {
				if replayed {
					signalRepair()
				} else if la.h.quiesce.beginOrDefer(func() {
					la.mu.Lock()
					la.quiescedStart = true
					la.mu.Unlock()
				}) {
					la.h.quiesce.end()
					signalRepair()
				}
			}
		}
		for _, s := range saves {
			_ = la.flushDeviceSave(nil, s.device, s.mu, nil)
		}
	}()
}

func (la *liveActivities) routes(api *http.ServeMux) {
	// The app reports its activity's push token (it rotates), the push-to-start token, or that the activity ended.
	api.HandleFunc("POST /api/v1/devices/{id}/live-activity", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ActivityID       string `json:"activity_id"`
			PushToken        string `json:"push_token"`
			PushToStartToken string `json:"push_to_start_token"`
			Ended            bool   `json:"ended"`
		}
		if err := decodeJSONBody(w, r, &body, 16<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		var replaced string // the token of the activity this report's activity takes over from (#12982)
		err := la.updateContext(r.Context(), r.PathValue("id"), func(v *db.LiveActivity) {
			if body.PushToStartToken != "" {
				v.PushToStartToken = body.PushToStartToken
			}
			switch {
			case body.Ended:
				if body.ActivityID == "" || body.ActivityID == v.ActivityID {
					v.ActivityID, v.PushToken = "", ""
				}
			case body.PushToken != "":
				if body.ActivityID != v.ActivityID {
					v.StartedAt = time.Now().UTC()
					if v.ActivityID != "" && v.PushToken != "" && v.PushToken != body.PushToken {
						replaced = v.PushToken
					}
				}
				v.ActivityID, v.PushToken = body.ActivityID, body.PushToken
			}
		})
		if errors.Is(err, db.ErrPushRegistrationConflict) {
			writeErrCode(w, http.StatusConflict, pushRegistrationRequired, i18n.Err("push.registration_required"))
			return
		}
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if replaced != "" {
			// One card per phone: the hub updates only the newest activity, so the one before it would sit frozen
			// beside it, "Waiting for hub update…", until iOS removed it hours later.
			la.endToken(r.PathValue("id"), replaced, la.now())
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// The current state, for the app to start or refresh its activity while it is open.
	api.HandleFunc("GET /api/v1/fleet", func(w http.ResponseWriter, r *http.Request) {
		st := la.snapshot()
		stats := la.h.fleetStats()
		st.Stats = &stats
		writeJSON(w, map[string]any{"fleet": st})
	})
}
