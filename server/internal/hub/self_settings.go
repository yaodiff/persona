package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The hub's own settings persona_settings offers (docs/assistant/assistant.md "Current structure", the "Control Persona
// itself" row): the tool is the assistant's (its own settings are its own: "About me", its launcher, …), which lists
// these (app.call settings.list) and hands the actions on them to the hub (app.tool persona_settings). One registry:
// each setting says what it is, how to read it and how a change is made. Reads are free; every change is a card on the
// phone that says what changes (before → after) and is applied only when approved. What the card needs filled in (a
// password) is typed on the phone and never passes through the model. Some things are never here: the hub's and
// devices' tokens, pairing, which machines the hub reaches, permission modes.

// selfSetting is one thing Persona may change. A value setting has set; a list has add / remove.
type selfSetting struct {
	key, title, about string
	value             string // what set / add take as value, for the model
	get               func(ctx context.Context) (string, error)
	set               func(ctx context.Context, v json.RawMessage) (*selfChange, error)
	add               func(ctx context.Context, v json.RawMessage) (*selfChange, error)
	remove            func(ctx context.Context, id string) (*selfChange, error)
}

// selfChange is a change waiting for the phone: what the card says, what it asks for, and how it is made.
type selfChange struct {
	summary string
	fields  []proto.Field
	apply   func(ctx context.Context, values map[string]string) (string, error)
}

func (s selfSetting) actions() []string {
	a := []string{"get"}
	if s.set != nil {
		a = append(a, "set")
	}
	if s.add != nil {
		a = append(a, "add")
	}
	if s.remove != nil {
		a = append(a, "remove")
	}
	return a
}

// selfSettings is the registry of the hub's own; the manager's (the inbox's rules, mail, the sources' own rules) are
// listed beside them (managerSettings).
func (h *Hub) selfSettings() []selfSetting {
	var out []selfSetting
	if h.store != nil {
		out = append(out, h.blockedClaudeDirsSetting())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func (h *Hub) toolPersonaSettings(ctx context.Context, from string, m *Machine, sessionID string, args json.RawMessage,
	report func(proto.MCPWaitKind)) (string, error) {
	var a struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
		ID     string          `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	all := h.selfSettings() // the list is the assistant's (it adds its own: settings.list)
	var s *selfSetting
	for i := range all {
		if all[i].key == a.Key {
			s = &all[i]
		}
	}
	if s == nil {
		for _, ms := range h.managerSettings(ctx, from) { // the caller's (mail, the sources' own rules): it does the change
			if ms.Key == a.Key {
				return h.managerSettingsTool(ctx, from, a.Action, map[string]any{"machine_id": m.cfg.ID, "session_id": sessionID,
					"arguments": args})
			}
		}
		return "", fmt.Errorf("no setting %q (action list shows them)", a.Key)
	}
	var change *selfChange
	var err error
	switch {
	case a.Action == "get":
		return s.get(ctx)
	case a.Action == "set" && s.set != nil:
		change, err = s.set(ctx, a.Value)
	case a.Action == "add" && s.add != nil:
		change, err = s.add(ctx, a.Value)
	case a.Action == "remove" && s.remove != nil:
		change, err = s.remove(ctx, a.ID)
	default:
		return "", fmt.Errorf("%s does not take %q (it takes %s)", s.key, a.Action, strings.Join(s.actions(), ", "))
	}
	if err != nil {
		return "", err
	}
	if change.apply == nil {
		return change.summary, nil // nothing to change
	}
	shown, _ := json.Marshal(map[string]string{"setting": s.title, "action": a.Action})
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: m.cfg.ID, SessionID: sessionID, Kind: proto.IxPermission,
		ToolName: "persona_settings", Summary: change.summary, ToolInput: shown, Fields: change.fields, SessionTitle: "Persona",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC()}
	r, err := h.waitMCPApproval(ctx, ix, report)
	if err != nil {
		return "", err
	}
	if !r.UserAllowed() { // only the user's own answer counts: an expired card, or one answered for them, is no decline (#3722)
		if r.UserDenied() {
			return "The user declined the change: nothing changed.", nil
		}
		return prompts.RenderSettingUnanswered(), nil
	}
	for _, f := range change.fields {
		if !f.Optional && strings.TrimSpace(r.Values[f.Key]) == "" {
			return "", fmt.Errorf("%s was left empty on the phone: nothing changed", f.Label)
		}
	}
	return change.apply(ctx, r.Values)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// managerSettingsTool hands persona_settings on one of manager id's own settings to it (extwire.HubSettingsTool) and
// answers what it says. A change is a card the manager shows, waits for (its own fifteen minutes) and then makes; the
// hub only passes the call on. So it waits as long as its caller does, not callBus's two minutes: those told the
// assistant the change had failed while the card stayed on the phone and was still applied when approved later
// (#5907). When the caller stops waiting, so does the manager, which withdraws its card (serveBus's Cancels). Reading
// (get) shows no card and keeps the two minutes.
func (h *Hub) managerSettingsTool(ctx context.Context, id, action string, params any) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	call := h.callBus
	if action != "get" {
		call = h.callBusAsIs
	}
	var res struct {
		Text string `json:"text"`
	}
	if err := call(ctx, id, extwire.MethodHubCall, extwire.HubCall{Method: extwire.HubSettingsTool, Params: raw}, &res); err != nil {
		return "", err
	}
	return res.Text, nil
}

// managerSettings are the manager's persona_settings entries (none while it is away).
func (h *Hub) managerSettings(ctx context.Context, id string) []extwire.Setting {
	if h.busConnOf(id) == nil {
		return nil
	}
	var res struct {
		Settings []extwire.Setting `json:"settings"`
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.extCallTo(ctx, id, extwire.HubSettingsList, map[string]any{}, &res); err != nil {
		h.logf("settings: the manager's: %v", err)
	}
	return res.Settings
}
