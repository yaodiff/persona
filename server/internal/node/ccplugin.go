package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Persona's Claude Code plugin (integrations/claude-code-plugin; docs/integrations/claude-integration.md "Owner input
// through the Persona plugin"). A claude outside Persona's terminals — Claude's desktop app, a terminal no engine runs,
// a background session — has no pane to type into, and its peer inbox tells the model that what comes through it is
// another session's, never the person's approval. The plugin, loaded in such a claude, asks this node for messages,
// owner turn interrupts and session-scoped model / effort overrides over the node socket while the session lives:
// messages go in with $.prompt.submit({asUser: true}), interrupts abort the active main-loop turn, and model / effort
// overrides rewrite main-loop turn.step calls without touching global settings files.
//
// Only the owner's own messages go that way (session.send's owner_input, which the devices manager sets for a message
// typed in the owner's app, devices.go ownerInputParams). With the plugin's busy_input capability they go at rest and
// while a turn runs; an older plugin gets them only at rest. One waiting on a dialog gets the message through the inbox
// as before (it queues there, or answers its question), and so does everyone else's message. Owner-attested interrupts
// use the plugin's interrupt capability, with the inbox as fallback. When the plugin does not take a message in time or
// says it did not submit it, it goes through the inbox too, and session.send says which way it went (delivered_as,
// relay_reason).
//
// The wire is HTTP/1.1 on the node socket (serveCCPluginHTTP), since a plugin's only local transport is $.http.fetch over
// a Unix socket: POST /claude-plugin/v1/poll and /ack, JSON both ways (the plugin's README.md). The node answers only
// the claude process that has the session open: the socket's other end, as the system names it (ccPluginPeerPID), must
// be the pid of that session's sessions/<pid>.json. No other process of the user, and no other session, can take a
// session's messages; and nothing that comes in on the socket ever becomes a message, which only session.send makes.

// ccPluginProtocol is the plugin protocol this node speaks (claude-plugin.status).
const ccPluginProtocol = 1

// ccPluginPrefix leads the plugin's endpoints.
const ccPluginPrefix = "/claude-plugin/v1/"

const (
	// ccPluginPollMost bounds how long a poll is held: under the 30 s after which Claude Code's fetch gives up.
	ccPluginPollMost = 25 * time.Second
	// ccPluginOtherRetry is how long a claude whose session the node does not deliver to this way (one in an engine's
	// pane, the assistant's own, an SDK's) waits before it asks again.
	ccPluginOtherRetry = 5 * time.Minute
	// ccPluginStrangerRetry is how long a caller the node does not take for the session it names waits: a claude that
	// has not written its sessions/<pid>.json yet asks again soon.
	ccPluginStrangerRetry = 15 * time.Second
	// ccPluginBodyMost bounds a request's body, and ccPluginRequestMost the whole request with its headers.
	ccPluginBodyMost    = 64 << 10
	ccPluginRequestMost = 256 << 10
	// ccPluginStale is how long after its last poll a plugin still counts: an idle one polls again at once, a busy
	// session's plugin keeps polling too (the node, not the plugin, tells whether the session rests).
	ccPluginStale = 90 * time.Second
	// ccPluginQueuedTTL bounds how long pre-queued session settings wait for a newly opened desktop session to poll.
	ccPluginQueuedTTL = 2 * time.Minute
)

var (
	// ccPluginPickupWait is how long a message waits for the session's plugin to take it: an idle plugin's next poll
	// comes within moments of the last one ending.
	ccPluginPickupWait = 3 * time.Second
	// ccPluginRejoinWindow and ccPluginRejoinWait: for this long after the node started, a session whose claude can
	// have the plugin (it is installed in that claude's config directory) but has not polled this node yet gets up to
	// ccPluginRejoinWait for its first poll. A node that just restarted (an update, a deploy) knows no plugin until
	// each polls again, which a plugin's backoff after the node went away puts a few seconds off; a message sent then
	// went through the inbox (no_plugin), where a busy session can drop it without a trace.
	ccPluginRejoinWindow = 3 * time.Minute
	ccPluginRejoinWait   = 10 * time.Second
	// ccPluginSubmitWait is the deadline the plugin gets: it submits nothing later than this after it was handed over.
	ccPluginSubmitWait = 5 * time.Second
	// ccPluginConfirmWait is how long the node looks for the prompt in the transcript, past ccPluginSubmitWait: only a
	// message the plugin can no longer submit goes through the inbox instead.
	ccPluginConfirmWait = 10 * time.Second
)

