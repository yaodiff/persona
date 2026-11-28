package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A persona_settings change is a card good for fifteen minutes. The hub waited for it through callBus, whose two
// minutes applied because nothing upstream sets a deadline: the hub's own card was withdrawn thirteen minutes early,
// and a change of one of the manager's settings was reported as failed while its card stayed up and still applied on
// a later approval (#5907).
//
// These run the hub's real bus endpoint (serveBus: its peer, its Cancels, its dispatch) against a manager played by
// hand, inside a synctest bubble. The connection is a net.Pipe, so a card's minutes pass at once and to the instant.

// loopbackConn is the hub's end of a pipe, with a loopback address: the bus refuses plaintext from an address it
// cannot place.
type loopbackConn struct{ net.Conn }

func (loopbackConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

// playedManager is the manager's end of the bus, played by a test.
type playedManager struct {
	peer *rpc.Peer
	ctx  context.Context
}

// playManager connects a manager that answers the hub's calls with answer to h's bus, inside the calling synctest
// bubble, and says its hello. Its own app.calls tell the hub when their caller stops waiting, as the real manager's
// do (assistant.NewBusHub).
func playManager(t *testing.T, h *Hub, answer rpc.Handler) *playedManager {
	t.Helper()
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	l := bubbleServer(t, false, mux)
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		near, far := net.Pipe()
		select {
		case l.conns <- loopbackConn{far}:
		case <-l.done:
			return nil, net.ErrClosed
		}
		t.Cleanup(func() { _ = near.Close() })
		return near, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	ws, err := bus.Dial(ctx, "http://hub.invalid", busToken(t, h, assistantwire.AssistantID, "Played manager"), &http.Client{Transport: tr})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = ws.CloseNow()
		// The hub ends what ran on a manager's connection a moment after it is gone (devicesGone): that moment passes
		// on the bubble's clock, which stops once the test's own goroutine is done.
		after := time.NewTimer(time.Second)
		defer after.Stop()
		<-after.C
		synctest.Wait()
	})
	conn := bus.NewConn(ctx, ws)
	peer := rpc.New("s", conn)
	peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != assistantwire.MethodFleetAskPrepared {
			return answer(ctx, method, params)
		}
		// Existing played managers still express their answer as fleet.ask's
		// Resolution. Adapt that answer to the prepared outcome and consume the
		// retained hub handle exactly as the real coordinator's apply bus call does.
		raw, err := answer(ctx, assistantwire.MethodFleetAsk, params)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(raw)
		var r proto.Resolution
		if err := json.Unmarshal(encoded, &r); err != nil {
			return nil, err
		}
		var in struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		switch {
		case r.UserAllowed():
			text, err := h.applyPreparedSetting(ctx, assistantwire.AssistantID, in.Interaction.ID, r)
			return approval.Outcome{Text: text}, err
		case r.UserDenied():
			return approval.Outcome{Text: "The user declined the change: nothing changed."}, nil
		default:
			return approval.Outcome{Text: prompts.RenderSettingUnanswered()}, nil
		}
	}
	peer.Cancels = map[string]bool{assistantwire.MethodApp: true}
	go func() { _ = peer.Serve(ctx, conn) }()
	if err := peer.Call(ctx, busHello, map[string]string{"id": assistantwire.AssistantID}, nil); err != nil {
		t.Fatal(err)
	}
	m := newMachine(h, MachineConfig{ID: "m1", Name: "m1"})
	registerMachine(h, m)
	return &playedManager{peer: peer, ctx: ctx}
}

// settingsTool is the assistant's persona_settings call as its manager sends it to the hub (app.call, the settings
// app's tool) with ctx, and what the assistant is told: the tool's text, and whether it is an error.
func (p *playedManager) settingsTool(ctx context.Context, timeoutMS int64, arguments string) (text string, failed bool, err error) {
	params, _ := json.Marshal(map[string]any{"name": "persona_settings", "arguments": json.RawMessage(arguments),
		"machine_id": "m1", "session_id": "s1"})
	var res proto.ToolResult
	if err := p.peer.Call(ctx, assistantwire.MethodApp, assistantwire.AppCall{AppID: assistantwire.AppSettings,
		Method: assistantwire.AppTool, Params: params, TimeoutMS: timeoutMS}, &res); err != nil {
		return "", true, err
	}
	return textOf(res), res.IsError, nil
}

