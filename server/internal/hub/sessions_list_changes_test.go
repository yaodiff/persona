package hub

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A hint names the sessions that changed, to the apps that ask for it (#6685).
func TestRuntimeListHintNamesTheChangedSessions(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "a", State: "idle", Title: "A"}, {MachineID: "demo", SessionID: "b", State: "idle", Title: "B"}}
	first := runtimePage(t, h, "summary=1")
	asks := &client{hub: h, listMode: true, out: make(chan []byte, 4), role: roleFull, urlFeatures: map[string]bool{featureSessionsListChanges: true}}
	plain := &client{hub: h, listMode: true, out: make(chan []byte, 4), role: roleFull}
	extension := &client{hub: h, listMode: true, out: make(chan []byte, 4), role: roleExtension, urlFeatures: map[string]bool{featureSessionsListChanges: true}}
	for _, c := range []*client{asks, plain, extension} {
		addTestClient(h, c)
	}
	type hint struct {
		Type, Revision string
		Since          *string
		Changed        [][2]string
	}
	read := func(c *client) hint {
		t.Helper()
		for {
			select {
			case raw := <-c.out:
				var v hint
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatalf("%s: %v", raw, err)
				}
				if v.Type != "sessions_list" {
					continue // the revs broadcast every app gets
				}
				if v.Revision == "" {
					t.Fatalf("no revision: %s", raw)
				}
				return v
			default:
				t.Fatal("no hint")
			}
		}
	}

	m.mu.Lock()
	m.sessions[1].Title = "B changed"
	m.mu.Unlock()
	h.sessionsChanged(m)
	got := read(asks)
	if got.Since == nil || *got.Since != first.Revision || !slices.Equal(got.Changed, [][2]string{{"demo", "b"}}) {
		t.Fatalf("hint for b's change: since %v changed %v, want since %s changed [[demo b]]", got.Since, got.Changed, first.Revision)
	}
	for name, c := range map[string]*client{"an app that did not ask": plain, "an extension's token": extension} {
		if v := read(c); v.Since != nil || v.Changed != nil || v.Revision != got.Revision {
			t.Errorf("%s: %+v, want only revision %s", name, v, got.Revision)
		}
	}
	// the next hint starts where this one ended
	m.mu.Lock()
	m.sessions = m.sessions[:1]
	m.mu.Unlock()
	h.sessionsChanged(m)
	next := read(asks)
	if next.Since == nil || *next.Since != got.Revision || !slices.Equal(next.Changed, [][2]string{{"demo", "b"}}) {
		t.Fatalf("hint for b's removal: since %v changed %v, want since %s changed [[demo b]]", next.Since, next.Changed, got.Revision)
	}
	// a change that sends no legacy list (reindexSessions) says the same
	m.onEvent("sessions.revs", json.RawMessage(`{"revs":{"a":7}}`))
	revs := read(asks)
	if revs.Since == nil || *revs.Since != next.Revision || !slices.Equal(revs.Changed, [][2]string{{"demo", "a"}}) {
		t.Fatalf("hint for a's revs: since %v changed %v, want since %s changed [[demo a]]", revs.Since, revs.Changed, next.Revision)
	}
}

// More changed ids than a hint holds: the hint says only its revision, and the app reads as before.
func TestRuntimeListHintLeavesOutTooManyChanges(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "seed", State: "idle"}}
	runtimePage(t, h, "summary=1")
	c := &client{hub: h, listMode: true, out: make(chan []byte, 4), role: roleFull, urlFeatures: map[string]bool{featureSessionsListChanges: true}}
	addTestClient(h, c)
	m.mu.Lock()
	for i := range sessionListChangedMax + 1 {
		m.sessions = append(m.sessions, proto.Session{MachineID: "demo", SessionID: "s" + string(rune('A'+i%26)) + string(rune('a'+i/26)), State: "idle"})
	}
	m.mu.Unlock()
	h.sessionsChanged(m)
	select {
	case raw := <-c.out:
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		if v["type"] != "sessions_list" || v["revision"] == "" || v["since"] != nil || v["changed"] != nil {
			t.Fatalf("hint for %d changes: %s", sessionListChangedMax+1, raw)
		}
	default:
		t.Fatal("no hint")
	}
}
