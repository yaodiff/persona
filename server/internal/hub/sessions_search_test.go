package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

type runtimeSearchPage struct {
	listTestPage
	Hits map[string]sessionlist.SearchHit `json:"search_hits"`
}

func searchRuntime(t *testing.T, h *Hub, query string) runtimeSearchPage {
	t.Helper()
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", query, w.Code, w.Body)
	}
	var out runtimeSearchPage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hits == nil || len(out.Hits) != len(out.Sessions) {
		t.Fatalf("missing/extraneous authority: %s", w.Body)
	}
	for _, s := range out.Sessions {
		if s.Assistant || out.Hits[runtimeListID(s.MachineID, s.SessionID)].Score == 0 {
			t.Fatalf("invalid Agent search row: %+v", s)
		}
	}
	return out
}
func TestRuntimeSearchBeyondLoadedRowsAndTruncatedPreview(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "computer", Name: "Studio"})
	h.machines, h.byID[m.cfg.ID] = []*Machine{m}, m
	for n := range 260 {
		m.sessions = append(m.sessions, proto.Session{MachineID: m.cfg.ID, SessionID: fmt.Sprintf("s%03d", n), Title: "Ordinary workspace", Cwd: "/work/project", State: "idle", UpdatedAt: time.Unix(int64(260-n), 0)})
	}
	m.sessions[259].LastText = strings.Repeat("中😀", 300) + " Needle proof " + strings.Repeat("界😀", 300)
	m.sessions = append(m.sessions, proto.Session{MachineID: m.cfg.ID, SessionID: "assistant", Title: "Needle main", Assistant: true, State: "idle"})
	q := "search=" + url.QueryEscape("host:studio needle proof") + "&summary=1&roots=1&limit=40"
	page := searchRuntime(t, h, q)
	if page.Total != 1 || page.Sessions[0].SessionID != "s259" || strings.Contains(page.Sessions[0].LastText, "Needle") {
		t.Fatalf("did not search complete indexed row: %+v", page)
	}
	hit := page.Hits[runtimeListID(m.cfg.ID, "s259")]
	if hit.Score != 45 || hit.Evidence == nil || hit.Evidence.Role != "agent" || hit.Evidence.Match != "Needle" || !utf8.ValidString(hit.Evidence.Before+hit.Evidence.After) || len([]rune(hit.Evidence.Before)) != 64 || len([]rune(hit.Evidence.After)) != 160 {
		t.Fatalf("bad bounded evidence: %+v", hit)
	}
	empty := searchRuntime(t, h, "search=absent&summary=1")
	if empty.Total != 0 || len(empty.Hits) != 0 || empty.HasMore {
		t.Fatalf("empty search: %+v", empty)
	}
	if !slices.Contains(h.features(), featureSessionsSearch) {
		t.Fatal("search capability not advertised")
	}
	// Legacy q remains a literal substring, not the new multi-term grammar.
	literal := runtimePage(t, h, "q=host%3Astudio+needle+proof&limit=40")
	if literal.Total != 0 {
		t.Fatal("legacy q changed")
	}
}

type searchPinsStore struct {
	Store
	pins  Pins
	fail  bool
	reads int
}

