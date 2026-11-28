package larksync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/modules/lark/larkauth"
)

// The Lark user sync (docs/extensions/bots.md "Reading as you: daily sync"): the owner authorizes a bot's app to read
// as them, and every day each (account × bot) binding reads what changed into the knowledge base through the
// larksync connector. The module owns the open platform (OAuth and readers) and the sync's state, routes, scheduler
// and connector. The accounts, extra bots and bindings are kept in settings through Host (their tokens sealed in
// top-level fields of list elements, where key rotation re-seals them: db.sealedSettings); the cursors, the dedup
// index and the outbox live in Postgres (larksyncwire.Store).
//
// The primary bot is the chat bot of bots.feishu, never copied here: its app is read from Host.PrimaryApp (the
// hub.json fallback included) whenever it is needed, and only what the sync adds to it (its tier, approval, the
// redirect URL configured on it, a scopes registration under way) lives in bots.feishu.sync. Settings from before
// the sync have none of these keys and keep working unchanged.

// syncRegistrar is the app-registration subset needed from the manager's registrar (matching botport.SyncRegistrar).
type syncRegistrar = interface {
	BeginWith(ctx context.Context, region, name string, opts larksyncwire.RegisterOptions) (botcard.RegisterChallenge, error)
	Poll(ctx context.Context, region, deviceCode string) (botcard.RegisterTicket, error)
}

// Host is what the user sync service asks of the extension manager (matching botport.SyncHost).
type Host interface {
	HasSettings() bool
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
	SyncStore() larksyncwire.Store
	SealSecret(plain string) (string, error)
	OpenSecret(sealed string) (string, error)
	PrimaryApp(ctx context.Context) (appID, secret, domain string)
	Registrar() syncRegistrar
	RegisteredDomain(region string) string
	CanWrite(r *http.Request) bool
	RunContext() context.Context
	Logf(format string, args ...any)
	BotLang(ctx context.Context) i18n.Lang
	UserLang() i18n.Lang
	Audit(ctx context.Context, action string, detail map[string]any)
	KBKick(connectorID string, scope connectorwire.Scope)
	EmitLarkMessages(binding string, msgs []larksyncwire.Message)
	PushLocalized(build func(i18n.Lang) map[string]any, collapseID string)
	IsMember() bool
}

const (
	prefsKey    = larksyncwire.SettingPrefs
	botsKey     = larksyncwire.SettingBots
	bindingsKey = larksyncwire.SettingBindings
	accountsKey = larksyncwire.SettingAccounts

	defaultHour         = 3                    // local time of the daily run when the owner never chose one
	authFor             = 10 * time.Minute     // how long an authorization started on the page stays valid
	authLasts           = 365 * 24 * time.Hour // how long an authorization lasts, at most (the platform's limit)
	refreshAt           = 15 * time.Minute     // an access token closer than this to its end is refreshed first
	backfillWindow      = 30 * 24 * time.Hour  // how far back a binding's first run reads
	runBudget           = 20 * time.Minute     // one full run's budget: what is left is read next time
	imBudget            = 5 * time.Minute      // one lightweight IM incremental run's budget
	imEvery             = 15 * time.Minute     // how often chat-enabled bindings run a lightweight IM incremental sync
	namesBudget         = 90 * time.Second     // one sender-name backfill round's own budget, after the run's commit
	pendingTail         = "~pending"           // the key suffix of a bot's authorization under way
	docsWindow          = 7 * 24 * time.Hour   // how far back the connector's Items reach
	outboxKeep          = 30 * 24 * time.Hour  // how long outbox rows are kept
	pruneEvery          = 24 * time.Hour       // how often the outbox is pruned
	tickEvery           = 5 * time.Minute      // how often the scheduler looks for due bindings
	parallelRuns        = 4                    // bindings run at once, at most
	failSoon            = 15 * time.Minute     // the retry after one of the first failed runs
	partialNext         = time.Hour            // the next run after a partial or rate-limited one
	requestBodyMost     = int64(1 << 20)       // request bodies of the sync's routes
	changesMost         = 100                  // chat batches one Changes call hands over by default
	staleAuth           = authFor + time.Minute
	settingsReadTimeout = 5 * time.Second
	registerFor         = 10 * time.Minute // when the platform does not say
	registerInterval    = 5
	registerName        = "Persona"

	// backfillVersion triggers the one-time idempotent sender-name backfill for outbox messages:
	// v1 resolved contact/v3 names for pre-#15486 rows; v2 adds chat-member, p2p chat-info, and message-carried
	// fallbacks (plus unnamed DM chat name repair) for personal/external tenants where contact/v3 misses; v3 reads a
	// large group's member list on demand past the old 20-page cap, and names members who left the group from the
	// binding's name book and from @mentions around their messages.
	backfillVersion = 3
	// linksBackfillVersion triggers the one-time idempotent remediation that scans already-synced chat
	// outbox messages and enqueues cloud document / wiki links missing body content in lark_sync_index.
	linksBackfillVersion = 1
)

