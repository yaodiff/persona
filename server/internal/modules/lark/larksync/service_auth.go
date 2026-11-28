package larksync

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Authorizing a bot's app to read as the owner (OAuth with PKCE and offline_access, so the refresh token lasts a year),
// on the same page right after the registration. The redirect URL (the web app's /lark-oauth) must be on the app:
// the module adds it with the application/v7 config patch. An app without the patch permission falls back to the
// device flow (a QR code, no redirect needed), and when that is not offered either, to the owner pasting the redirect
// URL into the app's security settings (the page links straight there) and then the browser. Whatever the mode, the
// answer names the account authorized, and it becomes (or renews) the (account, bot) binding.

// Authorization modes (the authorize route's answer, syncBinding.AuthMode).
const (
	modeBrowser = "browser"
	modeDevice  = "device"
	modeManual  = "manual"
)

// authorizeAnswer is the authorize route's answer.
type authorizeAnswer struct {
	Mode        string                `json:"mode"`
	URL         string                `json:"url,omitempty"`
	Prompt      *connectorwire.Prompt `json:"prompt,omitempty"`
	RedirectURI string                `json:"redirect_uri,omitempty"`
	ConsoleURL  string                `json:"console_url,omitempty"`
}

var (
	errRedirect = i18n.Err("hub-larksync.bad_redirect")
	errState    = i18n.Err("hub-larksync.auth_expired")
	errExchange = i18n.Err("hub-larksync.auth_failed")
)

// validRedirect says whether raw is an absolute http(s) URL of the web app's /lark-oauth page: at the root, or
// under the path prefix of a hub served behind a reverse proxy ("/persona/lark-oauth").
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		(u.Path == "/lark-oauth" || strings.HasSuffix(u.Path, "/lark-oauth")) && !strings.Contains(u.Path, "..") &&
		u.User == nil && u.Fragment == "" && u.RawQuery == ""
}

// newVerifier is a new PKCE code verifier (43 characters of base64url, RFC 7636) and state (64 hex).
func newVerifier() (verifier, state string) {
	v := make([]byte, 32)
	_, _ = rand.Read(v)
	st := make([]byte, 32)
	_, _ = rand.Read(st)
	return base64.RawURLEncoding.EncodeToString(v), hex.EncodeToString(st)
}

// pendingKey is bot's authorization under way.
func pendingKey(botID string) string { return botID + pendingTail }

