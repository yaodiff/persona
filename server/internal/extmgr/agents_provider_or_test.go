package extmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

type personalORPage struct {
	Sessions   []agents.Session    `json:"sessions"`
	Remove     []string            `json:"remove"`
	Added      []string            `json:"added"`
	HasMore    bool                `json:"has_more"`
	NextCursor string              `json:"next_cursor"`
	Revision   string              `json:"revision"`
	Total      int                 `json:"total"`
	Counts     *sessionlist.Counts `json:"counts,omitempty"`
}

func personalORRequest(t *testing.T, handler http.Handler, values url.Values, out any) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/v1/agents/sessions?"+values.Encode(), nil)
	handler.ServeHTTP(recorder, request)
	if out != nil && recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v; body %s", values.Encode(), err, recorder.Body)
		}
	}
	return recorder.Code
}

func personalORMustPage(t *testing.T, handler http.Handler, values url.Values) personalORPage {
	t.Helper()
	var page personalORPage
	if code := personalORRequest(t, handler, values, &page); code != http.StatusOK {
		t.Fatalf("%s: HTTP %d", values.Encode(), code)
	}
	return page
}

func personalORMustCounts(t *testing.T, handler http.Handler, values url.Values) sessionlist.Counts {
	t.Helper()
	values.Set("counts", "1")
	var counts sessionlist.Counts
	if code := personalORRequest(t, handler, values, &counts); code != http.StatusOK {
		t.Fatalf("%s: HTTP %d", values.Encode(), code)
	}
	return counts
}

func personalORIDs(rows []agents.Session) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func personalORReference(rows []agents.Session, providers ...string) ([]string, sessionlist.Counts) {
	set := make(map[string]bool, len(providers))
	for _, provider := range providers {
		set[provider] = true
	}
	matching := make([]agents.Session, 0)
	counts := sessionlist.Counts{
		Machine: map[string]int{}, Project: map[string]int{}, Group: map[string]int{},
		State: map[string]int{}, Todo: map[string]int{},
	}
	for _, row := range rows {
		if !set[row.Provider] { // deliberately independent, exact and case-sensitive
			continue
		}
		matching = append(matching, row)
		state := "idle"
		if row.UnreadCount > 0 {
			state = "unread"
		}
		counts.Total++
		counts.Machine[""]++
		counts.Project[""]++
		counts.Group[row.AccountID]++
		counts.State[state]++
		counts.Todo[""]++
	}
	sort.Slice(matching, func(a, b int) bool {
		if matching[a].RankTimeMs == matching[b].RankTimeMs {
			return matching[a].ID < matching[b].ID
		}
		return matching[a].RankTimeMs > matching[b].RankTimeMs
	})
	return personalORIDs(matching), counts
}

func personalORCheckCounts(t *testing.T, got, want sessionlist.Counts, revision string) {
	t.Helper()
	want.Revision = revision
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full-set counts:\n got %+v\nwant %+v", got, want)
	}
}

