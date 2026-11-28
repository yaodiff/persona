package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// errCredentialRevoked is returned when a push registration, prefs update, or web push subscription
// races with or follows revocation of its authenticating credential.
var errCredentialRevoked = errors.New("credential revoked")

// Device is an iPhone, a Mac or a Watch that wants push notifications.
type Device = db.Device

// kindMac is a Mac app's device (PushRegistrar on macOS): it gets alerts, not the phone's wake pushes.
const kindMac = "mac"

// apnsConfig lives in ~/.persona/apns.json; without it the hub pushes through the relay each phone registered with
// (docs/operations/push-relay.md), and with neither push is off.
type apnsConfig = apns.Config

type Pusher struct {
	dir     string
	store   Store
	quiesce *writeQuiescer
	logf    func(string, ...any)
	traffic func(trafficKey, int64, time.Duration)
	// direct: the hub's own APNs key. Set, every push goes straight to APNs as before; nil, through the relay.
	direct *apns.Client
	// relay posts to the devices' relays over connections kept between pushes (apns.NewTransport, #4800).
	relay *http.Client
	// privateOnly is office network mode (private_network.go): no relay is ever posted to, the hub's own key pushes only
	// sealed payloads with the relay's content-free envelope (sealedDirect), and Live Activities, which iOS reads in the
	// clear, are not pushed.
	privateOnly bool
	// retryWaits: how long a push the relay did not take for a reason that may pass waits before each further try
	// (postRelayRetrying); tests shorten them.
	retryWaits []time.Duration
	now        func() time.Time
	// desktopOnline: a desktop app is connected (Hub.desktopOnline), for "Phone Muted".
	desktopOnline func() bool
	// hubID is this hub's hub_id: every push says which hub sent it, so the app knows the group (plan 2.6 "Pushes").
	hubID func() string
	// userLang is the user's language (Hub.userLang): pushes to a device that registered without one read in it.
	userLang func() i18n.Lang
	// userLoc is the user's time zone (Hub.userLocation): quiet hours without a zone of their own read in it (quiethours.go).
	userLoc func() *time.Location
	// attention is the attention mode the assistant last reported (Hub.attentionMode, push_attention.go); nil: normal.
	attention func() *assistantwire.Attention
	// desktopOpen: the desktop app with this push device id is connected (Hub.desktopOpen); it posts its own
	// notifications for agents then (the Mac's MacAttentionWatcher).
	desktopOpen func(device string) bool
	// web pushes to the browsers that turned Web Push on (webpush.go, #7459).
	web *webPusher
	// devicesChanged runs after a device was registered, forgotten or reloaded, or its settings changed (Hub:
	// fleet.clients, whether a phone would be pushed an MCP form). It may run under the Pusher's locks: it must not block.
	devicesChanged func()
	// revoked reports whether a device credential has been revoked (Hub.isDeviceRevoked).
	// Must acquire only leaf locks (h.devtok.mu) because callers may hold saveMu or mu.
	revoked func(credential string) bool

	// sends counts the APNs and relay posts still on their way (sendBuiltWith, sendLiveActivity): each runs in its own
	// goroutine and logs when it ends, so whoever closes what they post to (a test's fake APNs) waits for them first.
	sends sync.WaitGroup

	mu sync.Mutex
	// sendMu lets maintenance skip an active transport instead of retiring a
	// target whose acceptance is still in flight. Reads remain concurrent.
	sendMu  sync.RWMutex
	devices map[string]Device
	// replaced: per device, the grant the hub renewed away from (renewGrant). The app keeps reporting the one it
	// cached; that one must not undo the renewal.
	replaced map[string]string
	// failed: per device, the last push that did not go (routeOf); cleared by the next that does.
	failed map[string]pushFailure
	// acceptedDirty is flushed by maintenance, not once per push. Accepted means
	// APNs/relay acceptance, never proof of OS delivery or a client wake.
	acceptedDirty map[string]bool
	// withdrawals: where each card's push went, to take it back when the card is over (push_withdraw.go).
	withdrawals withdrawals
	// todoPushes: when each to-do's push to the owner about one reason last rang (push_todo_dedup.go).
	todoPushes todoPushBook
	saveMu     sync.Mutex // one device row write at a time, always the latest state (register, setPrefsBy, renewal, forget, maintenance)
	// storeTimeout bounds each device row write, which never runs under mu; tests shorten it.
	storeTimeout time.Duration
	// prefsEdit holds one change of a device's settings from its read to its write (editPrefs); taken before saveMu
	// and mu, never by a push.
	prefsEdit sync.Mutex

	// gradingMu guards the low-distraction mode state (push_grading.go): the cached switch, per-manager push times for
	// repeat suppression, the held-push list and the delivered-push counts. gradingSwitchMu serializes owner switch
	// writes across the database and in-memory cache so concurrent toggles never invert cached state.
	gradingSwitchMu   sync.Mutex
	gradingMu         sync.Mutex
	gradingCached     *bool
	gradingCachedAt   time.Time
	gradingRefreshing bool
	gradingVersion    uint64 // bumped by setPushGrading, so a refresh that read before it does not undo it
	gradingLastAlert  map[string]map[string]time.Time
	intercepts        []heldPushRecord
	sent              []pushSent
	heldDirty         bool // the held-push list changed since the last save
	// sentPending are the delivered pushes not yet appended to the store's record (pushSentStore); sentPrunedAt is
	// when its last sweep ran.
	sentPending  []pushSent
	sentPrunedAt time.Time
	// The hub.push_sent setting a build from before the table reads (mirrorSent): sentMirrorable once this hub read
	// both at start, sentMirrorDue while it holds pushes the setting lacks, sentMirroredAt its last write.
	sentMirrorable bool
	sentMirrorDue  bool
	sentMirroredAt time.Time
	sentMirrorMu   sync.Mutex
	gradingWriting bool // the one saver goroutine is running

	// Cached versioned push grading configuration snapshot (hub.push_grading and
	// hub.push_grading_layer), resolved independently per manager.
	gradingEffective *pushGradingEffectiveCache

	maintenanceWake     chan struct{}
	maintenanceDeferred bool
	pendingForgets      []Device
}

