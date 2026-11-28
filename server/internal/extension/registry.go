package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Where a plugin comes from.
const (
	SourceBuiltin   = "builtin"   // compiled into personad; a row keeps its switch and settings
	SourceHubJSON   = "hub.json"  // hub.json's plugins[]: read-only here, never in the database
	SourceInstalled = "installed" // a package installed on the hub's machine, or a remote plugin
)

// SecretMost is the largest secret value kept.
const SecretMost = 16 << 10

// Record is a plugin's row (table plugins): what the user installed and chose.
type Record struct {
	ID         string   `json:"id"`
	StorageID  string   `json:"storage_id,omitempty"`
	PreviousID string   `json:"previous_id,omitempty"`
	Aliases    []string `json:"aliases,omitempty"`
	// Transaction-only compare-and-swap guards.
	ExpectedSource    string          `json:"-"`
	ExpectedUpdatedAt time.Time       `json:"-"`
	ExpectedID        string          `json:"-"`
	ExpectedVersion   string          `json:"-"`
	ExpectedConfig    json.RawMessage `json:"-"`
	// BuiltinPredecessor marks state handed to a same-ID official package by the host.
	BuiltinPredecessor bool `json:"builtin_predecessor,omitempty"`
	// DeleteSecrets removes these sealed rows in the same transaction as the record write.
	DeleteSecrets   []string `json:"-"`
	NewInstallation bool     `json:"-"`
	Source          string   `json:"source"`
	Origin          string   `json:"origin,omitempty"`
	// MarketKey is the verified market key (official, or a private market's owner key) captured from this installed package's
	// catalog entry, never inferred from the market's current mutable identity.
	MarketKey       string `json:"market_key,omitempty"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	// PreviousOrigin and PreviousMarketKey are PreviousVersion's own Origin and MarketKey, kept by the upgrade that
	// replaced it so a rollback restores that version's provenance, never the newer one's. An empty PreviousOrigin
	// (a row upgraded before they were kept) is no snapshot: a rollback then drops the verified key.
	PreviousOrigin    string          `json:"previous_origin,omitempty"`
	PreviousMarketKey string          `json:"previous_market_key,omitempty"`
	Manifest          json.RawMessage `json:"manifest"`
	Granted           Permissions     `json:"granted"`
	GrantedDigest     string          `json:"granted_digest,omitempty"`
	Enabled           bool            `json:"enabled"`
	Config            json.RawMessage `json:"config,omitempty"`
	Remote            *Remote         `json:"remote,omitempty"`
	// DisabledContributions are the contributions the user switched off ("<point>/<id>", docs/extensions/extensions.md "Contribution switches"):
	// they do not take effect while the rest of the extension runs. A connector's switch is the connector's own.
	DisabledContributions []string `json:"disabled_contributions,omitempty"`
	// PackOff, on an extension pack, are the members switching the pack off switched off (docs/extensions/extensions.md
	// "Dependencies, exports and packs", #1830): switching the pack back on turns these on again and no others.
	PackOff     []string  `json:"pack_off,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Remote is a plugin that runs elsewhere and is reached on these sockets.
type Remote struct {
	RPCSocket  string `json:"rpc_socket,omitempty"`
	HTTPSocket string `json:"http_socket,omitempty"`
	// Package: it is the installed package's program, attached (Attach); the package stays. Without it, the extension
	// was installed from remote: and has no package.
	Package bool `json:"package,omitempty"`
}

// AttachedPackage: an installed package whose program runs on its own and is reached on Remote's sockets (Attach). Its
// package stays installed and still takes effect (skills, instructions, gates); Detach runs it from there again.
func (r Record) AttachedPackage() bool {
	return r.Source == SourceInstalled && r.Remote != nil && r.Remote.Package
}

// Store keeps the registry (the hub's Postgres: tables plugins, plugin_secrets, plugin_audit).
type Store interface {
	Extensions(ctx context.Context) ([]Record, error)
	PutExtension(ctx context.Context, r Record) error
	// DeleteExtension deletes the row and its secrets.
	DeleteExtension(ctx context.Context, id string) error
	// ExtensionSecrets are the sealed secrets by name.
	ExtensionSecrets(ctx context.Context, id string) (map[string]string, error)
	PutExtensionSecret(ctx context.Context, id, name, sealed string) error
	DeleteExtensionSecret(ctx context.Context, id, name string) error
	AddExtensionAudit(ctx context.Context, id, action, actor string, detail json.RawMessage) error
}

// Sealer seals secrets with the manager's secret.key (the same as mail passwords and launchers' keys).
type Sealer interface {
	Seal(plain string) (string, error)
	Open(sealed string) (string, error)
}

// Extension is one registered extension.
type Extension struct {
	Record                Record
	builtinDefaultEnabled bool // the compiled default to restore after explicit uninstall
	// Manifest is what takes effect: the manifest without the contributions the user switched off (Without). Full is
	// the manifest as installed: what the extension may do (permissions, the ids it holds) and every contribution the
	// user can switch.
	Manifest *Manifest
	Full     *Manifest
	Schema   *Schema
	// Dir is the package's directory ("" for one installed from remote: or a hub.json plugin).
	Dir string
	// L10n are the package's translations of its manifest's text (l10n.go), read with it; never changed after.
	L10n Translations
	// Problem is why it cannot run on this hub (engines, a missing package); it is then not started.
	Problem  string
	Warnings []string
	// problemErr retains the message for responses in the request's language; Problem stays the log/CLI text.
	problemErr error
	// Legacy is a hub.json plugin's entry, as written (its command is a path on the machine, not in a package).
	Legacy any
}

// ReadOnly: hub.json's, changed there only.
func (p *Extension) ReadOnly() bool {
	return p.Record.Source == SourceHubJSON || errors.Is(p.problemErr, ErrIdentity)
}

// ProblemText is the compatibility problem in the client's language, when a translation is available.
func (p *Extension) ProblemText(l i18n.Lang) string {
	if p.problemErr != nil && p.problemErr.Error() == p.Problem {
		return i18n.ExtensionStatusText(l, i18n.Localize(p.problemErr, l))
	}
	return i18n.ExtensionStatusText(l, p.Problem)
}

// Change is what the registry tells the hub after it changed a plugin.
type Change struct {
	ID   string
	Kind string // installed | upgraded | rolled_back | removed | enabled | disabled | config | secret | attached | detached
	// Old is the plugin before the change (removed, upgraded, rolled back).
	Old *Extension
}

// Registry is every plugin the hub knows: built-in, from hub.json and installed.
type Registry struct {
	dir      string // <data dir>/plugins
	store    Store  // nil: nothing is kept (no database), nothing installs
	seal     Sealer
	build    int64
	platform string

	// OnChange is called after each change, outside the registry's lock.
	OnChange func(Change)
	// Logf is the hub's log (what Load did about an interrupted uninstall); nil: nothing is logged.
	Logf func(format string, args ...any)
	// RemoteManifest asks a remote plugin for its manifest (the hub's host; initialize, or GET /manifest).
	RemoteManifest func(ctx context.Context, r Remote) (*Manifest, error)
	// CanEnable says why a plugin (its source and manifest) may not be on now, nil when it may: asked before it is
	// switched on, installed (on at once) or upgraded while on. A rollback is not asked (it goes back to a version
	// that ran; the hub rolls back a broken upgrade itself).
	CanEnable func(ctx context.Context, source string, m *Manifest) error
	// PlanCandidates gives the main preview the joint upgrade graph; PlanGuard keeps market trust unchanged while committing.
	PlanCandidates func(context.Context, *Manifest, string) []*Manifest
	PlanGuard      func(context.Context, []*Preview) (func(), error)
	// BoundSecrets names the secrets of p that hold only for its configuration old, so changing it to next deletes
	// them (the built-in voice extension's key goes with its service's address, docs/extensions/extensions.md "Built-in voice extension").
	// Both are the configurations with the schema's defaults. SetConfig deletes them before it saves next, in the same
	// operation SetSecret runs in, so a secret never pairs with a configuration it was not set for. Nil: none.
	BoundSecrets func(p *Extension, old, next json.RawMessage) []string
	// SecretNeeds says why secret name of p may not be set under its configuration config (with the defaults), nil
	// when it may: SetSecret asks it in the same operation SetConfig runs in. Nil: any may.
	SecretNeeds func(p *Extension, name string, config json.RawMessage) error
	// SettingsCheck asks an extension that declares settings_check whether its settings and secrets as they would be
	// after a change work (#5906): an error saves nothing. Called outside the registry's operation, with the values in
	// plain text, never kept.
	SettingsCheck func(ctx context.Context, p *Extension, config json.RawMessage, secrets map[string]string) error

	operationMu sync.Mutex // serializes package/record changes, including plan recovery and cleanup
	planProblem error      // while a commit confirmation or package recovery is unresolved, no new writes; wait for a reload
	mu          sync.Mutex
	extensions  map[string]*Extension
	// settingsEpoch is odd while a mutation is active and even after its last overlapping mutation finishes.
	settingsEpoch  map[string]uint64
	settingsActive map[string]int
	reserved       map[string]map[string]string // point → contribution id → who has it (the hub's own)
	previews       map[string]*Preview
	now            func() time.Time
	// yields: built-in plugins an installed one of the same id takes the place of (Yields); yielded: those put aside.
	handoffs map[string]bool
	yields   map[string]bool
	yielded  map[string]*Extension
	// devSecrets are the secrets of the extensions being written (SourceDev), in memory only.
	devSecrets map[string]map[string]string
	// refSchemes: the schemes each extension's secrets reference, by id (secret_refs.go), noted at each read.
	refSchemes map[string][]string
	// keptOff are built-in extensions held off whatever their rows say (KeepOff).
	keptOff map[string]*keptOff
	// moduleHolds: the ids of modules an installed extension may supply (HoldModule), each with whether the modules
	// setting switched it off. dormant: the rows of those ids Load set aside (dormant.go).
	moduleHolds map[string]bool
	dormant     map[string]Record
}

// keptOff is a built-in held off (KeepOff): what switching it on answers, and its row's own switch. The hold is not
// the owner's choice, so it never reaches the database: a row written meanwhile keeps the switch it had (put).
type keptOff struct {
	why error
	row bool // the row's switch; the default AddBuiltin was given while there is no row
}

// NewRegistry is a registry for the hub of build, keeping packages under dir.
func NewRegistry(dir string, store Store, seal Sealer, build int64) *Registry {
	return &Registry{dir: dir, store: store, seal: seal, build: build, platform: runtime.GOOS + "-" + runtime.GOARCH,
		extensions: map[string]*Extension{}, reserved: map[string]map[string]string{}, previews: map[string]*Preview{}, now: time.Now,
		handoffs: map[string]bool{}, yields: map[string]bool{}, yielded: map[string]*Extension{},
		moduleHolds: map[string]bool{}, dormant: map[string]Record{}}
}

// Yields marks the built-in plugin id as one an installed plugin of the same id takes the place of, while it moves out
// of personad into a package (Telegram, docs/plans/plan-extensions.md): installing that plugin puts the built-in aside (the hub
// stops its program before the installed one starts), uninstalling it brings the built-in back.
func (r *Registry) Yields(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.yields[id] = true
}

// yielding says whether cur is a built-in an installed plugin may take the place of. Called with r.mu held.
func (r *Registry) yielding(cur *Extension) bool {
	return cur != nil && cur.Record.Source == SourceBuiltin && r.yields[cur.Record.ID]
}

// Dirs.
func (r *Registry) installedDir(id, version string) string {
	return filepath.Join(r.dir, "installed", id, version)
}

// DataDir is a plugin's own directory for its caches and data (deleted with it).
func (r *Registry) DataDir(id string) string { return filepath.Join(r.dir, "data", r.storageKey(id)) }

// HasStore says whether installs are possible.
func (r *Registry) HasStore() bool { return r.store != nil }

// Reserve marks contribution ids the hub has itself (its built-in connectors), so no plugin takes them.
func (r *Registry) Reserve(point string, ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reserved[point] == nil {
		r.reserved[point] = map[string]string{}
	}
	for _, id := range ids {
		r.reserved[point][id] = "the hub"
	}
}

// AddHubJSON registers a hub.json plugin: its manifest made by the hub from the entry (legacy is the entry).
func (r *Registry) AddHubJSON(m *Manifest, legacy any, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.extensions[m.ID] != nil {
		return fmt.Errorf("plugin %q: duplicate id", m.ID)
	}
	raw, _ := json.Marshal(m)
	mutation := r.settingsWriteLocked(m.ID)
	r.extensions[m.ID] = &Extension{Record: Record{ID: m.ID, Source: SourceHubJSON, Version: m.Version, Manifest: raw,
		Granted: m.Permissions, GrantedDigest: Digest(m), Enabled: enabled}, Manifest: m, Full: m, Legacy: legacy}
	mutation.finishLocked()
	return nil
}

// AddBuiltin registers a plugin compiled into personad (its program at dir, if any). Its switch and settings come
// from its row once Load reads it; enabled is the default.
func (r *Registry) AddBuiltin(m *Manifest, dir string, enabled bool) error {
	schema, err := ParseSchema(m.Configuration)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.extensions[m.ID] != nil {
		return fmt.Errorf("plugin %q: duplicate id", m.ID)
	}
	raw, _ := json.Marshal(m)
	mutation := r.settingsWriteLocked(m.ID)
	r.extensions[m.ID] = &Extension{Record: Record{ID: m.ID, Source: SourceBuiltin, Version: m.Version, Manifest: raw,
		Granted: m.Permissions, GrantedDigest: Digest(m), Enabled: enabled}, Manifest: m, Full: m, Schema: schema, Dir: dir, builtinDefaultEnabled: enabled}
	mutation.finishLocked()
	return nil
}

// AddBuiltinTranslated is AddBuiltin for a manifest written in English, with its text in the other languages (a
// built-in has no package to read l10n/ from).
func (r *Registry) AddBuiltinTranslated(m *Manifest, dir string, enabled bool, ts Translations) error {
	if err := r.AddBuiltin(m, dir, enabled); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extensions[m.ID].L10n = ts
	return nil
}

// KeepOff holds the built-in extension id (registered, before Load) switched off whatever its row says; switching it
// on answers why. For a built-in that took over a hub.json entry written disabled (docs/extensions/extensions.md "Built-in voice extension"):
// hub.json has the last word on it, its settings and secrets stay editable, and its row keeps its own switch for when
// hub.json lets it run.
func (r *Registry) KeepOff(id string, why error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	if p == nil || p.Record.Source != SourceBuiltin {
		return
	}
	if r.keptOff == nil {
		r.keptOff = map[string]*keptOff{}
	}
	r.keptOff[id] = &keptOff{why: why, row: p.Record.Enabled}
	p.Record.Enabled = false
}

// Load reads the rows: installed plugins, and the switches and settings of built-in ones. A row whose package is gone
// or that this hub cannot run is kept and listed with its problem.
func (r *Registry) Load(ctx context.Context) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	if r.store == nil {
		return nil
	}
	rows, err := r.store.Extensions(ctx)
	if err != nil {
		return err
	}
	r.recoverUninstalls(rows) // before the rows are read: a package put back is found where its row says
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropStalePreviews()
	clear(r.dormant)
	for _, row := range rows {
		cur := r.extensions[row.ID]
		if r.holdsDormant(row, cur) {
			r.dormant[row.ID] = row
			continue
		}
		switch {
		case row.Source == SourceBuiltin && cur != nil && cur.Record.Source == SourceBuiltin:
			mutation := r.settingsWriteLocked(row.ID)
			cur.Record.Enabled, cur.Record.Config = row.Enabled, row.Config
			if k := r.keptOff[row.ID]; k != nil {
				k.row, cur.Record.Enabled = row.Enabled, false
			}
			cur.Record.PackOff = row.PackOff
			cur.Record.DisabledContributions = row.DisabledContributions
			cur.Manifest = cur.Full.Without(row.DisabledContributions)
			cur.Record.InstalledAt, cur.Record.UpdatedAt = row.InstalledAt, row.UpdatedAt
			mutation.finishLocked()
		case row.Source == SourceInstalled && cur != nil && cur.Record.Source == SourceBuiltin && r.handoffs[row.ID]:
			// A pre-existing, unrelated install keeps its own slot. It has no
			// authority to restore or expose a built-in under its identity.
			next := r.fromRow(row)
			r.yielded[row.ID] = cur // uninstalling a conflicting install also restores the compiled default
			if !row.VerifiedBuiltinPredecessor() || r.keptOff[row.ID] != nil {
				next.Problem = ErrIdentity.Error()
				next.problemErr = ErrIdentity
				next.Record.Enabled = false
			}
			r.extensions[row.ID] = next
		case row.Source == SourceInstalled && cur == nil:
			mutation := r.settingsWriteLocked(row.ID)
			r.extensions[row.ID] = r.fromRow(row)
			mutation.finishLocked()
		case row.Source == SourceInstalled && r.yielding(cur):
			mutation := r.settingsWriteLocked(row.ID)
			r.yielded[row.ID] = cur
			r.extensions[row.ID] = r.fromRow(row)
			mutation.finishLocked()
		}
	}
	return nil
}

