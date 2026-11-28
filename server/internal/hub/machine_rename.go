package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Renaming a computer (docs/server/machines.md "Renaming a computer"): the admin page (PUT /api/v1/admin/machines/{id}),
// a signed-in full device (PUT /api/v1/machines/{id}/name) and the computer's own Persona Link (devices.rename, from
// its link) give it another name. The name is hub.json's: written there, then taken by the running hub at once, with
// no restart and no reconnect: the entries the manager is handed (it keeps the machine and its connection), the
// machines the apps see (a machines event), the mesh record. The id never changes: everything kept of the computer
// (pins, mutes, trust, credentials) is by id. An empty name is the id again.

// featureMachineRename: this hub renames computers (the routes above, and a link's rename).
const featureMachineRename = "machine_rename"

// errNoMachine: neither hub.json nor a manager's own list has that id (a member's computer is renamed on its own hub).
var errNoMachine = refuseAdd(http.StatusNotFound, "not_found", i18n.Err("hub-machine_trust.no_machine"))

// renameMachine gives computer id the name (trimmed; "" is its id again), in hub.json and in the running hub, and
// says the name it has now. by is who renamed it (the audit's actor), via how (admin | device | link | system).
func (h *Hub) renameMachine(ctx context.Context, id, name, by, via string) (string, error) {
	name = strings.TrimSpace(name)
	if err := checkMachineName(name); err != nil {
		return "", err
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", refuseAdd(http.StatusBadRequest, "invalid_name", i18n.Err("hub-admin_machines.invalid_name"))
	}
	shown := cmp.Or(name, id)
	h.machineAdd.Lock() // as an add: the entry is read and written whole
	defer h.machineAdd.Unlock()
	was := ""
	if mc := h.knownEntry(id); mc != nil {
		was = cmp.Or(mc.Name, id)
	}
	// One its manager keeps in its own list (machines_kept.go): renamed there, which keeps its connection as well.
	if kept, err := h.editKeptMachine(ctx, id, map[string]any{"name": shown}); kept {
		if err != nil {
			status, code := keeperErr(err)
			return "", refuseAdd(status, code, err)
		}
		return h.renamed(ctx, id, was, shown, by, via), nil
	}
	found := false
	err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
		list, err := machineEntries(o)
		if err != nil {
			return err
		}
		for _, raw := range list {
			if machineEntryID(raw) == id {
				var mc MachineConfig
				_ = json.Unmarshal(raw, &mc)
				was, found = cmp.Or(mc.Name, id), true
			}
		}
		switch {
		case !found:
			return errNoMachine
		case was == shown:
			return nil
		}
		return setMachineField(o, id, "name", shown)
	})
	if err != nil {
		return "", err
	}
	return h.renamed(ctx, id, was, shown, by, via), nil
}

// renamed takes computer id's new name (from was, as its entry has it now) into the running hub and the audit; shown
// is the name.
func (h *Hub) renamed(ctx context.Context, id, was, shown, by, via string) string {
	if was == shown {
		return shown
	}
	detail, _ := json.Marshal(map[string]string{"from": was, "to": shown, "via": via})
	h.machineAudit(ctx, id, "rename", by, detail)
	h.logf("[%s] renamed by %s (%s)", id, by, via)
	h.renameMachineLive(id, shown)
	return shown
}

// renameMachineLive takes a computer's new name into the running hub: the entry it hands the manager (which keeps the
// machine and its connection: only the name changed) and the machine the apps see. A name is no change waiting for a
// restart (pendingRestart).
func (h *Hub) renameMachineLive(id, name string) {
	h.editEntryLive(id, func(mc *MachineConfig) { mc.Name = name })
	if m := h.localMachine(id); m != nil {
		m.setName(name)
	}
	h.devicesConfigChanged()
	h.broadcastMachines() // and the mesh record
}

// editEntryLive changes computer id's entry the running hub hands its manager (renameMachineLive, link_switch.go
// kindLive); the caller hands it over (devicesConfigChanged).
func (h *Hub) editEntryLive(id string, edit func(*MachineConfig)) {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.entries {
		if d.entries[i].ID == id {
			edit(&d.entries[i])
		}
	}
}

// nameAfterSystem names a computer that has just paired its Persona Link after what its system calls it, when it has
// no name of its own yet (added without one: its id); a name it was given is kept, and so is the name of one switching
// from ssh (link_switch.go: it is the same computer, as the user knows it).
func (h *Hub) nameAfterSystem(ctx context.Context, id, system string) {
	if switching, err := h.linkSwitchUnderWay(ctx, id); switching || err != nil {
		return
	}
	system = strings.Join(strings.Fields(system), " ")
	if r := []rune(system); len(r) > maxMachineName {
		system = string(r[:maxMachineName])
	}
	mc := h.knownEntry(id)
	if system == "" || mc == nil || cmp.Or(mc.Name, id) != id {
		return
	}
	if _, err := h.renameMachine(ctx, id, system, "link:"+id, "system"); err != nil {
		h.logf("[%s] its system's name: %v", id, err)
	}
}

// mayRename: whether a computer may be renamed by p's request (a full device, not a pairing code's stand-in, or the
// master token), as for confirming one.
func mayRename(p principal) bool { return mayTrust(p) }

// machineRenameRoutes: a signed-in full device renames a computer (the apps' machine details).
func (h *Hub) machineRenameRoutes(api *http.ServeMux) {
	api.HandleFunc("PUT /api/v1/machines/{id}/name", func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if !mayRename(p) {
			writeErrCode(w, http.StatusForbidden, "forbidden", i18n.Err("hub-machine_rename.forbidden"))
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		name, err := h.renameMachine(r.Context(), r.PathValue("id"), in.Name, trustActor(p), "device")
		if err != nil {
			writeAddMachineErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "name": name})
	})
}

func init() {
	// A computer's Persona Link asks for another name (persona-link name, the tray, the menu bar). Only its manager
	// speaks for it, and only once the user confirmed it: a computer waiting to be confirmed cannot pass itself off as
	// another by its name.
	handleTypedBus(devicewire.RenameMethod, devicewire.MethodRename+": ", func(h *Hub, ctx context.Context, c *busConn, p devicewire.Rename) (devicewire.RenameReply, error) {
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return devicewire.RenameReply{}, err
		}
		if h.machineUntrusted(p.MachineID) {
			return devicewire.RenameReply{}, &rpc.Error{Code: devicewire.RenameCodePending, Message: i18n.T(h.userLang(), "hub-machine_rename.pending")}
		}
		name, err := h.renameMachine(ctx, p.MachineID, p.Name, "link:"+p.MachineID, "link")
		var e *addMachineError
		switch {
		case errors.As(err, &e):
			return devicewire.RenameReply{}, &rpc.Error{Code: e.code, Message: i18n.Localize(e.err, h.userLang())}
		case err != nil:
			return devicewire.RenameReply{}, err
		}
		return devicewire.RenameReply{Name: name}, nil
	})
}
