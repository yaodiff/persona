package extmgr

// The disk_offload extension point (docs/server/machine-usage-map.md "Extension point disk_offload"): the routes the
// apps' disk usage map reads providers and starts offloads and restores through. The manager lists the enabled
// contributions without calling a program, asks one program at a time with the computer's context (the same object a
// machine_section gets) and the caller's language, holds every answer to offload.schema.json before passing it on, and
// checks every path of an offload against the computer's own protection rules (machine.storage.check) before the
// program hears of it. Only a device with full credentials gets here: extensions' tokens and answer-only devices are
// refused. Core never names a provider; logs say counts, never paths.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// How long each route waits on the program (and the pre-check on the node).
var (
	offloadInventoryTimeout = 20 * time.Second
	offloadRunTimeout       = 30 * time.Second
	offloadJobTimeout       = 10 * time.Second
	offloadCheckTimeout     = 15 * time.Second
)

const (
	// offloadBodyMost bounds a run's body.
	offloadBodyMost = 64 << 10
	// offloadWordsMost is the most characters of a provider's own words kept in an error.
	offloadWordsMost = 199

	codeOffloadRefused       = "offload_refused"
	codeToolInstallRequired  = "tool_install_required"
	codeOffloadPluginFailed  = "plugin_failed"
	codeOffloadDeadline      = "deadline"
	codeOffloadMachineOff    = "machine_offline"
	codeOffloadCheckFailed   = "usage_failed"
	codeOffloadNotFound      = "not_found"
	codeOffloadBadRequest    = "bad_request"
	codeOffloadReadOnly      = "read_only"
	codeOffloadConfirmFirst  = proto.CodeConfirmationRequired
	codeOffloadNodeOutdated  = proto.CodeNodeOutdated
	offloadInventoryItemMost = 500
)

// offloadRoutes registers the five routes.
func (h *Manager) offloadRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/extensions/-/offload", h.offloadGate(h.getOffloadProviders))
	api.HandleFunc("GET /api/v1/extensions/{id}/-/offload/{cid}/inventory", h.offloadGate(h.withOffloadProvider(h.getOffloadInventory)))
	api.HandleFunc("POST /api/v1/extensions/{id}/-/offload/{cid}/run", h.offloadGate(h.withOffloadProvider(h.postOffloadRun)))
	api.HandleFunc("GET /api/v1/extensions/{id}/-/offload/{cid}/jobs/{job}", h.offloadGate(h.withOffloadProvider(h.getOffloadJob)))
	api.HandleFunc("DELETE /api/v1/extensions/{id}/-/offload/{cid}/jobs/{job}", h.offloadGate(h.withOffloadProvider(h.deleteOffloadJob)))
}

// offloadTarget is the contribution a route is for, its program, and the computer it is about.
type offloadTarget struct {
	p  extension.DiskOffloadProvider
	hp *hubExtension
	m  *Machine
}

// offloadGate lets a device with full credentials through: the map lists what is on a computer and moves it.
func (h *Manager) offloadGate(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pr := principalOf(r.Context()); pr.Role != roleFull || pr.Extension != "" {
			h.writeOffloadErr(w, r, http.StatusForbidden, codeOffloadReadOnly, i18n.Err("hub-extension_offload.read_only"), nil)
			return
		}
		f(w, r)
	}
}

// offloadMachine is the computer ?machine= (or a run's body) names, by id or name.
func (h *Manager) offloadMachine(id string) *Machine {
	if id == "" {
		return nil
	}
	if m := h.machineByID(id); m != nil {
		return m
	}
	return h.machineByName(id)
}

// withOffloadProvider resolves {id}/{cid} to an enabled contribution whose program takes calls, and ?machine= to a
// computer (a run names it in its body, read by postOffloadRun).
func (h *Manager) withOffloadProvider(f func(http.ResponseWriter, *http.Request, offloadTarget)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.extensions == nil {
			h.writeOffloadErr(w, r, http.StatusNotFound, codeOffloadNotFound, i18n.Err("hub-extension_offload.no_provider"), nil)
			return
		}
		p, ok := h.extensions.reg.DiskOffloadProviderOf(r.PathValue("id"), r.PathValue("cid"))
		if !ok || !p.On {
			h.writeOffloadErr(w, r, http.StatusNotFound, codeOffloadNotFound, i18n.Err("hub-extension_offload.no_provider"), nil)
			return
		}
		hp := h.extensions.get(p.Extension)
		if hp == nil || hp.proc == nil || !programAnswers(hp.proc) {
			h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadPluginFailed, i18n.Err("hub-extension_offload.not_running", i18n.Args{"name": p.Name}), nil)
			return
		}
		t := offloadTarget{p: p, hp: hp}
		if r.Method != http.MethodPost {
			if t.m = h.offloadMachine(r.URL.Query().Get("machine")); t.m == nil {
				h.writeOffloadErr(w, r, http.StatusNotFound, codeOffloadNotFound, errNoMachine, nil)
				return
			}
		}
		f(w, r, t)
	}
}