// authorize starts authorizing botID's app as the owner with tier ("": the bot's).
func (s *Service) authorize(ctx context.Context, botID, redirect, tier string) (authorizeAnswer, error) {
	if !validRedirect(redirect) {
		return authorizeAnswer{}, errRedirect
	}
	ua := s.getUserAuth()
	if ua == nil {
		return authorizeAnswer{}, errUnavailable
	}
	st, err := s.load(ctx)
	if err != nil {
		return authorizeAnswer{}, err
	}
	app, err := s.app(ctx, &st, botID)
	if err != nil {
		return authorizeAnswer{}, err
	}
	if !larksyncwire.ValidTier(tier) {
		tier = st.tierOf(botID)
	}
	scopes := larksyncwire.ScopesFor(tier)
	configured := st.prefs.PrimaryRedirect
	if b := st.bot(botID); b != nil {
		configured = b.Redirect
	}
	mode := modeBrowser
	consoleURL := ""
	// A known redirect does not imply the app has scopes added by a newer reader. Reconfigure a binding
	// reporting missing scopes when the owner explicitly starts authorization again.
	missingScopes := slices.ContainsFunc(st.bindings, func(b syncBinding) bool {
		return b.BotID == botID && b.Health == larksyncwire.HealthScopeMissing
	})
	if configured != redirect || missingScopes {
		rep := ua.ConfigureRedirect(ctx, app, redirect, scopes)
		if rep.Detail != "" {
			s.host.Logf("lark sync: %s: configure: %s", botID, rep.Detail)
		}
		switch {
		case rep.Configured:
			approval := larksyncwire.ApprovalReady
			if rep.Awaiting {
				approval = larksyncwire.ApprovalAwaiting
			}
			if _, err := s.change(ctx, func(st *syncState) error {
				if botID == larksyncwire.PrimaryBot {
					st.prefs.PrimaryRedirect, st.prefs.PrimaryApproval = redirect, approval
					return nil
				}
				b := st.bot(botID)
				if b == nil {
					return errNoBot
				}
				b.Redirect, b.Approval = redirect, approval
				return nil
			}); err != nil {
				return authorizeAnswer{}, err
			}
		case rep.MissingPatch:
			mode = modeDevice
			consoleURL = rep.ConsoleURL
		default:
			mode = modeManual
			consoleURL = rep.ConsoleURL
		}
		if consoleURL == "" {
			if base := botcard.AppConsoleURL(app.Domain, app.AppID); base != "" {
				consoleURL = base + "/safe"
			}
		}
		if mode != modeBrowser {
			_, _ = s.change(ctx, func(st *syncState) error {
				if botID == larksyncwire.PrimaryBot {
					st.prefs.PrimaryApproval = larksyncwire.ApprovalDegraded
				} else if b := st.bot(botID); b != nil {
					b.Approval = larksyncwire.ApprovalDegraded
				}
				return nil
			})
		}
	}
	now := s.clock()
	pending := syncBinding{Key: pendingKey(botID), BotID: botID, Tier: tier, AuthStartedAt: now}
	if mode == modeDevice {
		ch, err := ua.DeviceBegin(ctx, app, scopes)
		if err != nil {
			s.host.Logf("lark sync: %s: device flow: %v", botID, err)
			mode = modeManual
		} else {
			sealed, err := s.seal(ch.DeviceCode)
			if err != nil {
				return authorizeAnswer{}, err
			}
			expires := time.Duration(ch.ExpiresIn) * time.Second
			if expires <= 0 {
				expires = authFor
			}
			pending.AuthMode, pending.AuthDeviceEnc = modeDevice, sealed
			pending.AuthInterval, pending.AuthExpiresAt = max(ch.Interval, registerInterval), now.Add(expires)
			if err := s.putPending(ctx, pending); err != nil {
				return authorizeAnswer{}, err
			}
			s.host.Audit(ctx, auditAuthorize, map[string]any{"bot_id": botID, "mode": mode, "tier": tier})
			p := s.devicePrompt(ctx, ch.URL, ch.UserCode, pending.AuthInterval, "")
			return authorizeAnswer{Mode: mode, Prompt: &p}, nil
		}
	}
	verifier, state := newVerifier()
	sealed, err := s.seal(verifier)
	if err != nil {
		return authorizeAnswer{}, err
	}
	pending.AuthMode, pending.AuthState, pending.AuthVerifierEnc, pending.AuthRedirect = mode, state, sealed, redirect
	pending.AuthExpiresAt = now.Add(authFor)
	if err := s.putPending(ctx, pending); err != nil {
		return authorizeAnswer{}, err
	}
	s.host.Audit(ctx, auditAuthorize, map[string]any{"bot_id": botID, "mode": mode, "tier": tier})
	out := authorizeAnswer{Mode: mode, URL: ua.AuthorizeURL(larksyncwire.AuthRequest{App: app, RedirectURI: redirect,
		Scopes: scopes, State: state, Verifier: verifier})}
	if mode == modeManual {
		out.RedirectURI, out.ConsoleURL = redirect, consoleURL
	}
	return out, nil
}

// putPending keeps p as its bot's authorization under way, replacing an older one.
func (s *Service) putPending(ctx context.Context, p syncBinding) error {
	_, err := s.change(ctx, func(st *syncState) error {
		st.bindings = slices.DeleteFunc(st.bindings, func(b syncBinding) bool { return b.Key == p.Key })
		st.bindings = append(st.bindings, p)
		return nil
	})
	return err
}