// failBackoff is the wait after the third, fourth and later consecutive failed runs (a binding's circuit).
var failBackoff = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

// reauthReminders are the days before an authorization ends when the owner is reminded, once each.
var reauthReminders = []int{14, 7, 1}

// syncPrefs is bots.feishu.sync: the schedule and what the sync adds to the primary bot. Its sealed field is
// top-level (db.sealedSettings).
type syncPrefs struct {
	ScheduleHour *int `json:"schedule_hour,omitempty"`
	// IMPoll enables the 15-minute lightweight IM incremental sync for chat-enabled (full_with_im) bindings;
	// nil reads as on by default so existing and new bindings get timely chat updates and can turn it off in Settings.
	IMPoll *bool `json:"im_poll,omitempty"`
	// PrimaryTier is the scope tier the primary app asks for as the owner (a first registration included); "" is
	// TierDocs.
	PrimaryTier     string `json:"primary_tier,omitempty"`
	PrimaryApproval string `json:"primary_approval,omitempty"`
	// PrimaryRedirect is the redirect URL configured on the primary app ("" until it is).
	PrimaryRedirect string `json:"primary_redirect,omitempty"`
	// PrimaryRegistration is adding the user scopes to the primary app, under way; its device code is sealed in
	// RegistrationDeviceEnc.
	PrimaryRegistration   *syncRegistration `json:"primary_registration,omitempty"`
	RegistrationDeviceEnc string            `json:"registration_device_enc,omitempty"`
}

// syncRegistration is a registration of a sync bot's app (a new app, or the user scopes added to an existing one)
// under way. The device code is sealed in the owner's registration_device_enc.
type syncRegistration struct {
	Region   string    `json:"region"`
	URL      string    `json:"url"`
	Expires  time.Time `json:"expires"`
	Interval int       `json:"interval"`
	Tier     string    `json:"tier"`
	// AddTo is the app the scopes are added to ("" for a new app).
	AddTo   string `json:"add_to,omitempty"`
	Failure string `json:"failure,omitempty"` // localization key of a terminal outcome
}

// syncBot is one extra bot of bots.feishu.sync.bots: an app only for reading as the owner (no events, no long
// connection), possibly in another tenant or on the other cloud. Its sealed fields are top-level (db.sealedSettings).
type syncBot struct {
	BotID                 string            `json:"bot_id"`
	Label                 string            `json:"label,omitempty"`
	AppID                 string            `json:"app_id,omitempty"`
	SecretEnc             string            `json:"secret_enc,omitempty"`
	Domain                string            `json:"domain,omitempty"`
	Role                  string            `json:"role"`
	Tier                  string            `json:"tier"`
	Approval              string            `json:"approval,omitempty"`
	CreatedAt             time.Time         `json:"created_at,omitzero"`
	Redirect              string            `json:"redirect,omitempty"` // the redirect URL configured on the app
	Registration          *syncRegistration `json:"registration,omitempty"`
	RegistrationDeviceEnc string            `json:"registration_device_enc,omitempty"`
}

