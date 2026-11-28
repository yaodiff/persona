package extmgr

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Binding the Lark bot in one authorization (#4347, docs/extensions/bots.md "Pairing and permissions"): the bind step
// starts the accounts site's app registration, the user opens its page in Lark (or scans it from another device) and
// confirms, and the answer carries the new app's credentials and the confirming person's open id. The credentials are
// saved as if typed in, the app configured as after typing them, and that person becomes the owner: no pairing code.
// The registration is kept in the settings (bots.feishu), so a manager restart between two polls loses nothing; its
// sealed parts are top-level fields there (FeishuBotSettings.RegistrationDeviceEnc / RegistrationSecretEnc), where the
// key rotation re-seals them (db.sealedSettings).

// botRegistration is a registration under way.
type botRegistration struct {
	Region        string    `json:"region"` // "feishu" | "lark": whose accounts site is asked
	DeviceCodeEnc string    `json:"-"`      // sealed: polling with it yields the app's secret (kept in RegistrationDeviceEnc)
	URL           string    `json:"url"`    // the page to open or scan
	Expires       time.Time `json:"expires"`
	Interval      int       `json:"interval"` // seconds between polls
	// Failure keeps a terminal outcome until the user binds again; polling must not create a new app session.
	Failure string `json:"failure,omitempty"` // localization key
	// Once confirmed, what the answer carried, kept until the bind is finished (a failed finish is tried again on
	// the next poll instead of creating another app).
	AppID     string `json:"app_id,omitempty"`
	SecretEnc string `json:"-"` // sealed (kept in RegistrationSecretEnc)
	OpenID    string `json:"open_id,omitempty"`
}

const (
	stepRegister        = "register"
	botRegisterFor      = 10 * time.Minute // when the platform does not say
	botRegisterInterval = 5
	botRegisterName     = "Persona"
)

// acquireFeishuBind orders the whole binding operation, including its finish, so a late Begin/Poll cannot overwrite
// a later bind. The settings mutex remains available while the accounts site or open platform is being called.
func (h *Manager) acquireFeishuBind(ctx context.Context) (func(), error) {
	b := &h.bots
	b.mu.Lock()
	if b.bindGate == nil {
		b.bindGate = make(chan struct{}, 1)
	}
	gate := b.bindGate
	b.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// registrar is the accounts site's client (replaced in tests); nil in a build without the lark module.
func (h *Manager) registrar() botport.Registrar {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.registrar == nil && botport.Available(h.modules()) {
		b.registrar = botport.NewRegistrar("")
	}
	return b.registrar
}

// registeredDomain is the domain a registered app's calls go to: the region itself (tests point it elsewhere).
func (h *Manager) registeredDomain(region string) string {
	if h.bots.regDomain != nil {
		return h.bots.regDomain(region)
	}
	return region
}

// beginFeishuRegistration starts a registration, or goes on with the one under way. ok is false when the accounts
// site cannot be reached: the bind falls back to typing the credentials.
func (h *Manager) beginFeishuRegistration(ctx context.Context, errText string) (connector.Prompt, bool) {
	b := &h.bots
	b.mu.Lock()
	if h.loadBotsLocked(ctx) != nil {
		b.mu.Unlock()
		return connector.Prompt{}, false
	}
	reg := b.set.registration()
	now := b.clock()
	b.mu.Unlock()
	if reg != nil && reg.Failure == "" && (reg.AppID != "" || now.Before(reg.Expires)) {
		return h.registerPrompt(ctx, *reg, ""), true
	}
	if errText == "" && reg != nil && reg.Failure != "" {
		errText = i18n.T(h.botLang(ctx), reg.Failure)
	}
	region := "feishu"
	r := h.registrar()
	if r == nil { // a build without the lark module
		h.logf("bots: registration: %v", botport.ErrUnavailable)
		return connector.Prompt{Error: errText}, false
	}
	// The app also asks for the user scopes of the sync's tier (TierDocs unless the owner chose another), so the
	// owner can authorize it to read as them right after (internal/modules/lark/larksync) without registering again.
	ch, err := r.BeginWith(ctx, region, botRegisterName, larksyncwire.RegisterOptions{UserScopes: larksyncwire.ScopesFor(h.larkSyncPrimaryTier(ctx))})
	if err != nil {
		h.logf("bots: registration: %v", err)
		return connector.Prompt{Error: errText}, false
	}
	if ctx.Err() != nil {
		return connector.Prompt{}, false
	}
	sealed, err := h.sealKey(ch.DeviceCode)
	if err != nil {
		h.logf("bots: registration: %v", err)
		return connector.Prompt{Error: errText}, false
	}
	expires := time.Duration(ch.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = botRegisterFor
	}
	b.mu.Lock()
	now = b.clock()
	b.mu.Unlock()
	next := botRegistration{Region: region, DeviceCodeEnc: sealed, URL: ch.URL, Expires: now.Add(expires), Interval: max(ch.Interval, botRegisterInterval)}
	h.putRegistration(&next)
	return h.registerPrompt(ctx, next, ""), true
}

// registration is the registration under way with its sealed parts, nil when there is none.
func (s FeishuBotSettings) registration() *botRegistration {
	if s.Registration == nil {
		return nil
	}
	r := *s.Registration
	r.DeviceCodeEnc, r.SecretEnc = s.RegistrationDeviceEnc, s.RegistrationSecretEnc
	return &r
}

// putRegistration keeps reg (nil: drops it), its sealed parts in the settings' top-level fields. The bind gate is held.
func (h *Manager) putRegistration(reg *botRegistration) {
	b := &h.bots
	b.mu.Lock()
	if h.loadBotsLocked(context.Background()) != nil {
		b.mu.Unlock()
		return
	}
	b.set.Registration, b.set.RegistrationDeviceEnc, b.set.RegistrationSecretEnc = nil, "", ""
	if reg != nil {
		r := *reg
		b.set.RegistrationDeviceEnc, b.set.RegistrationSecretEnc = r.DeviceCodeEnc, r.SecretEnc
		r.DeviceCodeEnc, r.SecretEnc = "", ""
		b.set.Registration = &r
	}
	h.saveBotSettingsLocked()
	b.mu.Unlock()
}

func (h *Manager) registerPrompt(ctx context.Context, reg botRegistration, errText string) connector.Prompt {
	l := h.botLang(ctx)
	if errText == "" && reg.Failure != "" {
		errText = i18n.T(l, reg.Failure)
	}
	return connector.Prompt{Step: stepRegister, Kind: connector.PromptQRCode, URL: reg.URL, Interval: reg.Interval,
		Hint: i18n.T(l, "hub-channels.register_hint"), Link: reg.URL, LinkText: i18n.T(l, "hub-channels.register_open"), Error: errText}
}

// pollFeishuRegistration is the register step asked again: still waiting, or confirmed and bound.
func (h *Manager) pollFeishuRegistration(ctx context.Context, credentials func(string) connector.Prompt) (connector.Prompt, error) {
	l := h.botLang(ctx)
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return connector.Prompt{}, err
	}
	var reg botRegistration
	cur := b.set.registration()
	live := cur != nil
	if live {
		reg = *cur
	}
	now := b.clock()
	b.mu.Unlock()
	if !live {
		if v := h.botView(ctx); v.HasSecret {
			return h.bindFeishuStep(ctx, "", nil, false)
		}
		return credentials(""), nil
	}
	failed := func(key string) (connector.Prompt, error) {
		reg.Failure = key
		h.putRegistration(&reg)
		if key == "hub-channels.register_failed" {
			return credentials(i18n.T(l, key)), nil
		}
		return h.registerPrompt(ctx, reg, ""), nil
	}
	if reg.Failure != "" {
		if reg.Failure == "hub-channels.register_failed" {
			return credentials(i18n.T(l, reg.Failure)), nil
		}
		return h.registerPrompt(ctx, reg, ""), nil
	}
	if reg.AppID == "" {
		if !now.Before(reg.Expires) {
			return failed("hub-channels.register_expired")
		}
		code, err := h.openKey(reg.DeviceCodeEnc)
		if err != nil {
			return failed("hub-channels.register_failed")
		}
		r := h.registrar()
		if r == nil { // a build without the lark module
			return failed("hub-channels.register_failed")
		}
		t, err := r.Poll(ctx, reg.Region, code)
		if ctx.Err() != nil {
			return connector.Prompt{}, ctx.Err()
		}
		if err != nil { // the network: asked again on the next poll
			h.logf("bots: registration poll: %v", err)
			return h.registerPrompt(ctx, reg, ""), nil
		}
		switch t.Status {
		case botcard.RegisterDone:
			sealed, err := h.sealKey(t.AppSecret)
			if err != nil {
				return connector.Prompt{}, err
			}
			reg.AppID, reg.SecretEnc, reg.OpenID = t.AppID, sealed, t.OpenID
			if t.Brand == "lark" || t.Brand == "feishu" {
				reg.Region = t.Brand // the app lives on the tenant's cloud
			}
			h.putRegistration(&reg)
		case botcard.RegisterPending, botcard.RegisterSlowDown, botcard.RegisterFailed:
			// A tenant on the other cloud is answered there: ask that one from now on.
			if other := t.Brand; (other == "lark" || other == "feishu") && other != reg.Region {
				reg.Region = other
				h.putRegistration(&reg)
				return h.registerPrompt(ctx, reg, ""), nil
			}
			if t.Status == botcard.RegisterFailed {
				h.logf("bots: registration: %s", t.ErrorText)
				return failed("hub-channels.register_failed")
			}
			if t.Status == botcard.RegisterSlowDown {
				reg.Interval += 5
				h.putRegistration(&reg)
			}
			return h.registerPrompt(ctx, reg, ""), nil
		case botcard.RegisterExpired:
			return failed("hub-channels.register_expired")
		case botcard.RegisterDenied:
			return failed("hub-channels.register_denied")
		}
	}
	name, err := h.finishFeishuRegistration(ctx, reg)
	if err != nil {
		h.logf("bots: registration finish: %v", err)
		return h.registerPrompt(ctx, reg, i18n.Localize(err, l)), nil
	}
	h.putRegistration(nil)
	return connector.Prompt{Done: true, Account: firstNonEmpty(name, i18n.T(l, "hub-channels.paired"))}, nil
}

// finishFeishuRegistration binds the registered app: its credentials saved and checked like typed ones, the app
// configured, the confirming person made the owner and greeted in the private chat (which gives its chat id). It
// gives the owner's name.
func (h *Manager) finishFeishuRegistration(ctx context.Context, reg botRegistration) (string, error) {
	secret, err := h.openKey(reg.SecretEnc)
	if err != nil {
		return "", err
	}
	domain := h.registeredDomain(reg.Region)
	on := true
	if err := h.applyBotUpdate(ctx, &on, &reg.AppID, &secret, &domain, nil, true); err != nil {
		return "", err
	}
	// Best-effort, as after typed credentials: the template opened the scopes and the callback; this subscribes the
	// events over the long connection and submits the publish.
	if !botport.Available(h.modules()) {
		return "", botport.ErrUnavailable
	}
	if rep := botport.New(reg.AppID, secret, domain).AutoConfigure(ctx); rep.Detail != "" {
		h.logf("bots: auto-configure: %s", rep.Detail)
	}
	b := &h.bots
	b.mu.Lock()
	api := b.api
	b.mu.Unlock()
	if api == nil {
		return "", errBotOff
	}
	l := h.userLang()
	body := i18n.T(l, "hub-bots_chat.paired_body")
	card := botcard.NewCard(i18n.T(l, "hub-bots_chat.paired"), botcard.Green, i18n.T(l, "hub-bots_chat.paired")).Markdown(body)
	key := "registered-" + reg.AppID // the greeting lands once however often a failed finish is tried again
	msgID, chatID, err := api.SendUser(ctx, reg.OpenID, "interactive", card.JSON(), cutUTF8(key+":c", feishuMaxUUID))
	if botcard.CardRejected(err) {
		msgID, chatID, err = api.SendUser(ctx, reg.OpenID, "text", botcard.TextContent(body), cutUTF8(key+":t", feishuMaxUUID))
	}
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.set.Owner = &BotOwner{OpenID: reg.OpenID, ChatID: chatID, At: b.clock().UTC()}
	b.set.Pairing = nil
	h.saveBotSettingsLocked()
	b.mu.Unlock()
	h.rememberMessageChat(msgID, chatID)
	h.auditBot(ctx, botRecord{auditBotPaired, map[string]any{"open_id": reg.OpenID, "registered": true}})
	name := h.botName(ctx, api, reg.OpenID)
	b.mu.Lock()
	if b.set.Owner != nil && b.set.Owner.OpenID == reg.OpenID {
		b.set.Owner.Name = name
		h.saveBotSettingsLocked()
	}
	b.mu.Unlock()
	// The chat starts afresh; the greeting is already there.
	_ = h.notifyAssistant(assistantwire.EventFeishuOwner, assistantwire.FeishuOwner{Paired: true, Greeted: true})
	return name, nil
}