// getOffloadProviders: GET /api/v1/extensions/-/offload?machine= → wire.OffloadProviders, the enabled contributions.
// No program is called; machine, when given, must be a computer of this hub.
func (h *Manager) getOffloadProviders(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("machine"); id != "" && h.offloadMachine(id) == nil {
		h.writeOffloadErr(w, r, http.StatusNotFound, codeOffloadNotFound, errNoMachine, nil)
		return
	}
	out := wire.OffloadProviders{Providers: []wire.OffloadProvider{}}
	if h.extensions != nil {
		for _, p := range h.extensions.reg.DiskOffloadProviders() {
			if p.On {
				out.Providers = append(out.Providers, wire.OffloadProvider{Extension: p.Extension, ID: p.Contribution, Name: p.Name,
					Kinds: p.Kinds, Restore: p.Restore})
			}
		}
	}
	writeJSON(w, out)
}

// offloadContext is what every call carries: the contribution, the caller's language and the computer.
func (h *Manager) offloadContext(r *http.Request, t offloadTarget) (string, string, wire.UIMachine) {
	t.m.mu.Lock()
	m := wire.UIMachine{ID: t.m.cfg.ID, Name: t.m.name(), OS: t.m.info.OS, Online: t.m.online}
	t.m.mu.Unlock()
	return t.p.Contribution, string(h.offloadLang(r)), m
}

// offloadLang is the caller's language: the request's, else the user's.
func (h *Manager) offloadLang(r *http.Request) i18n.Lang {
	if l := i18n.FromRequest(r); i18n.Valid(l) {
		return l
	}
	return h.contextLang(r.Context())
}

// getOffloadInventory: GET …/inventory?machine=&refresh=1 → wire.OffloadInventory.
func (h *Manager) getOffloadInventory(w http.ResponseWriter, r *http.Request, t offloadTarget) {
	cid, locale, m := h.offloadContext(r, t)
	refresh := r.URL.Query().Get("refresh")
	params := wire.OffloadInventoryParams{Contribution: cid, Locale: locale, Machine: m, Refresh: refresh == "1" || refresh == "true"}
	var out wire.OffloadInventory
	if !h.offloadCall(w, r, t, offloadInventoryTimeout, "offload.inventory", params, "OffloadInventory", &out) {
		return
	}
	writeJSON(w, withItems(out))
}