// syncBinding is one (account, bot) pair of bots.feishu.sync.bindings, or a bot's authorization under way (Key
// "<bot>~pending", no account yet). Its sealed fields are top-level (db.sealedSettings).
type syncBinding struct {
	Key              string    `json:"key"`
	BotID            string    `json:"bot_id"`
	AccountID        string    `json:"account_id,omitempty"`
	Label            string    `json:"label,omitempty"`
	Tier             string    `json:"tier"`
	Enabled          bool      `json:"enabled"`
	AccessTokenEnc   string    `json:"access_token_enc,omitempty"`
	RefreshTokenEnc  string    `json:"refresh_token_enc,omitempty"`
	TokenExpiresAt   time.Time `json:"token_expires_at,omitzero"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	AuthorizedAt     time.Time `json:"authorized_at,omitzero"`
	GrantedScopes    []string  `json:"granted_scopes,omitempty"`
	Health           string    `json:"health,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	LastRunAt        time.Time `json:"last_run_at,omitzero"`
	NextRunAt        time.Time `json:"next_run_at,omitzero"`
	LastIMRunAt      time.Time `json:"last_im_run_at,omitzero"`
	NextIMRunAt      time.Time `json:"next_im_run_at,omitzero"`
	LastMessages     int       `json:"last_messages,omitempty"`
	LastDocs         int       `json:"last_docs,omitempty"`
	RemindedDays     []int     `json:"reminded_days,omitempty"`
	// Failures counts the consecutive failed runs (the circuit, failBackoff).
	Failures int `json:"failures,omitempty"`
	// BackfillVersion records the highest completed one-time data repair version for this binding (backfillVersion).
	BackfillVersion int `json:"backfill_version,omitempty"`
	// LinksBackfillVersion records the highest completed one-time dropped-links remediation version (linksBackfillVersion).
	LinksBackfillVersion int `json:"links_backfill_version,omitempty"`
	// NamesRounds counts the sender-name backfill rounds run since the binding last completed one (0 once
	// BackfillVersion reaches backfillVersion); NamesWarning is why the last round stopped short ("" when
	// it did not). A round that stops short degrades naming only and never sets Health.
	NamesRounds  int    `json:"names_rounds,omitempty"`
	NamesWarning string `json:"names_warning,omitempty"`
	// An authorization under way: browser (State, the PKCE verifier and the redirect), or device (the device code).
	AuthState       string    `json:"auth_state,omitempty"`
	AuthVerifierEnc string    `json:"auth_verifier_enc,omitempty"`
	AuthRedirect    string    `json:"auth_redirect,omitempty"`
	AuthStartedAt   time.Time `json:"auth_started_at,omitzero"`
	AuthDeviceEnc   string    `json:"auth_device_enc,omitempty"`
	AuthMode        string    `json:"auth_mode,omitempty"`
	AuthInterval    int       `json:"auth_interval,omitempty"`
	AuthExpiresAt   time.Time `json:"auth_expires_at,omitzero"`
}

// pending says whether b is an authorization under way rather than a binding.
func (b syncBinding) pending() bool { return b.AccountID == "" }

// authorized says whether b has a token pair to read with.
func (b syncBinding) authorized() bool {
	return !b.pending() && !b.AuthorizedAt.IsZero() && b.RefreshTokenEnc != ""
}

// authEnds is when b's authorization ends: a year after it was given, or earlier when the platform said so.
func (b syncBinding) authEnds() time.Time {
	end := b.AuthorizedAt.Add(authLasts)
	if !b.RefreshExpiresAt.IsZero() && b.RefreshExpiresAt.Before(end) {
		end = b.RefreshExpiresAt
	}
	return end
}

// syncAccount is one Lark account the owner authorized as (bots.feishu.sync.accounts): who it is, nothing secret.
type syncAccount struct {
	AccountID string `json:"account_id"` // "<brand>:<tenant_key>:<open_id>"
	Label     string `json:"label,omitempty"`
	Brand     string `json:"brand,omitempty"`
	TenantKey string `json:"tenant_key,omitempty"`
	OpenID    string `json:"open_id,omitempty"`
	UnionID   string `json:"union_id,omitempty"`
	Name      string `json:"name,omitempty"`
}

// syncState is the four settings as one value.
type syncState struct {
	prefs    syncPrefs
	bots     []syncBot
	bindings []syncBinding
	accounts []syncAccount
}

func (st *syncState) bot(id string) *syncBot {
	for i := range st.bots {
		if st.bots[i].BotID == id {
			return &st.bots[i]
		}
	}
	return nil
}

func (st *syncState) binding(key string) *syncBinding {
	for i := range st.bindings {
		if st.bindings[i].Key == key {
			return &st.bindings[i]
		}
	}
	return nil
}

func (st *syncState) account(id string) *syncAccount {
	for i := range st.accounts {
		if st.accounts[i].AccountID == id {
			return &st.accounts[i]
		}
	}
	return nil
}

