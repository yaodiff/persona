package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// renameHub is a running hub (its manager has the entries) with link computer ren-a (named "A") and ren-b (no name),
// both in hub.json.
func renameHub(t *testing.T) *Hub {
	t.Helper()
	h := newTestHub(t)
	for _, mc := range []MachineConfig{{ID: "ren-a", Name: "A", Kind: kindLink, Icon: "desktop"}, {ID: "ren-b", Kind: kindLink}} {
		m := newMachine(h, mc)
		h.machines, h.byID[mc.ID] = append(h.machines, m), m
		h.cfg.Machines = append(h.cfg.Machines, mc)
	}
	b, _ := json.Marshal(map[string]any{"token": "t", "machines": []map[string]any{
		{"id": "ren-a", "name": "A", "kind": "link", "icon": "desktop"}, {"id": "ren-b", "kind": "link"}}})
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	h.devices.entries, h.devices.started = slices.Clone(h.cfg.Machines), true
	return h
}

func diskEntry(t *testing.T, h *Hub, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Machines []map[string]any `json:"machines"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Machines {
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("hub.json has no %s", id)
	return nil
}

func listedName(h *Hub, id string) string {
	for _, m := range h.machineList() {
		if m.ID == id {
			return m.Name
		}
	}
	return ""
}

// The admin page renames a computer: hub.json's entry keeps its other keys, the running hub takes the name at once
// (what it hands the manager, what the apps see, its own messages), and no restart is waiting; "" is its id again.
func TestAdminRenamesAComputer(t *testing.T) {
	h := renameHub(t)
	mux := http.NewServeMux() // the admin API behind its sign-in
	h.adminMachineRoutes(mux)
	code, out := call(t, mux, "PUT", "/api/v1/admin/machines/ren-a", h.cfg.Token, `{"name":"  87K 的主机 "}`)
	if code != 200 || out["name"] != "87K 的主机" {
		t.Fatalf("%d %v", code, out)
	}
	if e := diskEntry(t, h, "ren-a"); e["name"] != "87K 的主机" || e["icon"] != "desktop" || e["kind"] != "link" {
		t.Fatalf("hub.json entry %v", e)
	}
	if mc := h.cfgEntry("ren-a"); mc == nil || mc.Name != "87K 的主机" {
		t.Fatalf("the manager's entry %+v", mc)
	}
	if got := listedName(h, "ren-a"); got != "87K 的主机" || h.machineName("ren-a") != "87K 的主机" {
		t.Fatalf("shown as %q / %q", got, h.machineName("ren-a"))
	}
	if r := h.pendingRestart("en"); r.Required {
		t.Fatalf("a rename waits for a restart: %v", r.Reasons)
	}
	cfg := h.devicesConfig()
	if !strings.Contains(string(slices.Concat(cfg.Machines...)), `"name":"87K 的主机"`) {
		t.Fatalf("handed over %s", slices.Concat(cfg.Machines...))
	}

	code, out = call(t, mux, "PUT", "/api/v1/admin/machines/ren-a", h.cfg.Token, `{"name":""}`)
	if code != 200 || out["name"] != "ren-a" || diskEntry(t, h, "ren-a")["name"] != "ren-a" || listedName(h, "ren-a") != "ren-a" {
		t.Fatalf("back to its id: %d %v", code, out)
	}
	for _, body := range []string{`{"name":"` + strings.Repeat("长", 41) + `"}`, `{"name":"a\nb"}`, `{"name":"a\u0007b"}`} {
		if code, out := call(t, mux, "PUT", "/api/v1/admin/machines/ren-a", h.cfg.Token, body); code != 400 || out["code"] != "invalid_name" {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/admin/machines/nope", h.cfg.Token, `{"name":"x"}`); code != 404 {
		t.Fatalf("no such computer: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/admin/machines/ren-a", h.cfg.Token, `{}`); code != 400 {
		t.Fatalf("no name: %d", code)
	}
}

// A signed-in full device renames a computer through the apps' API; the feature says it can.
func TestDeviceRenamesAComputer(t *testing.T) {
	h := renameHub(t)
	if !slices.Contains(h.features(), featureMachineRename) {
		t.Fatal("the hub does not say it renames computers")
	}
	code, out := call(t, h.routes(), "PUT", "/api/v1/machines/ren-b/name", h.cfg.Token, `{"name":"NAS"}`)
	if code != 200 || out["name"] != "NAS" || listedName(h, "ren-b") != "NAS" {
		t.Fatalf("%d %v", code, out)
	}
}

func busRename(h *Hub, id, name string) (devicewire.RenameReply, error) {
	raw, _ := json.Marshal(devicewire.Rename{MachineID: id, Name: name})
	out, err := busMethods[devicewire.MethodRename](h, context.Background(), nil, raw)
	if err != nil {
		return devicewire.RenameReply{}, err
	}
	return out.(devicewire.RenameReply), nil
}

// A computer's Persona Link renames it through its manager: only a confirmed one, with the hub's own checks.
func TestLinkRenamesItsComputer(t *testing.T) {
	h := renameHub(t)
	raw := json.RawMessage(`{"machine_id":"ren-a","name":"Studio","future_field":true}`)
	got, err := busMethods[devicewire.MethodRename](h, t.Context(), nil, raw)
	if err != nil || listedName(h, "ren-a") != "Studio" {
		t.Fatalf("%+v %v", got, err)
	}
	if reply, err := json.Marshal(got); err != nil || string(reply) != `{"name":"Studio"}` {
		t.Fatalf("wire reply = %s, %v", reply, err)
	}
	var re *rpc.Error
	if _, err := busRename(h, "ren-a", strings.Repeat("x", 41)); !errors.As(err, &re) || re.Code != devicewire.RenameCodeInvalidName {
		t.Fatalf("too long: %v", err)
	}
	h.setPendingLocal("ren-b", time.Now(), true)
	if _, err := busRename(h, "ren-b", "Mine now"); !errors.As(err, &re) || re.Code != devicewire.RenameCodePending {
		t.Fatalf("a pending computer renamed itself: %v", err)
	}
	if diskEntry(t, h, "ren-b")["name"] != nil {
		t.Fatal("hub.json changed")
	}
}

// A computer that pairs with no name of its own is named after its system, once; a name it was given is kept.
func TestPairedComputerNamedAfterItsSystem(t *testing.T) {
	h := renameHub(t)
	h.nameAfterSystem(context.Background(), "ren-b", " 87K-itx ")
	h.nameAfterSystem(context.Background(), "ren-a", "Other")
	if listedName(h, "ren-b") != "87K-itx" || listedName(h, "ren-a") != "A" {
		t.Fatalf("ren-b %q ren-a %q", listedName(h, "ren-b"), listedName(h, "ren-a"))
	}
	h.nameAfterSystem(context.Background(), "ren-b", "Later")
	if listedName(h, "ren-b") != "87K-itx" {
		t.Fatal("named after its system again")
	}
}

// What the manager reports of a renamed computer of this hub's does not undo the rename before it caught up.
func TestReportBeforeTheManagerCaughtUp(t *testing.T) {
	h := renameHub(t)
	if _, err := busRename(h, "ren-a", "New"); err != nil {
		t.Fatal(err)
	}
	h.applyDeviceStates(devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "ren-a", Name: "A", Kind: kindLink}}}})
	if got := listedName(h, "ren-a"); got != "New" {
		t.Fatalf("shown as %q", got)
	}
}