func (s *searchPinsStore) Setting(_ context.Context, key string, out any) (bool, error) {
	if key != "pins" {
		return false, nil
	}
	s.reads++
	if s.fail {
		return false, errors.New("pins unavailable")
	}
	*(out.(*Pins)) = s.pins
	return true, nil
}
func TestRuntimeSearchPinsOnlyWhenRequiredAndFrozen(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "computer", Name: "Studio"})
	h.machines, h.byID[m.cfg.ID] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: m.cfg.ID, SessionID: "a", Title: "Needle A", State: "idle"}, {MachineID: m.cfg.ID, SessionID: "b/part", Title: "Needle B", State: "idle"}}
	store := &searchPinsStore{pins: Pins{Sessions: []string{"computer/a", "computer/b/part"}}, fail: true}
	h.store = store
	if page := searchRuntime(t, h, "search=needle&limit=1"); page.Total != 2 || store.reads != 0 {
		t.Fatalf("plain search read pins: %+v reads=%d", page, store.reads)
	}
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?search=is%3Apinned", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "pins unavailable") {
		t.Fatalf("missing pin context silently became empty: %d %s", w.Code, w.Body)
	}
	store.fail = false
	query := "search=is%3Apinned&summary=1&limit=1"
	first := searchRuntime(t, h, query)
	if first.Total != 2 || !first.HasMore {
		t.Fatalf("pins missing: %+v", first)
	}
	reads := store.reads
	store.fail = true
	store.pins.Sessions = nil
	second := searchRuntime(t, h, query+"&cursor="+url.QueryEscape(first.Cursor))
	if second.Total != 2 || second.HasMore || store.reads != reads || second.Sessions[0].SessionID == first.Sessions[0].SessionID {
		t.Fatalf("continuation reread pins: %+v reads=%d/%d", second, store.reads, reads)
	}
}
func TestRuntimeSearchHierarchyAndRequestValidation(t *testing.T) {
	h, _ := hierarchyHub(t)
	page := searchRuntime(t, h, "search=state%3Aneeds_you&roots=1&summary=1")
	if page.Total != 1 || page.Sessions[0].SessionID != "lead" || page.Sessions[0].State != proto.StateIdle || page.Sessions[0].Children().NeedsYou != 1 {
		t.Fatalf("root state search differs from list: %+v", page)
	}
	for _, query := range []string{"search=", "search=x&search=y", "search=x&q=y", "search=x&since=v1", "search=x&counts=0", "search=x&with_counts=0", "search=x&catalog=0", "search=x&session_id=lead&machine_id=demo", "search=x&limit=101", "search=" + strings.Repeat("x", 1025)} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if w.Code != 400 {
			t.Errorf("%s: %d %s", query, w.Code, w.Body)
		}
	}
}
func TestRuntimeSearchExtensionScopeBeforeRankAndCounts(t *testing.T) {
	h, base, _, b := reachHub(t)
	h.devices.mu.Lock()
	h.cfg.Machines = append(h.cfg.Machines, MachineConfig{ID: "mc", Name: "mc", Kind: "local"})
	h.devices.mu.Unlock()
	if _, err := h.moveMachine(context.Background(), "mc", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}}, devicewire.State{Machine: proto.Machine{ID: "mc", Name: "mc"}})
	eventually(t, 5*time.Second, "owned second computer", func() bool { return h.localMachine("mc") != nil })
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "secret", Title: "Needle foreign"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "same", Title: "Needle one"}, {MachineID: "mb", SessionID: "health", Title: "Needle private", Space: "health"}, {MachineID: "mb", SessionID: "main", Title: "Needle assistant", Assistant: true}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mc", Sessions: []proto.Session{{MachineID: "mc", SessionID: "same", Title: "Needle two"}}})
	query := "/api/v1/sessions?search=needle&summary=1&limit=1"
	code, raw := extDo(t, base, "GET", query, "", nil)
	var first runtimeSearchPage
	if err := json.Unmarshal([]byte(raw), &first); err != nil || code != 200 || first.Total != 2 || len(first.Sessions) != 1 || !first.HasMore {
		t.Fatalf("scoped first: %d %s %v", code, raw, err)
	}
	code, raw = extDo(t, base, "GET", query+"&cursor="+url.QueryEscape(first.Cursor), "", nil)
	var second runtimeSearchPage
	if err := json.Unmarshal([]byte(raw), &second); err != nil || code != 200 || second.Total != 2 || len(second.Sessions) != 1 || second.HasMore || second.Sessions[0].MachineID == first.Sessions[0].MachineID {
		t.Fatalf("scoped second: %d %s %v", code, raw, err)
	}
	// Inspect values, not JSON field names: a legitimate runtime row carries assistant:false.
	// Both pages and their evidence maps must contain only the two authorized public rows.
	for i, page := range []runtimeSearchPage{first, second} {
		row := page.Sessions[0]
		title, allowed := map[string]string{"mb": "Needle one", "mc": "Needle two"}[row.MachineID]
		if !allowed || row.SessionID != "same" || row.Title != title || row.Assistant || row.Space != "" {
			t.Fatalf("page %d leaked an unauthorized or private row: %+v", i, row)
		}
		hit, ok := page.Hits[runtimeListID(row.MachineID, row.SessionID)]
		if len(page.Hits) != 1 || !ok || hit.Score <= 0 || hit.Evidence != nil {
			t.Fatalf("page %d leaked or mismatched search evidence: %+v", i, page.Hits)
		}
	}
	for _, suffix := range []string{"&manager=assistant", "&machine_id=ma"} {
		if code, raw := extDo(t, base, "GET", query+suffix, "", nil); code != http.StatusForbidden {
			t.Fatalf("scope override: %d %s", code, raw)
		}
	}
	// Moving a machine invalidates the extension's old authorization-bound snapshot.
	if _, err := h.moveMachine(context.Background(), "mc", assistantwire.AssistantID); err != nil {
		t.Fatal(err)
	}
	code, raw = extDo(t, base, "GET", query+"&cursor="+url.QueryEscape(first.Cursor), "", nil)
	if code != 400 {
		t.Fatalf("retired authorization cursor remained valid: %d %s", code, raw)
	}
}
