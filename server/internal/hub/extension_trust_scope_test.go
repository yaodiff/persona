package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	trustPath      = "/api/v1/assistant/extension-trust"
	trustExtension = "sample-memory"
	trustManager   = "m-0b0b0b"
)

func trustScopeRequest(t *testing.T, srv *httptest.Server, method, path, manager, token, body string, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if manager != "" {
		r.Header.Set(ManagerHeader, manager)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s for %q = %d, want %d: %s", method, path, manager, resp.StatusCode, want, raw)
	}
	return raw
}

func assertScopedInfoAccessRoster(t *testing.T, srv *httptest.Server, token string, want map[string]bool) {
	t.Helper()
	var out struct {
		Managers []struct {
			ID     string `json:"id"`
			Scoped *bool  `json:"extension_info_access_scoped"`
		} `json:"managers"`
	}
	raw := trustScopeRequest(t, srv, http.MethodGet, "/api/v1/managers", "", token, "", http.StatusOK)
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Managers) != len(want) {
		t.Fatalf("roster: %s (%v)", raw, err)
	}
	for _, m := range out.Managers {
		scoped, ok := want[m.ID]
		if !ok || scoped && (m.Scoped == nil || !*m.Scoped) || !scoped && m.Scoped != nil {
			t.Errorf("roster capability for %q, want %v: %s", m.ID, scoped, raw)
		}
	}
}

func connectTrustAssistant(t *testing.T, h *Hub, url, id string, store assistant.Store) *fakeExt {
	t.Helper()
	ext := &fakeExt{h: h, tokens: map[string]extwire.Authorized{}}
	bus := assistant.NewBusHub(assistant.BusOptions{ID: id, Hub: url, Token: busToken(t, h, id, id), Extensions: ext})
	svc := assistant.New(assistant.Options{ID: id, Hub: bus, Store: func() assistant.Store { return store }})
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bus.Serve(ctx, svc)
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})
	wait, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := bus.Wait(wait); err != nil {
		t.Fatal(err)
	}
	return ext
}

type trustLookupStore struct {
	*repositorystore.Store
	unavailable atomic.Bool
}

func (s *trustLookupStore) ExtensionTrustOf(ctx context.Context, extension string) (string, error) {
	if s.unavailable.Load() {
		return "", errors.New("trust store unavailable")
	}
	return s.Store.ExtensionTrustOf(ctx, extension)
}