// deviceStoreTimeout: how long a device row write may take before it gives up (the pool has no statement_timeout).
const deviceStoreTimeout = 5 * time.Second

func newPusher(dir string, store Store, logf func(string, ...any)) *Pusher {
	p := &Pusher{dir: dir, store: store, logf: logf, devices: map[string]Device{}, replaced: map[string]string{}, failed: map[string]pushFailure{}, withdrawals: newWithdrawals(), relay: &http.Client{Timeout: 20 * time.Second, Transport: apns.NewTransport()},
		retryWaits: []time.Duration{time.Second, 4 * time.Second}, now: time.Now, storeTimeout: deviceStoreTimeout,
		// Web Push in memory here; the hub gives it the store when it starts (hub.go): a test's store may have no settings.
		web: newWebPusher(nil, logf)}
	p.gradingEffective = newPushGradingEffectiveCache(gradingConfigTTL)
	p.gradingEffective.now = func() time.Time { return p.now() }
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), deviceStoreTimeout)
		if _, err := store.ReconcilePushDevices(ctx, p.now().UTC(), pushDeviceMaxIdle); err != nil {
			logf("push registration startup reconciliation: %v", err)
		}
		if list, err := store.Devices(ctx); err == nil {
			for _, d := range list {
				p.devices[d.DeviceID] = d
			}
		} else {
			logf("load devices: %v", err)
		}
		cancel()
	}
	p.loadPushGradingState() // the grading switch and held pushes survive restarts (hub_settings), whether or not APNs is configured
	b, err := os.ReadFile(filepath.Join(dir, "apns.json"))
	if err != nil {
		logf("push: no %s, pushing through each phone's relay (docs/operations/push-relay.md)", filepath.Join(dir, "apns.json"))
		return p
	}
	var cfg apnsConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		logf("push: apns.json: %v; pushing through the relay", err)
		return p
	}
	c, err := apns.New(cfg)
	if err != nil {
		logf("push: apns key: %v; pushing through the relay", err)
		return p
	}
	p.direct = c
	logf("push enabled (key %s, topic %s)", cfg.KeyID, cfg.Topic)
	return p
}

// validRelayURL: an https URL (the phone's build config; the hub sends the grant and Live Activity contents there),
// or http to this machine (the end-to-end script's local relay).
func validRelayURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.RawQuery != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		ip := net.ParseIP(u.Hostname())
		return u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}

func (p *Pusher) isRevoked(credential string) bool {
	return p != nil && credential != "" && p.revoked != nil && p.revoked(credential)
}

func (p *Pusher) register(d Device) error {
	return p.registerContext(context.Background(), d)
}

