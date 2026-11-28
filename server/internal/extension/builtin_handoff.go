package extension

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"
)

// AllowBuiltinHandoff registers this built-in's public ID for a same-ID package
// verified against the host's pinned official catalog. Register before Load.
func (r *Registry) AllowBuiltinHandoff(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handoffs[id] = true
}

// CanHandoffBuiltin is the host's declaration for the current built-in. A held
// provider (for example, one disabled by legacy configuration) cannot switch.
func (r *Registry) CanHandoffBuiltin(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	return r.handoffs[id] && p != nil && p.Record.Source == SourceBuiltin && r.keptOff[id] == nil
}

func builtinOrigin(id string) string { return "builtin:" + id }

// VerifiedBuiltinPredecessor identifies an installed package that may retain the
// compiled provider's stored references across the handoff.
func (row Record) VerifiedBuiltinPredecessor() bool {
	market := FromMarket(row.Origin)
	return row.Source == SourceInstalled && row.BuiltinPredecessor && market != "" && row.Origin == "market:"+market+"/"+row.ID &&
		row.MarketKey != "" && (row.MarketKey == Official.SigningKey || slices.Contains(Official.PreviousSigningKeys, row.MarketKey))
}

// Check the private verification result at commit too. A raw package cannot
// target a stateful installation by borrowing its public ID or catalog URL.
func (r *Registry) checkHandoffPreview(old *Extension, pv *Preview, requested string) error {
	if old.Record.ID != pv.Manifest.ID || pv.RenameFrom != "" || pv.source.Kind != "market" ||
		!IsOfficialMarket(pv.marketSpec) || pv.verifiedMarketKey == "" || pv.verifiedMarketKey != Official.SigningKey {
		return ErrIdentity
	}
	if pv.builtinHandoff {
		if old.Record.Source != SourceBuiltin || !r.handoffs[old.Record.ID] || r.keptOff[old.Record.ID] != nil {
			return ErrIdentity
		}
		if requested == "" || requested != pv.Digest {
			return errPlanDigest
		}
	}
	return nil
}

// guardHandoff pins the database state to the owner choices being transferred.
// A pristine built-in has no database row. Otherwise id, source, version and
// the database's own revision are compared again by the write transaction.
func (r *Registry) guardHandoff(ctx context.Context, old *Extension, row *Record) error {
	rows, err := r.store.Extensions(ctx)
	if err != nil {
		return err
	}
	want := old.Record
	for _, saved := range rows {
		if saved.StorageKey() != want.StorageKey() {
			continue
		}
		if saved.Source != want.Source || saved.ID != want.ID ||
			(want.Source == SourceInstalled && (saved.Version != want.Version || saved.MarketKey != want.MarketKey || saved.Origin != want.Origin || !saved.VerifiedBuiltinPredecessor())) {
			return ErrIdentity
		}
		if want.Source == SourceInstalled && (saved.GrantedDigest != want.GrantedDigest || !reflect.DeepEqual(saved.Granted, want.Granted)) {
			return ErrSettingsMoved
		}
		if !sameJSON(compactConfig(saved.Config), compactConfig(want.Config)) || saved.Enabled != want.Enabled ||
			!slices.Equal(saved.DisabledContributions, want.DisabledContributions) || !slices.Equal(saved.PackOff, want.PackOff) {
			return ErrSettingsMoved
		}
		row.ExpectedID, row.ExpectedVersion = saved.ID, saved.Version
		row.ExpectedSource, row.ExpectedUpdatedAt = saved.Source, saved.UpdatedAt
		row.InstalledAt = saved.InstalledAt
		return nil
	}
	if want.Source != SourceBuiltin || !want.InstalledAt.IsZero() {
		return ErrIdentity
	}
	row.ExpectedID, row.ExpectedVersion = "", ""
	row.NewInstallation = true
	row.InstalledAt = r.now()
	return nil
}

// rollbackBuiltin restores the compiled code, schema, default grants and
// translations. It writes the current owner settings into the same slot without
// deleting the sealed credentials, installation storage or files.
func (r *Registry) rollbackBuiltin(ctx context.Context, old *Extension, actor string, unlock func()) (*Extension, error) {
	id := old.Record.ID
	r.mu.Lock()
	back := snapshotExtension(r.yielded[id])
	r.mu.Unlock()
	if back == nil || !old.Record.VerifiedBuiltinPredecessor() {
		return nil, ErrIdentity
	}
	if old.Record.Enabled {
		if err := r.canEnable(ctx, SourceBuiltin, back.Full); err != nil {
			return nil, err
		}
	}
	row := back.Record
	row.Enabled, row.DisabledContributions, row.PackOff = old.Record.Enabled, old.Record.DisabledContributions, old.Record.PackOff
	var err error
	row.Config, err = rollbackFixedSlots(old.Full, back.Full, compactConfig(old.Record.Config))
	if err != nil {
		return nil, err
	}
	if err := back.Schema.CheckSaved(row.Config); err != nil {
		return nil, err
	}
	if err := checkEntrySecretNamespace(back.Full, back.Schema, row.Config); err != nil {
		return nil, err
	}
	mutation, err := r.commitBuiltinRollback(ctx, old, back, row)
	if err != nil {
		return nil, err
	}
	unlock()
	r.audit(ctx, id, "rollback", actor, map[string]any{"from": old.Record.Version, "to": back.Record.Version})
	mutation.changed(Change{ID: id, Kind: "rolled_back", Old: old})
	return back, nil
}

func (r *Registry) commitBuiltinRollback(ctx context.Context, old, back *Extension, row Record) (*settingsMutation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := old.Record.ID
	if current := r.extensions[id]; current == nil || !current.Record.BuiltinPredecessor || current.Record.Version != old.Record.Version {
		return nil, ErrIdentity
	}
	if err := r.conflicts(back.Full); err != nil {
		return nil, err
	}
	if err := r.checkDependencyGraph(back.Full); err != nil {
		return nil, err
	}
	if err := r.guardHandoff(ctx, old, &row); err != nil {
		return nil, err
	}
	row.UpdatedAt = r.now()
	mutation := r.settingsWriteLocked(id)
	if err := r.store.PutExtension(ctx, row); err != nil {
		mutation.finishLocked()
		var unknown *CommitUnknownError
		if errors.As(err, &unknown) {
			r.planProblem = err
		}
		return nil, err
	}
	row.ExpectedID, row.ExpectedVersion, row.ExpectedSource = "", "", ""
	row.ExpectedUpdatedAt = time.Time{}
	back.Record, back.Manifest = row, back.Full.Without(row.DisabledContributions)
	r.extensions[id] = back
	delete(r.yielded, id)
	r.prune(row.StorageKey())
	return mutation, nil
}
