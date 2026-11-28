package extension

import (
	"context"
	"encoding/json"
	"slices"
)

// Dormant rows (#16263, docs/architecture/core-and-modules.md "Modules from extensions"). A module an installed
// extension may supply (modapi.ExtensionModules) leaves rows of its id behind in two cases: a build without its
// built-in finds the built-in's row (an orphan), or the modules setting switched the module off. Load sets those rows
// aside instead of reading them: they are not listed, nothing runs them and nothing in them changes, including their
// sealed secrets and files. Load never fails because of one. An install from the official market adopts an orphan
// in place (adoptDormant); switching the module back on and restarting reads the row again as it was.

// HoldModule names id as a module an installed extension may supply, and whether the modules setting switched it off.
// The host calls it for every module on its list before Load.
func (r *Registry) HoldModule(id string, off bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moduleHolds[id] = off
}

// holdsDormant: Load sets row aside. Its id is a held module's, and either the module is switched off or the row is a
// built-in's that no registered built-in (cur) serves. Called with r.mu held.
func (r *Registry) holdsDormant(row Record, cur *Extension) bool {
	off, held := r.moduleHolds[row.ID]
	if !held {
		return false
	}
	return off || row.Source == SourceBuiltin && (cur == nil || cur.Record.Source != SourceBuiltin)
}

// adoptDormant lets the new install row of preview pv take over the dormant row held of the same id, or refuses it
// with ErrIdentity and writes nothing. Only an orphaned built-in row of a module left on is taken over, and only by
// a package of that id from the official market, verified with its current signing key, that provides every
// contribution the built-in's manifest names. The row keeps the owner's settings, switch and contribution choices and
// is marked as the built-in's verified successor, so its storage key, sealed secrets, data and the transcription
// pinned to it stay the built-in's. guardHandoff pins the stored row, so the write fails when it changed since Load.
// Called with r.mu held, from validatePlan: every install entry point (market, bundle, upload, command line) commits
// through it.
func (r *Registry) adoptDormant(ctx context.Context, held Record, pv *Preview, m *Manifest, requested string, row *Record) error {
	if r.moduleHolds[held.ID] || held.Source != SourceBuiltin || held.StorageKey() != m.ID || pv.replaces != "" {
		return ErrIdentity
	}
	orphan := &Extension{Record: held}
	if err := r.checkHandoffPreview(orphan, pv, requested); err != nil {
		return err
	}
	var builtin Manifest
	if err := json.Unmarshal(held.Manifest, &builtin); err != nil {
		return ErrIdentity
	}
	needed := builtin.Contributions()
	if len(needed) == 0 { // what the built-in served is unknown: nothing can stand in for it
		return ErrIdentity
	}
	for point, ids := range needed {
		offered := m.Items(point)
		for _, id := range ids {
			if !slices.Contains(offered, id) {
				return ErrIdentity
			}
		}
	}
	row.Config = compactConfig(held.Config)
	row.Enabled = held.Enabled
	row.DisabledContributions = m.StartsOff(&builtin, held.DisabledContributions)
	row.PackOff = slices.Clone(held.PackOff)
	row.BuiltinPredecessor = true
	row.PreviousOrigin, row.PreviousVersion = builtinOrigin(m.ID), held.Version
	row.NewInstallation = false
	return r.guardHandoff(ctx, orphan, row)
}
