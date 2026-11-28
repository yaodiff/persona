package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/subscriptions"
	"github.com/nwverse/persona/server/internal/subscriptionsui"
)

// The built-in sources of the Inbox Sources pages (#5278, docs/extensions/subscriptions.md "Pages"): the connectors
// that bring things into the Inbox or the knowledge base themselves, and mail. Their switches, rules and mutes here
// are the ones Settings › Connectors, an extension's page and the Inbox already change, so either place shows the
// other's change.

// kbArchiveDefault is how often the knowledge base asks a connector whose manifest names no Poll (the assistant's
// kbConnectorPoll), for the pages to say.
const kbArchiveDefault = 10 * time.Minute

// sourceReads are the Inbox's latest reads of each source since the manager started (memory only), by connector id
// and by "mail:<account id>": when, and the code of a failure.
type sourceReads struct {
	mu sync.Mutex
	m  map[string]subscriptionsui.Read
}

// note records a read of key at at; code "" is one that worked.
func (r *sourceReads) note(key string, at time.Time, code string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]subscriptionsui.Read{}
	}
	read := r.m[key]
	read.At, read.Error = at, code
	if code == "" {
		ok := at
		read.OK = &ok
	}
	r.m[key] = read
}

func (r *sourceReads) get(key string) *subscriptionsui.Read {
	r.mu.Lock()
	defer r.mu.Unlock()
	read, ok := r.m[key]
	if !ok {
		return nil
	}
	return &read
}

// builtinSources answers the pages for the manager.
type builtinSources struct{ h *Manager }

// builtinIcons are the hub's own sources' icons on the pages; a plugin's connector is a chat or the plugin icon.
var builtinIcons = map[string]string{"mail": "mail", "github": "code", "imessage": "chat", "larkcli": "chat", "lark": "chat",
	"telegram": "chat", "wechat": "chat"}

// listed says a connector is one of the pages' sources: it brings things into the Inbox or the knowledge base, and it
// was set up (one never configured is no source of the user's yet).
func (b builtinSources) listed(ctx context.Context, c connector.Connector) (bool, string) {
	m := c.Manifest()
	_, inbox := connector.As[connector.Inbox](c)
	if !(m.Has(connector.CapInbox) && inbox) && !m.Has(connector.CapArchive) && c.ID() != "mail" {
		return false, ""
	}
	state := connector.StatusOf(ctx, c).State
	return state != connector.StateNotConfigured, state
}

func (b builtinSources) List(ctx context.Context) []subscriptionsui.Builtin {
	h := b.h
	if h.connectors == nil {
		return nil
	}
	var out []subscriptionsui.Builtin
	for _, c := range h.connectors.All() {
		ok, state := b.listed(ctx, c)
		if !ok {
			continue
		}
		id, m := c.ID(), c.Manifest()
		src := subscriptionsui.Builtin{ID: id, Name: connector.DisplayName(ctx, c), Icon: builtinIcons[id], Status: state,
			Read: h.inboxReads.get(id)}
		if !h.connectors.Enabled(id) {
			src.Status = connector.StateDisabled
		}
		if hp := h.extensions.ownerOf(c); hp != nil {
			src.Extension = hp.id
		}
		if _, inbox := connector.As[connector.Inbox](c); inbox && m.Has(connector.CapInbox) {
			on := h.connectors.Feeds(id, connector.FeedInbox)
			src.Inbox, src.InboxEvery, src.Mutable = &on, int(inboxPoll/time.Second), true
		}
		if m.Has(connector.CapArchive) {
			on := h.connectors.Feeds(id, connector.FeedArchive)
			src.Archive, src.ArchiveEvery = &on, int(kbArchiveDefault/time.Second)
			if m.Poll > 0 {
				src.ArchiveEvery = int(m.Poll / time.Second)
			}
		}
		if id == "mail" {
			b.mail(&src)
		}
		b.rules(&src)
		b.via(ctx, &src)
		out = append(out, src)
	}
	return append(out, b.assistant(ctx))
}

