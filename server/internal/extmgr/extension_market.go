package extmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/netproxy"
)

// Plugin markets (docs/extensions/extensions.md "Extension marketplace"): the hub keeps the markets the user registered and their
// indexes, lists their plugins as one catalog, and installs one from a market. A device with full rights may do all of
// this; other sources (a directory, a file, a URL, an upload) stay with the hub's machine and the admin UI.

// marketStale is how old an index may be before the catalog reads it again (in the background).
const marketStale = 6 * time.Hour

// marketCAFileMost bounds a configured CA bundle so a bad path cannot turn into an unbounded read.
const marketCAFileMost = 1 << 20

// marketSelfRefreshWindow is shared by every extension installed from one market: they share the market's upstream
// token and API quota, so their accepted self-refresh requests share one window too.
const marketSelfRefreshWindow = 10 * time.Minute

type markets struct {
	mu     sync.Mutex
	loaded bool
	byID   map[string]*marketState
	// reading: the markets being read in the background, each with a channel closed when its read ends.
	reading map[string]chan struct{}
	// Client and GitHubAPI: tests point them at a fake. now is nil in production, and a clock in deterministic tests.
	client    *http.Client
	githubAPI string
	now       func() time.Time
}

type marketState struct {
	rec   extension.MarketRecord
	index *extension.MarketIndex
	// selfRefreshAt is when the last extension-initiated refresh was accepted, not when its network read finished.
	selfRefreshAt time.Time
}

// errMarketSignature: a market's index was read but its signature refuses it (the last good one is kept but not
// installed from). A catalog that could not be read is extension.ErrCatalogUnavailable instead.
var errMarketSignature = errors.New("market signature")

// signedIndex enforces official identity independently of stored signature state.
func signedIndex(rec extension.MarketRecord, f *extension.Fetched, now time.Time, build int64) (extension.MarketRecord, *extension.MarketIndex, error) {
	key := ""
	rec.FetchedAt = &now
	if extension.IsOfficialMarket(rec.Spec) {
		key = extension.Official.SigningKey
		if _, err := extension.ParseSigningKey(key); err != nil {
			rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			rec.Error = i18n.T(i18n.Default, "extension-market_security.signature")
			return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
		}
		if rec.SigningKey != "" && rec.SigningKey != key {
			rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			rec.Error = i18n.T(i18n.Default, "extension-market_security.owner_pin")
			return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
		}
	} else {
		rec.SigningKey = ""
	}
	keys := rec.TrustedKeys
	if key != "" {
		keys = []string{key}
	} else if len(keys) > 0 {
		// A private market's owner keys (extension_market_keys.go): any one of them must sign the catalog.
		normalized, err := extension.NormalizeMarketKeys(keys)
		if err != nil || len(normalized) == 0 {
			rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			rec.Error = i18n.T(i18n.Default, "extension-market_security.private_signature")
			return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
		}
		keys = normalized
	}
	verified, err := extension.ParseCatalogKeysForBuild(f.Raw, keys, build)
	if err != nil {
		rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
		rec.Error = err.Error()
		return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
	}
	rec.Signature = extension.MarketSignature{State: extension.SigUnsigned}
	if verified.SignedBy != "" {
		rec.Signature = extension.MarketSignature{State: extension.SigSigned, KeyID: keyIDOf(verified.SignedBy)}
	}
	// An empty owner pin is not silently rewritten. Runtime verification uses only
	// the compiled-in key; approved pin migration belongs to the rotation workflow.
	rec.Error, rec.Name, rec.Warnings, rec.Index = "", verified.Index.Name, verified.Warnings, append([]byte(nil), verified.Raw...)
	return rec, verified.Index, nil
}

func (h *Manager) marketStore() extension.MarketStore {
	s, _ := h.store.(extension.MarketStore)
	return s
}

func (h *Manager) marketsState() *markets {
	if h.extensions == nil {
		return nil
	}
	return &h.extensions.markets
}