// What session.send says about how a message for the owner went in (delivered_as), and why it went through the inbox
// (relay_reason). Only messages that carried owner_input to a claude outside Persona's terminals have them.
const (
	deliveredUserInput = "user_input" // the plugin submitted it as the person's words
	deliveredRelayed   = "relayed"    // through the session's peer inbox, as another session's message

	relayNoPlugin = "no_plugin" // no plugin polls from this session's claude
	relayBusy     = "busy"      // the session is in a state the plugin does not deliver in (its status file says neither)
	// relayOutdated: the session works or waits on a dialog, and the plugin it loaded is one from before busy_input
	// (0.1.0): it would hold its poll until the turn ends. Claude Code keeps the plugin a session loaded until that
	// session starts again (an update says "Restart to apply changes", and a desktop session cannot run
	// /reload-plugins), so the app says what to do on the computer.
	relayOutdated    = "plugin_outdated"
	relayUnavailable = "plugin_unavailable" // the plugin did not take it in time
	relayRefused     = "plugin_refused"     // the plugin took it and did not submit it
	relayUnconfirmed = "plugin_unconfirmed" // taken, and nothing showed it went in before its deadline
)

// ccPluginMessage is one message handed to a plugin, as the poll answers it.
type ccPluginMessage struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	DeadlineMS int64  `json:"deadline_ms"`
}

// ccPluginInterrupt asks the plugin to abort the running main-loop turn via $.turn.abort.
type ccPluginInterrupt struct {
	ID         string `json:"id"`
	DeadlineMS int64  `json:"deadline_ms"`
}