// devicePrompt is the device flow's QR step.
func (s *Service) devicePrompt(ctx context.Context, link, userCode string, interval int, errText string) connectorwire.Prompt {
	l := s.host.BotLang(ctx)
	p := connectorwire.Prompt{Step: stepAuthorize, Kind: connectorwire.PromptQRCode, URL: link, UserCode: userCode, Interval: interval,
		Hint: i18n.T(l, "hub-larksync.device_hint"), Link: link, LinkText: i18n.T(l, "hub-larksync.device_open_link"), Error: errText}
	if err := withQR(&p); err != nil {
		s.host.Logf("lark sync: device QR: %v", err)
	}
	return p
}

// withQR draws a QR step's code for the app from its URL.
func withQR(p *connectorwire.Prompt) error {
	p.QR = nil
	if p.Kind != connectorwire.PromptQRCode {
		return nil
	}
	if p.URL == "" {
		return errors.New("a QR sign-in step without a URL")
	}
	code, err := qr.Encode(p.URL, qr.L)
	if err != nil {
		return err
	}
	rows := make([]string, code.Size)
	var b strings.Builder
	for y := range code.Size {
		b.Reset()
		for x := range code.Size {
			if code.Black(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	p.QR = &connectorwire.QRCode{Size: code.Size, Rows: rows}
	return nil
}

// callback finishes a browser authorization: the page the platform redirected to hands back state and code.
func (s *Service) callback(ctx context.Context, state, code string) error {
	if state == "" || code == "" {
		return errState
	}
	ua := s.getUserAuth()
	if ua == nil {
		return errUnavailable
	}
	st, err := s.load(ctx)
	if err != nil {
		return err
	}
	var p *syncBinding
	for i := range st.bindings {
		b := &st.bindings[i]
		if b.pending() && b.AuthState != "" && subtle.ConstantTimeCompare([]byte(b.AuthState), []byte(state)) == 1 {
			p = b
			break
		}
	}
	if p == nil || !s.clock().Before(p.AuthExpiresAt) {
		return errState
	}
	verifier, err := s.open(p.AuthVerifierEnc)
	if err != nil {
		return errState
	}
	app, err := s.app(ctx, &st, p.BotID)
	if err != nil {
		return err
	}
	tok, err := ua.Exchange(ctx, app, code, p.AuthRedirect, verifier)
	if err != nil {
		s.host.Logf("lark sync: %s: exchange: %v", p.BotID, err)
		return errExchange
	}
	_, err = s.adopt(ctx, p.BotID, p.Key, p.Tier, app, tok)
	return err
}

// pollDevice asks about bot's device authorization: still waiting, failed (the authorization under way is
// dropped), or done: the account is bound and the prompt is done.
func (s *Service) pollDevice(ctx context.Context, botID string) (connectorwire.Prompt, error) {
	ua := s.getUserAuth()
	if ua == nil {
		return connectorwire.Prompt{}, errUnavailable
	}
	st, err := s.load(ctx)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	p := st.binding(pendingKey(botID))
	if p == nil || p.AuthMode != modeDevice {
		return connectorwire.Prompt{}, errState
	}
	l := s.host.BotLang(ctx)
	drop := func(key string) (connectorwire.Prompt, error) {
		_, _ = s.change(ctx, func(st *syncState) error {
			st.bindings = slices.DeleteFunc(st.bindings, func(b syncBinding) bool { return b.Key == p.Key })
			return nil
		})
		return connectorwire.Prompt{Step: stepAuthorize, Error: i18n.T(l, key)}, nil
	}
	if !s.clock().Before(p.AuthExpiresAt) {
		return drop("hub-larksync.auth_expired")
	}
	code, err := s.open(p.AuthDeviceEnc)
	if err != nil || code == "" {
		return drop("hub-larksync.auth_failed")
	}
	app, err := s.app(ctx, &st, botID)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	res, err := ua.DevicePoll(ctx, app, code)
	if ctx.Err() != nil {
		return connectorwire.Prompt{}, ctx.Err()
	}
	again := connectorwire.Prompt{Step: stepAuthorize, Interval: p.AuthInterval}
	if err != nil { // the network: asked again on the next poll
		s.host.Logf("lark sync: %s: device poll: %v", botID, err)
		return again, nil
	}
	switch res.Status {
	case larksyncwire.DevicePending:
		return again, nil
	case larksyncwire.DeviceSlowDown:
		interval := p.AuthInterval + 5
		_, _ = s.change(ctx, func(st *syncState) error {
			if b := st.binding(p.Key); b != nil {
				b.AuthInterval = interval
			}
			return nil
		})
		again.Interval = interval
		return again, nil
	case larksyncwire.DeviceDone:
		name, err := s.adopt(ctx, botID, p.Key, p.Tier, app, res.Token)
		if err != nil {
			return connectorwire.Prompt{}, err
		}
		return connectorwire.Prompt{Done: true, Account: name}, nil
	case larksyncwire.DeviceExpired:
		return drop("hub-larksync.auth_expired")
	case larksyncwire.DeviceDenied:
		return drop("hub-larksync.auth_denied")
	default:
		s.host.Logf("lark sync: %s: device flow: %s", botID, res.ErrorText)
		return drop("hub-larksync.auth_failed")
	}
}

// adopt binds the account tok belongs to with botID (pending: the authorization under way, dropped): the
// account is kept (who it is, nothing secret), the binding made or renewed with the sealed pair, healthy, and due to
// run soon. It gives the account's label.
func (s *Service) adopt(ctx context.Context, botID, pending, tier string, app larksyncwire.App, tok larksyncwire.Token) (string, error) {
	if tok.OpenID == "" || tok.AccessToken == "" || tok.RefreshToken == "" {
		return "", errExchange
	}
	br := brand(app.Domain)
	accID := accountID(br, tok.TenantKey, tok.OpenID)
	key := bindingKey(botID, accID)
	access, err := s.seal(tok.AccessToken)
	if err != nil {
		return "", err
	}
	refresh, err := s.seal(tok.RefreshToken)
	if err != nil {
		return "", err
	}
	// The binding's refresh lock: a run's refresh must not write the old pair over the new one.
	rt := s.runtime(key)
	rt.refreshMu.Lock()
	defer rt.refreshMu.Unlock()
	rt.unsaved = nil
	now := s.clock()
	label := ""
	_, err = s.change(ctx, func(st *syncState) error {
		a := st.account(accID)
		if a == nil {
			n := len(st.accounts) + 1
			st.accounts = append(st.accounts, syncAccount{AccountID: accID, Brand: br, TenantKey: tok.TenantKey, OpenID: tok.OpenID})
			a = &st.accounts[len(st.accounts)-1]
			a.Label = tok.Name
			if a.Label == "" {
				a.Label = i18n.T(s.host.UserLang(), "hub-larksync.account_n", i18n.Args{"n": n})
			}
		}
		a.UnionID, a.Name = tok.UnionID, tok.Name
		label = a.Label
		st.bindings = slices.DeleteFunc(st.bindings, func(b syncBinding) bool { return b.Key == pending })
		b := st.binding(key)
		if b == nil {
			st.bindings = append(st.bindings, syncBinding{Key: key, BotID: botID, AccountID: accID, Enabled: true})
			b = &st.bindings[len(st.bindings)-1]
		}
		if larksyncwire.ValidTier(tier) {
			b.Tier = tier
		}
		b.AccessTokenEnc, b.RefreshTokenEnc = access, refresh
		b.TokenExpiresAt, b.RefreshExpiresAt = tok.ExpiresAt, tok.RefreshExpiresAt
		b.AuthorizedAt, b.GrantedScopes = now, slices.Clone(tok.Scopes)
		b.Health, b.LastError, b.Failures, b.RemindedDays = larksyncwire.HealthOK, "", 0, nil
		b.NextRunAt = now
		b.NextIMRunAt = now
		return nil
	})
	if err != nil {
		return "", err
	}
	s.host.Audit(ctx, auditAuthorized, map[string]any{"bot_id": botID, "binding": key, "account_id": accID})
	return label, nil
}