func TestPersonalAgentHTTPProviderOR8000FrozenPagesCountsAndDelta(t *testing.T) {
	h, handler, _ := agentsHub(t)
	source := &listAgentSource{}
	providerCycle := []string{"provider-a", "provider-b", "provider-c", "Provider-A", "other"}
	for n := 0; n < 8000; n++ {
		source.rows = append(source.rows, agents.Wrap("demo", agents.AgentSession{
			SessionKey: fmt.Sprintf("s%04d", n), AccountID: fmt.Sprintf("account-%d", n%4),
			Provider: providerCycle[n%5], Title: fmt.Sprintf("Demo %04d", n), Kind: "main",
			CanSend: true, UnreadCount: n % 2, RankTimeMs: int64(n),
		}, ""))
	}
	h.agents.Add(source)
	providers := []string{"provider-a", "provider-c", "Provider-A"}
	frozenIDs, frozenCounts := personalORReference(source.rows, providers...)
	if len(frozenIDs) != 4800 {
		t.Fatalf("bad synthetic reference size %d", len(frozenIDs))
	}
	values := url.Values{
		"summary": {"1"}, "limit": {"200"},
		"provider": {"provider-c", "provider-a", "Provider-A", "provider-a"},
	}
	first := personalORMustPage(t, handler, values)
	if first.Total != len(frozenIDs) || first.Revision == "" || !first.HasMore || len(first.Sessions) != 200 {
		t.Fatalf("first OR page: total %d, rows %d, revision %q, has_more %v", first.Total, len(first.Sessions), first.Revision, first.HasMore)
	}
	personalORCheckCounts(t, personalORMustCounts(t, handler, personalORCloneHTTP(values)), frozenCounts, first.Revision)

	// Change membership after page one. The frozen union below must still be the
	// original reference; a delta from its revision must describe exactly these edits.
	source.rows[0].Provider = "other"      // leaves the OR set
	source.rows[1].Provider = "provider-a" // enters it
	source.rows = append(source.rows, agents.Wrap("demo", agents.AgentSession{
		SessionKey: "new", AccountID: "account-new", Provider: "provider-c", Title: "New",
		Kind: "main", CanSend: true, RankTimeMs: 10000,
	}, ""))
	h.agents.Updated("demo", nil)

	allIDs := personalORIDs(first.Sessions)
	cursor := first.NextCursor
	pages := 1
	for cursor != "" {
		nextValues := personalORCloneHTTP(values)
		nextValues["provider"] = []string{"Provider-A", "provider-a", "provider-c", "Provider-A"}
		nextValues.Set("cursor", cursor)
		page := personalORMustPage(t, handler, nextValues)
		if page.Total != len(frozenIDs) || page.Revision != first.Revision {
			t.Fatalf("frozen page %d moved: total %d, revision %q, want %d, %q", pages+1, page.Total, page.Revision, len(frozenIDs), first.Revision)
		}
		personalORCheckCounts(t, personalORMustCounts(t, handler, personalORCloneHTTP(nextValues)), frozenCounts, first.Revision)
		allIDs = append(allIDs, personalORIDs(page.Sessions)...)
		cursor = page.NextCursor
		pages++
		if pages > 24 {
			t.Fatal("OR pagination did not terminate")
		}
	}
	if pages != 24 || !reflect.DeepEqual(allIDs, frozenIDs) {
		t.Fatalf("frozen page union differs: pages %d, rows %d, want 24 pages and %d exact ordered IDs", pages, len(allIDs), len(frozenIDs))
	}

	_, currentCounts := personalORReference(source.rows, providers...)
	deltaValues := url.Values{
		"summary": {"1"}, "since": {first.Revision},
		"provider": {"Provider-A", "provider-c", "provider-a"},
	}
	delta := personalORMustPage(t, handler, deltaValues)
	// added names the rows created or newly entering the filter since the baseline (docs/protocol/protocol.md, #16389):
	// the new row, and s0001, which entered the OR set by its provider edit.
	if delta.Total != currentCounts.Total || !reflect.DeepEqual(personalORIDs(delta.Sessions), []string{"demo:new", "demo:s0001"}) ||
		!reflect.DeepEqual(delta.Remove, []string{"demo:s0000"}) || !reflect.DeepEqual(delta.Added, []string{"demo:new", "demo:s0001"}) {
		t.Fatalf("provider-edit delta = %+v", delta)
	}
	gotCurrent := personalORMustCounts(t, handler, url.Values{"provider": append([]string(nil), providers...)})
	personalORCheckCounts(t, gotCurrent, currentCounts, delta.Revision)
	if gotCurrent.Total-frozenCounts.Total != 1 {
		t.Fatalf("count delta = %d, want +1", gotCurrent.Total-frozenCounts.Total)
	}
}