// assistantSource is the id of the assistant's own source (#6430): the rows it puts into the Inbox itself (inbox_post,
// a scheduled task's "put it in the Inbox when done"). It has no connector: its one switch is the assistant's.
const assistantSource = "assistant"

// assistant is that source's row: Add to Inbox is the assistant's switch (on unless turned off). While the assistant
// cannot be reached the switch shows on, its default, and a change is refused by SetFeed.
func (b builtinSources) assistant(ctx context.Context) subscriptionsui.Builtin {
	on := true
	params := assistantwire.InboxPostParams{On: assistantwire.InboxPostLeftOut}
	if res, err := buswire.Call(ctx, assistantCaller(b.h.callAssistant), assistantwire.InboxPostMethod, params); err == nil {
		on = res.On
	}
	return subscriptionsui.Builtin{ID: assistantSource, Name: i18n.T(b.h.userLang(), "extmgr-inbox_sources.assistant"), Icon: "robot",
		Status: connector.StateOK, Inbox: &on, InboxEvery: 0}
}

// migrations are the built-in sources whose Inbox feed can be read through a default subscription (#6345, the M steps
// of docs/design/proposals/inbox-sources-migration.md), by connector: what each subscription reads.
var migrations = map[string]subscriptions.Migration{
	// iMessage's rows as its Collect makes them, ready-made (#6317).
	"imessage": {Connector: "imessage", Source: "imessage", Icon: "chat", Recipe: inboxRows(wire.ConnectorSubscriptionKindSubMessage)},
	// GitHub's rows as its Collect makes them, through the generic inbox_things (#6381, #6404).
	"github": {Connector: "github", Source: "github", Icon: "code", Recipe: inboxRows(wire.ConnectorSubscriptionKindSubNotice)},
	// Lark's rows as its extension's Collect makes them, through the generic inbox_things (#13672): chats, documents
	// and moments of several accounts, a conversation's batch of new messages beside its events' state (#13637).
	"lark": {Connector: "lark", Source: "lark", Icon: "chat", Recipe: inboxRows(wire.ConnectorSubscriptionKindSubMessage)},
}

// withheld are the sources whose default subscription is not offered yet: the owner says when (decision 6 of the
// migration), each after the source before it has had its week there. GitHub's is offered since iMessage's week
// (#13599); Lark's waits for GitHub's (#13672). One already switched still shows its switch, to switch back.
var withheld = map[string]bool{"lark": true}

// inboxRows is a default subscription's recipe: the source's own Inbox rows, ready-made (inbox_things), a row
// without a kind of its own being kind.
func inboxRows(kind string) subscriptions.Recipe {
	return subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: subscriptions.InboxThingsTool, ItemsPath: "$.things",
		Kind: kind, GroupBy: wire.ConnectorSubscriptionGroupThread}
}

// via is a built-in source's default subscription, for its page's switch: none when it has no migration.
func (b builtinSources) via(ctx context.Context, src *subscriptionsui.Builtin) {
	if _, ok := migrations[src.ID]; !ok || b.h.subs == nil {
		return
	}
	via := &subscriptionsui.Via{}
	if s, _, err := b.h.subs.Get(ctx, subscriptions.MigrationID(src.ID)); err == nil {
		via.On, via.Subscription = s.Migrated != nil, s.ID
		if via.On {
			src.Read = subscriptionRead(s) // the Inbox round no longer reads it (#11762)
		}
	}
	if withheld[src.ID] && !via.On {
		return // not offered yet
	}
	src.Via = via
}

