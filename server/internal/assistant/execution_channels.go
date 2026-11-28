package assistant

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Managed channels (docs/assistant/execution-spec.md "Channels"): one gateway or API connection, its key, headers,
// proxy and device scope kept once, run by every runtime it has a binding for. Each binding is a launcher alias the
// older clients, the model configuration and `personad launch <alias>` know: withChannelAliases makes it a managed
// launcher on every read of launchers.managed, so the delivery to the computers (catalog, pull, scope, the
// computers' own keys) is the launchers' own and nothing is copied. The aliases are never stored as launchers
// (persistedLaunchers) and are edited only through their channel.
//
// Every change is a point of the channel ledger a rollback returns to, the launchers a change folded into a channel
// included. Like a launcher's save, a change only updates the records and their revision and tells the computers:
// no running session is stopped (docs/assistant/execution-spec.md "Changes never stop a running session").

// execChannelsKey is execution.channels: []storedChannel, key and headers sealed.
const execChannelsKey = "execution.channels"

// execChannelLedgerKey is execution.channels.ledger: []ledgerChannel, the channels of every rollback point.
const execChannelLedgerKey = "execution.channels.ledger"

// execChannelLedgerLaunchersKey is execution.channels.ledger_launchers: []ledgerLauncher, the launchers a change
// replaced, by point.
const execChannelLedgerLaunchersKey = "execution.channels.ledger_launchers"

// execChannelStateKey is execution.channels.state: execChannelState.
const execChannelStateKey = "execution.channels.state"

// maxChannelPoints bounds the ledger; the oldest point goes first.
const maxChannelPoints = 10

// maxChannels bounds the catalog.
const maxChannels = 64

// maxChannelBindings bounds one channel's runtimes.
const maxChannelBindings = 16

// maxChannelModels bounds one channel's models.
const maxChannelModels = 256

// maxChannelEndpoints bounds one channel's protocols.
const maxChannelEndpoints = 8

// execChannelFresh is how long a read keeps the catalog it read (a change replaces it at once).
const execChannelFresh = 5 * time.Second

// storedChannel is a managed channel as kept: its key and headers sealed.
type storedChannel struct {
	// Name is the channel's name.
	Name string `json:"name"`
	// DisplayName is how people read it.
	DisplayName string `json:"display_name,omitempty"`
	// BaseURL is the gateway's address.
	BaseURL string `json:"base_url"`
	// KeySealed is the key, sealed.
	KeySealed string `json:"key_sealed,omitempty"`
	// HeadersSealed are the extra headers, sealed.
	HeadersSealed string `json:"headers_sealed,omitempty"`
	// Auth is api_key or bearer.
	Auth string `json:"auth,omitempty"`
	// Proxy is the gateway's proxy.
	Proxy string `json:"proxy,omitempty"`
	// NoProxy is NO_PROXY.
	NoProxy string `json:"no_proxy,omitempty"`
	// Share is the device scope.
	Share string `json:"share,omitempty"`
	// ScopeMachines are the scope's computers.
	ScopeMachines []string `json:"scope_machines,omitempty"`
	// Delivery is on_demand or push.
	Delivery string `json:"delivery,omitempty"`
	// Endpoints are the protocols it serves.
	Endpoints []proto.WireProtocol `json:"endpoints"`
	// Models are the models it offers.
	Models []proto.ChannelModel `json:"models,omitempty"`
	// Quota is its usage source.
	Quota *proto.ChannelQuota `json:"quota,omitempty"`
	// Bindings are its runtimes and their aliases.
	Bindings []proto.ChannelBinding `json:"bindings"`
	// Created is when it was first saved.
	Created time.Time `json:"created"`
	// Updated is when it was last saved; its revision.
	Updated time.Time `json:"updated"`
}

// rev is what changes whenever the channel is saved; its aliases carry it.
func (c storedChannel) rev() string { return strconv.FormatInt(c.Updated.UnixNano(), 36) }

