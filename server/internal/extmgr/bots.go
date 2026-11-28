package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The Feishu bot (docs/extensions/bots.md): the user talks to Persona through a Feishu / Lark custom app's bot. The hub keeps its
// transport: the app's credentials, the long connection to the open platform, pairing with its owner, sending and
// changing messages and cards. What the chat does — where a message goes, following its turn and the answer cards,
// what is asked there, the commands, pushes and the sync mirror — is the assistant's (internal/assistant, feishu*.go):
// the owner's messages and card presses go to it over the bus (assistant_channels.go), and it posts through the hub.
// Only the paired user is answered: in the bot's private chat, and in the topics of the group chats the owner has
// opted in (TopicChats, bots_topics.go). A group message from anyone else is answered only when group access allows it,
// and then by a guest that never reaches the owner's conversations (bots_group_access.go, bots_guest.go). Everything is
// off until switched on in the app (Settings › Connectors › Lark bot).

// BotsConfig is hub.json "bots".
type BotsConfig struct {
	Feishu FeishuBotConfig `json:"feishu,omitempty"`
	// Endpoints are the explicit Agents → Bots registrations; no structured session is inferred to be a bot. An entry
	// binds an exact Orca session (session_id) or an Orca bot (bot_id, followed to each new Home Chat).
	Endpoints []agents.BotRegistration `json:"endpoints,omitempty"`
	// Discovery is "off" to find no Orca bots on the computers (orcabots.go); anything else, or nothing, finds them
	// through each computer's local Orca (bots.roster). Endpoints registered by hand are kept either way.
	Discovery string `json:"discovery,omitempty"`
}

// FeishuBotConfig: the app's credentials, for those who would rather keep them in manager.json (0600) than enter them in
// the app. What the app saves wins.
type FeishuBotConfig struct {
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
	Domain    string `json:"domain,omitempty"` // feishu (default) | lark | the open platform's URL
}