// hubCallOf is the method of an extensions.hub_call the hub sent ("": another call).
func hubCallOf(method string, params json.RawMessage) string {
	if method != extwire.MethodHubCall {
		return ""
	}
	var c extwire.HubCall
	_ = json.Unmarshal(params, &c)
	return c.Method
}

// managerCard is the manager's side of a change of one of its own settings, as extmgr's toolPersonaSettings does it:
// a card that waits for the user for fifteen minutes, is withdrawn when its caller stops waiting, and whose change
// is made only on an approval that finds it still up.
type managerCard struct {
	approveAfter time.Duration // when the user approves; 0: never

	mu                 sync.Mutex
	start              time.Time
	shown              bool
	withdrawn, applied time.Duration // since start; 0: it did not happen
}

const inboxRulesChanged = "Inbox rules updated; it applies from the next items sorted."

// answer plays the manager: it lists inbox.rules as its setting, and holds the card of a change to it.
func (c *managerCard) answer(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch hubCallOf(method, params) {
	case extwire.HubSettingsList:
		return map[string]any{"settings": []extwire.Setting{{Key: "inbox.rules", Title: "Inbox rules", Actions: []string{"get", "set"}}}}, nil
	case extwire.HubSettingsTool:
		c.mu.Lock()
		c.shown = true
		c.mu.Unlock()
		approved := make(<-chan time.Time)
		if c.approveAfter > 0 {
			t := time.NewTimer(time.Until(c.start.Add(c.approveAfter)))
			defer t.Stop()
			approved = t.C
		}
		expired := time.NewTimer(15 * time.Minute)
		defer expired.Stop()
		select {
		case <-approved:
			c.note(&c.applied)
			return map[string]any{"text": inboxRulesChanged}, nil
		case <-expired.C:
			return map[string]any{"text": prompts.RenderSettingUnanswered()}, nil
		case <-ctx.Done():
			c.note(&c.withdrawn)
			return nil, ctx.Err()
		}
	}
	return map[string]any{}, nil
}

func (c *managerCard) note(at *time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*at = time.Since(c.start)
}

// fate is whether the card was shown, and when it was withdrawn and its change made (0: never).
func (c *managerCard) fate() (shown bool, withdrawn, applied time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shown, c.withdrawn, c.applied
}

// hubSettingCard is the card of a change of one of the hub's own settings, as toolPersonaSettings builds it.
func hubSettingCard() proto.Interaction {
	return proto.Interaction{ID: "ix_setting", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, ToolName: "persona_settings",
		SessionTitle: "Persona", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC()}
}

// waitPreparedSettingForTest keeps the original Resolution assertions pointed
// at the production prepared-settings path. It adapts only the result shape;
// every wait, deadline, cancellation and withdrawal still runs in the hub.
func waitPreparedSettingForTest(h *Hub, ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	out, err := h.waitPreparedSetting(ctx, assistantwire.AssistantID, ix, "",
		func(context.Context, proto.Resolution) (string, error) { return "applied", nil }, nil)
	if err != nil {
		return proto.Resolution{}, err
	}
	switch out.Text {
	case "applied":
		allow := true
		return proto.Resolution{Allow: &allow}, nil
	case prompts.RenderSettingUnanswered():
		return proto.Resolution{Dismiss: true}, nil
	default:
		return proto.Resolution{}, fmt.Errorf("unexpected prepared-setting outcome: %+v", out)
	}
}

// askedCard plays the manager for the hub's own card (fleet.ask): the user allows it after allowAfter (0: never), the
// manager ends it at its expiry when expires is set, as its queue does, and it is withdrawn when the hub stops waiting.
type askedCard struct {
	start      time.Time
	allowAfter time.Duration
	expires    bool
	withdrawn  atomic.Int64 // since start, as a time.Duration; 0: never
}

