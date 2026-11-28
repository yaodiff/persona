package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ownerDevs is a fleet of computers whose nodes take node.configure and answer botmux.owner_candidates.
type ownerDevs struct {
	Devices
	machines []devices.Summary
	answers  map[string]proto.BotmuxOwnerCandidates // by machine; missing: an older node (unknown method)

	mu         sync.Mutex
	configured map[string][]string // machine → the botmux_owner_union_id values it was handed, in order
}

func (d *ownerDevs) List() []devices.Summary    { return d.machines }
func (d *ownerDevs) Has(id string) bool         { return true }
func (d *ownerDevs) Canonical(id string) string { return id }

func (d *ownerDevs) CallNode(_ context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "node.configure":
		var p struct {
			Owner *string `json:"botmux_owner_union_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Owner == nil {
			return nil, errors.New("unexpected node.configure")
		}
		d.mu.Lock()
		if d.configured == nil {
			d.configured = map[string][]string{}
		}
		d.configured[id] = append(d.configured[id], *p.Owner)
		d.mu.Unlock()
		return json.RawMessage(`{}`), nil
	case proto.MethodBotmuxOwnerCandidates:
		a, ok := d.answers[id]
		if !ok {
			return nil, errors.New("unknown method")
		}
		return json.Marshal(a)
	}
	return nil, errors.New("unexpected " + method)
}

func (d *ownerDevs) handed(id string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.configured[id]...)
}

func ownerFleet() *ownerDevs {
	return &ownerDevs{machines: []devices.Summary{{ID: "m1", Online: true}, {ID: "m2", Online: true}, {ID: "m3"}}}
}

// The id is saved trimmed with the other Agents display settings, refused unless a union_id or empty, only by full
// access, and handed to every computer online on each change; empty clears it.
func TestBotmuxOwnerUnionIDSetting(t *testing.T) {
	st := &agentsDisplayStore{settingsBoardStore: newSettingsBoardStore()}
	s, h := navigationService(t, st)
	h.answerApp(assistantwire.AppSettings, assistantwire.AgentsDisplayGet, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	devs := ownerFleet()
	s.devs = devs
	call := func(method, body, role string) (int, agentsDisplay) {
		t.Helper()
		w := boardCall(s, method, "/api/v1/agents/display-settings", body, role)
		var got agentsDisplay
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}
	w := boardCall(s, "GET", "/api/v1/agents/display-settings", "", roleAnswer)
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || raw["botmux_owner_union_id"] != "" {
		t.Fatalf("default must carry an empty botmux_owner_union_id: %d %s", w.Code, w.Body)
	}
	if code, _ := call("PUT", `{"botmux_owner_union_id":"on_abc"}`, roleAnswer); code != http.StatusForbidden {
		t.Fatalf("answer-only device: %d", code)
	}
	for _, body := range []string{`{"botmux_owner_union_id":"ou_abc"}`, `{"botmux_owner_union_id":"on_a b"}`,
		`{"botmux_owner_union_id":1}`, `{"botmux_owner_union_id":"on_abc","show_botmux":true}`,
		`{"botmux_owner_union_id":"on_abc","only_if_unset":true}`} {
		if code, _ := call("PUT", body, roleFull); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", body, code)
		}
	}
	if code, got := call("GET", "", roleFull); code != 200 || got.BotmuxOwnerUnionID != "" {
		t.Fatalf("refused values must not be saved: %d %+v", code, got)
	}
	if devs.handed("m1") != nil {
		t.Fatalf("refused values must not be handed out: %v", devs.handed("m1"))
	}

	if code, got := call("PUT", `{"botmux_owner_union_id":"  on_abc123  "}`, roleFull); code != 200 || got.BotmuxOwnerUnionID != "on_abc123" || !got.ShowBotmux {
		t.Fatalf("save: %d %+v", code, got)
	}
	waitFor(t, "the computers online get the id", func() bool {
		return reflect.DeepEqual(devs.handed("m1"), []string{"on_abc123"}) && reflect.DeepEqual(devs.handed("m2"), []string{"on_abc123"})
	})
	if devs.handed("m3") != nil {
		t.Fatalf("an offline computer gets it when it connects, not now: %v", devs.handed("m3"))
	}
	if code, got := call("PUT", `{"botmux_owner_union_id":""}`, roleFull); code != 200 || got.BotmuxOwnerUnionID != "" {
		t.Fatalf("clear: %d %+v", code, got)
	}
	waitFor(t, "the computers online get the clearing", func() bool {
		return reflect.DeepEqual(devs.handed("m1"), []string{"on_abc123", ""})
	})
	// The other display settings are as they were.
	if code, got := call("PUT", `{"show_botmux":false}`, roleFull); code != 200 || got.ShowBotmux || got.BotmuxOwnerUnionID != "" {
		t.Fatalf("show_botmux after the owner: %d %+v", code, got)
	}
}

// A new connection is handed the id: this manager's own, or the default manager's for another manager; when it cannot
// be read the node is handed nothing (it keeps what it has, never a clearing by mistake).
func TestBotmuxOwnerUnionIDOnConnect(t *testing.T) {
	ctx := context.Background()
	ownerOf := func(calls []devicewire.NodeCall) (string, bool) {
		for _, c := range calls {
			var p struct {
				Owner *string `json:"botmux_owner_union_id"`
			}
			if c.Method == "node.configure" && json.Unmarshal(c.Params, &p) == nil && p.Owner != nil {
				return *p.Owner, true
			}
		}
		return "", false
	}
	st := &agentsDisplayStore{settingsBoardStore: newSettingsBoardStore()}
	s, h := navigationService(t, st)
	s.devs = ownerFleet()
	_ = st.PutSetting(ctx, agentsBotmuxOwnerKey, "on_mine")

	h.answerApp(assistantwire.AppSettings, assistantwire.AgentsDisplayGet, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	if got, ok := ownerOf(s.nodeConnectCalls(ctx, "m1")); !ok || got != "on_mine" {
		t.Fatalf("default manager: %q %v", got, ok)
	}
	h.answerApp(assistantwire.AppSettings, assistantwire.AgentsDisplayGet, func(json.RawMessage) (any, error) {
		return map[string]any{"show_botmux": true, "botmux_owner_union_id": "on_shared"}, nil
	})
	if got, ok := ownerOf(s.nodeConnectCalls(ctx, "m1")); !ok || got != "on_shared" {
		t.Fatalf("another manager: %q %v", got, ok)
	}
	h.answerApp(assistantwire.AppSettings, assistantwire.AgentsDisplayGet, func(json.RawMessage) (any, error) {
		return nil, errors.New("the default manager is away")
	})
	if got, ok := ownerOf(s.nodeConnectCalls(ctx, "m1")); ok {
		t.Fatalf("an unreadable id must hand nothing, got %q", got)
	}
}

// Suggestions merge what the computers online found in their bots.json: ids bots name as their only person first, an
// id that is not a union_id dropped, an older node (no such method) or an offline computer adding nothing. Only full
// access may read them, and they are never saved by themselves.
func TestBotmuxOwnerCandidates(t *testing.T) {
	st := &agentsDisplayStore{settingsBoardStore: newSettingsBoardStore()}
	s, _ := navigationService(t, st)
	devs := &ownerDevs{
		machines: []devices.Summary{{ID: "m1", Online: true}, {ID: "m2", Online: true}, {ID: "m3", Online: true}, {ID: "m4"}},
		answers: map[string]proto.BotmuxOwnerCandidates{
			"m1": {Candidates: []proto.BotmuxOwnerCandidate{{UnionID: "on_bob", Bots: 3}, {UnionID: "on_alice", Bots: 1, Sole: 1}}},
			"m2": {Candidates: []proto.BotmuxOwnerCandidate{{UnionID: "on_alice", Bots: 2, Sole: 2}, {UnionID: "ou_not_union", Bots: 9, Sole: 9}}},
			"m4": {Candidates: []proto.BotmuxOwnerCandidate{{UnionID: "on_offline", Bots: 5, Sole: 5}}},
		},
	}
	s.devs = devs
	if w := boardCall(s, "GET", "/api/v1/agents/botmux-owner-candidates", "", roleAnswer); w.Code != http.StatusForbidden {
		t.Fatalf("answer-only device: %d", w.Code)
	}
	start := time.Now()
	w := boardCall(s, "GET", "/api/v1/agents/botmux-owner-candidates", "", roleFull)
	var got botmuxOwnerCandidates
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("candidates: %d %s", w.Code, w.Body)
	}
	if time.Since(start) > botmuxOwnerCandidatesWait {
		t.Fatalf("the fan-out took %v", time.Since(start))
	}
	want := []botmuxOwnerCandidate{{UnionID: "on_alice", Bots: 3, Sole: 3, Machines: 2}, {UnionID: "on_bob", Bots: 3, Machines: 1}}
	if !reflect.DeepEqual(got.Candidates, want) {
		t.Fatalf("candidates = %+v, want %+v", got.Candidates, want)
	}
	if code, cur := func() (int, agentsDisplay) {
		w := boardCall(s, "GET", "/api/v1/agents/display-settings", "", roleFull)
		var d agentsDisplay
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		return w.Code, d
	}(); code != 200 || cur.BotmuxOwnerUnionID != "" {
		t.Fatalf("reading suggestions must not save one: %d %+v", code, cur)
	}
	for id := range devs.configured {
		t.Fatalf("reading suggestions must not configure %s", id)
	}
	ids := make([]string, 0, len(got.Candidates))
	for _, c := range got.Candidates {
		ids = append(ids, string(c.UnionID))
	}
	if !sort.SliceIsSorted(got.Candidates, func(i, j int) bool { return got.Candidates[i].Sole > got.Candidates[j].Sole }) {
		t.Fatalf("sole owners first: %v", ids)
	}
}
