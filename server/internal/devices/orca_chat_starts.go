package devices

// "Start new Agents as Orca chats" (#17650, docs/integrations/engines.md "Orca chats as New Agent's default"): a switch
// per computer, off until the user turns it on. With it on, the manager sends a person's New Agent start that keeps
// the default Claude Code or Codex launcher to the computer's Orca chat launcher (assistant/orca_chat_default.go); the
// assistant's own starts never go there. The manager keeps the switches in settings (machine_orca_chat_starts) and
// answers GET / PUT /api/v1/machines/{mid}/orca-chat-starts. The machine on the wire carries it (orca_chat_starts)
// only while Orca is in its engine set, where it applies, so the apps show it exactly there.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaChatStartsKey is the settings key holding map[machineID]bool: the computers the user turned the switch on for.
const orcaChatStartsKey = "machine_orca_chat_starts"

// orcaChatSwitches caches orcaChatStartsKey, loaded on first use (machine lists read it on every report), trusted for
// fileTransferFresh as the file transfer switches are: another manager process on the same settings (a blue-green
// switch) may have changed one.
type orcaChatSwitches struct {
	mu       sync.Mutex
	loadedAt time.Time // zero: never loaded
	failedAt time.Time
	on       map[string]bool
}

// orcaChatStartsOn is whether the user turned the switch on for machine; off when never set, and off while the
// settings cannot be read (starts then stay where they always went).
func (d *Devices) orcaChatStartsOn(machine string) bool {
	c := &d.orcaChats
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.loadedAt) >= fileTransferFresh && (c.failedAt.IsZero() || time.Since(c.failedAt) >= engineRetry) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.loadOrcaChatsLocked(ctx); err != nil {
			d.logf("orca chat start switches: %v (retried in %s)", err, engineRetry)
			c.failedAt, c.on = time.Now(), nil
		}
	}
	return c.on[machine]
}

func (d *Devices) loadOrcaChatsLocked(ctx context.Context) error {
	c := &d.orcaChats
	on := map[string]bool{}
	if d.opts.Store != nil {
		if _, err := d.opts.Store.Setting(ctx, orcaChatStartsKey, &on); err != nil {
			return err
		}
	}
	c.on, c.loadedAt, c.failedAt = on, time.Now(), time.Time{}
	return nil
}

// setOrcaChatStarts stores machine's switch: read, changed and written under the lock, so two switches flipped at once
// both stay.
func (d *Devices) setOrcaChatStarts(ctx context.Context, machine string, on bool) error {
	if d.opts.Store == nil {
		return i18n.Err("devices-api.no_store")
	}
	c := &d.orcaChats
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := d.loadOrcaChatsLocked(ctx); err != nil {
		return err
	}
	next := make(map[string]bool, len(c.on)+1)
	for id, v := range c.on {
		next[id] = v
	}
	if on {
		next[machine] = true
	} else {
		delete(next, machine)
	}
	if err := d.opts.Store.PutSetting(ctx, orcaChatStartsKey, next); err != nil {
		return err
	}
	c.on = next
	return nil
}

// OrcaChatStarts is whether a person's New Agent starts on machine id go to its Orca chat launchers (the assistant's
// routing, assistant/orca_chat_default.go): the switch is on and Orca is in the computer's engine set. False for an
// unknown machine.
func (d *Devices) OrcaChatStarts(_ context.Context, id string) bool {
	m := d.machineByID(id)
	if m == nil {
		return false
	}
	return orcaChatStartsField(d.engineChoiceOf(m.cfg.ID), d.orcaChatStartsOn(m.cfg.ID)) == proto.OrcaChatStartsOn
}

// orcaChatStartsField is proto.Machine.OrcaChatStarts: the switch, on or off, while Orca is in the computer's engine
// set (where it applies); absent ("") otherwise.
func orcaChatStartsField(choice proto.EngineChoice, on bool) proto.OrcaChatStarts {
	if !slices.Contains(choice.Hosts, proto.EngineOrca) {
		return ""
	}
	return proto.OrcaChatStartsOf(on)
}

// orcaChatStartsState is what GET / PUT /api/v1/machines/{mid}/orca-chat-starts answer: the switch, and whether it
// applies now (Orca is in the computer's engine set).
type orcaChatStartsState struct {
	On      bool `json:"on"`
	Applies bool `json:"applies"`
}

func (d *Devices) orcaChatStartsState(m *Machine) orcaChatStartsState {
	on := d.orcaChatStartsOn(m.cfg.ID)
	return orcaChatStartsState{On: on, Applies: orcaChatStartsField(d.engineChoiceOf(m.cfg.ID), on) != ""}
}

// getOrcaChatStarts: GET /api/v1/machines/{mid}/orca-chat-starts → {on, applies}.
func (d *Devices) getOrcaChatStarts(w http.ResponseWriter, r *http.Request, m *Machine) {
	writeJSON(w, d.orcaChatStartsState(m))
}

// putOrcaChatStarts: PUT /api/v1/machines/{mid}/orca-chat-starts {"on": bool} → {on, applies}. Only a full device
// changes it; stored while the computer is offline too, and while Orca is not in its engine set (it applies once it is).
func (d *Devices) putOrcaChatStarts(w http.ResponseWriter, r *http.Request, m *Machine) {
	if !fullRole(r) {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-api.orca_chat_starts_read_only"))
		return
	}
	var in struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || in.On == nil {
		writeErrCode(w, http.StatusBadRequest, "bad_request", i18n.Err("devices-api.need_on"))
		return
	}
	if err := d.setOrcaChatStarts(r.Context(), m.cfg.ID, *in.On); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	d.logf("[%s] orca chat starts turned %s", m.cfg.ID, map[bool]string{true: "on", false: "off"}[*in.On])
	d.changed() // the machine list carries the switch
	writeJSON(w, d.orcaChatStartsState(m))
}