// postOffloadRun: POST …/run {machine, action, paths?, items?, dry_run?, confirm?, install?} → 200 wire.OffloadRunResult
// for a dry run, 202 wire.OffloadStarted when a job started.
func (h *Manager) postOffloadRun(w http.ResponseWriter, r *http.Request, t offloadTarget) {
	var in wire.OffloadRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, offloadBodyMost))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || !slices.Contains(wire.OffloadActions, in.Action) {
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.bad_request"), nil)
		return
	}
	list, other := in.Paths, in.Items
	if in.Action == wire.OffloadActionRestore {
		list, other = in.Items, in.Paths
		if !t.p.Restore {
			h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.no_restore", i18n.Args{"name": t.p.Name}), nil)
			return
		}
	}
	if !h.offloadList(w, r, list, other) {
		return
	}
	if !in.DryRun && !in.Confirm {
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadConfirmFirst, i18n.Err("hub-extension_offload.confirmation_required"), nil)
		return
	}
	if t.m = h.offloadMachine(in.Machine); t.m == nil {
		h.writeOffloadErr(w, r, http.StatusNotFound, codeOffloadNotFound, errNoMachine, nil)
		return
	}
	if in.Action == wire.OffloadActionOffload {
		checked, ok := h.offloadPrecheck(w, r, t, in.Paths)
		if !ok {
			return
		}
		in.Paths = checked // the program acts on the paths the computer's rules judged, not the client's spelling
	}
	cid, locale, m := h.offloadContext(r, t)
	params := wire.OffloadRunParams{Contribution: cid, Locale: locale, Machine: m, Action: in.Action, Paths: in.Paths, Items: in.Items,
		DryRun: in.DryRun, Install: in.Install && !in.DryRun, RequestID: offloadRequestID()}
	var out wire.OffloadRunResult
	if !h.offloadCall(w, r, t, offloadRunTimeout, "offload.run", params, "OffloadRunResult", &out) {
		return
	}
	switch out.Status {
	case wire.OffloadRunStatusNeedsInstall:
		if out.Tool == nil {
			h.badOffloadAnswer(w, r, t, "offload.run", errors.New("needs_install without the tool"))
			return
		}
		h.writeOffloadErr(w, r, http.StatusConflict, codeToolInstallRequired,
			i18n.Err("hub-extension_offload.tool_install_required", i18n.Args{"tool": offloadToolName(out.Tool), "name": m.Name}),
			map[string]any{"tool": out.Tool})
	case wire.OffloadRunStatusRefused:
		// refusal names the computer's own protection rules; a program does not speak for them.
		for i := range out.Items {
			out.Items[i].Refusal = ""
		}
		h.writeOffloadErr(w, r, http.StatusUnprocessableEntity, codeOffloadRefused, i18n.Err("hub-extension_offload.refused", i18n.Args{"name": t.p.Name}),
			map[string]any{"items": nonNilRunItems(out.Items)})
	case wire.OffloadRunStatusStarted:
		if in.DryRun || out.Job == "" {
			h.badOffloadAnswer(w, r, t, "offload.run", errors.New("started without a job, or for a dry run"))
			return
		}
		h.extensions.logf("plugin %s: offload.run %s on %s started: %d paths, %d items", t.p.Extension, in.Action, m.ID, len(in.Paths), len(in.Items))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, wire.OffloadStarted{Job: out.Job})
	case wire.OffloadRunStatusOk:
		if !in.DryRun {
			h.badOffloadAnswer(w, r, t, "offload.run", errors.New("ok for a run that is not a dry run"))
			return
		}
		out.Items = nonNilRunItems(out.Items)
		writeJSON(w, out)
	}
}

// getOffloadJob: GET …/jobs/{job}?machine= → wire.OffloadJob.
func (h *Manager) getOffloadJob(w http.ResponseWriter, r *http.Request, t offloadTarget) {
	job := r.PathValue("job")
	if !offloadJobID(job) {
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.bad_request"), nil)
		return
	}
	cid, locale, m := h.offloadContext(r, t)
	var out wire.OffloadJob
	if !h.offloadCall(w, r, t, offloadJobTimeout, "offload.job", wire.OffloadJobParams{Contribution: cid, Locale: locale, Machine: m, Job: job}, "OffloadJob", &out) {
		return
	}
	writeJSON(w, withItems(out))
}

// deleteOffloadJob: DELETE …/jobs/{job}?machine= → {canceled}: false when the provider cannot cancel (its program has
// no offload.cancel).
func (h *Manager) deleteOffloadJob(w http.ResponseWriter, r *http.Request, t offloadTarget) {
	job := r.PathValue("job")
	if !offloadJobID(job) {
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.bad_request"), nil)
		return
	}
	cid, locale, m := h.offloadContext(r, t)
	ctx, cancel := context.WithTimeout(r.Context(), offloadJobTimeout)
	defer cancel()
	_, err := buswire.Call(ctx, t.hp.proc, extwire.OffloadCancelMethod, wire.OffloadJobParams{Contribution: cid, Locale: locale, Machine: m, Job: job})
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == extension.CodeUnknownMethod {
		writeJSON(w, map[string]bool{"canceled": false})
		return
	}
	if err != nil {
		h.writeOffloadCallErr(w, r, t, ctx, extwire.MethodOffloadCancel, err)
		return
	}
	h.extensions.logf("plugin %s: offload.cancel on %s", t.p.Extension, m.ID)
	writeJSON(w, map[string]bool{"canceled": true})
}

// offloadList checks a run's paths or item ids: 1 to proto.StoragePathsMost, none empty, and nothing of the other
// kind.
func (h *Manager) offloadList(w http.ResponseWriter, r *http.Request, list, other []string) bool {
	switch {
	case len(list) == 0 || len(other) > 0:
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.need_items"), nil)
		return false
	case len(list) > proto.StoragePathsMost:
		h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.too_many", i18n.Args{"most": proto.StoragePathsMost}), nil)
		return false
	}
	for i, s := range list {
		if strings.TrimSpace(s) == "" || len(s) > 4096 || strings.ContainsFunc(s, unicode.IsControl) || slices.Contains(list[:i], s) {
			h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.bad_request"), nil)
			return false
		}
	}
	return true
}

