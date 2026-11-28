package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/relayproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Urgent calls (docs/assistant/urgent-call.md): a manager decides a matter may ring the owner's phone and asks the
// hub (call.ring); the hub only delivers the ring, a VoIP push to every phone that registered a PushKit token. The
// phone opens it, reports the call and asks the manager for the matter. The hub keeps no state about calls.

func init() {
	handleTypedBus(assistantwire.CallRingMethod, assistantwire.MethodCallRing+": ", func(h *Hub, ctx context.Context, c *busConn, p assistantwire.CallRingRequest) (assistantwire.CallRingResult, error) {
		if p.Call.ID == "" || p.Call.Title == "" {
			return assistantwire.CallRingResult{}, &rpc.Error{Code: bus.CodeInvalidParams, Message: assistantwire.MethodCallRing + ": id and title are required"}
		}
		return h.push.ringCall(ctx, c.managerID(), p.Call), nil
	})
	// A manager's tools changed (urgent_call comes with its switch): asked for again, so the conversations it starts
	// from now on list them.
	handleBusEvent(assistantwire.EventToolsChanged, func(h *Hub, c *busConn, _ json.RawMessage) {
		id := c.managerID()
		if id != assistantwire.AssistantID {
			h.managerReconnected(id) // its tools are asked for when next needed
			return
		}
		go h.refreshAssistantTools()
	})
}

// refreshAssistantTools asks the assistant for its tools again and keeps them (assistantConnected does it on connect).
func (h *Hub) refreshAssistantTools() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := buswire.Call(ctx, h.managerCaller(assistantwire.AssistantID), assistantwire.ToolsMethod, assistantwire.SessionRef{})
	if err != nil {
		h.logf("assistant: tools: %v", err)
		return
	}
	a := &h.assistant
	a.toolsMu.Lock()
	a.tools = res.Tools
	a.toolsMu.Unlock()
}

// The way a device is rung, beside its pushes' (pushRoute): Reason pushNoToken when it registered no PushKit token
// (an older app, or one that cannot take calls), pushRelayFailed when its relay gave that token no grant (an older
// relay answers 403 to the ".voip" topic; Detail: the phone's words).
func (p *Pusher) callRouteOf(d Device) pushRoute {
	switch {
	case d.VoIPToken == "":
		return pushRoute{Route: pushNone, Reason: pushNoToken}
	case p.direct != nil && p.callable(d):
		return pushRoute{Route: pushDirect}
	case p.direct != nil:
		return pushRoute{Route: pushNone, Reason: pushNoKey} // office network mode: a phone without a push key is not rung
	case p.callable(d):
		return pushRoute{Route: pushRelay}
	case d.VoIPError != "":
		return pushRoute{Route: pushNone, Reason: pushRelayFailed, Detail: d.VoIPError}
	case d.RelayURL == "" || d.RelayGrant == "":
		return pushRoute{Route: pushNone, Reason: pushNoRelay}
	default:
		return pushRoute{Route: pushNone, Reason: pushRelayFailed, Detail: "no grant"}
	}
}

// callRouteOfID is callRouteOf for the device with this id; ok false when the hub has no such device.
func (p *Pusher) callRouteOfID(id string) (pushRoute, bool) {
	d, ok := p.device(id)
	if !ok {
		return pushRoute{}, false
	}
	return p.callRouteOf(d), true
}

// callable: a ring can reach d (its PushKit token, the ordinary relay registration, and the relay's grant for that
// token when the hub has no key). A relay URL kept only to diagnose a refused ordinary registration is not enough.
func (p *Pusher) callable(d Device) bool {
	if d.VoIPToken == "" {
		return false
	}
	if p.direct != nil {
		return !p.privateOnly || d.PushKey != "" // office network mode rings only sealed
	}
	return p.relayed(d) && d.VoIPGrant != ""
}

// pushTypeVoIP is the APNs push type of a ring (an APNs header, not a message the apps get).
const pushTypeVoIP = "voip"