// primaryTier is the tier of the primary app.
func (st *syncState) primaryTier() string {
	if larksyncwire.ValidTier(st.prefs.PrimaryTier) {
		return st.prefs.PrimaryTier
	}
	return larksyncwire.TierDocs
}

// scheduleHour is the local hour of the daily run.
func (st *syncState) scheduleHour() int {
	if h := st.prefs.ScheduleHour; h != nil && *h >= 0 && *h < 24 {
		return *h
	}
	return defaultHour
}

// imPollEnabled says whether high-frequency (15-minute) IM incremental polling is on (default true when unset).
func (st *syncState) imPollEnabled() bool {
	return st.prefs.IMPoll == nil || *st.prefs.IMPoll
}

// Service is the Lark user sync running inside the extension manager: the bindings' runtimes, the settings' lock,
// the routes, the connector and the scheduler.
type Service struct {
	host Host

	// mu orders every read-modify-write of the four settings; never held across a network call.
	mu sync.Mutex
	// last is the state as last read or written, for what answers from memory (the connector's Status).
	last     syncState
	haveLast bool

	rtMu     sync.Mutex
	runtimes map[string]*bindingRuntime

	// regMu orders the sync bots' registration steps (a begin or a poll at a time).
	regMu sync.Mutex

	// Seams (tests): the OAuth client and the readers (nil: the module's own), the clock and the scheduler's tick.
	userAuth  larksyncwire.UserAuth
	newSyncer func() larksyncwire.Syncer
	now       func() time.Time
	tick      <-chan time.Time

	authOnce sync.Once
	auth     larksyncwire.UserAuth

	lastPrune time.Time
}

// NewService constructs a Lark user sync service over host.
func NewService(host Host) *Service {
	return &Service{host: host}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// getUserAuth is the OAuth client.
func (s *Service) getUserAuth() larksyncwire.UserAuth {
	if s.userAuth != nil {
		return s.userAuth
	}
	s.authOnce.Do(func() {
		s.auth = larkauth.NewUserAuth("", "", nil)
	})
	return s.auth
}

// makeSyncer is a new binding's reader.
func (s *Service) makeSyncer() larksyncwire.Syncer {
	if s.newSyncer != nil {
		return s.newSyncer()
	}
	return NewSyncer(nil, s.now)
}

// available says whether the sync can run here: the database keeps the sync's tables and OAuth is available.
func (s *Service) available() bool {
	return s.host != nil && s.host.SyncStore() != nil && s.getUserAuth() != nil
}

var (
	errUnavailable  = i18n.Err("hub-larksync.unavailable")
	errUnread       = i18n.Err("hub-larksync.settings_unavailable")
	errSettingsSave = i18n.Err("hub-bots.settings_save_failed")
	errNoBot        = i18n.Err("hub-larksync.no_bot")
	errNoBinding    = i18n.Err("hub-larksync.no_binding")
	errNoApp        = i18n.Err("hub-larksync.no_app")
	errRunning      = errors.New("lark sync: the binding is running")
)

// seal seals plain with the manager's secret.key through Host: every value the sync seals goes through here, kept in
// a field db.sealedSettings lists.
func (s *Service) seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return s.host.SealSecret(plain)
}

// open opens a sealed value ("" stays "").
func (s *Service) open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	return s.host.OpenSecret(sealed)
}

// loadLocked reads the four settings (s.mu held).
func (s *Service) loadLocked(ctx context.Context) (syncState, error) {
	var st syncState
	if !s.host.HasSettings() {
		return st, nil
	}
	rctx, cancel := context.WithTimeout(ctx, settingsReadTimeout)
	defer cancel()
	for _, r := range []struct {
		key string
		out any
	}{{prefsKey, &st.prefs}, {botsKey, &st.bots}, {bindingsKey, &st.bindings}, {accountsKey, &st.accounts}} {
		if _, err := s.host.Setting(rctx, r.key, r.out); err != nil {
			s.host.Logf("lark sync: read %s: %v", r.key, err)
			return syncState{}, errUnread
		}
	}
	s.last, s.haveLast = st.clone(), true
	return st, nil
}

// load is the sync's settings now.
func (s *Service) load(ctx context.Context) (syncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(ctx)
}