// subscriptionRead is a switched source's latest read as its default subscription's runs keep it, as sourceReads
// would: the latest attempt, the latest that worked, and why the latest failed while it did. Stored, so a restart
// keeps it. None before its first run.
func subscriptionRead(s subscriptions.Subscription) *subscriptionsui.Read {
	if s.LastRunAt == nil {
		return nil
	}
	r := &subscriptionsui.Read{At: *s.LastRunAt, OK: s.LastOKAt}
	if s.Error != nil {
		r.Error = s.Error.Code
		if r.Error == "" {
			r.Error = assistantwire.ReadFailed
		}
	}
	return r
}

// SetMigrated reads source id's Inbox feed through its default subscription (on) or as before, named as the source.
func (b builtinSources) SetMigrated(ctx context.Context, id string, on bool, by subscriptions.Actor) error {
	m, ok := migrations[id]
	c := b.h.connectors.Get(id)
	if !ok || c == nil || b.h.subs == nil {
		return fmt.Errorf("no default subscription for %q", id)
	}
	if on && withheld[id] {
		return fmt.Errorf("%q's default subscription is not offered yet", id)
	}
	m.Label, m.SourceName = connector.DisplayName(ctx, c), c.Name()
	_, err := b.h.subs.Migrate(ctx, m, on, by)
	return err
}

// mail is the mail source's accounts: every account's inbox is read each mail round, with no switch for it.
func (b builtinSources) mail(src *subscriptionsui.Builtin) {
	h := b.h
	if h.mail == nil {
		return
	}
	on := h.connectors.Feeds("mail", connector.FeedInbox) // its Add to Inbox switch (#5307)
	src.Inbox, src.InboxEvery, src.Mutable, src.Read = &on, int(inboxMailPoll/time.Second), true, nil
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		h.logf("inbox sources: mail accounts: %v", err)
		return
	}
	for _, a := range accounts {
		src.Accounts = append(src.Accounts, subscriptionsui.Account{ID: a.ID, Name: a.Email, Read: h.inboxReads.get("mail:" + a.ID)})
	}
}

// rules are a source's own triage rules where it can have them: a plugin's that declares triage_rules, lark-cli's
// option; the others' items follow the Inbox's rules.
func (b builtinSources) rules(src *subscriptionsui.Builtin) {
	h := b.h
	if src.Extension != "" && h.extensions != nil {
		if p := h.extensions.reg.Get(src.Extension); p != nil && declaresTriageRules(p.Schema) {
			src.RulesOwn, src.Rules, src.RulesLocked = true, h.extensionTriageRules(src.Extension), p.ReadOnly()
		}
		return
	}
	if src.ID == "larkcli" {
		var o struct {
			Rules string `json:"triage_rules"`
		}
		_ = json.Unmarshal(h.optionsOf(src.ID), &o)
		src.RulesOwn, src.Rules = true, strings.TrimSpace(o.Rules)
	}
}

func (b builtinSources) SetFeed(ctx context.Context, id string, inbox, on bool, _ subscriptions.Actor) error {
	if id == assistantSource {
		if !inbox {
			return fmt.Errorf("%q has no knowledge-base feed", id)
		}
		post := assistantwire.InboxPostOff
		if on {
			post = assistantwire.InboxPostOn
		}
		_, err := buswire.Call(ctx, assistantCaller(b.h.callAssistant), assistantwire.InboxPostMethod, assistantwire.InboxPostParams{On: post})
		return err
	}
	f := connector.FeedArchive
	if inbox {
		f = connector.FeedInbox
	}
	if b.h.connectors.Get(id) == nil {
		return fmt.Errorf("no connector %q", id)
	}
	return b.h.setConnectorFeed(ctx, id, f, on)
}