// supply is the channel's own declaration. The runtimes and aliases are added from its aliases (execDecl).
func (c storedChannel) supply() proto.ChannelSupply {
	sup := proto.ChannelSupply{Name: c.Name, DisplayName: c.DisplayName, Kind: proto.ChannelManaged,
		Endpoints: slices.Clone(c.Endpoints), Models: slices.Clone(c.Models), Rev: c.rev()}
	if c.Quota != nil {
		q := *c.Quota
		sup.Quota = &q
	}
	return sup
}

// view is the channel as Settings shows it: never the key or the headers.
func (c storedChannel) view() proto.ChannelConfig {
	v := proto.ChannelConfig{Name: c.Name, DisplayName: c.DisplayName, BaseURL: c.BaseURL, HasKey: c.KeySealed != "",
		HasHeaders: c.HeadersSealed != "", Auth: c.Auth, Proxy: c.Proxy, NoProxy: c.NoProxy, Share: c.Share,
		ScopeMachines: slices.Clone(c.ScopeMachines), Delivery: c.Delivery, Endpoints: slices.Clone(c.Endpoints),
		Models: slices.Clone(c.Models), Bindings: slices.Clone(c.Bindings), Rev: c.rev(), Updated: c.Updated}
	if v.Endpoints == nil {
		v.Endpoints = []proto.WireProtocol{}
	}
	if v.Bindings == nil {
		v.Bindings = []proto.ChannelBinding{}
	}
	if c.Quota != nil {
		q := *c.Quota
		v.Quota = &q
	}
	return v
}

// providerModels are the channel's models as a launcher lists them: by canonical id, and, where the upstream takes
// the effort as a suffix, each "<model>-<effort>" too, so a runtime that reads only ids keeps finding them.
func (c storedChannel) providerModels() []proto.ProviderModel {
	var out []proto.ProviderModel
	for _, m := range c.Models {
		pm := proto.ProviderModel{ID: m.Canonical, Name: m.Name, ContextWindow: m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens, Efforts: proto.ProjectEfforts(m.Efforts, proto.EffortLevels),
			DefaultEffort: m.DefaultEffort}
		if !slices.Equal(pm.Efforts, m.Efforts) {
			pm.AllEfforts = slices.Clone(m.Efforts)
		}
		out = append(out, pm)
		if slices.ContainsFunc(m.Edges, func(e proto.ChannelEdge) bool { return e.EffortVia == proto.EffortModelSuffix }) {
			for _, e := range m.Efforts {
				out = append(out, proto.ProviderModel{ID: m.Canonical + "-" + e, ContextWindow: m.ContextWindow,
					MaxOutputTokens: m.MaxOutputTokens, NoEffort: true})
			}
		}
	}
	return out
}

// alias is binding b of the channel as the managed launcher it stands for.
func (c storedChannel) alias(b proto.ChannelBinding) storedLauncher {
	agent := b.Runtime
	if agent == "claude" {
		agent = "" // as a launcher made by hand names Claude Code
	}
	l := storedLauncher{KeySealed: c.KeySealed, HeadersSealed: c.HeadersSealed, Share: c.Share,
		ScopeMachines: slices.Clone(c.ScopeMachines), Created: c.Created, Updated: c.Updated, channelAlias: c.Name}
	l.ManagedLauncher = proto.ManagedLauncher{Agent: agent, API: b.API, Name: b.Alias, BaseURL: cmp.Or(b.BaseURL, c.BaseURL),
		Model: b.Model, Effort: b.Effort, Auth: c.Auth, Proxy: c.Proxy, NoProxy: c.NoProxy, Delivery: c.Delivery,
		Channel: c.Name, Models: c.providerModels()}
	if b.API != "" {
		l.Kind = "api"
	}
	return l
}

