package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/subscriptions"
	"github.com/nwverse/persona/server/internal/subscriptionsui"
)

// hostConnector is feedConnector whose links live on a host, for telling a subscription that may duplicate it.
type hostConnector struct{ feedConnector }

func (h *hostConnector) Manifest() connector.Manifest {
	m := h.feedConnector.Manifest()
	m.Hosts = []string{"chat.example.com"}
	return m
}

// The pages' built-in sources are the connectors' own: their switches are the ones Settings › Connectors changes,
// both ways, and each source says when the Inbox last read it and why a read failed.
func TestInboxSourcesAreTheConnectors(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&feedConnector{}); err != nil {
		t.Fatal(err)
	}
	b := builtinSources{h}
	list := connectorSources(b.List(ctx))
	if len(list) != 1 || list[0].ID != "example" || list[0].Inbox == nil || !*list[0].Inbox || list[0].Archive == nil || !*list[0].Archive ||
		list[0].InboxEvery != 120 || list[0].ArchiveEvery != 600 || !list[0].Mutable || list[0].Read != nil {
		t.Fatalf("listed: %+v", list)
	}
	if err := b.SetFeed(ctx, "example", false, false, subscriptions.Actor{}); err != nil {
		t.Fatal(err)
	}
	if c := listedExample(t, h); c["archive"] != false || c["inbox"] != true {
		t.Errorf("the pages' switch is Settings › Connectors' one: %v", c)
	}
	code, _ := call(t, h.routes(), "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"inbox":false}`)
	if l := b.List(ctx); code != 200 || *l[0].Inbox {
		t.Errorf("and the other way round: %d %+v", code, l[0])
	}
	var cs connectorSettings
	if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil || (*cs.feeds(connector.FeedArchive))["example"] {
		t.Errorf("kept for the next start: %+v %v", cs, err)
	}
	h.readConnectors(ctx)
	if b.List(ctx)[0].Read != nil {
		t.Error("a source the Inbox doesn't read has a read")
	}
	_ = b.SetFeed(ctx, "example", true, true, subscriptions.Actor{})
	h.readConnectors(ctx)
	if r := b.List(ctx)[0].Read; r == nil || r.Error != "" || r.OK == nil {
		t.Errorf("a read that worked: %+v", r)
	}

	h2 := newTestManager(t)
	h2.connectors = connector.NewRegistry(nil)
	if err := h2.addConnector(&failingInbox{err: context.DeadlineExceeded}); err != nil {
		t.Fatal(err)
	}
	h2.readConnectors(ctx)
	if r := (builtinSources{h2}).List(ctx)[0].Read; r == nil || r.Error != assistantwire.ReadTimeout || r.OK != nil {
		t.Errorf("a read that failed keeps its code: %+v", r)
	}
	if err := b.SetRules(ctx, "example", "x", subscriptions.Actor{}); err == nil {
		t.Error("a connector without rules of its own took some")
	}
}

// A service hint names the switched-on connector whose links live on its host.
func TestInboxSourcesNative(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&hostConnector{}); err != nil {
		t.Fatal(err)
	}
	b := builtinSources{h}
	if id, where := b.Native("chat:example.com"); id != "example" || where != "Example" {
		t.Errorf("native: %q %q", id, where)
	}
	if id, _ := b.Native("chat:other.example"); id != "" {
		t.Errorf("another host: %q", id)
	}
	if err := h.connectors.SetEnabled("example", false); err != nil {
		t.Fatal(err)
	}
	if id, _ := b.Native("chat:example.com"); id != "" || h.subscriptionNative("chat:example.com") != "" {
		t.Error("a switched-off connector reads nothing")
	}
}

// Mutes and trying triage are the assistant's: asked in this process, nothing kept here.
func TestInboxSourcesAskTheAssistant(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	a := withAssistant(h)
	a.on(assistantwire.MethodInboxMutes, func(raw json.RawMessage) (any, error) {
		return map[string]any{"mutes": []assistantwire.InboxMute{{ID: "m1", Source: "example", Tag: "sender", Value: "a@example.com", Name: "a@example.com"}}}, nil
	})
	a.on(assistantwire.MethodInboxUnmute, func(json.RawMessage) (any, error) { return json.RawMessage(`{}`), nil })
	a.on(assistantwire.MethodInboxTriagePreview, func(raw json.RawMessage) (any, error) {
		return map[string]any{"verdicts": []assistantwire.InboxTriageVerdict{{Tier: "fyi", Summary: "A note"}}}, nil
	})
	b := builtinSources{h}
	mutes, err := b.Mutes(ctx, "example")
	if err != nil || len(mutes) != 1 || mutes[0].Name != "a@example.com" {
		t.Fatalf("mutes: %+v %v", mutes, err)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxMutes)); got != `{"source":"example"}` {
		t.Errorf("mutes params = %s", got)
	}
	if err := b.Unmute(ctx, "m1"); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxUnmute)); got != `{"id":"m1"}` {
		t.Errorf("unmute params = %s", got)
	}
	v, err := h.triagePreview(ctx, subscriptions.TriagePreview{Source: "sub_x", Things: []connector.InboxThing{{Title: "Hello"}}})
	if err != nil || len(v) != 1 || v[0].Tier != "fyi" {
		t.Fatalf("triage preview: %+v %v", v, err)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxTriagePreview)); got != `{"source":"sub_x","name":"","things":[{"thread":"","kind":"","title":"Hello","at":"0001-01-01T00:00:00Z"}]}` {
		t.Errorf("triage preview params = %s", got)
	}
	refused := &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "the store is down"}
	a.on(assistantwire.MethodInboxMutes, func(json.RawMessage) (any, error) { return nil, refused })
	if _, err := b.Mutes(ctx, ""); !errors.Is(err, errInboxAway) || !errors.Is(err, refused) {
		t.Errorf("mutes error = %v, want inbox unavailable with the original RPC error", err)
	}
	h.opts.Assistant = nil
	if _, err := b.Mutes(ctx, ""); !errors.Is(err, errInboxAway) {
		t.Errorf("the assistant away: %v", err)
	}
}

