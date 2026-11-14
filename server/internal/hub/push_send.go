package hub

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// send alerts every device whose settings take it (nil: all of them).
// A device that chose "Phone Muted" gets none while a desktop app is connected (desktops.go), and a desktop app that is
// connected gets none of these: it shows them itself. What was asked for explicitly (nil) goes to it all the same.
// The hub's own alerts (pairing, a computer joining) always pass the grading filter: they are P0.
func (p *Pusher) send(payload map[string]any, collapseID string, wants func(NotifyPrefs) bool) {
	p.sendLocalized(func(i18n.Lang) map[string]any { return payload }, collapseID, wants)
}

// sendLocalized is send with the alert written per device, in its language (langOf).
func (p *Pusher) sendLocalized(build localized, collapseID string, wants func(NotifyPrefs) bool) {
	p.sendLocalizedQuiet(build, collapseID, wants, quietNone)
}

// sendLocalizedQuiet is sendLocalized with what each device's quiet hours do to this push (quiethours.go).
func (p *Pusher) sendLocalizedQuiet(build localized, collapseID string, wants func(NotifyPrefs) bool, rule quietRule) {
	p.sendLocalizedQuietTo(build, collapseID, wants, rule, nil)
}

// sendLocalizedQuietTo is sendLocalizedQuiet that tells sent each device the push goes to (nil: nobody).
func (p *Pusher) sendLocalizedQuietTo(build localized, collapseID string, wants func(NotifyPrefs) bool, rule quietRule, sent func(Device)) {
	p.sendFromPolicy("", pushPolicy{level: assistantwire.LevelP0}, build, collapseID, wants, rule, nil, sent)
}

// sendFrom is sendLocalizedQuietTo for a push manager asked for (notify.request, its extensions' pushes, a computer's
// personad notify): none to a device that turned that manager's pushes off (NotifyPrefs.MutedManagers), whatever else
// it takes. "" is the hub's own push. wants keeps its meaning: nil (asked for explicitly) still goes past Phone Muted
// and to an open desktop. Returns the selected count and the reachable count before preferences are applied. Such a
// push defaults to P1 for grading; callers that know its urgency use sendFromPolicy.
func (p *Pusher) sendFrom(manager string, build localized, collapseID string, wants func(NotifyPrefs) bool, rule quietRule, sent func(Device)) (selected, reachable int) {
	selected, reachable, _ = p.sendFromPolicy(manager, pushPolicy{level: assistantwire.LevelP1, manager: manager}, build, collapseID, wants, rule, nil, sent)
	return selected, reachable
}

// sendFromPolicy is sendFrom with the push's grading policy (level, related key and the held-push record's text).
// heldReason is "" when the push went out, else why the grading filter held it (lnmode.go): a caller that answers the
// model (the assistant's notify_user) tells it the notice reached the briefing instead of the phone.
func (p *Pusher) sendFromPolicy(manager string, policy pushPolicy, build localized, collapseID string, wants func(NotifyPrefs) bool, rule quietRule, only func(Device) bool, sent func(Device)) (selected, reachable int, heldReason string) {
	if p == nil {
		return 0, 0, ""
	}
	if p.lnModeOn() { // off: nothing below runs and the push goes exactly as before
		policy.manager = manager
		if policy.title == "" || policy.body == "" {
			// The held-push record's text, from the alert when the caller gave none. A payload need not have one (an
			// extension's push_localized may carry no aps at all).
			aps, _ := build(i18n.Default)["aps"].(map[string]any)
			alert, _ := aps["alert"].(map[string]any)
			if t, _ := alert["title"].(string); policy.title == "" {
				policy.title = t
			}
			if b, _ := alert["body"].(string); policy.body == "" {
				policy.body = b
			}
		}
		if allow, reason := p.applyLn(policy); !allow {
			// Held back for every device; the selection counts as zero. The WebSocket still carries it (callers run
			// notifyWSFrom themselves), so the app's lists and briefings keep updating.
			return 0, 0, reason
		}
	} else {
		// The switch is off, so applyLn never runs; the daily delivery counts still record one logical push.
		policy.manager = manager
		level := policy.level
		if level == "" {
			level = assistantwire.LevelP1
		}
		p.recordSent(policy, level, p.now())
	}
	selected, reachable = p.sendFromTo(manager, build, collapseID, wants, rule, only, sent)
	return selected, reachable, ""
}

