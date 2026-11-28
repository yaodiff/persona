package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// "Start new Agents as Orca chats" (#17650): off until the user turns it on, kept in settings under
// machine_orca_chat_starts, on the machine (orca_chat_starts) only while Orca is in its engine set, changed from a full
// device only, and reported to the assistant only where it applies.
func TestOrcaChatStartsSwitch(t *testing.T) {
	h, st, m, engineMux := engineHub(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/machines/{mid}/orca-chat-starts", h.withMachine(h.getOrcaChatStarts))
	mux.HandleFunc("PUT /api/v1/machines/{mid}/orca-chat-starts", h.withMachine(h.putOrcaChatStarts))
	mux.Handle("/", engineMux)
	ctx := context.Background()
	send := func(method, body, role string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/machines/m1/orca-chat-starts", strings.NewReader(body))
		if role != "" {
			req.Header.Set(assistantwire.RoleHeader, role)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Never set, on a computer without Orca: off, not applying, not on the machine.
	if code, out := send("GET", "", ""); code != 200 || out["on"] != false || out["applies"] != false {
		t.Fatalf("default: %d %v", code, out)
	}
	if m.snapshot().OrcaChatStarts != "" || h.OrcaChatStarts(ctx, "m1") {
		t.Fatalf("shown without Orca: %v", m.snapshot().OrcaChatStarts)
	}
	b, _ := json.Marshal(m.snapshot())
	if strings.Contains(string(b), "orca_chat_starts") {
		t.Fatalf("the wire names it without Orca: %s", b)
	}

	// Orca in the set: shown, off.
	if code, out := put(t, mux, "/api/v1/machines/m1/engine", `{"hosts":["herdr","orca"],"default":"herdr"}`); code != 200 || out["orca_chat_starts"] != "off" {
		t.Fatalf("with Orca: %d %v", code, out)
	}
	if got := m.snapshot().OrcaChatStarts; got != proto.OrcaChatStartsOff {
		t.Fatalf("with Orca, never set: %q", got)
	}

	// Bad bodies and devices that may not change it.
	if code, _ := send("PUT", `{}`, ""); code != http.StatusBadRequest {
		t.Fatalf("no on: %d", code)
	}
	if code, out := send("PUT", `{"on":true}`, "answer"); code != http.StatusForbidden || out["code"] != "read_only" {
		t.Fatalf("answer-only device: %d %v", code, out)
	}
	if h.OrcaChatStarts(ctx, "m1") {
		t.Fatal("turned on by a device that may not")
	}

	// On: stored, on the machine, read by the assistant.
	if code, out := send("PUT", `{"on":true}`, "full"); code != 200 || out["on"] != true || out["applies"] != true {
		t.Fatalf("on: %d %v", code, out)
	}
	var stored map[string]bool
	if ok, err := st.Setting(ctx, orcaChatStartsKey, &stored); !ok || err != nil || !stored["m1"] {
		t.Fatalf("stored %v %v %v", ok, err, stored)
	}
	if got := m.snapshot().OrcaChatStarts; got != proto.OrcaChatStartsOn || !h.OrcaChatStarts(ctx, "m1") {
		t.Fatalf("after on: %q", got)
	}
	// Another manager process on the same settings reads it back.
	h2 := newTestDevices(t)
	h2.opts.Store = st
	if !h2.orcaChatStartsOn("m1") {
		t.Fatal("not read back after a restart")
	}

	// Orca out of the set: kept, but neither shown nor applied; back in, it applies again.
	if code, out := put(t, mux, "/api/v1/machines/m1/engine", `{"hosts":["herdr"]}`); code != 200 || out["orca_chat_starts"] != nil {
		t.Fatalf("without Orca: %d %v", code, out)
	}
	if code, out := send("GET", "", ""); code != 200 || out["on"] != true || out["applies"] != false || h.OrcaChatStarts(ctx, "m1") {
		t.Fatalf("kept while Orca is off: %d %v", code, out)
	}
	if code, out := put(t, mux, "/api/v1/machines/m1/engine", `{"hosts":["orca"]}`); code != 200 || out["orca_chat_starts"] != "on" {
		t.Fatalf("Orca back: %d %v", code, out)
	}

	// Off again: removed from the stored set, not applied.
	if code, out := send("PUT", `{"on":false}`, ""); code != 200 || out["on"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	stored = nil
	if ok, _ := st.Setting(ctx, orcaChatStartsKey, &stored); !ok || stored["m1"] {
		t.Fatalf("stored after off %v", stored)
	}
	if got := m.snapshot().OrcaChatStarts; got != proto.OrcaChatStartsOff || h.OrcaChatStarts(ctx, "m1") {
		t.Fatalf("after off: %q", got)
	}
	if h.OrcaChatStarts(ctx, "nope") {
		t.Fatal("an unknown machine")
	}
}

// The routes are the manager's own (the hub relays them).
func TestOrcaChatStartsRoutesAreRelayed(t *testing.T) {
	for _, r := range []string{"GET /api/v1/machines/{mid}/orca-chat-starts", "PUT /api/v1/machines/{mid}/orca-chat-starts"} {
		if !slices.Contains(devicewire.Routes, r) {
			t.Errorf("%s is not in devicewire.Routes", r)
		}
	}
	h := newTestDevices(t)
	herdrMac(t, h, "mac", false)
	if code, out := call(t, h.Handler(http.NotFoundHandler()), "GET", "/api/v1/machines/mac/orca-chat-starts", ""); code != 200 || out["on"] != false {
		t.Fatalf("served: %d %v", code, out)
	}
}