func (p *Pusher) registerContext(ctx context.Context, d Device) error {
	d.UpdatedAt = p.now().UTC()
	d.RegisteredAt = d.UpdatedAt
	d.Registering = true
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	if p.isRevoked(d.Credential) || p.isRevoked(d.CredentialID) {
		return errCredentialRevoked
	}
	d.RelayURL = strings.TrimRight(d.RelayURL, "/")
	if d.RelayURL != "" && !validRelayURL(d.RelayURL) {
		p.logf("device %s: ignoring relay url %q", d.Name, d.RelayURL)
		d.RelayURL, d.RelayGrant = "", ""
	}
	if d.RelayGrant != "" {
		d.RelayError = ""
	}
	d.RelayError = clipRelayError(d.RelayError)
	cleanVoIP(&d)
	d.Features = cleanFeatures(d.Features)
	p.mu.Lock()
	cur := p.devices[d.DeviceID]
	d.Prefs = cur.Prefs // registration carries the token, not the settings
	if len(d.Prefs) == 0 {
		var latest time.Time
		for _, prior := range p.devices {
			if db.SamePushInstallation(prior, d) && len(prior.Prefs) > 0 && (latest.IsZero() || prior.RegisteredAt.After(latest)) {
				d.Prefs, latest = prior.Prefs, prior.RegisteredAt
			}
		}
	}
	d.LastPushAt = cur.LastPushAt
	if d.RelayGrant != "" && d.RelayGrant == p.replaced[d.DeviceID] && d.RelayURL == cur.RelayURL && d.APNSToken == cur.APNSToken {
		d.RelayGrant = cur.RelayGrant // the app's cached grant, which the hub already renewed
	}
	p.mu.Unlock()
	// Publish only after the ownership check and duplicate retirement commit.
	// A failed enrollment must not expose an unpersisted APNs target to sends.
	if err := p.writeDeviceIn(ctx, d); err != nil {
		if p.isRevoked(d.Credential) || p.isRevoked(d.CredentialID) {
			return errCredentialRevoked
		}
		return err
	}
	if p.isRevoked(d.Credential) || p.isRevoked(d.CredentialID) {
		removed := false
		p.mu.Lock()
		if existing, ok := p.devices[d.DeviceID]; ok {
			// When p.store != nil, writeDeviceIn already committed d.DeviceID under d's credential,
			// so stay consistent with p.deleteRow(d); without a store, remove only if owned by d's credential.
			owned := p.store != nil ||
				(d.Credential != "" && (existing.Credential == d.Credential || existing.CredentialID == d.Credential)) ||
				(d.CredentialID != "" && (existing.Credential == d.CredentialID || existing.CredentialID == d.CredentialID))
			if owned {
				delete(p.devices, d.DeviceID)
				delete(p.failed, d.DeviceID)
				delete(p.replaced, d.DeviceID)
				delete(p.acceptedDirty, d.DeviceID)
				removed = true
			}
		}
		p.mu.Unlock()
		if removed {
			p.changed()
		}
		p.deleteRow(d)
		return errCredentialRevoked
	}
	d.Registering = false
	defer p.changed() // after mu is let go
	p.mu.Lock()
	defer p.mu.Unlock()
	if d.APNSToken != cur.APNSToken || d.RelayURL != cur.RelayURL {
		delete(p.failed, d.DeviceID)
	}
	for id, old := range p.devices {
		if id != d.DeviceID && db.SamePushInstallation(old, d) {
			delete(p.devices, id)
			delete(p.failed, id)
			delete(p.replaced, id)
			delete(p.acceptedDirty, id)
		}
	}
	if latest := p.devices[d.DeviceID]; latest.LastPushAt.After(d.LastPushAt) {
		d.LastPushAt = latest.LastPushAt
	}
	p.devices[d.DeviceID] = d
	return nil
}

// writeDevice requires saveMu and never publishes a failed write.
func (p *Pusher) writeDevice(d Device) error {
	return p.writeDeviceIn(context.Background(), d)
}

func (p *Pusher) writeDeviceIn(parent context.Context, d Device) error {
	if p.store == nil {
		return nil
	}
	if !quiesceHeld(parent) {
		if !p.quiesce.begin() {
			return i18n.Err("hub-selfupdate.err_quiesced")
		}
		defer p.quiesce.end()
	}
	ctx, cancel := context.WithTimeout(parent, p.storeTimeout)
	defer cancel()
	if err := p.store.UpsertDevice(ctx, d); err != nil {
		p.logf("save device: %v", err)
		return err
	}
	return nil
}

// relayed: this device can be pushed through its relay (the hub has no key of its own).
func (p *Pusher) relayed(d Device) bool {
	return !p.privateOnly && p.direct == nil && d.RelayURL != "" && d.RelayGrant != "" && d.PushKey != ""
}

// reachable: a push to this device can go anywhere.
func (p *Pusher) reachable(d Device) bool {
	if p.direct != nil {
		return d.APNSToken != ""
	}
	return p.relayed(d)
}