// fromRow is an installed plugin as its row says.
func (r *Registry) fromRow(row Record) *Extension {
	row.ExpectedID, row.ExpectedVersion, row.NewInstallation = "", "", false
	row.ExpectedSource, row.ExpectedUpdatedAt = "", time.Time{}
	row.ExpectedConfig, row.DeleteSecrets = nil, nil
	p := &Extension{Record: row}
	m, err := Parse(row.Manifest)
	if err != nil {
		var loose Manifest
		_ = json.Unmarshal(row.Manifest, &loose)
		loose.ID = row.ID
		p.Manifest, p.Full = &loose, &loose
		p.problemErr = i18n.Err("extension-status.manifest_unreadable", i18n.Args{"err": err.Error()})
		p.Problem = p.problemErr.Error()
		return p
	}
	p.Full, p.Manifest = m, m.Without(row.DisabledContributions)
	p.Schema, _ = ParseSchema(m.Configuration)
	if err := checkEntrySecretNamespace(m, p.Schema, row.Config); err != nil {
		p.problemErr = err
		p.Problem = err.Error()
	}
	if row.Remote == nil || row.AttachedPackage() {
		p.Dir = r.installedDir(row.StorageKey(), row.Version)
		if !hasManifest(p.Dir) {
			p.problemErr = i18n.Err("extension-status.package_missing", i18n.Args{"path": p.Dir})
			p.Problem = p.problemErr.Error()
		} else {
			var warnings []string
			p.L10n, warnings = ReadTranslations(p.Dir)
			p.Warnings = append(p.Warnings, warnings...)
		}
	}
	if w, err := CheckEngines(m, r.build); err != nil {
		p.Problem = err.Error()
		p.problemErr = err
	} else if w != "" {
		p.Warnings = append(p.Warnings, w)
	}
	p.Warnings = append(p.Warnings, m.Lint()...)
	return p
}

