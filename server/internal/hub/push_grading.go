package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Push grading: when its switch is on, the hub delivers pushes by urgency instead of pushing
// everything. P0 still interrupts, P1 is held for the "Needs you" list / briefings, P2 stays silent, and the same
// related key may interrupt at most once in 12 hours. Every push the switch holds back is recorded with its reason,
// in memory and in hub_settings, behind a read-only API for extensions and the nightly review. With the switch off
// (the default) every push goes out exactly as before; the filter is a no-op. Every push that goes out is counted in
// memory and appended to the store's hub_push_sent table (pushSentStore), never by rewriting the whole record (#8257).
// A build from before that table reads only the whole-list hub.push_sent setting, so the hub keeps the setting for a
// switch back to one: written when the hub stops and at most hourly (mirrorSent), never per push.

const (
	gradingSwitchSettingKey = "hub.ln_mode"
	heldPushesSettingKey    = "hub.ln_intercepts"
	// pushSentSettingKey held the delivered pushes as one list before hub_push_sent, and is all a build from before
	// reads. loadSent adds its entries to the table (AdoptPushSentSetting) and mirrorSent writes the list back to it.
	pushSentSettingKey = "hub.push_sent"

	// gradingDedupWindow: how long the same related key stays quiet after it pushed once.
	gradingDedupWindow = 12 * time.Hour
	// gradingDedupMaxRetain matches the maximum valid versioned dedup_window (48h) so a shorter window on
	// another manager or a temporary configuration change cannot evict active dedup entries early.
	gradingDedupMaxRetain = 48 * time.Hour
	// gradingConfigTTL bounds how often one Pusher asks the database for the switch while pushes keep coming.
	gradingConfigTTL = 15 * time.Second
	// heldPushMax bounds how many held pushes are kept; the oldest go first.
	heldPushMax = 500
	// heldPushMaxAge: held pushes older than this are not worth showing any more.
	heldPushMaxAge       = 14 * 24 * time.Hour
	heldPushViewLimitMax = 200
	// pushSentMax bounds how many delivered pushes the daily counts remember; the oldest go first. Counts read this
	// full record, so they stay exact on a busy day where the 200-entry intercepts view would truncate.
	pushSentMax = 2000
	// pushSentPruneEvery: how often the writer sweeps hub_push_sent down to the age and count limits. Between sweeps
	// the table may hold a little more than the limits; reading it back applies them (loadSent).
	pushSentPruneEvery = time.Hour
	// pushSentMirrorEvery: how often, at most, a running hub writes the delivered pushes whole into the hub.push_sent
	// setting (mirrorSent). A stopping hub always writes it, so a switch back loses nothing; this bounds what a build
	// from before misses after a hub that was killed, and the setting's rewrites to 24 a day however many pushes go out.
	pushSentMirrorEvery = time.Hour
	// pushStatsDaysMax: how far back the read-only daily stats may go, matching the records' age.
	pushStatsDaysMax = 14
	// pushSaveRetryBackoff: how long the one settings writer waits before retrying a failed database write.
	pushSaveRetryBackoff = time.Second
	// pushInterceptBodyMaxRunes bounds the held-push body that is ever stored or returned: a summary one line long.
	// A held push's full body can carry the owner's decision questions, agent replies or notify_user text, and the
	// record outlives the push, so the hub keeps the title plus at most this many runes instead of the full text.
	pushInterceptBodyMaxRunes = 140
)

// Why a push was held back.
const (
	interceptReasonP1    = "ln_p1_held"   // P1 while push grading is on: kept for the list / briefing
	interceptReasonP2    = "ln_p2_silent" // P2 while push grading is on: silent
	interceptReasonDedup = "ln_dedup_12h" // the same related key pushed within the last 12 hours
)

// summaryBody keeps at most pushInterceptBodyMaxRunes runes of a held push's body, adding an ellipsis where it cut.
// The full text is never stored in the intercept record.
func summaryBody(s string) string {
	r := []rune(s)
	if len(r) <= pushInterceptBodyMaxRunes {
		return s
	}
	return string(r[:pushInterceptBodyMaxRunes-1]) + "…"
}

