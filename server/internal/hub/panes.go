package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// A terminal addressed by pane ("pane:<id>", docs/integrations/terminal.md "Standalone terminal") is a shell on the computer, agent or not.
// Only a full device may open one: the streams of agent sessions are watchable by an answer-only device (the watch
// shows what it is asked about), a shell's screen is more than that. The same goes for tokens an extension holds
// (agents.read opens an agent's screen, not a shell's).

// paneAddress: the session id of a stream is a pane address.
func paneAddress(id string) bool { return strings.HasPrefix(id, "pane:") }

// paneAddressFullOnly guards the HTTP screen reads, which take a session id: a pane address needs a full device.
func paneAddressFullOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if paneAddress(r.PathValue("sid")) && principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("a terminal opened by pane needs a full device"))
			return
		}
		next(w, r)
	}
}

// newShell opens a shell tab in one of a machine's workspaces. Only the user's click gets here. The node starts it in the
// workspace's own directory, or with cwd (the file page's Open Terminal Here, #5832) in that folder: a folder the read
// guard lets the device see, handed to the host as the tab's starting directory and never typed into the shell, so a
// request still cannot run a command. A node without the pane_cwd capability gets no cwd: 409 node_outdated instead.
// new_space (#6362: New Terminal on a computer with no workspace yet) asks the node to open a workspace in cwd; a node
// without pane_new_space would answer "no workspace", so it gets the same 409.
func (h *Hub) newShell(w http.ResponseWriter, r *http.Request, m *Machine) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("opening a terminal needs a full device"))
		return
	}
	var body struct {
		Kind        string `json:"kind"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		Cwd         string `json:"cwd,omitempty"`
		NewSpace    bool   `json:"new_space,omitempty"`
	}
	if err := decodeJSONBody(w, r, &body, 4<<10); err != nil || (body.WorkspaceID == "" && body.Cwd == "") {
		writeBodyErr(w, err, errors.New("workspace_id is required"))
		return
	}
	if (body.Cwd != "" && !m.hasCapability(nodeproto.PaneCwd)) || (body.NewSpace && !m.hasCapability(nodeproto.PaneNewSpace)) {
		writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-fileedit.outdated_feature", i18n.Args{"name": m.cfg.Name}), nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out json.RawMessage
	if err := m.call(ctx, "panes.new", body, &out); err != nil {
		callErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(out)
}

// The terminal browser's and the sidebar's context menus change things on a computer (docs/integrations/terminal.md "Node menus"):
// close a tab or a pane, split a pane, rename a tab, run an agent in a plain shell pane, fetch / pull / push. Each is
// a route of its own that a full device alone may call; an answer-only device (the watch) and the tokens an extension
// holds are refused here and by the lists of what they may call (devicetokens.go, extension_scopes.go). The body is
// read field by field: only the named string fields reach the node, so a request cannot name a directory or a command.

type nodeAction struct {
	method   string
	timeout  time.Duration
	required []string
	optional []string
	flags    []string // booleans, passed only when true
	// Fields required unless the named boolean flag is true. Unknown/string flags never bypass validation.
	requiredUnless map[string][]string
	strictFlags    []string
	strictStrings  []string
	anyUnless      []string // any of these true flags bypasses requiredUnless for alternate request modes
}

func (h *Hub) nodeAction(a nodeAction) http.HandlerFunc {
	return h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("changing a computer's terminals needs a full device"))
			return
		}
		var body map[string]any
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil {
			writeBodyErr(w, err, errors.New("bad request body"))
			return
		}
		for _, flag := range a.strictFlags {
			if v, present := body[flag]; present {
				if _, ok := v.(bool); !ok {
					writeErr(w, http.StatusBadRequest, i18n.Err("worktree-composer.mode_invalid"))
					return
				}
			}
		}
		for _, field := range a.strictStrings {
			if v, present := body[field]; present {
				if _, ok := v.(string); !ok {
					writeErr(w, http.StatusBadRequest, i18n.Err("worktree-composer.field_invalid"))
					return
				}
			}
		}
		params := map[string]any{}
		for _, k := range append(append([]string{}, a.required...), a.optional...) {
			if v, ok := body[k].(string); ok && v != "" {
				params[k] = v
			}
		}
		for _, k := range a.flags {
			if v, ok := body[k].(bool); ok && v {
				params[k] = true
			}
		}
		// What the node writes for people (its worktree refusals) is in the request's language: the client's locale
		// when it sent one, else its Accept-Language, else the user's (locale.go).
		if slices.Contains(a.optional, "locale") && params["locale"] == nil {
			params["locale"] = string(h.requestLang(r))
		}
		for _, k := range a.required {
			if params[k] == nil {
				writeErr(w, http.StatusBadRequest, errors.New(k+" is required"))
				return
			}
		}
		for flag, fields := range a.requiredUnless {
			if params[flag] == true || anyActionFlag(params, a.anyUnless) {
				continue
			}
			for _, k := range fields {
				if params[k] == nil {
					writeErr(w, http.StatusBadRequest, errors.New(k+" is required"))
					return
				}
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
		defer cancel()
		var out json.RawMessage
		if err := m.call(ctx, a.method, params, &out); err != nil {
			callErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if len(out) == 0 {
			out = json.RawMessage(`{}`)
		}
		_, _ = w.Write(out)
	})
}

func anyActionFlag(params map[string]any, flags []string) bool {
	for _, flag := range flags {
		if params[flag] == true {
			return true
		}
	}
	return false
}

// nodeActions are the routes of those menu items, by path under /api/v1/machines/{mid}/.
var nodeActions = map[string]nodeAction{
	"panes/close": {method: "panes.close", timeout: 30 * time.Second, required: []string{"pane"}},
	// #4754 "Close finished terminals": the terminals stopped agents left, those the user confirmed (panes, comma-separated);
	// preview lists them and closes nothing.
	"panes/close_finished": {method: "panes.close_finished", timeout: 60 * time.Second, optional: []string{"panes"}, flags: []string{"preview"}},
	"panes/split":          {method: "panes.split", timeout: 30 * time.Second, required: []string{"pane", "direction"}},
	"panes/agent":          {method: "panes.agent", timeout: 120 * time.Second, required: []string{"pane", "launcher"}},
	"panes/git":            {method: "panes.git", timeout: 90 * time.Second, required: []string{"op"}, optional: []string{"pane", "workspace_id", "kind"}},
	"tabs/close":           {method: "tabs.close", timeout: 30 * time.Second, required: []string{"tab_id"}, optional: []string{"kind"}},
	"tabs/rename":          {method: "tabs.rename", timeout: 30 * time.Second, required: []string{"tab_id", "label"}, optional: []string{"kind"}},
	// #2431 "New worktree…": the repository of a workspace the host lists, and a Git worktree of it opened as a workspace.
	// The branch and the path are typed in the dialog; the node refuses a path that holds something and never forces.
	"worktree/prepare": {method: "worktree.prepare", timeout: 30 * time.Second, required: []string{"workspace_id"}, optional: []string{"kind", "locale"}, flags: []string{"composer"}, strictFlags: []string{"composer"}},
	"worktree/rename":  {method: "worktree.rename", timeout: 30 * time.Second, required: []string{"workspace_id", "name"}, optional: []string{"kind", "locale"}},
	"worktree/create": {method: "worktree.create", timeout: 150 * time.Second, required: []string{"workspace_id"},
		optional: []string{"kind", "parent_workspace_id", "branch", "path", "name", "branch_override", "base_ref", "note", "sparse_preset_id", "source_id", "source_url", "locale"}, flags: []string{"auto_name", "composer", "no_parent"}, anyUnless: []string{"composer"}, strictFlags: []string{"composer", "no_parent"}, strictStrings: []string{"name", "branch_override", "base_ref", "parent_workspace_id", "note", "sparse_preset_id", "source_id", "source_url"},
		requiredUnless: map[string][]string{"auto_name": {"branch", "path"}}},
	"worktree/source": {method: "worktree.source", timeout: 35 * time.Second, required: []string{"workspace_id", "kind"}, optional: []string{"query", "locale"}},
	"worktree/preset": {method: "worktree.preset", timeout: 30 * time.Second, required: []string{"workspace_id", "kind", "name", "directories"}, optional: []string{"locale"}},
	"worktree/delete": {method: "worktree.delete", timeout: 150 * time.Second, required: []string{"workspace_id", "kind"}, optional: []string{"preview_token", "locale"}, flags: []string{"preview", "reconcile", "force", "delete_branch", "close_only"}, strictFlags: []string{"preview", "reconcile", "force", "delete_branch", "close_only"}},
	// #6115 a new agent in a worktree of its own: made from its folder, told apart, removed when it is closed and the
	// user says so (the node refuses uncommitted work and unmerged branches unless force, which the user confirms).
	"worktree/add":    {method: "worktree.add", timeout: 150 * time.Second, required: []string{"cwd"}, optional: []string{"branch", "name_hint", "kind", "locale"}},
	"worktree/status": {method: "worktree.status", timeout: 30 * time.Second, required: []string{"cwd"}, optional: []string{"kind"}},
	"worktree/remove": {method: "worktree.remove", timeout: 90 * time.Second, required: []string{"path"}, optional: []string{"kind"}, flags: []string{"delete_branch", "force"}},
}

// paneRoutes are the terminal browser's (panes, tabs) and the "New worktree…" dialog's.
func (h *Hub) paneRoutes(api *http.ServeMux) {
	// The terminal browser (docs/integrations/terminal.md "Standalone terminal"): a machine's workspaces, tabs and panes with plain shells, and
	// "New terminal". A pane is full shell access on the computer: full devices only (allowed in devicetokens.go).
	api.HandleFunc("GET /api/v1/machines/{mid}/panes", h.proxy("panes.list", nil))
	api.HandleFunc("POST /api/v1/machines/{mid}/panes", h.withMachine(h.newShell))
	// close / split / run an agent / git in a pane, close / rename a tab: full devices only (panes.go)
	api.HandleFunc("POST /api/v1/machines/{mid}/panes/close", h.nodeAction(nodeActions["panes/close"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/panes/close_finished", h.nodeAction(nodeActions["panes/close_finished"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/panes/split", h.nodeAction(nodeActions["panes/split"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/panes/agent", h.nodeAction(nodeActions["panes/agent"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/panes/git", h.nodeAction(nodeActions["panes/git"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/tabs/close", h.nodeAction(nodeActions["tabs/close"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/tabs/rename", h.nodeAction(nodeActions["tabs/rename"]))
	// #2431 "New worktree…": what the dialog starts from, and the worktree (full devices only, panes.go)
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/prepare", h.nodeAction(nodeActions["worktree/prepare"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/create", h.withClientOp("worktree_create", h.nodeAction(nodeActions["worktree/create"])))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/source", h.nodeAction(nodeActions["worktree/source"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/preset", h.nodeAction(nodeActions["worktree/preset"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/delete", h.nodeAction(nodeActions["worktree/delete"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/rename", h.nodeAction(nodeActions["worktree/rename"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/add", h.nodeAction(nodeActions["worktree/add"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/status", h.nodeAction(nodeActions["worktree/status"]))
	api.HandleFunc("POST /api/v1/machines/{mid}/worktree/remove", h.nodeAction(nodeActions["worktree/remove"]))
}