// List is every plugin, by id.
func (r *Registry) List() []*Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Extension, 0, len(r.extensions))
	for _, p := range r.extensions {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Record.ID < out[j].Record.ID })
	return out
}

// Snapshots is every plugin, by id, as stable snapshots. Manifests and schemas are immutable and shared;
// mutable records and their slices are copied under the registry lock.
func (r *Registry) Snapshots() []*Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Extension, 0, len(r.extensions))
	for _, p := range r.extensions {
		out = append(out, snapshotExtension(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Record.ID < out[j].Record.ID })
	return out
}

// Get is the plugin with id, nil when there is none.
func (r *Registry) Get(id string) *Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.extensions[r.canonicalID(id)]
}

// Snapshot is an extension's stable state for one process specification. Parsed manifests, schemas and legacy
// configuration are immutable and shared; the mutable record and its slices are copied under the registry lock.
func (r *Registry) Snapshot(id string) *Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	return snapshotExtension(r.extensions[r.canonicalID(id)])
}

func snapshotExtension(p *Extension) *Extension {
	if p == nil {
		return nil
	}
	out := *p
	out.Record.Manifest = slices.Clone(p.Record.Manifest)
	out.Record.Config = slices.Clone(p.Record.Config)
	out.Record.ExpectedConfig = slices.Clone(p.Record.ExpectedConfig)
	out.Record.DeleteSecrets = slices.Clone(p.Record.DeleteSecrets)
	out.Record.Aliases = slices.Clone(p.Record.Aliases)
	out.Record.Granted.Network = slices.Clone(p.Record.Granted.Network)
	out.Record.Granted.Hub = slices.Clone(p.Record.Granted.Hub)
	if p.Record.Granted.Storage != nil {
		storage := *p.Record.Granted.Storage
		storage.Keys = slices.Clone(storage.Keys)
		storage.Patterns = slices.Clone(storage.Patterns)
		out.Record.Granted.Storage = &storage
	}
	out.Record.DisabledContributions = slices.Clone(p.Record.DisabledContributions)
	out.Record.PackOff = slices.Clone(p.Record.PackOff)
	if p.Record.Remote != nil {
		remote := *p.Record.Remote
		out.Record.Remote = &remote
	}
	out.Warnings = slices.Clone(p.Warnings)
	return &out
}