// FeishuBotSettings is what the app set, in settings (botSettingsKey).
type FeishuBotSettings struct {
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"app_id,omitempty"`
	SecretEnc string `json:"secret_enc,omitempty"` // sealed with secret.key (secretkey.go sealKey)
	Domain    string `json:"domain,omitempty"`
	// LarkCLIProfile is the app's optional profile choice. An empty value keeps
	// the automatic manager rule; manager.json lark.cli.profile still wins.
	LarkCLIProfile string      `json:"larkcli_profile,omitempty"`
	Features       BotFeatures `json:"features"`
	Owner          *BotOwner   `json:"owner,omitempty"`
	Pairing        *botPairing `json:"pairing,omitempty"`
	// Configure is the sanitized automatic-configuration outcome for these credentials, until paired.
	Configure *botConfigure `json:"configure,omitempty"`
	// Registration is an app registration under way (bots_register.go): binding in one authorization.
	Registration *botRegistration `json:"registration,omitempty"`
	// RegistrationDeviceEnc and RegistrationSecretEnc are the registration's device code and, once confirmed, the new
	// app's secret, sealed with secret.key; top-level so the key rotation re-seals them (db.sealedSettings).
	RegistrationDeviceEnc string `json:"registration_device_enc,omitempty"`
	RegistrationSecretEnc string `json:"registration_secret_enc,omitempty"`
	// TopicChats are the group chats the owner opted in (bots_topics.go): their messages reach the assistant, and the
	// bot may send into them. The owner's own list: it goes when the owner does.
	TopicChats []BotChat `json:"topic_chats,omitempty"`
	// AISources are the messages the hub took as the owner's "@bot /ai …" in a group chat that is not opted in (#6146,
	// bots_topics.go): the only messages of such a group the bot may reply to, and so the only place it writes in one.
	// Kept across restarts (a to-do may be done weeks later, and its answer goes under the message); the oldest are
	// dropped past botAISourcesKeep. Like the topic chats, the owner's: it goes when the owner does.
	AISources []BotAISource `json:"ai_sources,omitempty"`
	// NudgeAfter is how many minutes a card the bot sent may wait unanswered before the assistant nudges under it once
	// (#6084, modules/lark/nudge.go): 0 is off, nil (never set) is assistantwire.NudgeAfterDefault.
	NudgeAfter *int `json:"nudge_after,omitempty"`
	// SourceReportDelayMinutes is the preview window before a reply to the source group. Zero skips it.
	SourceReportDelayMinutes *int `json:"source_report_delay_minutes,omitempty"`
	SourceReportFullReply    bool `json:"source_report_full_reply,omitempty"`
	// Handoff is the proxy-reply policy (bots_handoff.go). The owner's: it goes when the owner does.
	Handoff *botHandoffPolicy `json:"handoff,omitempty"`
	// GroupAccess is who else may @ the bot in a group (bots_group_access.go). nil is owner_only.
	GroupAccess *botGroupAccess `json:"group_access,omitempty"`
	// GuestSources are the non-owner @bot messages the hub took (bots_guest.go): each may get one reply.
	GuestSources []botGuestSource `json:"guest_sources,omitempty"`
}

// BotFeatures are the parts that can be switched on one by one: kept here with the credentials, acted on by the
// assistant (assistantwire.FeishuFeatures is the same).
type BotFeatures struct {
	Main     bool `json:"main"`      // messages go to Persona: the main conversation, unless the chat switched to another
	Cards    bool `json:"cards"`     // questions and approvals of what the chat drives show there as cards
	AllCards bool `json:"all_cards"` // and those of every agent and conversation
	Pushes   bool `json:"pushes"`    // notify_user and scheduled tasks' results go there too
	Sync     bool `json:"sync"`      // the main conversation's turns from elsewhere (the app, a task) are mirrored there
	Agents   bool `json:"agents"`    // coding agents on every computer can be driven from the chat
	// Reports: how the main conversation's tasks go (done, waiting for the user, cut off) is told in the chat, a few
	// at a time (#2768). On by default, and for settings saved before it: they have no such key.
	Reports bool `json:"reports"`
	// Board: /tasks keeps one standing "waiting for you" card in the chat and edits it in place (#6052). Off by default
	// (it changes what /tasks does), and for settings saved before it: they have no such key.
	Board bool `json:"board"`
	// Workbench: the workbench (#1961): /home, /today, /inbox, the board pinned as a home card, a workbench button on
	// every card, a permission field in /start, companion bots. Off by default, and for settings saved before it.
	Workbench bool `json:"workbench"`
	// OnlyConclusions: of the task reports only the conclusions are pushed; a finished turn, waiting and interrupted stay
	// in the task list and the briefing (#1226). Off by default, and for settings saved before it: they have no such key.
	OnlyConclusions bool `json:"only_conclusions"`
	// ReportSteps: the task report also pushes the execution steps — each finished turn with the stage bar and what the
	// agent said, interrupted, closed — as it did before #3218. Off by default, and for settings saved before it: then
	// only goal-level changes go out (reached, needs your decision, stuck).
	ReportSteps bool `json:"report_steps"`
	// AnswerNewCard: a finished run's answer is posted as a new card at the bottom of the chat (Lark notifies on it) and
	// the progress card is folded, instead of the answer being written into the progress card (#5396). Off by default,
	// and for settings saved before it: they have no such key.
	AnswerNewCard bool `json:"answer_new_card"`
	// TopicCot: a topic run also shows its thinking and tools in a native agent message, beside the progress card.
	// On by default, including settings saved before it; off keeps the card's folded process panel.
	TopicCot bool `json:"topic_cot"`
	// DMCot: a private-chat run also shows its thinking and tools in a native agent message, beside the progress card
	// (#16847). Off by default, and for settings saved before it: they have no such key.
	DMCot bool `json:"dm_cot"`
	// CotAnswer: a run whose agent message is allowed (dm_cot, topic_cot) gets its answer inside that message and no
	// progress or answer card; cards take over when the message cannot carry it (#16847). Off by default, and for
	// settings saved before it.
	CotAnswer bool `json:"cot_answer"`
	// ReadGroup: the owner's "@bot /read <task>" in a group the bot is in reads the group's recent messages, images and
	// files as the owner, then does the task and answers under that message (#11803, bots_read.go). Off by default, and
	// for settings saved before it: they have no such key.
	ReadGroup bool `json:"read_group"`
	// ReadGroupDirect: a /read's answer goes straight under the owner's message in the group, as it did before #14437.
	// Off by default, and for settings saved before it (they have no such key): the answer goes to the owner's private
	// chat first, and into the group only when the owner presses its button (the lark module's read_held.go).
	ReadGroupDirect bool `json:"read_group_direct"`
}

var defaultBotFeatures = BotFeatures{Main: true, Cards: true, Reports: true, TopicCot: true}

// botFeatureUpdate keeps partial settings intent separate from an internal full replacement.
// A patch is validated by its entry point, then merged into current features under bots.mu.
type botFeatureUpdate struct {
	replace *BotFeatures
	patch   json.RawMessage
}

func (u botFeatureUpdate) apply(current BotFeatures) (BotFeatures, error) {
	if u.replace != nil {
		return *u.replace, nil
	}
	err := json.Unmarshal(u.patch, &current)
	return current, err
}

// BotOwner is the paired user: the only one the bot answers, and where pushes go.
type BotOwner struct {
	OpenID string    `json:"open_id"`
	ChatID string    `json:"chat_id"`
	Name   string    `json:"name,omitempty"`
	At     time.Time `json:"at"`
}

type botPairing struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	Fails   int       `json:"fails,omitempty"` // wrong codes sent meanwhile; botPairTries end it
}

const (
	botSettingsKey = "bots.feishu"
	botPairFor     = 10 * time.Minute
	botPairTries   = 5
)

type botHub struct {
	mu        sync.Mutex
	loaded    bool
	loading   bool
	loadErr   error
	loadAfter time.Time
	// pairLifecycle advances after a committed unpair, including an already-unpaired cancellation.
	pairLifecycle uint64
	set           FeishuBotSettings
	api           botport.Client
	conn          botport.Conn
	stop          context.CancelFunc
	info          botcard.BotInfo
	base          context.Context
	told          map[string]time.Time // sender -> when the bot last refused them (10-minute window)
	pairTold      map[string]time.Time // sender -> when the pairing path last answered (short redelivery window)
	names         map[string]string    // open id -> name
	seen          []string             // the messages taken lately (Feishu delivers some again), memory only
	// msgChat is the chat of the messages the bot may use, seen or sent lately (message id -> chat id): what a reply's
	// parent is checked against (checkReplyParent), memory only.
	msgChat idRing
	// Pending recalls are tracked separately from bounded recent source history, so a
	// backlog cannot evict a message that is still waiting for its predecessor.
	queuedMessages map[botMessageRef]*botQueuedMessage
	// Recent source evidence distinguishes local cancellation from handoff.
	recentMessages     map[botMessageRef]botMessageState
	recentMessageOrder []botMessageRef
	// guestTold is the chat a rate-limit note was last posted in, so a busy group hears it once per botGuestNoteEvery.
	guestTold map[string]time.Time
	// guestAsks are the recent refused @bot requests, memory only, newest last, at most botGuestAsksKeep.
	guestAsks []botGuestAsk
	// answerFiles keeps prepared long replies across retries and hub restarts.
	answerFiles botAnswerFiles
	// line is closed once the last message taken has been handed to the assistant (botMessage): they go in the
	// order they came.
	line chan struct{}
	// now is replaced in tests.
	now func() time.Time
	// registrar is the accounts site's client for app registration, regDomain where a registered app's calls go
	// (bots_register.go); both replaced in tests.
	registrar botport.Registrar
	regDomain func(region string) string
	// bindGate serializes Feishu binding, including registration network calls, independently of mu.
	// A channel lets a waiting client's context cancel without waiting for the current bind.
	bindGate chan struct{}
	// sync is the user sync service provided by the lark module (larksync_host.go).
	syncMu sync.Mutex
	sync   botport.SyncService
}

func (b *botHub) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

const (
	botSettingsRetry       = 5 * time.Second
	botSettingsReadTimeout = 5 * time.Second
)

var errBotSettingsUnread = i18n.Err("hub-bots.settings_unavailable")

// loadBotsLocked publishes only a complete read; b.mu is held on entry and return, released during storage I/O.
// Concurrent lookups fail closed while the single read runs. Failed reads are retried by later lookups,
// at most once per cooldown, including records repaired outside the manager. The assistant owns bots.feishu.state.
func (h *Manager) loadBotsLocked(ctx context.Context) error {
	b := &h.bots
	if b.loaded {
		return nil
	}
	if b.loading {
		return errBotSettingsUnread
	}
	if b.loadErr != nil && b.clock().Before(b.loadAfter) {
		return b.loadErr
	}
	next := FeishuBotSettings{Features: defaultBotFeatures}
	if h.store != nil {
		readCtx, cancel := context.WithTimeout(ctx, botSettingsReadTimeout)
		b.loading = true
		b.mu.Unlock()
		_, err := h.store.Setting(readCtx, botSettingsKey, &next)
		cancel()
		b.mu.Lock()
		b.loading = false
		if err != nil {
			h.logf("bots: load settings: %v", err)
			b.loadErr, b.loadAfter = errBotSettingsUnread, b.clock().Add(botSettingsRetry)
			return b.loadErr
		}
	}
	b.set, b.loaded, b.loadErr = next, true, nil
	if b.base != nil {
		h.restartBotLocked()
		if h.cfg.Lark.CLI != nil {
			// CLI synchronization and registry replacement must run outside bots.mu.
			h.larkCLIRecovery.needed.Store(true)
			h.requestLarkCLIRecovery(b.base)
		}
	}
	return nil
}

func (h *Manager) loadBotSettings(ctx context.Context) error {
	h.bots.mu.Lock()
	err := h.loadBotsLocked(ctx)
	base := h.bots.base
	h.bots.mu.Unlock()
	h.requestLarkCLIRecovery(base)
	return err
}

func (h *Manager) saveBotSettingsLocked() error {
	if !h.bots.loaded {
		return errBotSettingsUnread
	}
	// A committed owner no longer needs setup guidance. Restore it on a rejected write so callers such as
	// manual redemption can roll back their own owner/code transaction without losing the previous diagnosis.
	previousConfigure := h.bots.set.Configure
	if h.bots.set.Owner != nil {
		h.bots.set.Configure = nil
	}
	if h.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.store.PutSetting(ctx, botSettingsKey, h.bots.set); err != nil {
		h.logf("bots: save settings: %v", err)
		h.bots.set.Configure = previousConfigure
		return err
	}
	return nil
}

// botCredentialsLocked: the app's id, secret and domain, from the app's settings, else hub.json (h.bots.mu held).
func (h *Manager) botCredentialsLocked() (appID, secret, domain string, fromConfig bool, err error) {
	s := h.bots.set
	if s.AppID != "" && s.SecretEnc != "" {
		secret, err = h.openKey(s.SecretEnc)
		return s.AppID, secret, s.Domain, false, err
	}
	c := h.cfg.Bots.Feishu
	if c.AppID != "" && c.AppSecret != "" {
		return c.AppID, c.AppSecret, c.Domain, true, nil
	}
	return "", "", "", false, nil
}

// startBots connects the bot when it is switched on (at start, and after the settings change).
func (h *Manager) startBots(ctx context.Context) {
	h.bots.mu.Lock()
	h.loadBotsLocked(ctx)
	h.bots.base = ctx
	h.restartBotLocked()
	h.bots.mu.Unlock()
}

// restartBot drops the connection and, if the bot is on and configured, opens a new one. A member hub keeps it closed:
// Feishu hands each event to one of the connections, and the center's bot serves the group (mesh_group.go).
func (h *Manager) restartBot() {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	h.restartBotLocked()
}

// restartBotLocked uses only the successfully loaded settings; b.mu is held.
func (h *Manager) restartBotLocked() {
	b := &h.bots
	b.info = botcard.BotInfo{} // the bot may be another app now: its own id is read again
	if b.stop != nil {
		b.stop()
		b.stop, b.conn = nil, nil
	}
	if !b.loaded || !b.set.Enabled || b.base == nil || b.base.Err() != nil || h.isMember() {
		b.api = nil
		return
	}
	appID, secret, domain, _, err := h.botCredentialsLocked()
	if err != nil {
		h.logf("bots: open the app secret: %v", err)
		return
	}
	if appID == "" {
		b.api = nil
		return
	}
	if !botport.Available(h.modules()) { // a build without the lark module (persona_without_lark), or switched off: the bot stays off
		if botport.InBuild() {
			h.logf("bots: the lark module is switched off in the modules setting")
		} else {
			h.logf("bots: %v", botport.ErrUnavailable)
		}
		b.api = nil
		return
	}
	b.api = botport.New(appID, secret, domain)
	b.conn = botport.NewConn(b.api, h.botEventHandler(b.api), h.logf)
	ctx, cancel := context.WithCancel(b.base)
	b.stop = cancel
	conn, api := b.conn, b.api
	go conn.Run(ctx)
	go func() {
		ictx, icancel := context.WithTimeout(ctx, 15*time.Second)
		defer icancel()
		info, err := api.BotInfo(ictx)
		if err != nil {
			h.logf("bots: bot info: %v", err)
			return
		}
		b.mu.Lock()
		if b.api == api {
			b.info = info
		}
		b.mu.Unlock()
	}()
}

// botAPI is the client while the bot is on and paired (nil otherwise), with the settings it runs under (a copy).
func (h *Manager) botAPI() (botport.Client, FeishuBotSettings) {
	b := &h.bots
	b.mu.Lock()
	defer func() {
		base := b.base
		b.mu.Unlock()
		h.requestLarkCLIRecovery(base)
	}()
	if !b.loaded {
		h.loadBotsLocked(context.Background())
	}
	set := b.set.copy()
	if b.api == nil || !b.set.Enabled || b.set.Owner == nil {
		return nil, set
	}
	return b.api, set
}

// nudgeAfter is the setting in minutes: the default while never set, 0 off.
func (s FeishuBotSettings) nudgeAfter() int {
	if s.NudgeAfter == nil {
		return assistantwire.NudgeAfterDefault
	}
	return *s.NudgeAfter
}

func (s FeishuBotSettings) sourceReportDelayMinutes() int {
	if s.SourceReportDelayMinutes == nil {
		return assistantwire.SourceReportDelayDefault
	}
	return *s.SourceReportDelayMinutes
}

// copy is the settings with nothing shared: the owner is changed in place under h.bots.mu.
func (s FeishuBotSettings) copy() FeishuBotSettings {
	s.TopicChats = slices.Clone(s.TopicChats)
	s.AISources = slices.Clone(s.AISources)
	if s.NudgeAfter != nil {
		n := *s.NudgeAfter
		s.NudgeAfter = &n
	}
	if s.SourceReportDelayMinutes != nil {
		delay := *s.SourceReportDelayMinutes
		s.SourceReportDelayMinutes = &delay
	}
	if s.Owner != nil {
		o := *s.Owner
		s.Owner = &o
	}
	if s.Pairing != nil {
		p := *s.Pairing
		s.Pairing = &p
	}
	if s.Configure != nil {
		r := *s.Configure
		s.Configure = &r
	}
	if s.Registration != nil {
		r := *s.Registration
		s.Registration = &r
	}
	if s.Handoff != nil {
		h := *s.Handoff
		h.DirectChats = slices.Clone(h.DirectChats)
		h.BlockedChats = slices.Clone(h.BlockedChats)
		s.Handoff = &h
	}
	s.GroupAccess = s.GroupAccess.copy()
	s.GuestSources = slices.Clone(s.GuestSources)
	return s
}

// --- the app's settings page ---

type feishuBotView struct {
	Enabled     bool          `json:"enabled"`
	AppID       string        `json:"app_id"`
	Domain      string        `json:"domain"`
	HasSecret   bool          `json:"has_secret"`
	FromConfig  bool          `json:"from_config"` // the credentials are manager.json's
	Features    BotFeatures   `json:"features"`
	Connected   bool          `json:"connected"`
	Error       string        `json:"error,omitempty"`
	LastEventAt int64         `json:"last_event_at,omitempty"`
	BotName     string        `json:"bot_name,omitempty"`
	Owner       *botOwnerView `json:"owner,omitempty"`
	Pairing     *botPairView  `json:"pairing,omitempty"`
	TopicChats  []botChatView `json:"topic_chats,omitempty"` // the group chats the owner opted in
	NudgeAfter  int           `json:"nudge_after"`           // minutes before the one nudge under an unanswered card, 0 off

	SourceReportDelayMinutes int  `json:"source_report_delay_minutes"`
	SourceReportFullReply    bool `json:"source_report_full_reply"`
	// Handoff is the proxy-reply policy; absent when the owner has never set one.
	Handoff *botHandoffView `json:"handoff,omitempty"`
	// Target and Agents are the assistant's (feishu.view): where the chat's messages go now, the agents bound to
	// threads of the chat; left out while it is not connected.
	Target  string                      `json:"target,omitempty"`
	Agents  []assistantwire.FeishuAgent `json:"agents,omitempty"`
	LarkCLI *larkCLIBotView             `json:"larkcli,omitempty"`
}

type botOwnerView struct {
	Name     string `json:"name"`
	PairedAt int64  `json:"paired_at"`
}

type botPairView struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

func (h *Manager) botView(ctx context.Context) feishuBotView {
	b := &h.bots
	b.mu.Lock()
	loadErr := h.loadBotsLocked(ctx)
	s := b.set
	appID, _, domain, fromConfig, _ := h.botCredentialsLocked()
	v := feishuBotView{Enabled: s.Enabled, AppID: appID, Domain: botcard.ResolveDomain(domain), HasSecret: appID != "",
		FromConfig: fromConfig, Features: s.Features, BotName: b.info.Name, NudgeAfter: s.nudgeAfter(),
		SourceReportDelayMinutes: s.sourceReportDelayMinutes(), SourceReportFullReply: s.SourceReportFullReply,
		Handoff: s.handoffView()}
	if loadErr != nil {
		v.Error = i18n.Localize(loadErr, h.botLang(ctx))
	} else if b.conn != nil {
		st := b.conn.State()
		v.Connected, v.Error = st.Connected, st.Error
		if !st.LastEvent.IsZero() {
			v.LastEventAt = st.LastEvent.Unix()
		}
	} else if s.Enabled && appID == "" {
		v.Error = i18n.T(h.botLang(ctx), "hub-bots.no_credentials")
	}
	if s.Owner != nil {
		v.Owner = &botOwnerView{Name: s.Owner.Name, PairedAt: s.Owner.At.Unix()}
	}
	if p := s.Pairing; p != nil && b.clock().Before(p.Expires) {
		v.Pairing = &botPairView{Code: p.Code, ExpiresAt: p.Expires.Unix()}
	}
	for _, c := range s.TopicChats {
		v.TopicChats = append(v.TopicChats, botChatView{ChatID: c.ChatID, Name: c.Name, AddedAt: c.At.Unix(), Home: c.ChatID == s.homeChat(), RequireOwnerMention: c.RequireOwnerMention, TodoMode: c.TodoMode.Effective(), Space: c.Space})
	}
	botAppID := appID
	b.mu.Unlock()
	v.LarkCLI = h.larkCLIView(ctx, botAppID)
	if chat, err := h.feishuChatView(ctx); err == nil {
		v.Target, v.Agents = chat.Target, chat.Agents
	}
	return v
}

const sourceReportDelayMaxMinutes = 60

func (h *Manager) setSourceReportSettings(ctx context.Context, delay *int, fullReply *bool) error {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	previousDelay, previousFullReply := b.set.SourceReportDelayMinutes, b.set.SourceReportFullReply
	var changes []botRecord
	if delay != nil && b.set.sourceReportDelayMinutes() != *delay {
		b.set.SourceReportDelayMinutes = delay
		changes = append(changes, botRecord{auditBotSourceReportDelay, map[string]any{"minutes": *delay}})
	}
	if fullReply != nil && previousFullReply != *fullReply {
		b.set.SourceReportFullReply = *fullReply
		changes = append(changes, botRecord{auditBotSourceReportFullReply, map[string]any{"enabled": *fullReply}})
	}
	if len(changes) == 0 {
		b.mu.Unlock()
		return nil
	}
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.SourceReportDelayMinutes, b.set.SourceReportFullReply = previousDelay, previousFullReply
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()
	h.auditBot(ctx, changes...)
	return nil
}

func (h *Manager) botRoutes(api *http.ServeMux) {
	h.channelBindRoute(api) // binding a channel carries its secrets: the hub's, never relayed
	api.HandleFunc("GET /api/v1/bots/feishu", func(w http.ResponseWriter, r *http.Request) {
		if err := h.loadBotSettings(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, h.botView(r.Context()))
	})
	api.HandleFunc("PUT /api/v1/bots/feishu", func(w http.ResponseWriter, r *http.Request) {
		if err := h.loadBotSettings(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		var in struct {
			Enabled        *bool           `json:"enabled"`
			AppID          *string         `json:"app_id"`
			AppSecret      *string         `json:"app_secret"`
			Domain         *string         `json:"domain"`
			LarkCLIProfile *string         `json:"larkcli_profile"`
			Features       json.RawMessage `json:"features"`
			// TopicChats replaces the list of group chats the owner opted in; absent leaves it, [] clears it.
			// An entry may carry home: true (the home topic group, "/topics home"); when no entry says anything about home,
			// the one the list has stays.
			TopicChats *[]struct {
				ChatID              string                       `json:"chat_id"`
				Name                string                       `json:"name"`
				Home                *bool                        `json:"home"`
				RequireOwnerMention *bool                        `json:"require_owner_mention"`
				TodoMode            *assistantwire.TopicTodoMode `json:"todo_mode"`
				Space               *string                      `json:"space"`
			} `json:"topic_chats"`
			// NudgeAfter is minutes before the one nudge under an unanswered card; 0 turns nudges off, absent leaves it.
			// Read as raw JSON so that anything but a whole number in range gets the same answer (nudgeAfterValue).
			NudgeAfter json.RawMessage `json:"nudge_after"`
			// Zero posts as soon as the result is ready. Absent leaves the current setting.
			SourceReportDelayMinutes json.RawMessage `json:"source_report_delay_minutes"`
			SourceReportFullReply    *bool           `json:"source_report_full_reply"`
			// Handoff replaces the proxy-reply policy keys that are present; absent leaves it (bots_handoff.go).
			Handoff json.RawMessage `json:"handoff"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if in.LarkCLIProfile != nil {
			if fixed := h.configuredLarkCLIProfile(); fixed != "" {
				writeErrCode(w, http.StatusConflict, "larkcli_profile_fixed", fmt.Errorf("lark-cli profile is fixed by manager.json: %s", fixed))
				return
			}
			if err := h.validateLarkCLIProfile(r.Context(), *in.LarkCLIProfile); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		nudge, err := nudgeAfterValue(in.NudgeAfter)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		reportDelay, err := botMinutesValue(in.SourceReportDelayMinutes, sourceReportDelayMaxMinutes, "hub-bots.source_report_delay_range")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		var fs *botFeatureUpdate
		if len(in.Features) > 0 && string(in.Features) != "null" {
			// Validate before any credential check or write, but keep the supplied keys until commit:
			// an older app omits newer switches, and another save may change them meanwhile.
			var f BotFeatures
			if err := json.Unmarshal(in.Features, &f); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			fs = &botFeatureUpdate{patch: in.Features}
		}
		var topics []BotChat
		mentionEdits := map[string]bool{}
		spaceEdits := map[string]string{}
		keepHome := true // no entry says anything about home: the list's own stays
		if in.TopicChats != nil {
			topics = []BotChat{}
			for _, c := range *in.TopicChats {
				if c.RequireOwnerMention != nil {
					mentionEdits[strings.TrimSpace(c.ChatID)] = *c.RequireOwnerMention
				}
				if c.Home != nil {
					keepHome = false
				}
				chat := BotChat{ChatID: c.ChatID, Name: c.Name, Home: c.Home != nil && *c.Home}
				if c.TodoMode != nil {
					if !c.TodoMode.Valid() {
						writeErr(w, http.StatusBadRequest, i18n.Err("hub-bots.topic_todo_mode_bad"))
						return
					}
					chat.TodoMode = *c.TodoMode
				}
				if c.Space != nil {
					sp := strings.TrimSpace(*c.Space)
					if err := h.checkTopicSpace(r.Context(), sp); err != nil {
						writeErr(w, http.StatusBadRequest, err)
						return
					}
					spaceEdits[strings.TrimSpace(c.ChatID)] = sp
					chat.Space = sp
				}
				topics = append(topics, chat)
			}
			// Everything is checked before anything is written: a list sent with a change that makes it impossible
			// (a new App ID unpairs the owner) must not leave the rest of the request applied.
			if err := checkTopicChats(topics); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if len(topics) > 0 {
				if err := h.topicChatsPossible(r.Context(), in.AppID); err != nil {
					writeErr(w, http.StatusBadRequest, err)
					return
				}
			}
		}
		if err := h.updateBot(r.Context(), in.Enabled, in.AppID, in.AppSecret, in.Domain, fs); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if topics != nil {
			if err := h.setTopicChatsOpt(r.Context(), topics, keepHome, mentionEdits, spaceEdits); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if nudge != nil {
			if err := h.setNudgeAfter(r.Context(), *nudge); err != nil {
				writeErr(w, http.StatusInternalServerError, i18n.Err("hub-bots.nudge_save_failed"))
				return
			}
		}
		if reportDelay != nil || in.SourceReportFullReply != nil {
			if err := h.setSourceReportSettings(r.Context(), reportDelay, in.SourceReportFullReply); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		if in.LarkCLIProfile != nil {
			if err := h.setLarkCLIProfile(r.Context(), *in.LarkCLIProfile); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		if len(in.Handoff) > 0 && string(in.Handoff) != "null" {
			if err := h.setHandoffPolicy(r.Context(), in.Handoff, in.AppID); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		writeJSON(w, h.botView(r.Context()))
	})
	// pair: a code the user sends the bot from Feishu within 10 minutes; whoever sends it becomes the owner.
	api.HandleFunc("POST /api/v1/bots/feishu/pair", func(w http.ResponseWriter, r *http.Request) {
		b := &h.bots
		b.mu.Lock()
		if err := h.loadBotsLocked(r.Context()); err != nil {
			b.mu.Unlock()
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		previous := b.set.Pairing
		b.set.Pairing = &botPairing{Code: botPairCode(), Expires: b.clock().Add(botPairFor)}
		if err := h.saveBotSettingsLocked(); err != nil {
			b.set.Pairing = previous
			b.mu.Unlock()
			writeErr(w, http.StatusServiceUnavailable, errBotSettingsSave)
			return
		}
		b.mu.Unlock()
		h.auditBot(r.Context(), botRecord{action: auditBotPairCode})
		writeJSON(w, h.botView(r.Context()))
	})
	api.HandleFunc("DELETE /api/v1/bots/feishu/owner", func(w http.ResponseWriter, r *http.Request) {
		if err := h.unpairBot(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, h.botView(r.Context()))
	})
	// test: a message to the owner, to see that the bot reaches them.
	api.HandleFunc("POST /api/v1/bots/feishu/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := h.testFeishuBot(ctx); err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errBotOff) {
				code = http.StatusConflict
			}
			writeErr(w, code, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// identity: the paired bot's public identity, never a secret (bots_handoff.go).
	api.HandleFunc("GET /api/v1/bots/feishu/identity", func(w http.ResponseWriter, r *http.Request) {
		if err := h.loadBotSettings(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, h.botIdentity(r.Context()))
	})
	// handoffs: an authorized caller hands a group question to the bot (bots_handoff.go).
	api.HandleFunc("POST /api/v1/bots/feishu/handoffs", h.postHandoff)
	// group-access: who besides the owner may @ the bot in a group (bots_group_access.go).
	api.HandleFunc("GET /api/v1/bots/feishu/group-access", h.getGroupAccess)
	api.HandleFunc("PUT /api/v1/bots/feishu/group-access", h.putGroupAccess)
	h.larkSyncRoutes(api) // the user sync: /api/v1/bots/feishu/sync…
}

// botPairCode is a new six-digit pairing code.
func botPairCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", 100000+n.Int64())
}

// unpairBot forgets the owner; the assistant forgets the chat's state (feishu.owner).
func (h *Manager) unpairBot(ctx context.Context) error {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	previous := b.set.copy()
	had := b.set.Owner != nil
	b.set.Owner, b.set.Pairing, b.set.TopicChats, b.set.AISources, b.set.Handoff = nil, nil, nil, nil, nil // the topic chats, the /ai sources and the handoff policy were the owner's
	b.set.GroupAccess, b.set.GuestSources = nil, nil                                                       // the group access and the guest sources were the owner's too
	b.set.Configure = nil
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set = previous
		b.mu.Unlock()
		return errBotSettingsSave
	}
	b.pairLifecycle++
	b.mu.Unlock()
	if had {
		h.auditBot(ctx, botRecord{action: auditBotUnpaired})
	}
	_ = h.notifyAssistant(assistantwire.EventFeishuOwner, assistantwire.FeishuOwner{})
	return nil
}

var errBotOff = i18n.Err("hub-bots.bot_off")

// botLang is the language of what the bots and the channels write: the request's when the context carries one, else
// the user's (docs/protocol/i18n.md "Protocol").
func (h *Manager) botLang(ctx context.Context) i18n.Lang {
	return i18n.From(ctx).Or(h.userLang())
}

// testFeishuBot sends the owner a card.
func (h *Manager) testFeishuBot(ctx context.Context) error {
	api, set := h.botAPI()
	if api == nil {
		return errBotOff
	}
	l := h.botLang(ctx)
	body := i18n.T(l, "hub-bots.test_body")
	card := botcard.NewCard("", "", i18n.T(l, "hub-bots.test_title")).Markdown(body)
	_, err := h.botPost(ctx, api, set.Owner.ChatID, "", card, body)
	return err
}

// updateBot applies the app's changes; new credentials are tried (the bot's info) before they are saved.
func (h *Manager) updateBot(ctx context.Context, enabled *bool, appID, secret, domain *string, features *botFeatureUpdate) error {
	if appID != nil || secret != nil || domain != nil {
		release, err := h.acquireFeishuBind(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	return h.applyBotUpdate(ctx, enabled, appID, secret, domain, features, false)
}

// applyBotUpdate requires the bind gate for credential updates. Only registration's own finish preserves its session.
func (h *Manager) applyBotUpdate(ctx context.Context, enabled *bool, appID, secret, domain *string, features *botFeatureUpdate, preserveRegistration bool) error {
	_, err := h.applyBotUpdateWithReceipt(ctx, enabled, appID, secret, domain, features, preserveRegistration)
	return err
}

// applyBotUpdateWithReceipt returns the exact committed credentials and pairing lifecycle before post-save work.
// Credential updates require the binding gate; a caller already holding it must not acquire it again.
func (h *Manager) applyBotUpdateWithReceipt(ctx context.Context, enabled *bool, appID, secret, domain *string, features *botFeatureUpdate, preserveRegistration bool) (botConfigureReceipt, error) {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return botConfigureReceipt{}, err
	}
	next := b.set.copy()
	b.mu.Unlock()
	reconnect := false
	creds, credsChanged := false, false
	if appID != nil || secret != nil || domain != nil {
		id, dom := next.AppID, next.Domain
		if appID != nil {
			id = strings.TrimSpace(*appID)
		}
		if domain != nil {
			dom = strings.TrimSpace(*domain)
		}
		newSecret := secret != nil && strings.TrimSpace(*secret) != ""
		// The secret kept now: what a new one is compared with (for the audit), and what is used without one.
		var prev string
		if next.SecretEnc != "" {
			var err error
			if prev, err = h.openKey(next.SecretEnc); err != nil && !newSecret {
				return botConfigureReceipt{}, err
			}
		}
		plain := prev
		if newSecret {
			plain = strings.TrimSpace(*secret)
		}
		credsChanged = id != next.AppID || dom != next.Domain || (id != "" && plain != prev)
		if id == "" {
			// Cleared: back to hub.json's, if any.
			next.AppID, next.SecretEnc, next.Domain = "", "", ""
		} else {
			if plain == "" {
				return botConfigureReceipt{}, i18n.Err("hub-bots.needs_secret")
			}
			if !botport.Available(h.modules()) {
				return botConfigureReceipt{}, botport.ErrUnavailable
			}
			vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, err := botport.New(id, plain, dom).BotInfo(vctx)
			cancel()
			if err != nil {
				return botConfigureReceipt{}, errors.New(i18n.T(h.botLang(ctx), "hub-bots.cannot_reach_app", i18n.Args{"error": err.Error()}))
			}
			sealed, err := h.sealKey(plain)
			if err != nil {
				return botConfigureReceipt{}, err
			}
			next.AppID, next.SecretEnc, next.Domain = id, sealed, dom
		}
		reconnect, creds = true, true
	}
	b.mu.Lock()
	// Onto the settings as they are now: the credentials were checked over the network meanwhile, and a pairing may
	// have come in.
	if creds && ctx.Err() != nil {
		b.mu.Unlock()
		return botConfigureReceipt{}, ctx.Err()
	}
	cur := &b.set
	previous := cur.copy()
	var merged BotFeatures
	if features != nil {
		var err error
		merged, err = features.apply(cur.Features)
		if err != nil {
			b.mu.Unlock()
			return botConfigureReceipt{}, err
		}
	}
	var records []botRecord
	if creds {
		if credsChanged {
			if !preserveRegistration {
				cur.Registration, cur.RegistrationDeviceEnc, cur.RegistrationSecretEnc = nil, "", ""
			}
			d := map[string]any{"app_id": next.AppID}
			if next.Domain != "" {
				d["domain"] = next.Domain
			}
			if next.AppID == "" {
				d["cleared"] = true
			}
			if next.AppID != cur.AppID && cur.Owner != nil {
				d["owner_reset"] = true
			}
			records = append(records, botRecord{auditBotCredentials, d})
		}
		if next.AppID != cur.AppID {
			cur.Owner, cur.Pairing, cur.TopicChats, cur.AISources, cur.Handoff, cur.GroupAccess, cur.GuestSources = nil, nil, nil, nil, nil, nil, nil
		}
		h.clearBotConfigureOnChangeLocked(next)
		cur.AppID, cur.SecretEnc, cur.Domain = next.AppID, next.SecretEnc, next.Domain
	}
	if enabled != nil && *enabled != cur.Enabled {
		cur.Enabled = *enabled
		reconnect = true
		records = append(records, botRecord{auditBotEnabled, map[string]any{"enabled": *enabled}})
	}
	if features != nil {
		if merged != cur.Features {
			records = append(records, botRecord{auditBotFeatures, map[string]any{"features": merged}})
		}
		cur.Features = merged
	}
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set = previous
		b.mu.Unlock()
		return botConfigureReceipt{}, errBotSettingsSave
	}
	receipt := h.botConfigureReceiptLocked()

	b.mu.Unlock()
	h.auditBot(ctx, records...)
	if reconnect {
		h.restartBot()
	}
	if creds {
		syncCtx := h.ctx
		if syncCtx == nil {
			syncCtx = context.Background()
		}
		if err := h.syncLarkCLIProfile(syncCtx); err != nil {
			h.logf("larkcli profile sync: %v", err)
		}
		h.refreshLarkCLIConnector(syncCtx)
	}
	h.sendReport()
	return receipt, nil
}