// voipTokenPattern is what a PushKit token looks like (hex, as the relay checks it).
var voipTokenPattern = lazyre.New(`^[0-9a-fA-F]{32,400}$`)

// cleanVoIP is register's check of what the phone said about calls: a token that is not one is dropped with its
// grant, a grant clears the error, and the error is clipped as relay_error is.
func cleanVoIP(d *Device) {
	if d.VoIPToken != "" && !voipTokenPattern.MatchString(d.VoIPToken) {
		d.VoIPToken, d.VoIPGrant = "", ""
	}
	if d.VoIPToken == "" {
		d.VoIPGrant = ""
	}
	if d.VoIPGrant != "" {
		d.VoIPError = ""
	}
	d.VoIPError = clipRelayError(d.VoIPError)
}

// callRingWait bounds how long call.ring waits for the pushes' answers (each send already has its own timeouts and
// retries); a ring that took longer is over anyway.
var callRingWait = 30 * time.Second

// ringCall sends one ring to every phone that can take calls and answers how many APNs or the relay took.
func (p *Pusher) ringCall(ctx context.Context, manager string, call assistantwire.CallRing) assistantwire.CallRingResult {
	if p == nil {
		return assistantwire.CallRingResult{Reason: assistantwire.CallNoPhone}
	}
	p.mu.Lock()
	var phones []Device
	for _, d := range p.devices {
		if p.callable(d) {
			phones = append(phones, d)
		}
	}
	p.mu.Unlock()
	if len(phones) == 0 {
		return assistantwire.CallRingResult{Reason: assistantwire.CallNoPhone}
	}
	payload := map[string]any{"urgent_call": map[string]any{
		"id": call.ID, "title": call.Title, "manager": manager, "conversation_id": call.ConversationID, "attempt": call.Attempt,
	}}
	if p.hubID != nil {
		payload["hub_id"] = p.hubID()
	}
	var took atomic.Int32
	var wg sync.WaitGroup
	for _, d := range phones {
		wg.Add(1)
		p.sends.Add(1)
		go func(d Device) {
			defer p.sends.Done()
			defer wg.Done()
			if err := p.ringOne(d, payload, call); err != nil {
				p.logf("urgent call %s: %v", d.Name, err)
				return
			}
			took.Add(1)
		}(d)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	wait := time.NewTimer(callRingWait)
	defer wait.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	case <-wait.C:
	}
	if n := int(took.Load()); n > 0 {
		return assistantwire.CallRingResult{Devices: n}
	}
	return assistantwire.CallRingResult{Reason: assistantwire.CallRefused}
}

// voipTTL is the push's lifetime in seconds: the call's, within what the relay takes.
func voipTTL(ttl int) int {
	if ttl <= 0 {
		return relayproto.DefaultVoIPTTL
	}
	return min(ttl, relayproto.MaxVoIPTTL)
}

// ringOne rings d: sealed to its key through its relay, or with the hub's own key (sealed too when the phone has a key,
// so the app opens every ring the same way). nil: APNs (or the relay) took it.
func (p *Pusher) ringOne(d Device, payload map[string]any, call assistantwire.CallRing) error {
	var sealed string
	if d.PushKey != "" {
		s, err := p.sealFor(d, payload)
		if err != nil {
			return fmt.Errorf("seal: %w", err) // never sent in the clear to a phone that has a key
		}
		sealed = s
	}
	if p.privateOnly && sealed == "" {
		return errors.New("office network mode rings only a phone with a push key")
	}
	if p.direct == nil {
		return p.ringRelay(d, relayproto.PushRequest{Type: pushTypeVoIP, CollapseID: relayCollapseID("call:" + call.ID), Sealed: sealed, TTL: call.TTL})
	}
	body, _ := json.Marshal(map[string]any{"e": sealed})
	if sealed == "" {
		stamped := map[string]any{"iat": p.now().Unix()}
		for k, v := range payload {
			stamped[k] = v
		}
		body, _ = json.Marshal(stamped)
	}
	r := apns.Request{Token: d.VoIPToken, Env: d.APNSEnv, Topic: d.Topic, PushType: pushTypeVoIP, Priority: "10",
		CollapseID: "call:" + call.ID, Expiration: p.now().Add(time.Duration(voipTTL(call.TTL)) * time.Second).Unix(), Body: body}
	res, err := p.direct.Send(context.Background(), r)
	for _, wait := range p.retryWaits {
		if (err == nil && !directRetryable(res.Status)) || errors.Is(err, apns.ErrInvalidDeviceToken) {
			break
		}
		time.Sleep(wait)
		res, err = p.direct.Send(context.Background(), r)
	}
	switch {
	case err != nil:
		return err
	case res.Gone():
		p.dropVoIP(d, "APNs: "+res.Reason)
		return fmt.Errorf("token gone (%s)", res.Reason)
	case res.Status != 200:
		return fmt.Errorf("APNs %d %s", res.Status, res.Reason)
	}
	return nil
}

