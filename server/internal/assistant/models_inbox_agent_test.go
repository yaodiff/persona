package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTraexIsOfferedForTerminalAgentsAndInboxExecution(t *testing.T) {
	s, f, _ := modelService(t)
	f.inbox = newFakeInbox()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "traex", Agent: "traex", Source: "zshrc"}}}
	f.agentModels = map[string][]proto.AgentModel{"traex": {{ID: "example-fast", Name: "Example Fast", Efforts: []string{"xhigh"}}}}

	if p := purposeOf(t, s, "agent.new"); !slices.Contains(p.ProviderIDs, "launcher.traex") {
		t.Fatalf("terminal provider missing: %+v", p)
	}
	if p := purposeOf(t, s, purposeAgent); !slices.Contains(p.ProviderIDs, "launcher.traex") {
		t.Fatalf("TRAE CLI missing for Inbox execution: %+v", p)
	}
	w := request(s, http.MethodGet, "/api/v1/model-config/providers/launcher.traex/models", "")
	var catalog proto.LauncherModels
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || catalog.Agent != "traex" ||
		len(catalog.Models) != 1 || catalog.Models[0].ID != "example-fast" || !slices.Equal(catalog.Models[0].Efforts, []string{"xhigh"}) {
		t.Fatalf("provider catalog: %d %s", w.Code, w.Body.String())
	}
	body := `{"revision":0,"provider":"launcher.traex","model":"example-fast","effort":"xhigh"}`
	if w := modelRequest(s, http.MethodPatch, purposeAgent, body); w.Code != http.StatusOK {
		t.Fatalf("Inbox write: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "agent.new", body); w.Code != http.StatusOK {
		t.Fatalf("terminal write: %d %s", w.Code, w.Body.String())
	}
	if p := purposeOf(t, s, "agent.new"); p.Effective.Model != "example-fast" || p.Effective.Effort != "xhigh" {
		t.Fatalf("terminal configuration did not keep picker values: %+v", p.Effective)
	}
	params, err := s.AgentStartParams(context.Background(), AgentStartParams{MachineID: "server"})
	if err != nil || params["launcher"] != "traex" || params["model"] != "example-fast" || params["effort"] != "xhigh" {
		t.Fatalf("saved terminal configuration did not reach session.start: %+v, %v", params, err)
	}
	if p := purposeOf(t, s, purposeAgent); p.Effective.Model != "example-fast" || p.Effective.Effort != "xhigh" {
		t.Fatalf("Inbox configuration did not keep picker values: %+v", p)
	}
	for _, unsupported := range []string{proto.ModePlan, "unknown"} {
		body := fmt.Sprintf(`{"revision":1,"permission_mode":%q}`, unsupported)
		if w := modelRequest(s, http.MethodPatch, purposeAgent, body); w.Code != http.StatusBadRequest {
			t.Fatalf("Inbox %s write: %d %s", unsupported, w.Code, w.Body.String())
		}
	}
	for revision, mode := range []string{proto.ModeDefault, "acceptEdits", "auto", proto.ModeBypass} {
		body := fmt.Sprintf(`{"revision":%d,"permission_mode":%q}`, revision+1, mode)
		if w := modelRequest(s, http.MethodPatch, purposeAgent, body); w.Code != http.StatusOK {
			t.Fatalf("Inbox %s write: %d %s", mode, w.Code, w.Body.String())
		}
		if got := purposeOf(t, s, purposeAgent).Effective.PermissionMode; got != mode {
			t.Fatalf("Inbox mode = %q, want %q", got, mode)
		}
	}
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":1,"permission_mode":"default"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("terminal TRAE permission write: %d %s", w.Code, w.Body.String())
	}
	it := &InboxItem{ID: "event-traex", Title: "Fixture", Agent: &InboxAgent{State: AgentRunning, ProcessingTurn: "traex-model"}}
	s.inbox.mu.Lock()
	s.inbox.items = map[string]*InboxItem{it.ID: it}
	s.inbox.mu.Unlock()
	before := len(f.sends)
	if _, _, err := s.startInboxAgent(context.Background(), InboxAgentConfig{Enabled: true}, it, "TRAE Inbox fixture"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.sends) <= before {
		f.mu.Unlock()
		t.Fatal("Inbox dispatch did not reach the node")
	}
	params = f.sends[before]
	f.mu.Unlock()
	for key, want := range map[string]any{"launcher": "traex", "model": "example-fast", "effort": "xhigh", "origin": OriginInbox, "origin_known": true, "inbox_permission_mode": proto.ModeBypass} {
		if params[key] != want {
			t.Errorf("assistant.send %s = %#v, want %#v", key, params[key], want)
		}
	}
}