// Through the route: the built-in source's page and its switch, and the button named after the assistant the app
// shows; the name never reaches an extension (uiParams has no field for it).
func TestInboxSourcesRoute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&feedConnector{}); err != nil {
		t.Fatal(err)
	}
	h.startSubscriptions(ctx)
	mux := http.NewServeMux()
	h.subscriptionRoutes(mux)
	post := func(body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/subscriptions/-/ui", strings.NewReader(body))
		mux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), principal{ID: "dev_phone", Role: roleFull})))
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return out
	}
	list, _ := json.Marshal(post(`{"context":{"ui_version":6,"locale":"en"}}`))
	if !strings.Contains(string(list), `"key":"b.example"`) || !strings.Contains(string(list), `"title":"Inbox Sources"`) {
		t.Fatalf("the list has the built-in source: %s", list)
	}
	post(`{"context":{"ui_version":6,"locale":"en"},"state":"{\"p\":\"builtin\",\"c\":\"example\"}","action":"builtin_dest","values":{"to":"to_kb","to_kb":"false"}}`)
	if h.connectors.Feeds("example", connector.FeedArchive) {
		t.Error("the page's switch didn't turn the connector's off")
	}
	var in uiRequest
	_ = json.Unmarshal([]byte(`{"context":{"assistant":"Claude"}}`), &in)
	if in.Context.Assistant == nil || *in.Context.Assistant != "Claude" {
		t.Fatal("the request's assistant name isn't read")
	}
	// the key at all, even "", says the app opens Set Up with … (#5661); an older app sends none
	for body, sent := range map[string]bool{`{"context":{"assistant":""}}`: true, `{"context":{}}`: false} {
		var r uiRequest
		_ = json.Unmarshal([]byte(body), &r)
		if (r.Context.Assistant != nil) != sent {
			t.Errorf("%s: sent %v", body, r.Context.Assistant != nil)
		}
	}
	slot, _ := extension.Slot("extension_page")
	params, _ := json.Marshal(h.uiParams(extension.UIItem{ID: "page"}, slot, in))
	if strings.Contains(string(params), "Claude") {
		t.Errorf("an extension is told the assistant's name: %s", params)
	}
}

// lastParams are the params of the last call of method to the fake assistant.
func lastParams(a *fakeAssistant, method string) json.RawMessage {
	got := a.got(method)
	if len(got) == 0 {
		return nil
	}
	return got[len(got)-1]
}

// The assistant's own source (#6430): a row on the pages whose Add to Inbox switch is the assistant's, asked and set in
// the assistant's process; away, it shows on (its default) and a change is refused rather than lost.
func TestInboxSourcesAssistantRow(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	a := withAssistant(h)
	on := false
	a.on(assistantwire.MethodInboxPost, func(raw json.RawMessage) (any, error) {
		var p struct {
			On *bool `json:"on"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.On != nil {
			on = *p.On
		}
		return map[string]any{"on": on}, nil
	})
	b := builtinSources{h}
	list := b.List(ctx)
	if len(list) != 1 || list[0].ID != assistantSource || list[0].Inbox == nil || *list[0].Inbox || list[0].Name == "" {
		t.Fatalf("the row: %+v", list)
	}
	if err := b.SetFeed(ctx, assistantSource, true, true, subscriptions.Actor{}); err != nil || !on || !*b.List(ctx)[0].Inbox {
		t.Fatalf("switched on: %v %v", err, on)
	}
	if err := b.SetFeed(ctx, assistantSource, true, false, subscriptions.Actor{}); err != nil || on {
		t.Fatalf("switched off: %v %v", err, on)
	}
	if got := a.got(assistantwire.MethodInboxPost); len(got) != 4 || string(got[0]) != `{}` ||
		string(got[1]) != `{"on":true}` || string(got[2]) != `{}` || string(got[3]) != `{"on":false}` {
		t.Errorf("post params = %s, want reads without on and writes with true and false", got)
	}
	if err := b.SetFeed(ctx, assistantSource, false, true, subscriptions.Actor{}); err == nil {
		t.Error("it has no knowledge-base feed")
	}
	h.opts.Assistant = nil
	if l := b.List(ctx); !*l[len(l)-1].Inbox {
		t.Error("the assistant away: the row shows its default, on")
	}
	if err := b.SetFeed(ctx, assistantSource, true, false, subscriptions.Actor{}); err == nil {
		t.Error("a change while the assistant is away is refused")
	}
}

// connectorSources is the pages' list without the assistant's own source (#6430), which is always its last row.
func connectorSources(l []subscriptionsui.Builtin) []subscriptionsui.Builtin {
	if n := len(l); n > 0 && l[n-1].ID == assistantSource {
		return l[:n-1]
	}
	return l
}
