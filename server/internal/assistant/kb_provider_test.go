package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/knowledgeuihost"
	"github.com/nwverse/persona/server/internal/rpc"
)

type kbImportProviderReadFixture struct {
	knowledgeuihost.Layer
	calls        int
	binding      assistantwire.KBImportProvider
	onCheckpoint func()
	day          func(assistantwire.KBDayParams) assistantwire.KBDayResult
}

func (f *kbImportProviderReadFixture) ProviderCheckpoint(_ *kb.Engine, _ *assistantwire.KBHistory, binding assistantwire.KBImportProvider, _ string, _ time.Time) (wire.KBImportCheckpointResult, bool) {
	f.calls++
	if f.onCheckpoint != nil {
		f.onCheckpoint()
	}
	return wire.KBImportCheckpointResult{State: "running", Approval: "approved", Read: 10, Written: 7}, binding == f.binding
}

func (f *kbImportProviderReadFixture) Day(_ context.Context, _ *kb.Engine, p assistantwire.KBDayParams) (assistantwire.KBDayResult, error) {
	return f.day(p), nil
}

func requireKBProviderRPCError(t *testing.T, result any, err error, code, message string) {
	t.Helper()
	var re *rpc.Error
	if result != nil || !errors.As(err, &re) || re.Code != code || re.Message != message {
		t.Fatalf("got (%+v, %v), want nil result and %s: %s", result, err, code, message)
	}
}

func TestKBProviderRPCStrictParams(t *testing.T) {
	s := &Service{}
	for _, method := range []string{assistantwire.MethodKBProviderCheckpoint, assistantwire.MethodKBProviderSentMarkers} {
		for _, tc := range []struct {
			name, raw, message string
		}{
			{"non-object", `null`, "expected an object of at most 1 MiB"},
			{"unknown query field", `{"params":{"unknown":true}}`, `json: unknown field "unknown"`},
			{"trailing object", `{} {}`, "expected one object"},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				result, handled, err := s.kbRPCRequest(t.Context(), method, json.RawMessage(tc.raw))
				if !handled {
					t.Fatalf("unhandled %s", method)
				}
				requireKBProviderRPCError(t, result, err, "invalid_params", method+": "+tc.message)
			})
		}
	}
}