// offloadPrecheck holds every path of an offload to the computer's protection rules (machine.storage.check, purpose
// offload) before the program hears of it: 422 offload_refused with the refused ones, 503 when the computer is offline,
// 409 when its node is too old to say. When all pass it returns the paths as the node resolved and judged them
// ("~" expanded, cleaned, the folders they are in followed through links), which are what the program is handed.
func (h *Manager) offloadPrecheck(w http.ResponseWriter, r *http.Request, t offloadTarget, paths []string) ([]string, bool) {
	t.m.mu.Lock()
	online, info := t.m.online && t.m.peer != nil, t.m.info
	t.m.mu.Unlock()
	if !online {
		h.writeOffloadErr(w, r, http.StatusServiceUnavailable, codeOffloadMachineOff, i18n.Err("hub-extension_offload.offline", i18n.Args{"name": t.m.name()}), nil)
		return nil, false
	}
	if !slices.Contains(info.Capabilities, nodeproto.MachineStorageUsage) {
		h.writeOffloadErr(w, r, http.StatusConflict, codeOffloadNodeOutdated, i18n.Err("hub-extension_offload.node_outdated"), nil)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), offloadCheckTimeout)
	defer cancel()
	var res proto.StorageCheckResult
	err := t.m.call(ctx, proto.MethodMachineStorageCheck, proto.StorageCheckParams{Paths: paths, Purpose: proto.StoragePurposeOffload}, &res)
	var re *rpc.Error
	errors.As(err, &re)
	switch {
	case err == nil:
	case errors.Is(err, errOffline):
		h.writeOffloadErr(w, r, http.StatusServiceUnavailable, codeOffloadMachineOff, i18n.Err("hub-extension_offload.offline", i18n.Args{"name": t.m.name()}), nil)
		return nil, false
	case re != nil && (re.Code == proto.CodeNodeOutdated || strings.HasPrefix(re.Message, "unknown method")):
		h.writeOffloadErr(w, r, http.StatusConflict, codeOffloadNodeOutdated, i18n.Err("hub-extension_offload.node_outdated"), nil)
		return nil, false
	case ctx.Err() != nil || re != nil && re.Message == context.DeadlineExceeded.Error():
		h.writeOffloadErr(w, r, http.StatusGatewayTimeout, codeOffloadDeadline, i18n.Err("hub-extension_offload.machine_deadline", i18n.Args{"name": t.m.name()}), nil)
		return nil, false
	default:
		h.extensions.logf("offload check on %s failed", t.m.cfg.ID)
		h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadCheckFailed, i18n.Err("hub-extension_offload.check_failed", i18n.Args{"name": t.m.name()}), nil)
		return nil, false
	}
	if len(res.Items) != len(paths) {
		h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadCheckFailed, i18n.Err("hub-extension_offload.check_failed", i18n.Args{"name": t.m.name()}), nil)
		return nil, false
	}
	l := h.offloadLang(r)
	var refused []wire.OffloadRunItem
	checked := make([]string, len(paths))
	for i, it := range res.Items {
		checked[i] = it.Path
		if it.OK && it.Path == "" {
			it.OK, it.Refusal = false, proto.RefusalNotFound // a node must say which path it judged
		}
		if it.OK {
			continue
		}
		code := it.Refusal
		if !slices.Contains(proto.StorageRefusals, code) {
			code = proto.RefusalOutsideHome
		}
		refused = append(refused, wire.OffloadRunItem{Path: paths[i], Refusal: code, Reason: i18n.T(l, "hub-extension_offload.refusal_"+code)})
	}
	if len(refused) > 0 {
		h.writeOffloadErr(w, r, http.StatusUnprocessableEntity, codeOffloadRefused, i18n.Err("hub-extension_offload.protected", i18n.Args{"count": len(refused)}),
			map[string]any{"items": refused})
		return nil, false
	}
	for i, p := range checked {
		if slices.Contains(checked[:i], p) { // two spellings of one path
			h.writeOffloadErr(w, r, http.StatusBadRequest, codeOffloadBadRequest, i18n.Err("hub-extension_offload.bad_request"), nil)
			return nil, false
		}
	}
	return checked, true
}