// withChannelAliases is the managed launchers as every reader sees them: real, then each channel's aliases in order.
// A real launcher keeps its name: an alias of the same name (which a save refuses) is left out.
func withChannelAliases(owned []storedLauncher, channels []storedChannel) []storedLauncher {
	if len(channels) == 0 {
		return owned
	}
	out := slices.Clone(owned)
	taken := map[string]bool{}
	for _, l := range owned {
		taken[l.Name] = true
	}
	for _, c := range channels {
		for _, b := range c.Bindings {
			if taken[b.Alias] {
				continue
			}
			taken[b.Alias] = true
			out = append(out, c.alias(b))
		}
	}
	return out
}

// persistedLaunchers is list without the channels' aliases: what launchers.managed keeps.
func persistedLaunchers(list []storedLauncher) []storedLauncher {
	out := make([]storedLauncher, 0, len(list))
	for _, l := range list {
		if l.channelAlias == "" {
			out = append(out, l)
		}
	}
	return out
}

// errChannelAlias refuses to edit or delete launcher l on its own: it is a channel's alias.
func errChannelAlias(l storedLauncher) error {
	return i18n.Err("assistant-execution.err_alias_read_only", i18n.Args{"launcher": l.Name, "channel": l.channelAlias})
}

// ---- reading ----

// execChannelBook caches the catalog for reads, at most execChannelFresh: a change replaces it at once, and the key
// rotation, which re-seals the stored values, is seen within that time.
type execChannelBook struct {
	mu   sync.Mutex
	at   time.Time
	list []storedChannel
}

func (b *execChannelBook) set(list []storedChannel) {
	b.mu.Lock()
	b.list, b.at = slices.Clone(list), time.Now()
	b.mu.Unlock()
}

// channelCatalog is the managed channels for a read; false when they could not be read.
func (s *Service) channelCatalog(ctx context.Context) ([]storedChannel, bool) {
	b := &s.execChannels
	b.mu.Lock()
	if !b.at.IsZero() && time.Since(b.at) < execChannelFresh {
		list := slices.Clone(b.list)
		b.mu.Unlock()
		return list, true
	}
	b.mu.Unlock()
	list, err := s.readChannelCatalog(ctx)
	if err != nil {
		s.logf("execution: read %s: %v", execChannelsKey, err)
		return nil, false
	}
	b.set(list)
	return list, true
}