func TestKBProviderReadRPC(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), nil)
	b := assistantwire.KBImportProvider{Extension: "knowledge-provider", StorageID: "old-install", InstalledAt: time.Date(2026, 11, 1, 12, 1, 2, 123000, time.UTC), Connector: "example", SourceID: "account-chat", FiledAs: "archive"}
	enabled := func() *KBSettings {
		return &KBSettings{ImportProvider: &b, History: &assistantwire.KBHistory{Enabled: true, Sources: []assistantwire.KBHistorySource{{Name: b.Connector.String()}}}}
	}
	set := func(cfg *KBSettings) { f.mu.Lock(); f.hello.KB = cfg; f.mu.Unlock() }
	set(enabled())
	kbEng(t, s)
	layer := &kbImportProviderReadFixture{Layer: s.ui, binding: b}
	s.ui = layer
	caller := assistantwire.KBProviderRequest{Extension: b.Extension, StorageID: b.StorageID, InstalledAt: b.InstalledAt.Format(time.RFC3339Nano)}
	call := func(t *testing.T, method string, who assistantwire.KBProviderRequest, params string) (any, error) {
		t.Helper()
		if err := json.Unmarshal([]byte(params), &who.Params); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(who)
		if err != nil {
			t.Fatal(err)
		}
		result, handled, err := s.kbRPCRequest(ctx, method, raw)
		if !handled {
			t.Fatalf("unhandled %s", method)
		}
		return result, err
	}
	deny := func(t *testing.T, method string, who assistantwire.KBProviderRequest, params string) {
		t.Helper()
		out, err := call(t, method, who, params)
		requireKBProviderRPCError(t, out, err, "permission_denied", "knowledge import selection unavailable")
	}
	checkpoint := assistantwire.MethodKBProviderCheckpoint
	markers := assistantwire.MethodKBProviderSentMarkers
	for _, tc := range []struct {
		name string
		edit func(*assistantwire.KBProviderRequest)
	}{
		{"another extension", func(r *assistantwire.KBProviderRequest) { r.Extension = "unselected" }},
		{"different storage", func(r *assistantwire.KBProviderRequest) { r.StorageID = "other" }},
		{"new installation", func(r *assistantwire.KBProviderRequest) {
			r.InstalledAt = b.InstalledAt.Add(time.Microsecond).Format(time.RFC3339Nano)
		}},
		{"invalid installation time", func(r *assistantwire.KBProviderRequest) { r.InstalledAt = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := caller
			tc.edit(&other)
			deny(t, checkpoint, other, "{}")
			deny(t, markers, other, "{}")
		})
	}
	for _, cfg := range []*KBSettings{{Off: true, ImportProvider: &b, History: enabled().History}, {ImportProvider: &b}, {History: enabled().History}} {
		set(cfg)
		deny(t, checkpoint, caller, "{}")
	}
	set(enabled())
	for _, tc := range []struct {
		method, params, message string
	}{
		{checkpoint, `{"day":"2026-11-01"}`, "expected an empty checkpoint query"},
		{markers, `{"approval":"approved","day":"2026-11-32"}`, "invalid day, approval or starting line"},
		{markers, `{"day":"2026-11-01"}`, "invalid day, approval or starting line"},
		{markers, `{"approval":"approved","day":"2026-11-01","after":-1}`, "invalid day, approval or starting line"},
	} {
		out, err := call(t, tc.method, caller, tc.params)
		requireKBProviderRPCError(t, out, err, "invalid_params", tc.method+": "+tc.message)
	}
	if layer.calls != 0 {
		t.Fatal("lookup before binding and query checks")
	}
	result, err := call(t, checkpoint, caller, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(wire.KBImportCheckpointResult); !ok {
		t.Fatalf("checkpoint result type %T", result)
	}
	got, err := json.Marshal(result)
	if err != nil || string(got) != `{"state":"running","approval":"approved","read":10,"written":7}` {
		t.Fatalf("checkpoint payload %s (%v)", got, err)
	}
	layer.onCheckpoint = func() { set(&KBSettings{History: enabled().History}) }
	deny(t, checkpoint, caller, "{}")
	layer.onCheckpoint = nil
	set(enabled())

	const query = `{"approval":"approved","day":"2026-11-01"}`
	deny(t, markers, caller, `{"approval":"superseded","day":"2026-11-01"}`)
	layer.day = func(assistantwire.KBDayParams) assistantwire.KBDayResult {
		return assistantwire.KBDayResult{SentOrigin: "unavailable"}
	}
	out, err := call(t, markers, caller, query)
	requireKBProviderRPCError(t, out, err, "unavailable", "sent markers unavailable")
	archive := func(q assistantwire.KBDayParams) assistantwire.KBDayResult {
		if q.ID != kb.ChatID(b.FiledAs, string(b.SourceID)) || q.Day != "2026-11-01" {
			t.Fatalf("archive lookup %+v", q)
		}
		return assistantwire.KBDayResult{SentOrigin: "available", Messages: []kb.DayMessage{{N: 2, Text: "private other person"}, {N: 3, SentBy: "assistant", SentLedgerID: 99, Text: "private archived text", RecalledAt: &b.InstalledAt}}}
	}
	layer.day = archive
	result, err = call(t, markers, caller, query)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(wire.KBSentMarkersResult); !ok {
		t.Fatalf("sent markers result type %T", result)
	}
	got, err = json.Marshal(result)
	if err != nil || string(got) != fmt.Sprintf(`{"markers":[{"n":3,"sent_by":"assistant","recalled_at":%q}]}`, caller.InstalledAt) {
		t.Fatalf("marker payload %s (%v)", got, err)
	}
	layer.day = func(q assistantwire.KBDayParams) assistantwire.KBDayResult {
		other := b
		other.SourceID = "another-account-chat"
		cfg := enabled()
		cfg.ImportProvider = &other
		set(cfg)
		return archive(q)
	}
	deny(t, markers, caller, query)
}

func TestKBProviderMarkerPagination(t *testing.T) {
	var rows []kb.DayMessage
	for n := 1; n <= 203; n++ {
		line := kb.DayMessage{N: n, Text: "archived data", SentLedgerID: 41}
		if n%2 == 0 {
			line.SentBy = "assistant"
		}
		rows = append(rows, line)
	}
	first := kbMarkerPage(rows, 0)
	if len(first.Markers) != 100 || first.NextAfter != 200 || first.Markers[0].N != 2 || first.Markers[99].N != 200 {
		t.Fatalf("first page %+v", first)
	}
	last := kbMarkerPage(rows, first.NextAfter)
	got, err := json.Marshal(last)
	if err != nil || string(got) != `{"markers":[{"n":202,"sent_by":"assistant"}]}` {
		t.Fatalf("last page %s (%v)", got, err)
	}
	exact := kbMarkerPage(rows[:201], 0)
	if len(exact.Markers) != 100 || exact.NextAfter != 0 {
		t.Fatal("an exactly full final page claimed a successor")
	}
	empty, _ := json.Marshal(kbMarkerPage(rows, 202))
	if string(empty) != `{"markers":[]}` {
		t.Fatalf("empty page %s", empty)
	}
}
