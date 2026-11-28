package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/usagesource"
)

// Account-pool gateways as a source of subscription usage windows (#2595, docs/server/usage-windows.md "Account-pool gateways").
//
// A relay (an account-pool gateway) holds several agent accounts and already knows how much of each one's 5-hour and weekly
// allowance is used. The manager asks it, read-only, every few minutes (usagesource) and keeps the last answer of each
// source in memory; the sidebar's merge (usagewindows.go) puts those accounts next to the ones the machines read and
// shows an account both saw once. The key is only ever in the manager's manager.json (0600; its usage section, beside the
// extensions' sections, docs/operations/config.md "manager.json") and in the request's Authorization header:
// not in a log line, an error, an API answer, the bus or the database.

const (
	usagePollDefault = 5 // minutes
	usagePollMost    = 24 * 60
	usageSourcesMost = 8
	// A gateway's numbers move with the traffic it carries (it harvests them from what upstream attaches to
	// ordinary responses), so an idle account's sample is hours old and still the latest there is: faded only after
	// this, where the machines' own samples fade after usageHubStaleAfter.
	usageGatewayStaleAfter = 6 * time.Hour
	usageGatewayFetchMost  = 45 * time.Second // one source's whole read (its calls are 15 s each)
	usageGatewayTick       = 15 * time.Second // how often the loop looks for a source that is due
)

// UsageConfig is manager.json's "usage".
type UsageConfig struct {
	// Enabled false: no gateway is asked at all (unset: asked). The machines' own windows are not affected.
	Enabled *bool `json:"enabled,omitempty"`
	// PollMinutes is how often each gateway is asked (default 5, at most a day).
	PollMinutes int                 `json:"poll_minutes,omitempty"`
	Sources     []UsageSourceConfig `json:"sources,omitempty"`
	// HiddenProviders are providers ("codex", "claude"…) the usage bars leave out (#11753). Display only: nodes and
	// gateways are still read and the scheduler's capacity view still counts them.
	HiddenProviders []string `json:"hidden_providers,omitempty"`
}

