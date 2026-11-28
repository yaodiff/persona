package larksync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The sync's routes, under the bots' relayed prefix (/api/v1/bots/feishu/sync…): the view, the schedule, the sync
// bots' registration, the authorization and the bindings. Reads answer any device; changes need a full-access device,
// as the bot's other changes do. Tokens, secrets and codes never leave the manager.

// syncView is GET /api/v1/bots/feishu/sync.
type syncView struct {
	Available    bool          `json:"available"`
	ScheduleHour int           `json:"schedule_hour"`
	IMPoll       bool          `json:"im_poll"`
	Accounts     []accountView `json:"accounts"`
	Bots         []botView     `json:"bots"`
	Bindings     []bindingView `json:"bindings"`
}

type accountView struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Brand string `json:"brand,omitempty"`
	Name  string `json:"name,omitempty"`
}

type botView struct {
	BotID     string `json:"bot_id"`
	Label     string `json:"label,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Role      string `json:"role"`
	Tier      string `json:"tier"`
	Approval  string `json:"approval,omitempty"`
	Primary   bool   `json:"primary"`
	HasSecret bool   `json:"has_secret"`
	// Registering: a registration is under way (its QR step is asked again with the register route).
	Registering bool `json:"registering,omitempty"`
}

type bindingView struct {
	Key           string    `json:"key"`
	BotID         string    `json:"bot_id"`
	AccountID     string    `json:"account_id"`
	Label         string    `json:"label,omitempty"`
	Tier          string    `json:"tier"`
	Enabled       bool      `json:"enabled"`
	Health        string    `json:"health"`
	Error         string    `json:"error,omitempty"`
	GrantedScopes []string  `json:"granted_scopes,omitempty"`
	AuthorizedAt  time.Time `json:"authorized_at,omitzero"`
	ReauthDueDays *int      `json:"reauth_due_days,omitempty"`
	LastRunAt     time.Time `json:"last_run_at,omitzero"`
	NextRunAt     time.Time `json:"next_run_at,omitzero"`
	LastMessages  int       `json:"last_messages"`
	LastDocs      int       `json:"last_docs"`
	Running       bool      `json:"running"`
}

// view is the view of the sync's settings.
func (s *Service) view(ctx context.Context) (syncView, error) {
	st, err := s.load(ctx)
	if err != nil {
		return syncView{}, err
	}
	now := s.clock()
	v := syncView{Available: s.available(), ScheduleHour: st.scheduleHour(), IMPoll: st.imPollEnabled(),
		Accounts: []accountView{}, Bots: []botView{}, Bindings: []bindingView{}}
	for _, a := range st.accounts {
		v.Accounts = append(v.Accounts, accountView{ID: a.AccountID, Label: a.Label, Brand: a.Brand, Name: a.Name})
	}
	appID, secret, domain := s.host.PrimaryApp(ctx)
	if appID != "" {
		v.Bots = append(v.Bots, botView{BotID: larksyncwire.PrimaryBot, AppID: appID, Domain: domain,
			Role: larksyncwire.RoleChatAndSync, Tier: st.primaryTier(), Approval: st.prefs.PrimaryApproval, Primary: true,
			HasSecret: secret != "", Registering: st.prefs.PrimaryRegistration != nil})
	}
	for _, b := range st.bots {
		v.Bots = append(v.Bots, botView{BotID: b.BotID, Label: b.Label, AppID: b.AppID, Domain: b.Domain,
			Role: larksyncwire.RoleSyncOnly, Tier: st.tierOf(b.BotID), Approval: b.Approval, HasSecret: b.SecretEnc != "",
			Registering: b.Registration != nil})
	}
	for _, b := range st.bindings {
		if b.pending() {
			continue
		}
		label := b.Label
		if a := st.account(b.AccountID); label == "" && a != nil {
			label = a.Label
		}
		health := b.Health
		switch {
		case !b.authorized():
			health = larksyncwire.HealthUnauthorized
		case health == "":
			health = larksyncwire.HealthOK
		}
		if health == larksyncwire.HealthOK {
			if approval := st.prefs.PrimaryApproval; b.BotID == larksyncwire.PrimaryBot && approval == larksyncwire.ApprovalAwaiting {
				health = larksyncwire.HealthAwaiting
			} else if bot := st.bot(b.BotID); bot != nil && bot.Approval == larksyncwire.ApprovalAwaiting {
				health = larksyncwire.HealthAwaiting
			}
		}
		bv := bindingView{Key: b.Key, BotID: b.BotID, AccountID: b.AccountID, Label: label, Tier: b.Tier,
			Enabled: b.Enabled, Health: health, Error: b.LastError, GrantedScopes: b.GrantedScopes, AuthorizedAt: b.AuthorizedAt,
			LastRunAt: b.LastRunAt, NextRunAt: b.NextRunAt, LastMessages: b.LastMessages, LastDocs: b.LastDocs,
			Running: s.runtime(b.Key).running.Load()}
		if bv.Tier == "" {
			bv.Tier = st.tierOf(b.BotID)
		}
		if b.authorized() {
			days := max(daysLeft(b, now), 0)
			bv.ReauthDueDays = &days
		}
		v.Bindings = append(v.Bindings, bv)
	}
	return v, nil
}

// checkWriter says whether the request may change the sync (a full-access device, not an extension); it answers
// 403 when not.
func (s *Service) checkWriter(w http.ResponseWriter, r *http.Request) bool {
	if !s.host.CanWrite(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("this device can only read and answer"))
		return false
	}
	return true
}

// writeSyncErr answers err with its status.
func writeSyncErr(w http.ResponseWriter, _ *http.Request, err error) {
	code, slug := http.StatusBadGateway, "failed"
	switch {
	case errors.Is(err, errUnavailable):
		code, slug = http.StatusServiceUnavailable, "unavailable"
	case errors.Is(err, errUnread), errors.Is(err, errSettingsSave):
		code, slug = http.StatusServiceUnavailable, "settings_unavailable"
	case errors.Is(err, errNoBot), errors.Is(err, errNoBinding):
		code, slug = http.StatusNotFound, "not_found"
	case errors.Is(err, errNoApp):
		code, slug = http.StatusConflict, "no_app"
	case errors.Is(err, errRedirect):
		code, slug = http.StatusBadRequest, "bad_redirect"
	case errors.Is(err, errState), errors.Is(err, errExchange):
		code, slug = http.StatusBadRequest, "auth_failed"
	case errors.Is(err, errRunning):
		code, slug = http.StatusConflict, "running"
	}
	apierr.WriteCode(w, code, slug, err)
}

// decodeBody reads the request's JSON body into v (an empty body leaves v as it is); false when it answered.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, requestBodyMost)).Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierr.WriteCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
				fmt.Errorf("request body is larger than %d bytes", tooLarge.Limit))
			return false
		}
		apierr.Write(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// writeView answers the view.
func (s *Service) writeView(w http.ResponseWriter, r *http.Request) {
	v, err := s.view(r.Context())
	if err != nil {
		writeSyncErr(w, r, err)
		return
	}
	apierr.WriteJSON(w, v)
}

// Routes registers the Lark user sync HTTP routes on api.
func (s *Service) Routes(api *http.ServeMux) {
	const base = "/api/v1/bots/feishu/sync"
	api.HandleFunc("GET "+base, s.writeView)
	api.HandleFunc("PUT "+base, func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			ScheduleHour *int  `json:"schedule_hour"`
			IMPoll       *bool `json:"im_poll"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.ScheduleHour != nil && (*in.ScheduleHour < 0 || *in.ScheduleHour > 23) {
			apierr.WriteCode(w, http.StatusBadRequest, "bad_hour", errors.New("schedule_hour is 0 to 23"))
			return
		}
		if in.ScheduleHour != nil || in.IMPoll != nil {
			now := s.clock()
			if _, err := s.change(r.Context(), func(st *syncState) error {
				if in.ScheduleHour != nil {
					v := *in.ScheduleHour
					st.prefs.ScheduleHour = &v
					for i := range st.bindings {
						if b := &st.bindings[i]; b.authorized() && b.Failures == 0 && b.NextRunAt.After(now) {
							b.NextRunAt = nextSlot(now, v, b.Key)
						}
					}
				}
				if in.IMPoll != nil {
					v := *in.IMPoll
					st.prefs.IMPoll = &v
				}
				return nil
			}); err != nil {
				writeSyncErr(w, r, err)
				return
			}
		}
		s.writeView(w, r)
	})
	api.HandleFunc("POST "+base+"/bots", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			Label  string `json:"label"`
			Tier   string `json:"tier"`
			Region string `json:"region"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if !s.available() || s.host.Registrar() == nil {
			writeSyncErr(w, r, errUnavailable)
			return
		}
		if !larksyncwire.ValidTier(in.Tier) {
			in.Tier = larksyncwire.TierDocs
		}
		id := "b" + randomHex(5)
		now := s.clock()
		if _, err := s.change(r.Context(), func(st *syncState) error {
			st.bots = append(st.bots, syncBot{BotID: id, Label: cutUTF8(strings.TrimSpace(in.Label), 80),
				Role: larksyncwire.RoleSyncOnly, Tier: in.Tier, CreatedAt: now})
			return nil
		}); err != nil {
			writeSyncErr(w, r, err)
			return
		}
		p, err := s.beginRegistration(r.Context(), id, in.Tier, in.Region, "")
		if err != nil {
			// A bot that never got an app is not kept.
			_, _ = s.change(context.WithoutCancel(r.Context()), func(st *syncState) error {
				st.bots = slices.DeleteFunc(st.bots, func(b syncBot) bool { return b.BotID == id })
				return nil
			})
			writeSyncErr(w, r, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"bot_id": id, "prompt": p})
	})
	api.HandleFunc("POST "+base+"/bots/{bot}/register", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			Step string `json:"step"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		p, err := s.pollRegistration(r.Context(), r.PathValue("bot"))
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		apierr.WriteJSON(w, p)
	})
	api.HandleFunc("POST "+base+"/bots/{bot}/scopes", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			Tier string `json:"tier"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if !larksyncwire.ValidTier(in.Tier) {
			in.Tier = larksyncwire.TierDocs
		}
		bot := r.PathValue("bot")
		st, err := s.load(r.Context())
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		app, err := s.app(r.Context(), &st, bot)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		region := brand(app.Domain)
		p, err := s.beginRegistration(r.Context(), bot, in.Tier, region, app.AppID)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		apierr.WriteJSON(w, p)
	})
	api.HandleFunc("DELETE "+base+"/bots/{bot}", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		bot := r.PathValue("bot")
		if bot == larksyncwire.PrimaryBot {
			apierr.WriteCode(w, http.StatusConflict, "primary", errors.New("the chat bot is removed in its own settings"))
			return
		}
		var dropped []string
		if _, err := s.change(r.Context(), func(st *syncState) error {
			if st.bot(bot) == nil {
				return errNoBot
			}
			st.bots = slices.DeleteFunc(st.bots, func(b syncBot) bool { return b.BotID == bot })
			st.bindings = slices.DeleteFunc(st.bindings, func(b syncBinding) bool {
				if b.BotID == bot && !b.pending() {
					dropped = append(dropped, b.Key)
				}
				return b.BotID == bot
			})
			return nil
		}); err != nil {
			writeSyncErr(w, r, err)
			return
		}
		s.forget(r.Context(), dropped...)
		s.host.Audit(r.Context(), auditRevoke, map[string]any{"bot_id": bot})
		s.writeView(w, r)
	})
	api.HandleFunc("POST "+base+"/bots/{bot}/authorize", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			RedirectURI string `json:"redirect_uri"`
			Tier        string `json:"tier"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		out, err := s.authorize(r.Context(), r.PathValue("bot"), in.RedirectURI, in.Tier)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
	api.HandleFunc("POST "+base+"/bots/{bot}/authorize/poll", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		p, err := s.pollDevice(r.Context(), r.PathValue("bot"))
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		apierr.WriteJSON(w, p)
	})
	api.HandleFunc("POST "+base+"/oauth", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			State string `json:"state"`
			Code  string `json:"code"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err := s.callback(r.Context(), in.State, in.Code); err != nil {
			writeSyncErr(w, r, err)
			return
		}
		s.writeView(w, r)
	})
	api.HandleFunc("PUT "+base+"/bindings/{key}", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		var in struct {
			Enabled *bool   `json:"enabled"`
			Label   *string `json:"label"`
			Tier    *string `json:"tier"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Tier != nil && !larksyncwire.ValidTier(*in.Tier) {
			apierr.WriteCode(w, http.StatusBadRequest, "bad_tier", errors.New("unknown tier"))
			return
		}
		key := r.PathValue("key")
		now := s.clock()
		if _, err := s.change(r.Context(), func(st *syncState) error {
			b := st.binding(key)
			if b == nil || b.pending() {
				return errNoBinding
			}
			if in.Enabled != nil {
				if *in.Enabled && !b.Enabled && b.NextRunAt.Before(now) {
					b.NextRunAt = nextSlot(now, st.scheduleHour(), key)
				}
				b.Enabled = *in.Enabled
			}
			if in.Label != nil {
				b.Label = cutUTF8(strings.TrimSpace(*in.Label), 80)
			}
			if in.Tier != nil {
				b.Tier = *in.Tier
			}
			return nil
		}); err != nil {
			writeSyncErr(w, r, err)
			return
		}
		s.writeView(w, r)
	})
	api.HandleFunc("DELETE "+base+"/bindings/{key}", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		key := r.PathValue("key")
		if _, err := s.change(r.Context(), func(st *syncState) error {
			if b := st.binding(key); b == nil || b.pending() {
				return errNoBinding
			}
			st.bindings = slices.DeleteFunc(st.bindings, func(b syncBinding) bool { return b.Key == key })
			return nil
		}); err != nil {
			writeSyncErr(w, r, err)
			return
		}
		s.forget(r.Context(), key)
		s.host.Audit(r.Context(), auditRevoke, map[string]any{"binding": key})
		s.writeView(w, r)
	})
	api.HandleFunc("POST "+base+"/bindings/{key}/run", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkWriter(w, r) {
			return
		}
		if !s.available() {
			writeSyncErr(w, r, errUnavailable)
			return
		}
		key := r.PathValue("key")
		st, err := s.load(r.Context())
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		if b := st.binding(key); b == nil || !b.authorized() {
			writeSyncErr(w, r, errNoBinding)
			return
		}
		base := s.host.RunContext()
		if base == nil {
			base = context.WithoutCancel(r.Context())
		}
		s.startRun(base, key) // a run under way already is the run asked for
		s.writeView(w, r)
	})
}

// forget drops what the sync kept for removed bindings: their cursors and runtimes. What they read stays
// in the knowledge base.
func (s *Service) forget(ctx context.Context, keys ...string) {
	store := s.host.SyncStore()
	for _, k := range keys {
		if store != nil {
			if err := store.DeleteLarkSyncCursors(context.WithoutCancel(ctx), k); err != nil {
				s.host.Logf("lark sync: %s: drop cursors: %v", k, err)
			}
		}
		s.dropRuntime(k)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cutUTF8 is s cut to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Ensure the connector's interfaces stay what the knowledge base asks for.
var (
	_ connectorwire.Archive      = syncConnector{}
	_ connectorwire.ArchiveMedia = syncConnector{}
	_ connectorwire.Items        = syncConnector{}
	_ connectorwire.Accounts     = syncConnector{}
)
