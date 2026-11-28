package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// notify_user over the real hub stack (#8138): the hub's HTTP and WebSocket, the assistant manager over the bus with
// its state in Postgres, a fake node for the assistant's machine. With no device that takes notifications the tool
// says the user was not alerted, and the north star does not count it as an interruption; with an app connected it
// says it was sent to that device, and that one counts.
func TestNotifyUserSaysWhetherADeviceGotItOverTheBus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	store := managerDB(t, h)
	ctx := context.Background()
	keys := []string{"assistant", "assistant.tasks", "assistant.routines", "assistant.followups", "assistant.runs"}
	clear := func() {
		for _, k := range keys {
			_ = store.PutSetting(context.Background(), k, nil)
		}
	}
	clear()
	t.Cleanup(clear)
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "main-1", Cwd: convDir, Title: "Main", UpdatedAt: time.Now().UTC()}}
	})
	n.known["main-1"] = true
	if err := store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main-1", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	dialAssistant(t, h, srv.URL, false)

	do := func(path string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	type reading struct {
		notify int
		missed map[string]int
	}
	// The day's readout (shared database: the test reads what it added, not the totals).
	northStar := func() reading {
		t.Helper()
		code, body := do("/api/v1/assistant/north-star?tz=UTC")
		if code != http.StatusOK {
			t.Fatalf("north-star: %d %s", code, body)
		}
		var ns assistant.NorthStar
		if err := json.Unmarshal(body, &ns); err != nil {
			t.Fatal(err)
		}
		r := reading{missed: ns.Interruptions.Excluded.NotifyMissed}
		if c := ns.Interruptions.Categories["notify_user"]; c != nil {
			r.notify = c.Count
		}
		return r
	}
	notify := func(title string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"title": title, "body": "The report is ready."})
		params, _ := json.Marshal(map[string]any{"name": "notify_user", "args": json.RawMessage(raw), "session_id": "main-1", "assistant": true})
		res, err := nodeAsks(ctx, n.m, "mcp.call", params)
		if err != nil {
			t.Fatal(err)
		}
		r := res.(proto.ToolResult)
		if r.IsError {
			t.Fatalf("notify_user: %+v", r)
		}
		return textOf(r)
	}
	inAnyLanguage := func(key string, args ...i18n.Args) []string {
		var out []string
		for _, l := range []i18n.Lang{"en", "zh-Hans", "ja"} {
			out = append(out, i18n.T(l, key, args...))
		}
		return out
	}

	before := northStar()

	// No phone, computer or browser: not "Notification sent.", and the model is told the user was not alerted.
	got := notify("Nobody gets this")
	if want := inAnyLanguage("assistant-notify.no_device_notice"); !slices.Contains(want, got) {
		t.Fatalf("with no device notify_user said %q, want one of %q", got, want)
	}

	// An app that takes notify events: it gets it, and the tool says one device did.
	app := dialTestApp(t, h, srv)
	app.wait(t, "the client hello acknowledgment", func(m map[string]any) bool {
		return m["type"] == "client_hello_ack"
	})
	got = notify("The app gets this")
	if want := inAnyLanguage("assistant-notify.sent_notice", i18n.Args{"count": 1}); !slices.Contains(want, got) {
		t.Fatalf("with an app notify_user said %q, want one of %q", got, want)
	}
	app.wait(t, "the push", func(m map[string]any) bool {
		return m["type"] == "notify" && m["title"] == "The app gets this"
	})

	// The north star counts only the one a device took; the other is listed apart, as no device.
	var after reading
	eventually(t, 10*time.Second, "the north star reading both", func() bool {
		after = northStar()
		return after.notify-before.notify >= 1 && after.missed["no_device"]-before.missed["no_device"] >= 1
	})
	if d := after.notify - before.notify; d != 1 {
		t.Fatalf("interruptions from notify_user grew by %d, want 1 (only the delivered one)", d)
	}
	if d := after.missed["no_device"] - before.missed["no_device"]; d != 1 {
		t.Fatalf("notify_missed no_device grew by %d, want 1 (%v → %v)", d, before.missed, after.missed)
	}
}
