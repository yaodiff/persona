package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// settle is what comes after an install or upgrade: it waits for the extension to come up, then for its program to
// reach the computers it runs on, and ends op.
func (h *Manager) settle(ctx context.Context, op *extOp, p *extension.Extension, upgrade bool) {
	ps := h.extensions
	id := p.Record.ID
	h.opStep(op, extension.StepInstall, extension.StepDone)
	h.opStep(op, extension.StepStart, extension.StepRunning)
	if err := h.waitStarted(ctx, id, upgrade, op); err != nil {
		h.opFail(op, err)
		return
	}
	h.opStep(op, extension.StepStart, extension.StepDone)
	if cur := ps.reg.Snapshot(id); cur != nil && cur.Manifest.NodeProgram() != nil {
		h.syncNodesFor(ctx, op, cur)
	}
	if h.needsSignIn(ctx, id) {
		h.update(op, func(v *ExtensionOperation) bool { v.Next = "auth"; return true })
	}
	h.opSucceed(op)
}

// waitStarted waits until extension id runs (or needs nothing to: no program, started by its first call, switched
// off, waiting for its settings). An upgrade that does not come up is rolled back by watchUpgrade: that is the
// failure then.
func (h *Manager) waitStarted(ctx context.Context, id string, upgrade bool, op *extOp) error {
	ps := h.extensions
	hp := ps.get(id)
	if hp == nil || hp.proc == nil {
		return nil
	}
	wait := host.ReadyWithin + 5*time.Second
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	opMeta := func() (name, version, from string) {
		ps.ops.mu.Lock()
		defer ps.ops.mu.Unlock()
		return op.v.Name, op.v.Version, op.v.From
	}
	for {
		// Subscribe before inspecting Status so a transition between Status and select cannot be missed.
		changes := hp.proc.Changes()
		state, why := hp.proc.Status()
		switch state {
		case host.StateRunning, host.StateStopped:
			return nil
		case host.StateError:
			name, version, from := opMeta()
			if upgrade {
				return localizedKind(errRolledBack, "hub-extension_operations.rolled_back", i18n.Args{"name": name, "to": version, "from": from, "why": why})
			}
			return i18n.Err("hub-extension_operations.start_failed", i18n.Args{"name": name, "why": why})
		}
		select {
		case <-ctx.Done():
			name, _, _ := opMeta()
			return i18n.Err("hub-extension_operations.start_timeout", i18n.Args{"name": name, "seconds": int(wait / time.Second)})
		case <-changes:
		}
	}
}

// syncNodesFor sends p's program to (or takes it off) every online computer it is for, and waits for each.
func (h *Manager) syncNodesFor(ctx context.Context, op *extOp, p *extension.Extension) {
	ps := h.extensions
	id := p.Record.ID
	var machines []*Machine
	for _, m := range h.machinesNow() {
		if m.isOnline() {
			machines = append(machines, m)
		}
	}
	h.update(op, func(v *ExtensionOperation) bool {
		setStep(v, extension.StepNodes, extension.StepRunning)
		stepOf(v, extension.StepNodes).Machines = &OpMachines{Total: len(machines)}
		return true
	})
	var wg sync.WaitGroup
	for _, m := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncErr := h.syncExtensionNodes(ctx, m) // after the one the change started (one at a time per machine)
			why := ""
			if syncErr != nil {
				why = syncErr.Error()
			}
			for _, st := range ps.nodes.views(id) {
				if st.MachineID == m.cfg.ID && st.Error != "" {
					why = st.Error
				}
			}
			h.update(op, func(v *ExtensionOperation) bool {
				ms := stepOf(v, extension.StepNodes).Machines
				ms.Done++
				if why != "" {
					if ms.Errors == nil {
						ms.Errors = map[string]string{}
					}
					ms.Errors[m.cfg.ID] = why
				}
				return true
			})
		}()
	}
	wg.Wait()
	h.opStep(op, extension.StepNodes, extension.StepDone)
}

// previewJob is a preview to run as an operation: what it is of, and how (again, for a retry).
type previewJob struct {
	kind, id string // opInstall or opUpgrade; the extension (as far as it is known before)
	ofMarket bool
	actor    string
	lang     i18n.Lang
	run      func(ctx context.Context) (*extension.Preview, error)
}