// ringRelay hands a ring to d's relay with its VoIP grant. A grant the relay no longer takes is renewed once with the
// PushKit token, as renewGrant does for the alert grant.
func (p *Pusher) ringRelay(d Device, req relayproto.PushRequest) error {
	via := d
	via.RelayGrant = d.VoIPGrant
	res, status, err := p.postRelayRetrying(via, req)
	if status == http.StatusUnauthorized {
		grant, rerr := p.renewVoIPGrant(d)
		if rerr != nil {
			return fmt.Errorf("%w; renewing the call grant: %v", err, rerr)
		}
		via.RelayGrant = grant
		res, _, err = p.postRelayRetrying(via, req)
	}
	if err != nil {
		return err
	}
	if res.gone {
		p.dropVoIP(d, "relay: token gone")
		return fmt.Errorf("token gone")
	}
	return nil
}

// renewVoIPGrant registers d's PushKit token with its relay again and keeps the new grant.
func (p *Pusher) renewVoIPGrant(d Device) (string, error) {
	if p.privateOnly {
		return "", errRelayOff
	}
	if d.VoIPToken == "" || d.Topic == "" {
		return "", fmt.Errorf("device has no call token to register")
	}
	env := d.APNSEnv
	if env != "sandbox" {
		env = "production"
	}
	body, _ := json.Marshal(relayproto.RegisterRequest{APNSToken: d.VoIPToken, Env: env, Topic: d.Topic + relayproto.VoIPTopicSuffix})
	resp, err := p.relay.Post(d.RelayURL+"/v1/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Grant string `json:"grant"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
	if resp.StatusCode != 200 || out.Grant == "" {
		return "", fmt.Errorf("register: %d %s", resp.StatusCode, out.Error)
	}
	err = p.editVoIP(d, func(cur *Device) { cur.VoIPGrant = out.Grant })
	if err != nil {
		return "", err
	}
	p.logf("relay %s: call grant renewed", d.Name)
	return out.Grant, nil
}

// dropVoIP forgets d's PushKit token after APNs called it dead; the device keeps its other pushes. The app registers a
// new one when PushKit gives it one.
func (p *Pusher) dropVoIP(d Device, why string) {
	if err := p.editVoIP(d, func(cur *Device) { cur.VoIPToken, cur.VoIPGrant, cur.VoIPError = "", "", why }); err != nil {
		p.logf("urgent call %s: forgetting the dead call token: %v", d.Name, err)
	}
}

// editVoIP changes the stored device when it still has d's PushKit token and relay.
func (p *Pusher) editVoIP(d Device, edit func(*Device)) error {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	cur, ok := p.devices[d.DeviceID]
	p.mu.Unlock()
	if !ok || cur.VoIPToken != d.VoIPToken || cur.RelayURL != d.RelayURL {
		return fmt.Errorf("push registration changed")
	}
	edit(&cur)
	if err := p.writeDevice(cur); err != nil {
		return err
	}
	p.mu.Lock()
	if latest := p.devices[d.DeviceID]; latest.LastPushAt.After(cur.LastPushAt) {
		cur.LastPushAt = latest.LastPushAt
	}
	p.devices[d.DeviceID] = cur
	p.mu.Unlock()
	return nil
}