// HubScopeAllowed checks the live permission record while holding the registry lock. Callers must not read the
// pointer returned by Get to authorize a request: SetEnabled can change its record concurrently. The legacy full-hub
// grant covers every scope but the ones only granted by name (InEveryScope).
func (r *Registry) HubScopeAllowed(id, scope string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	return p != nil && p.Record.Enabled && p.Problem == "" &&
		((p.Record.Granted.FullHub() && InEveryScope(scope)) || slices.Contains(p.Record.Granted.Hub, scope))
}

// HubScopeGranted is HubScopeAllowed without the legacy full-hub grant: the user granted scope itself. For what
// did not exist under that grant (models: a purpose of its own in Settings › Models).
func (r *Registry) HubScopeGranted(id, scope string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	return p != nil && p.Record.Enabled && p.Problem == "" && slices.Contains(p.Record.Granted.Hub, scope)
}

// SendsAsUser says whether extension id, enabled and compatible, has a connector contribution with this id that
// declares sends_as_user (ConnectorItem.SendsAsUser). It reads the live record under the registry lock, like
// HubScopeAllowed, and the manifest as installed (Full: what the user agreed to), not the one without the contributions
// switched off: a connector's switch is the connector's own (SwitchedElsewhere, Settings › Connectors), which neither
// takes it out of the manifest nor stops a send that already happened from being recorded.
func (r *Registry) SendsAsUser(id, contribution string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	if p == nil || !p.Record.Enabled || p.Problem != "" || p.Full == nil {
		return false
	}
	return slices.ContainsFunc(p.Full.SendsAsUser(), func(c ConnectorItem) bool { return c.ID == contribution })
}