// readChannelCatalog reads the managed channels from the store itself, for a change; no database is none.
func (s *Service) readChannelCatalog(ctx context.Context) ([]storedChannel, error) {
	st := s.store()
	if st == nil {
		return nil, nil
	}
	var list []storedChannel
	if _, err := st.Setting(ctx, execChannelsKey, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// execChannelPoint is one rollback point: the catalog at a revision, and what restoring it also does to the real
// launchers.
type execChannelPoint struct {
	// Revision is the catalog's revision the point restores.
	Revision int64 `json:"revision"`
	// At is when the change that replaced it was made.
	At time.Time `json:"at"`
	// Action is that change: save, delete or rollback.
	Action string `json:"action"`
	// Channel is the channel it was about.
	Channel string `json:"channel,omitempty"`
	// Names are the point's channels.
	Names []string `json:"names"`
	// Drop are the real launchers restoring it removes again (the ones a rollback brought back).
	Drop []string `json:"drop,omitempty"`
}

// execChannelState is the catalog's revision and its rollback points, oldest first.
type execChannelState struct {
	// Revision is the catalog's revision: every change adds one.
	Revision int64 `json:"revision"`
	// Points are the rollback points, oldest first.
	Points []execChannelPoint `json:"points,omitempty"`
}

// ledgerChannel is a channel of a rollback point.
type ledgerChannel struct {
	storedChannel
	// Point is the revision of the point it belongs to.
	Point int64 `json:"point"`
}

// ledgerLauncher is a real launcher a rollback point brings back.
type ledgerLauncher struct {
	storedLauncher
	// Point is the revision of the point it belongs to.
	Point int64 `json:"point"`
}

// execChannelRecords is everything a channel change reads and writes.
type execChannelRecords struct {
	owned    []storedLauncher
	catalog  []storedChannel
	state    execChannelState
	ledger   []ledgerChannel
	launched []ledgerLauncher
}

// readChannelRecords reads them all from the store, s.models.mu held; any failed read fails the change.
func (s *Service) readChannelRecords(ctx context.Context) (execChannelRecords, error) {
	var r execChannelRecords
	list, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return r, err
	}
	r.owned = persistedLaunchers(list)
	if r.catalog, err = s.readChannelCatalog(ctx); err != nil {
		return r, err
	}
	st := s.store()
	for key, out := range map[string]any{execChannelStateKey: &r.state, execChannelLedgerKey: &r.ledger,
		execChannelLedgerLaunchersKey: &r.launched} {
		if _, err := st.Setting(ctx, key, out); err != nil {
			return r, err
		}
	}
	return r, nil
}

// point makes what the change about to be made replaces a rollback point: the catalog as it is, the real launchers
// the change removes (brought back by restoring it) and the ones restoring it removes again. Then the oldest points
// go past maxChannelPoints, and the revision moves on.
func (r *execChannelRecords) point(now time.Time, action, channel string, removed []storedLauncher, drop []string) {
	p := execChannelPoint{Revision: r.state.Revision, At: now, Action: action, Channel: channel, Names: []string{}, Drop: drop}
	for _, c := range r.catalog {
		p.Names = append(p.Names, c.Name)
		r.ledger = append(r.ledger, ledgerChannel{storedChannel: c, Point: p.Revision})
	}
	for _, l := range removed {
		r.launched = append(r.launched, ledgerLauncher{storedLauncher: l, Point: p.Revision})
	}
	r.state.Points = append(r.state.Points, p)
	if n := len(r.state.Points) - maxChannelPoints; n > 0 {
		r.state.Points = slices.Delete(r.state.Points, 0, n)
	}
	r.keepPoints()
	r.state.Revision++
}

// keepPoints drops the ledger rows of points that are gone.
func (r *execChannelRecords) keepPoints() {
	kept := map[int64]bool{}
	for _, p := range r.state.Points {
		kept[p.Revision] = true
	}
	r.ledger = slices.DeleteFunc(r.ledger, func(c ledgerChannel) bool { return !kept[c.Point] })
	r.launched = slices.DeleteFunc(r.launched, func(l ledgerLauncher) bool { return !kept[l.Point] })
}

// restore is the catalog and real launchers of the point at index i, and the names of the real launchers it brought
// back; the point itself is taken out of the ledger. Restored channels get a new revision (now), so a computer
// drops what it pulled of the version it replaces.
func (r *execChannelRecords) restore(i int, now time.Time) (catalog []storedChannel, owned []storedLauncher, back []string, dropped []storedLauncher) {
	p := r.state.Points[i]
	catalog = []storedChannel{}
	for _, c := range r.ledger {
		if c.Point == p.Revision {
			ch := c.storedChannel
			ch.Updated = now
			catalog = append(catalog, ch)
		}
	}
	taken := map[string]bool{}
	for _, l := range withChannelAliases(nil, catalog) {
		taken[l.Name] = true
	}
	for _, l := range r.owned {
		if slices.Contains(p.Drop, l.Name) {
			dropped = append(dropped, l)
			continue
		}
		taken[l.Name] = true
		owned = append(owned, l)
	}
	for _, l := range r.launched {
		if l.Point == p.Revision && !taken[l.Name] {
			taken[l.Name] = true
			owned = append(owned, l.storedLauncher)
			back = append(back, l.Name)
		}
	}
	r.state.Points = slices.Delete(r.state.Points, i, i+1)
	r.keepPoints()
	return catalog, owned, back, dropped
}

// storeChannelRecords writes r (the catalog, the ledger and the real launchers with the aliases the catalog makes) in
// one transaction and tells the computers, as a launcher's save does (a computer's own key for an alias that is gone
// goes with it); s.models.mu held.
func (s *Service) storeChannelRecords(ctx context.Context, r execChannelRecords) error {
	catalog, ledger, launched := r.catalog, r.ledger, r.launched
	if catalog == nil {
		catalog = []storedChannel{}
	}
	if ledger == nil {
		ledger = []ledgerChannel{}
	}
	if launched == nil {
		launched = []ledgerLauncher{}
	}
	more := map[string]any{execChannelsKey: catalog, execChannelStateKey: r.state, execChannelLedgerKey: ledger,
		execChannelLedgerLaunchersKey: launched}
	if err := s.storeManagedAnd(ctx, withChannelAliases(r.owned, catalog), more); err != nil {
		return err
	}
	s.execChannels.set(catalog)
	return nil
}

// ---- changing ----

var (
	errChannelRevision   = errors.New("channel catalog revision mismatch")
	errChannelNotFound   = errors.New("no such channel")
	errChannelNoRollback = errors.New("no channel rollback point")
)

// channelErr is err in the user's words, or err itself.
func channelErr(err error) error {
	switch {
	case errors.Is(err, errChannelRevision):
		return i18n.Err("assistant-execution.err_channel_revision")
	case errors.Is(err, errChannelNotFound):
		return i18n.Err("assistant-execution.err_channel_not_found")
	case errors.Is(err, errChannelNoRollback):
		return i18n.Err("assistant-execution.err_no_rollback")
	}
	return err
}

// checkChannel checks in as a channel to keep beside the others, the real launchers and the runtime declarations.
func checkChannel(in proto.ChannelConfig, r execChannelRecords, caps map[string]proto.RuntimeCapability) error {
	bad := func(field string) error {
		return i18n.Err("assistant-execution.err_channel_field", i18n.Args{"channel": in.Name, "field": field})
	}
	if len(in.Name) > 64 || !managedLauncherName.MatchString(in.Name) {
		return i18n.Err("assistant-execution.err_channel_name", i18n.Args{"name": in.Name})
	}
	if u, err := url.Parse(in.BaseURL); err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return bad("base_url")
	}
	if len(in.Endpoints) == 0 || len(in.Endpoints) > maxChannelEndpoints {
		return bad("endpoints")
	}
	for i, p := range in.Endpoints {
		if !p.WellFormed() || slices.Contains(in.Endpoints[:i], p) {
			return bad("endpoints")
		}
	}
	switch {
	case !slices.Contains([]string{"", "api_key", "bearer"}, in.Auth):
		return bad("auth")
	case !slices.Contains([]string{"", shareAll, shareSelected, shareNone}, in.Share):
		return bad("share")
	case in.Share == shareSelected && len(in.ScopeMachines) == 0:
		return bad("scope_machines")
	case !slices.Contains([]string{"", proto.DeliveryOnDemand, proto.DeliveryPush}, in.Delivery):
		return bad("delivery")
	case in.Quota != nil && !(GatewayQuotaPool{Source: in.Quota.Source, Provider: in.Quota.Provider}).valid():
		return i18n.Err("assistant-execution.err_channel_quota", i18n.Args{"channel": in.Name})
	case len(in.Models) > maxChannelModels:
		return bad("models")
	case len(in.Bindings) == 0 || len(in.Bindings) > maxChannelBindings:
		return bad("bindings")
	}
	for i, m := range in.Models {
		if m.Canonical == "" || checkModelID(m.Canonical) != nil ||
			slices.ContainsFunc(in.Models[:i], func(o proto.ChannelModel) bool { return o.Canonical == m.Canonical }) {
			return bad("models")
		}
		for _, e := range m.Edges {
			if !e.Protocol.WellFormed() || !e.Tier.Valid() {
				return bad("models")
			}
		}
	}
	own := map[string]bool{}
	for _, l := range r.owned {
		own[l.Name] = true
	}
	if own[in.Name] && !slices.Contains(in.ReplaceLaunchers, in.Name) {
		return i18n.Err("assistant-execution.err_channel_name_taken", i18n.Args{"name": in.Name})
	}
	others := map[string]string{}
	for _, c := range r.catalog {
		if c.Name == in.Name {
			continue
		}
		for _, b := range c.Bindings {
			others[b.Alias] = c.Name
		}
	}
	for i, b := range in.Bindings {
		if !managedLauncherName.MatchString(b.Alias) || slices.Contains(reservedLauncherNames, b.Alias) ||
			proto.IsExternalLauncher(b.Alias) || !slices.Contains([]string{"", "messages", "responses", "chat"}, b.API) {
			return bad("bindings")
		}
		if slices.ContainsFunc(in.Bindings[:i], func(o proto.ChannelBinding) bool {
			return o.Alias == b.Alias || o.Runtime == b.Runtime && o.API == b.API
		}) {
			return bad("bindings")
		}
		if c, ok := others[b.Alias]; ok || own[b.Alias] && !slices.Contains(in.ReplaceLaunchers, b.Alias) {
			return i18n.Err("assistant-execution.err_alias_taken", i18n.Args{"launcher": b.Alias, "channel": cmp.Or(c, b.Alias)})
		}
		if b.API != "" {
			// a direct API connection of the channel: no runtime runs it, the API purposes call it
			if b.Runtime != "" || !slices.Contains(in.Endpoints, managedProtocol(proto.ManagedLauncher{Kind: "api", API: b.API}, caps)) {
				return bad("bindings")
			}
			continue
		}
		rc, ok := caps[b.Runtime]
		if !ok {
			return i18n.Err("assistant-execution.reason_runtime_unknown", i18n.Args{"runtime": b.Runtime})
		}
		if !rc.Managed {
			return i18n.Err("assistant-execution.err_runtime_unmanaged", i18n.Args{"runtime": b.Runtime})
		}
		if !slices.ContainsFunc(in.Endpoints, rc.Speaks) {
			return i18n.Err("assistant-execution.reason_no_common_protocol", i18n.Args{"runtime": b.Runtime,
				"channel": cmp.Or(in.DisplayName, in.Name), "needs": protocolList(rc.Protocols), "offers": protocolList(in.Endpoints)})
		}
	}
	for _, name := range in.ReplaceLaunchers {
		if !own[name] || !slices.ContainsFunc(in.Bindings, func(b proto.ChannelBinding) bool { return b.Alias == name }) {
			return i18n.Err("assistant-execution.err_replace", i18n.Args{"launcher": name, "channel": in.Name})
		}
	}
	return nil
}

