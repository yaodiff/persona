package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func speedPane(t *testing.T) (*Node, *remoteFixture, *codexRemote) {
	t.Helper()
	n, f, c := effortPane(t)
	joinPane(t, n)
	c.mu.Lock()
	c.models = map[string]codexModelEfforts{"example": {levels: []string{"low", "high"}, tiers: []proto.ServiceTier{{ID: "priority", Name: "Fast"}}}}
	thread := c.threads[remoteRaw]
	thread.model, thread.effort = "example", "high"
	c.threads[remoteRaw] = thread
	c.mu.Unlock()
	return n, f, c
}

func TestCodexSpeedNativeSettings(t *testing.T) {
	n, f, c := speedPane(t)
	row := proto.Session{SessionID: remoteRaw}
	n.codexSpeedRow(&row)
	if row.ServiceTier != "" || len(row.ServiceTiers) != 2 {
		t.Fatalf("unknown speed: %+v", row)
	}
	n.setCodexPending(remoteRaw, &codexPending{effort: "low", conn: c.generation, thread: remoteRaw})
	_, err := n.handle(context.Background(), "session.speed", json.RawMessage(`{"session_id":"`+remoteRaw+`","service_tier":"priority"}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := f.sent("thread/settings/update")
	if len(calls) != 1 || len(calls[0]) != 2 || calls[0]["serviceTier"] != "priority" {
		t.Fatalf("native call: %v", calls)
	}
	if thread := threadOf(c, remoteRaw); thread.serviceTier != "priority" || thread.effort != "high" || pendingOf(n, remoteRaw) != "low" {
		t.Fatalf("speed changed effort: %+v", thread)
	}
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if threadOf(c, remoteRaw).serviceTier != "priority" {
		t.Fatal("thread/read erased the speed it does not report")
	}
	if err := n.setSessionSpeed(context.Background(), remoteRaw, "default"); err != nil {
		t.Fatal(err)
	}
	if threadOf(c, remoteRaw).serviceTier != "default" {
		t.Fatal("Standard did not apply")
	}
}

func TestCodexSpeedRefusals(t *testing.T) {
	n, f, c := speedPane(t)
	if err := n.setSessionSpeed(context.Background(), remoteRaw, "invented"); err == nil || f.count("thread/settings/update") != 0 {
		t.Fatalf("unadvertised tier: %v", err)
	}
	for _, code := range []int{-32601, -32602, -32000} {
		f.mu.Lock()
		f.reject, f.rejectCode = "thread/settings/update", code
		f.mu.Unlock()
		if err := n.setSessionSpeed(context.Background(), remoteRaw, "priority"); err == nil {
			t.Fatal("native refusal became success")
		}
		if threadOf(c, remoteRaw).serviceTier != "" {
			t.Fatal("refused speed was advertised")
		}
	}
	if err := n.setSessionSpeed(context.Background(), "missing", "priority"); err == nil {
		t.Fatal("unsupported session accepted speed")
	}
}

func TestCodexSpeedAllAdvertisedTiers(t *testing.T) {
	n, f, c := speedPane(t)
	c.mu.Lock()
	model := c.models["example"]
	model.tiers = []proto.ServiceTier{
		{ID: "standard", Name: "Standard"}, {ID: "fast", Name: "Fast"},
		{ID: "ultrafast", Name: "Ultrafast"}, {ID: "flex", Name: "Flex"},
	}
	c.models["example"] = model
	c.mu.Unlock()
	for _, pick := range []struct{ id, native string }{
		{"ultrafast", "ultrafast"}, {"flex", "flex"}, {"priority", "fast"}, {"default", "standard"},
	} {
		if err := n.setSessionSpeed(context.Background(), remoteRaw, pick.id); err != nil {
			t.Fatal(err)
		}
		calls := f.sent("thread/settings/update")
		if call := calls[len(calls)-1]; len(call) != 2 || call["serviceTier"] != pick.native {
			t.Fatalf("speed %q requested as %v", pick.id, call)
		}
		row := proto.Session{SessionID: remoteRaw}
		n.codexSpeedRow(&row)
		if row.ServiceTier != pick.id || len(row.ServiceTiers) != 4 || row.ServiceTiers[1].ID != "priority" {
			t.Fatalf("speed %q row: %q %v", pick.id, row.ServiceTier, row.ServiceTiers)
		}
	}
}

func TestCodexSpeedNotifications(t *testing.T) {
	n, _, c := speedPane(t)
	for _, raw := range []string{`"priority"`, `null`, `"fast"`} {
		c.message(codexMessage{Method: "thread/settings/updated", Params: json.RawMessage(`{"threadId":"` + remoteRaw + `","threadSettings":{"model":"example","effort":"high","serviceTier":` + raw + `}}`)})
		row := proto.Session{SessionID: remoteRaw}
		n.codexSpeedRow(&row)
		if row.ServiceTier != codexServiceTier(json.RawMessage(raw)) {
			t.Fatalf("notification: %+v", row)
		}
	}
	for _, raw := range []string{"", `false`, `{}`} {
		if got := codexServiceTier(json.RawMessage(raw)); got != "" {
			t.Fatalf("invalid or absent %q: %q", raw, got)
		}
	}
	before := listKey([]proto.Session{{SessionID: "example", ServiceTier: "default"}})
	after := listKey([]proto.Session{{SessionID: "example", ServiceTier: "priority"}})
	if before == after {
		t.Fatal("speed changes do not publish")
	}
}

func TestCodexSpeedModelCatalog(t *testing.T) {
	n, f, c := speedPane(t)
	f.mu.Lock()
	f.models = map[string]any{"data": []any{map[string]any{"model": "example", "id": "alias", "serviceTiers": []any{map[string]any{"id": "priority", "name": "Fast", "description": "Increased usage"}}}}}
	f.mu.Unlock()
	c.loadModels()
	row := proto.Session{SessionID: remoteRaw}
	n.codexSpeedRow(&row)
	if len(row.ServiceTiers) != 2 || row.ServiceTiers[1].ID != "priority" || row.ServiceTiers[1].Name != "Fast" {
		t.Fatalf("catalog: %+v", row.ServiceTiers)
	}
	c.mu.Lock()
	delete(c.models, "example")
	c.mu.Unlock()
	row = proto.Session{SessionID: remoteRaw}
	n.codexSpeedRow(&row)
	if len(row.ServiceTiers) != 0 {
		t.Fatal("an unknown model advertised speed choices")
	}
}

func TestCodexSpeedTimeout(t *testing.T) {
	n, f, c := speedPane(t)
	f.mu.Lock()
	f.drop = "thread/settings/update"
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := n.setSessionSpeed(ctx, remoteRaw, "priority"); err == nil {
		t.Fatal("timeout became success")
	}
	if threadOf(c, remoteRaw).serviceTier != "" {
		t.Fatal("timeout advertised Fast")
	}
}

func TestCodexSpeedPreservesNewerNativeSettings(t *testing.T) {
	n, f, c := speedPane(t)
	f.mu.Lock()
	f.drop = "thread/settings/update"
	f.mu.Unlock()
	result := make(chan error, 1)
	go func() { result <- n.setSessionSpeed(context.Background(), remoteRaw, "priority") }()
	waitFor(t, "speed request", func() bool { return f.count("thread/settings/update") == 1 })
	// Native state may change more than once before an older request's acknowledgment arrives.
	for _, tier := range []string{"priority", "default"} {
		params, err := json.Marshal(map[string]any{"threadId": remoteRaw, "threadSettings": map[string]any{"model": "example", "serviceTier": tier}})
		if err != nil {
			t.Fatal(err)
		}
		c.message(codexMessage{Method: "thread/settings/updated", Params: params})
	}
	f.mu.Lock()
	var requestID json.RawMessage
	for _, message := range f.got {
		if message.Method == "thread/settings/update" {
			requestID = message.ID
		}
	}
	f.mu.Unlock()
	f.push(map[string]any{"id": requestID, "result": map[string]any{}})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if threadOf(c, remoteRaw).serviceTier != "default" {
		t.Fatal("an old acknowledgment erased newer native settings")
	}
}