// sendFromTo is sendFrom to the devices only takes (nil: every device), before their settings decide.
func (p *Pusher) sendFromTo(manager string, build localized, collapseID string, wants func(NotifyPrefs) bool, rule quietRule, only func(Device) bool, sent func(Device)) (selected, reachable int) {
	if p == nil {
		return 0, 0
	}
	online := p.desktopOnline != nil && p.desktopOnline()
	byPrefs := alertWanted(wants, online)
	var hush func(Device) bool
	if rule == quietHush {
		hush = func(d Device) bool { return p.inQuiet(devicePrefs(d.Prefs)) }
	}
	return p.sendBuiltWith(build, collapseID, "alert", "10", func(d Device) bool {
		if only != nil && !only(d) {
			return false
		}
		if wants != nil && p.desktopOpen != nil && p.desktopOpen(d.DeviceID) {
			return false
		}
		prefs := devicePrefs(d.Prefs)
		if prefs.mutes(manager) || rule == quietSkip && p.inQuiet(prefs) {
			return false
		}
		if !byPrefs(prefs) {
			return false
		}
		if sent != nil {
			sent(d)
		}
		return true
	}, hush)
}

// sendLiveActivity pushes to a Live Activity token (an activity's own, or the push-to-start one): its own push type and
// topic, the device's APNs environment. gone runs when APNs says the token is dead. Through the relay the payload is
// not sealed: iOS reads a Live Activity's content-state itself (docs/operations/push-relay.md "Live Activities are plaintext").
// The returned channel closes after the attempt and its retries, so revocation can wait before forgetting the device.
func (p *Pusher) sendLiveActivity(d Device, token string, payload map[string]any, priority string, gone func()) <-chan struct{} {
	done := make(chan struct{})
	if p == nil || token == "" || !p.reachable(d) {
		close(done)
		return done
	}
	body, _ := json.Marshal(payload)
	event := trafficPushEvent(payload, "liveactivity")
	if p.relayed(d) {
		aps, _ := payload["aps"].(map[string]any)
		p.notePush(d, nil, event, "relay", "attempt", 0, int64(len(body)), 0)
		go func() {
			defer close(done)
			started := time.Now()
			res, err := p.postRelay(d, relayproto.PushRequest{Type: "liveactivity", Priority: priority, Token: token, APS: aps})
			p.noteRelayResult(d, nil, event, err, res.gone, int64(len(body)), time.Since(started))
			if err != nil {
				p.logf("relay live activity %s: %v", d.Name, err)
			} else if res.gone {
				gone()
			}
		}()
		return done
	}
	p.notePush(d, nil, event, "apns", "attempt", 0, int64(len(body)), 0)
	go func() {
		defer close(done)
		started := time.Now()
		res, err := p.sendDirect(d, apns.Request{Token: token, Env: d.APNSEnv, Topic: d.Topic, PushType: "liveactivity", Priority: priority, Body: body})
		p.noteDirectResult(d, nil, event, err, res.Status, int64(len(body)), time.Since(started))
		if err != nil {
			p.logf("apns live activity %s: %v", d.Name, err)
			return
		}
		if res.Status != 200 {
			p.logf("apns live activity %s: %d %s", d.Name, res.Status, res.Reason)
			if res.Gone() {
				gone()
			}
		}
	}()
	return done
}

// wakeDevices sends a silent background push to the devices in ids: iOS gives the app ~30 s to connect and run a
// pending phone_* call. Best effort: the system throttles these and never delivers them to a force-quit app. Phones
// only: the phone_* tools run on the iPhone (a Mac registers as a desktop, docs/apple/mac.md).
func (p *Pusher) wakeDevices(reason string, ids map[string]bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	bound := false
	for _, d := range p.devices {
		if p.reachable(d) && (ids[d.DeviceID] || (d.CredentialID != "" && ids[d.CredentialID])) {
			bound = true
			break
		}
	}
	p.mu.Unlock()
	p.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}, "persona_wake": reason, compat.WakeField: reason}, "", "background", "5",
		func(d Device) bool {
			return d.Kind != kindMac && (ids[d.DeviceID] || (d.CredentialID != "" && ids[d.CredentialID]) || (!bound && ids["legacy"] && d.CredentialID == "" && d.Kind != "watch"))
		})
}

// sendAs pushes to every reachable device that to picks (nil: all of them). to runs outside p.mu: it may ask the hub.
func (p *Pusher) sendAs(payload map[string]any, collapseID, pushType, priority string, to func(Device) bool) {
	p.sendBuilt(func(i18n.Lang) map[string]any { return payload }, collapseID, pushType, priority, to)
}

// localized builds a push (or WebSocket notification) in a language.
type localized func(i18n.Lang) map[string]any

// langOf is the language of what is pushed to d: the one it registered with, else the user's (locale.go).
func (p *Pusher) langOf(d Device) i18n.Lang {
	if l := i18n.Lang(d.Locale); i18n.Valid(l) {
		return l
	}
	if p.userLang != nil {
		return p.userLang()
	}
	return i18n.Default
}

func (p *Pusher) sendBuilt(build localized, collapseID, pushType, priority string, to func(Device) bool) {
	p.sendBuiltWith(build, collapseID, pushType, priority, to, nil)
}