// change reads the settings, lets fn change them and writes back the keys it changed. fn returning an error
// writes nothing. It returns the state as written.
func (s *Service) change(ctx context.Context, fn func(st *syncState) error) (syncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadLocked(ctx)
	if err != nil {
		return st, err
	}
	before := st.clone()
	if err := fn(&st); err != nil {
		return before, err
	}
	if !s.host.HasSettings() {
		s.last, s.haveLast = st.clone(), true
		return st, nil
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, w := range []struct {
		key       string
		old, next any
	}{{prefsKey, before.prefs, st.prefs}, {botsKey, before.bots, st.bots},
		{bindingsKey, before.bindings, st.bindings}, {accountsKey, before.accounts, st.accounts}} {
		a, _ := json.Marshal(w.old)
		b, _ := json.Marshal(w.next)
		if bytes.Equal(a, b) {
			continue
		}
		if err := s.host.PutSetting(wctx, w.key, w.next); err != nil {
			s.host.Logf("lark sync: save %s: %v", w.key, err)
			return before, errSettingsSave
		}
	}
	s.last, s.haveLast = st.clone(), true
	return st, nil
}

// clone is a deep copy of st (the slices the callers change in place).
func (st syncState) clone() syncState {
	out := st
	out.bots = slices.Clone(st.bots)
	for i := range out.bots {
		if r := out.bots[i].Registration; r != nil {
			c := *r
			out.bots[i].Registration = &c
		}
	}
	out.bindings = slices.Clone(st.bindings)
	for i := range out.bindings {
		out.bindings[i].GrantedScopes = slices.Clone(st.bindings[i].GrantedScopes)
		out.bindings[i].RemindedDays = slices.Clone(st.bindings[i].RemindedDays)
	}
	out.accounts = slices.Clone(st.accounts)
	if r := st.prefs.PrimaryRegistration; r != nil {
		c := *r
		out.prefs.PrimaryRegistration = &c
	}
	if h := st.prefs.ScheduleHour; h != nil {
		v := *h
		out.prefs.ScheduleHour = &v
	}
	if p := st.prefs.IMPoll; p != nil {
		v := *p
		out.prefs.IMPoll = &v
	}
	return out
}

// app is bot's app as the readers need it: the primary's from bots.feishu (its hub.json fallback included).
func (s *Service) app(ctx context.Context, st *syncState, botID string) (larksyncwire.App, error) {
	if botID == larksyncwire.PrimaryBot {
		id, secret, domain := s.host.PrimaryApp(ctx)
		if id == "" || secret == "" {
			return larksyncwire.App{}, errNoApp
		}
		return larksyncwire.App{AppID: id, AppSecret: secret, Domain: domain}, nil
	}
	b := st.bot(botID)
	if b == nil {
		return larksyncwire.App{}, errNoBot
	}
	if b.AppID == "" || b.SecretEnc == "" {
		return larksyncwire.App{}, errNoApp
	}
	secret, err := s.open(b.SecretEnc)
	if err != nil {
		return larksyncwire.App{}, err
	}
	return larksyncwire.App{AppID: b.AppID, AppSecret: secret, Domain: b.Domain}, nil
}

// brand is the cloud of a domain: "lark" for Lark's, "feishu" for Feishu's and private deployments.
func brand(domain string) string {
	if botcard.ResolveDomain(domain) == botcard.DomainLark {
		return "lark"
	}
	return "feishu"
}

// accountID is an account's id: its cloud, tenant and open id.
func accountID(b, tenant, openID string) string {
	return b + ":" + tenant + ":" + openID
}

// bindingKey is the stable key of the (bot, account) binding: the account's id is hashed so the key fits a
// path segment and an attachment ref ("|"-separated) whatever the account's id holds.
func bindingKey(botID, accID string) string {
	sum := sha256.Sum256([]byte(accID))
	return botID + "~" + hex.EncodeToString(sum[:6])
}

// tierOf is the tier a bot reads with.
func (st *syncState) tierOf(botID string) string {
	if botID == larksyncwire.PrimaryBot {
		return st.primaryTier()
	}
	if b := st.bot(botID); b != nil && larksyncwire.ValidTier(b.Tier) {
		return b.Tier
	}
	return larksyncwire.TierDocs
}

// PrimaryTier is the tier the primary app's first registration asks the user scopes of (bindFeishu's
// register step); TierDocs when the sync settings cannot be read or were never set.
func (s *Service) PrimaryTier(ctx context.Context) string {
	st, err := s.load(ctx)
	if err != nil {
		return larksyncwire.TierDocs
	}
	return st.primaryTier()
}