// routed: a push to the device with this credential (or push registration id) can go somewhere. APNs keeps its alerts
// while it is away, so the hub keeps none of its WebSocket's for it (notifyring.go).
func (p *Pusher) routed(id string) bool {
	if p == nil || id == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.devices {
		if (d.DeviceID == id || d.CredentialID == id) && p.reachable(d) {
			return true
		}
	}
	return false
}

func (p *Pusher) currentPushTarget(d Device) bool {
	cur, ok := p.device(d.DeviceID)
	return ok && cur.APNSToken == d.APNSToken && cur.RelayURL == d.RelayURL && cur.PushKey == d.PushKey && cur.Topic == d.Topic && cur.APNSEnv == d.APNSEnv
}

func (p *Pusher) device(id string) (Device, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.devices[id]
	return d, ok
}

// signedOut forgets the devices a revoked credential registered (signing out that Mac, Settings › Devices): an app signed out gets
// no more pushes, its Live Activity included. Its notification settings go with it; pairing again starts from the
// defaults. Serialized under saveMu with registerContext and setPrefsBy so an in-flight write cannot resurrect a
// revoked credential and signedOut never deletes a newer credential's registration for the same device_id.
func (p *Pusher) signedOut(credential string) {
	if p == nil || credential == "" {
		return
	}
	p.saveMu.Lock()
	p.mu.Lock()
	var gone []Device
	for id, d := range p.devices {
		if d.Credential == credential || d.CredentialID == credential {
			gone = append(gone, d)
			delete(p.devices, id)
			delete(p.failed, id)
			delete(p.replaced, id)
			delete(p.acceptedDirty, id)
		}
	}
	p.mu.Unlock()
	if len(gone) > 0 {
		p.changed()
		for _, d := range gone {
			p.deleteRow(d)
			p.logf("device %s signed out: no more pushes", d.Name)
		}
	}
	p.saveMu.Unlock()
	p.web.signedOut(credential)
}

// webPusher is p's Web Push (nil for a nil Pusher).
func (p *Pusher) webPusher() *webPusher {
	if p == nil {
		return nil
	}
	return p.web
}

// forget drops a device, whatever token it has now (signedOut), with all of its push bookkeeping: a push accepted
// just before still marked dirty would have maintenance write a row for a device that is gone.
func (p *Pusher) forget(d Device) {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	delete(p.devices, d.DeviceID)
	delete(p.failed, d.DeviceID)
	delete(p.replaced, d.DeviceID)
	delete(p.acceptedDirty, d.DeviceID)
	p.mu.Unlock()
	p.changed()
	p.deleteRow(d)
}

// deleteRow deletes a forgotten device's row, bounded by storeTimeout. A delete that fails is logged: the row stays,
// so the device is loaded again on the next start and gets pushes until it is forgotten again.
func (p *Pusher) deleteRow(d Device) {
	if p.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
	defer cancel()
	if err := p.store.DeleteDevice(ctx, d.DeviceID); err != nil {
		p.logf("forget device %s: %v", d.Name, err)
	}
}

// forgetIfCurrent drops a device whose token APNs says is dead, only while that token (and relay) is still the one
// it has: a push's answer can come seconds after the app registered a new token (the relay retries, renews its
// grant), and that late answer must not delete the new registration and its settings. Under saveMu, so a save of a
// newer registration is not undone by the delete that follows it, or the other way round.
func (p *Pusher) forgetIfCurrent(d Device) {
	if !p.quiesce.beginOrDefer(func() {
		p.mu.Lock()
		p.pendingForgets = append(p.pendingForgets, d)
		p.mu.Unlock()
	}) {
		return
	}
	defer p.quiesce.end()
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	cur, ok := p.devices[d.DeviceID]
	still := ok && cur.APNSToken == d.APNSToken && cur.RelayURL == d.RelayURL
	p.mu.Unlock()
	if !still {
		p.logf("device %s: its old token is gone; kept, it has registered a new one", d.Name)
		return
	}
	if p.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		deleted, err := p.store.DeleteDeviceIfCurrent(ctx, d)
		if err != nil {
			p.logf("forget device %s: %v", d.Name, err)
		}
		if err == nil && !deleted {
			rows, readErr := p.store.Devices(ctx)
			if readErr == nil {
				p.mu.Lock()
				delete(p.devices, d.DeviceID)
				for _, row := range rows {
					if row.DeviceID == d.DeviceID {
						p.devices[d.DeviceID] = row
					}
				}
				p.mu.Unlock()
				p.changed()
			} else {
				p.logf("reload push registration after late gone: %v", readErr)
			}
			cancel()
			p.logf("device %s: its old token is gone; kept, persisted registration changed", d.Name)
			return
		}
		cancel()
	}
	p.mu.Lock()
	delete(p.devices, d.DeviceID)
	delete(p.failed, d.DeviceID)
	delete(p.replaced, d.DeviceID)
	delete(p.acceptedDirty, d.DeviceID)
	p.mu.Unlock()
	p.changed()
}