// offloadCall asks t's program within timeout and reads its answer into out after holding it to def
// (offload.schema.json); a failure is answered here, and false.
func (h *Manager) offloadCall(w http.ResponseWriter, r *http.Request, t offloadTarget, timeout time.Duration, method string, params any, def string, out any) bool {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var raw json.RawMessage
	if err := t.hp.proc.Call(ctx, method, params, &raw); err != nil {
		h.writeOffloadCallErr(w, r, t, ctx, method, err)
		return false
	}
	if err := wire.ValidateJSON(def, raw); err != nil {
		h.badOffloadAnswer(w, r, t, method, err)
		return false
	}
	if err := json.Unmarshal(raw, out); err != nil {
		h.badOffloadAnswer(w, r, t, method, err)
		return false
	}
	if inv, ok := out.(*wire.OffloadInventory); ok && len(inv.Items) > offloadInventoryItemMost {
		h.badOffloadAnswer(w, r, t, method, errors.New("more than 500 items"))
		return false
	}
	return true
}

// writeOffloadCallErr answers a failed call: 504 when the program took too long, 502 otherwise, with the provider's
// own words (one line, clipped) as the detail.
func (h *Manager) writeOffloadCallErr(w http.ResponseWriter, r *http.Request, t offloadTarget, ctx context.Context, method string, err error) {
	var re *rpc.Error
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		h.writeOffloadErr(w, r, http.StatusGatewayTimeout, codeOffloadDeadline, i18n.Err("hub-extension_offload.deadline", i18n.Args{"name": t.p.Name}), nil)
	case errors.Is(err, host.ErrNotRunning):
		h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadPluginFailed, i18n.Err("hub-extension_offload.not_running", i18n.Args{"name": t.p.Name}), nil)
	case errors.As(err, &re) && (re.Code == extension.CodeUnknownMethod || re.Code == extension.CodeNoContribution):
		h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadPluginFailed, i18n.Err("hub-extension_offload.not_provided", i18n.Args{"name": t.p.Name}), nil)
	default:
		why := err.Error()
		if re != nil {
			why = re.Message
		}
		h.extensions.logf("plugin %s: %s failed", t.p.Extension, method)
		h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadPluginFailed, i18n.Err("hub-extension_offload.failed", i18n.Args{"name": t.p.Name}),
			map[string]any{"detail": offloadWords(why)})
	}
}

// badOffloadAnswer logs an answer that broke the schema (the rule, never the answer) and fails the request with 502.
func (h *Manager) badOffloadAnswer(w http.ResponseWriter, r *http.Request, t offloadTarget, method string, err error) {
	h.extensions.logf("plugin %s: %s answer refused: %s", t.p.Extension, method, offloadWords(err.Error()))
	h.writeOffloadErr(w, r, http.StatusBadGateway, codeOffloadPluginFailed, i18n.Err("hub-extension_offload.bad_answer", i18n.Args{"name": t.p.Name}), nil)
}

// writeOffloadErr answers {error, code, …extra} with the error in the caller's language (wire.OffloadError).
func (h *Manager) writeOffloadErr(w http.ResponseWriter, r *http.Request, status int, code string, err error, extra map[string]any) {
	writeCode(w, status, code, i18n.Localize(err, h.offloadLang(r)), extra)
}

// offloadWords is a provider's words as an error's detail: one line with no control characters, clipped.
func offloadWords(s string) string {
	return providerWords(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s), offloadWordsMost)
}

// offloadToolName is what the install confirmation calls the tool.
func offloadToolName(t *wire.OffloadTool) string {
	if t.Title != "" {
		return t.Title
	}
	return t.Name
}

// offloadJobID is a job id as offload.run may answer one (offload.schema.json: 1 to 128 characters), without
// characters that do not belong in a path segment.
func offloadJobID(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '/' })
}

// offloadRequestID is a fresh run's id.
func offloadRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// withItems is v as JSON with an items list even when it has none (the generated types leave an empty one out), so
// the apps always read one.
func withItems(v any) any {
	raw, err := json.Marshal(v)
	var m map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return v
	}
	if _, ok := m["items"]; !ok {
		m["items"] = json.RawMessage("[]")
	}
	return m
}

func nonNilRunItems(items []wire.OffloadRunItem) []wire.OffloadRunItem {
	if items == nil {
		return []wire.OffloadRunItem{}
	}
	return items
}