// heldPushRecord is one push the grading filter held back, as the read-only API returns it.
type heldPushRecord struct {
	At         time.Time `json:"at"`
	Level      string    `json:"level"`
	Reason     string    `json:"reason"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body,omitempty"`
	RelatedKey string    `json:"related_key,omitempty"`
	TodoID     string    `json:"todo_id,omitempty"`
	MachineID  string    `json:"machine_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	Manager    string    `json:"manager,omitempty"`
	// Also holds the titles of P1 conclusion pushes that merged into this one: several conclusion cards arriving in
	// the same minute become one intercepted entry instead of a stack.
	Also []string `json:"also,omitempty"`
}

// pushSent is one alert push the filter let through to the devices: one entry per logical push, however many devices
// actually received it (the same rule a held push is one intercept by). It backs the read-only daily delivery counts
// and is kept with the switch off as well as on, so the counts describe every push that reached the phone.
type pushSent struct {
	// ID names this push in the store (hub_push_sent.push_id), set once when it is recorded and kept when a failed
	// write is retried, so a write that landed but reported an error is not added again.
	ID         string    `json:"-"`
	At         time.Time `json:"at"`
	Level      string    `json:"level"`
	RelatedKey string    `json:"related_key,omitempty"`
	Manager    string    `json:"manager"`
}

// pushSentStore is where delivered pushes are appended (internal/db/push_sent.go, migration 0522). Optional like
// peerStore: a pusher whose store lacks it keeps the counts in memory only, and the many test stores need not implement
// it.
type pushSentStore interface {
	AddPushSent(ctx context.Context, entries []db.PushSent) error
	PushSentAfter(ctx context.Context, after time.Time, limit int) ([]db.PushSent, error)
	PrunePushSent(ctx context.Context, before time.Time, keep int) (int64, error)
	AdoptPushSentSetting(ctx context.Context, key string, after time.Time, keep int) (added int, behind bool, err error)
}

// Asserted here because sentLog finds out at run time: a method gone astray would stop the counts surviving a restart.
var _ pushSentStore = (*db.DB)(nil)

// sentLog is the store's delivered-push table, or nil.
func (p *Pusher) sentLog() pushSentStore {
	log, _ := p.store.(pushSentStore)
	return log
}

func sentRows(in []pushSent) []db.PushSent {
	out := make([]db.PushSent, len(in))
	for i, e := range in {
		out[i] = db.PushSent{ID: e.ID, At: e.At, Level: e.Level, RelatedKey: e.RelatedKey, Manager: e.Manager}
	}
	return out
}

// pushPolicy says how urgent one alert push is and what it belongs to. The zero value is P1 with no related key.
type pushPolicy struct {
	kind       string
	level      string
	relatedKey string
	todoID     string
	machineID  string
	sessionID  string
	title      string
	body       string
	manager    string
	extension  string
	// What an attention mode reads (push_attention.go): how long the owner needs for it (assistantwire.DepthDeep),
	// the track it comes from, whether it is a finished turn, and whether it is the hub's own alert.
	depth  string
	track  string
	done   bool
	system bool
	// cardID: the card the push stands for (assistantwire.Notification.CardID): put in the payload as interaction_id
	// and remembered per device, so the card's end withdraws the push.
	cardID string
	// derivedLevel is true when a NotifyPush notification omitted its level and the hub derived
	// it; asked is true when the conversation explicitly requested the push via notify_user
	// (so notify_user.main / notify_user.others apply only to explicit notify_user alerts when
	// grading or attention mode is active); mainConv records whether the notification originated
	// from the assistant's main conversation.
	derivedLevel bool
	asked        bool
	mainConv     bool
}

func (p pushPolicy) levelOr(level string) pushPolicy {
	if p.level == "" {
		p.level = level
	}
	return p
}

// levelFor derives the urgency of a manager notification that named no level itself, from its kind.
func levelFor(kind, given string) string {
	if given != "" {
		return given
	}
	switch kind {
	case assistantwire.NotifyInput, assistantwire.NotifyDecide, assistantwire.NotifyReply:
		// A card or a decision: something only the owner can answer now.
		return assistantwire.LevelP0
	case assistantwire.NotifyDone:
		// A turn that finished: background news.
		return assistantwire.LevelP2
	default:
		// notify_user and the assistant's own alerts: worth knowing, not urgent.
		return assistantwire.LevelP1
	}
}

// pushGradingOn says whether the switch is on. It never waits on the database: the switch is read once at startup
// (loadPushGradingState), written through setPushGrading, and refreshed in the background when the cached answer is older than
// gradingConfigTTL (another hub process may have changed it). A database that does not answer keeps the last known answer,
// off before the first successful read, so an outage never turns filtering on by itself.
func (p *Pusher) pushGradingOn() bool {
	if p == nil {
		return false
	}
	p.gradingMu.Lock()
	defer p.gradingMu.Unlock()
	if p.store != nil && !p.gradingRefreshing && (p.gradingCached == nil || p.now().Sub(p.gradingCachedAt) >= gradingConfigTTL) {
		p.gradingRefreshing = true
		go p.refreshPushGrading(p.gradingVersion)
	}
	return p.gradingCached != nil && *p.gradingCached
}

// refreshPushGrading reads the switch from hub_settings into the cache (pushGradingOn starts it, one at a time). version is
// gradingVersion when it started: a setPushGrading meanwhile wins over what this read saw.
func (p *Pusher) refreshPushGrading(version uint64) {
	enabled, ok := p.readPushGrading()
	p.gradingMu.Lock()
	defer p.gradingMu.Unlock()
	p.gradingRefreshing = false
	if version != p.gradingVersion {
		return
	}
	if ok {
		p.gradingCached, p.gradingCachedAt = &enabled, p.now()
	} else if p.gradingCached != nil {
		p.gradingCachedAt = p.now() // keep the last answer; try again after another TTL
	}
}

// readPushGrading reads the switch; ok false when the database did not answer. No row is off.
func (p *Pusher) readPushGrading() (enabled, ok bool) {
	if p.store == nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
	defer cancel()
	found, err := p.store.Setting(ctx, gradingSwitchSettingKey, &enabled)
	if err != nil {
		return false, false
	}
	return found && enabled, true
}

// setPushGrading writes the owner's switch (hub_settings) and refreshes the cache immediately.
// Writes respect write quiesce, serialize in-process toggles via gradingSwitchMu, and update
// both hub.push_grading.enabled (when present) and hub.ln_mode inside one advisory-locked DB
// transaction so both settings never diverge.
func (p *Pusher) setPushGrading(ctx context.Context, enabled bool) error {
	if p == nil || p.store == nil {
		return errors.New("push settings are not available")
	}
	if !quiesceHeld(ctx) {
		if !p.quiesce.begin() {
			return i18n.Err("hub-selfupdate.err_quiesced")
		}
		defer p.quiesce.end()
	}
	p.gradingSwitchMu.Lock()
	defer p.gradingSwitchMu.Unlock()

	wctx, cancel := context.WithTimeout(ctx, p.storeTimeout)
	defer cancel()
	if storeAPI, ok := p.store.(pushGradingStoreAPI); ok {
		if err := storeAPI.SetPushGradingSwitch(wctx, pushGradingBaseSettingKey, gradingSwitchSettingKey, enabled); err != nil {
			return err
		}
	} else {
		if err := p.store.PutSetting(wctx, gradingSwitchSettingKey, enabled); err != nil {
			return err
		}
	}
	p.gradingMu.Lock()
	p.gradingCached, p.gradingCachedAt = &enabled, p.now()
	p.gradingVersion++
	p.gradingMu.Unlock()
	p.invalidatePushGradingCache()
	return nil
}

func (p *Pusher) effectiveCache() *pushGradingEffectiveCache {
	if p == nil {
		return nil
	}
	p.gradingMu.Lock()
	c := p.gradingEffective
	if c == nil {
		c = newPushGradingEffectiveCache(gradingConfigTTL)
		c.now = func() time.Time { return p.now() }
		p.gradingEffective = c
	}
	p.gradingMu.Unlock()
	return c
}

func (p *Pusher) invalidatePushGradingCache() {
	if p == nil {
		return
	}
	p.gradingMu.Lock()
	c := p.gradingEffective
	p.gradingMu.Unlock()
	if c != nil {
		c.invalidate()
	}
}

// loadPushGradingSnapshotCached reads the versioned hub.push_grading and
// hub.push_grading_layer snapshot through the pusher's bounded TTL cache.
// supported is false when the store does not implement pushGradingStoreAPI.
func (p *Pusher) loadPushGradingSnapshotCached(ctx context.Context) (snapshot pushGradingSnapshot, supported bool, err error) {
	if p == nil || p.store == nil {
		return pushGradingSnapshot{}, false, nil
	}
	if _, ok := p.store.(pushGradingStoreAPI); !ok {
		return pushGradingSnapshot{}, false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, p.storeTimeout)
	defer cancel()
	snap, err := p.effectiveCache().loadSnapshot(rctx, p.store)
	if err != nil {
		return pushGradingSnapshot{}, true, err
	}
	return snap, true, nil
}

// resolveEffectiveFromSnapshot resolves both the full effective configuration
// (with an applicable layer) and the owner-only base configuration (without any
// extension layer) for manager, preserving compatibility with the legacy
// hub.ln_mode switch when hub.push_grading has no explicit enabled flag.
func (p *Pusher) resolveEffectiveFromSnapshot(
	snapshot pushGradingSnapshot,
	hasSnap bool,
	manager string,
	draft *pushGradingLayer,
	dropLayer bool,
) (cfg pushGradingEffective, baseCfg pushGradingEffective, layerSource string, ownerEnabled bool) {
	ownerEnabled = p.pushGradingOn()
	var base *pushGradingConfig
	var layer *pushGradingConfig
	if hasSnap {
		base = snapshot.Base
		if !dropLayer && snapshot.Layer != nil && normalizeGradingManager(snapshot.Layer.Manager) == normalizeGradingManager(manager) {
			layer = &snapshot.Layer.Config
			layerSource = snapshot.Layer.Source
		}
	}
	if draft != nil && normalizeGradingManager(draft.Manager) == normalizeGradingManager(manager) {
		layer = &draft.Config
		layerSource = draft.Source
	}
	baseCfg = resolvePushGrading(base, nil, "")
	cfg = resolvePushGrading(base, layer, layerSource)
	if ownerEnabled && (base == nil || !base.Enabled.set || base.Enabled.null) {
		baseCfg.Enabled = true
		baseCfg.By["enabled"] = "base"
		cfg.Enabled = true
		cfg.By["enabled"] = "base"
	}
	return cfg, baseCfg, layerSource, ownerEnabled
}

// effectiveGradingFor resolves the per-manager effective push grading
// configuration without holding gradingMu. On a cold store read failure, it
// degrades cleanly to the cached owner switch (hub.ln_mode) with defaults.
func (p *Pusher) effectiveGradingFor(ctx context.Context, manager string) (cfg, baseCfg pushGradingEffective) {
	snap, supported, err := p.loadPushGradingSnapshotCached(ctx)
	if err != nil {
		if p != nil && p.logf != nil {
			p.logf("push grading: read effective config: %v", err)
		}
		cfg, baseCfg, _, _ = p.resolveEffectiveFromSnapshot(pushGradingSnapshot{}, false, manager, nil, false)
		return cfg, baseCfg
	}
	cfg, baseCfg, _, _ = p.resolveEffectiveFromSnapshot(snap, supported, manager, nil, false)
	return cfg, baseCfg
}

// applyPushGrading decides whether one alert push may go out. With grading off
// for this push's manager and no active owner attention mode, it allows
// everything and touches nothing. It runs once per push call, never per device:
// a held push is one intercepted entry however many devices would have received
// it. The dedup check and recording of an allowed push happen under one hold of
// gradingMu, so concurrent pushes with the same key remain atomic.
func (p *Pusher) applyPushGrading(policy pushPolicy) (allow bool, reason string) {
	level := policy.level
	if level == "" {
		level = assistantwire.LevelP1
	}
	now := p.now()
	attn := p.attentionNow()
	cfg, baseCfg := p.effectiveGradingFor(context.Background(), policy.manager)
	key := strings.TrimSpace(policy.relatedKey)
	in := heldPushRecord{
		At: now.UTC(), Level: level, Title: policy.title, Body: policy.body, RelatedKey: key, Manager: policy.manager,
		TodoID: strings.TrimSpace(policy.todoID), MachineID: strings.TrimSpace(policy.machineID), SessionID: strings.TrimSpace(policy.sessionID),
	}
	if in.MachineID == "" || in.SessionID == "" {
		// Clients follow a session only as a complete pair; do not persist a half-destination.
		in.MachineID, in.SessionID = "", ""
	}
	mgr := normalizeGradingManager(policy.manager)
	p.gradingMu.Lock()
	if !attn.Active() && !cfg.Enabled {
		// Grading is off for this manager and no attention mode is in force:
		// every push goes out as before, and the delivery counts still remember it.
		p.addSentLocked(pushSent{At: now.UTC(), Level: level, RelatedKey: key, Manager: policy.manager})
		p.gradingMu.Unlock()
		return true, ""
	}
	eval := evaluatePushGradingDecision(now, cfg, baseCfg, attn, p.gradingLastAlert[mgr], policy)
	if eval.Allow {
		if key != "" && (eval.EffectiveLevel == assistantwire.LevelP0 || attn.Active() || cfg.Enabled) {
			p.noteKeyPushedLocked(mgr, key, now)
		}
		p.addSentLocked(pushSent{At: now.UTC(), Level: eval.EffectiveLevel, RelatedKey: key, Manager: policy.manager})
		p.gradingMu.Unlock()
		return true, ""
	}
	in.Level = eval.EffectiveLevel
	in.Reason = eval.LiveReason
	merge := cfg.MergeHeld && !eval.AttentionHeld && eval.LiveReason == interceptReasonP1 && eval.EffectiveLevel == assistantwire.LevelP1
	p.addInterceptLocked(in, merge)
	p.gradingMu.Unlock()
	return false, eval.LiveReason
}

// noteKeyPushedLocked remembers that key alerted on manager at now, and forgets keys older than
// gradingDedupMaxRetain (48h, the maximum valid dedup_window) across managers so shorter windows
// on another manager can never evict active dedup entries early. gradingMu is held.
func (p *Pusher) noteKeyPushedLocked(manager, key string, now time.Time) {
	if p.gradingLastAlert == nil {
		p.gradingLastAlert = map[string]map[string]time.Time{}
	}
	for m, byKey := range p.gradingLastAlert {
		for k, at := range byKey {
			if now.Sub(at) >= gradingDedupMaxRetain {
				delete(byKey, k)
			}
		}
		if len(byKey) == 0 {
			delete(p.gradingLastAlert, m)
		}
	}
	mgr := normalizeGradingManager(manager)
	if p.gradingLastAlert[mgr] == nil {
		p.gradingLastAlert[mgr] = map[string]time.Time{}
	}
	p.gradingLastAlert[mgr][key] = now
}

// relatedDestination is the legacy destination a held-push client follows when related_key is a to-do or an http(s)
// URL form. Opaque keys all have an empty destination and do not split the existing same-minute conclusion merge.
func relatedDestination(key string) string {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "todo:") { // todo:<id>, the legacy todo:<id>@<round> and todo:<id>:<reason>
		if id := assistantwire.RelatedTodoID(key); id != "" {
			return "todo:" + id
		}
	}
	for _, scheme := range []string{"https:", "http:"} {
		if len(key) > len(scheme) && strings.EqualFold(key[:len(scheme)], scheme) {
			// WHATWG URLs also accept special forms such as https:foo and https:/foo. Be conservative for merging:
			// an unusual or even invalid suffix may split a row, but can never give it another row's link.
			rest := key[len(scheme):]
			if rest[0] != '?' && rest[0] != '#' {
				return "url:" + key
			}
		}
	}
	return ""
}

// addInterceptLocked keeps one held push and schedules its save. merge (P1): it folds into a recent P1 entry of the
// same manager and compatible destination opened in the same minute, or about the same related key within the 12-hour
// window, so several conclusion cards arriving together, or three cards about one PR, become one entry. Entries past
// the age and count limits go. gradingMu is held.
func (p *Pusher) addInterceptLocked(in heldPushRecord, merge bool) {
	in.Body = summaryBody(in.Body)
	if merge {
		minute := in.At.Truncate(time.Minute)
		for i := len(p.intercepts) - 1; i >= 0 && i >= len(p.intercepts)-64; i-- {
			e := &p.intercepts[i]
			if e.Reason != interceptReasonP1 || e.Manager != in.Manager ||
				e.TodoID != in.TodoID || e.MachineID != in.MachineID || e.SessionID != in.SessionID ||
				relatedDestination(e.RelatedKey) != relatedDestination(in.RelatedKey) {
				// One merged row has one destination. A different (or missing) source needs its own row.
				continue
			}
			sameMinute := e.At.Truncate(time.Minute).Equal(minute)
			sameKey := in.RelatedKey != "" && e.RelatedKey == in.RelatedKey && in.At.Sub(e.At) < gradingDedupWindow
			if sameMinute || sameKey {
				if in.Title != "" {
					e.Also = append(slices.Clip(e.Also), in.Title)
				}
				p.scheduleSaveLocked()
				return
			}
		}
	}
	cutoff := p.now().Add(-heldPushMaxAge)
	kept := p.intercepts[:0]
	for _, e := range p.intercepts {
		if e.At.After(cutoff) {
			kept = append(kept, e)
		}
	}
	p.intercepts = append(kept, in)
	if len(p.intercepts) > heldPushMax {
		p.intercepts = slices.Clone(p.intercepts[len(p.intercepts)-heldPushMax:])
	}
	p.scheduleSaveLocked()
}

// recordIntercept keeps one held push (tests and callers outside applyPushGrading).
func (p *Pusher) recordIntercept(in heldPushRecord) {
	p.gradingMu.Lock()
	p.addInterceptLocked(in, false)
	p.gradingMu.Unlock()
}

// addSentLocked remembers one delivered push for the daily counts, dropping entries past the age and count limits, and
// queues it for the writer, which appends only the new rows to the store (never the whole list). gradingMu is held.
func (p *Pusher) addSentLocked(in pushSent) {
	if in.ID == "" {
		in.ID = rand.Text()
	}
	cutoff := p.now().Add(-heldPushMaxAge)
	kept := p.sent[:0]
	for _, e := range p.sent {
		if e.At.After(cutoff) {
			kept = append(kept, e)
		}
	}
	p.sent = append(kept, in)
	if over := len(p.sent) - pushSentMax; over > 0 {
		// Dropped in place: a full record would otherwise copy all 2,000 entries into a new array on every push.
		n := copy(p.sent, p.sent[over:])
		clear(p.sent[n:])
		p.sent = p.sent[:n]
	}
	if p.sentLog() == nil { // a pusher without the table keeps the counts in memory only
		return
	}
	p.sentMirrorDue = true
	p.sentPending = append(p.sentPending, in)
	if len(p.sentPending) > pushSentMax { // a store that keeps failing: the oldest would be past the count limit anyway
		p.sentPending = slices.Clone(p.sentPending[len(p.sentPending)-pushSentMax:])
	}
	if !p.gradingWriting {
		p.gradingWriting = true
		go p.saveIntercepts()
	}
}

// recordSent remembers one delivered push for the daily counts. It is the entry for paths where applyPushGrading does not run
// (the switch is off in sendFromPolicy); applyPushGrading's own paths call addSentLocked under its lock.
func (p *Pusher) recordSent(policy pushPolicy, level string, now time.Time) {
	if p == nil {
		return
	}
	p.gradingMu.Lock()
	defer p.gradingMu.Unlock()
	p.addSentLocked(pushSent{At: now.UTC(), Level: level, RelatedKey: strings.TrimSpace(policy.relatedKey), Manager: policy.manager})
}

// scheduleSaveLocked marks the list changed and starts the one writer if it is not running. The writer saves the
// latest list, then again while it changed meanwhile, so saves never land out of order and a burst of held pushes is
// one or two writes, off the push path. gradingMu is held.
func (p *Pusher) scheduleSaveLocked() {
	if p.store == nil {
		return
	}
	p.heldDirty = true
	if !p.gradingWriting {
		p.gradingWriting = true
		go p.saveIntercepts()
	}
}

func (p *Pusher) saveIntercepts() {
	for {
		p.gradingMu.Lock()
		if !p.heldDirty && len(p.sentPending) == 0 {
			p.gradingWriting = false
			p.gradingMu.Unlock()
			return
		}
		saveIntercepts := p.heldDirty
		p.heldDirty = false
		var list []heldPushRecord
		if saveIntercepts {
			list = cloneIntercepts(p.intercepts)
		}
		// The delivered pushes since the last save: appended as rows, a burst in one statement.
		sent := p.sentPending
		p.sentPending = nil
		log := p.sentLog()
		now := p.now()
		prune := len(sent) > 0 && now.Sub(p.sentPrunedAt) >= pushSentPruneEvery
		if prune {
			p.sentPrunedAt = now // a failed sweep waits for the next one
		}
		p.gradingMu.Unlock()
		if !p.quiesce.beginOrDefer(func() {
			p.gradingMu.Lock()
			if saveIntercepts {
				p.heldDirty = true
			}
			if len(sent) > 0 {
				p.sentPending = append(sent, p.sentPending...)
				if len(p.sentPending) > pushSentMax {
					p.sentPending = slices.Clone(p.sentPending[len(p.sentPending)-pushSentMax:])
				}
			}
			p.gradingWriting = false
			p.gradingMu.Unlock()
		}) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		failedIntercepts, failedSent := false, false
		if saveIntercepts {
			if err := p.store.PutSetting(ctx, heldPushesSettingKey, list); err != nil {
				p.logf("push grading: save held pushes: %v", err)
				failedIntercepts = true
			}
		}
		if len(sent) > 0 && log != nil {
			if err := log.AddPushSent(ctx, sentRows(sent)); err != nil {
				p.logf("push grading: save delivered pushes: %v", err)
				failedSent = true
			} else if prune {
				if _, err := log.PrunePushSent(ctx, now.Add(-heldPushMaxAge), pushSentMax); err != nil {
					p.logf("push grading: sweep delivered pushes: %v", err)
				}
			}
		}
		cancel()
		p.quiesce.end()
		if !failedIntercepts && !failedSent {
			continue
		}
		// A failed write marks only its own part pending again and the writer retries after a backoff instead of
		// hammering a failing database or rewriting what already landed. waitInterceptsSaved waits it out.
		p.gradingMu.Lock()
		if failedIntercepts {
			p.heldDirty = true
		}
		if failedSent {
			// Ahead of what was added meanwhile, so the rows keep the order the pushes went out in.
			p.sentPending = append(sent, p.sentPending...)
			if len(p.sentPending) > pushSentMax {
				p.sentPending = slices.Clone(p.sentPending[len(p.sentPending)-pushSentMax:])
			}
		}
		p.gradingMu.Unlock()
		time.Sleep(pushSaveRetryBackoff)
	}
}

// waitInterceptsSaved waits (up to d) for the writer to finish; true when nothing is left to save.
func (p *Pusher) waitInterceptsSaved(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		p.gradingMu.Lock()
		idle := !p.gradingWriting && !p.heldDirty && len(p.sentPending) == 0
		p.gradingMu.Unlock()
		if idle {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func cloneIntercepts(in []heldPushRecord) []heldPushRecord {
	out := make([]heldPushRecord, len(in))
	for i, e := range in {
		e.Also = slices.Clone(e.Also)
		out[i] = e
	}
	return out
}

// loadPushGradingState reads the switch and the held-push list once at startup; an absent or unreadable row is off / empty.
func (p *Pusher) loadPushGradingState() {
	if p == nil || p.store == nil {
		return
	}
	if enabled, ok := p.readPushGrading(); ok {
		p.gradingMu.Lock()
		p.gradingCached, p.gradingCachedAt = &enabled, p.now()
		p.gradingMu.Unlock()
	}
	load := func(key string, dst any) bool {
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		defer cancel()
		ok, err := p.store.Setting(ctx, key, dst)
		if err != nil {
			// A failed read starts the record empty but does not delete anything: the row stays until a later write
			// replaces it, and a restart retries the read.
			p.logf("push grading: read %s: %v", key, err)
			return false
		}
		return ok
	}
	var stored []heldPushRecord
	if load(heldPushesSettingKey, &stored) {
		cutoff := p.now().Add(-heldPushMaxAge)
		sanitized := false
		p.gradingMu.Lock()
		p.intercepts = p.intercepts[:0]
		for _, e := range stored {
			if !e.At.After(cutoff) {
				sanitized = true // an expired row is dropped from memory and must be removed on disk too
				continue
			}
			// Rows written before the summary limit may still hold a full body: sanitize at load, mark the list
			// dirty and write it back, so the old full text cannot sit in the database until some unrelated write.
			if sum := summaryBody(e.Body); sum != e.Body {
				sanitized = true
				e.Body = sum
			}
			p.intercepts = append(p.intercepts, e)
		}
		if sanitized && p.store != nil {
			p.scheduleSaveLocked()
		}
		p.gradingMu.Unlock()
	}

	p.loadSent()
}

// loadSent reads the delivered pushes back from hub_push_sent at startup: first it adds the entries of the old
// whole-list setting the table lacks (all inside the limits after the upgrade; after a switch back to a build from
// before, the ones that build delivered), then sweeps the table to the age and count limits, then reads what is
// left, the same entries addSentLocked would have kept in memory. A failed step is logged and the counts start from
// what could be read; the setting is then left alone (mirrorSent), since what this hub holds may be less than it.
func (p *Pusher) loadSent() {
	log := p.sentLog()
	if log == nil {
		return
	}
	step := func(what string, f func(ctx context.Context) error) bool {
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		defer cancel()
		if err := f(ctx); err != nil {
			p.logf("push grading: %s: %v", what, err)
			return false
		}
		return true
	}
	now := p.now()
	cutoff := now.Add(-heldPushMaxAge)
	behind := false
	adopted := step("add "+pushSentSettingKey+" to its table", func(ctx context.Context) (err error) {
		var added int
		added, behind, err = log.AdoptPushSentSetting(ctx, pushSentSettingKey, cutoff, pushSentMax)
		if added > 0 {
			p.logf("push grading: added %d delivered pushes from %s to their table", added, pushSentSettingKey)
		}
		return err
	})
	step("sweep delivered pushes", func(ctx context.Context) error {
		_, err := log.PrunePushSent(ctx, cutoff, pushSentMax)
		return err
	})
	var rows []db.PushSent
	if !step("read delivered pushes", func(ctx context.Context) (err error) {
		rows, err = log.PushSentAfter(ctx, cutoff, pushSentMax)
		return err
	}) {
		return
	}
	p.gradingMu.Lock()
	p.sentPrunedAt = now
	p.sent = p.sent[:0]
	for _, e := range rows {
		p.sent = append(p.sent, pushSent{ID: e.ID, At: e.At, Level: e.Level, RelatedKey: e.RelatedKey, Manager: e.Manager})
	}
	p.sentMirrorable = adopted
	p.sentMirrorDue = p.sentMirrorDue || behind
	p.gradingMu.Unlock()
}

// mirrorSent writes the delivered pushes whole into the hub.push_sent setting when they changed since its last write:
// the record a build from before hub_push_sent reads, kept so that a switch back to one (personad hub switch, a
// self-update going back, the old binary put back by hand) finds the history and the daily counts as this hub has
// them. It is off the push path: a stopping hub calls it (Hub.closeDatabaseAfterWorkers, the updater's rollback) and
// a running one at most every pushSentMirrorEvery (mirrorSentIfDue). A hub that could not read the table or the
// setting at start does not write: its list may be short of what the setting holds. A failed write stays due.
func (p *Pusher) mirrorSent(ctx context.Context) error {
	if p == nil || p.store == nil {
		return nil
	}
	if !quiesceHeld(ctx) {
		if !p.quiesce.beginOrDefer(func() {
			p.mu.Lock()
			p.maintenanceDeferred = true
			p.mu.Unlock()
		}) {
			return nil
		}
		defer p.quiesce.end()
	}
	// One write at a time, so a list taken earlier never lands over a later one.
	p.sentMirrorMu.Lock()
	defer p.sentMirrorMu.Unlock()
	p.gradingMu.Lock()
	if !p.sentMirrorable || !p.sentMirrorDue {
		p.gradingMu.Unlock()
		return nil
	}
	p.sentMirrorDue = false
	now := p.now()
	cutoff := now.Add(-heldPushMaxAge)
	list := make([]pushSent, 0, len(p.sent))
	for _, e := range p.sent {
		if e.At.After(cutoff) {
			list = append(list, e)
		}
	}
	p.gradingMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.storeTimeout)
	defer cancel()
	err := p.store.PutSetting(ctx, pushSentSettingKey, list)
	p.gradingMu.Lock()
	if err != nil {
		p.sentMirrorDue = true
	} else {
		p.sentMirroredAt = now
	}
	p.gradingMu.Unlock()
	return err
}

// mirrorSentIfDue is mirrorSent for a running hub (maintenanceLoop): no sooner than pushSentMirrorEvery after the
// last write, so the first pushes after a start reach the setting at the next pass and later ones within the hour.
func (p *Pusher) mirrorSentIfDue(ctx context.Context) {
	p.gradingMu.Lock()
	due := p.sentMirrorDue && p.now().Sub(p.sentMirroredAt) >= pushSentMirrorEvery
	p.gradingMu.Unlock()
	if !due {
		return
	}
	if err := p.mirrorSent(ctx); err != nil {
		p.logf("push grading: keep %s for an older build: %v", pushSentSettingKey, err)
	}
}

// heldPushesView returns the held pushes newest first, at most limit (clamped to heldPushViewLimitMax). When manager
// is not empty, only that manager's entries count toward the limit: the filter runs before the truncation, so another
// manager's burst can never push an extension's own entries out of its 200.
func (p *Pusher) heldPushesView(manager string, limit int) []heldPushRecord {
	if limit <= 0 || limit > heldPushViewLimitMax {
		limit = heldPushViewLimitMax
	}
	p.gradingMu.Lock()
	defer p.gradingMu.Unlock()
	out := make([]heldPushRecord, 0, min(limit, len(p.intercepts)))
	for i := len(p.intercepts) - 1; i >= 0 && len(out) < limit; i-- {
		e := p.intercepts[i]
		if manager != "" && e.Manager != manager {
			continue
		}
		e.Also = slices.Clone(e.Also)
		out = append(out, e)
	}
	return out
}

// pushDayStats is one UTC calendar day's delivery counts for one manager, counted from the full in-memory records
// (up to 500 held / 2000 delivered entries) rather than the 200-entry view, so the numbers stay exact past 200.
type pushDayStats struct {
	Date        string         `json:"date"`
	Manager     string         `json:"manager"`
	Sent        int            `json:"sent"`
	Intercepted int            `json:"intercepted"`
	ByReason    map[string]int `json:"by_reason,omitempty"`
}

// pushDailyStats counts delivered and held pushes at or after since, per UTC day and manager. When manager is not empty
// only that manager's pushes are counted (an extension's view). Days with no pushes are omitted; days sort ascending.
func (p *Pusher) pushDailyStats(since time.Time, manager string) []pushDayStats {
	type agg struct {
		sent, intercepted int
		byReason          map[string]int
	}
	p.gradingMu.Lock()
	days := map[string]map[string]*agg{}
	add := func(at time.Time, mgr string, reason string, delivered bool) {
		if at.Before(since) {
			return
		}
		if manager != "" && mgr != manager {
			return
		}
		day := at.UTC().Format("2006-01-02")
		if days[day] == nil {
			days[day] = map[string]*agg{}
		}
		a := days[day][mgr]
		if a == nil {
			a = &agg{byReason: map[string]int{}}
			days[day][mgr] = a
		}
		if delivered {
			a.sent++
			return
		}
		a.intercepted++
		a.byReason[reason]++
	}
	for _, e := range p.sent {
		add(e.At, e.Manager, "", true)
	}
	for _, e := range p.intercepts {
		add(e.At, e.Manager, e.Reason, false)
	}
	p.gradingMu.Unlock()

	dates := make([]string, 0, len(days))
	for d := range days {
		dates = append(dates, d)
	}
	slices.Sort(dates)
	out := make([]pushDayStats, 0, len(dates))
	for _, d := range dates {
		managers := make([]string, 0, len(days[d]))
		for m := range days[d] {
			managers = append(managers, m)
		}
		slices.Sort(managers)
		for _, m := range managers {
			a := days[d][m]
			out = append(out, pushDayStats{Date: d, Manager: m, Sent: a.sent, Intercepted: a.intercepted, ByReason: a.byReason})
		}
	}
	return out
}

// interceptReaderAllowed says whether the extension token r carries is on the hub's explicit allow-list
// (hub.json intercept_readers). The scope gate already proved it has notify; this names the extension by ID.
func (h *Hub) interceptReaderAllowed(r *http.Request) bool {
	if h == nil || h.cfg == nil {
		return false
	}
	return slices.Contains(h.cfg.InterceptReaders, principalOf(r.Context()).Extension)
}

// setPushGrading turns the switch on or off: the route's PUT (the apps' Settings › Notifications) and persona_settings
// notifications.low_distraction once the owner approved it on the phone (self_settings_push_grading.go). The managers are told
// at once, since they hold back their own cards by the switch too (Hello.QuietPush).
func (h *Hub) setPushGrading(ctx context.Context, on bool) error {
	if err := h.push.setPushGrading(ctx, on); err != nil {
		return err
	}
	h.broadcast(map[string]any{"type": "push_grading", "enabled": on})
	h.assistantUpdated()
	return nil
}

// pushGradingRoutes serves the switch, the read-only list of held pushes and the read-only daily delivery counts. The routes
// sit behind the standard API gate; an extension token with the notify scope may read the switch, its own manager's
// held pushes and counts, but not change the switch.
func (h *Hub) pushGradingRoutes(api *http.ServeMux) {
	getSwitch := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"enabled": h.push.pushGradingOn()})
	}
	putSwitch := func(w http.ResponseWriter, r *http.Request) {
		if extensionToken(r) {
			writeErr(w, http.StatusForbidden, errors.New("the ln mode switch is for the hub's own apps"))
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSONBody(w, r, &body, 1<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if err := h.setPushGrading(r.Context(), body.Enabled); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, map[string]any{"enabled": body.Enabled})
	}
	api.HandleFunc("GET /api/v1/push/ln-mode", getSwitch)
	api.HandleFunc("GET /api/v1/push/grading", getSwitch)
	api.HandleFunc("PUT /api/v1/push/ln-mode", putSwitch)
	api.HandleFunc("PUT /api/v1/push/grading", putSwitch)
	h.pushGradingLayerRoutes(api)
	api.HandleFunc("GET /api/v1/push/intercepts", func(w http.ResponseWriter, r *http.Request) {
		em := extensionManager(r)
		if extensionToken(r) && !h.interceptReaderAllowed(r) {
			// The notify scope alone is not enough: held pushes can carry the owner's content, so only extension IDs
			// the hub configuration explicitly lists may read them.
			writeErr(w, http.StatusForbidden, errors.New("this extension is not allowed to read held pushes"))
			return
		}
		// The manager filter runs before the 200-entry limit: an extension token reads only its own manager's held
		// pushes; a full credential reads every manager's. Bodies are summaries already (summaryBody at record time).
		view := h.push.heldPushesView(em, 0)
		writeJSON(w, map[string]any{"intercepts": view})
	})
	api.HandleFunc("GET /api/v1/push/stats/daily", func(w http.ResponseWriter, r *http.Request) {
		now := h.push.now().UTC()
		since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		q := r.URL.Query()
		if raw := q.Get("since"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeErr(w, http.StatusBadRequest, errors.New("since must be an RFC3339 timestamp"))
				return
			}
			since = parsed
		} else if raw := q.Get("days"); raw != "" {
			days, err := strconv.Atoi(raw)
			if err != nil || days < 1 || days > pushStatsDaysMax {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("days must be a number from 1 to %d", pushStatsDaysMax))
				return
			}
			since = since.AddDate(0, 0, -(days - 1))
		}
		if oldest := now.Add(-heldPushMaxAge); since.Before(oldest) {
			since = oldest
		}
		// An extension token counts only its own manager's pushes; a full credential counts every manager's.
		writeJSON(w, map[string]any{
			"since": since.UTC().Format(time.RFC3339),
			"days":  h.push.pushDailyStats(since, extensionManager(r)),
		})
	})
}
