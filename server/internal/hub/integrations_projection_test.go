package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestIntegrationProjectionRelayFollowsOnlyTheTarget(t *testing.T) {
	h, srv, _ := managersHub(t)
	connect := func(id, name string, supports bool) *rawBus {
		t.Helper()
		r := dialRawBusWith(t, srv, busToken(t, h, id, name), func(_ context.Context, method string, params json.RawMessage) (any, error) {
			if method == assistantwire.MethodTools {
				return json.RawMessage(`{"tools":[]}`), nil
			}
			if method != assistantwire.MethodHTTP {
				return nil, fmt.Errorf("unexpected method %q", method)
			}
			var req assistantwire.HTTPRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			if req.Path == "/api/v1/channels" {
				return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"channels":[]}`)}, nil
			}
			if !supports {
				return assistantwire.HTTPResponse{Status: 404}, nil
			}
			item := assistantwire.IntegrationCatalogItem{Ref: assistantwire.IntegrationRef{Manager: assistantwire.IntegrationManagerID(id), Kind: "extension", ID: "shared"}, Name: name}
			var body []byte
			switch req.Path {
			case assistantwire.IntegrationsCatalogPath:
				body, _ = json.Marshal(assistantwire.IntegrationCatalog{Items: []assistantwire.IntegrationCatalogItem{item}})
			case assistantwire.IntegrationsCatalogPath + "/extension/shared":
				body, _ = json.Marshal(assistantwire.IntegrationDetail{Item: item})
			default:
				return nil, fmt.Errorf("unexpected path %q", req.Path)
			}
			return assistantwire.HTTPResponse{Status: 200, Body: body}, nil
		})
		caps := []string{assistantwire.CapabilityConfigOwn}
		if supports {
			caps = append(caps, assistantwire.CapabilityIntegrationsProjection)
		}
		if err := r.call(assistantwire.MethodHello, map[string]any{"id": id, "capabilities": caps}, nil); err != nil {
			t.Fatal(err)
		}
		return r
	}
	const team = "m-1a2b3c"
	connect(assistantwire.AssistantID, "Personal", true)
	old := connect(team, "Team", false)
	assertRoster := func(teamSupported bool) {
		t.Helper()
		var roster struct {
			Managers []struct {
				ID         string `json:"id"`
				Projection *bool  `json:"integrations_projection_v1"`
			} `json:"managers"`
		}
		if code := managersDo(t, h, srv, http.MethodGet, "/api/v1/managers", nil, &roster); code != 200 {
			t.Fatalf("roster HTTP %d", code)
		}
		if len(roster.Managers) != 2 {
			t.Fatalf("roster %+v", roster)
		}
		for _, info := range roster.Managers {
			want := info.ID == assistantwire.AssistantID || info.ID == team && teamSupported
			if want && (info.Projection == nil || !*info.Projection) || !want && info.Projection != nil {
				t.Fatalf("manager %s capability %+v, expected present %v", info.ID, info.Projection, want)
			}
		}
	}
	get := func(path, target string, want int, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		if target != "" {
			req.Header.Set(assistantwire.ManagerHeader, target)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s for %s: HTTP %d, want %d", path, target, resp.StatusCode, want)
		}
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := assistantwire.IntegrationsCatalogPath
	assertRoster(false)
	get(path, team, 404, nil)
	get("/api/v1/channels", team, 200, nil)
	if err := old.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "old manager disconnected", func() bool { return h.busConnOf(team) == nil })
	modern := connect(team, "Team", true)
	assertRoster(true)
	for _, tt := range []struct{ selector, query, id, name string }{
		{"", "", assistantwire.AssistantID, "Personal"},
		{assistantwire.AssistantID, "?manager=" + team, assistantwire.AssistantID, "Personal"},
		{team, "", team, "Team"},
	} {
		var catalog assistantwire.IntegrationCatalog
		get(path+tt.query, tt.selector, 200, &catalog)
		var detail assistantwire.IntegrationDetail
		get(path+"/extension/shared"+tt.query, tt.selector, 200, &detail)
		if len(catalog.Items) != 1 || catalog.Items[0].Ref != detail.Item.Ref ||
			string(detail.Item.Ref.Manager) != tt.id || detail.Item.Ref.ID != "shared" || detail.Item.Name != tt.name {
			t.Fatalf("%s returned %+v and %+v", tt.selector, catalog, detail)
		}
	}
	if err := modern.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "supporting manager disconnected", func() bool { return h.busConnOf(team) == nil })
	assertRoster(false)
	get(path, team, 503, nil)
}