// saveChannel keeps in (over the channel of its name), folding the launchers it replaces into it, as a change at
// revision (nil: whatever the catalog's is).
func (s *Service) saveChannel(ctx context.Context, in proto.ChannelConfig, revision *int64) error {
	in = trimChannelConfig(in)
	caps := mergeRuntimeCaps(builtinRuntimeCaps(), s.executionSettings(ctx).Runtimes)
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	r, err := s.readChannelRecords(ctx)
	if err != nil {
		return err
	}
	if revision != nil && *revision != r.state.Revision {
		return errChannelRevision
	}
	i := slices.IndexFunc(r.catalog, func(c storedChannel) bool { return c.Name == in.Name })
	if i < 0 && len(r.catalog) >= maxChannels {
		return i18n.Err("assistant-execution.err_channel_field", i18n.Args{"channel": in.Name, "field": "channels"})
	}
	if err := checkChannel(in, r, caps); err != nil {
		return err
	}
	now := time.Now().UTC()
	c := storedChannel{Name: in.Name, DisplayName: in.DisplayName, BaseURL: in.BaseURL, Auth: in.Auth, Proxy: in.Proxy,
		NoProxy: in.NoProxy, Share: in.Share, ScopeMachines: in.ScopeMachines, Delivery: in.Delivery,
		Endpoints: in.Endpoints, Models: in.Models, Quota: in.Quota, Bindings: in.Bindings, Created: now, Updated: now}
	if i >= 0 {
		old := r.catalog[i]
		c.Created, c.KeySealed, c.HeadersSealed = old.Created, old.KeySealed, old.HeadersSealed
	}
	var removed []storedLauncher
	for _, name := range in.ReplaceLaunchers {
		j := slices.IndexFunc(r.owned, func(l storedLauncher) bool { return l.Name == name })
		l := r.owned[j]
		removed = append(removed, l)
		if c.KeySealed == "" {
			c.KeySealed = l.KeySealed
		}
		if c.HeadersSealed == "" {
			c.HeadersSealed = l.HeadersSealed
		}
		if in.Share == "" && c.Share == "" {
			c.Share, c.ScopeMachines = l.share(), slices.Clone(l.ScopeMachines)
		}
		c.Auth, c.Proxy, c.NoProxy = cmp.Or(c.Auth, l.Auth), cmp.Or(c.Proxy, l.Proxy), cmp.Or(c.NoProxy, l.NoProxy)
		c.Delivery = cmp.Or(c.Delivery, l.Delivery)
	}
	if in.APIKey != "" {
		if c.KeySealed, err = s.sealKey(in.APIKey); err != nil {
			return err
		}
	}
	if in.Headers != "" {
		if c.HeadersSealed, err = s.sealKey(in.Headers); err != nil {
			return err
		}
	}
	c.Share, c.Delivery = cmp.Or(c.Share, shareAll), cmp.Or(c.Delivery, proto.DeliveryOnDemand)
	r.point(now, "save", c.Name, removed, nil)
	r.owned = slices.DeleteFunc(r.owned, func(l storedLauncher) bool { return slices.Contains(in.ReplaceLaunchers, l.Name) })
	r.catalog = slices.Clone(r.catalog)
	if i >= 0 {
		r.catalog[i] = c
	} else {
		r.catalog = append(r.catalog, c)
	}
	slices.SortFunc(r.catalog, func(a, b storedChannel) int { return strings.Compare(a.Name, b.Name) })
	return s.storeChannelRecords(ctx, r)
}