func (b builtinSources) SetRules(ctx context.Context, id, rules string, _ subscriptions.Actor) error {
	h := b.h
	c := h.connectors.Get(id)
	if c == nil {
		return fmt.Errorf("no connector %q", id)
	}
	rules = strings.TrimSpace(rules)
	if hp := h.extensions.ownerOf(c); hp != nil {
		title := i18n.T(h.userLang(), "hub-self_settings_sources.title", i18n.Args{"name": connector.DisplayName(ctx, c)})
		raw, _ := json.Marshal(rules)
		change, err := h.setExtensionTriageRules(hp.id, title, raw)
		if err != nil {
			return err
		}
		if change.apply == nil { // unchanged
			return nil
		}
		_, err = change.apply(ctx, nil)
		return err
	}
	if id != "larkcli" {
		return errors.New("this source has no triage rules of its own")
	}
	var o map[string]json.RawMessage
	if err := json.Unmarshal(h.optionsOf(id), &o); err != nil || o == nil {
		o = map[string]json.RawMessage{}
	}
	o["triage_rules"], _ = json.Marshal(rules)
	raw, _ := json.Marshal(o)
	if err := h.setConnectorOptions(ctx, id, raw); err != nil {
		return err
	}
	h.auditConnector(ctx, id, auditConnectorOptions, nil)
	return nil
}

// errInboxAway: the assistant could not say what is muted (away, or its stored mutes unreadable).
var errInboxAway = errors.New("the inbox is not available")

func (b builtinSources) Mutes(ctx context.Context, source string) ([]subscriptionsui.Mute, error) {
	res, err := buswire.Call(ctx, assistantCaller(b.h.callAssistant), assistantwire.InboxMutesMethod,
		assistantwire.InboxMutesParams{Source: source})
	if err != nil {
		return nil, errors.Join(errInboxAway, err)
	}
	out := []subscriptionsui.Mute{}
	for _, m := range res.Mutes {
		out = append(out, subscriptionsui.Mute{ID: m.ID, Source: m.Source, Tag: m.Tag, Name: m.Name, At: m.CreatedAt})
	}
	return out, nil
}

func (b builtinSources) Unmute(ctx context.Context, id string) error {
	_, err := buswire.Call(ctx, assistantCaller(b.h.callAssistant), assistantwire.InboxUnmuteMethod,
		assistantwire.InboxUnmuteParams{ID: assistantwire.InboxMuteID(id)})
	return err
}

// Native is the built-in source that reads a catalog service: the mail account on its domain (the mail source), or
// the switched-on connector whose links live on its host.
func (b builtinSources) Native(service string) (string, string) {
	h := b.h
	kind, domain, ok := strings.Cut(strings.ToLower(service), ":")
	if !ok || domain == "" {
		return "", ""
	}
	if kind == "mail" && h.mail != nil && h.connectors.Get("mail") != nil {
		if accounts, err := h.mail.ListAccounts(); err == nil {
			for _, a := range accounts {
				if _, d, ok := strings.Cut(strings.ToLower(a.Email), "@"); ok && d == domain {
					return "mail", a.Email
				}
			}
		}
	}
	for _, c := range h.connectors.All() {
		if !h.connectors.Enabled(c.ID()) {
			continue
		}
		for _, host := range c.Manifest().Hosts {
			if host = strings.ToLower(host); host == domain || strings.HasSuffix(host, "."+domain) {
				return c.ID(), h.subscriptionConnectorName(c.ID())
			}
		}
	}
	return "", ""
}

// triagePreview has the assistant's triage model try a preview's items (the pages' Try Triage with the Model): one
// verdict each, nothing kept.
func (h *Manager) triagePreview(ctx context.Context, tp subscriptions.TriagePreview) ([]subscriptionsui.Verdict, error) {
	in := assistantwire.InboxTriagePreview{Source: tp.Source, Name: tp.Name, Kinds: tp.Kinds, Things: tp.Things}
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxTriagePreviewMethod, in)
	if err != nil {
		return nil, err
	}
	out := make([]subscriptionsui.Verdict, 0, len(res.Verdicts))
	for _, v := range res.Verdicts {
		out = append(out, subscriptionsui.Verdict{Tier: v.Tier, Summary: v.Summary, Reason: v.Reason, Urgent: v.Urgent,
			State: v.State, Why: v.Why})
	}
	return out, nil
}
