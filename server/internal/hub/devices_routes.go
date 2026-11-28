package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// awaitOwner is owner (ownership.go) after waiting for the manager the request goes to, as a call to one of its
// machines waits (waitDevicesFor): the hub just started and it is on its way (bounded); else the forwarding answers 503
// assistant_offline at once. False once it answered.
func (h *Hub) awaitOwner(w http.ResponseWriter, r *http.Request, by ownerOf) (string, bool) {
	id, ok := h.owner(w, r, by)
	if ok {
		_ = h.waitDevicesFor(r.Context(), id)
	}
	return id, ok
}

// forwardAwaited is forwardOwned (ownership.go) with awaitOwner's wait.
func (h *Hub) forwardAwaited(w http.ResponseWriter, r *http.Request, by ownerOf) {
	if id, ok := h.awaitOwner(w, r, by); ok {
		h.forwardHTTP(w, r, id)
	}
}

// metricsRoutesRelayed relays the monitoring's routes (devicewire.MetricsRoutes: the machines' samples, their history,
// the token usage) to the manager, which keeps them.
func (h *Hub) metricsRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.MetricsRoutes {
		by := h.byRequest
		if strings.Contains(pattern, "/machines/{id}/") {
			by = h.byMachine
		}
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { h.forwardAwaited(w, r, by) })
	}
}

// usageRoutesRelayed relays the subscription usage windows' route (devicewire.UsageRoutes) to the manager, which
// asks the machines it connects and the account-pool gateways (docs/server/usage-windows.md); 503 assistant_offline while
// it is away.
func (h *Hub) usageRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.UsageRoutes {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { h.forwardAwaited(w, r, h.byRequest) })
	}
}

// transferRoutesRelayed relays computer to computer file transfers (devicewire.TransferRoutes, docs/protocol/file-transfer.md)
// to the manager that carries the computers: it reads one and writes the other over the connections it holds. A
// POST goes to its source computer's manager, and both computers must be that manager's (a transfer between two
// managers' computers would need one to reach the other's); GET and DELETE go by the request (?manager=, which the
// file_transfer events carry for a manager other than the built-in). 503 assistant_offline while it is away.
func (h *Hub) transferRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.TransferRoutes {
		post := strings.HasPrefix(pattern, http.MethodPost+" ")
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			by := h.byRequest
			if post {
				var ok bool
				if by, ok = h.transferSource(w, r); !ok {
					return
				}
			}
			h.forwardAwaited(w, r, by)
		})
	}
}

// transferSource reads a transfer's POST body for its computers (put back for the manager) and answers which manager
// it goes to; false once it answered (a bad body, an unknown computer, two managers' computers).
func (h *Hub) transferSource(w http.ResponseWriter, r *http.Request) (ownerOf, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeBodyErr(w, err, err)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req proto.FileTransferRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("bad JSON: "+err.Error()))
		return nil, false
	}
	if h.localMachine(req.From.Machine) == nil || h.localMachine(req.To.Machine) == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
		return nil, false
	}
	owner := h.machineOwner(req.From.Machine)
	if h.machineOwner(req.To.Machine) != owner {
		writeErrCode(w, http.StatusConflict, "different_managers", errors.New("these two computers belong to different managers"))
		return nil, false
	}
	return func(r *http.Request) (string, error) { return h.extensionsOwn(r, owner) }, true
}

// adminUsageRoutesRelayed relays the admin UI's usage sources page (devicewire.AdminUsageRoutes) to the manager. mux
// is the admin's signed-in routes: the hub has checked the session (and the CSRF token of a write) before, and tells
// the manager so (assistantwire.AdminHeader); the apps' API has no such route.
func (h *Hub) adminUsageRoutesRelayed(mux *http.ServeMux) {
	for _, pattern := range devicewire.AdminUsageRoutes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if id, ok := h.awaitOwner(w, r, h.byRequest); ok { // the manager it names, else the default: its usage sources
				h.forwardAdminHTTP(w, r, id)
			}
		})
	}
}

// deviceRoutesRelayed relays the apps' requests on the routes that are the manager's (devicewire.Routes: its own
// settings of each machine and what only it can do) to it, as the assistant's (forwardHTTP); while it is not
// connected they are answered 503 assistant_offline.
func (h *Hub) deviceRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.Routes {
		fullOnly := pattern == linkSwitchRoute // not an app's pairing code standing in for one (link_switch.go)
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if pattern == devicewire.HeldOutboxRoute && !personSent(r.Context()) {
				writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("outbox.read_only"))
				return
			}
			if (pattern == devicewire.HeldStartsListRoute || pattern == devicewire.HeldStartRetryRoute || pattern == devicewire.HeldStartDeleteRoute) && !personSent(r.Context()) {
				writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-held-starts.read_only"))
				return
			}
			if fullOnly && !mayTrust(principalOf(r.Context())) {
				writeErrCode(w, http.StatusForbidden, "forbidden", i18n.Err("hub-link_switch.forbidden"))
				return
			}
			if h.localMachine(r.PathValue("mid")) == nil {
				writeErr(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
				return
			}
			h.forwardAwaited(w, r, h.byMachine)
		})
	}
}

// managerHTTP asks manager id (the machine's, for a machine's route) one of its routes (devicewire.Routes) as an app
// would, for the hub's own features (persona_settings); a status that is not 2xx is an error with the body's message.
func (h *Hub) managerHTTP(ctx context.Context, id, method, path string, body any, out any) error {
	req := assistantwire.HTTPRequest{Method: method, Path: path, Header: map[string][]string{"Content-Type": {"application/json"}}}
	req.Header[assistantwire.RoleHeader] = []string{"full"}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.Body = b
	}
	res, err := buswire.Call(ctx, h.managerCaller(id), assistantwire.HTTPMethod, req)
	if err != nil {
		if errors.Is(err, errBusOffline) {
			return errAssistantOffline
		}
		return err
	}
	if res.Status >= 300 {
		if why := apierr.Describe(res.Body); why != "" { // with the cause a localized summary moved to detail (#5728)
			return errors.New(why)
		}
		return fmt.Errorf("%s %s: %d", method, path, res.Status)
	}
	if out != nil {
		return json.Unmarshal(res.Body, out)
	}
	return nil
}

// claudeDirsState is what GET /api/v1/machines/{mid}/claude-dirs answers (the manager's, claudedirs.go there).
type claudeDirsState struct {
	Dirs    []proto.ClaudeDir `json:"dirs"`
	Blocked []string          `json:"blocked"`
	Home    string            `json:"home,omitempty"`
	Live    bool              `json:"live"`
}

func (m *Machine) claudeDirsState(ctx context.Context) claudeDirsState {
	var st claudeDirsState
	if err := m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodGet, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs", nil, &st); err != nil {
		m.hub.logf("[%s] claude dirs: %v", m.cfg.ID, err)
	}
	return st
}

// readBlockedClaudeDirs is claudeDirsState's Blocked with the error, for changing the list: a failed read is not
// "none blocked", or the PUT that follows would replace every pattern with just the one changed.
func (m *Machine) readBlockedClaudeDirs(ctx context.Context) ([]string, error) {
	var st claudeDirsState
	if err := m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodGet, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs", nil, &st); err != nil {
		return nil, fmt.Errorf("read blocked claude dirs: %w", err)
	}
	return st.Blocked, nil
}

func (m *Machine) setBlockedClaudeDirs(ctx context.Context, patterns []string) error {
	return m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodPut, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs/blocked",
		map[string]any{"blocked": patterns}, nil)
}