// trimChannelConfig is in with the spaces around its words removed.
func trimChannelConfig(in proto.ChannelConfig) proto.ChannelConfig {
	in.Name, in.DisplayName, in.BaseURL = strings.TrimSpace(in.Name), strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.BaseURL)
	in.APIKey, in.Auth, in.Proxy, in.NoProxy = strings.TrimSpace(in.APIKey), strings.TrimSpace(in.Auth), strings.TrimSpace(in.Proxy), strings.TrimSpace(in.NoProxy)
	in.Share, in.Delivery = strings.TrimSpace(in.Share), strings.TrimSpace(in.Delivery)
	in.Bindings = slices.Clone(in.Bindings)
	for i := range in.Bindings {
		b := &in.Bindings[i]
		b.Runtime, b.Alias, b.API = strings.TrimSpace(b.Runtime), strings.TrimSpace(b.Alias), strings.TrimSpace(b.API)
		b.BaseURL, b.Model, b.Effort = strings.TrimSpace(b.BaseURL), strings.TrimSpace(b.Model), strings.TrimSpace(b.Effort)
	}
	if in.Quota != nil {
		q := proto.ChannelQuota{Source: strings.TrimSpace(in.Quota.Source), Provider: strings.TrimSpace(in.Quota.Provider)}
		in.Quota = &q
	}
	return in
}