func (c *askedCard) answer(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != assistantwire.MethodFleetAsk {
		return map[string]any{}, nil
	}
	var p struct {
		Interaction proto.Interaction `json:"interaction"`
	}
	_ = json.Unmarshal(params, &p)
	allowed, expired := make(<-chan time.Time), make(<-chan time.Time)
	if c.allowAfter > 0 {
		timer := time.NewTimer(time.Until(c.start.Add(c.allowAfter)))
		defer timer.Stop()
		allowed = timer.C
	}
	if c.expires {
		timer := time.NewTimer(time.Until(p.Interaction.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-allowed:
		yes := true
		return proto.Resolution{Allow: &yes}, nil
	case <-expired:
		return proto.Resolution{Dismiss: true}, nil
	case <-ctx.Done():
		c.withdrawn.Store(int64(time.Since(c.start)))
		return nil, ctx.Err()
	}
}

func (c *askedCard) withdrawnAfter() time.Duration { return time.Duration(c.withdrawn.Load()) }

// The hub's own card: an answer in its fourteenth minute is the user's answer, not a card withdrawn at two.
func TestHubSettingCardWaitsItsWholeLife(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		h.askVia = nil // the hub's own wait: fleet.ask to the manager on the bus
		card := &askedCard{start: time.Now(), allowAfter: 14 * time.Minute, expires: true}
		playManager(t, h, card.answer)
		r, err := waitPreparedSettingForTest(h, context.Background(), hubSettingCard())
		waited := time.Since(card.start)
		synctest.Wait()
		if err != nil || !r.UserAllowed() || waited != 14*time.Minute || card.withdrawnAfter() != 0 {
			t.Errorf("a card good for 15m, allowed after 14m: the hub answered after %s (allowed %v, dismiss %v, err %v); "+
				"the manager's card was withdrawn after %s (0 = never)", waited, r.UserAllowed(), r.Dismiss, err, card.withdrawnAfter())
		}
	})
}

// The hub's own card still ends. Nobody answers: the manager's own expiry is the answer, at the fifteenth minute and
// not before. A manager that says nothing then is given a little longer and told that the hub stopped, so no card is
// left behind.
func TestHubSettingCardEndsWithItsExpiry(t *testing.T) {
	t.Run("the manager expires it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newTestHub(t)
			h.askVia = nil
			card := &askedCard{start: time.Now(), expires: true}
			playManager(t, h, card.answer)
			r, err := waitPreparedSettingForTest(h, context.Background(), hubSettingCard())
			waited := time.Since(card.start)
			synctest.Wait()
			if err != nil || !r.Dismiss || r.UserAllowed() || r.UserDenied() || waited != 15*time.Minute || card.withdrawnAfter() != 0 {
				t.Errorf("a card good for 15m nobody answered: the hub answered after %s (%+v, err %v); the manager was told to "+
					"withdraw it after %s (0 = never)", waited, r, err, card.withdrawnAfter())
			}
		})
	})
	t.Run("the manager says nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newTestHub(t)
			h.askVia = nil
			card := &askedCard{start: time.Now()}
			playManager(t, h, card.answer)
			r, err := waitPreparedSettingForTest(h, context.Background(), hubSettingCard())
			waited := time.Since(card.start)
			synctest.Wait()
			if err != nil || !r.Dismiss || r.UserAllowed() || r.UserDenied() || waited <= 15*time.Minute || waited > 16*time.Minute ||
				card.withdrawnAfter() != waited {
				t.Errorf("a card good for 15m and a manager that says nothing: the hub answered after %s, want a little after 15m "+
					"(%+v, err %v); the manager was told to withdraw it after %s, want then", waited, r, err, card.withdrawnAfter())
			}
		})
	})
}