// EventSubscribers snapshots the enabled, compatible extensions subscribed to topic. A nonempty scope additionally
// requires that actual grant (or the legacy full-hub grant, where InEveryScope); an empty scope preserves the other
// event topics' rules.
func (r *Registry) EventSubscribers(topic, scope string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := []string{}
	for id, p := range r.extensions {
		if !p.Record.Enabled || p.Problem != "" || p.Manifest == nil {
			continue
		}
		if scope != "" && !(p.Record.Granted.FullHub() && InEveryScope(scope)) && !slices.Contains(p.Record.Granted.Hub, scope) {
			continue
		}
		if slices.Contains(p.Manifest.Topics(), topic) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// ErrorReportTaker is an extension given error reports: its error_reports contributions that take source.
type ErrorReportTaker struct {
	Extension     string
	Contributions []string
}

// ErrorReportTakers snapshots the enabled, compatible extensions granted error_reports whose contributions (the ones
// switched on) take reports from source; source "" asks whether any takes some.
func (r *Registry) ErrorReportTakers(source string) []ErrorReportTaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ErrorReportTaker
	for id, p := range r.extensions {
		if !p.Record.Enabled || p.Problem != "" || p.Manifest == nil || !p.Record.Granted.ErrorReports {
			continue
		}
		t := ErrorReportTaker{Extension: id}
		for _, it := range p.Manifest.ErrorReports() {
			if source == "" || len(it.Sources) == 0 || slices.Contains(it.Sources, source) {
				t.Contributions = append(t.Contributions, it.ID)
			}
		}
		if len(t.Contributions) > 0 {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Extension < out[j].Extension })
	return out
}

func (r *Registry) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// put keeps p's row (a built-in one gets its first row here) and says its revision, as write does. A built-in held
// off (KeepOff) is written with its row's own switch, not the hold.
func (r *Registry) put(ctx context.Context, row Record) (time.Time, error) {
	r.mu.Lock()
	if k := r.keptOff[row.ID]; k != nil {
		row.Enabled = k.row
	}
	r.mu.Unlock()
	return r.write(ctx, row)
}

// write keeps row as it is, at the revision after its own (revisionAfter), and says that revision: the caller puts
// it in the record with the change, since storage binds an installation by it (#16920).
func (r *Registry) write(ctx context.Context, row Record) (time.Time, error) {
	if r.store == nil {
		return row.UpdatedAt, nil // no database: the choice lasts until the hub restarts
	}
	if row.InstalledAt.IsZero() {
		row.InstalledAt = r.now()
	}
	row.UpdatedAt = r.revisionAfter(row.UpdatedAt)
	if row.Source == SourceInstalled {
		row.ExpectedID, row.ExpectedVersion = row.ID, row.Version
	}
	row.ExpectedSource = row.Source
	var err error
	if len(row.DeleteSecrets) > 0 {
		store, ok := r.store.(PlanStore)
		if !ok {
			return time.Time{}, fmt.Errorf("the store cannot delete secrets with the configuration")
		}
		err = store.PutExtensionPlan(ctx, []Record{row})
	} else {
		err = r.store.PutExtension(ctx, row)
	}
	var unknown *CommitUnknownError
	if errors.As(err, &unknown) {
		r.planProblem = err
	}
	if err == nil && row.Source == SourceBuiltin {
		r.mu.Lock()
		if current := r.extensions[row.ID]; current != nil && current.Record.Source == SourceBuiltin && current.Record.InstalledAt.IsZero() {
			current.Record.InstalledAt = row.InstalledAt
		}
		r.mu.Unlock()
	}
	if err != nil {
		return time.Time{}, err
	}
	return row.UpdatedAt, nil
}

// revisionAfter is the revision a write stores over a row at revision current. The database keeps the later of the
// one it is given and one microsecond past its own (#16754), at microsecond precision. Given the later of now and
// one microsecond past current, at that precision, it keeps exactly this one, so the record still matches its row.
func (r *Registry) revisionAfter(current time.Time) time.Time {
	next := r.now().Truncate(time.Microsecond)
	if floor := current.Truncate(time.Microsecond).Add(time.Microsecond); next.Before(floor) {
		return floor
	}
	return next
}

// Audit records what the hub changed about extension id outside the registry (its sandbox).
func (r *Registry) Audit(ctx context.Context, id, action, actor string, detail any) {
	r.audit(ctx, id, action, actor, detail)
}

func (r *Registry) audit(ctx context.Context, id, action, actor string, detail any) {
	if r.store == nil {
		return
	}
	b, _ := json.Marshal(detail)
	if detail == nil {
		b = []byte(`{}`)
	}
	_ = r.store.AddExtensionAudit(ctx, id, action, actor, b)
}

// settingsMutation fences one visible registry mutation. Its completion closes the seqlock before OnChange runs.
type settingsMutation struct {
	registry *Registry
	id       string
	once     sync.Once
}

func (r *Registry) settingsWrite(id string) *settingsMutation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settingsWriteLocked(id)
}

// settingsWriteLocked starts before the first store write or in-memory change. Overlapping operations after their
// operation lock is released stay in one odd epoch until the last one finishes.
func (r *Registry) settingsWriteLocked(id string) *settingsMutation {
	if r.settingsEpoch == nil {
		r.settingsEpoch = map[string]uint64{}
	}
	if r.settingsActive == nil {
		r.settingsActive = map[string]int{}
	}
	if r.settingsActive[id] == 0 {
		r.settingsEpoch[id]++
	}
	r.settingsActive[id]++
	return &settingsMutation{registry: r, id: id}
}

func (m *settingsMutation) finishLocked() {
	if m == nil {
		return
	}
	m.once.Do(func() {
		r := m.registry
		r.settingsActive[m.id]--
		if r.settingsActive[m.id] == 0 {
			r.settingsEpoch[m.id]++
		}
	})
}

func (m *settingsMutation) finish() {
	if m == nil {
		return
	}
	m.registry.mu.Lock()
	m.finishLocked()
	m.registry.mu.Unlock()
}

func (m *settingsMutation) changed(c Change) {
	m.finish()
	if m.registry.OnChange != nil {
		m.registry.OnChange(c)
	}
}