// deleteChannel removes the channel called name, and with it its aliases, as a change at revision (nil: any).
func (s *Service) deleteChannel(ctx context.Context, name string, revision *int64) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	r, err := s.readChannelRecords(ctx)
	if err != nil {
		return err
	}
	if revision != nil && *revision != r.state.Revision {
		return errChannelRevision
	}
	i := slices.IndexFunc(r.catalog, func(c storedChannel) bool { return c.Name == name })
	if i < 0 {
		return errChannelNotFound
	}
	r.point(time.Now().UTC(), "delete", name, nil, nil)
	r.catalog = slices.Delete(slices.Clone(r.catalog), i, i+1)
	return s.storeChannelRecords(ctx, r)
}

// rollbackChannels restores the rollback point of revision (nil: the newest). What it replaces becomes a point of its
// own, so a rollback can be undone the same way. Like any change, it stops no running session: new sessions, and
// each running one's next turn, use what it restores.
func (s *Service) rollbackChannels(ctx context.Context, revision *int64) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	r, err := s.readChannelRecords(ctx)
	if err != nil {
		return err
	}
	i := len(r.state.Points) - 1
	if revision != nil {
		i = slices.IndexFunc(r.state.Points, func(p execChannelPoint) bool { return p.Revision == *revision })
	}
	if i < 0 {
		return errChannelNoRollback
	}
	now := time.Now().UTC()
	catalog, owned, back, dropped := r.restore(i, now)
	r.point(now, "rollback", "", dropped, back)
	r.catalog, r.owned = catalog, owned
	return s.storeChannelRecords(ctx, r)
}

