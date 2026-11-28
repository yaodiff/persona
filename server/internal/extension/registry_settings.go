package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// editable is a plugin the user may switch and configure (not hub.json's).
func (r *Registry) editable(id string) (*Extension, error) {
	p := r.Get(id)
	if p == nil {
		return nil, i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if p.ReadOnly() {
		return nil, i18n.Err("extension-status.read_only", i18n.Args{"id": id})
	}
	if p.Record.Source == SourceDev {
		return nil, i18n.Err("extension-status.dev_read_only", i18n.Args{"id": id})
	}
	return p, nil
}

// editableSnapshot is editable's stable record/configuration snapshot for preparation outside the operation lock.
func (r *Registry) editableSnapshot(id string) (*Extension, error) {
	p := r.Snapshot(id)
	if p == nil {
		return nil, i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if p.ReadOnly() {
		return nil, i18n.Err("extension-status.read_only", i18n.Args{"id": id})
	}
	if p.Record.Source == SourceDev {
		return nil, i18n.Err("extension-status.dev_read_only", i18n.Args{"id": id})
	}
	return p, nil
}

// SetEnabled switches a plugin on or off; on only when CanEnable lets it.
func (r *Registry) SetEnabled(ctx context.Context, id string, on bool, actor string) error {
	return r.setEnabled(ctx, id, on, nil, actor)
}

// SetPackEnabled switches extension pack id on or off and records, in the same write, the members switching it off
// switched off (Record.PackOff; nil when it goes on). The members themselves are switched by the caller.
func (r *Registry) SetPackEnabled(ctx context.Context, id string, on bool, packOff []string, actor string) error {
	if packOff == nil {
		packOff = []string{}
	}
	return r.setEnabled(ctx, id, on, packOff, actor)
}

// setEnabled is SetEnabled; a non-nil packOff replaces the record's PackOff.
func (r *Registry) setEnabled(ctx context.Context, id string, on bool, packOff []string, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	r.mu.Lock()
	held := r.keptOff[id]
	r.mu.Unlock()
	if on && held != nil {
		return held.why
	}
	if on && !p.Record.Enabled {
		if err := r.canEnable(ctx, p.Record.Source, p.Manifest); err != nil {
			return err
		}
	}
	r.mu.Lock()
	row := p.Record
	row.Enabled = on
	if packOff != nil {
		row.PackOff = slices.Clone(packOff)
		if len(row.PackOff) == 0 {
			row.PackOff = nil
		}
	}
	r.mu.Unlock()
	mutation := r.settingsWrite(id)
	defer mutation.finish()
	revision, err := r.write(ctx, row) // the owner's choice, held off or not
	if err != nil {
		return err
	}
	r.mu.Lock()
	p.Record.Enabled = on
	p.Record.PackOff = row.PackOff
	p.Record.UpdatedAt = revision
	if held != nil {
		held.row = on
	}
	r.mu.Unlock()
	kind := map[bool]string{true: "enabled", false: "disabled"}[on]
	unlock()
	r.audit(ctx, id, kind, actor, nil)
	mutation.changed(Change{ID: id, Kind: kind})
	return nil
}

// ForgetPackOff drops member from the PackOff of every pack that switched it off: the user switched it on or off
// themselves, so switching the pack back on leaves it as they chose.
func (r *Registry) ForgetPackOff(ctx context.Context, member string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	r.mu.Lock()
	member = r.canonicalID(member)
	var rows []Record
	var packs []*Extension
	same := func(id string) bool { return r.canonicalID(id) == member } // a member renamed since keeps its old id as an alias
	for _, p := range r.extensions {
		if slices.ContainsFunc(p.Record.PackOff, same) {
			row := snapshotExtension(p).Record
			row.PackOff = slices.DeleteFunc(row.PackOff, same)
			if len(row.PackOff) == 0 {
				row.PackOff = nil
			}
			rows, packs = append(rows, row), append(packs, p)
		}
	}
	r.mu.Unlock()
	var changed []*settingsMutation
	defer func() { // after the operation, as every write tells it
		unlock()
		for _, mutation := range changed {
			mutation.changed(Change{ID: mutation.id, Kind: "pack_off"})
		}
	}()
	for i, row := range rows {
		mutation := r.settingsWrite(row.ID)
		revision, err := r.put(ctx, row)
		if err != nil {
			mutation.finish()
			return err
		}
		r.mu.Lock()
		packs[i].Record.PackOff = row.PackOff
		packs[i].Record.UpdatedAt = revision
		r.mu.Unlock()
		changed = append(changed, mutation)
	}
	return nil
}

// SetConfig replaces a plugin's configuration, held to its schema first.
func (r *Registry) SetConfig(ctx context.Context, id string, raw json.RawMessage, actor string) error {
	_, err := r.SetConfigClearing(ctx, id, raw, actor)
	return err
}

// ErrSecretNotCleared: a secret the new configuration unbinds (BoundSecrets) could not be deleted, so the
// configuration was not saved.
var ErrSecretNotCleared = i18n.Err("extension-status.secret_not_cleared")

// SetConfigClearing is SetConfig saying which secrets the change deleted. Entry-list removals and address changes
// commit their secret deletions with the configuration. BoundSecrets retains its legacy fail-closed cleanup.
func (r *Registry) SetConfigClearing(ctx context.Context, id string, raw json.RawMessage, actor string) (cleared []string, err error) {
	return r.setConfigClearing(ctx, id, raw, actor, false)
}

// SetConfigPreservingEntries saves an older client's full-config draft without letting it add, remove, reorder or
// rebind entry lists it cannot draw. Other fields still save normally.
func (r *Registry) SetConfigPreservingEntries(ctx context.Context, id string, raw json.RawMessage, actor string) (cleared []string, err error) {
	return r.setConfigClearing(ctx, id, raw, actor, true)
}

func (r *Registry) setConfigClearing(ctx context.Context, id string, raw json.RawMessage, actor string, preserveLists bool) (cleared []string, err error) {
	p, err := r.editableSnapshot(id)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareEntryConfig(p, raw, preserveLists)
	if err != nil {
		return nil, err
	}
	checked, err := r.checkConfigSettings(ctx, p, prepared)
	if err != nil {
		return nil, err
	}
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err = r.editable(id)
	if err != nil {
		return nil, err
	}
	id = p.Record.ID
	if !sameJSON(compactConfig(r.SavedConfig(p)), prepared.base) {
		return nil, ErrSettingsMoved
	}
	if err := r.stillChecked(p, checked); err != nil {
		return nil, err
	}
	var had map[string]string
	var readErr error
	if len(prepared.deleted) > 0 && r.store != nil {
		had, readErr = r.store.ExtensionSecrets(ctx, p.Record.StorageKey())
		if _, ok := r.store.(PlanStore); !ok {
			return nil, fmt.Errorf("%w: the store cannot delete entry secrets with the configuration", ErrSecretNotCleared)
		}
	}
	mutation := r.settingsWrite(id)
	defer mutation.finish()
	finish := func(err error) ([]string, error) {
		unlock()
		for _, name := range cleared {
			r.audit(ctx, id, "secret_delete", actor, map[string]any{"name": name, "why": "config"})
		}
		if err != nil {
			if len(cleared) > 0 { // the program runs again without them, on the configuration it had
				mutation.changed(Change{ID: id, Kind: "secret"})
			}
			return cleared, err
		}
		r.audit(ctx, id, "config", actor, nil) // not the values: they may be personal
		mutation.changed(Change{ID: id, Kind: "config"})
		return cleared, nil
	}
	boundCleared, clearErr := r.clearBound(ctx, p, prepared.raw)
	cleared = slices.Clone(boundCleared)
	if clearErr != nil {
		return finish(clearErr)
	}
	r.mu.Lock()
	row := p.Record
	row.Config = prepared.raw
	row.DeleteSecrets = slices.Clone(prepared.deleted)
	if row.Source == SourceInstalled {
		row.ExpectedConfig = slices.Clone(prepared.base)
	}
	r.mu.Unlock()
	revision, err := r.put(ctx, row)
	if err != nil {
		var unknown *CommitUnknownError
		if errors.As(err, &unknown) {
			r.planProblem = err
		}
		return finish(err)
	}
	for _, name := range prepared.deleted {
		if _, ok := had[name]; ok || readErr != nil {
			cleared = append(cleared, name)
		}
	}
	r.mu.Lock()
	p.Record.Config = prepared.raw
	p.Record.UpdatedAt = revision
	r.mu.Unlock()
	return finish(nil)
}

// clearBound deletes the secrets of p that configuration next unbinds (BoundSecrets), whether or not they are
// known to have a value (a failed read must not keep one), and says which went. Called in an operation.
func (r *Registry) clearBound(ctx context.Context, p *Extension, next json.RawMessage) ([]string, error) {
	if r.BoundSecrets == nil || r.store == nil {
		return nil, nil
	}
	names := r.BoundSecrets(p, r.Config(p), p.Schema.Effective(next))
	if len(names) == 0 {
		return nil, nil
	}
	had, readErr := r.store.ExtensionSecrets(ctx, p.Record.StorageKey()) // only to say which went
	var cleared []string
	for _, name := range names {
		if err := r.store.DeleteExtensionSecret(ctx, p.Record.StorageKey(), name); err != nil {
			return cleared, fmt.Errorf("%w（%s：%v）", ErrSecretNotCleared, name, err)
		}
		if _, ok := had[name]; ok || readErr != nil {
			cleared = append(cleared, name)
		}
	}
	return cleared, nil
}

// Config is what the plugin gets: its configuration with the schema's defaults.
func (r *Registry) Config(p *Extension) json.RawMessage {
	r.mu.Lock()
	raw := p.Record.Config
	r.mu.Unlock()
	return p.Schema.Effective(raw)
}

// SavedConfig is the configuration as the user saved it, without the schema's defaults ({} when none).
func (r *Registry) SavedConfig(p *Extension) json.RawMessage {
	r.mu.Lock()
	raw := slices.Clone(p.Record.Config)
	r.mu.Unlock()
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (r *Registry) secretSpec(p *Extension, name string) error {
	fixed := slices.ContainsFunc(p.Manifest.Secrets, func(s SecretSpec) bool { return s.Name == name })
	_, entry := activeEntrySecrets(p)[name]
	if !fixed && !entry {
		return i18n.Err("extension-status.secret_missing", i18n.Args{"id": p.Record.ID, "name": name})
	}
	return nil
}

// entrySecretAddressError refuses a credential before extension code can see it when a configured egress binding has
// no valid target. The operation-locked write repeats this check against the then-current snapshot.
func entrySecretAddressError(p *Extension, name string) error {
	binding, ok := activeEntrySecrets(p)[name]
	if !ok {
		return nil
	}
	for _, network := range p.Manifest.Permissions.ConfiguredNetworks() {
		if network.List != binding.List.Name {
			continue
		}
		var address string
		_ = json.Unmarshal(binding.Entry.Values[network.Field], &address)
		if target, _ := configuredTargetValue(network, address); target.Host == "" {
			return i18n.ErrEnglish("extension-status.entry_address_required")
		}
	}
	return nil
}

// SetSecret keeps one of a plugin's declared secrets, sealed. The value never goes to a log or back to anyone.
func (r *Registry) SetSecret(ctx context.Context, id, name, value, actor string) error {
	if strings.HasPrefix(value, refMarker) {
		return i18n.Err("extension-status.secret_size", i18n.Args{"max": SecretMost >> 10}) // never typed: refused like an empty one
	}
	return r.setSecret(ctx, id, name, value, actor, map[string]any{"name": name})
}

// setSecret keeps value (or a SealedReference) as secret name, checked first, audited with detail.
func (r *Registry) setSecret(ctx context.Context, id, name, value, actor string, detail map[string]any) error {
	checked, err := r.checkSettings(ctx, id, nil, name, value)
	if err != nil {
		return err
	}
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	stable := r.Snapshot(id)
	if stable == nil {
		return i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if err := r.secretSpec(stable, name); err != nil {
		return err
	}
	if err := entrySecretAddressError(stable, name); err != nil {
		return err
	}
	if err := r.stillChecked(p, checked); err != nil {
		return err
	}
	if r.SecretNeeds != nil {
		if err := r.SecretNeeds(p, name, r.Config(p)); err != nil {
			return err
		}
	}
	if value == "" || len(value) > SecretMost {
		return i18n.Err("extension-status.secret_size", i18n.Args{"max": SecretMost >> 10})
	}
	if r.store == nil || r.seal == nil {
		return i18n.Err("extension-status.secret_no_database")
	}
	mutation := r.settingsWrite(id)
	defer mutation.finish()
	if err := r.ensureRow(ctx, p); err != nil {
		return err
	}
	sealed, err := r.seal.Seal(value)
	if err != nil {
		return err
	}
	if err := r.store.PutExtensionSecret(ctx, p.Record.StorageKey(), name, sealed); err != nil {
		return err
	}
	unlock()
	r.audit(ctx, id, "secret_set", actor, detail)
	mutation.changed(Change{ID: id, Kind: "secret"})
	return nil
}

// DeleteSecret forgets one of a plugin's secrets.
func (r *Registry) DeleteSecret(ctx context.Context, id, name, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	if err := r.secretSpec(p, name); err != nil {
		return err
	}
	if r.store == nil {
		return nil
	}
	mutation := r.settingsWrite(id)
	defer mutation.finish()
	if err := r.store.DeleteExtensionSecret(ctx, p.Record.StorageKey(), name); err != nil {
		return err
	}
	unlock()
	r.audit(ctx, id, "secret_delete", actor, map[string]any{"name": name})
	mutation.changed(Change{ID: id, Kind: "secret"})
	return nil
}

// ensureRow gives a built-in plugin its row before a secret refers to it.
func (r *Registry) ensureRow(ctx context.Context, p *Extension) error {
	if p.Record.Source != SourceBuiltin {
		return nil
	}
	rows, err := r.store.Extensions(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(rows, func(x Record) bool { return x.ID == p.Record.ID }) {
		return nil
	}
	r.mu.Lock()
	row := p.Record
	r.mu.Unlock()
	revision, err := r.put(ctx, row)
	if err != nil {
		return err
	}
	r.mu.Lock()
	p.Record.UpdatedAt = revision
	r.mu.Unlock()
	return nil
}

// Secrets are a plugin's declared fixed and currently live entry secrets in plain text, for handing to it only.
func (r *Registry) Secrets(ctx context.Context, id string) (map[string]string, error) {
	p := r.Snapshot(id)
	if p == nil {
		return nil, nil
	}
	allowed := activeSecretNames(p, p.Record.Config)
	r.mu.Lock()
	dev, isDev := r.devSecrets[id]
	r.mu.Unlock()
	if isDev {
		out := map[string]string{}
		for name, value := range dev {
			if allowed[name] {
				out[name] = value
			}
		}
		return out, nil
	}
	if r.store == nil || r.seal == nil {
		return nil, nil
	}
	sealed, err := r.store.ExtensionSecrets(ctx, r.storageKey(id))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sealed))
	for name, s := range sealed {
		if !allowed[name] {
			continue
		}
		plain, err := r.seal.Open(s)
		if err != nil {
			return nil, i18n.Err("extension-status.secret_decrypt", i18n.Args{"id": id, "name": name})
		}
		out[name] = plain
	}
	r.noteReferences(id, out)
	return out, nil
}

// SecretsSet says which declared fixed and live entry secrets have a value.
func (r *Registry) SecretsSet(ctx context.Context, p *Extension) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	// Callers often hold Get's live pointer. Derive entry identities from one stable record/configuration snapshot.
	r.mu.Lock()
	p = snapshotExtension(p)
	r.mu.Unlock()
	names := activeSecretNames(p, p.Record.Config)
	if p.Record.Source == SourceDev {
		r.mu.Lock()
		for name := range names {
			_, out[name] = r.devSecrets[p.Record.ID][name]
		}
		r.mu.Unlock()
		return out
	}
	if r.store == nil || p.ReadOnly() {
		return out
	}
	sealed, err := r.store.ExtensionSecrets(ctx, p.Record.StorageKey())
	if err != nil {
		return out
	}
	for name := range names {
		_, out[name] = sealed[name]
	}
	return out
}

// Missing are what a plugin needs before it can run: required secrets without a value, a configuration its schema
// does not take (after an upgrade changed it). Empty: ready.
func (r *Registry) Missing(ctx context.Context, p *Extension) []string {
	if p == nil {
		return nil
	}
	g := r.dependencyGraph(ctx)
	if g.nodes[p.Record.ID] == nil {
		g.nodes[p.Record.ID] = snapshotExtension(p)
	}
	return slices.Clone(g.problems(p.Record.ID))
}

// ErrSettingsMoved: the settings changed while a change of them was being checked (settings_check); nothing saved.
var ErrSettingsMoved = i18n.Err("extension-status.settings_moved")

// checkConfigSettings checks exactly the post-cleanup state: no removed entry secret and no credential whose bound
// address is changing is given to the extension together with the proposed configuration.
func (r *Registry) checkConfigSettings(ctx context.Context, p *Extension, prepared preparedConfig) (*settingsChecked, error) {
	checked := &settingsChecked{config: r.Config(p), manifest: slices.Clone(p.Record.Manifest)}
	if !p.Manifest.SettingsCheck || r.SettingsCheck == nil {
		return checked, nil
	}
	secrets, err := r.Secrets(ctx, p.Record.ID)
	if err != nil {
		return nil, err
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	for _, name := range prepared.deleted {
		delete(secrets, name)
	}
	if r.BoundSecrets != nil {
		for _, name := range r.BoundSecrets(p, r.Config(p), p.Schema.Effective(prepared.raw)) {
			delete(secrets, name)
		}
	}
	allowed := activeSecretNames(p, prepared.raw)
	for name := range secrets {
		if !allowed[name] {
			delete(secrets, name)
		}
	}
	if err := r.SettingsCheck(ctx, p, p.Schema.Effective(prepared.raw), secrets); err != nil {
		return nil, err
	}
	return checked, nil
}

// settingsChecked is what a change was checked against: the configuration and complete manifest the extension had
// then (nil: not checked). A replacement manifest invalidates preparation as well as the extension's own check.
type settingsChecked struct {
	config   json.RawMessage
	manifest json.RawMessage
}

// checkSettings has extension id check its settings as a change would leave them (SettingsCheck): config replaced
// by next (nil: kept), or secret name set to value (name ""). Only for one that declares settings_check, and only with
// values that pass the schema and the secret's rules first (those errors are the registry's own, later).
func (r *Registry) checkSettings(ctx context.Context, id string, next json.RawMessage, name, value string) (*settingsChecked, error) {
	p := r.Snapshot(id)
	if p == nil {
		return nil, nil
	}
	now := r.Config(p)
	checked := &settingsChecked{config: now, manifest: slices.Clone(p.Record.Manifest)}
	if !p.Manifest.SettingsCheck || r.SettingsCheck == nil {
		return checked, nil
	}
	config := now
	if next != nil {
		if p.Schema.Check(next) != nil {
			return nil, nil
		}
		config = p.Schema.Effective(next)
	}
	if name != "" && (r.secretSpec(p, name) != nil || value == "" || len(value) > SecretMost) {
		return nil, nil
	}
	if name != "" {
		if err := entrySecretAddressError(p, name); err != nil {
			return nil, err
		}
	}
	secrets, err := r.Secrets(ctx, id)
	if err != nil {
		return nil, err
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	if name != "" {
		secrets[name] = value
	}
	if err := r.SettingsCheck(ctx, p, config, secrets); err != nil {
		return nil, err
	}
	return checked, nil
}

// stillChecked refuses a change whose check (checkSettings) saw other settings than p has now: one saved meanwhile
// was not checked with this one. Called in the operation.
func (r *Registry) stillChecked(p *Extension, c *settingsChecked) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	raw := slices.Clone(p.Record.Config)
	manifest := slices.Clone(p.Record.Manifest)
	r.mu.Unlock()
	if bytes.Equal(manifest, c.manifest) && string(p.Schema.Effective(raw)) == string(c.config) {
		return nil
	}
	return ErrSettingsMoved
}