// changed tells devicesChanged (nil: nobody) the devices or their settings may differ now.
func (p *Pusher) changed() {
	if p.devicesChanged != nil {
		p.devicesChanged()
	}
}

// prefs is a device's notification settings (defaults when it never set any).
func (p *Pusher) prefs(id string) NotifyPrefs {
	p.mu.Lock()
	defer p.mu.Unlock()
	return devicePrefs(p.devices[id].Prefs)
}

// setPrefsBy stores a device's notification settings (also for a device that has not registered a token yet),
// written with a device token (`credential`): a device that has none recorded takes it, so revoking the token
// forgets a device only its prefs made (a browser's Settings › Notifications, which never registers a push token).
// The row is written under saveMu, not mu: a slow database holds this one save (up to storeTimeout), not every
// other device's settings and every push. The settings take effect once the row is written.
func (p *Pusher) setPrefsBy(id, credential string, prefs NotifyPrefs) error {
	raw, _ := json.Marshal(prefs)
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	if p.isRevoked(credential) {
		return errCredentialRevoked
	}
	if p.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		err := p.store.SetDevicePrefs(ctx, id, credential, raw)
		cancel()
		if err != nil {
			if p.isRevoked(credential) {
				return errCredentialRevoked
			}
			return err
		}
	}
	if p.isRevoked(credential) {
		p.mu.Lock()
		cur, ok := p.devices[id]
		owned := ok && (cur.Credential == credential || cur.CredentialID == credential)
		if owned {
			delete(p.devices, id)
			delete(p.failed, id)
			delete(p.replaced, id)
			delete(p.acceptedDirty, id)
		}
		p.mu.Unlock()
		if owned {
			p.changed()
		}
		deleteDB := owned
		if !ok && p.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
			rows, err := p.store.Devices(ctx)
			cancel()
			if err == nil {
				for _, row := range rows {
					if row.DeviceID == id && (row.Credential == credential || row.CredentialID == credential) {
						deleteDB = true
						break
					}
				}
			}
		}
		if deleteDB {
			p.deleteRow(Device{DeviceID: id, Name: id})
		}
		return errCredentialRevoked
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.devices[id]
	if !ok {
		d = Device{DeviceID: id}
	}
	if d.Credential == "" {
		d.Credential = credential
	}
	d.Prefs = raw
	p.devices[id] = d
	p.changed() // "Needs you" and Phone Muted decide whether an MCP form would be pushed
	return nil
}

// editPrefs changes a device's settings with edit and stores them as setPrefsBy does, holding prefsEdit from the read
// to the write: two partial changes at once (the apps send one switch per PUT) both take effect, the later one is not
// written over the settings as they were before the earlier was stored. before is the settings as they were; after
// as stored, normalized as devicePrefs reads them. An error from edit stores nothing.
func (p *Pusher) editPrefs(id, credential string, edit func(*NotifyPrefs) error) (before, after NotifyPrefs, err error) {
	p.prefsEdit.Lock()
	defer p.prefsEdit.Unlock()
	after = p.prefs(id)
	before = after
	if after.Look != nil {
		look := *after.Look
		before.Look = &look // a partial look changes the one after points to
	}
	if err := edit(&after); err != nil {
		return before, after, err
	}
	raw, _ := json.Marshal(after)
	after = devicePrefs(raw) // normalizes an unknown "done"
	return before, after, p.setPrefsBy(id, credential, after)
}

// mailTargets are the languages some device wants its mail translated into (Settings › Translation on, mail in scope).
func (p *Pusher) mailTargets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, d := range p.devices {
		t := devicePrefs(d.Prefs).Translate
		if t.Enabled && t.Mail && assistantwire.TranslateLanguages[t.Target] != "" && !seen[t.Target] {
			seen[t.Target] = true
			out = append(out, t.Target)
		}
	}
	sort.Strings(out)
	return out
}

// ready reports whether pushes can go anywhere (a device that can be reached: the hub's own key, or its relay).
func (p *Pusher) ready() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.devices {
		if p.reachable(d) {
			return true
		}
	}
	return false
}