// A card given no expiry lives as long as the manager lets it (its hook_timeout_minutes): the hub waits for the
// manager's word on it, past two minutes and past the fifteen of a card that has them.
func TestHubCardWithoutExpiryWaitsForTheManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		h.askVia = nil
		card := &askedCard{start: time.Now(), allowAfter: 25 * time.Minute}
		playManager(t, h, card.answer)
		ix := hubSettingCard()
		ix.ExpiresAt = time.Time{}
		r, err := waitPreparedSettingForTest(h, context.Background(), ix)
		waited := time.Since(card.start)
		synctest.Wait()
		if err != nil || !r.UserAllowed() || waited != 25*time.Minute || card.withdrawnAfter() != 0 {
			t.Errorf("a card with no expiry, allowed after 25m: the hub answered after %s (allowed %v, dismiss %v, err %v); "+
				"the manager's card was withdrawn after %s (0 = never)", waited, r.UserAllowed(), r.Dismiss, err, card.withdrawnAfter())
		}
	})
}

func TestHubFleetAskBusWait(t *testing.T) {
	const delay = 3 * time.Minute
	for _, tc := range []struct {
		name                       string
		allowAfter, expiresAfter   time.Duration
		wantDuration, wantWithdraw time.Duration
	}{
		{name: "no expiry", allowAfter: delay, wantDuration: delay},
		{name: "expiry with grace", expiresAfter: delay, wantDuration: delay + cardAnswerGrace, wantWithdraw: delay + cardAnswerGrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				h.askVia = nil
				card := &askedCard{start: time.Now(), allowAfter: tc.allowAfter}
				playManager(t, h, card.answer)
				ix := hubSettingCard()
				ix.ExpiresAt = time.Time{}
				if tc.expiresAfter != 0 {
					ix.ExpiresAt = card.start.Add(tc.expiresAfter).UTC()
				}

				r, err := h.waitInteraction(t.Context(), ix)
				waited := time.Since(card.start)
				synctest.Wait()
				if wantAllow := tc.allowAfter != 0; err != nil || r.UserAllowed() != wantAllow || r.Dismiss == wantAllow ||
					waited != tc.wantDuration || card.withdrawnAfter() != tc.wantWithdraw {
					t.Fatalf("fleet ask after %s = %+v, %v, withdrawn after %s; want allowed %v, wait %s, withdrawal %s",
						waited, r, err, card.withdrawnAfter(), wantAllow, tc.wantDuration, tc.wantWithdraw)
				}
			})
		})
	}
}

// The hub's own card goes with its caller, as before: one that stops waiting after five minutes leaves no card.
func TestHubSettingCardGoesWithItsCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		h.askVia = nil
		card := &askedCard{start: time.Now(), expires: true}
		playManager(t, h, card.answer)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r, err := waitPreparedSettingForTest(h, ctx, hubSettingCard())
		waited := time.Since(card.start)
		synctest.Wait()
		if err != nil || !r.Dismiss || waited != 5*time.Minute || card.withdrawnAfter() != 5*time.Minute {
			t.Errorf("the caller stopped after 5m: the hub answered after %s (%+v, err %v); the manager's card was withdrawn after %s",
				waited, r, err, card.withdrawnAfter())
		}
	})
}

const setInboxRules = `{"action":"set","key":"inbox.rules","value":"newsletters are noise"}`

// One of the manager's settings: its card is the manager's, and an approval in its fourteenth minute is the change
// made and the assistant told so, not "context deadline exceeded" at two minutes and the change made anyway.
func TestManagerSettingChangeWaitsForItsCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		card := &managerCard{start: time.Now(), approveAfter: 14 * time.Minute}
		mgr := playManager(t, h, card.answer)
		text, failed, err := mgr.settingsTool(mgr.ctx, 0, setInboxRules)
		told := time.Since(card.start)
		synctest.Wait()
		_, withdrawn, applied := card.fate()
		if err != nil || failed || text != inboxRulesChanged || told != 14*time.Minute || applied != 14*time.Minute || withdrawn != 0 {
			t.Errorf("approved after 14m: the assistant was told %q after %s (error %v, err %v); the manager made the change after %s "+
				"(0 = never) and withdrew the card after %s (0 = never)", text, told, failed, err, applied, withdrawn)
		}
	})
}