// ccPluginSettings is a session-only model / effort override handed to a plugin on /poll.
type ccPluginSettings struct {
	ID     string `json:"id"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type ccPluginQueuedSettings struct {
	settings ccPluginSettings
	at       time.Time
}

// ccPluginDelivery is what a woken poll returns to the plugin.
type ccPluginDelivery struct {
	Messages    []ccPluginMessage  `json:"messages"`
	Interrupt   *ccPluginInterrupt `json:"interrupt,omitempty"`
	Settings    *ccPluginSettings  `json:"settings,omitempty"`
	Permissions bool               `json:"permissions,omitempty"`
}

// ccPluginAck is what the plugin said became of a message, interrupt or settings update.
type ccPluginAck struct {
	Submitted bool
	Reason    string
}

type ccPluginCaps struct {
	busyInput   bool
	interrupt   bool
	settings    bool
	permissions bool
}

// ccPluginEffort carries the time of the step itself so a later poll cannot outrank a newer session choice.
type ccPluginEffort struct {
	AtMS  int64  `json:"at_ms"`
	Level string `json:"level,omitempty"`
	Reset bool   `json:"reset,omitempty"`
}

// ccPluginSession is a session whose plugin polls this node.
type ccPluginSession struct {
	pid               int
	version, engine   string
	caps              ccPluginCaps
	permAuthorized    bool
	classicPassedOver bool
	effort            ccPluginEffort
	effortSeen        time.Time
	seen              time.Time
	// poll is the waiting poll's hand-off, nil while none waits; done ends it early (a newer poll).
	poll chan ccPluginDelivery
	done chan struct{}
	acks map[string]chan ccPluginAck // messages, interrupts or settings handed over and not settled, by id
}

// ccPluginRegistry is every session whose plugin polled this node since it started; its zero value is ready.
type ccPluginRegistry struct {
	mu             sync.Mutex
	sessions       map[string]*ccPluginSession
	queued         map[string]ccPluginQueuedSettings
	personaHooks   map[string]ccPluginPersonaHook
	perms          map[ccPluginPermID]*ccPluginPerm
	completedReqs  map[ccPluginPermID]time.Time
	completedTools map[string]time.Time
}

// polled records a poll from session id's claude pid and makes it the session's waiting poll; the poll it replaces
// (the same session's, still waiting) is ended. When settings were pre-queued for id and the plugin supports settings,
// polled returns them immediately without leaving a waiting channel. When permission authorization changed (for example
// the first authorized poll of a permissions-capable plugin), permAuthChanged is true and the caller responds at once
// instead of holding the first poll for 25 s. It also reports changes to session metadata.
func (r *ccPluginRegistry) polled(id string, pid int, version, engine string, caps ccPluginCaps, allowPerms, classicPassedOver bool, effort ccPluginEffort) (poll chan ccPluginDelivery, done chan struct{}, immediate *ccPluginSettings, permAuthChanged, metadataChanged bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*ccPluginSession{}
	}
	now := time.Now()
	for other, o := range r.sessions {
		if o.poll == nil && len(o.acks) == 0 && now.Sub(o.seen) > ccPluginStale {
			delete(r.sessions, other) // its claude exited or moved on; a live one polls again and is back
		}
	}
	for qid, q := range r.queued {
		if now.Sub(q.at) > ccPluginQueuedTTL {
			delete(r.queued, qid)
		}
	}
	s := r.sessions[id]
	isNew := false
	if s == nil || s.pid != pid {
		s = &ccPluginSession{pid: pid, acks: map[string]chan ccPluginAck{}}
		r.sessions[id] = s
		isNew = true
	}
	if s.done != nil {
		close(s.done)
		s.poll, s.done = nil, nil
	}
	auth := caps.permissions && allowPerms
	permAuthChanged = s.permAuthorized != auth
	changed := isNew || s.caps != caps || permAuthChanged || s.classicPassedOver != classicPassedOver
	// Only regular readback polls carry a snapshot. A timestamped reset on load or session change
	// invalidates the previous snapshot without counting as evidence against a session setting.
	if effort.AtMS > 0 && effort.AtMS >= s.effort.AtMS {
		changed = changed || s.effort.Level != effort.Level || s.effort.Reset != effort.Reset || now.Sub(s.effortSeen) >= ccPluginStale
		s.effort, s.effortSeen = effort, now
	}
	s.version, s.engine, s.caps, s.permAuthorized, s.classicPassedOver, s.seen = version, engine, caps, auth, classicPassedOver, now
	if caps.settings {
		if q, ok := r.queued[id]; ok {
			delete(r.queued, id)
			cp := q.settings
			return nil, nil, &cp, permAuthChanged, changed
		}
	}
	if permAuthChanged {
		return nil, nil, nil, true, changed
	}
	s.poll, s.done = make(chan ccPluginDelivery, 1), make(chan struct{})
	return s.poll, s.done, nil, false, changed
}

// pollEnded takes poll off session id when it is still the waiting one; ok false when a delivery was handed to it (or
// a newer poll replaced it), which the caller then reads from it.
func (r *ccPluginRegistry) pollEnded(id string, poll chan ccPluginDelivery) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil || s.poll != poll {
		return false
	}
	s.poll, s.done = nil, nil
	s.seen = time.Now()
	return true
}

// session is session id's plugin while it polls from claude pid (a poll waits, or the last one ended a moment ago);
// nil for none.
func (r *ccPluginRegistry) session(id string, pid int) *ccPluginSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil && s.pid == pid && (s.poll != nil || time.Since(s.seen) < ccPluginStale) {
		return s
	}
	return nil
}

// observedEffort returns a snapshot and the process that supplied it while its plugin is live.
func (r *ccPluginRegistry) observedEffort(id string) (ccPluginEffort, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s != nil && !s.effort.Reset && s.effort.AtMS > 0 && time.Since(s.effortSeen) < ccPluginStale {
		return s.effort, s.pid
	}
	return ccPluginEffort{}, 0
}

// busyInputCapable reports whether session id's polling plugin supports queueing owner messages while a turn runs.
func (r *ccPluginRegistry) busyInputCapable(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.caps.busyInput && (s.poll != nil || time.Since(s.seen) < ccPluginStale)
}

// interruptCapable reports whether session id's polling plugin supports aborting the running turn via $.turn.abort.
func (r *ccPluginRegistry) interruptCapable(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.caps.interrupt && (s.poll != nil || time.Since(s.seen) < ccPluginStale)
}

// settingsCapable reports whether session id's polling plugin supports session-scoped model/effort updates.
func (r *ccPluginRegistry) settingsCapable(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.caps.settings && (s.poll != nil || time.Since(s.seen) < ccPluginStale)
}

// permissionsCapable reports whether session id's polling plugin is authorized and supports permission approval bridging.
func (r *ccPluginRegistry) permissionsCapable(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.caps.permissions && s.permAuthorized && (s.poll != nil || time.Since(s.seen) < ccPluginStale)
}

// permissionsAuthorized reports whether session id's current poll authorization permits bridging permissions.
func (r *ccPluginRegistry) permissionsAuthorized(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.permAuthorized
}

// classicPassedOver reports whether the session's plugin saw a main-loop tool call bypass classic.PreToolUse.
func (r *ccPluginRegistry) classicPassedOver(id string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	return s != nil && s.pid == pid && s.classicPassedOver && (s.poll != nil || time.Since(s.seen) < ccPluginStale)
}

// cancelPermissions withdraws every active plugin permission card for one session and process.
func (r *ccPluginRegistry) cancelPermissions(sessionID string, pid int) {
	var toCancel []*ccPluginPerm
	r.mu.Lock()
	for _, p := range r.perms {
		if p.sessionID == sessionID && p.pid == pid {
			toCancel = append(toCancel, p)
		}
	}
	r.mu.Unlock()
	for _, p := range toCancel {
		p.cancelAsPassthrough()
	}
}

// queueSettings stores desired model/effort settings for session id so the next settings-capable poll receives them
// immediately (or wakes a currently waiting poll right away).
func (r *ccPluginRegistry) queueSettings(id, model, effort string) {
	if id == "" || (model == "" && effort == "") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queued == nil {
		r.queued = map[string]ccPluginQueuedSettings{}
	}
	prev := r.queued[id].settings
	if model == "" {
		model = prev.Model
	}
	if effort == "" {
		effort = prev.Effort
	}
	upd := ccPluginSettings{
		ID:     strings.ReplaceAll(newUUID(), "-", ""),
		Model:  model,
		Effort: effort,
	}
	if s := r.sessions[id]; s != nil && s.caps.settings && s.poll != nil {
		s.poll <- ccPluginDelivery{Messages: []ccPluginMessage{}, Settings: &upd}
		s.poll, s.done = nil, nil
		delete(r.queued, id)
		return
	}
	r.queued[id] = ccPluginQueuedSettings{settings: upd, at: time.Now()}
}

// moveQueuedSettings moves pre-queued settings from oldID to newID when Claude Desktop continues a resting session
// under a new transcript UUID.
func (r *ccPluginRegistry) moveQueuedSettings(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.queued[oldID]
	if !ok {
		return
	}
	delete(r.queued, oldID)
	if r.queued == nil {
		r.queued = map[string]ccPluginQueuedSettings{}
	}
	r.queued[newID] = q
}

// expect makes room for the plugin's word on item mid, before it is handed over.
func (r *ccPluginRegistry) expect(id, mid string) chan ccPluginAck {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan ccPluginAck, 1)
	if s := r.sessions[id]; s != nil {
		s.acks[mid] = ch
	}
	return ch
}

// settle forgets item mid.
func (r *ccPluginRegistry) settle(id, mid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil {
		delete(s.acks, mid)
	}
}

// acked passes the plugin's word on item mid on, false for an item the node is not waiting on.
func (r *ccPluginRegistry) acked(id string, pid int, mid string, a ccPluginAck) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil || s.pid != pid {
		return false
	}
	ch, ok := s.acks[mid]
	if !ok {
		return false
	}
	select {
	case ch <- a:
	default:
	}
	return true
}

// handOver gives d to session id's waiting poll from claude pid, waiting up to wait for one; false when none took it.
func (r *ccPluginRegistry) handOver(ctx context.Context, id string, pid int, d ccPluginDelivery, wait time.Duration) bool {
	if d.Messages == nil {
		d.Messages = []ccPluginMessage{}
	}
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		s := r.sessions[id]
		if s != nil && s.pid == pid && s.poll != nil {
			s.poll <- d // room for one, and only handed over once: the poll is taken off below
			s.poll, s.done = nil, nil
			r.mu.Unlock()
			return true
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ccPluginServes: the node hands the owner's messages to the plugin in claude c, a claude outside Persona's terminals
// (a variable so the live test can serve the stream-json claude it starts, which an SDK's entrypoint names).
var ccPluginServes = func(n *Node, c claudeProc) bool { return n.outsideSource(c) != "" }

// ccPluginStatus is one session in claude-plugin.status.
type ccPluginStatus struct {
	SessionID     string    `json:"session_id"`
	PID           int       `json:"pid"`
	PluginVersion string    `json:"plugin_version"`
	EngineVersion string    `json:"engine_version"`
	Polling       bool      `json:"polling"`
	SeenAt        time.Time `json:"seen_at"`
}

// status lists the sessions whose plugin polled, those whose claude still runs (live: by session, its pid).
func (r *ccPluginRegistry) status(live map[string]int) []ccPluginStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []ccPluginStatus{}
	for id, s := range r.sessions {
		if live[id] != s.pid {
			delete(r.sessions, id) // its claude exited, or went on in another session (a /clear polls under the new id)
			continue
		}
		out = append(out, ccPluginStatus{SessionID: id, PID: s.pid, PluginVersion: s.version, EngineVersion: s.engine,
			Polling: s.poll != nil, SeenAt: s.seen.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// ccPluginInput: session id, open in claude pid, has the plugin (its session row's capabilities.plugin_input).
func (n *Node) ccPluginInput(id string, pid int) bool {
	return n.ccPlugins.session(id, pid) != nil
}

// ccPluginSettingsCapable: session id, open in claude pid, has a polling plugin that supports session-scoped
// model / effort switching.
func (n *Node) ccPluginSettingsCapable(id string, pid int) bool {
	return n.ccPlugins.settingsCapable(id, pid)
}

// ccPluginPermissionsCapable: session id, open in claude pid, has a polling plugin authorized for permission bridging.
func (n *Node) ccPluginPermissionsCapable(id string, pid int) bool {
	return n.ccPlugins.permissionsCapable(id, pid)
}

// ccPluginClassicPassedOver: session id's plugin saw Claude Code pass classic hooks over it.
func (n *Node) ccPluginClassicPassedOver(id string, pid int) bool {
	return n.ccPlugins.classicPassedOver(id, pid)
}

// queueCCPluginSettings queues session-scoped model / effort overrides for session id (e.g. a newly seeded or resting
// desktop session whose claude process has not polled yet).
func (n *Node) queueCCPluginSettings(id, model, effort string) {
	n.ccPlugins.queueSettings(id, model, effort)
}

// ccPluginSetSettings sends a session-scoped model / effort switch to a live outside Claude session via Persona's
// plugin, or queues it for a resting Claude Desktop session that will open in the app on next send.
func (n *Node) ccPluginSetSettings(ctx context.Context, id, model, effort string) (handled bool, err error) {
	if c, ok := n.outsideClaude(id); ok && outsidePane(c) == "" {
		if !n.ccPlugins.settingsCapable(id, c.pid) {
			return false, nil
		}
		upd := ccPluginSettings{
			ID:     strings.ReplaceAll(newUUID(), "-", ""),
			Model:  model,
			Effort: effort,
		}
		acks := n.ccPlugins.expect(id, upd.ID)
		defer n.ccPlugins.settle(id, upd.ID)
		if !n.ccPlugins.handOver(ctx, id, c.pid, ccPluginDelivery{Messages: []ccPluginMessage{}, Settings: &upd}, ccPluginPickupWait) {
			// Poll is momentarily reconnecting: queue it for the next poll rather than dropping the user's pick.
			n.ccPlugins.queueSettings(id, model, effort)
			return true, nil
		}
		timer := time.NewTimer(ccPluginSubmitWait)
		defer timer.Stop()
		select {
		case a := <-acks:
			if !a.Submitted {
				return true, fmt.Errorf("claude plugin rejected settings (%s)", a.Reason)
			}
			return true, nil
		case <-timer.C:
			return true, nil
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	if claudeDesktopOpens && readClaudeDesktopIndex(n.home).about[id].local != "" && n.desktopPluginReady() {
		n.ccPlugins.queueSettings(id, model, effort)
		return true, nil
	}
	return false, nil
}

// desktopPluginReady reports whether Persona's current Claude plugin is installed so a resting Claude Desktop session
// will receive queued settings once opened.
func (n *Node) desktopPluginReady() bool {
	st := n.claudePluginStatus()
	return st != nil && st.Version != "" && st.Version == st.Bundled
}

// ccPluginInstalledFor: Persona's plugin is installed and on in Claude Code config directory dir, so a claude started
// there since has it loaded (a variable: tests stand in for the installer's status).
var ccPluginInstalledFor = func(n *Node, dir string) bool {
	st := n.claudePluginStatus()
	if st == nil {
		return false
	}
	for _, d := range st.ConfigDirs {
		if (d.State == proto.ClaudePluginInstalled || d.State == proto.ClaudePluginOutdated) && sameDir(d.Path, dir) {
			return true
		}
	}
	return false
}

// ccPluginRejoined: no plugin of session id's claude c has polled this node yet, the node started a moment ago, and c
// can have one (ccPluginRejoinWindow); true once its first poll came within ccPluginRejoinWait.
func (n *Node) ccPluginRejoined(ctx context.Context, c claudeProc, id string) bool {
	if time.Since(n.bootAt) > ccPluginRejoinWindow || c.dir == "" || !ccPluginInstalledFor(n, filepath.Dir(c.dir)) {
		return false
	}
	deadline := time.Now().Add(ccPluginRejoinWait)
	for n.ccPlugins.session(id, c.pid) == nil {
		if time.Now().After(deadline) {
			log.Printf("send %s: claude %d can have Persona's plugin and none polled within %s of this node starting", id, c.pid, ccPluginRejoinWait)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return true
}

// withOwnerRoute adds how the owner's message went (delivered_as, relay_reason) to a send's coded error, as a send that
// went says them in its answer: a message the inbox read and the session did not take still says why it went through
// the inbox (an outdated plugin, none), so the app can say what would have delivered it. Other errors are as they are.
func withOwnerRoute(err error, owner *ownerDelivery) error {
	var re *rpc.Error
	if owner == nil || owner.as == "" || !errors.As(err, &re) || re.Code == "" {
		return err
	}
	data := map[string]any{}
	if len(re.Data) > 0 && json.Unmarshal(re.Data, &data) != nil {
		return err
	}
	data["delivered_as"] = owner.as
	if owner.reason != "" {
		data["relay_reason"] = owner.reason
	}
	b, merr := json.Marshal(data)
	if merr != nil {
		return err
	}
	out := *re
	out.Data = b
	return &out
}

// ccPluginStatusNow is claude-plugin.status's answer.
func (n *Node) ccPluginStatusNow() map[string]any {
	live := map[string]int{}
	for _, c := range n.liveClaudes() {
		live[c.SessionID] = c.pid
	}
	return map[string]any{"protocol": ccPluginProtocol, "sessions": n.ccPlugins.status(live)}
}

// isHTTPRequestLine: the socket's first line is an HTTP request's (the plugin's), not a JSON request.
func isHTTPRequestLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("POST /")) || bytes.HasPrefix(line, []byte("GET /"))
}

// serveCCPluginHTTP answers one HTTP request on the node socket: the plugin's. first is the request's start, already
// read; rest the connection's reader after it.
func (n *Node) serveCCPluginHTTP(ctx context.Context, c net.Conn, first []byte, rest *bufio.Reader) {
	// A request is small on /poll and /ack; /permission may carry large ExitPlanMode plans or Write/Edit tool_input.
	bodyLimit, reqLimit := int64(ccPluginBodyMost), int64(ccPluginRequestMost)
	if bytes.HasPrefix(first, []byte("POST "+ccPluginPrefix+"permission ")) {
		bodyLimit, reqLimit = ccPluginPermBodyMost, ccPluginPermRequestMost
	}
	br := bufio.NewReader(io.LimitReader(io.MultiReader(bytes.NewReader(first), rest), reqLimit))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, bodyLimit+1))
	if err != nil || int64(len(body)) > bodyLimit {
		writeCCPluginHTTP(c, http.StatusRequestEntityTooLarge, map[string]any{"error": "too_large"})
		return
	}
	// The plugin going away (its session ended, a reload) ends a poll it left waiting.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = br.ReadByte()
		cancel()
	}()
	status, out := n.ccPluginRequest(ctx, c, req.Method, req.URL.Path, body)
	writeCCPluginHTTP(c, status, out)
}

// ccPluginRequest is one of the plugin's requests, answered with a status and a JSON body.
func (n *Node) ccPluginRequest(ctx context.Context, c net.Conn, method, path string, body []byte) (int, any) {
	what, ok := strings.CutPrefix(path, ccPluginPrefix)
	if !ok || method != http.MethodPost || (what != "poll" && what != "ack" && what != "permission" && what != "permission-done") {
		return http.StatusNotFound, map[string]any{"error": "unknown"}
	}
	var in struct {
		SessionID         string         `json:"session_id"`
		PluginVersion     string         `json:"plugin_version"`
		EngineVersion     string         `json:"engine_version"`
		Capabilities      []string       `json:"capabilities"`
		ClassicPassedOver bool           `json:"classic_passed_over"`
		ObservedEffort    ccPluginEffort `json:"observed_effort"`
		WaitMS            int64          `json:"wait_ms"`
		ID                string         `json:"id"`
		Submitted         bool           `json:"submitted"`
		Reason            string         `json:"reason"`
	}
	if json.Unmarshal(body, &in) != nil || !validID(in.SessionID) {
		return http.StatusBadRequest, map[string]any{"error": "invalid"}
	}
	pid, err := ccPluginPeerPID(c)
	if err != nil || pid <= 0 {
		// A system that does not say who is at the other end: nothing is handed to anyone.
		return http.StatusForbidden, map[string]any{"error": "peer_unknown", "retry_ms": ccPluginOtherRetry.Milliseconds()}
	}
	claude, ok := n.liveClaudeOf(pid)
	if !ok || claude.SessionID != in.SessionID {
		return http.StatusForbidden, map[string]any{"error": "not_this_session", "retry_ms": ccPluginStrangerRetry.Milliseconds()}
	}
	switch what {
	case "ack":
		n.ccPlugins.acked(in.SessionID, pid, in.ID, ccPluginAck{Submitted: in.Submitted, Reason: clipReason(in.Reason)})
		return http.StatusOK, map[string]any{"ok": true}
	case "permission":
		return n.ccPluginPermission(ctx, pid, claude, body)
	case "permission-done":
		return n.ccPluginPermissionDone(pid, claude, body)
	}
	if !ccPluginServes(n, claude) {
		// A pane's claude takes typing, and the assistant's or an SDK's is driven by its own host: none of them gets
		// messages this way.
		return http.StatusOK, map[string]any{"messages": []ccPluginMessage{}, "retry_ms": ccPluginOtherRetry.Milliseconds()}
	}
	wait := time.Duration(in.WaitMS) * time.Millisecond
	if wait <= 0 || wait > ccPluginPollMost {
		wait = ccPluginPollMost
	}
	caps := ccPluginCaps{
		busyInput:   slices.Contains(in.Capabilities, "busy_input"),
		interrupt:   slices.Contains(in.Capabilities, "interrupt"),
		settings:    slices.Contains(in.Capabilities, "settings"),
		permissions: slices.Contains(in.Capabilities, "permissions"),
	}
	allowPerms := caps.permissions && n.ccPluginServesPermissions(claude) && !in.ClassicPassedOver
	effort := in.ObservedEffort
	if !desktopEntrypoint(claude.Entrypoint) {
		effort = ccPluginEffort{}
	} else if effort.Level != effortAuto && !slices.Contains(proto.EffortLevels, effort.Level) {
		effort.Level = ""
	}
	poll, done, immediate, permAuthChanged, changed := n.ccPlugins.polled(in.SessionID, pid, clipReason(in.PluginVersion), clipReason(in.EngineVersion), caps, allowPerms, in.ClassicPassedOver, effort)
	if permAuthChanged && in.ClassicPassedOver {
		n.ccPlugins.cancelPermissions(in.SessionID, pid)
	}
	if changed {
		n.notifySessionsSoon()
	}
	if immediate != nil {
		return http.StatusOK, ccPluginDelivery{Messages: []ccPluginMessage{}, Settings: immediate, Permissions: allowPerms}
	}
	if permAuthChanged {
		return http.StatusOK, ccPluginDelivery{Messages: []ccPluginMessage{}, Permissions: allowPerms}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case d := <-poll:
		d.Permissions = n.ccPlugins.permissionsAuthorized(in.SessionID, pid)
		return http.StatusOK, d
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
	if !n.ccPlugins.pollEnded(in.SessionID, poll) {
		select {
		case d := <-poll: // handed over as the poll ended
			d.Permissions = n.ccPlugins.permissionsAuthorized(in.SessionID, pid)
			return http.StatusOK, d
		default:
		}
	}
	return http.StatusOK, ccPluginDelivery{Messages: []ccPluginMessage{}, Permissions: n.ccPlugins.permissionsAuthorized(in.SessionID, pid)}
}

// clipReason keeps a short word from the plugin (a version, a reason) short.
func clipReason(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// writeCCPluginHTTP writes one HTTP response with a JSON body and closes the exchange.
func writeCCPluginHTTP(c net.Conn, status int, body any) {
	b, _ := json.Marshal(body)
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(b), b)
}

// liveClaudeOf is the running claude whose process is pid.
func (n *Node) liveClaudeOf(pid int) (claudeProc, bool) {
	for _, c := range n.liveClaudes() {
		if c.pid == pid {
			return c, true
		}
	}
	return claudeProc{}, false
}

// ownerInputExact: params carry the owner mark exactly as the devices manager writes it, "owner_input": true, and no
// other key that encoding/json would also read as it (OWNER_INPUT, Owner_Input…: its field match ignores case). The
// devices manager strips every such key a caller sent (devices/owner_input.go); this keeps a node from honouring a
// variant that reached it some other way (#9607).
func ownerInputExact(params json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(params))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	owner := false
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		var value json.RawMessage
		if err != nil || !ok || decoder.Decode(&value) != nil {
			return false
		}
		if strings.EqualFold(key, "owner_input") {
			if owner || key != "owner_input" || string(value) != "true" {
				return false
			}
			owner = true
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false
	}
	_, err := decoder.Token()
	return owner && err == io.EOF
}

// ownerDelivery is where the send says how the owner's message went in (a tmux pane, the plugin or the inbox), for
// session.send's answer.
type ownerDelivery struct{ as, reason string }

type ownerDeliveryKey struct{}

// withOwnerDelivery marks a send as the owner's own message (owner_input): an outside Claude's tmux pane takes it as
// terminal input; without a pane, sendOutside may hand it to the plugin.
func withOwnerDelivery(ctx context.Context) (context.Context, *ownerDelivery) {
	d := new(ownerDelivery)
	return context.WithValue(ctx, ownerDeliveryKey{}, d), d
}

// ownerDeliveryOf is the send's ownerDelivery, nil for a message that is not the owner's own.
func ownerDeliveryOf(ctx context.Context) *ownerDelivery {
	d, _ := ctx.Value(ownerDeliveryKey{}).(*ownerDelivery)
	return d
}

// ccPluginSend hands text, the owner's own message, to the plugin in claude c, which has session id open. true when the
// session took it as the person's input: the transcript shows the prompt (or its queue entry), or the plugin said it
// submitted it. false with why it did not, nothing submitted (the caller sends it through the inbox).
//
// At rest the prompt starts a turn. While a turn runs, and while the session waits on a dialog (a permission prompt, a
// plan to approve: sendOutside sends the answer to a question at priority now, never here), Claude Code writes the
// prompt's queue entry at once and runs it as the person's words when the turn ends; a session in bypassPermissions
// mode does not hold it, as it can hold an inbox message (measured with Claude Code 2.1.329, 2026-11-22: docs/
// integrations/claude-integration.md "Owner input through the Persona plugin"). Only a plugin that says busy_input
// gets it then: an older one waits on its submit until that turn, polling nothing meanwhile.
func (n *Node) ccPluginSend(ctx context.Context, c claudeProc, id, text string) (bool, string) {
	if n.ccPlugins.session(id, c.pid) == nil && !n.ccPluginRejoined(ctx, c, id) {
		return false, relayNoPlugin
	}
	idle := claudeBgStatus[c.Status] == proto.StatusIdle
	working := c.Status == "busy" || c.Status == "waiting"
	switch {
	case idle:
	case working && n.ccPlugins.busyInputCapable(id, c.pid):
	case working:
		return false, relayOutdated
	default:
		return false, relayBusy
	}
	path := n.transcriptPath(id)
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	m := ccPluginMessage{ID: strings.ReplaceAll(newUUID(), "-", ""), Text: text, DeadlineMS: time.Now().Add(ccPluginSubmitWait).UnixMilli()}
	acks := n.ccPlugins.expect(id, m.ID)
	defer n.ccPlugins.settle(id, m.ID)
	if !n.ccPlugins.handOver(ctx, id, c.pid, ccPluginDelivery{Messages: []ccPluginMessage{m}}, ccPluginPickupWait) {
		return false, relayUnavailable
	}
	needle := ccPluginNeedle(text)
	deadline := time.Now().Add(ccPluginConfirmWait)
	submitted := false
	for {
		if path == "" {
			path = n.transcriptPath(id)
		}
		if path != "" && ccPluginSeen(path, offset, needle) {
			return true, ""
		}
		if submitted && !idle {
			return true, "" // queued via $.prompt.submit behind the running turn or dialog; the ack confirmed it
		}
		if time.Now().After(deadline) {
			if submitted {
				return true, "" // its turn started with it (the plugin's submit resolves then); the line is late
			}
			log.Printf("send %s: the plugin took it and nothing showed it went in within %s", id, ccPluginConfirmWait)
			return false, relayUnconfirmed
		}
		select {
		case a := <-acks:
			if !a.Submitted {
				log.Printf("send %s: the plugin did not submit it (%s)", id, a.Reason)
				return false, relayRefused
			}
			submitted = true
		case <-ctx.Done():
			return false, relayUnconfirmed
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// ccPluginInterrupt asks Persona's plugin in claude c to abort the running main-loop turn via $.turn.abort.
// Returns true when the plugin confirmed the abort (or the turn settled while the abort was in flight); false when no
// interrupt-capable plugin answered or it declined, leaving interruptOutside to fall back to the peer inbox.
func (n *Node) ccPluginInterrupt(ctx context.Context, c claudeProc, id string) bool {
	if c.Status != "busy" || !n.ccPlugins.interruptCapable(id, c.pid) {
		return false
	}
	req := ccPluginInterrupt{
		ID:         strings.ReplaceAll(newUUID(), "-", ""),
		DeadlineMS: time.Now().Add(ccPluginSubmitWait).UnixMilli(),
	}
	acks := n.ccPlugins.expect(id, req.ID)
	defer n.ccPlugins.settle(id, req.ID)
	if !n.ccPlugins.handOver(ctx, id, c.pid, ccPluginDelivery{Messages: []ccPluginMessage{}, Interrupt: &req}, ccPluginPickupWait) {
		return false
	}
	deadline := time.Now().Add(ccPluginSubmitWait)
	for {
		if now, ok := n.outsideClaude(id); !ok || now.Status != "busy" {
			log.Printf("interrupt %s: claude %d settled after Persona's plugin took the stop", id, c.pid)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case a := <-acks:
			if a.Submitted {
				log.Printf("interrupt %s: claude %d aborted its turn through Persona's plugin", id, c.pid)
				return true
			}
			log.Printf("interrupt %s: the plugin did not abort (%s); falling back to inbox", id, a.Reason)
			return false
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ccPluginNeedle is the first words of text: at most 40 runes, to the end of its first line.
func ccPluginNeedle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	r := []rune(strings.TrimSpace(line))
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// ccPluginSeen: a line past offset is the plugin's prompt with needle in it (a user line Persona's plugin submitted as
// the person's words), or the queue entry Claude Code writes when it takes it.
func ccPluginSeen(path string, offset int64, needle string) bool {
	if needle == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		l, err := peerLine(br)
		if ccPluginLineSeen(l, needle) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// ccPluginName is the plugin's name, as Claude Code writes it in a prompt's origin.
const ccPluginName = "persona"

// ccPluginLineSeen: l is the plugin's prompt with needle, or a queue entry whose text has it.
func ccPluginLineSeen(l []byte, needle string) bool {
	if !bytes.Contains(l, []byte(`"queue-operation"`)) && !bytes.Contains(l, []byte(`"plugin"`)) {
		return false
	}
	var e struct {
		Type      string `json:"type"`
		Operation string `json:"operation"`
		Content   string `json:"content"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Origin *struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			AsUser bool   `json:"asUser"`
		} `json:"origin"`
	}
	if json.Unmarshal(l, &e) != nil {
		return false
	}
	switch {
	case e.Type == "queue-operation":
		return e.Operation == "enqueue" && strings.Contains(e.Content, needle)
	case e.Type == "user" && e.Origin != nil && e.Origin.Kind == "plugin" && e.Origin.Name == ccPluginName && e.Origin.AsUser:
		var said string
		_ = json.Unmarshal(e.Message.Content, &said)
		return strings.Contains(said, needle)
	}
	return false
}