func TestExtensionTrustThroughManagerRouting(t *testing.T) {
	h, srv, _ := managersHub(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	newStore := func() *repositorystore.Store {
		t.Helper()
		url, err := dbtest.Template(ctx, os.Getenv(dbtest.Env))
		if err != nil {
			t.Fatal(err)
		}
		database, err := db.Open(ctx, url, db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(database.Close)
		return repositorystore.New(database)
	}
	aStore, bStore := newStore(), &trustLookupStore{Store: newStore()}
	builtin := assistantwire.AssistantID
	a := connectTrustAssistant(t, h, srv.URL, builtin, aStore)
	b := connectTrustAssistant(t, h, srv.URL, trustManager, bStore)
	aToken := a.issue(trustExtension, "Sample Memory", []string{extwire.ScopeAgentsRead})
	bToken := b.issue(trustExtension, "Sample Memory", []string{extwire.ScopeAgentsRead})
	issue := func(body, role string) string {
		t.Helper()
		raw := trustScopeRequest(t, srv, http.MethodPost, "/api/v1/auth/tokens", "", h.cfg.Token, body, http.StatusOK)
		var out struct{ Token, Role string }
		if err := json.Unmarshal(raw, &out); err != nil || out.Role != role || out.Token == "" {
			t.Fatalf("issue %s client credential: role %q, error %v", role, out.Role, err)
		}
		return out.Token
	}
	full := issue(`{"kind":"phone"}`, roleFull)
	scoped := issue(`{"kind":"other","scopes":["agents.read"]}`, roleScoped)
	assertScopedInfoAccessRoster(t, srv, full, map[string]bool{builtin: true, trustManager: true})
	for _, id := range []string{builtin, trustManager} {
		var settings struct {
			Labs *bool `json:"integrations_v2"`
		}
		raw := trustScopeRequest(t, srv, http.MethodGet, "/api/v1/assistant", id, full, "", http.StatusOK)
		if err := json.Unmarshal(raw, &settings); err != nil || settings.Labs == nil || *settings.Labs {
			t.Fatalf("%s: expected Labs off in the default settings (%v)", id, err)
		}
	}
	checkTrust := func(id, trust string, cats []string) {
		t.Helper()
		var out struct {
			Items []assistantwire.ExtensionTrust
		}
		raw := trustScopeRequest(t, srv, http.MethodGet, trustPath, id, full, "", http.StatusOK)
		want := []assistantwire.ExtensionTrust{}
		if trust != "" {
			want = append(want, assistantwire.ExtensionTrust{Extension: trustExtension, Trust: trust, Categories: cats})
		}
		if err := json.Unmarshal(raw, &out); err != nil || !reflect.DeepEqual(out.Items, want) {
			t.Fatalf("GET trust for %q = %s, want %+v (%v)", id, raw, want, err)
		}
	}
	checkOwner := func(id, token string, want extwire.OwnerVerdict) {
		t.Helper()
		var out extwire.Authorized
		p := extwire.Authorize{Hash: hashToken(token), Pattern: "GET /api/v1/sessions", Method: http.MethodGet, Path: "/api/v1/sessions"}
		if err := h.callBus(ctx, id, extwire.MethodAuthorize, p, &out); err != nil ||
			out.Extension != trustExtension || !out.OK || out.Owner != want {
			t.Errorf("%s authorize: extension %q, route allowed %v, owner %v, want owner %v (%v)", id,
				out.Extension, out.OK, out.Owner, want, err)
		}
	}

	checkTrust("", "", nil)
	checkTrust(trustManager, "", nil)
	checkOwner(builtin, aToken, extwire.OwnerThirdParty)
	checkOwner(trustManager, bToken, extwire.OwnerThirdParty)
	const owner = `{"trust":"owner"}`
	const thirdParty = `{"trust":"third_party","categories":["health"]}`
	putPath := trustPath + "/" + trustExtension
	trustScopeRequest(t, srv, http.MethodPut, putPath, builtin, full, owner, http.StatusOK)
	trustScopeRequest(t, srv, http.MethodPut, putPath, trustManager, full, thirdParty, http.StatusOK)
	checkTrust("", assistantwire.ExtensionTrustOwner, []string{})
	checkTrust(trustManager, assistantwire.ExtensionTrustThirdParty, []string{"health"})
	checkOwner(builtin, aToken, extwire.OwnerConfirmed)
	checkOwner(trustManager, bToken, extwire.OwnerThirdParty)
	if got := h.assistantProfileOf(builtin).ExtensionOwners; !slices.Equal(got, []string{trustExtension}) {
		t.Errorf("profile for older hubs: owner list = %q", got)
	}
	for _, token := range []string{bToken, scoped} {
		for _, id := range []string{builtin, trustManager} {
			trustScopeRequest(t, srv, http.MethodGet, trustPath, id, token, "", http.StatusForbidden)
			trustScopeRequest(t, srv, http.MethodPut, putPath, id, token, owner, http.StatusForbidden)
		}
	}
	checkTrust(builtin, assistantwire.ExtensionTrustOwner, []string{})
	checkTrust(trustManager, assistantwire.ExtensionTrustThirdParty, []string{"health"})

	trustScopeRequest(t, srv, http.MethodPatch, "/api/v1/managers/"+trustManager, "", full, `{"default":true}`, http.StatusOK)
	trustScopeRequest(t, srv, http.MethodPut, putPath, "", full, owner, http.StatusOK)
	trustScopeRequest(t, srv, http.MethodPut, putPath, builtin, full, thirdParty, http.StatusOK)
	checkTrust("", assistantwire.ExtensionTrustOwner, []string{})
	checkTrust(builtin, assistantwire.ExtensionTrustThirdParty, []string{"health"})
	checkOwner(trustManager, bToken, extwire.OwnerConfirmed)
	checkOwner(builtin, aToken, extwire.OwnerThirdParty)
	bStore.unavailable.Store(true)
	checkOwner(trustManager, bToken, extwire.OwnerThirdParty)
	bStore.unavailable.Store(false)
	checkOwner(trustManager, bToken, extwire.OwnerConfirmed)
	if got := h.assistantProfileOf(builtin).ExtensionOwners; len(got) != 0 {
		t.Errorf("profile for older hubs after revoke: owner list = %q", got)
	}
}

func TestExtensionReadAuthorizationUsesCurrentAssistant(t *testing.T) {
	h, srv, _ := managersHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Kind: "local"}, {ID: "mb", Kind: "local"}}
	builtin := assistantwire.AssistantID
	aToken, bToken := extTokenPrefix+randomToken(), extTokenPrefix+randomToken()
	legacy := &extwire.Authorized{OK: true, Extension: trustExtension}
	var reply atomic.Pointer[extwire.Authorized]
	answer := func(token string, result func() *extwire.Authorized) func(string, json.RawMessage) (any, error) {
		return func(method string, raw json.RawMessage) (any, error) {
			if method != extwire.MethodAuthorize {
				return map[string]any{}, nil
			}
			var p extwire.Authorize
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			if p.Hash != hashToken(token) {
				return extwire.Authorized{}, nil
			}
			out := result()
			if out == nil {
				return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: "not ready"}
			}
			return out, nil
		}
	}
	a := connectGuardManager(t, h, srv.URL, builtin, answer(aToken, func() *extwire.Authorized { return legacy }))
	bAnswer := answer(bToken, reply.Load)
	b := connectGuardManager(t, h, srv.URL, trustManager, bAnswer)
	if _, err := h.moveMachine(t.Context(), "mb", trustManager); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		m       *guardManager
		machine string
	}{{a, "ma"}, {b, "mb"}} {
		id := pair.machine
		pair.m.report(t, devicewire.State{Machine: proto.Machine{ID: id, Name: id}})
		eventually(t, 5*time.Second, id+" in the hub", func() bool { return h.localMachine(id) != nil })
		h.takeFleetSessions(assistantwire.FleetSessions{MachineID: id, Sessions: []proto.Session{
			{MachineID: id, SessionID: "regular", Title: "REGULAR", Cwd: "/work/project"},
			{MachineID: id, SessionID: "track", Title: "TRACK-" + id, Space: "health"},
		}})
	}
	snapshot := func(m *guardManager, seq uint64, p *assistantwire.Profile) {
		t.Helper()
		if err := m.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: m.id, Seq: seq, Profile: p}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ownerProfile := &assistantwire.Profile{ExtensionOwners: []string{trustExtension}}
	snapshot(a, 1, ownerProfile)
	const sessions = "/api/v1/sessions?machine_id="
	read := func(token, machine string, want bool) {
		t.Helper()
		raw := trustScopeRequest(t, srv, http.MethodGet, sessions+machine, "", token, "", http.StatusOK)
		if strings.Contains(string(raw), "TRACK-"+machine) != want || !strings.Contains(string(raw), "REGULAR") {
			t.Errorf("sessions for %s, want track visible %v: %s", machine, want, raw)
		}
	}
	read(aToken, "ma", true)
	trustScopeRequest(t, srv, http.MethodGet, sessions+"mb", "", bToken, "", http.StatusServiceUnavailable)
	reply.Store(legacy)
	read(bToken, "mb", false) // B has no snapshot yet, and A's has the same extension id.
	snapshot(b, 1, ownerProfile)
	read(bToken, "mb", true)
	snapshot(b, 2, &assistantwire.Profile{})
	read(bToken, "mb", false)
	snapshot(b, 3, ownerProfile)
	read(bToken, "mb", true)
	snapshot(b, 4, nil) // A partial report cannot confirm the owner list.
	read(bToken, "mb", false)
	snapshot(b, 5, ownerProfile)
	read(bToken, "mb", true)

	r := httptest.NewRequest(http.MethodGet, sessions+"mb", nil)
	p, ok, opens, _, _ := h.authorizeExtension(t.Context(), bToken, r)
	if !ok || !opens {
		t.Fatal("the old connection did not authenticate the extension")
	}
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
	disconnect := func() {
		t.Helper()
		if err := h.busConnOf(trustManager).ws.CloseNow(); err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, "B disconnected", func() bool { return h.busConnOf(trustManager) == nil })
	}
	disconnect()
	trustScopeRequest(t, srv, http.MethodGet, sessions+"mb", "", bToken, "", http.StatusUnauthorized)
	assertScopedInfoAccessRoster(t, srv, h.cfg.Token, map[string]bool{builtin: false, trustManager: false})

	b = connectGuardManagerWith(t, srv.URL, b.id, b.token, bAnswer)
	read(bToken, "mb", false) // The retained display profile belongs to the previous connection.
	snapshot(b, 6, ownerProfile)
	read(bToken, "mb", true)
	if h.ownerExtension(r) {
		t.Error("a request authenticated on the previous connection still has owner access")
	}
	if err := b.call(t, busHello, map[string]any{"id": b.id, "capabilities": []string{assistantwire.CapabilityExtensionInfoAccessScoped}}, nil); err != nil {
		t.Fatal(err)
	}
	assertScopedInfoAccessRoster(t, srv, h.cfg.Token, map[string]bool{builtin: false, trustManager: true})
	read(bToken, "mb", false) // An announced capability with a missing verdict fails closed.
	noOwner := &extwire.Authorized{OK: true, Extension: trustExtension, Owner: extwire.OwnerThirdParty}
	reply.Store(noOwner)
	read(bToken, "mb", false) // A false verdict wins over B's earlier affirmative profile.
	snapshot(b, 7, &assistantwire.Profile{})
	reply.Store(&extwire.Authorized{OK: true, Extension: trustExtension, Owner: extwire.OwnerConfirmed})
	read(bToken, "mb", true) // The verified reply can grant owner reads even without a matching profile.
	read(aToken, "ma", true) // A still uses the legacy snapshot of its own current connection.
	snapshot(b, 8, ownerProfile)
	reply.Store(noOwner)
	read(bToken, "mb", false)
	reply.Store(&extwire.Authorized{Extension: trustExtension, Scope: extwire.ScopeAgentsRead, Owner: extwire.OwnerConfirmed})
	trustScopeRequest(t, srv, http.MethodGet, sessions+"mb", "", bToken, "", http.StatusForbidden)
	disconnect()
	assertScopedInfoAccessRoster(t, srv, h.cfg.Token, map[string]bool{builtin: false, trustManager: false})
}
