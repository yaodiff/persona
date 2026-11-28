package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestDeploymentMachineResponsesAndReplay(t *testing.T) {
	h := newTestHub(t)
	rawError := i18n.T(i18n.Default, "devices-deploy.hub_changed")
	m := newMachine(h, MachineConfig{ID: "example-deploy", Name: "Example", Kind: kindLink})
	m.snap = &proto.Machine{ID: m.cfg.ID, Name: m.cfg.Name, DeployError: rawError, DeployReason: i18n.T(i18n.Default, "devices-deploy.node_temporary")}
	h.machines = []*Machine{m}
	h.byID = map[string]*Machine{m.cfg.ID: m}
	en := &client{hub: h, role: roleFull, out: make(chan []byte, 32), acceptLang: i18n.En}
	ja := &client{hub: h, out: make(chan []byte, 32), acceptLang: i18n.Ja}
	h.clients = map[*client]struct{}{en: {}, ja: {}}
	h.broadcastMachines()
	read := func(c *client, l i18n.Lang) {
		t.Helper()
		select {
		case raw := <-c.out:
			var event struct {
				Type     string          `json:"type"`
				Machines []proto.Machine `json:"machines"`
				Seq      int64           `json:"event_seq"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "machines" || len(event.Machines) != 1 || event.Machines[0].DeployError != i18n.T(l, "devices-deploy.hub_changed") || event.Machines[0].DeployReason != i18n.T(l, "devices-deploy.node_temporary") {
				t.Fatalf("%s: %s", l, raw)
			}
		case <-time.After(time.Second):
			t.Fatal("no machines event")
		}
	}
	read(en, i18n.En)
	read(ja, i18n.Ja)
	if m.snap.DeployError != rawError {
		t.Fatal("sending translated text mutated canonical report")
	}
	// An event recorded in Chinese is localized again during resume, without changing event_seq.
	resumed := &client{hub: h, out: make(chan []byte, 8), acceptLang: i18n.Ja}
	h.mu.Lock()
	h.helloLocked(resumed, wsHello{Type: "hello"}, h.events.stream, 0, true)
	h.mu.Unlock()
	<-resumed.out // hello
	read(resumed, i18n.Ja)
	// Changing this connection's language updates existing status without another manager report.
	h.onClientHello(en, []byte(`{"locale":"ja"}`))
	read(en, i18n.Ja)
	h.userLocale.Store(i18n.En)
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.En, i18n.Ja} {
		r := httptest.NewRequest("GET", "/api/v1/machines", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Accept-Language", string(l))
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		h.routes().ServeHTTP(w, r)
		var response struct {
			Machines []proto.Machine `json:"machines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(response.Machines) != 1 || response.Machines[0].DeployError != i18n.T(l, "devices-deploy.hub_changed") {
			t.Fatalf("HTTP %s: %d %s", l, w.Code, w.Body.String())
		}
	}
}

func TestUnknownMachineErrorsInRequestLanguage(t *testing.T) {
	h := newTestHub(t)
	h.userLocale.Store(i18n.Ja)
	handler := h.routes()
	for _, path := range []string{"/api/v1/machines/example-missing/claude-dirs", "/api/v1/machines/example-missing/dirs", "/api/v1/machines/example-missing/deploy"} {
		for _, l := range []i18n.Lang{i18n.ZhHans, i18n.En, i18n.Ja} {
			method := "GET"
			if strings.HasSuffix(path, "/deploy") {
				method = "POST"
			}
			r := httptest.NewRequest(method, path, nil)
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			r.Header.Set("Accept-Language", string(l))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusNotFound || body.Error != i18n.T(l, "devices-api.unknown_machine") {
				t.Fatalf("%s %s: %d %s", l, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestDeploymentErrorBodyKeepsCodesAndUnknownFields(t *testing.T) {
	raw := []byte(`{"error":"当前连接使用临时 node，请先安装常驻 node 服务","code":"node_outdated","retryable":false,"future":{"x":1}}`)
	got := localizedDeploymentError(i18n.En, raw)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	var text string
	_ = json.Unmarshal(body["error"], &text)
	if text != i18n.T(i18n.En, "devices-deploy.node_temporary") || string(body["code"]) != `"node_outdated"` || string(body["retryable"]) != "false" || string(body["future"]) != `{"x":1}` {
		t.Fatalf("changed envelope: %s", got)
	}
	for _, raw := range []string{"not JSON", `{"error":"unknown node failure","code":"node_outdated"}`} {
		if got := string(localizedDeploymentError(i18n.Ja, []byte(raw))); got != raw {
			t.Fatalf("changed unknown response: %s", got)
		}
	}
}

func TestComputerBrowserSnapshotsUseConnectionLanguage(t *testing.T) {
	h := newTestHub(t)
	note := i18n.T(i18n.ZhHans, "hub-computer.note_outdated")
	browserNote := i18n.T(i18n.ZhHans, "hub-browser.note_outdated")
	source := proto.Machine{ID: "example", Name: "Example", Computer: &proto.Computer{Note: note, Job: &proto.ComputerJob{Error: note, Step: note}}, Browser: &proto.Browser{Note: browserNote, Job: &proto.ComputerJob{Error: browserNote, Step: browserNote}}}
	m := newMachine(h, MachineConfig{ID: "example", Name: "Example", Kind: kindLink})
	m.snap = &source
	computer, _ := json.Marshal(source.Computer)
	browser, _ := json.Marshal(source.Browser)
	h.ext.report.Store(&extwire.Report{Machines: map[string]extwire.MachineExtras{"example": {Computer: computer, Browser: browser}}})
	h.machines = []*Machine{m}
	h.byID = map[string]*Machine{"example": m}
	clients := map[i18n.Lang]*client{}
	h.clients = map[*client]struct{}{}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		c := &client{hub: h, role: roleFull, out: make(chan []byte, 16), acceptLang: l}
		clients[l] = c
		addTestClient(h, c)
	}
	h.broadcastMachines()
	for l, c := range clients {
		raw := <-c.out
		var event struct {
			Machines []proto.Machine `json:"machines"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		got := event.Machines[0]
		if got.Computer == nil || got.Computer.Job == nil || got.Browser == nil {
			t.Fatalf("missing extension snapshot: %s", raw)
		}
		if got.Computer.Note != i18n.T(l, "hub-computer.note_outdated") || got.Computer.Job.Error != got.Computer.Note || got.Browser.Note != i18n.T(l, "hub-browser.note_outdated") {
			t.Fatalf("%s: %s", l, raw)
		}
		request := httptest.NewRequest("GET", "/api/v1/machines", nil)
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Accept-Language", string(l))
		request.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		rec := httptest.NewRecorder()
		h.routes().ServeHTTP(rec, request)
		if err := json.Unmarshal(rec.Body.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || event.Machines[0].Computer.Note != got.Computer.Note {
			t.Fatalf("HTTP %s: %s", l, rec.Body)
		}
	}
	resumed := &client{hub: h, out: make(chan []byte, 8), acceptLang: i18n.Ja}
	h.mu.Lock()
	h.helloLocked(resumed, wsHello{Type: "hello"}, h.events.stream, 0, true)
	h.mu.Unlock()
	<-resumed.out
	var replay struct {
		Machines []proto.Machine `json:"machines"`
	}
	if err := json.Unmarshal(<-resumed.out, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Machines[0].Computer.Note != i18n.T(i18n.Ja, "hub-computer.note_outdated") {
		t.Fatal("replay retained source locale")
	}
	if source.Computer.Note != note || source.Computer.Job.Error != note || source.Browser.Note != browserNote {
		t.Fatal("shared canonical snapshot mutated")
	}
	// A subsequent client hello projects the same saved snapshot, without another report from the node.
	h.onClientHello(clients[i18n.En], []byte(`{"locale":"ja"}`))
	if err := json.Unmarshal(<-clients[i18n.En].out, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Machines[0].Computer.Note != i18n.T(i18n.Ja, "hub-computer.note_outdated") {
		t.Fatal("locale switch kept old status")
	}
}

// The notes nodes write on their engines (Paseo's and Orca's, in the canonical Chinese of node-engine; an older
// node's Orca note is that same text, #17037) reach each app in its language, in the machine list and in a machines
// event; the manager's snapshot keeps the canonical text, and a note the hub has no words for stays as it is.
func TestEngineNotesLocalized(t *testing.T) {
	args := i18n.Args{"reason": "no Node.js found (set PERSONA_NODEJS)"}
	paseo := i18n.T(i18n.Default, "node-engine.paseo_unreachable", args)
	orca := i18n.T(i18n.Default, "node-engine.orca_incompatible")
	other := "a note from a newer node"
	in := []proto.Machine{{ID: "laptop", Engines: []proto.Engine{{ID: "orca", Note: orca},
		{ID: "paseo", Available: true, Note: paseo}, {ID: "tern", Note: other}}}}
	for _, l := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		got := localizedMachines(l, in)[0].Engines
		if got[1].Note != i18n.T(l, "node-engine.paseo_unreachable", args) ||
			got[0].Note != i18n.T(l, "node-engine.orca_incompatible") || got[2].Note != other {
			t.Fatalf("%s: %+v", l, got)
		}
	}
	if in[0].Engines[1].Note != paseo {
		t.Fatal("the manager's snapshot was changed")
	}

	raw, _ := json.Marshal(map[string]any{"type": "machines", "machines": in})
	var event struct {
		Machines []proto.Machine `json:"machines"`
	}
	if err := json.Unmarshal(localizedMachineEvent(i18n.En, raw), &event); err != nil {
		t.Fatal(err)
	}
	if got := event.Machines[0].Engines[1].Note; got != i18n.T(i18n.En, "node-engine.paseo_unreachable", args) {
		t.Fatalf("event: %q", got)
	}
}