// sendBuiltWith is sendBuilt where the devices hush picks get the push without a sound (hushed, quiet hours).
// Counts describe this selection, not asynchronous APNs or relay acceptance or delivery.
func (p *Pusher) sendBuiltWith(build localized, collapseID, pushType, priority string, to func(Device) bool, hush func(Device) bool) (selected, candidates int) {
	if p == nil {
		return 0, 0
	}
	p.mu.Lock()
	reachable := make([]Device, 0, len(p.devices))
	for _, d := range p.devices {
		if p.reachable(d) {
			reachable = append(reachable, d)
		}
	}
	p.mu.Unlock()
	if collapseID == "" {
		// One id for this push, kept by every retry (sendDirect, postRelayRetrying): a push APNs took whose answer was
		// lost shows once, not twice (#4158). Each push gets its own, so different ones do not replace each other.
		collapseID = "p_" + randomHex(8)
	}
	devices := reachable[:0]
	for _, d := range reachable {
		if to == nil || to(d) {
			devices = append(devices, d)
		}
	}
	if len(devices) == 0 {
		return 0, len(reachable)
	}
	type built struct {
		payload map[string]any
		body    []byte
		// unfit: the direct body is over APNs' limit even shortened, so it is not sent (APNs would refuse it with 413).
		unfit error
	}
	type variant struct {
		lang  i18n.Lang
		quiet bool
	}
	byLang := map[variant]built{}
	for _, d := range devices {
		lang := p.langOf(d)
		key := variant{lang, hush != nil && hush(d)}
		b, ok := byLang[key]
		if ok && b.payload == nil {
			continue // nothing in its language: logged once below
		}
		if !ok {
			b.payload = cleanAlert(build(lang))
			if b.payload == nil {
				// build had nothing in this language: its devices get no push, and the hub_id below is not written to a nil map
				p.logf("push: no %s payload in %s: its devices get none", pushType, lang)
				byLang[key] = b
				continue
			}
			if key.quiet {
				b.payload = hushed(b.payload)
			}
			if p.hubID != nil {
				b.payload = maps.Clone(b.payload)
				b.payload["hub_id"] = p.hubID()
			}
			b.body, _ = json.Marshal(b.payload)
			if p.direct != nil && len(b.body) > apnsMaxAlert {
				// A long card summary or answer would be refused whole (413): sent shortened, as the relay's are (#3809).
				fitted := maps.Clone(b.payload)
				if body, err := fitAlert(fitted, apnsMaxAlert); err == nil {
					b.body = body
				} else {
					b.unfit = fmt.Errorf("push of %d bytes is over APNs' %d-byte limit", len(b.body), apnsMaxAlert)
					p.logf("apns: %s push of %d bytes does not fit even shortened, not sent: %v", pushType, len(b.body), err)
				}
			}
			byLang[key] = b
		}
		payload, body := b.payload, b.body
		event := trafficPushEvent(payload, pushType)
		if p.relayed(d) {
			sealed, err := p.sealFor(d, payload)
			if err != nil {
				p.logf("relay %s: seal: %v", d.Name, err) // never falls back to sending it in the clear
				p.pushed(d, fmt.Errorf("seal: %w", err))
				continue
			}
			p.notePush(d, nil, event, "relay", "attempt", 0, int64(len(body)), 0)
			go func(d Device) {
				started := time.Now()
				res, err := p.postRelay(d, relayproto.PushRequest{Type: pushType, Priority: priority, CollapseID: relayCollapseID(collapseID), Sealed: sealed})
				p.noteRelayResult(d, nil, event, err, res.gone, int64(len(body)), time.Since(started))
				if err != nil {
					p.logf("relay %s: %v", d.Name, err)
				} else if res.gone {
					p.forgetIfCurrent(d)
				}
			}(d)
			continue
		}
		if b.unfit != nil {
			p.pushed(d, b.unfit)
			continue
		}
		p.notePush(d, nil, event, "apns", "attempt", 0, int64(len(body)), 0)
		go func(d Device) {
			sent := time.Now()
			res, err := p.sendDirect(d, apns.Request{Token: d.APNSToken, Env: d.APNSEnv, Topic: d.Topic,
				PushType: pushType, Priority: priority, CollapseID: collapseID, Body: body})
			p.noteDirectResult(d, nil, event, err, res.Status, int64(len(body)), time.Since(sent))
			if err != nil {
				p.logf("apns %s: %v", d.Name, err)
				return
			}
			if res.Status != 200 {
				p.logf("apns %s: %d %s", d.Name, res.Status, res.Reason)
				if res.Gone() {
					p.forgetIfCurrent(d)
				}
			} else { // when APNs took it: for measuring wake-up latency (docs/assistant/assistant.md)
				p.logf("apns %s: %s push accepted in %d ms", d.Name, pushType, time.Since(sent).Milliseconds())
			}
		}(d)
	}
	return len(devices), len(reachable)
}