// load reads the markets from the store once.
func (h *Manager) marketsLoad(ctx context.Context) error {
	ms, st := h.marketsState(), h.marketStore()
	if ms == nil || st == nil {
		return i18n.Err("hub-extension_market.no_database")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.loaded {
		return nil
	}
	rows, err := st.ExtensionMarkets(ctx)
	if err != nil {
		return err
	}
	ms.byID = map[string]*marketState{}
	for _, r := range rows {
		state := &marketState{rec: r}
		if len(r.Index) > 0 {
			// Never trust a cached state flag: the complete envelope is verified again.
			f := &extension.Fetched{Raw: r.Index}
			rec, ix, verifyErr := signedIndex(r, f, time.Now(), h.build.Number)
			rec.FetchedAt = r.FetchedAt
			if r.Signature.Bad() && requiresSignature(r) {
				rec.Signature, rec.Error = r.Signature, r.Error
				ix = nil
			}
			if verifyErr != nil || ix == nil {
				rec.FetchedAt = nil
			}
			state.rec, state.index = rec, ix
		} else if extension.IsOfficialMarket(r.Spec) {
			state.rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			state.rec.Error = i18n.T(i18n.Default, "extension-market_security.cache")
			state.rec.FetchedAt = nil
		} else if len(r.TrustedKeys) > 0 {
			// A private market with keys and no verified catalog yet: nothing to install from until it is read.
			state.rec.SigningKey = ""
			state.rec.FetchedAt = nil
		} else {
			state.rec.SigningKey = ""
			state.rec.Signature = extension.MarketSignature{State: extension.SigUnsigned}
			state.rec.Error = ""
		}
		if err := st.PutExtensionMarket(ctx, state.rec); err != nil {
			return err
		}
		ms.byID[r.ID] = state
	}
	id, err := h.seedOfficialMarket(ctx)
	if err != nil {
		return err
	}
	ms.loaded = true
	if id != "" {
		ms.readLater(h, id)
	}
	for id, state := range ms.byID {
		if state.rec.FetchedAt == nil {
			ms.readLater(h, id)
		}
	}

	return nil
}

// officialMarketKey is the setting that says the official market was added (its spec): once only, so a market its
// owner removed is not added again; a new official spec is added once more.
const officialMarketKey = "extension_official_market"

// seedOfficialMarket adds the official market to a hub that has not had it yet (a new one, or one upgraded from before
// #2351), with its pinned key; a market of the same repository the owner already added counts as it (ms.mu held).
// It is the id of the one it added ("" none).
func (h *Manager) seedOfficialMarket(ctx context.Context) (string, error) {
	o := extension.Official
	if o.Spec == "" {
		return "", nil
	}
	var done struct {
		Spec string `json:"spec"`
	}
	found, err := h.store.Setting(ctx, officialMarketKey, &done)
	if err != nil {
		return "", err
	}
	ms, st := h.marketsState(), h.marketStore()
	rotated := ""
	for _, s := range ms.byID {
		if !extension.IsOfficialMarket(s.rec.Spec) || s.rec.SigningKey == "" || s.rec.SigningKey == o.SigningKey ||
			!slices.Contains(o.PreviousSigningKeys, s.rec.SigningKey) {
			continue
		}
		key, err := extension.ParseSigningKey(o.SigningKey)
		if err != nil {
			return "", err
		}
		if !found || done.Spec != o.Spec {
			registered := done
			registered.Spec = o.Spec
			if err := h.store.PutSetting(ctx, officialMarketKey, registered); err != nil {
				return "", err
			}
			done, found = registered, true
		}
		rec := s.rec
		rec.SigningKey = o.SigningKey
		rec.Signature = extension.MarketSignature{State: extension.SigKeyChanged, KeyID: extension.KeyID(key)}
		rec.Error = i18n.T(h.contextLang(ctx), "hub-extension_market.pin_rotated_pending")
		if err := st.PutExtensionMarket(ctx, rec); err != nil {
			return "", err
		}
		s.rec, s.index = rec, nil
		rotated = rec.ID
	}
	if rotated != "" {
		return rotated, nil
	}
	if found && done.Spec == o.Spec {
		return "", nil
	}
	mark := func() error { done.Spec = o.Spec; return h.store.PutSetting(ctx, officialMarketKey, done) }
	for _, s := range ms.byID {
		if extension.IsOfficialMarket(s.rec.Spec) {
			return "", mark()
		}
	}
	spec, err := extension.ParseMarket(o.Spec)
	if err != nil {
		return "", err
	}
	rec := extension.MarketRecord{ID: extension.MarketID(spec), Spec: spec.String(), SigningKey: o.SigningKey, AddedAt: time.Now(), Warnings: []string{},
		Signature: extension.MarketSignature{State: extension.SigInvalid}, Error: i18n.T(i18n.Default, "extension-market_security.cache")}
	if err := st.PutExtensionMarket(ctx, rec); err != nil {
		return "", err
	}
	ms.byID[rec.ID] = &marketState{rec: rec}
	if err := mark(); err != nil {
		return "", err
	}
	return rec.ID, nil
}

func (h *Manager) market(rec extension.MarketRecord) (*extension.Market, error) {
	spec, err := extension.ParseMarket(rec.Spec)
	if err != nil {
		return nil, err
	}
	client, err := h.marketClient()
	if err != nil {
		return nil, err
	}
	mk := &extension.Market{Spec: spec, HubBuild: h.build.Number, TrustedKey: rec.SigningKey, Client: client, GitHubAPI: h.extensions.markets.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if extension.IsOfficialMarket(rec.Spec) {
		mk.TrustedKey = extension.Official.SigningKey
	} else {
		mk.TrustedKeys = slices.Clone(rec.TrustedKeys)
	}
	if rec.TokenSealed != "" {
		if mk.Token, err = (hubSealer{h}).Open(rec.TokenSealed); err != nil {
			return nil, i18n.Err("hub-extension_market.token_unreadable")
		}
	}
	if err := h.configureMarket(mk); err != nil {
		return nil, err
	}
	return mk, nil
}

// marketRead reads a market's index now and keeps it (or why it could not). An index its signature refuses is not
// taken: the last good one stays, and the market says why (docs/extensions/extensions.md "Signing"). A catalog
// that could not be read at all (extension.ErrCatalogUnavailable: 404, 401, a timeout) changes no signature state:
// an official market keeps installing from its last verified index.
func (h *Manager) marketRead(ctx context.Context, id string) (MarketView, error) {
	ms := h.marketsState()
	ms.mu.Lock()
	s := ms.byID[id]
	var snapshot extension.MarketRecord
	if s != nil {
		snapshot = s.rec
	}
	ms.mu.Unlock()
	if s == nil {
		return MarketView{}, errNoMarket
	}
	mk, err := h.market(snapshot)
	var f *extension.Fetched
	if err == nil {
		f, err = mk.FetchSigned(ctx)
	}
	now := time.Now()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s = ms.byID[id]
	if s == nil {
		return MarketView{}, errNoMarket
	}
	rec := s.rec
	if !sameMarketTrust(rec, snapshot) {
		return h.marketView(s), i18n.Err("extension-market_security.changed")
	}
	nextIndex := s.index
	if err != nil {
		rec.FetchedAt = &now
		switch {
		case errors.Is(err, extension.ErrCatalogUnverified) && requiresSignature(rec):
			// A catalog was read and does not verify: quarantined, the last good index unusable too.
			rec.Signature, rec.Error = extension.MarketSignature{State: extension.SigInvalid}, err.Error()
			err = fmt.Errorf("%w: %w", errMarketSignature, err)
		case rec.Signature.Bad():
			// Not read (or not reached): a quarantine stands, and keeps saying why.
		default:
			// Not read: nothing was learned, so the last verified index and its state stay.
			rec.Error = err.Error()
		}
	} else {
		var ix *extension.MarketIndex
		if rec, ix, err = signedIndex(rec, f, now, h.build.Number); ix != nil {
			nextIndex = ix
		} else {
			h.logf("extension market %s: %v", id, err)
		}
	}
	if perr := h.marketStore().PutExtensionMarket(ctx, rec); perr != nil {
		// Keep a failed verification quarantined in memory, but let writes retry.
		if rec.Signature.Bad() {
			s.rec = rec
		}
		return h.marketView(s), perr
	}
	s.rec, s.index = rec, nextIndex
	return h.marketView(s), err
}

func keyIDOf(key string) string {
	k, err := extension.ParseSigningKey(key)
	if err != nil {
		return "无"
	}
	return extension.KeyID(k)
}

// signatureText is a market's signature for the log.
func signatureText(s extension.MarketSignature) string {
	if s.State == extension.SigSigned {
		return "signed by " + s.KeyID
	}
	return s.State
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

var errNoMarket = i18n.Err("hub-extension_market.no_market")

// marketsDir is where git markets' clones are kept.
func (h *Manager) marketsDir() string { return filepath.Join(h.extensions.root, "markets") }

// packageCache is where market packages are kept by their sha256, so installing one again does not download it again.
func (h *Manager) packageCache() string { return filepath.Join(h.extensions.root, "cache", "packages") }

// marketClient is how the hub reads markets. extension_market.proxy sends all of them through its HTTP or SOCKS5
// proxy. Without one, GitHub's hosts use github.proxy when set (#3277), and everything else follows HTTPS_PROXY.
// extension_market.ca_file appends private roots to this client's system pool, never to another client. Tests set
// their own client. An invalid configured CA file fails instead of silently using the system-only pool.
func (h *Manager) marketClient() (*http.Client, error) {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.client != nil {
		return ms.client, nil
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	if h.cfg != nil && h.cfg.ExtensionMarket.CAFile != "" {
		caFile := h.cfg.ExtensionMarket.CAFile
		info, err := os.Stat(caFile)
		if err != nil {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %w", caFile, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: not a regular file", caFile)
		}
		if info.Size() > marketCAFileMost {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %d bytes exceeds %d-byte limit", caFile, info.Size(), marketCAFileMost)
		}
		f, err := os.Open(caFile)
		if err != nil {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %w", caFile, err)
		}
		pemBytes, readErr := io.ReadAll(io.LimitReader(f, marketCAFileMost+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %w", caFile, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %w", caFile, closeErr)
		}
		if len(pemBytes) > marketCAFileMost {
			return nil, fmt.Errorf("extension_market.ca_file: read %q: %d bytes exceeds %d-byte limit", caFile, len(pemBytes), marketCAFileMost)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("extension_market.ca_file: load system roots: %w", err)
		}
		if roots == nil {
			return nil, errors.New("extension_market.ca_file: load system roots: no system certificate pool")
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("extension_market.ca_file: parse %q: no PEM certificates found", caFile)
		}
		tlsConfig := &tls.Config{}
		if t.TLSClientConfig != nil {
			tlsConfig = t.TLSClientConfig.Clone()
		}
		tlsConfig.RootCAs = roots
		t.TLSClientConfig = tlsConfig
	}
	var upstream *url.URL
	var upstreamErr error
	if h.cfg != nil {
		upstream, upstreamErr = netproxy.ParseHTTPOrSOCKS5(h.cfg.ExtensionMarket.Proxy)
	}
	switch {
	case upstreamErr != nil:
		// An explicit but invalid proxy must never turn into a direct connection.
		t.Proxy = func(*http.Request) (*url.URL, error) { return nil, upstreamErr }
	case upstream != nil:
		t.Proxy = http.ProxyURL(upstream)
	default:
		var gh *url.URL
		if h.cfg != nil && h.cfg.GitHub != nil {
			gh, _ = netproxy.Parse(h.cfg.GitHub.Proxy) // a malformed one: as the environment says (the connector says it is wrong)
		}
		if gh != nil {
			t.Proxy = func(r *http.Request) (*url.URL, error) {
				if githubHost(r.URL.Hostname()) {
					return gh, nil
				}
				return envProxy(r)
			}
		}
	}
	ms.client = &http.Client{Transport: t}
	return ms.client, nil
}

// envProxy is where marketClient sends hosts other than GitHub's: the environment's proxy. A variable so tests do
// not depend on the proxy of the machine running them (http.ProxyFromEnvironment reads it once per process).
var envProxy = http.ProxyFromEnvironment

// githubHost says whether host is GitHub's: its site, API and where release assets and raw files are served from.
func githubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "ghcr.io" || host == "pkg-containers.githubusercontent.com" || host == "github.com" || strings.HasSuffix(host, ".github.com") || host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

// official is the registered official market (ms.mu held).
func (ms *markets) official() *marketState {
	for _, s := range ms.byID {
		if extension.IsOfficialMarket(s.rec.Spec) {
			return s
		}
	}
	return nil
}

// withSpec is the market registered at spec (ms.mu held).
func (ms *markets) withSpec(spec string) *marketState {
	for _, s := range ms.byID {
		if s.rec.Spec == spec {
			return s
		}
	}
	return nil
}

// moveTargetTaken checks both an exact address and the single official identity (ms.mu held).
func (ms *markets) moveTargetTaken(id, spec string) bool {
	for otherID, s := range ms.byID {
		if otherID != id && (s.rec.Spec == spec || extension.IsOfficialMarket(spec) && extension.IsOfficialMarket(s.rec.Spec)) {
			return true
		}
	}
	return false
}

// refreshStale reads again, in the background, the indexes older than marketStale (one read per market at a time).
func (h *Manager) refreshStale() {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for id, s := range ms.byID {
		if s.rec.FetchedAt == nil || time.Since(*s.rec.FetchedAt) >= marketStale {
			ms.readLater(h, id)
		}
	}
}

// selfRefreshLater admits one extension-initiated read of id per marketSelfRefreshWindow. It returns the remaining
// window for a call to refuse, or zero after starting (or joining) the existing deduplicated background read. ok is
// false when id is not registered. Admission and scheduling share the market lock, so concurrent callers cannot pass
// together and a 429 never moves the window.
func (h *Manager) selfRefreshLater(id string) (retry time.Duration, ok bool) {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.byID[id]
	if s == nil {
		return 0, false
	}
	now := time.Now()
	if ms.now != nil {
		now = ms.now()
	}
	if !s.selfRefreshAt.IsZero() {
		elapsed := max(now.Sub(s.selfRefreshAt), time.Duration(0))
		if elapsed < marketSelfRefreshWindow {
			return marketSelfRefreshWindow - elapsed, true
		}
	}
	s.selfRefreshAt = now
	ms.readLater(h, id)
	return 0, true
}

// readLater reads market id again in the background, unless it is being read already (ms.mu held).
func (ms *markets) readLater(h *Manager, id string) {
	if ms.reading[id] != nil {
		return
	}
	if ms.reading == nil {
		ms.reading = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	ms.reading[id] = done
	go func() {
		defer func() { ms.mu.Lock(); delete(ms.reading, id); ms.mu.Unlock(); close(done) }()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if _, err := h.marketRead(ctx, id); err != nil {
			h.logf("plugin market %s: %v", id, err)
		}
	}()
}

// firstReadWait bounds how long a request waits for a market's first read (awaitFirstReads).
var firstReadWait = 10 * time.Second

// awaitFirstReads waits, at most firstReadWait or until ctx ends, for the reads in flight of markets this hub has
// never read (#5626). A new hub adds the official market on the first request and reads it in the background; without
// the wait that request (a newcomer's first look at the catalog) gets the "not verified yet" placeholder as if the
// market could not be read. A market read before, even one that failed, is answered as it stands.
func (h *Manager) awaitFirstReads(ctx context.Context) {
	ms := h.marketsState()
	if ms == nil {
		return
	}
	ms.mu.Lock()
	var waits []chan struct{}
	for id, s := range ms.byID {
		if done := ms.reading[id]; done != nil && s.rec.FetchedAt == nil {
			waits = append(waits, done)
		}
	}
	ms.mu.Unlock()
	if len(waits) == 0 {
		return
	}
	limit := time.NewTimer(firstReadWait)
	defer limit.Stop()
	for _, done := range waits {
		select {
		case <-done:
		case <-limit.C:
			return
		case <-ctx.Done():
			return
		}
	}
}
