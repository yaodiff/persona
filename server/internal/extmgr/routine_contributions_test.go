package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/modapi"
)

// The program records exactly what crossed the RPC connection. The slow fixture
// holds its answer beyond the host's collection deadline.
func serveRoutineForTest() {
	id := os.Getenv("ROUTINE_FIXTURE_ID")
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{
		ID: id, Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{"routine_facts": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "collect" {
				return nil, pserve.UnknownMethod(method)
			}
			if err := os.WriteFile(os.Getenv("ROUTINE_FIXTURE_LOG"), params, 0o600); err != nil {
				return nil, err
			}
			if id == "slow" {
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return wire.RoutineFactsResult{Facts: []string{"It will be sunny."}, Sections: []wire.RoutineReportSection{
				{Title: "Forecast", Body: "Dry through this afternoon."},
			}}, nil
		}},
	})
}

func installRoutineForTest(t *testing.T, h *Manager, id string) string {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "last-request")
	manifest := fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":"Weather","version":"1.0.0","engines":{"extension_protocol":1},
		"activation_events":["on_call"],"runtime":{"command":{%q:"weather"},"env":{"ROUTINE_FIXTURE_ID":%q,"ROUTINE_FIXTURE_LOG":%q}},
		"permissions":{"routine_facts":true},"contributes":{"routine_facts":[{"id":%q,"title":"Weather today"}]}}`,
		id, runtime.GOOS+"-"+runtime.GOARCH, id, calls, id+"-today")
	dir := declarativePackage(t, manifest)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "weather"), []byte(fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=routine exec %q\n", self)), 0o755); err != nil {
		t.Fatal(err)
	}
	installForTest(t, h, dir)
	return calls
}

func TestRoutineContributions(t *testing.T) {
	h, store := extensionTestHub(t)
	logPath := installRoutineForTest(t, h, "weather")
	collect := func(m *Manager) []modapi.RoutineContribution {
		return m.RoutineContributions(t.Context(), "2026-03-14", "ja-JP")
	}
	want := []modapi.RoutineContribution{{
		ExtensionID: "weather", ContributionID: "weather-today", Title: "Weather today",
		Facts:    []string{"It will be sunny."},
		Sections: []modapi.RoutineSection{{Title: "Forecast", Body: "Dry through this afternoon."}},
	}}
	if got := collect(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("first routine got %+v", got)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil || !reflect.DeepEqual(fields, map[string]string{
		"contribution": "weather-today", "date": "2026-03-14", "language": "ja",
	}) {
		t.Fatalf("fields delivered to provider: %s", raw)
	}
	// A fresh manager discovers this provider once from its installation and grant.
	h.extensions.get("weather").proc.Stop()
	resume := func() *Manager {
		m := newTestManager(t)
		m.cfg.dir, m.store = h.cfg.dir, store
		startTestExtensions(t, m)
		return m
	}
	restarted := resume()
	if got := collect(restarted); !reflect.DeepEqual(got, want) {
		t.Fatalf("after restart got %+v", got)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	code, body := call(t, restarted.routes(), "PUT", "/api/v1/extensions/weather/-/contributions/routine_facts/weather-today",
		restarted.cfg.Token, `{"enabled":false}`)
	if code != 200 || len(collect(restarted)) != 0 {
		t.Fatalf("switching off: %d %v", code, body)
	}
	// A legacy install can have a manifest that names the point but no grant in
	// its saved permission record. Restore it with the item on. No RPC is sent.
	store.mu.Lock()
	row := store.rows["weather"]
	row.DisabledContributions = nil
	row.Granted.RoutineFacts = false
	store.rows["weather"] = row
	store.mu.Unlock()
	if got := collect(resume()); len(got) != 0 {
		t.Fatalf("ungranted install contributed %+v", got)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("a disabled or ungranted provider received a request: %v", err)
	}
}

func TestRoutineContributionsTimeout(t *testing.T) {
	h, _ := extensionTestHub(t)
	installRoutineForTest(t, h, "weather")
	slowLog := installRoutineForTest(t, h, "slow")
	start := time.Now()
	result := h.RoutineContributions(t.Context(), "2026-03-14", "en")
	if elapsed := time.Since(start); elapsed > 4*time.Second || len(result) != 1 || result[0].ExtensionID != "weather" {
		t.Fatalf("got %+v in %v", result, elapsed)
	}
	if _, err := os.Stat(slowLog); err != nil {
		t.Fatal("the slow provider was never called:", err)
	}
}

// Recheck even early answers at the end of a collection. The provider can
// finish and then be switched off while the host waits for a slower peer.
func TestRoutineContributionsRevokedDuringRead(t *testing.T) {
	h, _ := extensionTestHub(t)
	first := installRoutineForTest(t, h, "weather")
	installRoutineForTest(t, h, "slow")
	result := make(chan []modapi.RoutineContribution, 1)
	go func() {
		result <- h.RoutineContributions(t.Context(), "2026-03-14", "en")
	}()
	waitLong(t, "the weather provider received its request", func() bool {
		_, err := os.Stat(first)
		return err == nil
	})
	if err := h.extensions.reg.SetContribution(t.Context(), "weather", "routine_facts", "weather-today", false, "test"); err != nil {
		t.Fatal(err)
	}
	if got := <-result; len(got) != 0 {
		t.Fatalf("retired contribution delivered after switch: %+v", got)
	}
}

func TestRoutineAnswerBounds(t *testing.T) {
	item := wire.RoutineFactsItem{ID: "weather", Title: "Weather"}
	for _, raw := range []string{
		strings.Repeat(" ", routineAnswerBytes) + "{}",
		`{"facts":["one","two","three","four","five"]}`,
		`{"facts":["good"],"sections":[{"title":" ","body":"text"}]}`,
		`{"sections":[{"title":"Weather","body":"a\u0000b"}]}`,
		`{"facts":["two\nlines"]}`,
		`{"facts":["` + strings.Repeat("x", 281) + `"]}`,
		`{"extension":"someone-else"}`,
	} {
		entry, err := routineAnswer("test", item, json.RawMessage(raw))
		if err == nil || len(entry.Facts) != 0 || len(entry.Sections) != 0 {
			t.Fatalf("accepted invalid %d-byte answer", len(raw))
		}
	}
}