// The AI twin's opt-in override (#4232), checked through the twin's own start (startInboxAgent) and the parameters the
// node is started with, not only through resolution.
func TestInboxAgentModelOverrideAndReset(t *testing.T) {
	s, f, st := modelService(t)
	f.inbox = newFakeInbox()
	ctx := context.Background()
	inherited := ModelBinding{Provider: "launcher.claude", Model: "agents-default", Effort: "medium"}
	setModel(t, s, "agent.new", inherited)
	// What the twin had before category-only settings: still there, never read or copied.
	legacy := modelRecord{Revision: 42, Binding: ModelBinding{Provider: "launcher.retired", Model: "retired", Effort: "low"}}
	if err := st.PutSetting(ctx, modelKey(purposeAgent), legacy); err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "twin-fast", Model: "provider-default"}})
	// Lark /agent set apart: the twin neither follows nor changes it.
	lark := ModelBinding{Provider: "launcher.claude", Model: "lark-selected", Effort: "high"}
	setModel(t, s, purposeLarkAgent, lark)

	assertState := func(want ModelBinding, revision int64, overridden bool) {
		t.Helper()
		p := purposeOf(t, s, purposeAgent)
		if p.Effective != want || p.Revision != revision || p.CategoryID != "agent.new" || p.Parent != "agent.new" ||
			!slices.Equal(p.Fields, []string{"provider", "model", "effort", "permission_mode"}) {
			t.Fatalf("purpose: %+v; want binding %+v at revision %d", p, want, revision)
		}
		if overridden {
			if p.Override != want || p.InheritedFrom != "" {
				t.Fatalf("independent binding not exposed: %+v", p)
			}
		} else if p.Override != (ModelBinding{}) || p.InheritedFrom == "" {
			t.Fatalf("inheritance not exposed: %+v", p)
		}
		it := &InboxItem{ID: "event", Title: "Message", Agent: &InboxAgent{State: AgentRunning, ProcessingTurn: "model-override"}}
		s.inbox.mu.Lock()
		s.inbox.items = map[string]*InboxItem{it.ID: it}
		s.inbox.mu.Unlock()
		before := len(f.sends)
		machine, session, err := s.startInboxAgent(ctx, InboxAgentConfig{Enabled: true}, it, "Model configuration test only")
		if err != nil || machine == "" || session == "" {
			t.Fatalf("twin start: %s/%s %v", machine, session, err)
		}
		f.mu.Lock()
		if len(f.sends) == before {
			f.mu.Unlock()
			t.Fatal("the twin's start reached no node")
		}
		send := f.sends[len(f.sends)-1]
		f.mu.Unlock()
		if send["launcher"] != want.Provider[len("launcher."):] || send["model"] != want.Model || send["effort"] != want.Effort || send["model_fingerprint"] == "" {
			t.Fatalf("node startup: %+v; want %+v", send, want)
		}
		t.Logf("node startup launcher=%s model=%s effort=%s revision=%d", send["launcher"], send["model"], send["effort"], revision)
		var untouched modelRecord
		if _, err := st.Setting(ctx, modelKey(purposeAgent), &untouched); err != nil || !reflect.DeepEqual(untouched, legacy) {
			t.Fatalf("dormant record changed: %+v %v", untouched, err)
		}
		if b, err := s.resolveModel(ctx, purposeLarkAgent); err != nil || b != lark {
			t.Fatalf("Lark /agent changed: %+v %v", b, err)
		}
		if b, err := s.resolveModel(ctx, "agent.new"); err != nil || b != inherited {
			t.Fatalf("Agents changed: %+v %v", b, err)
		}
	}

	t.Run("no override ignores dormant record", func(t *testing.T) { assertState(inherited, 0, false) })
	custom := ModelBinding{Provider: "launcher.twin-fast", Model: "twin-selected", Effort: "low"}
	keys := settingsSnapshot(st)
	w := modelRequest(s, http.MethodPut, purposeAgent, `{"revision":0,"provider":"launcher.twin-fast","model":"twin-selected","effort":"low"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	// A save writes the twin's own record and nothing else: not Agents, Lark /agent or the Inbox's settings.
	if changed := changedSettings(keys, settingsSnapshot(st)); !slices.Equal(changed, []string{"model_config.overrides.v1.inbox.agent"}) {
		t.Fatalf("save wrote %v", changed)
	}
	t.Run("explicit override reaches node", func(t *testing.T) { assertState(custom, 1, true) })
	// A sparse update keeps the provider and model; both verbs share optimistic locking.
	w = modelRequest(s, http.MethodPatch, purposeAgent, `{"revision":1,"effort":"high"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("sparse update: %d %s", w.Code, w.Body.String())
	}
	custom.Effort = "high"
	assertState(custom, 2, true)
	// Agents changing underneath leaves a saved override as it is.
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", Model: "agents-meanwhile"})
	if p := purposeOf(t, s, purposeAgent); p.Effective != custom {
		t.Fatalf("Agents' change reached the override: %+v", p)
	}
	setModel(t, s, "agent.new", inherited)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		w = modelRequest(s, method, purposeAgent, `{"revision":0,"model":"stale"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s stale write: %d %s", method, w.Code, w.Body.String())
		}
	}
	w = modelRequest(s, http.MethodPut, purposeAgent, `{"revision":2,"provider":null,"model":null,"effort":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	t.Run("reset follows Agents again", func(t *testing.T) { assertState(inherited, 3, false) })
	inherited.Model = "agents-changed"
	setModel(t, s, "agent.new", inherited)
	assertState(inherited, 3, false)
	// The reset survives a manager restart, without restoring the retired namespace.
	s2 := newServiceOn(t, f, st)
	p := purposeOf(t, s2, purposeAgent)
	if p.Revision != 3 || p.Override != (ModelBinding{}) || p.Effective != inherited {
		t.Fatalf("restart: %+v", p)
	}
}

func TestInboxAgentModelValidationAndProviderUse(t *testing.T) {
	s, _, _ := modelService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "twin-fast"}})
	for _, body := range []string{
		`{"provider":"launcher.twin-fast"}`,
		`{"revision":0,"provider":"launcher.missing"}`,
		`{"revision":0,"effort":"unsupported"}`,
		`{"revision":0,"model":"not a model id"}`,
	} {
		if w := modelRequest(s, http.MethodPut, purposeAgent, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// The twin is an Agents run: Codex accepts the same explicit purpose effort.
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "twin-fast"}}, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex-custom"}})
	if w := modelRequest(s, http.MethodPut, purposeAgent, `{"revision":0,"provider":"launcher.codex-custom","model":"selected","effort":"high"}`); w.Code != http.StatusOK {
		t.Fatalf("Codex effort rejected: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPut, purposeAgent, `{"revision":1,"provider":"launcher.twin-fast","model":"selected"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	uses, err := s.providerModelUses(context.Background(), "launcher.twin-fast")
	if err != nil || len(uses) != 1 {
		t.Fatalf("override provider not protected: %v %v", uses, err)
	}
	if w := request(s, http.MethodDelete, "/api/v1/launchers/twin-fast", ""); w.Code == http.StatusOK {
		t.Fatalf("provider in use deleted: %s", w.Body.String())
	}
	// The other uses of Agents' siblings stay read-only.
	for _, id := range []string{"inbox.judge", "translate", "schedule"} {
		p := purposeOf(t, s, id)
		w := modelRequest(s, http.MethodPut, id, fmt.Sprintf(`{"revision":%d,"model":"other"}`, p.Revision))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s became writable: %d %s", id, w.Code, w.Body.String())
		}
	}
}

// settingsSnapshot is a copy of what st holds, key by key.
func settingsSnapshot(st *downStore) map[string][]byte {
	st.memStore.mu.Lock()
	defer st.memStore.mu.Unlock()
	out := make(map[string][]byte, len(st.memStore.vals))
	for k, v := range st.memStore.vals {
		out[k] = bytes.Clone(v)
	}
	return out
}

// changedSettings are the keys added, removed or changed from before to after, sorted.
func changedSettings(before, after map[string][]byte) []string {
	changed := []string{}
	for k, v := range after {
		if old, ok := before[k]; !ok || !bytes.Equal(old, v) {
			changed = append(changed, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	return slices.Compact(changed)
}