// UsageSourceConfig is one gateway.
type UsageSourceConfig struct {
	ID string `json:"id"`
	// Name is what the sidebar calls the source ("Relay").
	Name string `json:"name"`
	// Type: relay | relay_key | generic | extension (usagesource; an older manager.json's earlier name of relay reads as relay).
	Type string `json:"type"`
	URL  string `json:"url"`
	// Extension and Launcher are for the extension type only (usagesource.TypeExtension): the extension that reads, by
	// id, and the launcher whose gateway it reads. URL and Key stay empty; the launcher has both.
	Extension string `json:"extension,omitempty"`
	Launcher  string `json:"launcher,omitempty"`
	// Key is the gateway's management key, for relay_key an ordinary API key of the gateway (a secret either way:
	// manager.json is 0600; no API answer or log ever has it).
	Key     string `json:"key,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	direct  *DirectUsageSource
}

// DirectUsageSource is an installed contribution, separate from persisted manager settings.
type DirectUsageSource struct {
	extension    string
	contribution string
	name         string
	updatedAt    time.Time
}

// NewDirectUsageSource describes an installed direct usage contribution at a settings revision.
func NewDirectUsageSource(extension, contribution, name string, updatedAt time.Time) DirectUsageSource {
	return DirectUsageSource{extension: extension, contribution: contribution, name: name, updatedAt: updatedAt}
}

func (s DirectUsageSource) Extension() string    { return s.extension }
func (s DirectUsageSource) Contribution() string { return s.contribution }
func (s DirectUsageSource) Name() string         { return s.name }
func (s DirectUsageSource) UpdatedAt() time.Time { return s.updatedAt }

// UnmarshalJSON reads a source as manager.json has it, older type names included (usagesource.StoredType): a section
// written before the relay type had its name keeps working, and the next edit of the source saves the new name.
func (s *UsageSourceConfig) UnmarshalJSON(b []byte) error {
	type plain UsageSourceConfig
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = UsageSourceConfig(p)
	s.Type = usagesource.StoredType(s.Type)
	return nil
}

func (c UsageConfig) on() bool       { return c.Enabled == nil || *c.Enabled }
func (s UsageSourceConfig) on() bool { return s.Enabled == nil || *s.Enabled }
func (c UsageConfig) interval() time.Duration {
	m := c.PollMinutes
	if m <= 0 {
		m = usagePollDefault
	}
	return time.Duration(min(m, usagePollMost)) * time.Minute
}

// fingerprint changes when what a source reads changes (its type, address or key): the samples of the old one are
// not the new one's.
func (s UsageSourceConfig) fingerprint() string {
	sum := sha256.Sum256([]byte(s.Type + "\x00" + s.URL + "\x00" + s.Key + "\x00" + s.Extension + "\x00" + s.Launcher))
	if s.direct != nil {
		sum = sha256.Sum256([]byte(s.ID + "\x00" + s.direct.updatedAt.Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(sum[:8])
}

// gatewayState is what is known of one source.
type gatewayState struct {
	fingerprint string
	samples     []proto.UsageAccount // the last answer that worked; Sources and Ref are put on when read
	okAt        time.Time            // when it worked
	triedAt     time.Time            // the last attempt, worked or not
	errCode     string               // usagesource code of the last attempt; "" when it worked
	seen        usagesource.Seen
	// inventory is the last answer's account list (usage.gateway_accounts; relay only) and complete whether it
	// was the whole list.
	inventory []proto.GatewayAccount
	complete  bool
	reading   bool
}

// usageGateways polls the sources and holds their last answers.
type usageGateways struct {
	mu    sync.Mutex
	cfg   UsageConfig
	state map[string]*gatewayState
	wake  chan struct{}
	// fetch reads one source (usagesource.Fetch; tests put their own).
	fetch func(ctx context.Context, cfg usagesource.Config, now time.Time) (usagesource.Result, error)
	// fetchExtension reads one extension source (Options.UsageExtension; nil: none can be read here).
	fetchExtension func(ctx context.Context, extension, launcher string) (json.RawMessage, error)
	discoverDirect func() []DirectUsageSource
	fetchDirect    func(context.Context, DirectUsageSource) (json.RawMessage, error)
	direct         []UsageSourceConfig
	// changed is called when the answers changed (the merged list is stale).
	changed func()
	logf    func(string, ...any)
}

func (g *usageGateways) init(cfg UsageConfig, changed func(), logf func(string, ...any)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.changed, g.logf = changed, logf
	g.setLocked(cfg)
}

// setConfig is the admin UI's edit, applied at once: sources added or changed are read soon, removed ones forgotten.
func (g *usageGateways) setConfig(cfg UsageConfig) {
	g.mu.Lock()
	g.setLocked(cfg)
	changed := g.changed
	g.mu.Unlock()
	g.poke()
	if changed != nil {
		changed()
	}
}

func (g *usageGateways) setLocked(cfg UsageConfig) {
	cfg.Sources = slices.Clone(cfg.Sources)
	cfg.HiddenProviders = canonicalHiddenUsageProviders(cfg.HiddenProviders)
	g.cfg = cfg
	g.pruneLocked()
}

func (g *usageGateways) sourcesLocked() []UsageSourceConfig {
	return slices.Concat(g.cfg.Sources, g.direct)
}

func (g *usageGateways) pruneLocked() {
	if g.state == nil {
		g.state = map[string]*gatewayState{}
	}
	keep := map[string]bool{}
	for _, s := range g.sourcesLocked() {
		keep[s.ID] = true
		if st := g.state[s.ID]; st != nil && st.fingerprint != s.fingerprint() {
			delete(g.state, s.ID)
		}
	}
	for id := range g.state {
		if !keep[id] {
			delete(g.state, id)
		}
	}
}

func canonicalHiddenUsageProviders(in []string) []string {
	var out []string
	for _, provider := range in {
		out = append(out, canonicalUsageProvider(provider))
	}
	return uniqueSorted(out)
}

func (g *usageGateways) config() UsageConfig {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.cfg
	c.Sources = slices.Clone(c.Sources)
	c.HiddenProviders = slices.Clone(c.HiddenProviders)
	return c
}

func (g *usageGateways) poke() {
	g.mu.Lock()
	if g.wake == nil {
		g.wake = make(chan struct{}, 1)
	}
	w := g.wake
	g.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

// run asks every source that is due, and again when the config changes, until ctx ends.
func (g *usageGateways) run(ctx context.Context) {
	g.mu.Lock()
	if g.wake == nil {
		g.wake = make(chan struct{}, 1)
	}
	wake := g.wake
	g.mu.Unlock()
	tick := time.NewTicker(usageGatewayTick)
	defer tick.Stop()
	for {
		g.pollDue(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-wake:
		}
	}
}

// pollDue reads, in parallel, the sources that were never read or whose last attempt is older than the interval.
func (g *usageGateways) pollDue(ctx context.Context, now time.Time) {
	var direct []UsageSourceConfig
	if g.discoverDirect != nil {
		for _, d := range g.discoverDirect() {
			direct = append(direct, UsageSourceConfig{
				ID: "extension:" + d.extension + "/" + d.contribution, Name: d.name, Type: usagesource.TypeExtension,
				Extension: d.extension, direct: &d,
			})
		}
	}
	g.mu.Lock()
	changedSources := !slices.EqualFunc(g.direct, direct, func(a, b UsageSourceConfig) bool { return *a.direct == *b.direct })
	if changedSources {
		g.direct = direct
		g.pruneLocked()
	}
	changed := g.changed
	defer func() {
		if changedSources && changed != nil {
			changed()
		}
	}()
	if !g.cfg.on() {
		g.mu.Unlock()
		return
	}
	var due []UsageSourceConfig
	for _, s := range g.sourcesLocked() {
		interval := g.cfg.interval()
		if !s.on() {
			continue
		}
		st := g.state[s.ID]
		if s.direct != nil && st != nil && st.okAt.IsZero() {
			interval = usageGatewayTick
		}
		if st != nil && (st.reading || now.Sub(st.triedAt) < interval) {
			continue
		}
		if st == nil {
			st = &gatewayState{fingerprint: s.fingerprint()}
			g.state[s.ID] = st
		}
		st.reading = true
		due = append(due, s)
	}
	g.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.read(ctx, s, now)
		}()
	}
	wg.Wait()
}

func (g *usageGateways) read(ctx context.Context, s UsageSourceConfig, now time.Time) {
	c, cancel := context.WithTimeout(ctx, usageGatewayFetchMost)
	defer cancel()
	fetch := g.fetch
	if fetch == nil {
		fetch = usagesource.Fetch
	}
	var res usagesource.Result
	var err error
	if s.Type == usagesource.TypeExtension {
		res, err = g.readExtension(c, s, now)
	} else {
		res, err = fetch(c, usagesource.Config{Type: s.Type, URL: s.URL, Key: s.Key}, now)
	}
	g.mu.Lock()
	st := g.state[s.ID]
	if st == nil || st.fingerprint != s.fingerprint() { // edited or removed while it was being read
		g.mu.Unlock()
		return
	}
	st.reading = false
	st.triedAt = now
	if err != nil {
		code := usagesource.CodeOf(err)
		if st.errCode != code && g.logf != nil { // once per change, and only the code: never the gateway's text or the key
			g.logf("usage source %s: %s", s.ID, code)
		}
		st.errCode = code
	} else {
		if st.errCode != "" && g.logf != nil {
			g.logf("usage source %s: reading again", s.ID)
		}
		st.errCode, st.samples, st.okAt, st.seen = "", res.Accounts, now, res.Seen
		st.inventory, st.complete = res.Inventory, res.Complete
	}
	changed := g.changed
	g.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// readExtension asks the extension of an extension source (usage.query): its answer is checked and cut to size
// (usagesource.FromExtension). The extension's own errors are not kept: only that it failed.
func (g *usageGateways) readExtension(ctx context.Context, s UsageSourceConfig, now time.Time) (usagesource.Result, error) {
	var raw json.RawMessage
	var err error
	switch {
	case s.direct != nil && g.fetchDirect != nil:
		raw, err = g.fetchDirect(ctx, *s.direct)
	case s.direct == nil && g.fetchExtension != nil && s.Extension != "" && s.Launcher != "":
		raw, err = g.fetchExtension(ctx, s.Extension, s.Launcher)
	default:
		return usagesource.Result{}, &usagesource.Error{Code: usagesource.CodeBadConfig}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return usagesource.Result{}, &usagesource.Error{Code: usagesource.CodeTimeout}
		}
		return usagesource.Result{}, &usagesource.Error{Code: usagesource.CodeUnreachable}
	}
	return usagesource.FromExtension(raw, now)
}

// accounts are the samples of every source that is on, as the sidebar's merge takes them: named after their source,
// with the gateway's account id (qualified by the source) as their identity. A source whose last read failed still
// gives its last answer, marked as such.
func (g *usageGateways) accounts(now time.Time) []proto.UsageAccount {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.cfg.on() {
		return nil
	}
	var out []proto.UsageAccount
	for _, s := range g.sourcesLocked() {
		st := g.state[s.ID]
		if !s.on() || st == nil {
			continue
		}
		extensionIDs := make(map[[2]string]int)
		for _, a := range st.samples {
			if a.Pass && a.Ref != "" {
				extensionIDs[[2]string{canonicalUsageProvider(a.Provider), a.Ref}]++
			}
		}
		for n, a := range st.samples {
			if now.Sub(a.SampledAt) > usageHubMaxAge {
				continue
			}
			a.Windows = slices.Clone(a.Windows)
			a.Sources = []string{s.Name}
			if a.Pass {
				// Extension rows never merge. Prefer their own account ID so a reorder does not retarget a pin.
				a.Origin = s.ID + "#" + strconv.Itoa(n)
				if a.Ref != "" && extensionIDs[[2]string{canonicalUsageProvider(a.Provider), a.Ref}] == 1 {
					a.Origin = s.ID + ":account:" + a.Ref
				}
				a.Ref = ""
			} else if a.Ref != "" {
				a.Ref = s.ID + ":" + a.Ref
				a.Origin = a.Ref
			} else {
				a.Origin = s.ID + "#" + strconv.Itoa(n)
			}
			if st.errCode != "" {
				a.Problem, a.Stale = usageReadProblem(st.errCode), true
			}
			if now.Sub(a.SampledAt) > usageGatewayStaleAfter {
				a.Stale = true
			}
			out = append(out, a)
		}
	}
	return out
}

// usageReadProblem is what the usage bars say about a source whose last read failed, from its error code (#1902): the
// gateway turned the key away (the fix is a new key under Admin → Usage sources), could not be reached (the network),
// or answered with something else that went wrong. The words for each code stay on the admin page; the bars only need
// to point the right way. It replaces a relay's own "error" on an account: the source failing is the newer news.
func usageReadProblem(code string) string {
	switch code {
	case usagesource.CodeUnauthorized, usagesource.CodeForbidden:
		return "rejected"
	case usagesource.CodeUnreachable, usagesource.CodeTimeout:
		return "unreachable"
	default:
		return "failed"
	}
}

// gatewayAccounts is usage.gateway_accounts (#4576, docs/server/usage-windows.md "The gateway's account list"): every account the
// gateway sources that are on listed, disabled ones included, from their last read that worked (nothing is asked of a
// gateway for it). A source whose last read failed still gives that list, its accounts marked stale and its error
// code said; one not read yet is listed with no accounts and the error usagesource.CodePending, so that a consumer
// does not take it for a gateway with no accounts. Only the relay type has an account list. No key, no
// address: a source is its id, name and type.
func (g *usageGateways) gatewayAccounts() proto.GatewayAccounts {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := proto.GatewayAccounts{Sources: []proto.GatewaySource{}, Accounts: []proto.GatewayAccount{}}
	if !g.cfg.on() {
		return out
	}
	for _, s := range g.cfg.Sources {
		if !s.on() || s.Type != usagesource.TypeRelay {
			continue
		}
		src := proto.GatewaySource{ID: s.ID, Name: s.Name, Type: s.Type}
		st := g.state[s.ID]
		if st != nil {
			src.Error, src.Complete = st.errCode, st.complete
			if !st.okAt.IsZero() {
				t := st.okAt.UTC()
				src.CheckedAt = &t
				src.Listed = st.seen.Listed
			}
		}
		if src.CheckedAt == nil && src.Error == "" {
			src.Error = usagesource.CodePending
		}
		if st != nil {
			for _, a := range st.inventory {
				a.Source = s.ID
				a.Windows = slices.Clone(a.Windows)
				if src.Error != "" {
					a.Stale = true
				}
				out.Accounts = append(out.Accounts, a)
			}
		}
		out.Sources = append(out.Sources, src)
	}
	return out
}

// gatewayStatus is one source as the admin page shows it.
type gatewayStatus struct {
	OKAt    *time.Time        `json:"ok_at,omitempty"`    // the last read that worked
	TriedAt *time.Time        `json:"tried_at,omitempty"` // the last attempt
	Error   string            `json:"error,omitempty"`    // its code, when it failed
	Seen    *usagesource.Seen `json:"seen,omitempty"`
}

func (g *usageGateways) status(id string) gatewayStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state[id]
	if st == nil {
		return gatewayStatus{}
	}
	out := gatewayStatus{Error: st.errCode}
	if !st.okAt.IsZero() {
		t := st.okAt
		out.OKAt = &t
		seen := st.seen
		seen.Providers = map[string]int{}
		for k, v := range st.seen.Providers {
			seen.Providers[k] = v
		}
		out.Seen = &seen
	}
	if !st.triedAt.IsZero() {
		t := st.triedAt
		out.TriedAt = &t
	}
	return out
}

func uniqueSorted(list []string) []string {
	out := slices.Clone(list)
	sort.Strings(out)
	return slices.Compact(out)
}

// UsageGatewayAccounts returns the last-read gateway account lists for the bus and the local extension manager.
func (d *Devices) UsageGatewayAccounts() proto.GatewayAccounts { return d.usageGW.gatewayAccounts() }