func personalORCloneHTTP(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func TestPersonalAgentHTTPProviderORValidationAndCursor(t *testing.T) {
	h, handler, _ := agentsHub(t)
	h.agents.Add(&listAgentSource{rows: []agents.Session{
		agents.Wrap("demo", agents.AgentSession{SessionKey: "a", Provider: "provider-a", RankTimeMs: 3}, ""),
		agents.Wrap("demo", agents.AgentSession{SessionKey: "b", Provider: "provider-b", RankTimeMs: 2}, ""),
		agents.Wrap("demo", agents.AgentSession{SessionKey: "c", Provider: "other", RankTimeMs: 1}, ""),
	}})

	// Strict repeated-provider parsing is reached by an existing paged opt-in.
	// Provider alone remains on the legacy route, as the golden test below pins.
	tooMany := make([]string, 65)
	for n := range tooMany {
		tooMany[n] = "provider-a"
	}
	for name, values := range map[string]url.Values{
		"empty member": {"summary": {"1"}, "provider": {"provider-a", ""}},
		"more than 64": {"summary": {"1"}, "provider": tooMany},
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("GET", "/api/v1/agents/sessions?"+values.Encode(), nil)
			handler.ServeHTTP(recorder, request)
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if recorder.Code != http.StatusBadRequest || body["code"] != "invalid_query" {
				t.Fatalf("HTTP %d, body %v", recorder.Code, body)
			}
		})
	}

	firstValues := url.Values{"summary": {"1"}, "limit": {"1"}, "provider": {"provider-a", "provider-b"}}
	first := personalORMustPage(t, handler, firstValues)
	withCounts := personalORCloneHTTP(firstValues)
	withCounts.Set("with_counts", "1")
	both := personalORMustPage(t, handler, withCounts)
	wantCounts := personalORMustCounts(t, handler, personalORCloneHTTP(firstValues))
	if both.Counts == nil || !reflect.DeepEqual(*both.Counts, wantCounts) {
		t.Fatalf("inline OR counts = %+v, want standalone %+v", both.Counts, wantCounts)
	}
	for name, providers := range map[string][]string{
		"changed set": {"provider-a", "other"},
		"added value": {"provider-a", "provider-b", "other"},
		"missing set": nil,
	} {
		t.Run(name, func(t *testing.T) {
			values := personalORCloneHTTP(firstValues)
			values.Set("cursor", first.NextCursor)
			if providers == nil {
				values.Del("provider")
			} else {
				values["provider"] = providers
			}
			if code := personalORRequest(t, handler, values, nil); code != http.StatusBadRequest {
				t.Fatalf("cursor with %s: HTTP %d, want 400", name, code)
			}
		})
	}
}

func TestPersonalAgentLegacyResponseGoldens(t *testing.T) {
	h, handler, _ := agentsHub(t)
	h.agents.Add(&listAgentSource{rows: []agents.Session{
		agents.Wrap("demo", agents.AgentSession{SessionKey: "b", AccountID: "account-b", Provider: "provider-b", Title: "Beta", Kind: "main", CanSend: true, RankTimeMs: 20}, ""),
		agents.Wrap("demo", agents.AgentSession{SessionKey: "a", AccountID: "account-a", Provider: "provider-a", Title: "Alpha", Kind: "main", UnreadCount: 2, RankTimeMs: 10}, ""),
	}})
	for _, test := range []struct {
		name    string
		query   string
		fixture string
	}{
		{name: "no parameters", fixture: "agents_legacy_sessions_all.json"},
		{name: "single empty scalar", query: "provider=", fixture: "agents_legacy_sessions_all.json"},
		{name: "single exact scalar", query: "provider=provider-a", fixture: "agents_legacy_sessions_provider_a.json"},
		{name: "unpaged repetition keeps the first scalar", query: "provider=provider-a&provider=provider-b", fixture: "agents_legacy_sessions_provider_a.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want, err := os.ReadFile("testdata/" + test.fixture)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("GET", "/api/v1/agents/sessions?"+test.query, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), want) {
				t.Fatalf("legacy bytes changed: HTTP %d\n got %s\nwant %s", recorder.Code, recorder.Body, want)
			}
		})
	}
}
