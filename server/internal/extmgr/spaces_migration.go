package extmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

// This one-time migration installs the product surface that used to be built
// in. It is deliberately based on persisted pre-cutover records, never on
// whether this binary happens to be running for the first time.
const (
	spacesMigrationID     = "space-conversations"
	spacesMigrationKey    = "spaces.extension_migration_v1"
	spacesMigrationActor  = "Spaces upgrade"
	spacesMigrationNeeded = "needed"
)

type spacesMigrationState struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type spacesMigrationStore interface {
	Tracks(context.Context) ([]db.Track, error)
}

func (h *Manager) startSpacesMigration(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			done, err := h.migrateSpaces(ctx)
			if err != nil {
				h.logf("spaces migration: %v", err)
			}
			if done {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

var spacesMigrationRun sync.Map

// migrateSpaces is one bounded step of the upgrade. An existing installation,
// including a disabled one, always wins over migration. An uninstalled fresh
// hub has no pre-cutover records and therefore never receives the extension.
func (h *Manager) migrateSpaces(ctx context.Context) (bool, error) {
	if h.store == nil || h.extensions == nil || h.extensions.reg == nil {
		return false, nil
	}
	if p := h.extensions.reg.Get(spacesMigrationID); p != nil {
		return true, h.store.PutSetting(ctx, spacesMigrationKey, spacesMigrationState{Status: "present"})
	}
	var state spacesMigrationState
	found, err := h.store.Setting(ctx, spacesMigrationKey, &state)
	if err != nil {
		return false, err
	}
	if found && (state.Status == "not_needed" || state.Status == "present") {
		return true, nil
	}
	st, ok := h.store.(spacesMigrationStore)
	if !ok {
		return false, nil
	}
	tracks, err := st.Tracks(ctx)
	if err != nil {
		return false, err
	}
	used := false
	for _, t := range tracks {
		if !t.Builtin && t.Slug != "health" {
			used = true
			break
		}
	}
	if !used {
		return true, h.store.PutSetting(ctx, spacesMigrationKey, spacesMigrationState{Status: "not_needed"})
	}
	_, loaded := spacesMigrationRun.LoadOrStore(h, struct{}{})
	if loaded {
		return false, nil
	}
	defer spacesMigrationRun.Delete(h)
	if err := h.installSpacesExtension(ctx); err != nil {
		_ = h.store.PutSetting(ctx, spacesMigrationKey, spacesMigrationState{Status: spacesMigrationNeeded, Error: err.Error()})
		return false, err
	}
	return true, h.store.PutSetting(ctx, spacesMigrationKey, spacesMigrationState{Status: "present"})
}

func (h *Manager) installSpacesExtension(ctx context.Context) error {
	return h.installSpacesOperation(ctx, h.startOp(opInstall, spacesMigrationID, spacesMigrationActor, h.contextLang(ctx), true))
}

func (h *Manager) installSpacesOperation(ctx context.Context, op *extOp) error {
	var installErr error
	defer func() {
		if installErr != nil {
			h.opFail(op, installErr)
		}
	}()
	if err := h.marketsLoad(ctx); err != nil {
		installErr = err
		return installErr
	}
	var official string
	for _, view := range h.marketViews() {
		if view.Official {
			official = view.ID
			break
		}
	}
	if official == "" {
		installErr = errors.New("the official extension market is not registered")
		return installErr
	}
	if _, err := h.marketRead(ctx, official); err != nil {
		// The market keeps its last verified catalog for an unavailable read.
		// A signature failure is never bypassed by this migration.
		if errors.Is(err, errMarketSignature) || errors.Is(err, extension.ErrCatalogUnverified) {
			installErr = err
			return installErr
		}
		h.logf("spaces migration: refreshing the official market: %v", err)
	}
	rec, entry, err := h.marketEntry(official, spacesMigrationID)
	if err != nil {
		installErr = err
		return installErr
	}
	if !extension.IsOfficialMarket(rec.Spec) || rec.Signature.State != extension.SigSigned || rec.Signature.KeyID != keyIDOf(extension.Official.SigningKey) {
		installErr = errors.New("the Spaces extension requires a verified official market")
		return installErr
	}
	if err := entry.EngineProblem(h.build.Number); err != nil {
		installErr = err
		return installErr
	}
	mk, err := h.market(rec)
	if err != nil {
		installErr = err
		return installErr
	}
	pv, err := h.extensions.reg.PreviewMarket(extension.WithProgress(ctx, h.progress(op)), mk, rec.ID, entry, "")
	if err != nil {
		installErr = err
		return installErr
	}
	if pv.Manifest.ID != spacesMigrationID || pv.RenameFrom != "" {
		h.extensions.reg.Discard(pv.ID)
		installErr = fmt.Errorf("unexpected Spaces migration identity %q", pv.Manifest.ID)
		return installErr
	}
	h.opPreviewed(op, pv, nil)
	h.installing(op, pv, pv.Digest)
	p, err := h.extensions.reg.Install(ctx, pv.ID, pv.Digest, spacesMigrationActor)
	if err != nil {
		installErr = err
		return installErr
	}
	op.retry = func() (*extOp, error) {
		next := h.startOp(opInstall, spacesMigrationID, spacesMigrationActor, h.contextLang(context.Background()), true)
		go func() {
			if err := h.installSpacesOperation(context.Background(), next); err != nil {
				h.logf("spaces migration retry: %v", err)
			}
		}()
		return next, nil
	}
	h.settle(ctx, op, p, false)
	if op.v.State == opFailed {
		installErr = errors.New(op.v.Error)
		return installErr
	}
	h.tracksChanged()
	return nil
}