// One of the manager's settings, and the hub stops waiting after five minutes: because the assistant's tool call
// stopped (its caller went away), or at the deadline the call came with. The manager is told, so its card goes then,
// and the approval that comes a minute later changes nothing: what the assistant heard and what happened agree.
func TestManagerSettingCardGoesWhenTheHubStopsWaiting(t *testing.T) {
	for name, tc := range map[string]struct {
		callerStops time.Duration // the manager's app.call is cancelled then (rpc.cancel)
		timeoutMS   int64         // app.call's timeout_ms: the hub's own deadline
	}{
		"the tool call stops":     {callerStops: 5 * time.Minute},
		"the call's own deadline": {timeoutMS: (5 * time.Minute).Milliseconds()},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				card := &managerCard{start: time.Now(), approveAfter: 6 * time.Minute}
				mgr := playManager(t, h, card.answer)
				ctx := mgr.ctx
				if tc.callerStops > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.callerStops)
					defer cancel()
				}
				text, failed, err := mgr.settingsTool(ctx, tc.timeoutMS, setInboxRules)
				told := time.Since(card.start)
				// The rest of the card's fifteen minutes and more: what would still happen has happened.
				rest := time.NewTimer(20 * time.Minute)
				defer rest.Stop()
				<-rest.C
				synctest.Wait()
				shown, withdrawn, applied := card.fate()
				if !shown {
					t.Fatal("the manager was not asked for the change: no card")
				}
				if !failed && err == nil {
					t.Errorf("the assistant was told the change went through: %q", text)
				}
				if told != 5*time.Minute || withdrawn != 5*time.Minute || applied != 0 {
					t.Errorf("the hub stopped waiting after 5m: the assistant was told %q after %s (error %v, err %v); the manager "+
						"withdrew its card after %s (0 = never told) and made the change after %s (0 = never)",
						text, told, failed, err, withdrawn, applied)
				}
			})
		})
	}
}

// Everything else on the bus keeps callBus's two minutes: a call without a deadline of its own, what the hub asks of
// the manager's extensions, and reading one of the manager's settings, which shows no card.
func TestOtherBusCallsKeepTwoMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		const unanswered = "example.unanswered"
		mgr := playManager(t, h, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch call := hubCallOf(method, params); {
			case call == extwire.HubSettingsList:
				return map[string]any{"settings": []extwire.Setting{{Key: "inbox.rules", Title: "Inbox rules", Actions: []string{"get", "set"}}}}, nil
			case call != "" || method == unanswered || method == assistantwire.MethodStart:
				<-ctx.Done() // a manager that does not answer
				return nil, ctx.Err()
			}
			return map[string]any{}, nil
		})
		for _, c := range []struct {
			name string
			call func() error
		}{
			{"a call to the manager", func() error {
				return h.callBus(context.Background(), assistantwire.AssistantID, unanswered, nil, nil)
			}},
			{"starting an assistant conversation", func() error {
				_, err := h.asst().Start(context.Background(), assistantwire.StartParams{Text: "Start a conversation"})
				return err
			}},
			{"what the hub asks of the manager's extensions", func() error {
				return h.extCall(context.Background(), extwire.HubGitHubLogin, map[string]any{}, nil)
			}},
			{"reading one of the manager's settings", func() error {
				text, failed, err := mgr.settingsTool(mgr.ctx, 0, `{"action":"get","key":"inbox.rules"}`)
				if err == nil && failed && strings.Contains(text, context.DeadlineExceeded.Error()) {
					return context.DeadlineExceeded // as the tool's error text says
				}
				return fmt.Errorf("the tool answered %q (error %v, err %v)", text, failed, err)
			}},
		} {
			start := time.Now()
			err := c.call()
			if waited := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || waited != 2*time.Minute {
				t.Errorf("%s: ended after %s with %v, want callBus's deadline after 2m", c.name, waited, err)
			}
		}
	})
}