// launch runs job as a new operation: in the background when async (its answer is nil, nil), else before it returns.
// from is the failed operation it retries: what the user agreed to there carries over.
func (h *Manager) launch(ctx context.Context, job previewJob, from *extOp, async bool) (*extOp, *extension.Preview, error) {
	asked := ctx
	if async {
		ctx = context.WithoutCancel(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	op := h.startOpWith(job.kind, job.id, job.actor, job.lang, job.ofMarket, func(op *extOp) {
		op.cancel = cancel
		op.retry = func() (*extOp, error) {
			next, _, _ := h.launch(i18n.With(context.Background(), job.lang), job, op, true)
			return next, nil
		}
		if from != nil {
			op.consented, op.consentedBundle = from.consented, maps.Clone(from.consentedBundle)
		}
	})
	do := func() (*extension.Preview, error) {
		defer cancel(nil)
		pv, err := job.run(extension.WithProgress(ctx, h.progress(op)))
		if err != nil {
			if !async && asked.Err() != nil && errors.Is(err, context.Canceled) && !errors.Is(context.Cause(ctx), extension.ErrUserCanceled) {
				// The app stopped waiting (a phone's request timed out on a slow download, #3692): its package
				// goes on downloading into the cache, and asking again picks it up.
				err = i18n.Err("hub-extension_operations.client_left")
			}
			h.opFail(op, err)
			return nil, err
		}
		if h.opPreviewed(op, pv, func(refs []bundleRef) {
			upgrade := ""
			if pv.Upgrade {
				upgrade = pv.Manifest.ID
				if pv.RenameFrom != "" {
					upgrade = pv.RenameFrom
				}
			}
			_, _ = h.installOp(i18n.With(context.Background(), job.lang), op, upgrade, pv.ID, pv.Digest, refs, job.actor)
		}) {
			return nil, extension.ErrUserCanceled
		}
		return pv, nil
	}
	if async {
		go func() { _, _ = do() }()
		return op, nil, nil
	}
	pv, err := do()
	return op, pv, err
}

// installOp installs (upgrade "") or upgrades (upgrade: its id) the previewed previewID as op, as the install and
// upgrade calls do; what comes after (start, the computers) goes on in the background.
func (h *Manager) installOp(ctx context.Context, op *extOp, upgrade, previewID, digest string, refs []bundleRef, actor string) (*extension.Extension, error) {
	reg := h.extensions.reg
	pv := reg.PreviewOf(previewID)
	if pv != nil && pv.RenameFrom != "" {
		if upgrade != "" && upgrade != pv.Manifest.ID && reg.CanonicalID(upgrade) != pv.RenameFrom {
			return nil, i18n.Err("extension-identity.invalid")
		}
		upgrade = pv.RenameFrom
	}
	h.installing(op, pv, digest)
	p, err := h.installExtensionPlan(extension.WithProgress(ctx, h.progress(op)), upgrade, previewID, digest, refs, actor) // members and it together
	if err != nil {
		h.opFail(op, err)
		return nil, err
	}
	go h.settle(context.WithoutCancel(ctx), op, p, upgrade != "")
	return p, nil
}

// uninstallOp is an uninstall as an operation (async: in the background, the answer 202 with it). The answer of a
// synchronous one is the one it always was.
func (h *Manager) uninstallOp(w http.ResponseWriter, r *http.Request, id string, o uninstallOptions, async bool) {
	ps := h.extensions
	p := ps.reg.Get(id)
	if p == nil {
		h.uninstallPack(w, r, id, o) // its 404
		return
	}
	op := h.startOpWith(opUninstall, id, opActor(r, o.actor), h.requestLang(r), extension.FromMarket(p.Record.Origin) != "", func(op *extOp) {
		op.retry = func() (*extOp, error) {
			bg := r.Clone(context.WithoutCancel(r.Context()))
			rec := &opRecorder{header: http.Header{}, status: http.StatusOK}
			h.uninstallOp(rec, bg, id, o, true)
			var out struct {
				Operation ExtensionOperation `json:"operation"`
			}
			_ = json.Unmarshal(rec.body, &out)
			if next := h.opByID(out.Operation.ID); next != nil {
				return next, nil
			}
			return nil, i18n.Err("hub-extension_operations.not_found")
		}
	})
	run := func(r *http.Request) *opRecorder {
		h.opStep(op, extension.StepStop, extension.StepRunning)
		rec := &opRecorder{header: http.Header{}, status: http.StatusOK}
		h.uninstallPack(rec, r, id, o)
		if rec.status >= 300 {
			var e struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.body, &e)
			h.opFail(op, codedErr{code: e.Code, msg: e.Error})
			return rec
		}
		h.opStep(op, extension.StepStop, extension.StepDone)
		h.opStep(op, extension.StepRemove, extension.StepDone)
		if p.Manifest.NodeProgram() != nil {
			h.syncNodesFor(context.WithoutCancel(r.Context()), op, p)
		}
		// The answer's warnings (files left to delete by hand) go on the operation, as the synchronous answer has them.
		var out struct {
			Warnings []string `json:"warnings"`
		}
		_ = json.Unmarshal(rec.body, &out)
		if len(out.Warnings) > 0 {
			h.update(op, func(v *ExtensionOperation) bool {
				v.Warnings = out.Warnings
				return false // opSucceed pushes it
			})
		}
		h.opSucceed(op)
		return rec
	}
	if async {
		bg := r.Clone(context.WithoutCancel(r.Context()))
		go run(bg)
		h.writeOp(w, op)
		return
	}
	rec := run(r)
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body)
}

// opRecorder keeps an answer the hub writes, to read it (and pass it on) after.
type opRecorder struct {
	header http.Header
	status int
	body   []byte
	wrote  bool
}

func (o *opRecorder) Header() http.Header { return o.header }
func (o *opRecorder) WriteHeader(code int) {
	if !o.wrote {
		o.status, o.wrote = code, true
	}
}
func (o *opRecorder) Write(b []byte) (int, error) {
	o.wrote = true
	o.body = append(o.body, b...)
	return len(b), nil
}

// opActor is who an operation is shown as started by: the device's name, or def (the CLI, the hub).
func opActor(r *http.Request, def string) string {
	if p := principalOf(r.Context()); p.Name != "" {
		return p.Name
	}
	return actorOr(r, def)
}