// channelCatalogView is GET /api/v1/execution/channels.
func (s *Service) channelCatalogView(ctx context.Context) (proto.ChannelCatalogView, error) {
	catalog, err := s.readChannelCatalog(ctx)
	if err != nil {
		return proto.ChannelCatalogView{}, err
	}
	var state execChannelState
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, execChannelStateKey, &state); err != nil {
			return proto.ChannelCatalogView{}, err
		}
	}
	v := proto.ChannelCatalogView{Revision: state.Revision, Channels: []proto.ChannelConfig{}, Rollbacks: []proto.ChannelRevision{}}
	for _, c := range catalog {
		v.Channels = append(v.Channels, c.view())
	}
	for i := len(state.Points) - 1; i >= 0; i-- {
		p := state.Points[i]
		v.Rollbacks = append(v.Rollbacks, proto.ChannelRevision{Revision: p.Revision, At: p.At, Action: p.Action,
			Channel: p.Channel, Channels: slices.Clone(p.Names)})
	}
	return v, nil
}

// channelQuotaOf is the usage source of the channel launcher is an alias of; false for a launcher of its own, or a
// channel that declares none.
func (s *Service) channelQuotaOf(ctx context.Context, launcher string) (GatewayQuotaPool, bool) {
	l, ok := s.managedNamed(ctx, launcher)
	if !ok || l.Channel == "" {
		return GatewayQuotaPool{}, false
	}
	catalog, _ := s.channelCatalog(ctx)
	i := slices.IndexFunc(catalog, func(c storedChannel) bool { return c.Name == l.Channel })
	if i < 0 || catalog[i].Quota == nil {
		return GatewayQuotaPool{}, false
	}
	pool := GatewayQuotaPool{Source: catalog[i].Quota.Source, Provider: catalog[i].Quota.Provider}
	return pool, pool.valid()
}

// channelPoolModel is the name model's use is counted under in the quota source of launcher's channel: the model's
// declared pool, else model itself.
func (s *Service) channelPoolModel(ctx context.Context, launcher, model string) string {
	l, ok := s.managedNamed(ctx, launcher)
	if !ok || l.Channel == "" || model == "" {
		return model
	}
	catalog, _ := s.channelCatalog(ctx)
	for _, c := range catalog {
		if c.Name != l.Channel {
			continue
		}
		ch := c.supply()
		if m, _, ok := findModel(ch, model); ok && m.QuotaPool != "" {
			return m.QuotaPool
		}
	}
	return model
}
