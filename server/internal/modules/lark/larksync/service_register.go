package larksync

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Registering the sync's apps: a new sync-only bot's app, or the user scopes of a tier added to an existing app (a
// sync bot's, or the primary chat bot's), through the accounts site's device-code registration the chat bot's own
// binding uses (bots_register.go). A sync-only app has no events, so nothing is configured after it: the owner goes
// on to authorize it as themselves (service_auth.go). The registration under way is kept in the settings (its device
// code sealed in a top-level field), so a manager restart between two polls loses nothing.

const (
	stepRegister  = "register"
	stepAuthorize = "authorize"
)

// Audit actions of the sync (plugin_audit, channel.Feishu): never a token, a secret or a code.
const (
	auditRegister   = "larksync_register"   // {bot_id, tier, add_to?}: a registration started
	auditRegistered = "larksync_registered" // {bot_id, app_id, tier}: it was confirmed
	auditAuthorize  = "larksync_authorize"  // {bot_id, mode, tier}: an authorization started
	auditAuthorized = "larksync_authorized" // {bot_id, binding, account_id}: an account was authorized
	auditRevoke     = "larksync_revoke"     // {binding} or {bot_id}: a binding or a bot was removed
)

// regOf is bot's registration under way with its sealed device code ("primary": the prefs').
func regOf(st *syncState, botID string) (*syncRegistration, string) {
	if botID == larksyncwire.PrimaryBot {
		return st.prefs.PrimaryRegistration, st.prefs.RegistrationDeviceEnc
	}
	if b := st.bot(botID); b != nil {
		return b.Registration, b.RegistrationDeviceEnc
	}
	return nil, ""
}

// putReg keeps reg (nil: drops it) on bot.
func putReg(st *syncState, botID string, reg *syncRegistration, deviceEnc string) error {
	if reg == nil {
		deviceEnc = ""
	}
	if botID == larksyncwire.PrimaryBot {
		st.prefs.PrimaryRegistration, st.prefs.RegistrationDeviceEnc = reg, deviceEnc
		return nil
	}
	b := st.bot(botID)
	if b == nil {
		return errNoBot
	}
	b.Registration, b.RegistrationDeviceEnc = reg, deviceEnc
	return nil
}

// beginRegistration starts bot's registration with the user scopes of tier, adding them to addTo's app when
// it is set. The bot (a sync bot) must exist in the settings already.
func (s *Service) beginRegistration(ctx context.Context, botID, tier, region, addTo string) (connectorwire.Prompt, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	r := s.host.Registrar()
	if r == nil {
		return connectorwire.Prompt{}, errUnavailable
	}
	if region != "lark" {
		region = "feishu"
	}
	ch, err := r.BeginWith(ctx, region, registerName, larksyncwire.RegisterOptions{UserScopes: larksyncwire.ScopesFor(tier), AppID: addTo})
	if err != nil {
		s.host.Logf("lark sync: registration: %v", err)
		return connectorwire.Prompt{}, i18n.Err("hub-channels.register_failed")
	}
	sealed, err := s.seal(ch.DeviceCode)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	expires := time.Duration(ch.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = registerFor
	}
	reg := syncRegistration{Region: region, URL: ch.URL, Expires: s.clock().Add(expires),
		Interval: max(ch.Interval, registerInterval), Tier: tier, AddTo: addTo}
	if _, err := s.change(ctx, func(st *syncState) error {
		return putReg(st, botID, &reg, sealed)
	}); err != nil {
		return connectorwire.Prompt{}, err
	}
	s.host.Audit(ctx, auditRegister, map[string]any{"bot_id": botID, "tier": tier, "add_to": addTo})
	return s.regPrompt(ctx, reg), nil
}

// regPrompt is the QR step of a registration.
func (s *Service) regPrompt(ctx context.Context, reg syncRegistration) connectorwire.Prompt {
	l := s.host.BotLang(ctx)
	errText := ""
	if reg.Failure != "" {
		errText = i18n.T(l, reg.Failure)
	}
	p := connectorwire.Prompt{Step: stepRegister, Kind: connectorwire.PromptQRCode, URL: reg.URL, Interval: reg.Interval,
		Hint: i18n.T(l, "hub-larksync.register_hint"), Link: reg.URL, LinkText: i18n.T(l, "hub-channels.register_open"), Error: errText}
	if err := withQR(&p); err != nil {
		s.host.Logf("lark sync: registration QR: %v", err)
	}
	return p
}

// pollRegistration asks the accounts site about bot's registration: still waiting, a terminal failure kept
// until the owner starts again, or confirmed: the app (or the scopes added to it) is kept and the prompt is done.
func (s *Service) pollRegistration(ctx context.Context, botID string) (connectorwire.Prompt, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	st, err := s.load(ctx)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	if botID != larksyncwire.PrimaryBot && st.bot(botID) == nil {
		return connectorwire.Prompt{}, errNoBot
	}
	cur, deviceEnc := regOf(&st, botID)
	if cur == nil {
		// Nothing under way: done when the app is there.
		if _, err := s.app(ctx, &st, botID); err == nil {
			return connectorwire.Prompt{Done: true}, nil
		}
		return connectorwire.Prompt{}, errNoApp
	}
	reg := *cur
	keep := func() error {
		_, err := s.change(ctx, func(st *syncState) error { return putReg(st, botID, &reg, deviceEnc) })
		return err
	}
	failed := func(key string) (connectorwire.Prompt, error) {
		reg.Failure = key
		if err := keep(); err != nil {
			return connectorwire.Prompt{}, err
		}
		return s.regPrompt(ctx, reg), nil
	}
	if reg.Failure != "" {
		return s.regPrompt(ctx, reg), nil
	}
	if !s.clock().Before(reg.Expires) {
		return failed("hub-channels.register_expired")
	}
	code, err := s.open(deviceEnc)
	if err != nil || code == "" {
		return failed("hub-channels.register_failed")
	}
	r := s.host.Registrar()
	if r == nil {
		return failed("hub-channels.register_failed")
	}
	t, err := r.Poll(ctx, reg.Region, code)
	if ctx.Err() != nil {
		return connectorwire.Prompt{}, ctx.Err()
	}
	if err != nil { // the network: asked again on the next poll
		s.host.Logf("lark sync: registration poll: %v", err)
		return s.regPrompt(ctx, reg), nil
	}
	switch t.Status {
	case botcard.RegisterDone:
	case botcard.RegisterPending, botcard.RegisterSlowDown, botcard.RegisterFailed:
		if other := t.Brand; (other == "lark" || other == "feishu") && other != reg.Region {
			reg.Region = other // the tenant is on the other cloud: ask that one from now on
			if err := keep(); err != nil {
				return connectorwire.Prompt{}, err
			}
			return s.regPrompt(ctx, reg), nil
		}
		if t.Status == botcard.RegisterFailed {
			s.host.Logf("lark sync: registration: %s", t.ErrorText)
			return failed("hub-channels.register_failed")
		}
		if t.Status == botcard.RegisterSlowDown {
			reg.Interval += 5
			if err := keep(); err != nil {
				return connectorwire.Prompt{}, err
			}
		}
		return s.regPrompt(ctx, reg), nil
	case botcard.RegisterExpired:
		return failed("hub-channels.register_expired")
	case botcard.RegisterDenied:
		return failed("hub-channels.register_denied")
	default:
		return s.regPrompt(ctx, reg), nil
	}
	region := reg.Region
	if t.Brand == "lark" || t.Brand == "feishu" {
		region = t.Brand
	}
	secret := ""
	if botID != larksyncwire.PrimaryBot && t.AppSecret != "" {
		if secret, err = s.seal(t.AppSecret); err != nil {
			return connectorwire.Prompt{}, err
		}
	}
	appID := ""
	if _, err := s.change(ctx, func(st *syncState) error {
		prevTier := st.tierOf(botID)
		if botID == larksyncwire.PrimaryBot {
			// The chat bot's credentials stay as they are: only the scopes were added to its app.
			st.prefs.PrimaryTier, st.prefs.PrimaryApproval = reg.Tier, larksyncwire.ApprovalReady
			st.prefs.PrimaryRedirect = "" // configured again with the new scopes on the next authorization
		} else {
			b := st.bot(botID)
			if b == nil {
				return errNoBot
			}
			if t.AppID != "" && (b.AppID == "" || b.AppID == t.AppID || reg.AddTo == "") {
				b.AppID = t.AppID
			}
			if secret != "" {
				b.SecretEnc = secret
			}
			if b.Domain == "" || reg.AddTo == "" {
				b.Domain = s.host.RegisteredDomain(region)
			}
			b.Tier, b.Approval, b.Redirect = reg.Tier, larksyncwire.ApprovalReady, ""
			appID = b.AppID
		}
		// Existing bindings keep their already-authorized tier until the owner authorizes again with the new scopes.
		if reg.AddTo != "" && prevTier != reg.Tier {
			for i := range st.bindings {
				if bd := &st.bindings[i]; bd.BotID == botID && !bd.pending() && bd.Tier == "" {
					bd.Tier = prevTier
				}
			}
		}
		return putReg(st, botID, nil, "")
	}); err != nil {
		return connectorwire.Prompt{}, err
	}
	s.host.Audit(ctx, auditRegistered, map[string]any{"bot_id": botID, "app_id": appID, "tier": reg.Tier})
	return connectorwire.Prompt{Done: true}, nil
}
